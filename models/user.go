package models

import "time"

// Role is a user's access level.
type Role string

const (
	RoleAnonymous Role = "anonymous"
	RoleGamer     Role = "gamer"
	RoleAdmin     Role = "admin"
)

// User is a registered user of the service.
type User struct {
	ID        uint      `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time `gorm:"index"`

	Username string `gorm:"uniqueIndex;not null"`
	Email    string `gorm:"uniqueIndex;not null"`
	Age      int    `gorm:"not null"`
	Role     Role   `gorm:"not null;default:'anonymous'"`
}
