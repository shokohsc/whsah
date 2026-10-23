package metadata

// Config selects which remote metadata sources are enabled.
type Config struct {
	// CustomURL is a pre-aggregated HTTP metadata source.
	CustomURL string
	// RAWGAPIKey enables the rawg.io provider.
	RAWGAPIKey string
	// BrowserlessURL and BrowserlessToken enable the FitGirl provider.
	BrowserlessURL   string
	BrowserlessToken string
	// FitGirlSearchURL overrides the default FitGirl search endpoint.
	FitGirlSearchURL string
	// YouTubeAPIKey enables live stream and replay lookups.
	YouTubeAPIKey string
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
	if cfg.YouTubeAPIKey != "" {
		providers = append(providers, &YouTubeProvider{APIKey: cfg.YouTubeAPIKey})
	}
	return providers
}
