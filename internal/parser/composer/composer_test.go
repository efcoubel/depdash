package composer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efcoubel/depdash/internal/parser"
)

const fixture = "../../../testdata/composer/symfony"

func find(t *testing.T, p *parser.Project, name string) parser.Dependency {
	t.Helper()
	for _, d := range p.Dependencies {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("dépendance %s absente", name)
	return parser.Dependency{}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseFixture(t *testing.T) {
	p, err := New().Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ecosystem != "composer" || p.Runtime != "php" || !p.HasLockfile {
		t.Errorf("projet inattendu : %+v", p)
	}
	if p.RuntimeVersion != "8.2.20" || p.RuntimeSource != "composer.json (config.platform.php)" {
		t.Errorf("PHP = %q depuis %q", p.RuntimeVersion, p.RuntimeSource)
	}
	// 4 directes + 2 de dev + 3 sous-dépendances ; php et ext-* sont exclus.
	if len(p.Dependencies) != 9 {
		t.Errorf("%d dépendances, 9 attendues", len(p.Dependencies))
	}

	tests := []struct {
		name, constraint, installed string
		direct, dev                 bool
	}{
		{"symfony/framework-bundle", "7.1.*", "7.1.3", true, false},
		{"twig/twig", "^3.0", "3.10.3", true, false},
		{"phpunit/phpunit", "^11.0", "11.2.8", true, true},
		{"symfony/maker-bundle", "^1.60", "dev-main", true, true},
		{"doctrine/dbal", "", "4.0.4", false, false},
		{"sebastian/diff", "", "6.0.2", false, true},
	}
	for _, tt := range tests {
		d := find(t, p, tt.name)
		if d.Constraint != tt.constraint || d.Installed != tt.installed || d.Direct != tt.direct || d.Dev != tt.dev {
			t.Errorf("%s = %+v", tt.name, d)
		}
	}
	for _, d := range p.Dependencies {
		if !strings.Contains(d.Name, "/") {
			t.Errorf("paquet de plateforme non filtré : %s", d.Name)
		}
	}
}

func TestPHPVersionPriority(t *testing.T) {
	manifest := `{"require": {"php": "^8.1"}, "config": {"platform": {"php": "8.2.5"}}}`
	steps := []struct {
		file, content, version, source string
	}{
		{"composer.json", `{"require": {"php": "^8.1"}}`, "8.1", "composer.json (require.php)"},
		{"composer.json", manifest, "8.2.5", "composer.json (config.platform.php)"},
		{"docker/php/Dockerfile", "FROM --platform=linux/amd64 php:8.3-fpm-alpine AS base\n", "8.3", "docker/php/Dockerfile"},
		{".php-version", "8.4.1\n", "8.4.1", ".php-version"},
	}
	// Chaque étape ajoute une source plus prioritaire que les précédentes.
	dir := t.TempDir()
	for _, step := range steps {
		write(t, dir, step.file, step.content)
		p, err := New().Parse(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.RuntimeVersion != step.version || p.RuntimeSource != step.source {
			t.Errorf("après %s : PHP %q depuis %q, attendu %q depuis %q",
				step.file, p.RuntimeVersion, p.RuntimeSource, step.version, step.source)
		}
	}
}

func TestParseWithoutLockfile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "composer.json", `{"require": {"laravel/framework": "^11.0"}, "require-dev": [], "config": []}`)
	p, err := New().Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.HasLockfile {
		t.Error("lockfile signalé à tort")
	}
	d := find(t, p, "laravel/framework")
	if d.Installed != "" || d.Constraint != "^11.0" || !d.Direct {
		t.Errorf("laravel/framework = %+v", d)
	}
	if p.RuntimeVersion != "" {
		t.Errorf("version PHP inventée : %q", p.RuntimeVersion)
	}
}

func TestParseInvalid(t *testing.T) {
	dir := t.TempDir()
	if _, err := New().Parse(dir); err == nil {
		t.Error("manifeste absent : erreur attendue")
	}
	write(t, dir, "composer.json", `{"require": `)
	if _, err := New().Parse(dir); err == nil {
		t.Error("composer.json invalide : erreur attendue")
	}
	write(t, dir, "composer.json", `{"require": {"a/b": "^1"}}`)
	write(t, dir, "composer.lock", `not json`)
	if _, err := New().Parse(dir); err == nil {
		t.Error("composer.lock invalide : erreur attendue")
	}
}

func TestDetect(t *testing.T) {
	if !New().Detect(fixture) {
		t.Error("fixture non détectée")
	}
	if New().Detect(t.TempDir()) {
		t.Error("dossier vide détecté")
	}
	if New().Ecosystem() != "composer" || len(New().Files()) == 0 {
		t.Error("métadonnées du parser")
	}
}
