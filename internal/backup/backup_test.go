package backup

import (
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
	pxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(pxSrv.Close)
	return &Service{db: db, px: proxmox.New(pxSrv.URL, "node", "token"), storage: "backup", interval: time.Hour, delay: time.Hour, logger: slog.Default()}, db
}

func TestBackupIncrementsCount(t *testing.T) {
	s, db := testBackupService(t)
	now := time.Now()
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "100", LastUserSession: &now})

	if err := s.tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var sess models.Session
	if err := db.First(&sess, "vm_id = ?", "100").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.Backups != 1 {
		t.Fatalf("expected backups=1, got %d", sess.Backups)
	}
}

func TestDeletesAfterDelay(t *testing.T) {
	s, db := testBackupService(t)
	deleted := 0
	pxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted++
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(pxSrv.Close)
	s.px = proxmox.New(pxSrv.URL, "node", "token")

	last := time.Now().Add(-2 * time.Hour) // delay is 1 hour
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "100", LastUserSession: &last})

	if err := s.tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if deleted != 1 {
		t.Fatalf("expected 1 delete call, got %d", deleted)
	}
	var sess models.Session
	if err := db.Unscoped().First(&sess, "vm_id = ?", "100").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if !sess.DeletedAt.Valid {
		t.Fatal("expected session to be soft-deleted")
	}
}

func TestNoDeleteBeforeDelay(t *testing.T) {
	s, db := testBackupService(t)
	deleteCalled := false
	pxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalled = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(pxSrv.Close)
	s.px = proxmox.New(pxSrv.URL, "node", "token")

	last := time.Now().Add(-5 * time.Minute) // delay is 1 hour
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "100", LastUserSession: &last})

	if err := s.tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if deleteCalled {
		t.Fatal("unexpected delete call before delay")
	}
	var sess models.Session
	if err := db.First(&sess, "vm_id = ?", "100").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.DeletedAt.Valid {
		t.Fatal("session should not be deleted before delay")
	}
}

func TestFailedBackupDoesNotDelete(t *testing.T) {
	s, db := testBackupService(t)
	deleted := false
	pxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "backup failed", http.StatusInternalServerError)
	}))
	t.Cleanup(pxSrv.Close)
	s.px = proxmox.New(pxSrv.URL, "node", "token")

	last := time.Now().Add(-2 * time.Hour)
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "100", LastUserSession: &last})

	if err := s.tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if deleted {
		t.Fatal("deleted a VM whose backup failed")
	}
	var sess models.Session
	if err := db.First(&sess, "vm_id = ?", "100").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.Backups != 0 || sess.DeletedAt.Valid {
		t.Fatalf("unexpected session after failed backup: %+v", sess)
	}
}

func TestNoBackupStorageDoesNothing(t *testing.T) {
	s, db := testBackupService(t)
	s.storage = ""
	last := time.Now().Add(-2 * time.Hour)
	s.db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "100", LastUserSession: &last})

	if err := s.tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var sess models.Session
	if err := db.First(&sess, "vm_id = ?", "100").Error; err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.Backups != 0 || sess.DeletedAt.Valid {
		t.Fatalf("unexpected session without backup storage: %+v", sess)
	}
}
