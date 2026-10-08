package web

import (
	"context"
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/efcoubel/depdash/internal/config"
	"github.com/efcoubel/depdash/internal/registry"
	"github.com/efcoubel/depdash/internal/status"
	"github.com/efcoubel/depdash/internal/store"
)

// Les vues sont calculées à chaque requête à partir de la base : le statut
// n'est jamais stocké. Elles servent à la fois aux templates et à l'export
// JSON.

type ComponentView struct {
	Kind        string        `json:"kind"` // runtime ou framework
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Source      string        `json:"source,omitempty"`
	Manual      bool          `json:"manual"`
	Status      status.Status `json:"status"`
	Cycle       string        `json:"cycle,omitempty"`
	LTS         bool          `json:"lts"`
	Latest      string        `json:"latest,omitempty"`
	LatestLTS   string        `json:"latest_lts,omitempty"`
	EOLActive   string        `json:"eol_active,omitempty"`
	EOLSecurity string        `json:"eol_security,omitempty"`
	Guide       string        `json:"guide,omitempty"`
	FetchedAt   *time.Time    `json:"fetched_at,omitempty"`
	Timeline    *Timeline     `json:"-"`
}

type Timeline struct {
	Rows  []TimelineRow
	Today template.CSS
	Start string
	End   string
}

type TimelineRow struct {
	Cycle    string
	Tags     []string
	Current  bool
	Active   template.CSS // support actif
	Security template.CSS // correctifs de sécurité seuls
	Text     string
}

type DepView struct {
	ID         int64         `json:"-"`
	Name       string        `json:"name"`
	Constraint string        `json:"constraint,omitempty"`
	Installed  string        `json:"installed,omitempty"`
	Latest     string        `json:"latest,omitempty"`
	Direct     bool          `json:"direct"`
	Dev        bool          `json:"dev"`
	Manual     bool          `json:"manual"`
	Status     status.Status `json:"status"`
	Changelog  string        `json:"changelog,omitempty"`
	URL        string        `json:"-"`
	Stale      bool          `json:"stale"`
	FetchedAt  *time.Time    `json:"fetched_at,omitempty"`
}

type StatusCount struct {
	Status status.Status
	Count  int
}

type ProjectView struct {
	ID          int64          `json:"id"`
	Title       string         `json:"name"`
	Repo        string         `json:"repo"`
	Path        string         `json:"path"`
	Ecosystem   string         `json:"ecosystem"`
	HasLockfile bool           `json:"has_lockfile"`
	ScanError   string         `json:"scan_error,omitempty"`
	LastScanAt  *time.Time     `json:"last_scan_at,omitempty"`
	Worst       status.Status  `json:"status"`
	Runtime     *ComponentView `json:"runtime,omitempty"`
	Framework   *ComponentView `json:"framework,omitempty"`
	Counts      []StatusCount  `json:"-"`
	CountByKey  map[string]int `json:"counts"`
	Deps        []DepView      `json:"dependencies"`
}

type builder struct {
	mapping    config.Mapping
	warn       time.Duration
	now        time.Time
	lifecycles map[string]store.Lifecycle
}

func (s *Server) newBuilder(ctx context.Context) (*builder, error) {
	lifecycles, err := s.Store.Lifecycles(ctx)
	if err != nil {
		return nil, err
	}
	return &builder{mapping: s.Mapping, warn: s.EOLWarning, now: s.now(), lifecycles: lifecycles}, nil
}

// project assemble la vue d'un projet. Le résumé (compteurs, statut le plus
// grave) ne porte que sur les dépendances directes ; deps peut contenir
// aussi les sous-dépendances pour le tableau détaillé.
func (b *builder) project(p store.Project, deps []store.Dependency) ProjectView {
	v := ProjectView{
		ID: p.ID, Title: p.Title(), Repo: p.RepoName, Path: p.Path, Ecosystem: p.Ecosystem,
		HasLockfile: p.HasLockfile, ScanError: p.ScanError, LastScanAt: timePtr(p.LastScanAt),
		CountByKey: map[string]int{},
	}
	if p.RuntimeVersion != "" || p.Runtime != "" {
		product, _ := b.mapping.Runtime(p.Runtime)
		if product.Name == "" {
			product.Name = p.Runtime
		}
		v.Runtime = b.component("runtime", product, p.RuntimeVersion, p.RuntimeSource, p.RuntimeManual)
		v.Worst = max(v.Worst, v.Runtime.Status)
	}
	if p.Framework != "" {
		product, _ := b.mapping.Framework(p.Framework)
		product.Name = p.Framework
		v.Framework = b.component("framework", product, p.FrameworkVersion, "", p.FrameworkManual)
		v.Worst = max(v.Worst, v.Framework.Status)
	}

	counts := map[status.Status]int{}
	for _, d := range deps {
		dv := b.dependency(p.Ecosystem, d)
		v.Deps = append(v.Deps, dv)
		if d.Direct {
			counts[dv.Status]++
			v.Worst = max(v.Worst, dv.Status)
		}
	}
	for _, st := range status.All {
		if counts[st] > 0 {
			v.Counts = append(v.Counts, StatusCount{st, counts[st]})
			v.CountByKey[st.Key()] = counts[st]
		}
	}
	return v
}

func (b *builder) component(kind string, product config.Product, version, source string, manual bool) *ComponentView {
	c := &ComponentView{
		Kind: kind, Name: product.Name, Version: version, Source: source, Manual: manual,
		Status: status.Unknown, Guide: product.Guide,
	}
	cached, ok := b.lifecycles[product.Product]
	if !ok {
		return c
	}
	c.FetchedAt = timePtr(cached.FetchedAt)
	lc := status.Component(version, cached.Cycles, b.now, b.warn)
	c.Status = lc.Status
	if lc.Latest != nil {
		c.Latest = lc.Latest.Latest
	}
	if lc.LatestLTS != nil {
		c.LatestLTS = lc.LatestLTS.Latest
	}
	if lc.Current != nil {
		c.Cycle = lc.Current.Cycle
		c.LTS = lc.Current.LTS
		c.EOLActive = boundText(lc.Current.EOLActive)
		c.EOLSecurity = boundText(lc.Current.EOLSecurity)
	}
	c.Timeline = b.timeline(lc)
	return c
}

func boundText(b registry.Bound) string {
	if t, ok := b.Date(); ok {
		return t.Format("02/01/2006")
	}
	switch b {
	case "true":
		return "terminé"
	case "false":
		return "pas de date annoncée"
	}
	return ""
}

// timeline prépare la frise de support : une ligne pour le cycle utilisé,
// une pour la dernière LTS, une pour le dernier cycle.
func (b *builder) timeline(lc status.Lifecycle) *Timeline {
	type entry struct {
		cycle *registry.Cycle
		tags  []string
	}
	var entries []entry
	add := func(c *registry.Cycle, tag string) {
		if c == nil {
			return
		}
		for i := range entries {
			if entries[i].cycle == c {
				entries[i].tags = append(entries[i].tags, tag)
				return
			}
		}
		entries = append(entries, entry{c, []string{tag}})
	}
	add(lc.Current, "utilisée")
	add(lc.LatestLTS, "dernière LTS")
	add(lc.Latest, "dernière")
	if len(entries) == 0 {
		return nil
	}

	type span struct {
		release, active, end time.Time
		ok                   bool
	}
	spans := make([]span, len(entries))
	start, end := b.now, b.now
	for i, e := range entries {
		release, okRelease := registry.Bound(e.cycle.ReleaseDate).Date()
		security, okSecurity := e.cycle.EOLSecurity.Date()
		active, okActive := e.cycle.EOLActive.Date()
		if !okSecurity {
			security, okSecurity = active, okActive
		}
		if !okRelease || !okSecurity || !security.After(release) {
			continue
		}
		if !okActive || active.After(security) {
			active = security
		}
		if active.Before(release) {
			active = release
		}
		spans[i] = span{release, active, security, true}
		if release.Before(start) {
			start = release
		}
		if security.After(end) {
			end = security
		}
	}
	total := end.Sub(start).Hours()
	if total <= 0 {
		total = 1
	}
	pct := func(t time.Time) float64 { return t.Sub(start).Hours() / total * 100 }
	bar := func(from, to time.Time) template.CSS {
		return template.CSS(fmt.Sprintf("left:%.2f%%;width:%.2f%%", pct(from), pct(to)-pct(from)))
	}

	tl := &Timeline{
		Today: template.CSS(fmt.Sprintf("left:%.2f%%", pct(b.now))),
		Start: start.Format("2006"), End: end.Format("2006"),
	}
	for i, e := range entries {
		row := TimelineRow{Cycle: e.cycle.Cycle, Tags: e.tags, Current: e.cycle == lc.Current}
		if e.cycle.LTS {
			row.Tags = append(row.Tags, "LTS")
		}
		if sp := spans[i]; sp.ok {
			row.Active = bar(sp.release, sp.active)
			row.Security = bar(sp.active, sp.end)
			row.Text = fmt.Sprintf("sortie %s · support actif jusqu'au %s · sécurité jusqu'au %s",
				sp.release.Format("02/01/2006"), sp.active.Format("02/01/2006"), sp.end.Format("02/01/2006"))
		} else {
			row.Text = "dates de support non publiées"
			if e.cycle.EOLSecurity.Passed(b.now) {
				row.Text = "support terminé"
			} else if e.cycle.EOLSecurity == "false" {
				row.Text = "supportée, pas de date de fin annoncée"
			}
		}
		tl.Rows = append(tl.Rows, row)
	}
	return tl
}

func (b *builder) dependency(ecosystem string, d store.Dependency) DepView {
	v := DepView{
		ID: d.ID, Name: d.Name, Constraint: d.Constraint, Installed: d.Installed,
		Direct: d.Direct, Dev: d.Dev, Manual: d.Manual, Status: status.Unknown,
		URL: packageURL(ecosystem, d.Name),
	}
	if !d.Cached {
		return v
	}
	v.FetchedAt = timePtr(d.FetchedAt)
	v.Stale = !d.ExpiresAt.After(b.now)
	v.Status, v.Latest = status.Package(d.Installed, d.Constraint, d.Release)
	if d.Release.Latest != "" || len(d.Release.Prereleases) > 0 {
		v.Changelog = changelogURL(ecosystem, d.Name, d.Release.URL)
	}
	return v
}

func packageURL(ecosystem, name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/packages/" + url.PathEscape(ecosystem) + "/" + strings.Join(parts, "/")
}

// changelogURL pointe vers la page des versions publiées : les releases du
// dépôt quand il est sur GitHub, la page du registre sinon.
func changelogURL(ecosystem, name, repo string) string {
	if strings.HasPrefix(repo, "https://github.com/") {
		return repo + "/releases"
	}
	switch ecosystem {
	case "npm":
		return "https://www.npmjs.com/package/" + name + "?activeTab=versions"
	case "composer":
		return "https://packagist.org/packages/" + name
	}
	return repo
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// ---- filtres ----

type depFilter struct {
	Status string // clé de statut, vide = tous
	Scope  string // direct (défaut) ou all
	NoDev  bool
}

func (f depFilter) apply(deps []DepView) []DepView {
	out := make([]DepView, 0, len(deps))
	for _, d := range deps {
		if f.Scope != "all" && !d.Direct {
			continue
		}
		if f.NoDev && d.Dev {
			continue
		}
		if f.Status != "" && d.Status.Key() != f.Status {
			continue
		}
		out = append(out, d)
	}
	// Les plus graves d'abord, puis l'ordre alphabétique.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status > out[j].Status
		}
		return out[i].Name < out[j].Name
	})
	return out
}
