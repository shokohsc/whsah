package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NexusModsProvider queries the Nexus Mods API for the game's mod page and
// genre. A personal API key is required.
type NexusModsProvider struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

func (p *NexusModsProvider) Name() string { return "NexusMods" }

type nexusGame struct {
	Name         string `json:"name"`
	Genre        string `json:"genre"`
	DomainName   string `json:"domain_name"`
	NexusModsURL string `json:"nexusmods_url"`
}

func (p *NexusModsProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	if p.APIKey == "" {
		return nil, errors.New("nexusmods: API key not configured")
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.nexusmods.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/games.json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", p.APIKey)
	req.Header.Set("Application-Name", "selfhosted-gaming-vdi")
	req.Header.Set("Application-Version", "1.0")
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
		return nil, fmt.Errorf("nexusmods: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var games []nexusGame
	if err := json.Unmarshal(body, &games); err != nil {
		return nil, fmt.Errorf("nexusmods: decode: %w", err)
	}
	var match *nexusGame
	for i := range games {
		if titlesMatch(title, games[i].Name) {
			match = &games[i]
			break
		}
	}
	if match == nil {
		return nil, fmt.Errorf("nexusmods: no match for %q", title)
	}
	url := match.NexusModsURL
	if url == "" && match.DomainName != "" {
		url = "https://www.nexusmods.com/" + match.DomainName
	}
	meta := &Metadata{
		Title:   match.Name,
		Sources: []Source{{Name: p.Name(), URL: url}},
	}
	for _, genre := range strings.Split(match.Genre, ",") {
		meta.Genres = appendUnique(meta.Genres, genre)
	}
	return meta, nil
}
