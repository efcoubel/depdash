// Package scheduler orchestre les scans : périodiques ou à la demande, un
// seul à la fois.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/efcoubel/depdash/internal/config"
	"github.com/efcoubel/depdash/internal/enrich"
	"github.com/efcoubel/depdash/internal/parser"
	"github.com/efcoubel/depdash/internal/scanner"
	"github.com/efcoubel/depdash/internal/store"
)

// State décrit l'activité du planificateur pour l'interface.
type State struct {
	Running   bool
	LastRun   time.Time
	LastError string
}

type Scheduler struct {
	Scanner  *scanner.Scanner
	Store    *store.Store
	Enricher *enrich.Enricher // nil : pas d'enrichissement (mode hors ligne)
	Mapping  config.Mapping
	Log      *slog.Logger

	mu    sync.Mutex
	state State
	ctx   context.Context
}

// Start lance un scan immédiat puis un scan à chaque intervalle, jusqu'à
// l'annulation de ctx.
func (s *Scheduler) Start(ctx context.Context, interval time.Duration) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	go func() {
		s.Trigger()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Trigger()
			}
		}
	}()
}

func (s *Scheduler) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Trigger lance un scan complet en arrière-plan. Renvoie false si un scan
// est déjà en cours.
func (s *Scheduler) Trigger() bool {
	return s.launch(func(ctx context.Context) error { return s.ScanAll(ctx, false) })
}

// TriggerProject lance le rescan forcé d'un seul projet en arrière-plan.
func (s *Scheduler) TriggerProject(id int64) bool {
	return s.launch(func(ctx context.Context) error { return s.ScanProject(ctx, id) })
}

func (s *Scheduler) launch(run func(context.Context) error) bool {
	s.mu.Lock()
	if s.state.Running {
		s.mu.Unlock()
		return false
	}
	s.state.Running = true
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		start := time.Now()
		err := run(ctx)
		// Un scan à froid décode des mégaoctets de JSON : la mémoire est
		// rendue au système plutôt que gardée en réserve jusqu'au prochain.
		debug.FreeOSMemory()
		s.mu.Lock()
		s.state = State{LastRun: time.Now()}
		if err != nil && !errors.Is(err, context.Canceled) {
			s.state.LastError = err.Error()
			s.Log.Error("scan en échec", "error", err)
		} else {
			s.Log.Info("scan terminé", "duration", time.Since(start).Round(time.Millisecond))
		}
		s.mu.Unlock()
	}()
	return true
}

// ScanAll détecte les projets, reparse ceux dont les manifestes ont changé
// (tous si force), retire ceux qui ont disparu, puis enrichit.
func (s *Scheduler) ScanAll(ctx context.Context, force bool) error {
	found, err := s.Scanner.Scan()
	if err != nil {
		return fmt.Errorf("parcours de %s : %w", s.Scanner.Root, err)
	}
	now := time.Now()
	var keep []int64
	repos := map[int64]bool{}
	var parsed, unchanged int
	for _, f := range found {
		if err := ctx.Err(); err != nil {
			return err
		}
		repoID, err := s.Store.UpsertRepo(ctx, f.RepoPath, f.RepoName)
		if err != nil {
			return err
		}
		repos[repoID] = true
		id, changed, err := s.scanOne(ctx, f, repoID, force)
		if err != nil {
			return err
		}
		keep = append(keep, id)
		if changed {
			parsed++
		} else {
			unchanged++
		}
	}
	if err := s.Store.DeleteProjectsExcept(ctx, keep); err != nil {
		return err
	}
	ids := make([]int64, 0, len(repos))
	for id := range repos {
		ids = append(ids, id)
	}
	if err := s.Store.TouchRepos(ctx, ids, now); err != nil {
		return err
	}
	s.Log.Info("inventaire à jour", "projects", len(found), "parsed", parsed, "unchanged", unchanged)
	return s.enrich(ctx)
}

// ScanProject reparse un projet déjà connu, sans condition d'empreinte.
func (s *Scheduler) ScanProject(ctx context.Context, id int64) error {
	p, err := s.Store.Project(ctx, id)
	if err != nil {
		return err
	}
	for _, ps := range s.Scanner.Parsers {
		if ps.Ecosystem() != p.Ecosystem {
			continue
		}
		f := scanner.Found{
			Dir: filepath.Join(s.Scanner.Root, p.Path), Path: p.Path,
			RepoPath: p.RepoPath, RepoName: p.RepoName, Parser: ps,
		}
		if !ps.Detect(f.Dir) {
			return fmt.Errorf("%s : manifeste introuvable, lancez un scan global", p.Path)
		}
		if _, _, err := s.scanOne(ctx, f, p.RepoID, true); err != nil {
			return err
		}
		if err := s.Store.TouchRepos(ctx, []int64{p.RepoID}, time.Now()); err != nil {
			return err
		}
		return s.enrich(ctx)
	}
	return fmt.Errorf("écosystème inconnu : %s", p.Ecosystem)
}

// scanOne parse un projet si nécessaire. Un manifeste invalide marque le
// projet en erreur ; seule une erreur de base interrompt le scan.
func (s *Scheduler) scanOne(ctx context.Context, f scanner.Found, repoID int64, force bool) (id int64, changed bool, err error) {
	eco := f.Parser.Ecosystem()
	hash, hashErr := parser.Hash(f.Dir, f.Parser.Files())

	existing, err := s.Store.ProjectByKey(ctx, f.Path, eco)
	known := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, false, err
	}
	if known && !force && hashErr == nil && existing.ManifestHash == hash &&
		existing.ScanError == "" && existing.RepoID == repoID {
		return existing.ID, false, nil
	}

	var parsed *parser.Project
	parseErr := hashErr
	if parseErr == nil {
		parsed, parseErr = f.Parser.Parse(f.Dir)
	}
	if parseErr != nil {
		s.Log.Warn("projet en erreur", "path", f.Path, "ecosystem", eco, "error", parseErr)
		id, err := s.Store.SaveProjectError(ctx, repoID, f.Path, eco, hash, parseErr.Error())
		return id, true, err
	}

	p := store.Project{
		RepoID: repoID, Path: f.Path, Ecosystem: eco,
		Runtime: parsed.Runtime, RuntimeVersion: parsed.RuntimeVersion, RuntimeSource: parsed.RuntimeSource,
		HasLockfile: parsed.HasLockfile, ManifestHash: hash,
	}
	p.Framework, p.FrameworkVersion = DetectFramework(s.Mapping, eco, parsed.Dependencies)
	id, err = s.Store.SaveProject(ctx, p, parsed.Dependencies)
	return id, true, err
}

func (s *Scheduler) enrich(ctx context.Context) error {
	if s.Enricher == nil {
		return nil
	}
	return s.Enricher.Run(ctx)
}

// DetectFramework déduit le framework des dépendances directes, selon
// l'ordre de priorité de la table de correspondance.
func DetectFramework(m config.Mapping, ecosystem string, deps []parser.Dependency) (name, version string) {
	for _, rule := range m.Frameworks {
		if rule.Ecosystem != ecosystem {
			continue
		}
		for _, d := range deps {
			if !d.Direct || d.Name != rule.Package {
				continue
			}
			if d.Installed != "" {
				return rule.Name, d.Installed
			}
			return rule.Name, parser.FirstVersion(d.Constraint)
		}
	}
	return "", ""
}
