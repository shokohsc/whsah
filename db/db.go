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
	return db.AutoMigrate(
		&models.User{},
		&models.Game{},
		&models.Session{},
		&models.GameJob{},
	)
}
