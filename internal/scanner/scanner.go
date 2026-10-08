// Package scanner parcourt le dossier des projets et repère les manifestes.
// Il ne lit que des noms de fichiers : le contenu est l'affaire des parsers.
package scanner

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/efcoubel/depdash/internal/parser"
)

// Found est un projet détecté : un dossier et le parser qui le reconnaît.
// Un dossier contenant composer.json et package.json donne deux projets.
type Found struct {
	Dir      string // chemin absolu
	Path     string // chemin relatif à la racine scannée
	RepoPath string // chemin relatif du repo qui contient le projet
	RepoName string
	Parser   parser.Parser
}

type Scanner struct {
	Root     string
	MaxDepth int
	Parsers  []parser.Parser
	ignore   map[string]bool
}

func New(root string, maxDepth int, ignore []string, parsers ...parser.Parser) *Scanner {
	s := &Scanner{Root: filepath.Clean(root), MaxDepth: maxDepth, Parsers: parsers, ignore: map[string]bool{}}
	for _, name := range ignore {
		s.ignore[name] = true
	}
	return s
}

// Scan renvoie tous les projets sous la racine, triés par chemin.
func (s *Scanner) Scan() ([]Found, error) {
	if _, err := os.Stat(s.Root); err != nil {
		return nil, err
	}
	var found []Found
	s.walk(s.Root, 0, "", &found)
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Path != found[j].Path {
			return found[i].Path < found[j].Path
		}
		return found[i].Parser.Ecosystem() < found[j].Parser.Ecosystem()
	})
	return found, nil
}

// walk descend dans dir. repo est le chemin absolu du repo englobant le plus
// proche, vide tant qu'aucun .git n'a été croisé.
func (s *Scanner) walk(dir string, depth int, repo string, found *[]Found) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dossier illisible : on continue ailleurs
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			repo = dir
			break
		}
	}
	for _, p := range s.Parsers {
		if p.Detect(dir) {
			*found = append(*found, s.found(dir, repo, p))
		}
	}
	if depth >= s.MaxDepth {
		return
	}
	for _, e := range entries {
		// Les liens symboliques ne sont pas suivis : pas de boucle, pas de
		// sortie du dossier monté.
		if !e.IsDir() || s.ignore[e.Name()] {
			continue
		}
		s.walk(filepath.Join(dir, e.Name()), depth+1, repo, found)
	}
}

func (s *Scanner) found(dir, repo string, p parser.Parser) Found {
	rel := s.rel(dir)
	if repo == "" {
		// Sans .git, le dossier de premier niveau sous la racine fait office
		// de repo.
		repo = s.Root
		if rel != "." {
			repo = filepath.Join(s.Root, strings.SplitN(rel, string(filepath.Separator), 2)[0])
		}
	}
	return Found{Dir: dir, Path: rel, RepoPath: s.rel(repo), RepoName: filepath.Base(repo), Parser: p}
}

func (s *Scanner) rel(path string) string {
	rel, err := filepath.Rel(s.Root, path)
	if err != nil {
		return path
	}
	return rel
}
