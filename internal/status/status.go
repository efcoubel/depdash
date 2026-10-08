// Package status calcule le statut d'un composant. Rien n'est stocké : le
// statut est recalculé à chaque affichage, pour qu'un changement de seuil
// s'applique immédiatement.
package status

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/efcoubel/depdash/internal/registry"
)

// Status est ordonné par gravité croissante : le plus grand l'emporte quand
// on résume un projet.
type Status int

const (
	UpToDate Status = iota
	Unknown
	MinorUpdate
	MajorAvailable
	EOLSoon
	Unsupported
)

// All liste les statuts du plus grave au moins grave, pour l'affichage.
var All = []Status{Unsupported, EOLSoon, MajorAvailable, MinorUpdate, Unknown, UpToDate}

func (s Status) Label() string {
	switch s {
	case UpToDate:
		return "À jour"
	case MinorUpdate:
		return "Mise à jour mineure"
	case MajorAvailable:
		return "Majeure disponible"
	case EOLSoon:
		return "Fin de support proche"
	case Unsupported:
		return "Non supportée"
	default:
		return "Inconnu"
	}
}

// Key est l'identifiant stable du statut : classes CSS, filtres, JSON.
func (s Status) Key() string {
	switch s {
	case UpToDate:
		return "ok"
	case MinorUpdate:
		return "minor"
	case MajorAvailable:
		return "major"
	case EOLSoon:
		return "eol-soon"
	case Unsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

func (s Status) MarshalJSON() ([]byte, error) { return json.Marshal(s.Key()) }

// Package calcule le statut d'une dépendance. installed est la version du
// lockfile ; sans lockfile, la contrainte du manifeste sert d'approximation.
// Le second résultat est la version de référence retenue : la dernière
// stable, ou la dernière préversion si la version installée en est une.
func Package(installed, constraint string, rel registry.Release) (Status, string) {
	latest := rel.Latest
	if installed == "" {
		return fromConstraint(constraint, latest), latest
	}
	iv, err := semver.NewVersion(installed)
	if err != nil {
		return Unknown, latest // branche (dev-main), alias, URL git…
	}
	lv, _ := semver.NewVersion(latest)
	if iv.Prerelease() != "" || lv == nil {
		// Les préversions ne comptent que si l'on en utilise déjà une, ou si
		// le paquet n'a jamais publié de version stable.
		for _, raw := range rel.Prereleases {
			if pv, err := semver.NewVersion(raw); err == nil && (lv == nil || pv.GreaterThan(lv)) {
				lv, latest = pv, raw
			}
		}
	}
	if lv == nil {
		return Unknown, latest
	}
	return compare(iv, lv), latest
}

func compare(installed, latest *semver.Version) Status {
	switch {
	case !installed.LessThan(latest):
		return UpToDate
	case installed.Major() != latest.Major():
		return MajorAvailable
	case installed.Minor() != latest.Minor():
		return MinorUpdate
	default:
		return UpToDate // même mineure : un correctif de retard reste « à jour »
	}
}

var (
	stabilityRe = regexp.MustCompile(`@(dev|alpha|beta|rc|stable)\b`)
	orRe        = regexp.MustCompile(`\s*\|\|?\s*`)
	baseRe      = regexp.MustCompile(`\d+(?:\.\d+){0,2}`)
)

// fromConstraint estime le statut quand seule la contrainte est connue : à
// jour si elle accepte la dernière version, en retard sinon.
func fromConstraint(constraint, latest string) Status {
	lv, err := semver.NewVersion(latest)
	if err != nil || strings.TrimSpace(constraint) == "" {
		return Unknown
	}
	// Composer accepte « | » comme « || » et des drapeaux de stabilité.
	normalized := orRe.ReplaceAllString(stabilityRe.ReplaceAllString(constraint, ""), " || ")
	if c, err := semver.NewConstraint(normalized); err == nil && c.Check(lv) {
		return UpToDate
	}
	// La plus haute version citée par la contrainte donne l'ordre de
	// grandeur du retard.
	var base *semver.Version
	for _, raw := range baseRe.FindAllString(constraint, -1) {
		if v, err := semver.NewVersion(raw); err == nil && (base == nil || v.GreaterThan(base)) {
			base = v
		}
	}
	if base == nil {
		return Unknown
	}
	if base.Major() != lv.Major() {
		if base.GreaterThan(lv) {
			return UpToDate
		}
		return MajorAvailable
	}
	return MinorUpdate
}

// Lifecycle est le résultat du calcul pour un runtime ou un framework.
type Lifecycle struct {
	Status    Status
	Current   *registry.Cycle // cycle de la version utilisée
	Latest    *registry.Cycle // cycle le plus récent
	LatestLTS *registry.Cycle // LTS la plus récente
}

// Component calcule le statut d'un runtime ou d'un framework à partir de ses
// cycles de support. warn est le seuil « fin de support proche ».
func Component(version string, cycles []registry.Cycle, now time.Time, warn time.Duration) Lifecycle {
	lc := Lifecycle{Status: Unknown}
	for i := range cycles {
		c := &cycles[i]
		if lc.Latest == nil || newer(c.Cycle, lc.Latest.Cycle) {
			lc.Latest = c
		}
		if c.LTS && (lc.LatestLTS == nil || newer(c.Cycle, lc.LatestLTS.Cycle)) {
			lc.LatestLTS = c
		}
	}
	lc.Current = match(version, cycles)
	if lc.Current == nil {
		return lc
	}

	cur := lc.Current
	if cur.EOLSecurity.Passed(now) {
		lc.Status = Unsupported
		return lc
	}
	if end, ok := cur.EOLSecurity.Date(); ok && end.Sub(now) < warn {
		lc.Status = EOLSoon
		return lc
	}

	// Dans le cycle le plus récent, ou dans une LTS encore supportée, on
	// n'est pas « en retard » : seul un écart de mineure à l'intérieur du
	// cycle est signalé (Node 22.1 quand 22.11 existe).
	if cur == lc.Latest || cur.LTS {
		lc.Status = UpToDate
		iv, err1 := semver.NewVersion(version)
		lv, err2 := semver.NewVersion(cur.Latest)
		if err1 == nil && err2 == nil && strings.Count(version, ".") >= 1 &&
			iv.Major() == lv.Major() && iv.Minor() < lv.Minor() {
			lc.Status = MinorUpdate
		}
		return lc
	}

	lc.Status = MajorAvailable
	cv, err1 := semver.NewVersion(cur.Cycle)
	lv, err2 := semver.NewVersion(lc.Latest.Cycle)
	if err1 == nil && err2 == nil && cv.Major() == lv.Major() {
		lc.Status = MinorUpdate
	}
	return lc
}

// match retrouve le cycle d'une version : « 8.3.12 » → cycle « 8.3 »,
// « 22.11.0 » → cycle « 22 ». Le cycle le plus précis l'emporte.
func match(version string, cycles []registry.Cycle) *registry.Cycle {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		return nil
	}
	var best *registry.Cycle
	for i := range cycles {
		c := &cycles[i]
		if version != c.Cycle && !strings.HasPrefix(version, c.Cycle+".") {
			continue
		}
		if best == nil || len(c.Cycle) > len(best.Cycle) {
			best = c
		}
	}
	return best
}

func newer(a, b string) bool {
	av, err1 := semver.NewVersion(a)
	bv, err2 := semver.NewVersion(b)
	if err1 != nil || err2 != nil {
		return err1 == nil // un cycle lisible passe devant un cycle illisible
	}
	return av.GreaterThan(bv)
}
