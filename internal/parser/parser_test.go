package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHash(t *testing.T) {
	dir := t.TempDir()
	patterns := []string{"a.json", "b.lock", "Dockerfile.*"}
	hash := func() string {
		t.Helper()
		h, err := Hash(dir, patterns)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	write(t, dir, "a.json", "{}")
	h1 := hash()
	if h1 != hash() {
		t.Error("empreinte instable")
	}
	write(t, dir, "ignored.txt", "x")
	if h1 != hash() {
		t.Error("un fichier hors motifs modifie l'empreinte")
	}
	write(t, dir, "a.json", `{"x":1}`)
	h2 := hash()
	if h2 == h1 {
		t.Error("un contenu modifié ne change pas l'empreinte")
	}
	write(t, dir, "b.lock", "")
	h3 := hash()
	if h3 == h2 {
		t.Error("un fichier vide ajouté ne change pas l'empreinte")
	}
	write(t, dir, "Dockerfile.dev", "FROM php:8.3")
	if hash() == h3 {
		t.Error("un fichier trouvé par motif ne change pas l'empreinte")
	}
}

func TestFirstVersion(t *testing.T) {
	tests := map[string]string{
		"^8.2":        "8.2",
		">=18.17 <21": "18.17",
		"v20.11.0":    "20.11.0",
		"8.3.*":       "8.3",
		"lts/iron":    "",
		"":            "",
	}
	for in, want := range tests {
		if got := FirstVersion(in); got != want {
			t.Errorf("FirstVersion(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestDockerImageVersion(t *testing.T) {
	dir := t.TempDir()
	if v, _ := DockerImageVersion(dir, "php"); v != "" {
		t.Errorf("version sans Dockerfile : %q", v)
	}
	write(t, dir, "Dockerfile", "# build\nFROM node:20-alpine AS assets\nFROM ghcr.io/acme/php:8.3.12-fpm\n")
	if v, f := DockerImageVersion(dir, "php"); v != "8.3.12" || f != "Dockerfile" {
		t.Errorf("php = %q dans %q", v, f)
	}
	if v, _ := DockerImageVersion(dir, "node"); v != "20" {
		t.Errorf("node = %q", v)
	}
	if v, _ := DockerImageVersion(dir, "python"); v != "" {
		t.Errorf("python = %q", v)
	}
}

func TestStringMap(t *testing.T) {
	var m StringMap
	if err := json.Unmarshal([]byte(`{"a": "1", "b": false}`), &m); err != nil || m["a"] != "1" || len(m) != 1 {
		t.Errorf("objet : %v %v", m, err)
	}
	if err := json.Unmarshal([]byte(`[]`), &m); err != nil || len(m) != 0 {
		t.Errorf("tableau vide : %v %v", m, err)
	}
	if err := json.Unmarshal([]byte(`"x"`), &m); err == nil {
		t.Error("chaîne : erreur attendue")
	}
}

func TestReadVersionFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".nvmrc", "  v20.11.0\n")
	if got := ReadVersionFile(filepath.Join(dir, ".nvmrc")); got != "20.11.0" {
		t.Errorf("version lue = %q", got)
	}
	if got := ReadVersionFile(filepath.Join(dir, "absent")); got != "" {
		t.Errorf("fichier absent = %q", got)
	}
}

func TestSortDependencies(t *testing.T) {
	deps := []Dependency{{Name: "b", Installed: "1.0.0"}, {Name: "a", Installed: "2.0.0"}, {Name: "a", Installed: "1.0.0"}}
	SortDependencies(deps)
	if deps[0].Installed != "1.0.0" || deps[0].Name != "a" || deps[1].Installed != "2.0.0" || deps[2].Name != "b" {
		t.Errorf("ordre = %+v", deps)
	}
}
