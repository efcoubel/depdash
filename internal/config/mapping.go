package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

//go:embed mapping.json
var defaultMapping []byte

// Product relie un runtime ou un framework à son produit endoflife.date.
type Product struct {
	Name    string `json:"name"`
	Product string `json:"product"`
	Guide   string `json:"guide"`
}

// FrameworkRule déduit un framework de la présence d'un paquet.
type FrameworkRule struct {
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	Product
}

// Mapping est la table de correspondance modifiable entre les paquets
// détectés et les produits endoflife.date. L'ordre des frameworks fixe la
// priorité : Next.js passe avant React, Nuxt avant Vue.
type Mapping struct {
	Runtimes   map[string]Product `json:"runtimes"`
	Frameworks []FrameworkRule    `json:"frameworks"`
}

// DefaultMapping renvoie la table embarquée dans le binaire.
func DefaultMapping() Mapping {
	var m Mapping
	if err := json.Unmarshal(defaultMapping, &m); err != nil {
		panic("mapping.json embarqué invalide : " + err.Error())
	}
	return m
}

// LoadMapping lit la table depuis path ; si le fichier n'existe pas, la table
// embarquée est utilisée.
func LoadMapping(path string) (Mapping, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultMapping(), nil
	}
	if err != nil {
		return Mapping{}, err
	}
	var m Mapping
	if err := json.Unmarshal(data, &m); err != nil {
		return Mapping{}, fmt.Errorf("%s : %w", path, err)
	}
	return m, nil
}

// Runtime renvoie le produit associé à un runtime (« php », « node »).
func (m Mapping) Runtime(runtime string) (Product, bool) {
	p, ok := m.Runtimes[runtime]
	return p, ok
}

// Framework retrouve un framework par son nom affiché, sans tenir compte de
// la casse, pour que les saisies manuelles profitent aussi du cycle de vie.
func (m Mapping) Framework(name string) (Product, bool) {
	for _, f := range m.Frameworks {
		if strings.EqualFold(f.Name, name) || strings.EqualFold(f.Product.Product, name) {
			return f.Product, true
		}
	}
	return Product{}, false
}

// Products liste tous les produits endoflife.date connus de la table.
func (m Mapping) Products() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, r := range m.Runtimes {
		add(r.Product)
	}
	for _, f := range m.Frameworks {
		add(f.Product.Product)
	}
	return out
}
