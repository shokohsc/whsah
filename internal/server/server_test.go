package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/models"
)

func testDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&models.User{}, &models.Game{}, &models.Session{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	users := []models.User{
		{Username: "dev-user", Email: "dev@example.local", Age: 30, Role: models.RoleGamer},
		{Username: "child", Email: "child@example.local", Age: 10, Role: models.RoleGamer},
		{Username: "admin", Email: "admin@example.local", Age: 40, Role: models.RoleAdmin},
	}
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
}

func seedGames(t *testing.T, db *gorm.DB) {
	t.Helper()
	games := []models.Game{
		{Title: "Kids Game", PEGIRating: "3", LibraryPath: "/smb/kids", VMConfig: models.VMConfig{CPUCores: 2, RAMSize: 2048, DiskSize: 64}},
		{Title: "Teen Game", PEGIRating: "12", LibraryPath: "/smb/teen", VMConfig: models.VMConfig{CPUCores: 4, RAMSize: 8192, DiskSize: 128}},
		{Title: "Adult Game", PEGIRating: "18", LibraryPath: "/smb/adult", VMConfig: models.VMConfig{CPUCores: 8, RAMSize: 16384, DiskSize: 256}},
	}
	for i := range games {
		if err := db.Create(&games[i]).Error; err != nil {
			t.Fatalf("seed game: %v", err)
		}
	}
}

func withUserContext(r *http.Request, sub string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxUserKey, sub))
}

func reqBody(v any) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

func TestHealth(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), config.Default())
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestConfig(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), config.Default())
	r := httptest.NewRequest(http.MethodGet, "/config", nil)
	w := httptest.NewRecorder()
	srv.handleConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("config status = %d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if out["backup_interval"] != "168h0m0s" {
		t.Fatalf("unexpected backup_interval: %v", out["backup_interval"])
	}
}

func TestListGames(t *testing.T) {
	db := testDB(t)
	seedGames(t, db)
	srv := New(db, NewVerifier(OIDC{}), config.Default())
	r := httptest.NewRequest(http.MethodGet, "/games", nil)
	w := httptest.NewRecorder()
	srv.handleListGames(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list games status = %d", w.Code)
	}
	var games []models.Game
	if err := json.Unmarshal(w.Body.Bytes(), &games); err != nil {
		t.Fatalf("decode games: %v", err)
	}
	if len(games) != 3 {
		t.Fatalf("expected 3 games, got %d", len(games))
	}
}

func TestCreateSessionAgeGate(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	seedGames(t, db)
	srv := New(db, NewVerifier(OIDC{}), config.Default())

	// adult (30) can play 18-rated game
	r := httptest.NewRequest(http.MethodPost, "/sessions", reqBody(map[string]any{"game_id": 3, "vm_id": "vm-1"}))
	r = withUserContext(r, "dev-user")
	w := httptest.NewRecorder()
	srv.handleCreateSession(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("adult create session status = %d, want %d; body=%s", w.Code, http.StatusCreated, w.Body.String())
	}

	// child (10) cannot play 18-rated game
	r = httptest.NewRequest(http.MethodPost, "/sessions", reqBody(map[string]any{"game_id": 3, "vm_id": "vm-2"}))
	r = withUserContext(r, "child")
	w = httptest.NewRecorder()
	srv.handleCreateSession(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("child create session status = %d, want %d; body=%s", w.Code, http.StatusForbidden, w.Body.String())
	}

	// child can play 3-rated game
	r = httptest.NewRequest(http.MethodPost, "/sessions", reqBody(map[string]any{"game_id": 1, "vm_id": "vm-3"}))
	r = withUserContext(r, "child")
	w = httptest.NewRecorder()
	srv.handleCreateSession(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("child kids-game status = %d, want %d; body=%s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestCreateSessionVMConfigOverride(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	seedGames(t, db)
	srv := New(db, NewVerifier(OIDC{}), config.Default())

	r := httptest.NewRequest(http.MethodPost, "/sessions", reqBody(map[string]any{
		"game_id":        1,
		"vm_id":          "vm-override",
		"cpu_cores":      8,
		"ram_size":       16384,
		"disk_size":      512,
	}))
	r = withUserContext(r, "dev-user")
	w := httptest.NewRecorder()
	srv.handleCreateSession(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create with override status = %d; body=%s", w.Code, w.Body.String())
	}

	var sess models.Session
	if err := db.First(&sess, "vm_id = ?", "vm-override").Error; err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if sess.OverrideCPUCores == nil || *sess.OverrideCPUCores != 8 {
		t.Fatalf("expected override cpu_cores 8, got %v", sess.OverrideCPUCores)
	}
	if sess.OverrideRAMSize == nil || *sess.OverrideRAMSize != 16384 {
		t.Fatalf("expected override ram_size 16384, got %v", sess.OverrideRAMSize)
	}
	if sess.OverrideDiskSize == nil || *sess.OverrideDiskSize != 512 {
		t.Fatalf("expected override disk_size 512, got %v", sess.OverrideDiskSize)
	}
}

func TestListSessionsFiltersByUser(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	seedGames(t, db)
	// dev-user owns session 1, admin owns session 2
	db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-a"})
	db.Create(&models.Session{UserID: 3, GameID: 2, VMID: "vm-b"})
	srv := New(db, NewVerifier(OIDC{}), config.Default())

	r := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	r = withUserContext(r, "dev-user")
	w := httptest.NewRecorder()
	srv.handleListSessions(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list sessions status = %d", w.Code)
	}
	var sessions []models.Session
	json.Unmarshal(w.Body.Bytes(), &sessions)
	if len(sessions) != 1 || sessions[0].VMID != "vm-a" {
		t.Fatalf("expected only dev-user's session, got %v", sessions)
	}

	// admin sees all
	r = httptest.NewRequest(http.MethodGet, "/sessions", nil)
	r = withUserContext(r, "admin")
	w = httptest.NewRecorder()
	srv.handleListSessions(w, r)
	json.Unmarshal(w.Body.Bytes(), &sessions)
	if len(sessions) != 2 {
		t.Fatalf("expected admin to see 2 sessions, got %d", len(sessions))
	}
}

func TestDeleteSessionOwnership(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-del"})
	srv := New(db, NewVerifier(OIDC{}), config.Default())

	// owner can delete
	r := httptest.NewRequest(http.MethodDelete, "/sessions/1", nil)
	r = withUserContext(r, "dev-user")
	w := httptest.NewRecorder()
	srv.handleDeleteSession(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("owner delete status = %d, want %d; body=%s", w.Code, http.StatusNoContent, w.Body.String())
	}

	// recreate and non-owner cannot delete
	db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-del2"})
	r = httptest.NewRequest(http.MethodDelete, "/sessions/2", nil)
	r = withUserContext(r, "child")
	w = httptest.NewRecorder()
	srv.handleDeleteSession(w, r)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("non-owner delete status = %d, want 403/404; body=%s", w.Code, w.Body.String())
	}

	// admin can delete any
	db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-del3"})
	r = httptest.NewRequest(http.MethodDelete, "/sessions/3", nil)
	r = withUserContext(r, "admin")
	w = httptest.NewRecorder()
	srv.handleDeleteSession(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("admin delete status = %d, want %d; body=%s", w.Code, http.StatusNoContent, w.Body.String())
	}
}

func TestOverrideVMConfig(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	db.Create(&models.Session{UserID: 1, GameID: 1, VMID: "vm-ov"})
	srv := New(db, NewVerifier(OIDC{}), config.Default())

	r := httptest.NewRequest(http.MethodPost, "/sessions/1/override", reqBody(map[string]any{"cpu_cores": 16}))
	r = withUserContext(r, "dev-user")
	w := httptest.NewRecorder()
	srv.handleOverrideVMConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("override status = %d; body=%s", w.Code, w.Body.String())
	}
	var sess models.Session
	db.First(&sess, "vm_id = ?", "vm-ov")
	if sess.OverrideCPUCores == nil || *sess.OverrideCPUCores != 16 {
		t.Fatalf("expected override cpu_cores 16, got %v", sess.OverrideCPUCores)
	}
}

func TestCreateGameRequiresAdmin(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	srv := New(db, NewVerifier(OIDC{}), config.Default())

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	protected := srv.requireRole(models.RoleAdmin)(inner)

	// gamer cannot access admin-only endpoint
	r := httptest.NewRequest(http.MethodPost, "/games", nil)
	r = withUserContext(r, "dev-user")
	w := httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("gamer admin access status = %d, want 403; body=%s", w.Code, w.Body.String())
	}

	// admin can access admin-only endpoint
	r = httptest.NewRequest(http.MethodPost, "/games", nil)
	r = withUserContext(r, "admin")
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin access status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// handleCreateGame itself works when called directly (auth already passed)
	r = httptest.NewRequest(http.MethodPost, "/games", reqBody(map[string]any{"title": "Y", "pegi_rating": "12"}))
	r = withUserContext(r, "admin")
	w = httptest.NewRecorder()
	srv.handleCreateGame(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("handleCreateGame status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
}
