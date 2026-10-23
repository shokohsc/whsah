package config

import "time"

// DefaultBackupInterval is the default interval between VM backups (once per week).
const DefaultBackupInterval = 7 * 24 * time.Hour

// DefaultVMDeletionDelay is the default delay after which a VM is deleted once
// backed up, measured from the last user game session (1 month).
const DefaultVMDeletionDelay = 30 * 24 * time.Hour

// DefaultIdleSecs is the default agent idle shutdown delay (30 minutes).
const DefaultIdleSecs = 1800

// DefaultFrontendOrigin is the browser origin allowed to call the API from
// the main site (the API itself is served from the api.<domain> subdomain).
const DefaultFrontendOrigin = "https://domain.tld"

// DefaultPublicURL is the public base URL of the API server (the api.<domain>
// subdomain), used to build the OIDC redirect URI.
const DefaultPublicURL = "https://api.domain.tld"

// DefaultAge is the age assigned to a user auto-provisioned on first login.
// OIDC carries no standard age claim, so admins adjust it per user afterwards.
const DefaultAge = 18

// ProxmoxAuthMode selects how the server authenticates to Proxmox VE.
type ProxmoxAuthMode string

const (
	// ProxmoxAuthToken uses a PVE API token (PVEAPIToken header).
	ProxmoxAuthToken ProxmoxAuthMode = "token"
	// ProxmoxAuthPassword uses username/password (ticket + CSRF token).
	ProxmoxAuthPassword ProxmoxAuthMode = "password"
)

// KubernetesMode selects how the server authenticates to Kubernetes.
type KubernetesMode string

const (
	// KubernetesModeAuto detects in-cluster vs out-of-cluster automatically.
	KubernetesModeAuto KubernetesMode = "auto"
	// KubernetesModeInCluster forces use of the in-cluster service account.
	KubernetesModeInCluster KubernetesMode = "incluster"
	// KubernetesModeOutOfCluster forces use of a kubeconfig file.
	KubernetesModeOutOfCluster KubernetesMode = "outofcluster"
)

// Config holds the service-wide, user-facing configurable values.
type Config struct {
	// BackupInterval is how often VM sessions are backed up.
	BackupInterval time.Duration
	// VMDeletionDelay is how long after the last user game session a backed-up
	// VM is deleted.
	VMDeletionDelay time.Duration
	// DatabaseDSN is the PostgreSQL connection string.
	DatabaseDSN string
	// ListenAddr is the HTTP listen address.
	ListenAddr string
	// IdleSecs is the agent idle shutdown delay in seconds.
	IdleSecs uint

	// ProxmoxBase is the Proxmox VE API base URL.
	ProxmoxBase string
	// ProxmoxNode is the Proxmox node name.
	ProxmoxNode string
	// ProxmoxAuthMode selects token or password authentication.
	ProxmoxAuthMode ProxmoxAuthMode
	// ProxmoxToken is the API token secret (token mode): "userid!tokenid=secret".
	ProxmoxToken string
	// ProxmoxUser is the Proxmox VE username (password mode).
	ProxmoxUser string
	// ProxmoxPassword is the Proxmox VE password (password mode).
	ProxmoxPassword string

	// LibraryPath is the default game library mount path used by the agent.
	LibraryPath string

	// MetadataURL is the custom HTTP metadata source used by game metadata
	// jobs (empty = custom source disabled).
	MetadataURL string
	// RAWGAPIKey enables the RAWG metadata provider.
	RAWGAPIKey string
	// BrowserlessURL is the browserless instance used to scrape FitGirl
	// (empty = FitGirl scraping disabled).
	BrowserlessURL string
	// BrowserlessToken authenticates to the browserless instance.
	BrowserlessToken string
	// FitGirlSearchURL is the FitGirl search endpoint; {query} is replaced
	// with the URL-escaped game title.
	FitGirlSearchURL string
	// YouTubeAPIKey enables the YouTube provider for live streams and replays.
	YouTubeAPIKey string

	// LokiURL is the remote Loki endpoint for agent logs (empty = disabled).
	LokiURL string

	// KubernetesMode selects in-cluster vs out-of-cluster auth.
	KubernetesMode KubernetesMode
	// Kubeconfig is the path to a kubeconfig file (out-of-cluster mode).
	Kubeconfig string

	// AgentToken is the shared token agents use to authenticate to the server.
	AgentToken string

	// FrontendOrigin is the browser origin allowed by CORS to call the API
	// (empty disables CORS).
	FrontendOrigin string
	// OIDCPublicURL is the public base URL of this server, used to build the
	// OIDC redirect URI (<OIDCPublicURL>/auth/callback).
	OIDCPublicURL string
	// OIDCClientSecret is the confidential client secret used to exchange the
	// authorization code for tokens (empty for public clients).
	OIDCClientSecret string
	// AdminSubjects are OIDC subjects auto-provisioned (or promoted) to admin.
	AdminSubjects []string
	// DefaultAge is the age assigned to users auto-provisioned on first login.
	DefaultAge int
}

// Default returns a Config populated with the plan.md defaults.
func Default() Config {
	return Config{
		BackupInterval:  DefaultBackupInterval,
		VMDeletionDelay: DefaultVMDeletionDelay,
		ListenAddr:      ":8080",
		IdleSecs:        DefaultIdleSecs,
		ProxmoxAuthMode: ProxmoxAuthToken,
		KubernetesMode:  KubernetesModeAuto,
		FrontendOrigin:  DefaultFrontendOrigin,
		OIDCPublicURL:   DefaultPublicURL,
		DefaultAge:      DefaultAge,
	}
}
