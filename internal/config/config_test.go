package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDefaults(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ProjectsDir != "/projects" || c.DataDir != "/data" || c.Port != 8080 || c.MaxDepth != 4 ||
		c.ScanInterval != 6*time.Hour || c.EOLWarning != 180*24*time.Hour ||
		c.MappingFile != "/data/mapping.json" || len(c.Ignore) != len(DefaultIgnore) {
		t.Errorf("valeurs par défaut : %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := FromEnv(env(map[string]string{
		"DEPDASH_PROJECTS_DIR":  "/src",
		"DEPDASH_DATA_DIR":      "/tmp/data",
		"DEPDASH_PORT":          "9090",
		"DEPDASH_SCAN_INTERVAL": "30m",
		"DEPDASH_MAX_DEPTH":     "2",
		"DEPDASH_EOL_WARNING":   "90d",
		"DEPDASH_IGNORE":        "tmp, .cache,,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ProjectsDir != "/src" || c.DataDir != "/tmp/data" || c.Port != 9090 || c.MaxDepth != 2 ||
		c.ScanInterval != 30*time.Minute || c.EOLWarning != 90*24*time.Hour ||
		c.MappingFile != "/tmp/data/mapping.json" {
		t.Errorf("surcharges : %+v", c)
	}
	if n := len(c.Ignore); n != len(DefaultIgnore)+2 || c.Ignore[n-2] != "tmp" || c.Ignore[n-1] != ".cache" {
		t.Errorf("dossiers ignorés : %v", c.Ignore)
	}
}

func TestInvalid(t *testing.T) {
	for key, value := range map[string]string{
		"DEPDASH_PORT":          "abc",
		"DEPDASH_SCAN_INTERVAL": "bientôt",
		"DEPDASH_MAX_DEPTH":     "-1",
		"DEPDASH_EOL_WARNING":   "xd",
	} {
		if _, err := FromEnv(env(map[string]string{key: value})); err == nil {
			t.Errorf("%s=%s : erreur attendue", key, value)
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := map[string]time.Duration{
		"180d": 180 * 24 * time.Hour,
		"2w":   14 * 24 * time.Hour,
		"6h":   6 * time.Hour,
		"1.5d": 36 * time.Hour,
	}
	for in, want := range tests {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v", in, got, err)
		}
	}
}

func TestMapping(t *testing.T) {
	m, err := LoadMapping(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := m.Runtime("php"); !ok || p.Product != "php" {
		t.Errorf("runtime php = %+v", p)
	}
	if p, ok := m.Framework("symfony"); !ok || p.Product != "symfony" || p.Guide == "" {
		t.Errorf("framework symfony = %+v", p)
	}
	if p, ok := m.Framework("nextjs"); !ok || p.Name != "Next.js" {
		t.Errorf("recherche par produit = %+v", p)
	}
	if _, ok := m.Framework("inconnu"); ok {
		t.Error("framework inconnu trouvé")
	}
	if len(m.Products()) < 8 {
		t.Errorf("produits = %v", m.Products())
	}

	path := filepath.Join(t.TempDir(), "mapping.json")
	os.WriteFile(path, []byte(`{"runtimes": {"php": {"name": "PHP", "product": "php"}},
		"frameworks": [{"ecosystem": "composer", "package": "slim/slim", "name": "Slim", "product": "slim"}]}`), 0o644)
	m, err = LoadMapping(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Framework("Slim"); !ok || len(m.Frameworks) != 1 {
		t.Errorf("fichier personnalisé ignoré : %+v", m)
	}
	os.WriteFile(path, []byte(`{`), 0o644)
	if _, err := LoadMapping(path); err == nil {
		t.Error("fichier invalide : erreur attendue")
	}
}
