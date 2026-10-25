package models

import (
	"time"

	"gorm.io/gorm"
)

// Role is a user's access level.
type Role string

const (
	RoleAnonymous Role = "anonymous"
	RoleGamer     Role = "gamer"
	RoleAdmin     Role = "admin"
)

// User is a registered user of the service.
type User struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`

	// Subject is the OIDC "sub" claim: the immutable identity key. It is empty
	// only for rows provisioned before subject binding existed.
	Subject  string `gorm:"uniqueIndex:idx_users_subject,where:subject <> '';size:255" json:"-"`
	Username string `gorm:"uniqueIndex;not null" json:"username"`
	Email    string `gorm:"uniqueIndex;not null" json:"email"`
	Age      int    `gorm:"not null" json:"age"`
	Role     Role   `gorm:"not null;default:'anonymous'" json:"role"`
	// EnvAdmin records that the role came from VDI_ADMIN_SUBJECTS, so removing
	// the subject from that list demotes the user on the next login.
	EnvAdmin bool `gorm:"not null;default:false" json:"-"`
}
