package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Browserless renders pages through a remote browserless instance, which lets
// the worker scrape JavaScript-heavy sites without a local browser.
type Browserless struct {
	URL    string
	Token  string
	Client *http.Client
}

// Content renders target and returns the page HTML.
func (b *Browserless) Content(ctx context.Context, target string) ([]byte, error) {
	if strings.TrimSpace(b.URL) == "" {
		return nil, fmt.Errorf("browserless: URL not configured")
	}
	endpoint := strings.TrimRight(b.URL, "/") + "/content"
	if b.Token != "" {
		endpoint += "?token=" + url.QueryEscape(b.Token)
	}
	payload, err := json.Marshal(map[string]any{
		"url":         target,
		"gotoOptions": map[string]any{"waitUntil": "networkidle2", "timeout": 30000},
	})
	if err != nil {
		return nil, err
	}
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
		return nil, fmt.Errorf("browserless: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
