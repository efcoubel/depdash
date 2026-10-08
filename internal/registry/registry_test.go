package registry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/efcoubel/depdash/internal/registry"
	"github.com/efcoubel/depdash/internal/registry/endoflife"
	"github.com/efcoubel/depdash/internal/registry/npm"
	"github.com/efcoubel/depdash/internal/registry/packagist"
)

func client() *registry.Client {
	c := registry.NewClient()
	c.Backoff = time.Millisecond
	return c
}

func serve(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath conserve le %2F des paquets npm à scope.
		body, ok := routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPick(t *testing.T) {
	versions := []string{"v7.1.3", "7.0.9", "dev-main", "7.2.x-dev", "8.0.0-RC1", "7.1.0-beta2", "1.2.3.4", "n'importe quoi"}
	rel := registry.Pick(versions, "")
	if rel.Latest != "7.1.3" {
		t.Errorf("dernière stable = %q", rel.Latest)
	}
	if !reflect.DeepEqual(rel.Prereleases, []string{"8.0.0-RC1"}) {
		t.Errorf("préversions = %v", rel.Prereleases)
	}
	// Le « latest » déclaré par le registre prime sur le maximum calculé.
	if rel := registry.Pick([]string{"4.9.0", "5.0.0"}, "4.9.0"); rel.Latest != "4.9.0" {
		t.Errorf("version préférée ignorée : %q", rel.Latest)
	}
	// … sauf s'il désigne une préversion.
	if rel := registry.Pick([]string{"4.9.0", "5.0.0-beta.1"}, "5.0.0-beta.1"); rel.Latest != "4.9.0" {
		t.Errorf("préversion retenue comme stable : %q", rel.Latest)
	}
	if rel := registry.Pick(nil, ""); rel.Latest != "" || rel.Prereleases != nil {
		t.Errorf("liste vide : %+v", rel)
	}
}

func TestBound(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		bound   registry.Bound
		passed  bool
		hasDate bool
	}{
		{"2026-01-01", true, true},
		{"2026-10-08", true, true},
		{"2027-01-01", false, true},
		{"true", true, false},
		{"false", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		if _, ok := tt.bound.Date(); ok != tt.hasDate || tt.bound.Passed(now) != tt.passed {
			t.Errorf("borne %q", tt.bound)
		}
	}
}

func TestPackagist(t *testing.T) {
	srv := serve(t, map[string]string{
		"/p2/symfony/console.json": `{"minified": "composer/2.0", "packages": {"symfony/console": [
			{"name": "symfony/console", "version": "v8.0.0-RC1", "source": {"type": "git", "url": "https://github.com/symfony/console.git"}},
			{"version": "v7.1.3"}, {"version": "v7.1.2"}, {"version": "v6.4.10"}, {"version": "dev-main"}
		]}}`,
		"/p2/acme/empty.json": `{"packages": {"acme/empty": []}}`,
	})
	reg := packagist.New(client(), srv.URL)
	if reg.Ecosystem() != "composer" {
		t.Errorf("écosystème = %q", reg.Ecosystem())
	}

	rel, err := reg.Latest(context.Background(), "Symfony/Console")
	if err != nil {
		t.Fatal(err)
	}
	want := registry.Release{Latest: "7.1.3", Prereleases: []string{"8.0.0-RC1"}, URL: "https://github.com/symfony/console"}
	if !reflect.DeepEqual(rel, want) {
		t.Errorf("release = %+v", rel)
	}

	for _, name := range []string{"acme/unknown", "acme/empty", "pas-un-paquet", "../../etc/passwd"} {
		if _, err := reg.Latest(context.Background(), name); !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%s : %v, ErrNotFound attendue", name, err)
		}
	}
}

func TestNPM(t *testing.T) {
	var accept atomic.Value
	routes := map[string]string{
		"/react": `{"name": "react", "dist-tags": {"latest": "19.3.0", "next": "19.4.0-canary.1"},
			"versions": {"18.3.1": {}, "19.3.0": {}, "19.4.0-canary.1": {}, "19.0.0-rc.1": {}}}`,
		"/@tanstack%2Freact-query": `{"dist-tags": {"latest": "5.51.11"}, "versions": {"5.51.11": {}}}`,
		"/unpublished":             `{"name": "unpublished", "versions": {}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept.Store(r.Header.Get("Accept"))
		body, ok := routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	reg := npm.New(client(), srv.URL)

	rel, err := reg.Latest(context.Background(), "react")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Latest != "19.3.0" || !reflect.DeepEqual(rel.Prereleases, []string{"19.4.0-canary.1"}) {
		t.Errorf("release = %+v", rel)
	}
	if got := accept.Load(); got != "application/vnd.npm.install-v1+json" {
		t.Errorf("Accept = %q : le format abrégé n'est pas demandé", got)
	}

	if rel, err := reg.Latest(context.Background(), "@tanstack/react-query"); err != nil || rel.Latest != "5.51.11" {
		t.Errorf("paquet à scope : %+v, %v", rel, err)
	}
	for _, name := range []string{"missing", "unpublished", "../secret", "a b"} {
		if _, err := reg.Latest(context.Background(), name); !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%s : %v, ErrNotFound attendue", name, err)
		}
	}
}

func TestEndOfLife(t *testing.T) {
	srv := serve(t, map[string]string{
		"/api/nodejs.json": `[
			{"cycle": "26", "releaseDate": "2026-05-05", "lts": "2099-10-28", "eol": "2029-04-30", "latest": "26.11.1", "support": "2027-10-27"},
			{"cycle": "24", "releaseDate": "2025-05-06", "lts": "2025-10-28", "eol": "2028-04-30", "latest": "24.9.0", "support": "2026-10-20"},
			{"cycle": 22, "releaseDate": "2024-04-24", "lts": true, "eol": true, "latest": "22.20.0", "support": false}
		]`,
		"/api/react.json": `[{"cycle": "19", "releaseDate": "2024-12-05", "eol": false, "latest": "19.3.0", "lts": false, "support": true}]`,
	})
	eol := endoflife.New(client(), srv.URL)

	cycles, err := eol.Cycles(context.Background(), "nodejs")
	if err != nil {
		t.Fatal(err)
	}
	want := []registry.Cycle{
		// lts dans le futur : pas encore LTS.
		{Cycle: "26", Latest: "26.11.1", ReleaseDate: "2026-05-05", EOLActive: "2027-10-27", EOLSecurity: "2029-04-30"},
		{Cycle: "24", Latest: "24.9.0", LTS: true, ReleaseDate: "2025-05-06", EOLActive: "2026-10-20", EOLSecurity: "2028-04-30"},
		// support: false signifie « support actif terminé ».
		{Cycle: "22", Latest: "22.20.0", LTS: true, ReleaseDate: "2024-04-24", EOLActive: "true", EOLSecurity: "true"},
	}
	if !reflect.DeepEqual(cycles, want) {
		t.Errorf("cycles =\n%+v\nattendu\n%+v", cycles, want)
	}

	cycles, err = eol.Cycles(context.Background(), "react")
	if err != nil {
		t.Fatal(err)
	}
	// support: true signifie « toujours supporté » : aucune borne atteinte.
	if c := cycles[0]; c.EOLActive != "false" || c.EOLSecurity != "false" || c.LTS {
		t.Errorf("react = %+v", c)
	}

	for _, product := range []string{"inconnu", "../php"} {
		if _, err := eol.Cycles(context.Background(), product); !errors.Is(err, registry.ErrNotFound) {
			t.Errorf("%s : %v, ErrNotFound attendue", product, err)
		}
	}
}

func TestClientRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flaky":
			if calls.Add(1) == 1 {
				http.Error(w, "indisponible", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"ok": true}`)
		case "/down":
			calls.Add(1)
			http.Error(w, "indisponible", http.StatusBadGateway)
		case "/forbidden":
			calls.Add(1)
			http.Error(w, "interdit", http.StatusForbidden)
		case "/garbage":
			fmt.Fprint(w, `<html>`)
		}
	}))
	defer srv.Close()
	c := client()
	var out struct{ OK bool }

	if err := c.GetJSON(context.Background(), srv.URL+"/flaky", nil, &out); err != nil || !out.OK || calls.Load() != 2 {
		t.Errorf("seconde tentative : err=%v, appels=%d", err, calls.Load())
	}
	calls.Store(0)
	if err := c.GetJSON(context.Background(), srv.URL+"/down", nil, &out); err == nil || calls.Load() != 2 {
		t.Errorf("2 tentatives attendues : err=%v, appels=%d", err, calls.Load())
	}
	calls.Store(0)
	if err := c.GetJSON(context.Background(), srv.URL+"/forbidden", nil, &out); err == nil || calls.Load() != 1 {
		t.Errorf("une erreur 4xx n'est pas retentée : err=%v, appels=%d", err, calls.Load())
	}
	if err := c.GetJSON(context.Background(), srv.URL+"/garbage", nil, &out); err == nil {
		t.Error("réponse illisible : erreur attendue")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.GetJSON(ctx, srv.URL+"/flaky", nil, &out); !errors.Is(err, context.Canceled) {
		t.Errorf("contexte annulé : %v", err)
	}
}

func TestClientLimitsConcurrency(t *testing.T) {
	var current, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := current.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		current.Add(-1)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	c := client()
	done := make(chan struct{})
	for range 30 {
		go func() {
			var out struct{}
			c.GetJSON(context.Background(), srv.URL, nil, &out)
			done <- struct{}{}
		}()
	}
	for range 30 {
		<-done
	}
	if peak.Load() > 8 {
		t.Errorf("%d requêtes simultanées, 8 au plus attendues", peak.Load())
	}
}
