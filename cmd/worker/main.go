package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/db"
	"selfhosted-gaming-vdi/internal/logging"
	"selfhosted-gaming-vdi/internal/metadata"
	"selfhosted-gaming-vdi/internal/worker"
)

// Worker consumes game jobs (config generation, metadata retrieval) queued in
// PostgreSQL and writes the results back to the games table.
func main() {
	logger := logging.Setup("worker")
	cfg := config.Default()

	if v := os.Getenv("VDI_DATABASE_DSN"); v != "" {
		cfg.DatabaseDSN = v
	}
	if v := os.Getenv("VDI_METADATA_URL"); v != "" {
		cfg.MetadataURL = v
	}
	if v := os.Getenv("VDI_LIBRARY_PATH"); v != "" {
		cfg.LibraryPath = v
	}
	if v := os.Getenv("VDI_RAWG_API_KEY"); v != "" {
		cfg.RAWGAPIKey = v
	}
	if v := os.Getenv("VDI_NEXUSMODS_API_KEY"); v != "" {
		cfg.NexusModsAPIKey = v
	}
	if v := os.Getenv("VDI_BROWSERLESS_URL"); v != "" {
		cfg.BrowserlessURL = v
	}
	if v := os.Getenv("VDI_BROWSERLESS_TOKEN"); v != "" {
		cfg.BrowserlessToken = v
	}
	if v := os.Getenv("VDI_FITGIRL_SEARCH_URL"); v != "" {
		cfg.FitGirlSearchURL = v
	}
	if v := os.Getenv("VDI_YOUTUBE_API_KEY"); v != "" {
		cfg.YouTubeAPIKey = v
	}
	if v := os.Getenv("VDI_IGDB_CLIENT_ID"); v != "" {
		cfg.IGDBClientID = v
	}
	if v := os.Getenv("VDI_IGDB_CLIENT_SECRET"); v != "" {
		cfg.IGDBClientSecret = v
	}
	if v := os.Getenv("VDI_TWITCH_CLIENT_ID"); v != "" {
		cfg.TwitchClientID = v
	}
	if v := os.Getenv("VDI_TWITCH_CLIENT_SECRET"); v != "" {
		cfg.TwitchClientSecret = v
	}

	gormDB, err := db.Open(cfg)
	if err != nil {
		logger.Error("db open failed", "err", logging.SafeError(err))
		os.Exit(1)
	}
	if err := db.Migrate(gormDB); err != nil {
		logger.Error("db migrate failed", "err", logging.SafeError(err))
		os.Exit(1)
	}

	fetcher := metadata.New(metadata.BuildProviders(metadata.Config{
		CustomURL:          cfg.MetadataURL,
		RAWGAPIKey:         cfg.RAWGAPIKey,
		NexusModsAPIKey:    cfg.NexusModsAPIKey,
		BrowserlessURL:     cfg.BrowserlessURL,
		BrowserlessToken:   cfg.BrowserlessToken,
		FitGirlSearchURL:   cfg.FitGirlSearchURL,
		YouTubeAPIKey:      cfg.YouTubeAPIKey,
		IGDBClientID:       cfg.IGDBClientID,
		IGDBClientSecret:   cfg.IGDBClientSecret,
		TwitchClientID:     cfg.TwitchClientID,
		TwitchClientSecret: cfg.TwitchClientSecret,
	})...)
	fetcher.Logger = logger

	svc := worker.New(gormDB, fetcher, worker.DefaultPollInterval).WithLogger(logger).WithLibraryPath(cfg.LibraryPath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc.Run(ctx)
}
