package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efcoubel/depdash/internal/parser/composer"
	"github.com/efcoubel/depdash/internal/parser/npm"
)

func tree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScan(t *testing.T) {
	root := tree(t,
		// monorepo : back Symfony + front React dans le même repo
		"shop/.git/HEAD",
		"shop/back/composer.json",
		"shop/front/package.json",
		// un dossier, deux écosystèmes
		"blog/.git/HEAD",
		"blog/composer.json",
		"blog/package.json",
		// sans .git : le dossier de premier niveau fait office de repo
		"scripts/tools/package.json",
		// repo imbriqué
		"clients/acme/api/.git/HEAD",
		"clients/acme/api/composer.json",
		// dossiers ignorés
		"blog/vendor/symfony/console/composer.json",
		"blog/node_modules/react/package.json",
		"shop/front/dist/package.json",
		"shop/back/var/cache/composer.json",
		"blog/tmp/composer.json",
		// trop profond (profondeur 5)
		"a/b/c/d/e/package.json",
	)
	s := New(root, 4, []string{"vendor", "node_modules", ".git", "var", "dist", "build", "tmp"}, composer.New(), npm.New())
	found, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}

	type row struct{ path, ecosystem, repoPath, repoName string }
	var got []row
	for _, f := range found {
		got = append(got, row{f.Path, f.Parser.Ecosystem(), f.RepoPath, f.RepoName})
		if f.Dir != filepath.Join(root, f.Path) {
			t.Errorf("dossier absolu incohérent : %s", f.Dir)
		}
	}
	want := []row{
		{"blog", "composer", "blog", "blog"},
		{"blog", "npm", "blog", "blog"},
		{"clients/acme/api", "composer", "clients/acme/api", "api"},
		{"scripts/tools", "npm", "scripts", "scripts"},
		{"shop/back", "composer", "shop", "shop"},
		{"shop/front", "npm", "shop", "shop"},
	}
	if len(got) != len(want) {
		t.Fatalf("projets trouvés :\n%v\nattendus :\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("projet %d = %v, attendu %v", i, got[i], want[i])
		}
	}
}

func TestScanDepth(t *testing.T) {
	root := tree(t, "package.json", "a/package.json", "a/b/package.json")
	for depth, want := range map[int]int{0: 1, 1: 2, 2: 3} {
		found, err := New(root, depth, nil, npm.New()).Scan()
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != want {
			t.Errorf("profondeur %d : %d projets, %d attendus", depth, len(found), want)
		}
	}
	// Un manifeste à la racine même du dossier scanné.
	found, _ := New(root, 0, nil, npm.New()).Scan()
	if found[0].Path != "." || found[0].RepoPath != "." || found[0].RepoName != filepath.Base(root) {
		t.Errorf("projet racine = %+v", found[0])
	}
}

func TestScanSkipsSymlinks(t *testing.T) {
	root := tree(t, "real/package.json")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skip("liens symboliques indisponibles")
	}
	found, err := New(root, 4, nil, npm.New()).Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Path != "real" {
		t.Errorf("le lien symbolique a été suivi : %+v", found)
	}
}

func TestScanMissingRoot(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "absent"), 4, nil, npm.New()).Scan(); err == nil {
		t.Error("racine absente : erreur attendue")
	}
}
