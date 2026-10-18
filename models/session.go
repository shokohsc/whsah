package models

import "time"

// Session is an active or finished user game session on a VM.
type Session struct {
	ID        uint        `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	DeletedAt *time.Time  `gorm:"index" json:"deleted_at,omitempty"`

	UserID   uint   `gorm:"index;not null" json:"user_id"`
	GameID   uint   `gorm:"index;not null" json:"game_id"`
	VMID     string `gorm:"not null" json:"vm_id"` // Proxmox VM identifier
	Backups  int    `gorm:"not null;default:0" json:"backups"` // number of backups taken

	// LastUserSession drives the VM deletion delay (default 1 month after it).
	LastUserSession *time.Time `json:"last_user_session,omitempty"`
	// Per-session VM config overrides (cpu cores, ram size, disk size).
	OverrideCPUCores *int `gorm:"default:null" json:"override_cpu_cores,omitempty"`
	OverrideRAMSize  *int `gorm:"default:null" json:"override_ram_size,omitempty"`
	OverrideDiskSize *int `gorm:"default:null" json:"override_disk_size,omitempty"`
}
