package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/db"
	"selfhosted-gaming-vdi/internal/backup"
	"selfhosted-gaming-vdi/internal/k8s"
	"selfhosted-gaming-vdi/internal/proxmox"
	"selfhosted-gaming-vdi/internal/server"
)

func main() {
	cfg := config.Default()
	if v := os.Getenv("VDI_DATABASE_DSN"); v != "" {
		cfg.DatabaseDSN = v
	}
	if v := os.Getenv("VDI_BACKUP_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.BackupInterval = d
		}
	}
	if v := os.Getenv("VDI_VM_DELETION_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.VMDeletionDelay = d
		}
	}

	gormDB, err := db.Open(cfg)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	if err := db.Migrate(gormDB); err != nil {
		log.Fatalf("db migrate: %v", err)
	}

	var px *proxmox.Client
	if base := os.Getenv("VDI_PROXMOX_BASE"); base != "" {
		px = proxmox.New(base, os.Getenv("VDI_PROXMOX_NODE"), os.Getenv("VDI_PROXMOX_TOKEN"))
	}

	var kube *k8s.Client
	if os.Getenv("VDI_KUBECONFIG") != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		if c, err := k8s.New(os.Getenv("VDI_KUBECONFIG")); err == nil {
			kube = c
		} else {
			log.Printf("k8s client: %v", err)
		}
	}

	oidc := server.OIDC{
		Issuer:   os.Getenv("VDI_OIDC_ISSUER"),
		ClientID: os.Getenv("VDI_OIDC_CLIENT_ID"),
	}
	verifier := server.NewVerifier(oidc)

	srv := server.New(gormDB, verifier, cfg)

	// Backup scheduler + VM deletion delay (runs in the server).
	go backup.New(gormDB, px, cfg).Run(context.Background())

	_ = kube // Talos node cordon/drain/delete invoked by admin flows.

	addr := os.Getenv("VDI_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	httpSrv := &http.Server{Addr: addr, Handler: srv.Routes()}

	go func() {
		log.Printf("server listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
