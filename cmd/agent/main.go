package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/internal/agent"
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

	a := agent.New(serverURL, vmID, gameLib, agentToken)

	// Loki remote logging (optional).
	if lokiURL := os.Getenv("VDI_LOKI_URL"); lokiURL != "" {
		a.WithLogger(agent.NewLogger(agent.NewLoki(lokiURL, vmID)))
	} else {
		a.WithLogger(agent.NewLogger(nil))
	}

	if share != "" && creds != "" {
		if err := a.MountSMB(share, creds); err != nil {
			log.Printf("smb mount: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Register(ctx); err != nil {
		log.Printf("register: %v", err)
	}

	if err := a.StartStreaming(); err != nil {
		log.Printf("streaming: %v", err)
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
	log.Println("agent shutting down")
}
