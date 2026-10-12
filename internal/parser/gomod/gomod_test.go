package gomod

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efcoubel/depdash/internal/parser"
)

const manifest = `module example.com/app

go 1.24.0

require (
	github.com/Masterminds/semver/v3 v3.5.0
	modernc.org/sqlite v1.60.1
)

require golang.org/x/sys v0.48.0 // indirect
`

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

func TestParse(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", manifest)
	p, err := New().Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ecosystem != Ecosystem || p.Runtime != "go" || !p.HasLockfile {
		t.Errorf("projet = %+v", p)
	}
	if p.RuntimeVersion != "1.24.0" || p.RuntimeSource != "go.mod (go)" {
		t.Errorf("runtime = %q (%s)", p.RuntimeVersion, p.RuntimeSource)
	}
	want := []parser.Dependency{
		{Name: "github.com/Masterminds/semver/v3", Constraint: "v3.5.0", Installed: "3.5.0", Direct: true},
		{Name: "golang.org/x/sys", Installed: "0.48.0"},
		{Name: "modernc.org/sqlite", Constraint: "v1.60.1", Installed: "1.60.1", Direct: true},
	}
	if len(p.Dependencies) != len(want) {
		t.Fatalf("dépendances = %+v", p.Dependencies)
	}
	for i, d := range want {
		if p.Dependencies[i] != d {
			t.Errorf("dépendance %d = %+v, attendue %+v", i, p.Dependencies[i], d)
		}
	}
}

func TestGoVersionPriority(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", manifest+"\ntoolchain go1.25.3\n")
	check := func(version, source string) {
		t.Helper()
		p, err := New().Parse(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.RuntimeVersion != version || p.RuntimeSource != source {
			t.Errorf("runtime = %q (%s), attendu %q (%s)", p.RuntimeVersion, p.RuntimeSource, version, source)
		}
	}
	check("1.25.3", "go.mod (toolchain)")
	write(t, dir, "Dockerfile", "FROM golang:1.26-alpine AS build\n")
	check("1.26", "Dockerfile")
}

func TestParseInvalid(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "require (\n")
	if _, err := New().Parse(dir); err == nil {
		t.Error("go.mod invalide : erreur attendue")
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	if New().Detect(dir) {
		t.Error("dossier vide détecté")
	}
	write(t, dir, "go.mod", manifest)
	if !New().Detect(dir) {
		t.Error("go.mod non détecté")
	}
	if len(New().Files()) == 0 {
		t.Error("métadonnées du parser")
	}
}
