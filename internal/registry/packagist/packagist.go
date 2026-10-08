// Package packagist interroge les métadonnées Composer v2 de Packagist.
package packagist

import (
	"context"
	"regexp"
	"strings"

	"github.com/efcoubel/depdash/internal/registry"
)

const DefaultURL = "https://repo.packagist.org"

type Registry struct {
	client *registry.Client
	base   string
}

func New(client *registry.Client, baseURL string) *Registry {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Registry{client: client, base: strings.TrimRight(baseURL, "/")}
}

func (*Registry) Ecosystem() string { return "composer" }

// nameRe valide « vendor/paquet » : rien d'autre qu'un nom de paquet ne part
// vers Packagist.
var nameRe = regexp.MustCompile(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`)

type response struct {
	Packages map[string][]struct {
		Version string `json:"version"`
		Source  *struct {
			URL string `json:"url"`
		} `json:"source"`
	} `json:"packages"`
}

func (r *Registry) Latest(ctx context.Context, pkg string) (registry.Release, error) {
	pkg = strings.ToLower(pkg)
	if !nameRe.MatchString(pkg) {
		return registry.Release{}, registry.ErrNotFound
	}
	var resp response
	if err := r.client.GetJSON(ctx, r.base+"/p2/"+pkg+".json", nil, &resp); err != nil {
		return registry.Release{}, err
	}
	entries := resp.Packages[pkg]
	if len(entries) == 0 {
		return registry.Release{}, registry.ErrNotFound
	}
	versions := make([]string, 0, len(entries))
	var url string
	for _, e := range entries {
		versions = append(versions, e.Version)
		// Le format est « minifié » : seule la première entrée porte tous les
		// champs, les suivantes ne répètent que ce qui change.
		if url == "" && e.Source != nil {
			url = strings.TrimSuffix(e.Source.URL, ".git")
		}
	}
	rel := registry.Pick(versions, "")
	rel.URL = url
	return rel, nil
}
