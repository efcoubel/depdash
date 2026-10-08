// Package config lit la configuration depuis les variables d'environnement.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultIgnore liste les dossiers jamais parcourus par le scanner.
var DefaultIgnore = []string{"vendor", "node_modules", ".git", "var", "dist", "build"}

type Config struct {
	ProjectsDir  string
	DataDir      string
	Port         int
	ScanInterval time.Duration
	MaxDepth     int
	EOLWarning   time.Duration
	Ignore       []string
	MappingFile  string
}

// Load lit la configuration depuis l'environnement du processus.
func Load() (Config, error) { return FromEnv(os.Getenv) }

// FromEnv construit la configuration à partir d'une fonction de lecture,
// ce qui permet de la tester sans toucher à l'environnement réel.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		ProjectsDir:  "/projects",
		DataDir:      "/data",
		Port:         8080,
		ScanInterval: 6 * time.Hour,
		MaxDepth:     4,
		EOLWarning:   180 * 24 * time.Hour,
		Ignore:       append([]string(nil), DefaultIgnore...),
	}
	if v := getenv("DEPDASH_PROJECTS_DIR"); v != "" {
		c.ProjectsDir = v
	}
	if v := getenv("DEPDASH_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := getenv("DEPDASH_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("DEPDASH_PORT invalide : %q", v)
		}
		c.Port = n
	}
	if v := getenv("DEPDASH_SCAN_INTERVAL"); v != "" {
		d, err := ParseDuration(v)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("DEPDASH_SCAN_INTERVAL invalide : %q", v)
		}
		c.ScanInterval = d
	}
	if v := getenv("DEPDASH_MAX_DEPTH"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, fmt.Errorf("DEPDASH_MAX_DEPTH invalide : %q", v)
		}
		c.MaxDepth = n
	}
	if v := getenv("DEPDASH_EOL_WARNING"); v != "" {
		d, err := ParseDuration(v)
		if err != nil || d < 0 {
			return c, fmt.Errorf("DEPDASH_EOL_WARNING invalide : %q", v)
		}
		c.EOLWarning = d
	}
	for _, name := range strings.Split(getenv("DEPDASH_IGNORE"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			c.Ignore = append(c.Ignore, name)
		}
	}
	c.MappingFile = getenv("DEPDASH_MAPPING_FILE")
	if c.MappingFile == "" {
		c.MappingFile = filepath.Join(c.DataDir, "mapping.json")
	}
	return c, nil
}

// ParseDuration étend time.ParseDuration avec les suffixes « d » (jours) et
// « w » (semaines) : « 180d », « 2w », « 6h ».
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if num, ok := strings.CutSuffix(s, suffix); ok {
			n, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, fmt.Errorf("durée invalide : %q", s)
			}
			return time.Duration(n * float64(unit)), nil
		}
	}
	return time.ParseDuration(s)
}
