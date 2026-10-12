package scheduler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/efcoubel/depdash/internal/config"
	"github.com/efcoubel/depdash/internal/parser/composer"
	"github.com/efcoubel/depdash/internal/parser/npm"
	"github.com/efcoubel/depdash/internal/scanner"
	"github.com/efcoubel/depdash/internal/store"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanAllIgnoresManifestsWithNothingToTrack(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app/composer.json", `{"require": {"symfony/console": "^6.4"}}`)
	write(t, root, "app/public/composer.json", `{}`)
	write(t, root, "app/out/package.json", `{"type": "commonjs"}`)
	// sans dépendance, mais la version du runtime reste à suivre
	write(t, root, "tool/composer.json", `{"require": {"php": "^8.2"}}`)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Scheduler{
		Scanner: scanner.New(root, 4, config.DefaultIgnore, composer.New(), npm.New()),
		Store:   st,
		Mapping: config.DefaultMapping(),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()

	paths := func() []string {
		t.Helper()
		if err := s.ScanAll(ctx, false); err != nil {
			t.Fatal(err)
		}
		projects, err := st.Projects(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range projects {
			out = append(out, p.Path)
		}
		return out
	}

	if got := paths(); len(got) != 2 || got[0] != "app" || got[1] != "tool" {
		t.Fatalf("projets = %v, attendus [app tool]", got)
	}

	// Un projet connu dont le manifeste se vide quitte l'inventaire.
	write(t, root, "app/composer.json", `{}`)
	if got := paths(); len(got) != 1 || got[0] != "tool" {
		t.Fatalf("projets = %v, attendus [tool]", got)
	}
}
