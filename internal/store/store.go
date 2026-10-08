// Package store persiste l'inventaire et les caches dans SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/efcoubel/depdash/internal/parser"
	"github.com/efcoubel/depdash/internal/registry"
)

// ErrNotFound est renvoyée quand la ligne demandée n'existe pas.
var ErrNotFound = errors.New("introuvable")

type Store struct {
	db *sql.DB
}

// migrations est appliquée dans l'ordre ; PRAGMA user_version retient le
// nombre de migrations déjà passées. Ne jamais modifier une entrée existante.
var migrations = []string{
	`CREATE TABLE repos (
		id           INTEGER PRIMARY KEY,
		path         TEXT NOT NULL UNIQUE,
		name         TEXT NOT NULL,
		last_scan_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE projects (
		id                INTEGER PRIMARY KEY,
		repo_id           INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
		path              TEXT NOT NULL,
		ecosystem         TEXT NOT NULL,
		runtime           TEXT NOT NULL DEFAULT '',
		runtime_version   TEXT NOT NULL DEFAULT '',
		runtime_source    TEXT NOT NULL DEFAULT '',
		runtime_manual    INTEGER NOT NULL DEFAULT 0,
		framework         TEXT NOT NULL DEFAULT '',
		framework_version TEXT NOT NULL DEFAULT '',
		framework_manual  INTEGER NOT NULL DEFAULT 0,
		has_lockfile      INTEGER NOT NULL DEFAULT 0,
		manifest_hash     TEXT NOT NULL DEFAULT '',
		scan_error        TEXT NOT NULL DEFAULT '',
		UNIQUE (path, ecosystem)
	);
	CREATE TABLE dependencies (
		id                INTEGER PRIMARY KEY,
		project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		name              TEXT NOT NULL,
		"constraint"      TEXT NOT NULL DEFAULT '',
		installed_version TEXT NOT NULL DEFAULT '',
		is_direct         INTEGER NOT NULL DEFAULT 0,
		is_dev            INTEGER NOT NULL DEFAULT 0,
		is_manual         INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX dependencies_project ON dependencies(project_id);
	CREATE INDEX dependencies_name ON dependencies(name);
	CREATE TABLE package_cache (
		ecosystem      TEXT NOT NULL,
		name           TEXT NOT NULL,
		latest_version TEXT NOT NULL DEFAULT '',
		versions_json  TEXT NOT NULL DEFAULT '[]',
		repo_url       TEXT NOT NULL DEFAULT '',
		fetched_at     INTEGER NOT NULL,
		expires_at     INTEGER NOT NULL,
		PRIMARY KEY (ecosystem, name)
	);
	CREATE TABLE lifecycle_cache (
		product      TEXT NOT NULL,
		cycle        TEXT NOT NULL,
		latest       TEXT NOT NULL DEFAULT '',
		is_lts       INTEGER NOT NULL DEFAULT 0,
		release_date TEXT NOT NULL DEFAULT '',
		eol_active   TEXT NOT NULL DEFAULT '',
		eol_security TEXT NOT NULL DEFAULT '',
		fetched_at   INTEGER NOT NULL,
		PRIMARY KEY (product, cycle)
	);`,
}

// Open ouvre (et crée au besoin) la base dans dataDir.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return open("file:" + filepath.Join(dataDir, "depdash.db"))
}

func open(dsn string) (*Store, error) {
	dsn += "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d : %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// ---- repos et projets ----

// Project est un projet tel qu'enregistré, avec son repo.
type Project struct {
	ID               int64
	RepoID           int64
	RepoName         string
	RepoPath         string
	LastScanAt       time.Time
	Path             string
	Ecosystem        string
	Runtime          string
	RuntimeVersion   string
	RuntimeSource    string
	RuntimeManual    bool
	Framework        string
	FrameworkVersion string
	FrameworkManual  bool
	HasLockfile      bool
	ManifestHash     string
	ScanError        string
}

// UpsertRepo enregistre un repo et renvoie son identifiant.
func (s *Store) UpsertRepo(ctx context.Context, path, name string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO repos (path, name) VALUES (?, ?)
		 ON CONFLICT (path) DO UPDATE SET name = excluded.name
		 RETURNING id`, path, name).Scan(&id)
	return id, err
}

// TouchRepos date le dernier scan des repos donnés.
func (s *Store) TouchRepos(ctx context.Context, ids []int64, at time.Time) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE repos SET last_scan_at = ? WHERE id = ?`, unix(at), id); err != nil {
			return err
		}
	}
	return nil
}

const projectColumns = `p.id, p.repo_id, r.name, r.path, r.last_scan_at, p.path, p.ecosystem,
	p.runtime, p.runtime_version, p.runtime_source, p.runtime_manual,
	p.framework, p.framework_version, p.framework_manual,
	p.has_lockfile, p.manifest_hash, p.scan_error`

type scanner interface{ Scan(dest ...any) error }

func scanProject(row scanner) (Project, error) {
	var p Project
	var lastScan int64
	err := row.Scan(&p.ID, &p.RepoID, &p.RepoName, &p.RepoPath, &lastScan, &p.Path, &p.Ecosystem,
		&p.Runtime, &p.RuntimeVersion, &p.RuntimeSource, &p.RuntimeManual,
		&p.Framework, &p.FrameworkVersion, &p.FrameworkManual,
		&p.HasLockfile, &p.ManifestHash, &p.ScanError)
	p.LastScanAt = fromUnix(lastScan)
	return p, err
}

// Projects liste tous les projets, triés par repo puis par chemin.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+projectColumns+` FROM projects p JOIN repos r ON r.id = p.repo_id
		 ORDER BY r.name COLLATE NOCASE, p.path, p.ecosystem`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Project(ctx context.Context, id int64) (Project, error) {
	return s.projectWhere(ctx, `p.id = ?`, id)
}

// ProjectByKey retrouve un projet par son chemin et son écosystème.
func (s *Store) ProjectByKey(ctx context.Context, path, ecosystem string) (Project, error) {
	return s.projectWhere(ctx, `p.path = ? AND p.ecosystem = ?`, path, ecosystem)
}

func (s *Store) projectWhere(ctx context.Context, where string, args ...any) (Project, error) {
	p, err := scanProject(s.db.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects p JOIN repos r ON r.id = p.repo_id WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// SaveProject enregistre le résultat d'un parsing réussi et remplace les
// dépendances détectées. Les saisies manuelles (runtime, framework,
// dépendances) sont conservées : elles priment sur la détection.
func (s *Store) SaveProject(ctx context.Context, p Project, deps []parser.Dependency) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO projects (repo_id, path, ecosystem, runtime, runtime_version, runtime_source,
			framework, framework_version, has_lockfile, manifest_hash, scan_error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
		 ON CONFLICT (path, ecosystem) DO UPDATE SET
			repo_id = excluded.repo_id,
			runtime = excluded.runtime,
			runtime_version = CASE WHEN runtime_manual THEN runtime_version ELSE excluded.runtime_version END,
			runtime_source = CASE WHEN runtime_manual THEN runtime_source ELSE excluded.runtime_source END,
			framework = CASE WHEN framework_manual THEN framework ELSE excluded.framework END,
			framework_version = CASE WHEN framework_manual THEN framework_version ELSE excluded.framework_version END,
			has_lockfile = excluded.has_lockfile,
			manifest_hash = excluded.manifest_hash,
			scan_error = ''
		 RETURNING id`,
		p.RepoID, p.Path, p.Ecosystem, p.Runtime, p.RuntimeVersion, p.RuntimeSource,
		p.Framework, p.FrameworkVersion, p.HasLockfile, p.ManifestHash).Scan(&id)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM dependencies WHERE project_id = ? AND NOT is_manual`, id); err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO dependencies (project_id, name, "constraint", installed_version, is_direct, is_dev)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, d := range deps {
		if _, err := stmt.ExecContext(ctx, id, d.Name, d.Constraint, d.Installed, d.Direct, d.Dev); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// SaveProjectError marque un projet en erreur sans toucher à son dernier
// inventaire valide. L'empreinte est enregistrée pour que la correction du
// manifeste déclenche un nouveau parsing.
func (s *Store) SaveProjectError(ctx context.Context, repoID int64, path, ecosystem, hash, message string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO projects (repo_id, path, ecosystem, manifest_hash, scan_error) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (path, ecosystem) DO UPDATE SET
			repo_id = excluded.repo_id, manifest_hash = excluded.manifest_hash, scan_error = excluded.scan_error
		 RETURNING id`, repoID, path, ecosystem, hash, message).Scan(&id)
	return id, err
}

// DeleteProjectsExcept supprime les projets disparus du disque, puis les
// repos devenus vides.
func (s *Store) DeleteProjectsExcept(ctx context.Context, keep []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Une table temporaire évite la limite du nombre de paramètres SQL.
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS keep (id INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM keep`); err != nil {
		return err
	}
	for _, id := range keep {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO keep (id) VALUES (?)`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id NOT IN (SELECT id FROM keep)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM repos WHERE id NOT IN (SELECT repo_id FROM projects)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- dépendances ----

// Dependency est une dépendance d'un projet, complétée par ce que le cache
// sait de son paquet.
type Dependency struct {
	ID         int64
	ProjectID  int64
	Name       string
	Constraint string
	Installed  string
	Direct     bool
	Dev        bool
	Manual     bool

	Cached    bool // le registre a déjà répondu pour ce paquet
	Release   registry.Release
	FetchedAt time.Time
	ExpiresAt time.Time
}

const dependencyQuery = `SELECT d.id, d.project_id, d.name, d."constraint", d.installed_version,
		d.is_direct, d.is_dev, d.is_manual,
		c.latest_version, c.versions_json, c.repo_url, c.fetched_at, c.expires_at
	FROM dependencies d
	JOIN projects p ON p.id = d.project_id
	LEFT JOIN package_cache c ON c.ecosystem = p.ecosystem AND c.name = d.name`

func (s *Store) queryDependencies(ctx context.Context, where string, args ...any) ([]Dependency, error) {
	rows, err := s.db.QueryContext(ctx, dependencyQuery+" "+where+" ORDER BY d.name, d.installed_version", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dependency
	for rows.Next() {
		var d Dependency
		var latest, versions, url sql.NullString
		var fetched, expires sql.NullInt64
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Constraint, &d.Installed,
			&d.Direct, &d.Dev, &d.Manual, &latest, &versions, &url, &fetched, &expires); err != nil {
			return nil, err
		}
		if fetched.Valid {
			d.Cached = true
			d.Release.Latest = latest.String
			d.Release.URL = url.String
			_ = json.Unmarshal([]byte(versions.String), &d.Release.Prereleases)
			d.FetchedAt = fromUnix(fetched.Int64)
			d.ExpiresAt = fromUnix(expires.Int64)
		}
		out = append(out, d)
	}
	return overrideManual(out), rows.Err()
}

// overrideManual applique la règle « une saisie manuelle prime » : pour un
// projet donné, une dépendance manuelle masque la détection du même nom.
func overrideManual(deps []Dependency) []Dependency {
	type key struct {
		project int64
		name    string
	}
	manual := map[key]bool{}
	for _, d := range deps {
		if d.Manual {
			manual[key{d.ProjectID, d.Name}] = true
		}
	}
	if len(manual) == 0 {
		return deps
	}
	out := deps[:0]
	for _, d := range deps {
		if d.Manual || !manual[key{d.ProjectID, d.Name}] {
			out = append(out, d)
		}
	}
	return out
}

// Dependencies liste toutes les dépendances d'un projet.
func (s *Store) Dependencies(ctx context.Context, projectID int64) ([]Dependency, error) {
	return s.queryDependencies(ctx, `WHERE d.project_id = ?`, projectID)
}

// DirectDependencies liste les dépendances directes de tous les projets,
// regroupées par projet.
func (s *Store) DirectDependencies(ctx context.Context) (map[int64][]Dependency, error) {
	deps, err := s.queryDependencies(ctx, `WHERE d.is_direct`)
	if err != nil {
		return nil, err
	}
	out := map[int64][]Dependency{}
	for _, d := range deps {
		out[d.ProjectID] = append(out[d.ProjectID], d)
	}
	return out, nil
}

// PackageUsage liste les utilisations d'un paquet dans tous les projets.
func (s *Store) PackageUsage(ctx context.Context, ecosystem, name string) ([]Dependency, error) {
	return s.queryDependencies(ctx, `WHERE p.ecosystem = ? AND d.name = ?`, ecosystem, name)
}

// AddManualDependency ajoute ou remplace une dépendance saisie à la main.
func (s *Store) AddManualDependency(ctx context.Context, projectID int64, name, version string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM dependencies WHERE project_id = ? AND name = ? AND is_manual`, projectID, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO dependencies (project_id, name, installed_version, is_direct, is_manual) VALUES (?, ?, ?, 1, 1)`,
		projectID, name, version); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteManualDependency retire une saisie manuelle ; la détection reprend
// la main au prochain affichage.
func (s *Store) DeleteManualDependency(ctx context.Context, projectID, depID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM dependencies WHERE id = ? AND project_id = ? AND is_manual`, depID, projectID)
	return err
}

// SetManualRuntime fixe à la main la version du runtime. Une version vide
// annule la saisie : le prochain scan rétablit la détection.
func (s *Store) SetManualRuntime(ctx context.Context, projectID int64, version string) error {
	if version == "" {
		_, err := s.db.ExecContext(ctx,
			`UPDATE projects SET runtime_manual = 0, manifest_hash = '' WHERE id = ?`, projectID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET runtime_manual = 1, runtime_version = ?, runtime_source = 'saisie manuelle' WHERE id = ?`,
		version, projectID)
	return err
}

// SetManualFramework fixe à la main le framework et sa version. Un nom vide
// annule la saisie.
func (s *Store) SetManualFramework(ctx context.Context, projectID int64, name, version string) error {
	if name == "" {
		_, err := s.db.ExecContext(ctx,
			`UPDATE projects SET framework_manual = 0, manifest_hash = '' WHERE id = ?`, projectID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET framework_manual = 1, framework = ?, framework_version = ? WHERE id = ?`,
		name, version, projectID)
	return err
}

// ---- cache des paquets ----

// PackageKey identifie un paquet dans le cache.
type PackageKey struct{ Ecosystem, Name string }

// StalePackages liste les paquets utilisés dont le cache est absent ou
// expiré. directOnly restreint aux dépendances directes.
func (s *Store) StalePackages(ctx context.Context, now time.Time, directOnly bool) ([]PackageKey, error) {
	query := `SELECT DISTINCT p.ecosystem, d.name
		FROM dependencies d
		JOIN projects p ON p.id = d.project_id
		LEFT JOIN package_cache c ON c.ecosystem = p.ecosystem AND c.name = d.name
		WHERE (c.expires_at IS NULL OR c.expires_at <= ?)`
	if directOnly {
		query += ` AND d.is_direct`
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY p.ecosystem, d.name`, unix(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageKey
	for rows.Next() {
		var k PackageKey
		if err := rows.Scan(&k.Ecosystem, &k.Name); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// PutPackage enregistre la réponse d'un registre. Une Release vide mémorise
// un paquet introuvable, pour ne pas le redemander à chaque scan.
func (s *Store) PutPackage(ctx context.Context, key PackageKey, rel registry.Release, now time.Time, ttl time.Duration) error {
	versions, _ := json.Marshal(rel.Prereleases)
	if rel.Prereleases == nil {
		versions = []byte("[]")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO package_cache (ecosystem, name, latest_version, versions_json, repo_url, fetched_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (ecosystem, name) DO UPDATE SET
			latest_version = excluded.latest_version, versions_json = excluded.versions_json,
			repo_url = excluded.repo_url, fetched_at = excluded.fetched_at, expires_at = excluded.expires_at`,
		key.Ecosystem, key.Name, rel.Latest, string(versions), rel.URL, unix(now), unix(now.Add(ttl)))
	return err
}

// ---- cache des cycles de vie ----

// Lifecycle regroupe les cycles d'un produit et la date de leur lecture.
type Lifecycle struct {
	Cycles    []registry.Cycle
	FetchedAt time.Time
}

// Lifecycles renvoie tout le cache des cycles de vie, par produit.
func (s *Store) Lifecycles(ctx context.Context) (map[string]Lifecycle, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT product, cycle, latest, is_lts, release_date, eol_active, eol_security, fetched_at
		 FROM lifecycle_cache ORDER BY product, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Lifecycle{}
	for rows.Next() {
		var product string
		var c registry.Cycle
		var fetched int64
		if err := rows.Scan(&product, &c.Cycle, &c.Latest, &c.LTS, &c.ReleaseDate,
			&c.EOLActive, &c.EOLSecurity, &fetched); err != nil {
			return nil, err
		}
		lc := out[product]
		lc.Cycles = append(lc.Cycles, c)
		lc.FetchedAt = fromUnix(fetched)
		out[product] = lc
	}
	return out, rows.Err()
}

// PutLifecycle remplace les cycles d'un produit.
func (s *Store) PutLifecycle(ctx context.Context, product string, cycles []registry.Cycle, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM lifecycle_cache WHERE product = ?`, product); err != nil {
		return err
	}
	for _, c := range cycles {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO lifecycle_cache
				(product, cycle, latest, is_lts, release_date, eol_active, eol_security, fetched_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			product, c.Cycle, c.Latest, c.LTS, c.ReleaseDate,
			string(c.EOLActive), string(c.EOLSecurity), unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ProjectTitle donne le nom affiché d'un projet : le repo seul quand le
// projet est à sa racine, « repo / sous-dossier » dans un monorepo.
func (p Project) Title() string {
	if p.Path == p.RepoPath {
		return p.RepoName
	}
	sub := strings.TrimPrefix(p.Path, p.RepoPath+string(filepath.Separator))
	if p.RepoPath == "." {
		sub = p.Path
	}
	return p.RepoName + " / " + filepath.ToSlash(sub)
}
