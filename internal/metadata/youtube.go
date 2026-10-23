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

// YouTubeProvider uses the YouTube Data API to find live streams and video
// replays for a game. An API key is required.
type YouTubeProvider struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

func (p *YouTubeProvider) Name() string { return "YouTube" }

func (p *YouTubeProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	if p.APIKey == "" {
		return nil, errors.New("youtube: API key not configured")
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://www.googleapis.com/youtube/v3"
	}
	searchQuery := title + " game"
	meta := &Metadata{
		Sources: []Source{{Name: p.Name(), URL: "https://www.youtube.com/results?search_query=" + url.QueryEscape(searchQuery)}},
	}

	live, err := p.search(ctx, base, searchQuery, "live")
	if err == nil {
		meta.LiveStreams = live
	}
	replays, err := p.search(ctx, base, searchQuery, "")
	if err != nil {
		return nil, err
	}
	meta.VideoReplays = replays
	return meta, nil
}

func (p *YouTubeProvider) search(ctx context.Context, base, query, eventType string) ([]string, error) {
	q := url.Values{
		"part":       {"snippet"},
		"type":       {"video"},
		"maxResults": {"5"},
		"q":          {query},
		"key":        {p.APIKey},
	}
	if eventType != "" {
		q.Set("eventType", eventType)
	}
	var resp struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
		} `json:"items"`
	}
	if err := p.getJSON(ctx, base+"/search?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, item := range resp.Items {
		if item.ID.VideoID != "" {
			out = appendUnique(out, "https://www.youtube.com/watch?v="+item.ID.VideoID)
		}
	}
	return out, nil
}

func (p *YouTubeProvider) getJSON(ctx context.Context, endpoint string, out any) error {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("youtube: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("youtube: decode: %w", err)
	}
	return nil
}
