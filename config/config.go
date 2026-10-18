package config

import "time"

// DefaultBackupInterval is the default interval between VM backups (once per week).
const DefaultBackupInterval = 7 * 24 * time.Hour

// DefaultVMDeletionDelay is the default delay after which a VM is deleted once
// backed up, measured from the last user game session (1 month).
const DefaultVMDeletionDelay = 30 * 24 * time.Hour

// Config holds the service-wide, user-facing configurable durations.
type Config struct {
	// BackupInterval is how often VM sessions are backed up.
	BackupInterval time.Duration
	// VMDeletionDelay is how long after the last user game session a backed-up
	// VM is deleted.
	VMDeletionDelay time.Duration
	// DatabaseDSN is the PostgreSQL connection string.
	DatabaseDSN string
}

// Default returns a Config populated with the plan.md defaults.
func Default() Config {
	return Config{
		BackupInterval:  DefaultBackupInterval,
		VMDeletionDelay: DefaultVMDeletionDelay,
	}
}
