package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/internal/agent"
	"selfhosted-gaming-vdi/internal/logging"
)

func main() {
	cfg := config.Default()

	serverURL := os.Getenv("VDI_SERVER_URL")
	if serverURL == "" {
		serverURL = "http://localhost:8080"
	}
	vmID := os.Getenv("VDI_VM_ID")
	gameLib := os.Getenv("VDI_GAME_LIB")
	if gameLib == "" {
		gameLib = cfg.LibraryPath
	}
	creds := os.Getenv("VDI_SMB_CREDS")
	share := os.Getenv("VDI_SMB_SHARE")
	agentToken := os.Getenv("VDI_AGENT_TOKEN")

	// Loki remote logging (optional): JSON to stdout plus a Loki stream.
	var extra []slog.Handler
	if lokiURL := os.Getenv("VDI_LOKI_URL"); lokiURL != "" {
		extra = append(extra, agent.NewLoki(lokiURL, vmID, logging.Level()))
	}
	logger := logging.Setup("agent", extra...)

	a := agent.New(serverURL, vmID, gameLib, agentToken).WithLogger(logger)

	if share != "" && creds != "" {
		if err := a.MountSMB(share, creds); err != nil {
			logger.Error("smb mount failed", "err", logging.SafeError(err))
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Register(ctx); err != nil {
		logger.Error("register failed", "err", logging.SafeError(err))
	}

	if err := a.StartStreaming(); err != nil {
		logger.Error("streaming failed", "err", logging.SafeError(err))
	}

	// Shutdown after idle (default 30 min) unless overridden.
	idleSecs := cfg.IdleSecs
	if v := os.Getenv("VDI_IDLE_SECS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			idleSecs = uint(n)
		}
	}
	a.ShutdownAfterInactivity(idleSecs)

	<-ctx.Done()
	logger.Info("agent shutting down")
}
