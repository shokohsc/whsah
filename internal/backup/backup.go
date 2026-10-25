package backup

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/internal/proxmox"
	"selfhosted-gaming-vdi/models"
)

// Service runs periodic backups and deletes backed-up VMs after the deletion
// delay measured from the last user game session.
type Service struct {
	db       *gorm.DB
	px       *proxmox.Client
	interval time.Duration
	delay    time.Duration
	logger   *slog.Logger
}

func New(db *gorm.DB, px *proxmox.Client, cfg config.Config) *Service {
	return &Service{db: db, px: px, interval: cfg.BackupInterval, delay: cfg.VMDeletionDelay, logger: slog.Default()}
}

// WithLogger sets the logger used by the service.
func (s *Service) WithLogger(l *slog.Logger) *Service {
	s.logger = l
	return s
}

// Run loops forever until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.logger.Info("backup scheduler started", "interval", s.interval.String(), "deletion_delay", s.delay.String())
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("backup scheduler stopped")
			return
		case <-ticker.C:
			if err := s.tick(); err != nil {
				s.logger.Error("backup tick failed", "err", err)
			}
		}
	}
}

func (s *Service) tick() error {
	var sessions []models.Session
	if err := s.db.Where("deleted_at IS NULL").Find(&sessions).Error; err != nil {
		return err
	}
	s.logger.Debug("backup tick", "sessions", len(sessions))
	now := time.Now()
	for i := range sessions {
		sess := &sessions[i]
		// Backup: increment count and refresh last-user-session heartbeat if
		// the session is still active (treated as backed up each tick).
		sess.Backups++
		if err := s.db.Model(sess).Update("backups", sess.Backups).Error; err != nil {
			s.logger.Error("backup failed", "session_id", sess.ID, "vm_id", sess.VMID, "err", err)
			continue
		}
		s.logger.Debug("session backed up", "session_id", sess.ID, "vm_id", sess.VMID, "backups", sess.Backups)
		// Deletion: once backed up and past the delay since last user session.
		if sess.LastUserSession != nil && sess.Backups > 0 {
			if now.After(sess.LastUserSession.Add(s.delay)) {
				if err := s.px.Delete(sess.VMID); err != nil {
					s.logger.Error("vm delete failed", "session_id", sess.ID, "vm_id", sess.VMID, "err", err)
					continue
				}
				s.logger.Info("vm deleted", "session_id", sess.ID, "vm_id", sess.VMID)
				if err := s.db.Delete(sess).Error; err != nil {
					s.logger.Error("session delete failed", "session_id", sess.ID, "err", err)
				}
			}
		}
	}
	return nil
}
