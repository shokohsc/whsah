package db

import (
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/models"
)

// Open connects to PostgreSQL using GORM.
func Open(cfg config.Config) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(cfg.DatabaseDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
}

// Migrate auto-migrates the main tables (users, games, sessions, game jobs).
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&models.User{},
		&models.Game{},
		&models.Session{},
		&models.GameJob{},
		&models.LibraryPath{},
	); err != nil {
		return err
	}
	// Older versions made provider usernames/emails unique. They are display
	// attributes now; duplicate values must not break login.
	for _, idx := range []string{"idx_users_username", "idx_users_email"} {
		if db.Migrator().HasIndex(&models.User{}, idx) {
			if err := db.Migrator().DropIndex(&models.User{}, idx); err != nil {
				return err
			}
		}
	}
	return nil
}
