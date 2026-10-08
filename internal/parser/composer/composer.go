// Package composer lit composer.json et composer.lock.
package composer

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

const Ecosystem = "composer"

type Parser struct{}

func New() Parser { return Parser{} }

func (Parser) Ecosystem() string { return Ecosystem }

func (Parser) Detect(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "composer.json"))
	return err == nil && !info.IsDir()
}

func (Parser) Files() []string {
	return append([]string{"composer.json", "composer.lock", ".php-version"}, parser.DockerfileGlobs...)
}

type manifest struct {
	Require    parser.StringMap `json:"require"`
	RequireDev parser.StringMap `json:"require-dev"`
	Config     json.RawMessage  `json:"config"`
}

type lockfile struct {
	Packages    []lockPackage `json:"packages"`
	PackagesDev []lockPackage `json:"packages-dev"`
}

type lockPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (Parser) Parse(dir string) (*parser.Project, error) {
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("composer.json invalide : %w", err)
	}

	p := &parser.Project{Ecosystem: Ecosystem, Runtime: "php"}
	p.RuntimeVersion, p.RuntimeSource = phpVersion(dir, m)

	var lock *lockfile
	data, err = os.ReadFile(filepath.Join(dir, "composer.lock"))
	switch {
	case err == nil:
		lock = &lockfile{}
		if err := json.Unmarshal(data, lock); err != nil {
			return nil, fmt.Errorf("composer.lock invalide : %w", err)
		}
		p.HasLockfile = true
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}

	installed := map[string]string{}
	if lock != nil {
		for _, pkg := range append(append([]lockPackage(nil), lock.Packages...), lock.PackagesDev...) {
			installed[strings.ToLower(pkg.Name)] = normalize(pkg.Version)
		}
	}

	direct := map[string]bool{}
	add := func(deps parser.StringMap, dev bool) {
		for name, constraint := range deps {
			if isPlatform(name) {
				continue
			}
			name = strings.ToLower(name)
			if direct[name] {
				continue
			}
			direct[name] = true
			p.Dependencies = append(p.Dependencies, parser.Dependency{
				Name: name, Constraint: constraint, Installed: installed[name], Direct: true, Dev: dev,
			})
		}
	}
	add(m.Require, false)
	add(m.RequireDev, true)

	if lock != nil {
		transitive := func(pkgs []lockPackage, dev bool) {
			for _, pkg := range pkgs {
				name := strings.ToLower(pkg.Name)
				if direct[name] {
					continue
				}
				p.Dependencies = append(p.Dependencies, parser.Dependency{
					Name: name, Installed: normalize(pkg.Version), Dev: dev,
				})
			}
		}
		transitive(lock.Packages, false)
		transitive(lock.PackagesDev, true)
	}

	parser.SortDependencies(p.Dependencies)
	return p, nil
}

// phpVersion applique l'ordre de priorité du cahier des charges et renvoie la
// version avec la source retenue.
func phpVersion(dir string, m manifest) (version, source string) {
	if v := parser.ReadVersionFile(filepath.Join(dir, ".php-version")); v != "" {
		return v, ".php-version"
	}
	if v, file := parser.DockerImageVersion(dir, "php"); v != "" {
		return v, file
	}
	var config struct {
		Platform map[string]any `json:"platform"`
	}
	// config peut valoir [] dans un manifeste généré : l'erreur est ignorée.
	if json.Unmarshal(m.Config, &config) == nil {
		if s, ok := config.Platform["php"].(string); ok {
			if v := parser.FirstVersion(s); v != "" {
				return v, "composer.json (config.platform.php)"
			}
		}
	}
	if v := parser.FirstVersion(m.Require["php"]); v != "" {
		return v, "composer.json (require.php)"
	}
	return "", ""
}

// isPlatform reconnaît les paquets de plateforme (php, ext-json, lib-curl,
// composer-runtime-api…) : ils n'ont pas de vendor et n'existent pas sur
// Packagist.
func isPlatform(name string) bool { return !strings.Contains(name, "/") }

// normalize retire le préfixe « v » des versions (« v7.1.3 » → « 7.1.3 ») et
// laisse intactes les branches (« dev-main »).
func normalize(version string) string {
	if len(version) > 1 && version[0] == 'v' && version[1] >= '0' && version[1] <= '9' {
		return version[1:]
	}
	return version
}
