// Package goproxy interroge le proxy de modules Go.
package goproxy

import (
	"context"
	"strings"

	"golang.org/x/mod/module"

	"github.com/efcoubel/depdash/internal/registry"
)

const DefaultURL = "https://proxy.golang.org"

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

func (*Registry) Ecosystem() string { return "go" }

type response struct {
	Version string `json:"Version"`
}

// Latest renvoie la dernière version du module tel qu'il est nommé. Une
// majeure suivante vit sous un autre chemin (« …/v4 ») : elle n'est pas vue.
func (r *Registry) Latest(ctx context.Context, pkg string) (registry.Release, error) {
	// EscapePath valide le chemin et encode les majuscules : « Azure » → « !azure ».
	escaped, err := module.EscapePath(pkg)
	if err != nil {
		return registry.Release{}, registry.ErrNotFound
	}
	var resp response
	if err := r.client.GetJSON(ctx, r.base+"/"+escaped+"/@latest", nil, &resp); err != nil {
		return registry.Release{}, err
	}
	// Un module sans tag répond par une pseudo-version, rangée par Pick avec
	// les préversions.
	rel := registry.Pick([]string{resp.Version}, "")
	if rel.Latest == "" && len(rel.Prereleases) == 0 {
		return registry.Release{}, registry.ErrNotFound
	}
	rel.URL = sourceURL(pkg)
	return rel, nil
}

// sourceURL donne le dépôt quand le chemin du module le désigne directement,
// la page pkg.go.dev sinon.
func sourceURL(pkg string) string {
	parts := strings.Split(pkg, "/")
	if parts[0] == "github.com" && len(parts) >= 3 {
		return "https://" + strings.Join(parts[:3], "/")
	}
	return "https://pkg.go.dev/" + pkg
}
