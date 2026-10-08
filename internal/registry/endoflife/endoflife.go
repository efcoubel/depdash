// Package endoflife lit les cycles de support publiés par endoflife.date.
package endoflife

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/efcoubel/depdash/internal/registry"
)

const DefaultURL = "https://endoflife.date"

type Client struct {
	client *registry.Client
	base   string
	now    func() time.Time
}

func New(client *registry.Client, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Client{client: client, base: strings.TrimRight(baseURL, "/"), now: time.Now}
}

var productRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Les champs eol, support et lts valent selon les produits un booléen ou une
// date : ils sont décodés à la main.
type cycle struct {
	Cycle       json.RawMessage `json:"cycle"`
	Latest      string          `json:"latest"`
	ReleaseDate string          `json:"releaseDate"`
	EOL         json.RawMessage `json:"eol"`
	Support     json.RawMessage `json:"support"`
	LTS         json.RawMessage `json:"lts"`
}

func (c *Client) Cycles(ctx context.Context, product string) ([]registry.Cycle, error) {
	if !productRe.MatchString(product) {
		return nil, registry.ErrNotFound
	}
	var raw []cycle
	if err := c.client.GetJSON(ctx, c.base+"/api/"+product+".json", nil, &raw); err != nil {
		return nil, err
	}
	now := c.now()
	out := make([]registry.Cycle, 0, len(raw))
	for _, r := range raw {
		name := scalar(r.Cycle)
		if name == "" {
			continue
		}
		out = append(out, registry.Cycle{
			Cycle:       name,
			Latest:      r.Latest,
			ReleaseDate: r.ReleaseDate,
			// eol indique si la fin est atteinte, support si le support est
			// encore actif : le second booléen est inversé pour que les deux
			// bornes se lisent de la même façon.
			EOLSecurity: bound(r.EOL, false),
			EOLActive:   bound(r.Support, true),
			// lts vaut true, ou la date à partir de laquelle le cycle devient LTS.
			LTS: registry.Bound(scalar(r.LTS)).Passed(now),
		})
	}
	return out, nil
}

// scalar rend une valeur JSON simple sous forme de texte : "8.3" → 8.3,
// 22 → 22, true → true, null → vide.
func scalar(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return s
}

func bound(raw json.RawMessage, invert bool) registry.Bound {
	s := scalar(raw)
	if invert {
		switch s {
		case "true":
			return "false"
		case "false":
			return "true"
		}
	}
	return registry.Bound(s)
}
