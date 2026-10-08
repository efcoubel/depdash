package npm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efcoubel/depdash/internal/parser"
)

const fixtures = "../../../testdata/npm/"

func find(t *testing.T, p *parser.Project, name, version string) parser.Dependency {
	t.Helper()
	for _, d := range p.Dependencies {
		if d.Name == name && (version == "" || d.Installed == version) {
			return d
		}
	}
	t.Fatalf("dépendance %s %s absente", name, version)
	return parser.Dependency{}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseLockV3(t *testing.T) {
	p, err := New().Parse(fixtures + "react-app")
	if err != nil {
		t.Fatal(err)
	}
	if p.Runtime != "node" || p.RuntimeVersion != "20.11.0" || p.RuntimeSource != ".nvmrc" || !p.HasLockfile {
		t.Errorf("projet inattendu : %+v", p)
	}
	// 6 directes + query-core, js-tokens, loose-envify et deux postcss ; le
	// workspace lié et la racine sont exclus.
	if len(p.Dependencies) != 11 {
		t.Errorf("%d dépendances, 11 attendues", len(p.Dependencies))
	}

	tests := []struct {
		name, version, constraint string
		direct, dev               bool
	}{
		{"next", "14.2.5", "14.2.5", true, false},
		{"@tanstack/react-query", "5.51.11", "^5.51.0", true, false},
		{"vitest", "2.0.0-beta.12", "^2.0.0-beta.12", true, true},
		{"@tanstack/query-core", "5.51.9", "", false, false},
		{"postcss", "8.4.31", "", false, false}, // imbriqué sous next
		{"postcss", "8.4.40", "", false, true},
	}
	for _, tt := range tests {
		d := find(t, p, tt.name, tt.version)
		if d.Constraint != tt.constraint || d.Direct != tt.direct || d.Dev != tt.dev {
			t.Errorf("%s %s = %+v", tt.name, tt.version, d)
		}
	}
}

func TestParseLockV1(t *testing.T) {
	p, err := New().Parse(fixtures + "legacy-v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Dependencies) != 5 {
		t.Errorf("%d dépendances, 5 attendues", len(p.Dependencies))
	}
	if d := find(t, p, "vue", ""); d.Installed != "2.6.14" || !d.Direct || d.Dev {
		t.Errorf("vue = %+v", d)
	}
	if d := find(t, p, "mocha", ""); !d.Direct || !d.Dev {
		t.Errorf("mocha = %+v", d)
	}
	if d := find(t, p, "debug", "2.6.9"); d.Direct || d.Dev {
		t.Errorf("debug imbriqué = %+v", d)
	}
	if d := find(t, p, "debug", "4.3.1"); d.Direct || !d.Dev {
		t.Errorf("debug de dev = %+v", d)
	}
	if p.RuntimeVersion != "" {
		t.Errorf("version Node inventée : %q", p.RuntimeVersion)
	}
}

func TestNodeVersionPriority(t *testing.T) {
	steps := []struct {
		file, content, version, source string
	}{
		{"Dockerfile", "FROM node:22-alpine\n", "22", "Dockerfile"},
		{"package.json", `{"engines": {"node": ">=18.17"}}`, "18.17", "package.json (engines.node)"},
		{".node-version", "v20.9.0\n", "20.9.0", ".node-version"},
		{".nvmrc", "21\n", "21", ".nvmrc"},
	}
	dir := t.TempDir()
	write(t, dir, "package.json", `{}`)
	for _, step := range steps {
		write(t, dir, step.file, step.content)
		p, err := New().Parse(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.RuntimeVersion != step.version || p.RuntimeSource != step.source {
			t.Errorf("après %s : Node %q depuis %q, attendu %q depuis %q",
				step.file, p.RuntimeVersion, p.RuntimeSource, step.version, step.source)
		}
	}
	// Un alias nvm sans numéro n'est pas une version : la source suivante sert.
	write(t, dir, ".nvmrc", "lts/iron\n")
	if p, _ := New().Parse(dir); p.RuntimeSource != ".node-version" {
		t.Errorf("alias nvm : source %q", p.RuntimeSource)
	}
}

func TestParseWithoutLockfile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"dependencies": {"react": "^19.0.0"}, "engines": ["node"]}`)
	p, err := New().Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.HasLockfile || len(p.Dependencies) != 1 {
		t.Fatalf("projet inattendu : %+v", p)
	}
	if d := p.Dependencies[0]; d.Installed != "" || d.Constraint != "^19.0.0" {
		t.Errorf("react = %+v", d)
	}
}

func TestParseInvalid(t *testing.T) {
	dir := t.TempDir()
	if _, err := New().Parse(dir); err == nil {
		t.Error("manifeste absent : erreur attendue")
	}
	write(t, dir, "package.json", `{`)
	if _, err := New().Parse(dir); err == nil {
		t.Error("package.json invalide : erreur attendue")
	}
	write(t, dir, "package.json", `{}`)
	write(t, dir, "package-lock.json", `[`)
	if _, err := New().Parse(dir); err == nil {
		t.Error("package-lock.json invalide : erreur attendue")
	}
}

func TestDetect(t *testing.T) {
	if !New().Detect(fixtures + "react-app") {
		t.Error("fixture non détectée")
	}
	if New().Detect(t.TempDir()) {
		t.Error("dossier vide détecté")
	}
	if New().Ecosystem() != "npm" || len(New().Files()) == 0 {
		t.Error("métadonnées du parser")
	}
}
