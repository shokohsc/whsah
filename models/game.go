package models

import "time"

// VMConfig is the VM configuration exposed to games and to user overrides.
type VMConfig struct {
	CPUCores int `gorm:"not null;default:2"`   // number of CPU cores
	RAMSize  int `gorm:"not null;default:4096"` // RAM size in MB
	DiskSize int `gorm:"not null;default:128"`  // disk size in GB
}

// Game is a playable game with its metadata and default VM configuration.
type Game struct {
	ID          uint      `gorm:"primaryKey"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time `gorm:"index"`

	Title       string    `gorm:"not null"`
	Metadata    string    `gorm:"not null"` // JSON-serialized game metadata
	LibraryPath string    `gorm:"not null"` // SMB share path mounted in the VM

	VMConfig VMConfig `gorm:"embedded"` // cpu cores, ram size, disk size

	PEGIRating string `gorm:"not null"`
}
