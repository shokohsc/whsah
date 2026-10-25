package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeProvider struct {
	name  string
	meta  *Metadata
	err   error
	calls int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	f.calls++
	return f.meta, f.err
}

func TestPEGIFromESRB(t *testing.T) {
	cases := map[string]string{
		"E":            "3",
		"E10+":         "7",
		"Teen":         "12",
		"Everyone 10+": "7",
		"T":            "12",
		"M":            "18",
		"AO":           "18",
		"":             "",
	}
	for esrb, want := range cases {
		if got := PEGIFromESRB(esrb); got != want {
			t.Fatalf("PEGIFromESRB(%q) = %q, want %q", esrb, got, want)
		}
	}
}

func TestTitlesMatch(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Hades", "Hades", true},
		{"Hades", "Hades – v1.35966 (v1.0) + Bonus Soundtrack", true},
		{"Hades", "Hades II", false},
		{"Hades", "Hades 2", false},
		{"Hades", "Hades: Game of the Year Edition", true},
		{"The Witcher 3", "The Witcher 3: Wild Hunt", false},
	}
	for _, c := range cases {
		if got := titlesMatch(c.a, c.b); got != c.want {
			t.Fatalf("titlesMatch(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAggregatorMergesAndCrossReferences(t *testing.T) {
	first := &fakeProvider{name: "first", meta: &Metadata{
		Title:      "Hades",
		PEGIRating: "12",
		Rating:     4.4,
		Genres:     []string{"Action"},
		Developer:  "Supergiant Games",
		Sources:    []Source{{Name: "first", URL: "https://first.example/hades"}},
	}}
	second := &fakeProvider{name: "second", meta: &Metadata{
		Title:             "Hades",
		PEGIRating:        "18",
		Tags:              []string{"Roguelike", "Action"},
		Genres:            []string{"RPG"},
		MetacriticScore:   93,
		PCSpecs:           PCRequirements{Minimum: "8 GB RAM"},
		VideoReplays:      []string{"https://video.example/hades"},
		Reviews:           []Review{{Source: "second", Text: "great"}},
		SensCritiqueScore: 8.5,
		Sources:           []Source{{Name: "second", URL: "https://second.example/hades"}},
	}}
	svc := New(first, second)

	meta, err := svc.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.PEGIRating != "12" {
		t.Fatalf("first provider should win PEGI, got %q", meta.PEGIRating)
	}
	if meta.Developer != "Supergiant Games" {
		t.Fatalf("developer = %q", meta.Developer)
	}
	if meta.MetacriticScore != 93 || meta.SensCritiqueScore != 8.5 {
		t.Fatalf("scores = %d/%v", meta.MetacriticScore, meta.SensCritiqueScore)
	}
	if len(meta.Genres) != 2 {
		t.Fatalf("genres = %v", meta.Genres)
	}
	if len(meta.Tags) != 2 {
		t.Fatalf("tags = %v", meta.Tags)
	}
	if len(meta.Sources) != 2 {
		t.Fatalf("sources = %v", meta.Sources)
	}
}

func TestAggregatorFallsBackToESRB(t *testing.T) {
	svc := New(&fakeProvider{name: "one", meta: &Metadata{ESRBRating: "T"}})
	meta, err := svc.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.PEGIRating != "12" {
		t.Fatalf("PEGI fallback = %q", meta.PEGIRating)
	}
}

func TestAggregatorToleratesPartialFailure(t *testing.T) {
	svc := New(
		&fakeProvider{name: "down", err: errors.New("down")},
		&fakeProvider{name: "up", meta: &Metadata{Title: "Hades"}},
	)
	meta, err := svc.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.Title != "Hades" {
		t.Fatalf("title = %q", meta.Title)
	}
}

func TestAggregatorFailsWhenAllProvidersFail(t *testing.T) {
	svc := New(&fakeProvider{name: "down", err: errors.New("down")})
	if _, err := svc.Fetch(context.Background(), "Hades"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRAWGProvider(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("key") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/games":
			if r.URL.Query().Get("search") != "Hades" {
				t.Fatalf("search = %q", r.URL.Query().Get("search"))
			}
			fmt.Fprint(w, `{"results":[{"id":2,"slug":"hades-2","name":"Hades II"},{"id":1,"slug":"hades","name":"Hades"}]}`)
		case "/api/games/1":
			fmt.Fprint(w, `{"id":1,"slug":"hades","name":"Hades","released":"2020-09-17","rating":4.4,"metacritic":93,
				"genres":[{"name":"Action"},{"name":"RPG"}],"tags":[{"name":"Roguelike"}],
				"developers":[{"name":"Supergiant Games"}],"publishers":[{"name":"Supergiant Games"}],
				"esrb_rating":{"name":"Teen"},"platforms":[{"platform":{"name":"PC"}}],
				"movies":[{"name":"Trailer","data":{"max":"https://video.example/hades.mp4"}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &RAWGProvider{APIKey: "secret", BaseURL: srv.URL + "/api"}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.Title != "Hades" || meta.Developer != "Supergiant Games" || meta.ESRBRating != "Teen" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if meta.MetacriticScore != 93 || meta.Rating != 4.4 || len(meta.Genres) != 2 {
		t.Fatalf("unexpected scores/genres: %+v", meta)
	}
	if len(meta.VideoReplays) != 1 || len(meta.Sources) != 1 {
		t.Fatalf("unexpected videos/sources: %+v", meta)
	}
	if requests != 2 {
		t.Fatalf("expected search + detail requests, got %d", requests)
	}
}

func TestSteamProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/storesearch/":
			fmt.Fprint(w, `{"items":[{"id":1145360,"name":"Hades"},{"id":1145350,"name":"Hades II"}]}`)
		case r.URL.Path == "/api/appdetails":
			fmt.Fprint(w, `{"1145360":{"success":true,"data":{"steam_appid":1145360,"name":"Hades",
				"developers":["Supergiant Games"],"publishers":["Supergiant Games"],
				"genres":[{"description":"Action"}],"release_date":{"date":"17 Sep, 2020"},
				"platforms":{"windows":true,"mac":false,"linux":false},"metacritic":{"score":93},
				"ratings":{"esrb":{"rating":"m"},"pegi":{"rating":"18"}},
				"pc_requirements":{"minimum":"<ul><li><strong>Minimum:</strong> Memory: <strong>8 GB</strong></li></ul>","recommended":"<ul><li><strong>Recommended:</strong> Memory: 16 GB</li></ul>"},
				"movies":[{"name":"Trailer","hls_h264":"https://video.example/hades.m3u8"}]}}}`)
		case r.URL.Path == "/appreviews/1145360":
			fmt.Fprint(w, `{"reviews":[{"author":{"steamid":"42"},"review":"great game","voted_up":true}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &SteamProvider{BaseURL: srv.URL}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.Title != "Hades" || meta.PEGIRating != "18" || meta.ESRBRating != "M" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if meta.MetacriticScore != 93 || len(meta.Platforms) != 1 {
		t.Fatalf("unexpected score/platforms: %+v", meta)
	}
	if !strings.Contains(meta.PCSpecs.Minimum, "8 GB") || !strings.Contains(meta.PCSpecs.Recommended, "16 GB") {
		t.Fatalf("unexpected specs: %+v", meta.PCSpecs)
	}
	if len(meta.VideoReplays) != 1 || len(meta.Reviews) != 1 {
		t.Fatalf("unexpected videos/reviews: %+v %+v", meta.VideoReplays, meta.Reviews)
	}
}

func TestPCGamingWikiProvider(t *testing.T) {
	const wikitext = `{{Infobox game
|developers   = 
{{Infobox game/row/developer|Supergiant Games}}
|publishers   = 
{{Infobox game/row/publisher|Supergiant Games}}
|release dates= 
{{Infobox game/row/date|Windows|September 17, 2020}}
{{Infobox game/row/date|OS X|October 1, 2020}}
|reception    = 
{{Infobox game/row/reception|Metacritic|hades|93}}
|taxonomy     =
{{Infobox game/row/taxonomy/genres            | Action, Roguelike }}
{{Infobox game/row/taxonomy/themes            | Classical, Fantasy }}
}}
==System requirements==
{{System requirements
|OSfamily = Windows
|minOS    = 10
|minCPU   = dual-core 2.4 GHz
|minRAM   = 8 GB
|minGPU   =
|minVRAM  = 2 GB
|minHD    = 10 GB
|recOS    =
|recCPU   = quad-core
|recRAM   = 16 GB
|recGPU   = RTX 2060
|recHD    = 20 GB
}}
{{References}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "query":
			if r.URL.Query().Get("titles") != "" {
				fmt.Fprintf(w, `{"query":{"pages":{"1":{"title":"%s"}}}}`, r.URL.Query().Get("titles"))
				return
			}
			fmt.Fprint(w, `{"query":{"search":[{"title":"Hades II"},{"title":"Hades"}]}}`)
		case "parse":
			if r.URL.Query().Get("page") != "Hades" {
				t.Fatalf("page = %q", r.URL.Query().Get("page"))
			}
			body := strings.ReplaceAll(wikitext, `"`, `\"`)
			body = strings.ReplaceAll(body, "\n", `\n`)
			fmt.Fprintf(w, `{"parse":{"wikitext":{"*":"%s"}}}`, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &PCGamingWikiProvider{BaseURL: srv.URL}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.Developer != "Supergiant Games" || meta.Publisher != "Supergiant Games" {
		t.Fatalf("unexpected companies: %+v", meta)
	}
	if meta.ReleaseDate != "September 17, 2020" || meta.MetacriticScore != 93 {
		t.Fatalf("unexpected release/score: %+v", meta)
	}
	if strings.Join(meta.Genres, ",") != "Action,Roguelike" {
		t.Fatalf("genres = %v", meta.Genres)
	}
	if strings.Join(meta.Tags, ",") != "Classical,Fantasy" {
		t.Fatalf("tags = %v", meta.Tags)
	}
	if !strings.Contains(meta.PCSpecs.Minimum, "CPU: dual-core 2.4 GHz") ||
		!strings.Contains(meta.PCSpecs.Minimum, "VRAM: 2 GB") ||
		strings.Contains(meta.PCSpecs.Minimum, "GPU: |") ||
		strings.Contains(meta.PCSpecs.Recommended, "OS: |") ||
		!strings.Contains(meta.PCSpecs.Recommended, "RAM: 16 GB") {
		t.Fatalf("specs = %+v", meta.PCSpecs)
	}
	if strings.Join(meta.Platforms, ",") != "PC,macOS" {
		t.Fatalf("platforms = %v", meta.Platforms)
	}
}

func TestFitGirlProviderThroughBrowserless(t *testing.T) {
	const searchHTML = `<html><body>
<article class="post category-lossless-repack tag-3d tag-roguelike">
<h1 class="entry-title"><a href="https://fitgirl-repacks.site/hades-2/" rel="bookmark">Hades II – v1.1</a></h1>
</article>
<article class="post category-lossless-repack tag-3d tag-action-rpg">
<h1 class="entry-title"><a href="https://fitgirl-repacks.site/hades/" rel="bookmark">Hades – v1.35966 (v1.0) + Bonus Soundtrack</a></h1>
</article>
</body></html>`
	const articleHTML = `<html><body>
<h1 class="entry-title">Hades – v1.35966 (v1.0) + Bonus Soundtrack</h1>
<div class="entry-content">
<p>Genres/Tags: RPG, Action RPG, Isometric</p>
<p>Company: Supergiant Games</p>
<p>PEGI: 12</p>
<p>System Requirements</p><p>OS: Windows 10</p><p>Processor: Dual Core</p><p>Memory: 8 GB</p><p>Graphics: GTX 950</p><p>Storage: 10 GB</p>
<p>Repack Features</p>
</div></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		switch {
		case strings.Contains(payload.URL, "?s=Hades"):
			fmt.Fprint(w, searchHTML)
		case strings.HasSuffix(payload.URL, "/hades/"):
			fmt.Fprint(w, articleHTML)
		default:
			t.Fatalf("unexpected browserless url %q", payload.URL)
		}
	}))
	defer srv.Close()

	p := &FitGirlProvider{Browserless: &Browserless{URL: srv.URL}, SearchURL: srv.URL + "/?s={query}"}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.Title != "Hades" || meta.Developer != "Supergiant Games" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if strings.Join(meta.Tags, ",") != "RPG,Action RPG,Isometric,3d" {
		t.Fatalf("tags = %v", meta.Tags)
	}
	if meta.PEGIRating != "12" {
		t.Fatalf("pegi = %q", meta.PEGIRating)
	}
	if !strings.Contains(meta.PCSpecs.Minimum, "Processor: Dual Core") {
		t.Fatalf("specs = %+v", meta.PCSpecs)
	}
	if len(meta.Sources) != 1 {
		t.Fatalf("sources = %v", meta.Sources)
	}
}

func TestYouTubeProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("eventType") == "live" {
			fmt.Fprint(w, `{"items":[{"id":{"videoId":"live1"}}]}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":{"videoId":"replay1"}},{"id":{"videoId":"replay2"}}]}`)
	}))
	defer srv.Close()

	p := &YouTubeProvider{APIKey: "key", BaseURL: srv.URL}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(meta.LiveStreams) != 1 || len(meta.VideoReplays) != 2 {
		t.Fatalf("streams = %v, replays = %v", meta.LiveStreams, meta.VideoReplays)
	}
}

func TestIGDBProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if r.Form.Get("client_id") != "id" || r.Form.Get("client_secret") != "secret" || r.Form.Get("grant_type") != "client_credentials" {
				t.Fatalf("unexpected token form: %v", r.Form)
			}
			fmt.Fprint(w, `{"access_token":"token","expires_in":3600}`)
		case "/v4/games":
			if r.Header.Get("Client-ID") != "id" || r.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("unexpected headers: %v", r.Header)
			}
			query, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(query), `search "Hades"`) {
				t.Fatalf("unexpected query: %s", query)
			}
			fmt.Fprint(w, `[
				{"name":"Hades II","url":"https://www.igdb.com/games/hades-ii"},
				{"name":"Hades","url":"https://www.igdb.com/games/hades","rating":88,
				 "first_release_date":1600300800,
				 "genres":[{"name":"Role-playing (RPG)"}],
				 "platforms":[{"name":"PC (Microsoft Windows)"}],
				 "involved_companies":[
					{"publisher":false,"company":{"name":"Supergiant Games"}},
					{"publisher":true,"company":{"name":"Supergiant Games"}}
				 ],
				 "age_ratings":[{"category":1,"rating":5},{"category":2,"rating":3}]}
			]`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &IGDBProvider{ClientID: "id", ClientSecret: "secret", BaseURL: srv.URL + "/v4", TokenURL: srv.URL + "/token"}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.Title != "Hades" || meta.Developer != "Supergiant Games" || meta.Publisher != "Supergiant Games" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if meta.PEGIRating != "12" || meta.ESRBRating != "T" {
		t.Fatalf("unexpected ratings: %+v", meta)
	}
	if meta.ReleaseDate != "2020-09-17" || meta.Rating != 4.4 {
		t.Fatalf("unexpected release/rating: %+v", meta)
	}
	if strings.Join(meta.Genres, ",") != "Role-playing (RPG)" || strings.Join(meta.Platforms, ",") != "PC (Microsoft Windows)" {
		t.Fatalf("unexpected genres/platforms: %+v", meta)
	}
	if len(meta.Sources) != 1 || meta.Sources[0].Name != "IGDB" {
		t.Fatalf("unexpected sources: %+v", meta.Sources)
	}
}

func TestTwitchProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"token","expires_in":3600}`)
			return
		}
		if r.Header.Get("Client-ID") != "id" || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/helix/games":
			if r.URL.Query().Get("name") != "Hades" {
				t.Fatalf("name = %q", r.URL.Query().Get("name"))
			}
			fmt.Fprint(w, `{"data":[{"id":"2","name":"Hades II"},{"id":"1","name":"Hades"}]}`)
		case "/helix/streams":
			if r.URL.Query().Get("game_id") != "1" {
				t.Fatalf("streams game_id = %q", r.URL.Query().Get("game_id"))
			}
			fmt.Fprint(w, `{"data":[{"user_login":"streamer","user_name":"Streamer"}]}`)
		case "/helix/videos":
			if r.URL.Query().Get("game_id") != "1" {
				t.Fatalf("videos game_id = %q", r.URL.Query().Get("game_id"))
			}
			fmt.Fprint(w, `{"data":[{"id":"v1"},{"id":"v2"}]}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &TwitchProvider{ClientID: "id", ClientSecret: "secret", BaseURL: srv.URL + "/helix", TokenURL: srv.URL + "/token"}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Join(meta.LiveStreams, ",") != "https://www.twitch.tv/streamer" {
		t.Fatalf("live streams = %v", meta.LiveStreams)
	}
	if strings.Join(meta.VideoReplays, ",") != "https://www.twitch.tv/videos/v1,https://www.twitch.tv/videos/v2" {
		t.Fatalf("replays = %v", meta.VideoReplays)
	}
	if len(meta.Sources) != 1 || meta.Sources[0].Name != "Twitch" {
		t.Fatalf("sources = %v", meta.Sources)
	}
}

func TestIGDBRatingMappings(t *testing.T) {
	pegi := map[int]string{1: "3", 2: "7", 3: "12", 4: "16", 5: "18", 0: ""}
	for rating, want := range pegi {
		if got := igdbPEGI(rating); got != want {
			t.Fatalf("igdbPEGI(%d) = %q, want %q", rating, got, want)
		}
	}
	esrb := map[int]string{2: "EC", 3: "E", 4: "E10+", 5: "T", 6: "M", 7: "AO", 0: ""}
	for rating, want := range esrb {
		if got := igdbESRB(rating); got != want {
			t.Fatalf("igdbESRB(%d) = %q, want %q", rating, got, want)
		}
	}
}

func TestNexusModsProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/games.json" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("apikey") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `[
			{"name":"Hades II","genre":"RPG","domain_name":"hades2","nexusmods_url":"https://www.nexusmods.com/hades2"},
			{"name":"Hades","genre":"Action, Roguelike","domain_name":"hades","nexusmods_url":"https://www.nexusmods.com/hades"}
		]`)
	}))
	defer srv.Close()

	p := &NexusModsProvider{APIKey: "secret", BaseURL: srv.URL}
	meta, err := p.Fetch(context.Background(), "Hades")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.Title != "Hades" {
		t.Fatalf("title = %q", meta.Title)
	}
	if strings.Join(meta.Genres, ",") != "Action,Roguelike" {
		t.Fatalf("genres = %v", meta.Genres)
	}
	if len(meta.Sources) != 1 || meta.Sources[0].URL != "https://www.nexusmods.com/hades" {
		t.Fatalf("sources = %v", meta.Sources)
	}
}

func TestBuildProviders(t *testing.T) {
	providers := BuildProviders(Config{})
	if len(providers) != 2 {
		t.Fatalf("expected the two public providers, got %d", len(providers))
	}
	providers = BuildProviders(Config{
		CustomURL:          "https://custom.example",
		RAWGAPIKey:         "key",
		NexusModsAPIKey:    "nexus",
		BrowserlessURL:     "https://browserless.example",
		YouTubeAPIKey:      "youtube",
		IGDBClientID:       "igdb-id",
		IGDBClientSecret:   "igdb-secret",
		TwitchClientID:     "twitch-id",
		TwitchClientSecret: "twitch-secret",
	})
	if len(providers) != 9 {
		t.Fatalf("expected 9 providers, got %d", len(providers))
	}
	providers = BuildProviders(Config{IGDBClientID: "id", TwitchClientID: "id"})
	if len(providers) != 2 {
		t.Fatalf("providers without secrets must stay disabled, got %d", len(providers))
	}
}
