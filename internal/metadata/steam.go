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

// SteamProvider queries the public Steam store API.
type SteamProvider struct {
	BaseURL string
	Client  *http.Client
}

func (p *SteamProvider) Name() string { return "Steam" }

type steamApp struct {
	SteamAppID int      `json:"steam_appid"`
	Name       string   `json:"name"`
	Developers []string `json:"developers"`
	Publishers []string `json:"publishers"`
	Genres     []struct {
		Description string `json:"description"`
	} `json:"genres"`
	ReleaseDate struct {
		Date string `json:"date"`
	} `json:"release_date"`
	Platforms struct {
		Windows bool `json:"windows"`
		Mac     bool `json:"mac"`
		Linux   bool `json:"linux"`
	} `json:"platforms"`
	Metacritic struct {
		Score int `json:"score"`
	} `json:"metacritic"`
	Ratings map[string]struct {
		Rating string `json:"rating"`
	} `json:"ratings"`
	PCRequirements struct {
		Minimum     string `json:"minimum"`
		Recommended string `json:"recommended"`
	} `json:"pc_requirements"`
	Movies []struct {
		Name     string `json:"name"`
		MP4      string `json:"mp4"`
		HLSH264  string `json:"hls_h264"`
		DASHH264 string `json:"dash_h264"`
	} `json:"movies"`
}

func (p *SteamProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://store.steampowered.com"
	}
	var search struct {
		Items []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	q := url.Values{"term": {title}, "cc": {"us"}, "l": {"en"}}
	if err := p.getJSON(ctx, base+"/api/storesearch/?"+q.Encode(), &search); err != nil {
		return nil, err
	}
	appID := 0
	for _, item := range search.Items {
		if titlesMatch(title, item.Name) {
			appID = item.ID
			break
		}
	}
	if appID == 0 {
		return nil, fmt.Errorf("steam: no match for %q", title)
	}

	var details map[string]struct {
		Success bool     `json:"success"`
		Data    steamApp `json:"data"`
	}
	if err := p.getJSON(ctx, fmt.Sprintf("%s/api/appdetails?appids=%d&l=en", base, appID), &details); err != nil {
		return nil, err
	}
	result := details[fmt.Sprint(appID)]
	if !result.Success {
		return nil, errors.New("steam: app details unavailable")
	}
	app := result.Data

	meta := &Metadata{
		Title:       app.Name,
		ReleaseDate: app.ReleaseDate.Date,
		PCSpecs: PCRequirements{
			Minimum:     trimLabel(htmlText(app.PCRequirements.Minimum), "Minimum:"),
			Recommended: trimLabel(htmlText(app.PCRequirements.Recommended), "Recommended:"),
		},
		MetacriticScore: app.Metacritic.Score,
		Sources: []Source{{
			Name: p.Name(),
			URL:  fmt.Sprintf("%s/app/%d", base, appID),
		}},
	}
	if len(app.Developers) > 0 {
		meta.Developer = app.Developers[0]
	}
	if len(app.Publishers) > 0 {
		meta.Publisher = app.Publishers[0]
	}
	for _, genre := range app.Genres {
		meta.Genres = appendUnique(meta.Genres, genre.Description)
	}
	if app.Platforms.Windows {
		meta.Platforms = appendUnique(meta.Platforms, "PC")
	}
	if app.Platforms.Mac {
		meta.Platforms = appendUnique(meta.Platforms, "macOS")
	}
	if app.Platforms.Linux {
		meta.Platforms = appendUnique(meta.Platforms, "Linux")
	}
	if rating, ok := app.Ratings["pegi"]; ok {
		meta.PEGIRating = normalizePEGI(rating.Rating)
	}
	if rating, ok := app.Ratings["esrb"]; ok {
		meta.ESRBRating = strings.ToUpper(rating.Rating)
	}
	for _, movie := range app.Movies {
		for _, video := range []string{movie.HLSH264, movie.DASHH264, movie.MP4} {
			if video != "" {
				meta.VideoReplays = appendUnique(meta.VideoReplays, video)
				break
			}
		}
	}
	meta.Reviews = p.reviews(ctx, base, appID)
	return meta, nil
}

func trimLabel(text, label string) string {
	if strings.HasPrefix(strings.ToLower(text), strings.ToLower(label)) {
		return strings.TrimSpace(text[len(label):])
	}
	return text
}

func (p *SteamProvider) reviews(ctx context.Context, base string, appID int) []Review {
	var resp struct {
		Reviews []struct {
			Author struct {
				SteamID string `json:"steamid"`
			} `json:"author"`
			Review  string `json:"review"`
			VotedUp bool   `json:"voted_up"`
		} `json:"reviews"`
	}
	q := url.Values{"json": {"1"}, "language": {"all"}, "purchase_type": {"all"}, "num_per_page": {"5"}}
	if err := p.getJSON(ctx, fmt.Sprintf("%s/appreviews/%d?%s", base, appID, q.Encode()), &resp); err != nil {
		return nil
	}
	var reviews []Review
	for _, r := range resp.Reviews {
		text := strings.TrimSpace(r.Review)
		if text == "" {
			continue
		}
		if len(text) > 1000 {
			text = text[:1000]
		}
		review := Review{Source: "Steam", Author: r.Author.SteamID, Text: text}
		if r.VotedUp {
			review.Score = 1
		}
		reviews = append(reviews, review)
	}
	return reviews
}

func (p *SteamProvider) getJSON(ctx context.Context, endpoint string, out any) error {
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
		return fmt.Errorf("steam: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("steam: decode: %w", err)
	}
	return nil
}
