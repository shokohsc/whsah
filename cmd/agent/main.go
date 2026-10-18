package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"selfhosted-gaming-vdi/internal/agent"
)

func main() {
	serverURL := os.Getenv("VDI_SERVER_URL")
	if serverURL == "" {
		serverURL = "http://localhost:8080"
	}
	vmID := os.Getenv("VDI_VM_ID")
	gameLib := os.Getenv("VDI_GAME_LIB") // SMB mount point
	creds := os.Getenv("VDI_SMB_CREDS")
	share := os.Getenv("VDI_SMB_SHARE")

	a := agent.New(serverURL, vmID, gameLib)

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
	idleSecs := uint(1800)
	if v := os.Getenv("VDI_IDLE_SECS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			idleSecs = uint(n)
		}
	}
	a.ShutdownAfterInactivity(idleSecs)

	<-ctx.Done()
	log.Println("agent shutting down")
}
