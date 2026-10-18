package models

import "time"

// VMConfig is the VM configuration exposed to games and to user overrides.
type VMConfig struct {
	CPUCores int `gorm:"not null;default:2" json:"cpu_cores"`   // number of CPU cores
	RAMSize  int `gorm:"not null;default:4096" json:"ram_size"` // RAM size in MB
	DiskSize int `gorm:"not null;default:128" json:"disk_size"` // disk size in GB
}

// Game is a playable game with its metadata and default VM configuration.
type Game struct {
	ID          uint        `gorm:"primaryKey" json:"id"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	DeletedAt   *time.Time  `gorm:"index" json:"deleted_at,omitempty"`

	Title       string    `gorm:"not null" json:"title"`
	Metadata    string    `gorm:"not null" json:"metadata"` // JSON-serialized game metadata
	LibraryPath string    `gorm:"not null" json:"library_path"` // SMB share path mounted in the VM

	VMConfig VMConfig `gorm:"embedded" json:"vm_config"` // cpu cores, ram size, disk size

	PEGIRating string `gorm:"not null" json:"pegi_rating"`
}
