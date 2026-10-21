package backup

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"selfhosted-gaming-vdi/internal/proxmox"
	"selfhosted-gaming-vdi/models"
)

func testBackupService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Session{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(countSrv.Close)
	return &Service{db: db, px: proxmox.New(countSrv.URL, "node", "token"), interval: time.Hour, delay: time.Hour, logger: slog.Default()}, db
}

func TestBackupIncrementsCount(t *testing.T) {
	s, db := testBackupService(t)
	now := time.Now()
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-1", LastUserSession: &now})

	if err := s.tick(testCtx()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var sess models.Session
	if err := db.First(&sess, "vm_id = ?", "vm-1").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.Backups != 1 {
		t.Fatalf("expected backups=1, got %d", sess.Backups)
	}
}

func TestDeletesAfterDelay(t *testing.T) {
	s, db := testBackupService(t)
	deleted := 0
	// replace px with a counting server
	old := s.px
	_ = old
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleted++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(countSrv.Close)
	s.px = proxmox.New(countSrv.URL, "node", "token")

	last := time.Now().Add(-2 * time.Hour) // delay is 1 hour
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-del", LastUserSession: &last})

	if err := s.tick(testCtx()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if deleted != 1 {
		t.Fatalf("expected 1 delete call, got %d", deleted)
	}
	var sess models.Session
	if err := db.Unscoped().First(&sess, "vm_id = ?", "vm-del").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if !sess.DeletedAt.Valid {
		t.Fatal("expected session to be soft-deleted")
	}
}

func TestNoDeleteBeforeDelay(t *testing.T) {
	s, db := testBackupService(t)
	deleteCalled := false
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(countSrv.Close)
	s.px = proxmox.New(countSrv.URL, "node", "token")

	last := time.Now().Add(-5 * time.Minute) // delay is 1 hour
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-keep", LastUserSession: &last})

	if err := s.tick(testCtx()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if deleteCalled {
		t.Fatal("unexpected delete call before delay")
	}
	var sess models.Session
	if err := db.First(&sess, "vm_id = ?", "vm-keep").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.DeletedAt.Valid {
		t.Fatal("session should not be deleted before delay")
	}
}

func testCtx() context.Context { return context.Background() }
