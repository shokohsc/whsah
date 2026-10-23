package models

import "time"

// LibraryCategory is the kind of game files a library path holds.
type LibraryCategory string

const (
	CategoryRepacks   LibraryCategory = "repacks"
	CategoryPortables LibraryCategory = "portables"
	CategoryISOs      LibraryCategory = "isos"
	CategorySetups    LibraryCategory = "setups"
)

// LibraryPath is a directory holding game folders, configured by an admin.
type LibraryPath struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Category LibraryCategory `gorm:"not null;uniqueIndex:idx_library_category_path" json:"category"`
	Path     string          `gorm:"not null;uniqueIndex:idx_library_category_path" json:"path"`
}

// ValidLibraryCategory reports whether c is one of the supported categories.
func ValidLibraryCategory(c LibraryCategory) bool {
	switch c {
	case CategoryRepacks, CategoryPortables, CategoryISOs, CategorySetups:
		return true
	}
	return false
}
