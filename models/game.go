package models

import (
	"time"

	"gorm.io/gorm"
)

// VMConfig is the VM configuration exposed to games and to user overrides.
type VMConfig struct {
	CPUCores int `gorm:"not null;default:2" json:"cpu_cores"`   // number of CPU cores
	RAMSize  int `gorm:"not null;default:4096" json:"ram_size"` // RAM size in MB
	DiskSize int `gorm:"not null;default:128" json:"disk_size"` // disk size in GB
}

// Game is a playable game with its metadata and default VM configuration.
type Game struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`

	Title       string          `gorm:"not null" json:"title"`
	Metadata    string          `gorm:"not null" json:"metadata"`            // JSON-serialized game metadata
	LibraryPath string          `gorm:"not null" json:"library_path"`        // SMB share path mounted in the VM
	Category    LibraryCategory `gorm:"not null;default:''" json:"category"` // library category the game came from

	VMConfig VMConfig `gorm:"embedded" json:"vm_config"` // cpu cores, ram size, disk size

	PEGIRating string `gorm:"not null" json:"pegi_rating"`
}

// pegiFloors maps a PEGI rating to its minimum age. Unknown ratings have no
// floor and are denied by the server.
var pegiFloors = map[string]int{"3": 3, "7": 7, "12": 12, "16": 16, "18": 18}

// ValidPEGIRating reports whether s is one of the five PEGI ratings.
func ValidPEGIRating(s string) bool {
	_, ok := pegiFloors[s]
	return ok
}

// PEGIFloor returns the minimum age for a PEGI rating. The second result is
// false for an unknown rating.
func PEGIFloor(s string) (int, bool) {
	floor, ok := pegiFloors[s]
	return floor, ok
}

// SanitizedName returns the cleaned game name used for metadata lookups.
func (g *Game) SanitizedName() string {
	if g == nil {
		return ""
	}
	return SanitizeGameName(g.Title)
}
