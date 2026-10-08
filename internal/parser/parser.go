// Package parser définit le contrat commun aux écosystèmes : lire les
// manifestes d'un dossier et en tirer un inventaire, sans jamais y écrire.
package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Dependency est une dépendance telle que déclarée et, si un lockfile existe,
// telle qu'installée.
type Dependency struct {
	Name       string
	Constraint string // contrainte du manifeste, vide pour une sous-dépendance
	Installed  string // version exacte du lockfile, vide sans lockfile
	Direct     bool
	Dev        bool
}

// Project est l'inventaire d'un projet pour un écosystème donné.
type Project struct {
	Ecosystem      string
	Runtime        string // « php », « node »
	RuntimeVersion string
	RuntimeSource  string // d'où vient la version : « .php-version », « Dockerfile »…
	HasLockfile    bool
	Dependencies   []Dependency
}

// Parser lit les manifestes d'un écosystème.
type Parser interface {
	Ecosystem() string
	Detect(dir string) bool
	Parse(dir string) (*Project, error)
	// Files liste les motifs (au sens de filepath.Glob, relatifs au dossier du
	// projet) des fichiers dont le contenu influence Parse. Leur empreinte
	// permet de ne pas reparser un projet inchangé.
	Files() []string
}

// DockerfileGlobs liste les emplacements usuels d'un Dockerfile de projet.
var DockerfileGlobs = []string{
	"Dockerfile", "Dockerfile.*", "docker/Dockerfile", "docker/*/Dockerfile", ".docker/*/Dockerfile",
}

// Hash calcule l'empreinte SHA-256 des fichiers correspondant aux motifs.
// Le nom de chaque fichier entre dans l'empreinte : un fichier ajouté ou
// supprimé la modifie, même vide.
func Hash(dir string, patterns []string) (string, error) {
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return "", err
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, file)
		fmt.Fprintf(h, "%s\x00", rel)
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var versionRe = regexp.MustCompile(`\d+(?:\.\d+){0,2}`)

// FirstVersion extrait le premier numéro de version d'une chaîne :
// « ^8.2 » → « 8.2 », « v20.11.0 » → « 20.11.0 », « lts/iron » → « ».
func FirstVersion(s string) string { return versionRe.FindString(s) }

// ReadVersionFile lit un fichier d'une ligne comme .php-version ou .nvmrc.
func ReadVersionFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return FirstVersion(strings.TrimSpace(string(data)))
}

// DockerImageVersion cherche « FROM <image>:X » dans les Dockerfile du projet
// et renvoie la version ainsi que le fichier où elle a été trouvée.
func DockerImageVersion(dir, image string) (version, file string) {
	re := regexp.MustCompile(`(?im)^\s*FROM\s+(?:--platform=\S+\s+)?(?:\S+/)?` + regexp.QuoteMeta(image) + `:v?(\d+(?:\.\d+){0,2})`)
	for _, pattern := range DockerfileGlobs {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		sort.Strings(matches)
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if m := re.FindSubmatch(data); m != nil {
				rel, _ := filepath.Rel(dir, path)
				return string(m[1]), rel
			}
		}
	}
	return "", ""
}

// StringMap décode un objet JSON de chaînes en tolérant les formes que l'on
// rencontre dans les manifestes réels : tableau vide à la place d'un objet
// vide (sérialisation PHP), valeurs non textuelles.
type StringMap map[string]string

func (m *StringMap) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		var list []any
		if json.Unmarshal(data, &list) == nil {
			*m = StringMap{}
			return nil
		}
		return err
	}
	out := make(StringMap, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	*m = out
	return nil
}

// SortDependencies trie par nom puis par version, pour un résultat stable.
func SortDependencies(deps []Dependency) {
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].Name != deps[j].Name {
			return deps[i].Name < deps[j].Name
		}
		return deps[i].Installed < deps[j].Installed
	})
}
