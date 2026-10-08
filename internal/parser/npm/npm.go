// Package npm lit package.json et package-lock.json (formats v1, v2 et v3).
package npm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/efcoubel/depdash/internal/parser"
)

const Ecosystem = "npm"

type Parser struct{}

func New() Parser { return Parser{} }

func (Parser) Ecosystem() string { return Ecosystem }

func (Parser) Detect(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "package.json"))
	return err == nil && !info.IsDir()
}

func (Parser) Files() []string {
	return append([]string{"package.json", "package-lock.json", ".nvmrc", ".node-version"}, parser.DockerfileGlobs...)
}

type manifest struct {
	Dependencies    parser.StringMap `json:"dependencies"`
	DevDependencies parser.StringMap `json:"devDependencies"`
	Engines         json.RawMessage  `json:"engines"`
}

type lockfile struct {
	// Formats v2 et v3 : une entrée par chemin d'installation.
	Packages map[string]lockPackage `json:"packages"`
	// Format v1 : arbre imbriqué.
	Dependencies map[string]lockV1 `json:"dependencies"`
}

type lockPackage struct {
	Version string `json:"version"`
	Dev     bool   `json:"dev"`
	Link    bool   `json:"link"`
}

type lockV1 struct {
	Version      string            `json:"version"`
	Dev          bool              `json:"dev"`
	Dependencies map[string]lockV1 `json:"dependencies"`
}

// locked est une entrée du lockfile ramenée à une forme commune.
type locked struct {
	name, version string
	dev, top      bool
}

func (Parser) Parse(dir string) (*parser.Project, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("package.json invalide : %w", err)
	}

	p := &parser.Project{Ecosystem: Ecosystem, Runtime: "node"}
	p.RuntimeVersion, p.RuntimeSource = nodeVersion(dir, m)

	var entries []locked
	data, err = os.ReadFile(filepath.Join(dir, "package-lock.json"))
	switch {
	case err == nil:
		var lock lockfile
		if err := json.Unmarshal(data, &lock); err != nil {
			return nil, fmt.Errorf("package-lock.json invalide : %w", err)
		}
		p.HasLockfile = true
		entries = flatten(lock)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}

	installed := map[string]string{}
	for _, e := range entries {
		if e.top {
			installed[e.name] = e.version
		}
	}

	direct := map[string]bool{}
	add := func(deps parser.StringMap, dev bool) {
		for name, constraint := range deps {
			if direct[name] {
				continue
			}
			direct[name] = true
			p.Dependencies = append(p.Dependencies, parser.Dependency{
				Name: name, Constraint: constraint, Installed: installed[name], Direct: true, Dev: dev,
			})
		}
	}
	add(m.Dependencies, false)
	add(m.DevDependencies, true)

	// Un même paquet peut être installé plusieurs fois à des versions
	// différentes : chaque couple nom + version n'est listé qu'une fois.
	seen := map[string]bool{}
	for _, e := range entries {
		if e.top && direct[e.name] {
			continue
		}
		key := e.name + "@" + e.version
		if seen[key] {
			continue
		}
		seen[key] = true
		p.Dependencies = append(p.Dependencies, parser.Dependency{Name: e.name, Installed: e.version, Dev: e.dev})
	}

	parser.SortDependencies(p.Dependencies)
	return p, nil
}

func flatten(lock lockfile) []locked {
	var out []locked
	if len(lock.Packages) > 0 {
		for path, pkg := range lock.Packages {
			// Les entrées hors node_modules sont la racine et les workspaces ;
			// les liens pointent vers un workspace local.
			i := strings.LastIndex(path, "node_modules/")
			if i < 0 || pkg.Link || pkg.Version == "" {
				continue
			}
			out = append(out, locked{
				name: path[i+len("node_modules/"):], version: pkg.Version,
				dev: pkg.Dev, top: i == 0,
			})
		}
		return out
	}
	var walk func(deps map[string]lockV1, top bool)
	walk = func(deps map[string]lockV1, top bool) {
		for name, dep := range deps {
			if dep.Version != "" {
				out = append(out, locked{name: name, version: dep.Version, dev: dep.Dev, top: top})
			}
			walk(dep.Dependencies, false)
		}
	}
	walk(lock.Dependencies, true)
	return out
}

// nodeVersion applique l'ordre de priorité du cahier des charges et renvoie
// la version avec la source retenue.
func nodeVersion(dir string, m manifest) (version, source string) {
	for _, name := range []string{".nvmrc", ".node-version"} {
		if v := parser.ReadVersionFile(filepath.Join(dir, name)); v != "" {
			return v, name
		}
	}
	var engines parser.StringMap
	// engines est parfois un tableau dans de vieux manifestes : erreur ignorée.
	if json.Unmarshal(m.Engines, &engines) == nil {
		if v := parser.FirstVersion(engines["node"]); v != "" {
			return v, "package.json (engines.node)"
		}
	}
	if v, file := parser.DockerImageVersion(dir, "node"); v != "" {
		return v, file
	}
	return "", ""
}
