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

// RAWGProvider queries the rawg.io API. An API key is required.
type RAWGProvider struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

func (p *RAWGProvider) Name() string { return "RAWG" }

type rawgNamed struct {
	Name string `json:"name"`
}

type rawgGame struct {
	ID         int         `json:"id"`
	Slug       string      `json:"slug"`
	Name       string      `json:"name"`
	Released   string      `json:"released"`
	Rating     float64     `json:"rating"`
	Metacritic int         `json:"metacritic"`
	Genres     []rawgNamed `json:"genres"`
	Tags       []rawgNamed `json:"tags"`
	Developers []rawgNamed `json:"developers"`
	Publishers []rawgNamed `json:"publishers"`
	ESRBRating *rawgNamed  `json:"esrb_rating"`
	Platforms  []struct {
		Platform rawgNamed `json:"platform"`
	} `json:"platforms"`
	Movies []struct {
		Name string            `json:"name"`
		Data map[string]string `json:"data"`
	} `json:"movies"`
}

func (p *RAWGProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	if p.APIKey == "" {
		return nil, errors.New("rawg: API key not configured")
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.rawg.io/api"
	}
	var search struct {
		Results []rawgGame `json:"results"`
	}
	q := url.Values{"key": {p.APIKey}, "search": {title}, "page_size": {"10"}}
	if err := p.getJSON(ctx, base+"/games?"+q.Encode(), &search); err != nil {
		return nil, err
	}
	var match *rawgGame
	for i := range search.Results {
		if titlesMatch(title, search.Results[i].Name) {
			match = &search.Results[i]
			break
		}
	}
	if match == nil {
		return nil, fmt.Errorf("rawg: no match for %q", title)
	}

	game := *match
	var detail rawgGame
	if err := p.getJSON(ctx, fmt.Sprintf("%s/games/%d?key=%s", base, game.ID, url.QueryEscape(p.APIKey)), &detail); err == nil && detail.ID != 0 {
		game = detail
	}

	meta := &Metadata{
		Title:           game.Name,
		Rating:          game.Rating,
		ReleaseDate:     game.Released,
		MetacriticScore: game.Metacritic,
		Sources:         []Source{{Name: p.Name(), URL: "https://rawg.io/games/" + game.Slug}},
	}
	if game.ESRBRating != nil {
		meta.ESRBRating = game.ESRBRating.Name
	}
	for _, g := range game.Genres {
		meta.Genres = append(meta.Genres, g.Name)
	}
	for i, t := range game.Tags {
		if i == 20 {
			break
		}
		meta.Tags = append(meta.Tags, t.Name)
	}
	for _, d := range game.Developers {
		if meta.Developer == "" {
			meta.Developer = d.Name
		}
	}
	for _, pub := range game.Publishers {
		if meta.Publisher == "" {
			meta.Publisher = pub.Name
		}
	}
	for _, platform := range game.Platforms {
		meta.Platforms = appendUnique(meta.Platforms, platform.Platform.Name)
	}
	for _, movie := range game.Movies {
		if video := movie.Data["max"]; video != "" {
			meta.VideoReplays = appendUnique(meta.VideoReplays, video)
		}
	}
	return meta, nil
}

func (p *RAWGProvider) getJSON(ctx context.Context, endpoint string, out any) error {
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
		return fmt.Errorf("rawg: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("rawg: decode: %w", err)
	}
	return nil
}
