// Package registry définit le contrat des sources de versions officielles
// et le client HTTP qu'elles partagent.
package registry

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// ErrNotFound signale un paquet ou un produit inconnu de la source.
var ErrNotFound = errors.New("introuvable dans le registre")

// Release décrit l'état d'un paquet dans son registre.
type Release struct {
	Latest      string   // dernière version stable
	Prereleases []string // préversions plus récentes que Latest
	URL         string   // dépôt source, si le registre le fournit
}

// Registry donne la dernière version des paquets d'un écosystème.
type Registry interface {
	Ecosystem() string
	Latest(ctx context.Context, pkg string) (Release, error)
}

// Bound est une borne de support telle que publiée par endoflife.date :
// une date « 2027-01-31 », « true » (terminé, date inconnue), « false »
// (pas de fin annoncée) ou vide (information absente).
type Bound string

// Date renvoie la date de la borne quand elle en porte une.
func (b Bound) Date() (time.Time, bool) {
	t, err := time.Parse(time.DateOnly, string(b))
	return t, err == nil
}

// Passed indique si la borne est dépassée à l'instant now.
func (b Bound) Passed(now time.Time) bool {
	if b == "true" {
		return true
	}
	t, ok := b.Date()
	return ok && !now.Before(t)
}

// Cycle est une branche de versions d'un produit (PHP 8.3, Symfony 7.4,
// Node 22) avec ses dates de support.
type Cycle struct {
	Cycle       string
	Latest      string
	LTS         bool
	ReleaseDate string
	EOLActive   Bound // fin du support actif
	EOLSecurity Bound // fin des correctifs de sécurité
}

// Lifecycle donne les cycles de support d'un runtime ou d'un framework.
type Lifecycle interface {
	Cycles(ctx context.Context, product string) ([]Cycle, error)
}

// Pick choisit, parmi toutes les versions publiées, la dernière stable et
// les préversions qui la dépassent. Les branches (dev-*) et les versions
// illisibles sont ignorées. preferred, s'il est stable, prime sur le
// maximum calculé : c'est le « latest » déclaré par le registre.
func Pick(versions []string, preferred string) Release {
	type parsed struct {
		raw string
		v   *semver.Version
	}
	var stable *parsed
	var pre []parsed
	for _, raw := range versions {
		if strings.HasPrefix(raw, "dev-") || strings.HasSuffix(raw, "-dev") {
			continue
		}
		v, err := semver.NewVersion(raw)
		if err != nil {
			continue
		}
		p := parsed{strings.TrimPrefix(raw, "v"), v}
		if v.Prerelease() != "" {
			pre = append(pre, p)
			continue
		}
		if stable == nil || v.GreaterThan(stable.v) {
			stable = &p
		}
	}
	if v, err := semver.NewVersion(preferred); err == nil && v.Prerelease() == "" {
		stable = &parsed{strings.TrimPrefix(preferred, "v"), v}
	}
	var rel Release
	if stable != nil {
		rel.Latest = stable.raw
	}
	for _, p := range pre {
		if stable == nil || p.v.GreaterThan(stable.v) {
			rel.Prereleases = append(rel.Prereleases, p.raw)
		}
	}
	return rel
}
