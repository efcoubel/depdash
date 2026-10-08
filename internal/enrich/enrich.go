// Package enrich complète l'inventaire avec les données des registres et
// les range dans le cache. L'affichage ne lit que le cache : une API
// indisponible laisse la dernière valeur connue en place.
package enrich

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/efcoubel/depdash/internal/config"
	"github.com/efcoubel/depdash/internal/registry"
	"github.com/efcoubel/depdash/internal/store"
)

const (
	PackageTTL   = 6 * time.Hour
	LifecycleTTL = 24 * time.Hour
)

type Enricher struct {
	Store      *store.Store
	Registries map[string]registry.Registry
	Lifecycle  registry.Lifecycle
	Mapping    config.Mapping
	Log        *slog.Logger
	Now        func() time.Time
}

func New(st *store.Store, lifecycle registry.Lifecycle, mapping config.Mapping, log *slog.Logger, registries ...registry.Registry) *Enricher {
	e := &Enricher{Store: st, Lifecycle: lifecycle, Mapping: mapping, Log: log, Now: time.Now,
		Registries: map[string]registry.Registry{}}
	for _, r := range registries {
		e.Registries[r.Ecosystem()] = r
	}
	return e
}

// Run rafraîchit tout ce qui est absent du cache ou expiré : les cycles de
// vie, puis les dépendances directes, puis les sous-dépendances. Les
// directes passent d'abord pour que le tableau de bord soit utile au plus
// vite sur un cache froid.
func (e *Enricher) Run(ctx context.Context) error {
	if err := e.lifecycles(ctx); err != nil {
		return err
	}
	if err := e.packages(ctx, true); err != nil {
		return err
	}
	return e.packages(ctx, false)
}

func (e *Enricher) lifecycles(ctx context.Context) error {
	projects, err := e.Store.Projects(ctx)
	if err != nil {
		return err
	}
	cached, err := e.Store.Lifecycles(ctx)
	if err != nil {
		return err
	}
	now := e.Now()
	wanted := map[string]bool{}
	for _, p := range projects {
		if r, ok := e.Mapping.Runtime(p.Runtime); ok {
			wanted[r.Product] = true
		}
		if f, ok := e.Mapping.Framework(p.Framework); ok {
			wanted[f.Product] = true
		}
	}
	var wg sync.WaitGroup
	for product := range wanted {
		if lc, ok := cached[product]; ok && now.Sub(lc.FetchedAt) < LifecycleTTL {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cycles, err := e.Lifecycle.Cycles(ctx, product)
			if err != nil {
				e.Log.Warn("cycle de vie indisponible, cache conservé", "product", product, "error", err)
				return
			}
			if err := e.Store.PutLifecycle(ctx, product, cycles, now); err != nil {
				e.Log.Error("écriture du cache", "product", product, "error", err)
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func (e *Enricher) packages(ctx context.Context, directOnly bool) error {
	now := e.Now()
	stale, err := e.Store.StalePackages(ctx, now, directOnly)
	if err != nil {
		return err
	}
	var fetched, failed atomic.Int64
	var wg sync.WaitGroup
	for _, key := range stale {
		reg, ok := e.Registries[key.Ecosystem]
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Le client du registre borne lui-même la concurrence.
			rel, err := reg.Latest(ctx, key.Name)
			switch {
			case errors.Is(err, registry.ErrNotFound):
				rel = registry.Release{} // mémorisé comme introuvable
			case err != nil:
				failed.Add(1)
				e.Log.Debug("registre indisponible, cache conservé", "package", key.Name, "error", err)
				return
			}
			if err := e.Store.PutPackage(ctx, key, rel, now, PackageTTL); err != nil {
				e.Log.Error("écriture du cache", "package", key.Name, "error", err)
				return
			}
			fetched.Add(1)
		}()
	}
	wg.Wait()
	if len(stale) > 0 {
		e.Log.Info("paquets enrichis", "direct", directOnly, "fetched", fetched.Load(), "failed", failed.Load())
	}
	return ctx.Err()
}
