// Package npm interroge le registre npm.
package npm

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/efcoubel/depdash/internal/registry"
)

const DefaultURL = "https://registry.npmjs.org"

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

func (*Registry) Ecosystem() string { return "npm" }

// nameRe valide un nom de paquet npm, avec ou sans scope.
var nameRe = regexp.MustCompile(`^(@[a-z0-9~][a-z0-9._~-]*/)?[a-zA-Z0-9~][a-zA-Z0-9._~-]*$`)

// Le format abrégé évite de télécharger les README et scripts de chaque
// version : seuls dist-tags et la liste des versions servent ici.
var header = http.Header{"Accept": {"application/vnd.npm.install-v1+json"}}

type response struct {
	DistTags map[string]string `json:"dist-tags"`
	// Seules les clés servent : struct{} fait ignorer au décodeur les
	// métadonnées de chaque version au lieu de les garder en mémoire.
	Versions map[string]struct{} `json:"versions"`
}

func (r *Registry) Latest(ctx context.Context, pkg string) (registry.Release, error) {
	if !nameRe.MatchString(pkg) {
		return registry.Release{}, registry.ErrNotFound
	}
	var resp response
	// PathEscape encode le « / » d'un scope : @scope/nom → @scope%2Fnom.
	if err := r.client.GetJSON(ctx, r.base+"/"+url.PathEscape(pkg), header, &resp); err != nil {
		return registry.Release{}, err
	}
	versions := make([]string, 0, len(resp.Versions))
	for v := range resp.Versions {
		versions = append(versions, v)
	}
	rel := registry.Pick(versions, resp.DistTags["latest"])
	if rel.Latest == "" && len(rel.Prereleases) == 0 {
		return registry.Release{}, registry.ErrNotFound
	}
	rel.URL = "https://www.npmjs.com/package/" + pkg
	return rel, nil
}
