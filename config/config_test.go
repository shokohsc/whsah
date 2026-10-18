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
