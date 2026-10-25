package metadata

// Config selects which remote metadata sources are enabled.
type Config struct {
	// CustomURL is a pre-aggregated HTTP metadata source.
	CustomURL string
	// RAWGAPIKey enables the rawg.io provider.
	RAWGAPIKey string
	// NexusModsAPIKey enables the Nexus Mods provider.
	NexusModsAPIKey string
	// BrowserlessURL and BrowserlessToken enable the FitGirl provider.
	BrowserlessURL   string
	BrowserlessToken string
	// FitGirlSearchURL overrides the default FitGirl search endpoint.
	FitGirlSearchURL string
	// YouTubeAPIKey enables live stream and replay lookups.
	YouTubeAPIKey string
	// IGDBClientID and IGDBClientSecret enable the IGDB provider. IGDB uses
	// Twitch application credentials.
	IGDBClientID     string
	IGDBClientSecret string
	// TwitchClientID and TwitchClientSecret enable Twitch live streams and
	// video replays.
	TwitchClientID     string
	TwitchClientSecret string
}

// BuildProviders returns the enabled providers in merge priority order.
func BuildProviders(cfg Config) []Provider {
	var providers []Provider
	if cfg.CustomURL != "" {
		providers = append(providers, &HTTPProvider{BaseURL: cfg.CustomURL})
	}
	if cfg.RAWGAPIKey != "" {
		providers = append(providers, &RAWGProvider{APIKey: cfg.RAWGAPIKey})
	}
	if cfg.IGDBClientID != "" && cfg.IGDBClientSecret != "" {
		providers = append(providers, &IGDBProvider{ClientID: cfg.IGDBClientID, ClientSecret: cfg.IGDBClientSecret})
	}
	if cfg.NexusModsAPIKey != "" {
		providers = append(providers, &NexusModsProvider{APIKey: cfg.NexusModsAPIKey})
	}
	providers = append(providers, &SteamProvider{}, &PCGamingWikiProvider{})
	if cfg.BrowserlessURL != "" {
		providers = append(providers, &FitGirlProvider{
			SearchURL: cfg.FitGirlSearchURL,
			Browserless: &Browserless{
				URL:   cfg.BrowserlessURL,
				Token: cfg.BrowserlessToken,
			},
		})
	}
	if cfg.TwitchClientID != "" && cfg.TwitchClientSecret != "" {
		providers = append(providers, &TwitchProvider{ClientID: cfg.TwitchClientID, ClientSecret: cfg.TwitchClientSecret})
	}
	if cfg.YouTubeAPIKey != "" {
		providers = append(providers, &YouTubeProvider{APIKey: cfg.YouTubeAPIKey})
	}
	return providers
}
