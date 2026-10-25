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
	"sync"
	"time"
)

// appTokenSource caches an OAuth2 client-credentials token. IGDB and Twitch
// share the same Twitch application credentials.
type appTokenSource struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

func (t *appTokenSource) get(ctx context.Context, client *http.Client, endpoint, clientID, secret string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expires) {
		return t.token, nil
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {secret},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("twitch token: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("twitch token: decode: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("twitch token: empty access token")
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	t.token = out.AccessToken
	t.expires = time.Now().Add(ttl - time.Minute)
	return t.token, nil
}

// TwitchProvider queries the Twitch Helix API for live streams and archived
// videos of a game. A Twitch application client id and secret are required.
type TwitchProvider struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	TokenURL     string
	Client       *http.Client

	token appTokenSource
}

func (p *TwitchProvider) Name() string { return "Twitch" }

func (p *TwitchProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	if p.ClientID == "" || p.ClientSecret == "" {
		return nil, errors.New("twitch: client id and secret not configured")
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.twitch.tv/helix"
	}
	tokenURL := p.TokenURL
	if tokenURL == "" {
		tokenURL = "https://id.twitch.tv/oauth2/token"
	}
	token, err := p.token.get(ctx, client, tokenURL, p.ClientID, p.ClientSecret)
	if err != nil {
		return nil, err
	}

	var games struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, client, base+"/games?name="+url.QueryEscape(title), token, &games); err != nil {
		return nil, err
	}
	var gameID, gameName string
	for _, game := range games.Data {
		if titlesMatch(title, game.Name) {
			gameID, gameName = game.ID, game.Name
			break
		}
	}
	if gameID == "" {
		return nil, fmt.Errorf("twitch: no match for %q", title)
	}

	meta := &Metadata{
		Sources: []Source{{
			Name: p.Name(),
			URL:  "https://www.twitch.tv/search?term=" + url.QueryEscape(gameName),
		}},
	}
	var streams struct {
		Data []struct {
			UserLogin string `json:"user_login"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, client, base+"/streams?game_id="+url.QueryEscape(gameID)+"&first=5", token, &streams); err == nil {
		for _, stream := range streams.Data {
			if stream.UserLogin != "" {
				meta.LiveStreams = appendUnique(meta.LiveStreams, "https://www.twitch.tv/"+stream.UserLogin)
			}
		}
	}
	var videos struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, client, base+"/videos?game_id="+url.QueryEscape(gameID)+"&first=5&type=archive", token, &videos); err != nil {
		return nil, err
	}
	for _, video := range videos.Data {
		if video.ID != "" {
			meta.VideoReplays = appendUnique(meta.VideoReplays, "https://www.twitch.tv/videos/"+video.ID)
		}
	}
	return meta, nil
}

func (p *TwitchProvider) getJSON(ctx context.Context, client *http.Client, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Client-ID", p.ClientID)
	req.Header.Set("Authorization", "Bearer "+token)
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
		return fmt.Errorf("twitch: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("twitch: decode: %w", err)
	}
	return nil
}
