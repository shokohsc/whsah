package backup

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/models"
	"selfhosted-gaming-vdi/internal/proxmox"
)

// Service runs periodic backups and deletes backed-up VMs after the deletion
// delay measured from the last user game session.
type Service struct {
	db      *gorm.DB
	px      *proxmox.Client
	interval time.Duration
	delay   time.Duration
}

func New(db *gorm.DB, px *proxmox.Client, cfg config.Config) *Service {
	return &Service{db: db, px: px, interval: cfg.BackupInterval, delay: cfg.VMDeletionDelay}
}

// Run loops forever until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				log.Printf("backup tick: %v", err)
			}
		}
	}
}

func (s *Service) tick(ctx context.Context) error {
	var sessions []models.Session
	if err := s.db.Where("deleted_at IS NULL").Find(&sessions).Error; err != nil {
		return err
	}
	now := time.Now()
	for i := range sessions {
		sess := &sessions[i]
		// Backup: increment count and refresh last-user-session heartbeat if
		// the session is still active (treated as backed up each tick).
		sess.Backups++
		if err := s.db.Model(sess).Update("backups", sess.Backups).Error; err != nil {
			log.Printf("backup session %d: %v", sess.ID, err)
			continue
		}
		// Deletion: once backed up and past the delay since last user session.
		if sess.LastUserSession != nil && sess.Backups > 0 {
			if now.After(sess.LastUserSession.Add(s.delay)) {
				if err := s.px.Delete(sess.VMID); err != nil {
					log.Printf("delete vm %s: %v", sess.VMID, err)
					continue
				}
				if err := s.db.Delete(sess).Error; err != nil {
					log.Printf("delete session %d: %v", sess.ID, err)
				}
			}
		}
		_ = ctx
	}
	return nil
}
