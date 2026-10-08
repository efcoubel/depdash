// Package web sert le tableau de bord : pages HTML rendues côté serveur,
// htmx pour les filtres et le suivi des scans, export JSON.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/efcoubel/depdash/internal/config"
	"github.com/efcoubel/depdash/internal/scheduler"
	"github.com/efcoubel/depdash/internal/status"
	"github.com/efcoubel/depdash/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	Store      *store.Store
	Scheduler  *scheduler.Scheduler
	Mapping    config.Mapping
	EOLWarning time.Duration
	Log        *slog.Logger
	Now        func() time.Time

	pages map[string]*template.Template
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Handler assemble les routes. Les requêtes POST venant d'un autre site sont
// refusées : l'outil n'a pas d'authentification, une page tierce ne doit
// pas pouvoir déclencher d'action sur localhost.
func (s *Server) Handler() http.Handler {
	s.pages = map[string]*template.Template{}
	for _, page := range []string{"overview", "project", "package", "error"} {
		s.pages[page] = template.Must(template.New("layout.html").Funcs(funcs).
			ParseFS(assets, "templates/layout.html", "templates/"+page+".html"))
	}
	static, _ := fs.Sub(assets, "static")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /projects/{id}", s.project)
	mux.HandleFunc("GET /packages/{ecosystem}/{name...}", s.pkg)
	mux.HandleFunc("POST /scan", s.scan)
	mux.HandleFunc("POST /projects/{id}/scan", s.scanProject)
	mux.HandleFunc("POST /projects/{id}/manual", s.manual)
	mux.HandleFunc("GET /scan/status", s.scanStatus)
	mux.HandleFunc("GET /api/projects", s.apiProjects)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	return http.NewCrossOriginProtection().Handler(mux)
}

var funcs = template.FuncMap{
	"ago": func(t *time.Time) string {
		if t == nil {
			return "jamais"
		}
		d := time.Since(*t)
		switch {
		case d < time.Minute:
			return "à l'instant"
		case d < time.Hour:
			return fmt.Sprintf("il y a %d min", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("il y a %d h", int(d.Hours()))
		default:
			return fmt.Sprintf("il y a %d j", int(d.Hours()/24))
		}
	},
	"datetime": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Local().Format("02/01/2006 15:04")
	},
	"statuses": func() []status.Status { return status.All },
	"components": func(p ProjectView) []*ComponentView {
		var out []*ComponentView
		for _, c := range []*ComponentView{p.Runtime, p.Framework} {
			if c != nil {
				out = append(out, c)
			}
		}
		return out
	},
}

// page est le contexte commun à tous les templates.
type page struct {
	Title string
	Path  string // page courante, pour y revenir après une action
	Scan  scheduler.State
	Data  any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, code int, name, title string, data any) {
	var buf bytes.Buffer
	ctx := page{Title: title, Path: r.URL.RequestURI(), Scan: s.Scheduler.State(), Data: data}
	if err := s.pages[name].Execute(&buf, ctx); err != nil {
		s.Log.Error("rendu du template", "page", name, "error", err)
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	buf.WriteTo(w)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, "error", "Introuvable", "Cette page n'existe pas ou plus.")
		return
	}
	s.Log.Error("requête en échec", "error", err)
	s.render(w, r, http.StatusInternalServerError, "error", "Erreur", "Une erreur interne est survenue.")
}

// ---- vue d'ensemble ----

type overviewData struct {
	Projects   []ProjectView
	Total      int
	Totals     []StatusCount // nombre de projets par statut le plus grave
	Ecosystems []string
	Status     string
	Ecosystem  string
}

func (s *Server) views(r *http.Request) ([]ProjectView, error) {
	ctx := r.Context()
	b, err := s.newBuilder(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	deps, err := s.Store.DirectDependencies(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]ProjectView, 0, len(projects))
	for _, p := range projects {
		views = append(views, b.project(p, deps[p.ID]))
	}
	return views, nil
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	views, err := s.views(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data := overviewData{
		Total:     len(views),
		Status:    r.URL.Query().Get("status"),
		Ecosystem: r.URL.Query().Get("ecosystem"),
	}
	byStatus := map[status.Status]int{}
	ecosystems := map[string]bool{}
	for _, v := range views {
		byStatus[v.Worst]++
		ecosystems[v.Ecosystem] = true
		if data.Status != "" && v.Worst.Key() != data.Status {
			continue
		}
		if data.Ecosystem != "" && v.Ecosystem != data.Ecosystem {
			continue
		}
		data.Projects = append(data.Projects, v)
	}
	for _, st := range status.All {
		if byStatus[st] > 0 {
			data.Totals = append(data.Totals, StatusCount{st, byStatus[st]})
		}
	}
	for eco := range ecosystems {
		data.Ecosystems = append(data.Ecosystems, eco)
	}
	sort.Strings(data.Ecosystems)
	s.render(w, r, http.StatusOK, "overview", "Vue d'ensemble", data)
}

// ---- vue projet ----

type projectData struct {
	Project ProjectView
	Deps    []DepView
	Filter  depFilter
	Total   int
	Manual  []DepView
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	p, err := s.Store.Project(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	deps, err := s.Store.Dependencies(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	b, err := s.newBuilder(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	data := projectData{
		Project: b.project(p, deps),
		Filter:  depFilter{Status: q.Get("status"), Scope: q.Get("scope"), NoDev: q.Get("nodev") != ""},
	}
	if data.Filter.Scope != "all" {
		data.Filter.Scope = "direct"
	}
	data.Total = len(data.Project.Deps)
	data.Deps = data.Filter.apply(data.Project.Deps)
	for _, d := range data.Project.Deps {
		if d.Manual {
			data.Manual = append(data.Manual, d)
		}
	}
	s.render(w, r, http.StatusOK, "project", data.Project.Title, data)
}

// ---- vue transversale d'un paquet ----

type usage struct {
	Project store.Project
	Dep     DepView
}

type packageData struct {
	Ecosystem string
	Name      string
	Latest    string
	Changelog string
	Usages    []usage
}

func (s *Server) pkg(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := packageData{Ecosystem: r.PathValue("ecosystem"), Name: r.PathValue("name")}
	deps, err := s.Store.PackageUsage(ctx, data.Ecosystem, data.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(deps) == 0 {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	byID := map[int64]store.Project{}
	for _, p := range projects {
		byID[p.ID] = p
	}
	b := &builder{mapping: s.Mapping, warn: s.EOLWarning, now: s.now()}
	for _, d := range deps {
		dv := b.dependency(data.Ecosystem, d)
		data.Latest, data.Changelog = d.Release.Latest, dv.Changelog
		data.Usages = append(data.Usages, usage{byID[d.ProjectID], dv})
	}
	sort.SliceStable(data.Usages, func(i, j int) bool {
		return data.Usages[i].Project.Title() < data.Usages[j].Project.Title()
	})
	s.render(w, r, http.StatusOK, "package", data.Name, data)
}

// ---- scans ----

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	s.Scheduler.Trigger()
	s.back(w, r, "/")
}

func (s *Server) scanProject(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := s.Store.Project(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Scheduler.TriggerProject(id)
	s.back(w, r, "/projects/"+strconv.FormatInt(id, 10))
}

// back renvoie vers la page d'origine. Seul un chemin local est accepté.
func (s *Server) back(w http.ResponseWriter, r *http.Request, fallback string) {
	target := r.FormValue("back")
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.Contains(target, "\\") {
		target = fallback
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// scanStatus est interrogé par htmx tant qu'un scan tourne. Quand il se
// termine, l'en-tête HX-Refresh fait recharger la page avec les données à
// jour.
func (s *Server) scanStatus(w http.ResponseWriter, r *http.Request) {
	state := s.Scheduler.State()
	if !state.Running {
		w.Header().Set("HX-Refresh", "true")
	}
	var buf bytes.Buffer
	if err := s.pages["overview"].ExecuteTemplate(&buf, "scan-status", page{Scan: state}); err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// ---- saisie manuelle ----

func (s *Server) manual(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := s.Store.Project(ctx, id); err != nil {
		s.fail(w, r, err)
		return
	}
	field := func(name string) string {
		v := strings.TrimSpace(r.FormValue(name))
		if len(v) > 200 {
			v = v[:200]
		}
		return v
	}
	name, version := field("name"), field("version")

	var err error
	rescan := false
	switch r.FormValue("action") {
	case "runtime":
		err = s.Store.SetManualRuntime(ctx, id, version)
		rescan = version == ""
	case "framework":
		if version == "" {
			name = "" // sans version, la saisie est annulée
		}
		err = s.Store.SetManualFramework(ctx, id, name, version)
		rescan = true // nouveau produit possible : son cycle de vie est à charger
	case "dependency":
		if name != "" && version != "" {
			err = s.Store.AddManualDependency(ctx, id, name, version)
			rescan = true
		}
	case "delete-dependency":
		depID, _ := strconv.ParseInt(r.FormValue("dep"), 10, 64)
		err = s.Store.DeleteManualDependency(ctx, id, depID)
	default:
		http.Error(w, "action inconnue", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if rescan {
		s.Scheduler.TriggerProject(id)
	}
	http.Redirect(w, r, "/projects/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ---- API ----

func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request) {
	views, err := s.views(r)
	if err != nil {
		s.Log.Error("export JSON", "error", err)
		http.Error(w, `{"error":"erreur interne"}`, http.StatusInternalServerError)
		return
	}
	for i := range views {
		if views[i].Deps == nil {
			views[i].Deps = []DepView{}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(map[string]any{"generated_at": s.now().UTC(), "projects": views})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		http.Error(w, "base indisponible", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}
