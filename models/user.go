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
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	DeletedAt *time.Time `gorm:"index" json:"deleted_at,omitempty"`

	Username string `gorm:"uniqueIndex;not null" json:"username"`
	Email    string `gorm:"uniqueIndex;not null" json:"email"`
	Age      int    `gorm:"not null" json:"age"`
	Role     Role   `gorm:"not null;default:'anonymous'" json:"role"`
}
