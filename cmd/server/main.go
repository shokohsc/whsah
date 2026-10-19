package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
	if v := os.Getenv("VDI_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
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
	if v := os.Getenv("VDI_IDLE_SECS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			cfg.IdleSecs = uint(n)
		}
	}
	if v := os.Getenv("VDI_PROXMOX_BASE"); v != "" {
		cfg.ProxmoxBase = v
	}
	if v := os.Getenv("VDI_PROXMOX_NODE"); v != "" {
		cfg.ProxmoxNode = v
	}
	if v := os.Getenv("VDI_PROXMOX_AUTH_MODE"); v != "" {
		cfg.ProxmoxAuthMode = config.ProxmoxAuthMode(v)
	}
	if v := os.Getenv("VDI_PROXMOX_TOKEN"); v != "" {
		cfg.ProxmoxToken = v
	}
	if v := os.Getenv("VDI_PROXMOX_USER"); v != "" {
		cfg.ProxmoxUser = v
	}
	if v := os.Getenv("VDI_PROXMOX_PASSWORD"); v != "" {
		cfg.ProxmoxPassword = v
	}
	if v := os.Getenv("VDI_LIBRARY_PATH"); v != "" {
		cfg.LibraryPath = v
	}
	if v := os.Getenv("VDI_LOKI_URL"); v != "" {
		cfg.LokiURL = v
	}
	if v := os.Getenv("VDI_KUBERNETES_MODE"); v != "" {
		cfg.KubernetesMode = config.KubernetesMode(v)
	}
	if v := os.Getenv("VDI_KUBECONFIG"); v != "" {
		cfg.Kubeconfig = v
	}
	if v := os.Getenv("VDI_AGENT_TOKEN"); v != "" {
		cfg.AgentToken = v
	}
	if v := os.Getenv("VDI_FRONTEND_ORIGIN"); v != "" {
		cfg.FrontendOrigin = v
	}
	if v := os.Getenv("VDI_ADMIN_SUBJECTS"); v != "" {
		cfg.AdminSubjects = strings.Split(v, ",")
	}
	if v := os.Getenv("VDI_DEFAULT_AGE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.DefaultAge = n
		}
	}
	if v := os.Getenv("VDI_OIDC_PUBLIC_URL"); v != "" {
		cfg.OIDCPublicURL = v
	}
	if v := os.Getenv("VDI_OIDC_CLIENT_SECRET"); v != "" {
		cfg.OIDCClientSecret = v
	}

	gormDB, err := db.Open(cfg)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	if err := db.Migrate(gormDB); err != nil {
		log.Fatalf("db migrate: %v", err)
	}

	// Proxmox client: token or password auth, nil if not configured.
	var px *proxmox.Client
	if cfg.ProxmoxBase != "" {
		switch cfg.ProxmoxAuthMode {
		case config.ProxmoxAuthPassword:
			px = proxmox.NewPassword(cfg.ProxmoxBase, cfg.ProxmoxNode, cfg.ProxmoxUser, cfg.ProxmoxPassword)
		default:
			px = proxmox.New(cfg.ProxmoxBase, cfg.ProxmoxNode, cfg.ProxmoxToken)
		}
	}

	// Kubernetes client: in-cluster service account or kubeconfig.
	var kube *k8s.Client
	inCluster := false
	switch cfg.KubernetesMode {
	case config.KubernetesModeInCluster:
		inCluster = true
	case config.KubernetesModeAuto:
		inCluster = os.Getenv("KUBERNETES_SERVICE_HOST") != ""
	case config.KubernetesModeOutOfCluster:
		inCluster = false
	}
	if inCluster {
		if c, err := k8s.NewInCluster(); err == nil {
			kube = c
		} else {
			log.Printf("k8s incluster: %v", err)
		}
	} else if cfg.Kubeconfig != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		if c, err := k8s.New(cfg.Kubeconfig); err == nil {
			kube = c
		} else {
			log.Printf("k8s kubeconfig: %v", err)
		}
	}

	oidc := server.OIDC{
		Issuer:   os.Getenv("VDI_OIDC_ISSUER"),
		ClientID: os.Getenv("VDI_OIDC_CLIENT_ID"),
	}
	verifier := server.NewVerifier(oidc)

	srv := server.New(gormDB, verifier, px, cfg)

	// Backup scheduler + VM deletion delay (runs in the server).
	go backup.New(gormDB, px, cfg).Run(context.Background())

	_ = kube // Talos node cordon/drain/delete invoked by admin flows.

	addr := cfg.ListenAddr
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
