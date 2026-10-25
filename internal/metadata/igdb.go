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

// IGDBProvider queries the IGDB API through Twitch application credentials.
type IGDBProvider struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	TokenURL     string
	Client       *http.Client

	token appTokenSource
}

func (p *IGDBProvider) Name() string { return "IGDB" }

type igdbNamed struct {
	Name string `json:"name"`
}

type igdbGame struct {
	Name              string      `json:"name"`
	URL               string      `json:"url"`
	Rating            float64     `json:"rating"`
	AggregatedRating  float64     `json:"aggregated_rating"`
	FirstReleaseDate  int64       `json:"first_release_date"`
	Genres            []igdbNamed `json:"genres"`
	Platforms         []igdbNamed `json:"platforms"`
	InvolvedCompanies []struct {
		Publisher bool      `json:"publisher"`
		Company   igdbNamed `json:"company"`
	} `json:"involved_companies"`
	AgeRatings []struct {
		Category int `json:"category"`
		Rating   int `json:"rating"`
	} `json:"age_ratings"`
}

func (p *IGDBProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	if p.ClientID == "" || p.ClientSecret == "" {
		return nil, errors.New("igdb: client id and secret not configured")
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.igdb.com/v4"
	}
	tokenURL := p.TokenURL
	if tokenURL == "" {
		tokenURL = "https://id.twitch.tv/oauth2/token"
	}
	token, err := p.token.get(ctx, client, tokenURL, p.ClientID, p.ClientSecret)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(
		`search %s; fields name,url,rating,aggregated_rating,first_release_date,genres.name,platforms.name,involved_companies.company.name,involved_companies.publisher,age_ratings.category,age_ratings.rating; limit 10;`,
		apicalypseQuote(title),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/games", strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-ID", p.ClientID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")

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
		return nil, fmt.Errorf("igdb: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var results []igdbGame
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("igdb: decode: %w", err)
	}
	var match *igdbGame
	for i := range results {
		if titlesMatch(title, results[i].Name) {
			match = &results[i]
			break
		}
	}
	if match == nil {
		return nil, fmt.Errorf("igdb: no match for %q", title)
	}
	return igdbMetadata(match), nil
}

func apicalypseQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func igdbMetadata(game *igdbGame) *Metadata {
	meta := &Metadata{
		Title:       game.Name,
		ReleaseDate: igdbReleaseDate(game.FirstReleaseDate),
		Sources:     []Source{{Name: "IGDB", URL: game.URL}},
	}
	if game.Rating > 0 {
		meta.Rating = game.Rating / 20
	} else if game.AggregatedRating > 0 {
		meta.Rating = game.AggregatedRating / 20
	}
	for _, genre := range game.Genres {
		meta.Genres = appendUnique(meta.Genres, genre.Name)
	}
	for _, platform := range game.Platforms {
		meta.Platforms = appendUnique(meta.Platforms, platform.Name)
	}
	for _, company := range game.InvolvedCompanies {
		if company.Company.Name == "" {
			continue
		}
		if company.Publisher {
			if meta.Publisher == "" {
				meta.Publisher = company.Company.Name
			}
			continue
		}
		if meta.Developer == "" {
			meta.Developer = company.Company.Name
		}
	}
	for _, rating := range game.AgeRatings {
		switch rating.Category {
		case 1: // ESRB
			if meta.ESRBRating == "" {
				meta.ESRBRating = igdbESRB(rating.Rating)
			}
		case 2: // PEGI
			if meta.PEGIRating == "" {
				meta.PEGIRating = igdbPEGI(rating.Rating)
			}
		}
	}
	return meta
}

func igdbReleaseDate(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02")
}

func igdbPEGI(rating int) string {
	switch rating {
	case 1:
		return "3"
	case 2:
		return "7"
	case 3:
		return "12"
	case 4:
		return "16"
	case 5:
		return "18"
	}
	return ""
}

func igdbESRB(rating int) string {
	switch rating {
	case 2:
		return "EC"
	case 3:
		return "E"
	case 4:
		return "E10+"
	case 5:
		return "T"
	case 6:
		return "M"
	case 7:
		return "AO"
	}
	return ""
}
