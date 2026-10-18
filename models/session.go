package models

import "time"

// Session is an active or finished user game session on a VM.
type Session struct {
	ID        uint      `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time `gorm:"index"`

	UserID          uint      `gorm:"index;not null"`
	GameID          uint      `gorm:"index;not null"`
	VMID            string    `gorm:"not null"` // Proxmox VM identifier
	LastUserSession *time.Time // last user game session (drives deletion delay)
	Backups         int       `gorm:"not null;default:0"` // number of backups taken
}
