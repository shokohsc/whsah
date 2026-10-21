package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/db"
	"selfhosted-gaming-vdi/internal/logging"
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

	gormDB, err := db.Open(cfg)
	if err != nil {
		logger.Error("db open failed", "err", logging.SafeError(err))
		os.Exit(1)
	}
	if err := db.Migrate(gormDB); err != nil {
		logger.Error("db migrate failed", "err", logging.SafeError(err))
		os.Exit(1)
	}

	svc := worker.New(gormDB, &worker.HTTPFetcher{BaseURL: cfg.MetadataURL}, worker.DefaultPollInterval).WithLogger(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc.Run(ctx)
}
