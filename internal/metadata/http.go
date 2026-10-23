package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPProvider reads pre-aggregated metadata from a custom HTTP source:
// GET {base}/games?title={title} returns a Metadata JSON body.
type HTTPProvider struct {
	BaseURL string
	Client  *http.Client
}

func (p *HTTPProvider) Name() string { return "custom" }

func (p *HTTPProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	if p.BaseURL == "" {
		return nil, errors.New("metadata source not configured")
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + "/games?title=" + url.QueryEscape(title)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("metadata source: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var meta Metadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("metadata source: decode: %w", err)
	}
	if len(meta.Sources) == 0 {
		meta.Sources = []Source{{Name: p.Name(), URL: endpoint}}
	}
	return &meta, nil
}
