package status

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/efcoubel/depdash/internal/registry"
)

func TestPackage(t *testing.T) {
	tests := []struct {
		name                  string
		installed, constraint string
		rel                   registry.Release
		want                  Status
		latest                string
	}{
		{"dernière version", "7.1.3", "", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"correctif de retard", "7.1.1", "", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"préfixe v", "v7.1.3", "", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"mineure de retard", "7.0.9", "", registry.Release{Latest: "7.1.3"}, MinorUpdate, "7.1.3"},
		{"majeure de retard", "6.4.10", "", registry.Release{Latest: "7.1.3"}, MajorAvailable, "7.1.3"},
		{"en avance sur le registre", "8.0.0", "", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"paquet introuvable", "1.0.0", "", registry.Release{}, Unknown, ""},
		{"branche", "dev-main", "", registry.Release{Latest: "1.2.0"}, Unknown, "1.2.0"},

		// Les préversions ne comptent que si l'on en utilise une.
		{"stable ignore les préversions", "2.0.0", "",
			registry.Release{Latest: "2.0.0", Prereleases: []string{"3.0.0-beta.1"}}, UpToDate, "2.0.0"},
		{"préversion comparée aux préversions", "3.0.0-alpha.2", "",
			registry.Release{Latest: "2.0.0", Prereleases: []string{"3.0.0-beta.1", "3.0.0-alpha.2"}}, UpToDate, "3.0.0-beta.1"},
		{"préversion dépassée par une stable", "2.0.0-rc.1", "",
			registry.Release{Latest: "3.1.0"}, MajorAvailable, "3.1.0"},
		{"paquet sans version stable", "0.1.0-beta.1", "",
			registry.Release{Prereleases: []string{"0.2.0-beta.1"}}, MinorUpdate, "0.2.0-beta.1"},

		// Sans lockfile, la contrainte sert d'approximation.
		{"contrainte satisfaite", "", "^7.0", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"contrainte composer avec alternative", "", "^6.4|^7.0", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"contrainte avec drapeau", "", "^7.0@dev", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"contrainte en retard d'une majeure", "", "^6.4", registry.Release{Latest: "7.1.3"}, MajorAvailable, "7.1.3"},
		{"contrainte en retard d'une mineure", "", "7.0.*", registry.Release{Latest: "7.1.3"}, MinorUpdate, "7.1.3"},
		{"contrainte en avance", "", "^8.0", registry.Release{Latest: "7.1.3"}, UpToDate, "7.1.3"},
		{"contrainte illisible", "", "github:acme/lib", registry.Release{Latest: "7.1.3"}, Unknown, "7.1.3"},
		{"contrainte vide", "", "", registry.Release{Latest: "7.1.3"}, Unknown, "7.1.3"},
		{"contrainte sans registre", "", "^7.0", registry.Release{}, Unknown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, latest := Package(tt.installed, tt.constraint, tt.rel)
			if got != tt.want || latest != tt.latest {
				t.Errorf("Package(%q, %q) = %s, %q ; attendu %s, %q",
					tt.installed, tt.constraint, got.Label(), latest, tt.want.Label(), tt.latest)
			}
		})
	}
}

var now = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

const warn = 180 * 24 * time.Hour

var php = []registry.Cycle{
	{Cycle: "8.5", Latest: "8.5.11", EOLActive: "2027-12-31", EOLSecurity: "2029-12-31"},
	{Cycle: "8.4", Latest: "8.4.26", EOLActive: "2026-12-31", EOLSecurity: "2028-12-31"},
	{Cycle: "8.2", Latest: "8.2.31", EOLActive: "2024-12-31", EOLSecurity: "2026-12-31"},
	{Cycle: "8.1", Latest: "8.1.34", EOLActive: "2023-11-25", EOLSecurity: "2025-12-31"},
	{Cycle: "7", Latest: "7.4.33", EOLSecurity: "true"},
}

var symfony = []registry.Cycle{
	{Cycle: "8.1", Latest: "8.1.8", EOLActive: "2027-01-31", EOLSecurity: "2027-01-31"},
	{Cycle: "8.0", Latest: "8.0.16", EOLActive: "2026-07-31", EOLSecurity: "2026-07-31"},
	{Cycle: "7.4", Latest: "7.4.20", LTS: true, EOLActive: "2028-11-30", EOLSecurity: "2029-11-30"},
	{Cycle: "6.4", Latest: "6.4.30", LTS: true, EOLActive: "2026-11-30", EOLSecurity: "2027-11-30"},
}

var node = []registry.Cycle{
	{Cycle: "26", Latest: "26.11.1", EOLActive: "2027-10-27", EOLSecurity: "2029-04-30"},
	{Cycle: "24", Latest: "24.9.0", LTS: true, EOLActive: "2026-10-20", EOLSecurity: "2028-04-30"},
}

var react = []registry.Cycle{
	{Cycle: "19", Latest: "19.3.0", EOLActive: "false", EOLSecurity: "false"},
	{Cycle: "18", Latest: "18.3.1", EOLActive: "2024-12-05", EOLSecurity: "false"},
}

func TestComponent(t *testing.T) {
	tests := []struct {
		name    string
		version string
		cycles  []registry.Cycle
		want    Status
		current string
	}{
		{"dernier cycle", "8.5.11", php, UpToDate, "8.5"},
		{"dernier cycle, correctif de retard", "8.5.2", php, UpToDate, "8.5"},
		{"version partielle", "8.5", php, UpToDate, "8.5"},
		{"mineure de retard, encore supportée", "8.4.1", php, MinorUpdate, "8.4"},
		{"fin de support dans moins de 6 mois", "8.2.20", php, EOLSoon, "8.2"},
		{"fin de support dépassée", "8.1.0", php, Unsupported, "8.1"},
		{"fin de support sans date", "7.4.33", php, Unsupported, "7"},
		{"cycle inconnu", "5.6.40", php, Unknown, ""},
		{"version vide", "", php, Unknown, ""},
		{"aucun cycle en cache", "8.3.1", nil, Unknown, ""},

		// Une LTS encore supportée n'est pas « en retard ».
		{"LTS supportée face à une majeure", "7.4.20", symfony, UpToDate, "7.4"},
		{"LTS supportée, ancienne majeure", "6.4.30", symfony, UpToDate, "6.4"},
		{"hors LTS, support terminé", "8.0.16", symfony, Unsupported, "8.0"},
		{"dernier cycle bientôt en fin de vie", "v8.1.8", symfony, EOLSoon, "8.1"},

		// Cycle majeur seul : l'écart de mineure se lit dans la version.
		{"Node LTS à jour", "24.9.0", node, UpToDate, "24"},
		{"Node LTS, mineure de retard", "24.1.0", node, MinorUpdate, "24"},
		{"Node, majeure seule", "24", node, UpToDate, "24"},
		{"Node courant, mineure de retard", "26.2.0", node, MinorUpdate, "26"},

		{"sans date de fin, majeure disponible", "18.3.1", react, MajorAvailable, "18"},
		{"sans date de fin, à jour", "19.3.0", react, UpToDate, "19"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lc := Component(tt.version, tt.cycles, now, warn)
			current := ""
			if lc.Current != nil {
				current = lc.Current.Cycle
			}
			if lc.Status != tt.want || current != tt.current {
				t.Errorf("Component(%q) = %s (cycle %q) ; attendu %s (cycle %q)",
					tt.version, lc.Status.Label(), current, tt.want.Label(), tt.current)
			}
		})
	}
}

func TestComponentLatest(t *testing.T) {
	lc := Component("6.4.30", symfony, now, warn)
	if lc.Latest == nil || lc.Latest.Cycle != "8.1" {
		t.Errorf("dernier cycle = %+v", lc.Latest)
	}
	if lc.LatestLTS == nil || lc.LatestLTS.Cycle != "7.4" {
		t.Errorf("dernière LTS = %+v", lc.LatestLTS)
	}
	if lc := Component("8.5.1", php, now, warn); lc.LatestLTS != nil {
		t.Errorf("PHP n'a pas de LTS : %+v", lc.LatestLTS)
	}
}

func TestThresholdIsConfigurable(t *testing.T) {
	// PHP 8.2 s'arrête le 31/12/2026, soit 84 jours après « now ».
	if got := Component("8.2.20", php, now, 30*24*time.Hour).Status; got != MinorUpdate {
		t.Errorf("seuil de 30 jours : %s", got.Label())
	}
	if got := Component("8.2.20", php, now, 90*24*time.Hour).Status; got != EOLSoon {
		t.Errorf("seuil de 90 jours : %s", got.Label())
	}
}

func TestStatusMetadata(t *testing.T) {
	if !(Unsupported > EOLSoon && EOLSoon > MajorAvailable && MajorAvailable > MinorUpdate &&
		MinorUpdate > Unknown && Unknown > UpToDate) {
		t.Error("ordre de gravité")
	}
	seen := map[string]bool{}
	for _, s := range All {
		if s.Label() == "" || s.Key() == "" || seen[s.Key()] {
			t.Errorf("statut %d mal décrit", s)
		}
		seen[s.Key()] = true
	}
	if len(seen) != 6 {
		t.Errorf("%d statuts, 6 attendus", len(seen))
	}
	if data, _ := json.Marshal(EOLSoon); string(data) != `"eol-soon"` {
		t.Errorf("JSON = %s", data)
	}
}
