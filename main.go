package main

import (
	"fmt"
	"log"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/db"
)

func main() {
	cfg := config.Default()
	fmt.Printf("backup interval: %v\nvm deletion delay: %v\n", cfg.BackupInterval, cfg.VMDeletionDelay)

	// In production, set cfg.DatabaseDSN from env/flags and call:
	// gormDB, err := db.Open(cfg)
	// if err != nil { log.Fatal(err) }
	// if err := db.Migrate(gormDB); err != nil { log.Fatal(err) }
	_ = db.Open
	log.Println("config loaded; ready to connect to PostgreSQL")
}
