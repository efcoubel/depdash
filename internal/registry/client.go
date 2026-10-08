package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	maxConcurrent = 8
	maxBody       = 64 << 20
)

// Client est le client HTTP d'un registre : 8 requêtes simultanées au plus,
// 10 s de délai par tentative, 2 tentatives avec backoff.
type Client struct {
	HTTP     *http.Client
	Attempts int
	Backoff  time.Duration
	sem      chan struct{}
}

func NewClient() *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: 10 * time.Second},
		Attempts: 2,
		Backoff:  500 * time.Millisecond,
		sem:      make(chan struct{}, maxConcurrent),
	}
}

// GetJSON récupère url et décode la réponse dans out. Une réponse 404 donne
// ErrNotFound sans nouvelle tentative ; les erreurs réseau, 429 et 5xx sont
// retentées.
func (c *Client) GetJSON(ctx context.Context, url string, header http.Header, out any) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	var err error
	for attempt := 0; attempt < c.Attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.Backoff * time.Duration(attempt)):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var retry bool
		retry, err = c.get(ctx, url, header, out)
		if err == nil || !retry {
			return err
		}
	}
	return err
}

func (c *Client) get(ctx context.Context, url string, header http.Header, out any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "depdash")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ctx.Err() == nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("%s : HTTP %d", req.URL.Host, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("%s : HTTP %d", req.URL.Host, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return false, fmt.Errorf("%s : réponse illisible : %w", req.URL.Host, err)
	}
	return false, nil
}
