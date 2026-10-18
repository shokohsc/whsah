package config

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.BackupInterval != 7*24*time.Hour {
		t.Fatalf("expected weekly backup interval, got %v", cfg.BackupInterval)
	}
	if cfg.VMDeletionDelay != 30*24*time.Hour {
		t.Fatalf("expected 1-month deletion delay, got %v", cfg.VMDeletionDelay)
	}
}

func TestDefaultsFromEnv(t *testing.T) {
	// VDI_BACKUP_INTERVAL and VDI_VM_DELETION_DELAY are parsed in main, but
	// the defaults themselves must remain stable.
	cfg := Default()
	if cfg.BackupInterval <= 0 {
		t.Fatal("backup interval must be positive")
	}
	if cfg.VMDeletionDelay <= 0 {
		t.Fatal("vm deletion delay must be positive")
	}
}
