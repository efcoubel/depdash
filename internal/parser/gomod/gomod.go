// Package gomod lit go.mod.
package gomod

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/efcoubel/depdash/internal/parser"
)

const Ecosystem = "go"

type Parser struct{}

func New() Parser { return Parser{} }

func (Parser) Ecosystem() string { return Ecosystem }

func (Parser) Detect(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
}

func (Parser) Files() []string {
	return append([]string{"go.mod"}, parser.DockerfileGlobs...)
}

func (Parser) Parse(dir string) (*parser.Project, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	// ParseLax tolère les directives qu'une version plus récente de Go
	// aurait introduites.
	f, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil {
		return nil, fmt.Errorf("go.mod invalide : %w", err)
	}

	// go.mod fixe lui-même les versions exactes : il tient lieu de lockfile.
	p := &parser.Project{Ecosystem: Ecosystem, Runtime: "go", HasLockfile: true}
	p.RuntimeVersion, p.RuntimeSource = goVersion(dir, f)

	for _, r := range f.Require {
		d := parser.Dependency{
			Name:      r.Mod.Path,
			Installed: strings.TrimPrefix(r.Mod.Version, "v"),
			Direct:    !r.Indirect,
		}
		if d.Direct {
			d.Constraint = r.Mod.Version
		}
		p.Dependencies = append(p.Dependencies, d)
	}

	parser.SortDependencies(p.Dependencies)
	return p, nil
}

// goVersion retient, dans l'ordre, l'image du Dockerfile (ce qui compile
// réellement), la directive toolchain, puis la directive go, qui n'est qu'un
// minimum.
func goVersion(dir string, f *modfile.File) (version, source string) {
	if v, file := parser.DockerImageVersion(dir, "golang"); v != "" {
		return v, file
	}
	if v := parser.FirstVersion(toolchain(f)); v != "" {
		return v, "go.mod (toolchain)"
	}
	if f.Go != nil {
		if v := parser.FirstVersion(f.Go.Version); v != "" {
			return v, "go.mod (go)"
		}
	}
	return "", ""
}

// toolchain lit la directive dans l'arbre syntaxique : ParseLax ne la
// reporte pas dans File.Toolchain.
func toolchain(f *modfile.File) string {
	for _, stmt := range f.Syntax.Stmt {
		if line, ok := stmt.(*modfile.Line); ok && len(line.Token) == 2 && line.Token[0] == "toolchain" {
			return line.Token[1]
		}
	}
	return ""
}
