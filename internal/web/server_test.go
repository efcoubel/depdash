package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/efcoubel/depdash/internal/config"
	"github.com/efcoubel/depdash/internal/enrich"
	composerparser "github.com/efcoubel/depdash/internal/parser/composer"
	npmparser "github.com/efcoubel/depdash/internal/parser/npm"
	"github.com/efcoubel/depdash/internal/registry"
	"github.com/efcoubel/depdash/internal/registry/endoflife"
	npmregistry "github.com/efcoubel/depdash/internal/registry/npm"
	"github.com/efcoubel/depdash/internal/registry/packagist"
	"github.com/efcoubel/depdash/internal/scanner"
	"github.com/efcoubel/depdash/internal/scheduler"
	"github.com/efcoubel/depdash/internal/store"
	"github.com/efcoubel/depdash/internal/web"
)

// Ces tests font tourner toute la chaîne — scan, parsing, enrichissement,
// base, rendu — sur les fixtures, avec des registres simulés.

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

type env struct {
	t        *testing.T
	projects string
	sched    *scheduler.Scheduler
	enricher *enrich.Enricher
	store    *store.Store
	handler  http.Handler
	hits     *atomic.Int32 // requêtes reçues par les registres simulés
	down     *atomic.Bool  // simule une panne des registres
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func packagistBody(name string, versions ...string) string {
	entries := make([]string, len(versions))
	for i, v := range versions {
		entries[i] = fmt.Sprintf(`{"version": %q}`, v)
	}
	return fmt.Sprintf(`{"packages": {%q: [%s]}}`, name, strings.Join(entries, ","))
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, projects: t.TempDir(), hits: &atomic.Int32{}, down: &atomic.Bool{}}

	// Un monorepo : back Symfony + front Next.js sous le même .git.
	copyDir(t, "../../testdata/composer/symfony", filepath.Join(e.projects, "shop", "back"))
	copyDir(t, "../../testdata/npm/react-app", filepath.Join(e.projects, "shop", "front"))
	if err := os.MkdirAll(filepath.Join(e.projects, "shop", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	routes := map[string]string{
		"/p2/symfony/framework-bundle.json": packagistBody("symfony/framework-bundle", "v8.1.8", "v7.4.20", "v7.1.3"),
		"/p2/symfony/console.json":          packagistBody("symfony/console", "v7.1.9", "v7.1.3"),
		"/p2/twig/twig.json":                packagistBody("twig/twig", "v3.14.0", "v3.10.3"),
		"/p2/doctrine/orm.json":             packagistBody("doctrine/orm", "3.2.1"),
		"/p2/phpunit/phpunit.json":          packagistBody("phpunit/phpunit", "12.0.1", "11.2.8"),
		"/p2/symfony/maker-bundle.json":     packagistBody("symfony/maker-bundle", "v1.60.0"),
		"/p2/doctrine/dbal.json":            packagistBody("doctrine/dbal", "4.0.4"),
		"/next":                             `{"dist-tags": {"latest": "15.1.0"}, "versions": {"15.1.0": {}, "14.2.5": {}}}`,
		"/react":                            `{"dist-tags": {"latest": "19.3.0"}, "versions": {"19.3.0": {}, "18.3.1": {}}}`,
		"/react-dom":                        `{"dist-tags": {"latest": "19.3.0"}, "versions": {"19.3.0": {}}}`,
		"/typescript":                       `{"dist-tags": {"latest": "5.5.4"}, "versions": {"5.5.4": {}}}`,
		"/api/php.json": `[
			{"cycle": "8.5", "releaseDate": "2025-11-20", "eol": "2029-12-31", "latest": "8.5.11", "lts": false, "support": "2027-12-31"},
			{"cycle": "8.2", "releaseDate": "2022-12-08", "eol": "2026-12-31", "latest": "8.2.31", "lts": false, "support": "2024-12-31"}]`,
		"/api/symfony.json": `[
			{"cycle": "8.1", "releaseDate": "2026-05-29", "eol": "2027-07-31", "latest": "8.1.8", "lts": false, "support": "2027-07-31"},
			{"cycle": "7.4", "releaseDate": "2025-11-27", "eol": "2029-11-30", "latest": "7.4.20", "lts": true, "support": "2028-11-30"},
			{"cycle": "7.1", "releaseDate": "2024-05-31", "eol": "2025-01-31", "latest": "7.1.11", "lts": false, "support": "2025-01-31"}]`,
		"/api/nodejs.json": `[
			{"cycle": "26", "releaseDate": "2026-05-05", "eol": "2029-04-30", "latest": "26.11.1", "lts": false, "support": "2027-10-27"},
			{"cycle": "20", "releaseDate": "2023-04-18", "eol": "2026-04-30", "latest": "20.20.0", "lts": true, "support": "2024-10-22"}]`,
		"/api/nextjs.json": `[
			{"cycle": "15", "releaseDate": "2024-10-21", "eol": false, "latest": "15.1.0", "lts": false, "support": true},
			{"cycle": "14", "releaseDate": "2023-10-26", "eol": "2027-10-26", "latest": "14.2.30", "lts": false, "support": "2024-10-21"}]`,
	}
	registries := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		if e.down.Load() {
			http.Error(w, "panne", http.StatusServiceUnavailable)
			return
		}
		body, ok := routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(registries.Close)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.store = st

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := registry.NewClient()
	client.Backoff = time.Millisecond
	mapping := config.DefaultMapping()
	e.enricher = enrich.New(st, endoflife.New(client, registries.URL), mapping, log,
		packagist.New(client, registries.URL), npmregistry.New(client, registries.URL))
	e.enricher.Now = func() time.Time { return now }
	e.sched = &scheduler.Scheduler{
		Scanner:  scanner.New(e.projects, 4, config.DefaultIgnore, composerparser.New(), npmparser.New()),
		Store:    st,
		Enricher: e.enricher,
		Mapping:  mapping,
		Log:      log,
	}
	server := &web.Server{
		Store: st, Scheduler: e.sched, Mapping: mapping, EOLWarning: 180 * 24 * time.Hour, Log: log,
		Now: func() time.Time { return now },
	}
	e.handler = server.Handler()
	return e
}

func (e *env) scan() {
	e.t.Helper()
	if err := e.sched.ScanAll(context.Background(), false); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) request(method, target string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *env) get(target string) string {
	e.t.Helper()
	rec := e.request(http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET %s : HTTP %d\n%s", target, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func (e *env) projectID(path, ecosystem string) int64 {
	e.t.Helper()
	p, err := e.store.ProjectByKey(context.Background(), path, ecosystem)
	if err != nil {
		e.t.Fatalf("projet %s (%s) : %v", path, ecosystem, err)
	}
	return p.ID
}

func contains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("la page ne contient pas %q", want)
		}
	}
}

type apiProject struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
	Status    string `json:"status"`
	ScanError string `json:"scan_error"`
	Runtime   *struct {
		Version, Source, Status, Cycle, Latest string
		EOLSecurity                            string `json:"eol_security"`
		Manual                                 bool
	} `json:"runtime"`
	Framework *struct {
		Name, Version, Status, Latest string
		LatestLTS                     string `json:"latest_lts"`
		Manual                        bool
	} `json:"framework"`
	Counts       map[string]int `json:"counts"`
	Dependencies []struct {
		Name, Installed, Constraint, Latest, Status string
		Dev, Manual, Stale                          bool
	} `json:"dependencies"`
}

func (e *env) api() map[string]apiProject {
	e.t.Helper()
	var out struct {
		Projects []apiProject `json:"projects"`
	}
	if err := json.Unmarshal([]byte(e.get("/api/projects")), &out); err != nil {
		e.t.Fatal(err)
	}
	byName := map[string]apiProject{}
	for _, p := range out.Projects {
		byName[p.Name] = p
	}
	return byName
}

func (p apiProject) dep(t *testing.T, name string) (status, latest string) {
	t.Helper()
	for _, d := range p.Dependencies {
		if d.Name == name {
			return d.Status, d.Latest
		}
	}
	t.Fatalf("dépendance %s absente de l'export", name)
	return "", ""
}

func TestScanAndExport(t *testing.T) {
	e := setup(t)
	e.scan()
	projects := e.api()
	if len(projects) != 2 {
		t.Fatalf("%d projets, 2 attendus : %v", len(projects), projects)
	}

	back := projects["shop / back"]
	if back.Ecosystem != "composer" || back.Runtime == nil || back.Framework == nil {
		t.Fatalf("back = %+v", back)
	}
	// PHP 8.2 : fin du support sécurité le 31/12/2026, moins de 6 mois.
	if r := back.Runtime; r.Version != "8.2.20" || r.Status != "eol-soon" || r.Cycle != "8.2" ||
		r.Latest != "8.5.11" || r.EOLSecurity != "31/12/2026" || !strings.Contains(r.Source, "platform") {
		t.Errorf("runtime back = %+v", r)
	}
	// Symfony 7.1 : hors support depuis janvier 2025.
	if f := back.Framework; f.Name != "Symfony" || f.Version != "7.1.3" || f.Status != "unsupported" ||
		f.Latest != "8.1.8" || f.LatestLTS != "7.4.20" {
		t.Errorf("framework back = %+v", f)
	}
	if back.Status != "unsupported" {
		t.Errorf("statut le plus grave du back = %s", back.Status)
	}
	for name, want := range map[string][2]string{
		"symfony/framework-bundle": {"major", "8.1.8"},
		"symfony/console":          {"ok", "7.1.9"}, // même mineure
		"twig/twig":                {"minor", "3.14.0"},
		"doctrine/orm":             {"ok", "3.2.1"},
		"phpunit/phpunit":          {"major", "12.0.1"},
		"symfony/maker-bundle":     {"unknown", "1.60.0"}, // dev-main installé
	} {
		if st, latest := back.dep(t, name); st != want[0] || latest != want[1] {
			t.Errorf("%s = %s (dernière %s), attendu %s (dernière %s)", name, st, latest, want[0], want[1])
		}
	}
	if c := back.Counts; c["ok"] != 2 || c["minor"] != 1 || c["major"] != 2 || c["unknown"] != 1 {
		t.Errorf("compteurs du back = %v", c)
	}

	front := projects["shop / front"]
	if r := front.Runtime; r == nil || r.Version != "20.11.0" || r.Status != "unsupported" || r.Source != ".nvmrc" {
		t.Errorf("runtime front = %+v", r)
	}
	// Next.js passe avant React dans la table de correspondance.
	if f := front.Framework; f == nil || f.Name != "Next.js" || f.Version != "14.2.5" || f.Status != "major" {
		t.Errorf("framework front = %+v", f)
	}
	// Absents du registre simulé : mémorisés comme introuvables.
	if st, _ := front.dep(t, "@tanstack/react-query"); st != "unknown" {
		t.Errorf("paquet introuvable = %s", st)
	}
	if st, latest := front.dep(t, "react"); st != "major" || latest != "19.3.0" {
		t.Errorf("react = %s %s", st, latest)
	}
}

func TestIncrementalRescan(t *testing.T) {
	e := setup(t)
	e.scan()
	before, _ := e.store.ProjectByKey(context.Background(), "shop/back", "composer")
	cold := e.hits.Load()
	if cold == 0 {
		t.Fatal("aucune requête vers les registres")
	}

	// Cache chaud, rien de modifié : aucune requête, mêmes identifiants.
	e.scan()
	if got := e.hits.Load(); got != cold {
		t.Errorf("%d requêtes sur cache chaud", got-cold)
	}
	after, _ := e.store.ProjectByKey(context.Background(), "shop/back", "composer")
	if after.ID != before.ID || after.ManifestHash != before.ManifestHash {
		t.Errorf("projet inchangé modifié : %+v → %+v", before, after)
	}

	// Un lockfile modifié est reparsé.
	lock := filepath.Join(e.projects, "shop", "back", "composer.lock")
	data, _ := os.ReadFile(lock)
	os.WriteFile(lock, []byte(strings.Replace(string(data), `"version": "v3.10.3"`, `"version": "v3.14.0"`, 1)), 0o644)
	e.scan()
	if st, _ := e.api()["shop / back"].dep(t, "twig/twig"); st != "ok" {
		t.Errorf("twig après mise à jour du lockfile = %s", st)
	}

	// Un projet supprimé du disque disparaît de la base.
	os.RemoveAll(filepath.Join(e.projects, "shop", "front"))
	e.scan()
	if projects := e.api(); len(projects) != 1 {
		t.Errorf("%d projets après suppression", len(projects))
	}
}

func TestInvalidManifestDoesNotStopScan(t *testing.T) {
	e := setup(t)
	e.scan()
	manifest := filepath.Join(e.projects, "shop", "back", "composer.json")
	valid, _ := os.ReadFile(manifest)

	os.WriteFile(manifest, []byte(`{"require": `), 0o644)
	e.scan()
	projects := e.api()
	back := projects["shop / back"]
	if back.ScanError == "" {
		t.Error("projet invalide non marqué en erreur")
	}
	if len(back.Dependencies) == 0 {
		t.Error("le dernier inventaire valide a été perdu")
	}
	if projects["shop / front"].ScanError != "" || projects["shop / front"].Runtime == nil {
		t.Error("le projet voisin a été affecté")
	}
	contains(t, e.get("/"), "Erreur")
	contains(t, e.get(fmt.Sprintf("/projects/%d", back.ID)), "Projet en erreur")

	// Le manifeste corrigé efface l'erreur.
	os.WriteFile(manifest, valid, 0o644)
	e.scan()
	if got := e.api()["shop / back"].ScanError; got != "" {
		t.Errorf("erreur persistante : %s", got)
	}
}

func TestDegradedMode(t *testing.T) {
	e := setup(t)
	e.scan()

	// Six heures plus tard le cache a expiré et les registres sont en panne :
	// les dernières valeurs connues restent affichées, marquées comme telles.
	e.down.Store(true)
	later := now.Add(7 * time.Hour)
	e.enricher.Now = func() time.Time { return later }
	if err := e.sched.ScanAll(context.Background(), false); err != nil {
		t.Fatalf("une panne des registres ne doit pas faire échouer le scan : %v", err)
	}
	back := e.api()["shop / back"]
	if st, latest := back.dep(t, "twig/twig"); st != "minor" || latest != "3.14.0" {
		t.Errorf("valeur en cache perdue : %s %s", st, latest)
	}
	if back.Framework.Status != "unsupported" {
		t.Errorf("cycle de vie en cache perdu : %s", back.Framework.Status)
	}
}

func TestPages(t *testing.T) {
	e := setup(t)
	e.scan()
	back := e.projectID("shop/back", "composer")
	front := e.projectID("shop/front", "npm")

	overview := e.get("/")
	contains(t, overview, "shop / back", "shop / front", "PHP 8.2.20", "Symfony 7.1.3", "Next.js 14.2.5",
		"Non supportée", "Rescanner tout", "/static/htmx.min.js")

	filtered := e.get("/?ecosystem=npm")
	contains(t, filtered, "shop / front")
	if strings.Contains(filtered, "shop / back") {
		t.Error("le filtre par écosystème ne filtre pas")
	}
	contains(t, e.get("/?status=ok"), "Aucun projet ne correspond")

	page := e.get(fmt.Sprintf("/projects/%d", back))
	contains(t, page, "Fin de support proche", "31/12/2026", "Dernière LTS", "7.4.20", "tl-track",
		"Guide de migration officiel", "config.platform.php", "twig/twig", "phpunit/phpunit",
		"/packages/composer/twig/twig")
	// Par défaut, seules les dépendances directes sont listées.
	if strings.Contains(page, "doctrine/dbal") {
		t.Error("sous-dépendance affichée par défaut")
	}
	contains(t, e.get(fmt.Sprintf("/projects/%d?scope=all", back)), "doctrine/dbal", "indirecte")
	if strings.Contains(e.get(fmt.Sprintf("/projects/%d?nodev=1", back)), "phpunit/phpunit") {
		t.Error("dépendance de dev non masquée")
	}
	minor := e.get(fmt.Sprintf("/projects/%d?status=minor", back))
	contains(t, minor, "twig/twig")
	if strings.Contains(minor, "doctrine/orm") {
		t.Error("le filtre par statut ne filtre pas")
	}

	contains(t, e.get(fmt.Sprintf("/projects/%d", front)), "Next.js", "@tanstack/react-query",
		"/packages/npm/@tanstack/react-query", "supportée, pas de date de fin annoncée")

	usage := e.get("/packages/npm/@tanstack/react-query")
	contains(t, usage, "shop / front", "5.51.11", "^5.51.0")
	contains(t, e.get("/packages/composer/twig/twig"), "shop / back", "3.14.0")

	contains(t, e.get("/healthz"), "ok")
	contains(t, e.get("/static/style.css"), "--bg")

	for _, target := range []string{"/projects/999", "/projects/abc", "/packages/npm/absent", "/nope"} {
		if rec := e.request(http.MethodGet, target, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s : HTTP %d, 404 attendu", target, rec.Code)
		}
	}
}

func TestNoLockfileNotice(t *testing.T) {
	e := setup(t)
	os.Remove(filepath.Join(e.projects, "shop", "back", "composer.lock"))
	e.scan()
	back := e.api()["shop / back"]
	// Sans lockfile, la contrainte sert d'approximation.
	if f := back.Framework; f == nil || f.Version != "7.1" {
		t.Errorf("framework sans lockfile = %+v", f)
	}
	if st, _ := back.dep(t, "twig/twig"); st != "ok" {
		t.Errorf("twig ^3.0 face à 3.14.0 = %s", st)
	}
	contains(t, e.get(fmt.Sprintf("/projects/%d", back.ID)), "Pas de lockfile", "contrainte")
}

func waitIdle(t *testing.T, e *env) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.sched.State().Running {
		if time.Now().After(deadline) {
			t.Fatal("le scan ne se termine pas")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRescanRoutes(t *testing.T) {
	e := setup(t)

	rec := e.request(http.MethodPost, "/scan", url.Values{"back": {"/?ecosystem=npm"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?ecosystem=npm" {
		t.Errorf("POST /scan : HTTP %d vers %q", rec.Code, rec.Header().Get("Location"))
	}
	waitIdle(t, e)
	if len(e.api()) != 2 {
		t.Fatal("le scan déclenché par HTTP n'a rien trouvé")
	}

	// Une cible de retour externe est ignorée.
	rec = e.request(http.MethodPost, "/scan", url.Values{"back": {"//evil.example/"}})
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("redirection ouverte vers %q", loc)
	}
	waitIdle(t, e)

	// Le rescan d'un projet est forcé, même sans changement de manifeste.
	back := e.projectID("shop/back", "composer")
	rec = e.request(http.MethodPost, fmt.Sprintf("/projects/%d/scan", back), nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/projects/%d", back) {
		t.Errorf("POST scan projet : HTTP %d vers %q", rec.Code, rec.Header().Get("Location"))
	}
	waitIdle(t, e)
	if rec := e.request(http.MethodPost, "/projects/999/scan", nil); rec.Code != http.StatusNotFound {
		t.Errorf("rescan d'un projet inconnu : HTTP %d", rec.Code)
	}

	// Scan terminé : le fragment demande à htmx de recharger la page.
	rec = e.request(http.MethodGet, "/scan/status", nil)
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Error("HX-Refresh absent une fois le scan terminé")
	}

	// Une requête POST venue d'un autre site est refusée.
	req := httptest.NewRequest(http.MethodPost, "/scan", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST cross-site : HTTP %d, 403 attendu", rec.Code)
	}
}

func TestManualEntries(t *testing.T) {
	e := setup(t)
	e.scan()
	id := e.projectID("shop/back", "composer")
	post := func(form url.Values) {
		t.Helper()
		rec := e.request(http.MethodPost, fmt.Sprintf("/projects/%d/manual", id), form)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST manual %v : HTTP %d", form, rec.Code)
		}
		waitIdle(t, e)
	}

	// La saisie manuelle prime sur la détection, et survit aux rescans.
	post(url.Values{"action": {"runtime"}, "version": {"8.5.1"}})
	post(url.Values{"action": {"framework"}, "name": {"symfony"}, "version": {"7.4.2"}})
	post(url.Values{"action": {"dependency"}, "name": {"twig/twig"}, "version": {"3.14.0"}})
	if err := e.sched.ScanAll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	back := e.api()["shop / back"]
	if r := back.Runtime; r.Version != "8.5.1" || !r.Manual || r.Status != "ok" {
		t.Errorf("runtime manuel = %+v", r)
	}
	if f := back.Framework; f.Version != "7.4.2" || !f.Manual || f.Status != "ok" {
		t.Errorf("framework manuel (LTS supportée) = %+v", f)
	}
	twig := 0
	for _, d := range back.Dependencies {
		if d.Name == "twig/twig" {
			twig++
			if !d.Manual || d.Installed != "3.14.0" || d.Status != "ok" {
				t.Errorf("dépendance manuelle = %+v", d)
			}
		}
	}
	if twig != 1 {
		t.Errorf("twig/twig listé %d fois : la saisie doit masquer la détection", twig)
	}
	page := e.get(fmt.Sprintf("/projects/%d", id))
	contains(t, page, "saisie manuelle", "Retirer")

	// Version vide : retour à la détection.
	post(url.Values{"action": {"runtime"}, "version": {""}})
	post(url.Values{"action": {"framework"}, "name": {""}, "version": {""}})
	deps, _ := e.store.Dependencies(context.Background(), id)
	for _, d := range deps {
		if d.Manual {
			post(url.Values{"action": {"delete-dependency"}, "dep": {fmt.Sprint(d.ID)}})
		}
	}
	back = e.api()["shop / back"]
	if r := back.Runtime; r.Version != "8.2.20" || r.Manual {
		t.Errorf("runtime après annulation = %+v", r)
	}
	if f := back.Framework; f.Version != "7.1.3" || f.Manual {
		t.Errorf("framework après annulation = %+v", f)
	}
	if st, _ := back.dep(t, "twig/twig"); st != "minor" {
		t.Errorf("twig après retrait de la saisie = %s", st)
	}

	rec := e.request(http.MethodPost, fmt.Sprintf("/projects/%d/manual", id), url.Values{"action": {"autre"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("action inconnue : HTTP %d", rec.Code)
	}
}
