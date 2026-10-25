package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/internal/proxmox"
	"selfhosted-gaming-vdi/internal/worker"
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
	if err := db.AutoMigrate(&models.User{}, &models.Game{}, &models.Session{}, &models.GameJob{}, &models.LibraryPath{}); err != nil {
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
	return r.WithContext(context.WithValue(r.Context(), ctxUserKey, Identity{Subject: sub}))
}

func reqBody(v any) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

func TestHealth(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestConfig(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
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
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
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
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

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
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	r := httptest.NewRequest(http.MethodPost, "/sessions", reqBody(map[string]any{
		"game_id":   1,
		"vm_id":     "vm-override",
		"cpu_cores": 8,
		"ram_size":  16384,
		"disk_size": 512,
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
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

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
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

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
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

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
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

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

func TestAgentVMProxyRequiresToken(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	r := httptest.NewRequest(http.MethodPost, "/agent/vm/100/start", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401; body=%s", w.Code, w.Body.String())
	}

	r = httptest.NewRequest(http.MethodPost, "/agent/vm/100/start", nil)
	r.Header.Set("Authorization", "Bearer wrong-token")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

func TestAgentVMProxyStartStopConfig(t *testing.T) {
	db := testDB(t)
	var started, stopped, configRequested []string
	pxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		vmid := ""
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] == "qemu" && i+1 < len(parts) {
				vmid = parts[i+1]
				break
			}
		}
		switch {
		case strings.Contains(r.URL.Path, "/status/start"):
			started = append(started, vmid)
		case strings.Contains(r.URL.Path, "/status/stop"):
			stopped = append(stopped, vmid)
		case strings.Contains(r.URL.Path, "/qemu/") && strings.Contains(r.URL.Path, "/config"):
			configRequested = append(configRequested, vmid)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": 100, "cores": 4, "memory": 8192, "size": 128}})
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer pxSrv.Close()

	px := proxmox.New(pxSrv.URL, "pve-node", "token")
	srv := New(db, NewVerifier(OIDC{}), px, config.Config{AgentToken: "agent-secret"})

	// start
	r := httptest.NewRequest(http.MethodPost, "/agent/vm/100/start", nil)
	r.Header.Set("Authorization", "Bearer agent-secret")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("start status = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	// stop
	r = httptest.NewRequest(http.MethodPost, "/agent/vm/100/stop", nil)
	r.Header.Set("Authorization", "Bearer agent-secret")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("stop status = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	// config
	r = httptest.NewRequest(http.MethodGet, "/agent/vm/100/config", nil)
	r.Header.Set("Authorization", "Bearer agent-secret")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("config status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	if len(started) != 1 || started[0] != "100" {
		t.Fatalf("unexpected started: %v", started)
	}
	if len(stopped) != 1 || stopped[0] != "100" {
		t.Fatalf("unexpected stopped: %v", stopped)
	}
	if len(configRequested) != 1 || configRequested[0] != "100" {
		t.Fatalf("unexpected config requested: %v", configRequested)
	}
}

func TestEnqueueGameJob(t *testing.T) {
	db := testDB(t)
	seedGames(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	// unknown kind -> 400
	r := httptest.NewRequest(http.MethodPost, "/games/1/jobs", reqBody(map[string]any{"kind": "bogus"}))
	r = withUserContext(r, "admin")
	w := httptest.NewRecorder()
	srv.handleEnqueueGameJob(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind status = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	// missing game -> 404
	r = httptest.NewRequest(http.MethodPost, "/games/999/jobs", reqBody(map[string]any{"kind": "config"}))
	r = withUserContext(r, "admin")
	w = httptest.NewRecorder()
	srv.handleEnqueueGameJob(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing game status = %d, want 404; body=%s", w.Code, w.Body.String())
	}

	// config job with explicit payload -> 202
	r = httptest.NewRequest(http.MethodPost, "/games/2/jobs", reqBody(map[string]any{
		"kind":    "config",
		"payload": map[string]any{"cpu_cores": 4, "ram_size": 8192, "disk_size": 128},
	}))
	r = withUserContext(r, "admin")
	w = httptest.NewRecorder()
	srv.handleEnqueueGameJob(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("config job status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var job models.GameJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.GameID != 2 || job.Kind != models.JobKindConfig || job.Status != models.JobStatusPending {
		t.Fatalf("unexpected job: %+v", job)
	}
	if !strings.Contains(job.Payload, "cpu_cores") {
		t.Fatalf("payload not stored: %q", job.Payload)
	}

	// metadata job -> 202
	r = httptest.NewRequest(http.MethodPost, "/games/1/jobs", reqBody(map[string]any{"kind": "metadata"}))
	r = withUserContext(r, "admin")
	w = httptest.NewRecorder()
	srv.handleEnqueueGameJob(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("metadata job status = %d, want 202; body=%s", w.Code, w.Body.String())
	}

	var stored models.GameJob
	if err := db.First(&stored, job.ID).Error; err != nil {
		t.Fatalf("stored job: %v", err)
	}
	if stored.Status != models.JobStatusPending {
		t.Fatalf("expected pending, got %s", stored.Status)
	}
}

func TestEnqueueLibraryJob(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	r := httptest.NewRequest(http.MethodPost, "/library/jobs", nil)
	r = withUserContext(r, "admin")
	w := httptest.NewRecorder()
	srv.handleEnqueueLibraryJob(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("library job status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var job models.GameJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.Kind != models.JobKindLibrary || job.Status != models.JobStatusPending {
		t.Fatalf("unexpected job: %+v", job)
	}
	if job.GameID != models.LibraryJobGameID {
		t.Fatalf("game id = %d, want %d", job.GameID, models.LibraryJobGameID)
	}
}

func TestLibraryJobRouteRequiresAdmin(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	// no token -> 401
	r := httptest.NewRequest(http.MethodPost, "/library/jobs", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", w.Code)
	}

	// gamer -> 403
	r = httptest.NewRequest(http.MethodPost, "/library/jobs", nil)
	r.Header.Set("Authorization", "Bearer any")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("gamer status = %d, want 403", w.Code)
	}

	// admin -> 202 (dev mode maps every token to the dev-user identity)
	if err := db.Model(&models.User{}).Where("username = ?", "dev-user").Update("role", models.RoleAdmin).Error; err != nil {
		t.Fatalf("promote user: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/library/jobs", nil)
	r.Header.Set("Authorization", "Bearer any")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("admin status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestLibraryPathsCRUD(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	r := httptest.NewRequest(http.MethodPost, "/library/paths", reqBody(map[string]any{"category": "isos", "path": "/games/isos"}))
	w := httptest.NewRecorder()
	srv.handleCreateLibraryPath(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var lib models.LibraryPath
	if err := json.Unmarshal(w.Body.Bytes(), &lib); err != nil {
		t.Fatalf("decode library path: %v", err)
	}
	if lib.Category != models.CategoryISOs || lib.Path != "/games/isos" {
		t.Fatalf("unexpected library path: %+v", lib)
	}

	r = httptest.NewRequest(http.MethodPost, "/library/paths", reqBody(map[string]any{"category": "isos", "path": "/games/isos"}))
	w = httptest.NewRecorder()
	srv.handleCreateLibraryPath(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/library/paths", reqBody(map[string]any{"category": "other", "path": "/games/other"}))
	w = httptest.NewRecorder()
	srv.handleCreateLibraryPath(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid category status = %d, want 400", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/library/paths", nil)
	w = httptest.NewRecorder()
	srv.handleListLibraryPaths(w, r)
	var paths []models.LibraryPath
	if err := json.Unmarshal(w.Body.Bytes(), &paths); err != nil {
		t.Fatalf("decode paths: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d", len(paths))
	}

	r = httptest.NewRequest(http.MethodDelete, "/library/paths/1", nil)
	r.SetPathValue("id", "1")
	w = httptest.NewRecorder()
	srv.handleDeleteLibraryPath(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", w.Code)
	}
	var count int64
	db.Model(&models.LibraryPath{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 paths, got %d", count)
	}
}

func TestResetLibrary(t *testing.T) {
	db := testDB(t)
	seedGames(t, db)
	db.Create(&models.LibraryPath{Category: models.CategoryRepacks, Path: "/games/repacks"})
	worker.Enqueue(db, 1, models.JobKindConfig, nil)
	worker.Enqueue(db, 1, models.JobKindMetadata, nil)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	r := httptest.NewRequest(http.MethodDelete, "/library", nil)
	w := httptest.NewRecorder()
	srv.handleResetLibrary(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var out map[string]int64
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode reset: %v", err)
	}
	if out["games"] != 3 || out["jobs"] != 2 {
		t.Fatalf("unexpected reset counts: %+v", out)
	}

	var games, jobs, paths int64
	db.Model(&models.Game{}).Count(&games)
	db.Model(&models.GameJob{}).Count(&jobs)
	db.Model(&models.LibraryPath{}).Count(&paths)
	if games != 0 || jobs != 0 {
		t.Fatalf("expected games/jobs cleared, got %d/%d", games, jobs)
	}
	if paths != 1 {
		t.Fatalf("expected library paths kept, got %d", paths)
	}
}

func TestListGameJobs(t *testing.T) {
	db := testDB(t)
	seedGames(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	worker.Enqueue(db, 1, models.JobKindConfig, nil)
	worker.Enqueue(db, 1, models.JobKindMetadata, nil)
	worker.Enqueue(db, 2, models.JobKindConfig, nil)

	r := httptest.NewRequest(http.MethodGet, "/games/1/jobs", nil)
	r = withUserContext(r, "admin")
	w := httptest.NewRecorder()
	srv.handleListGameJobs(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list jobs status = %d; body=%s", w.Code, w.Body.String())
	}
	var jobs []models.GameJob
	if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs for game 1, got %d", len(jobs))
	}
}

func TestGetJob(t *testing.T) {
	db := testDB(t)
	seedGames(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
	job, err := worker.Enqueue(db, 1, models.JobKindConfig, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// invalid id
	r := httptest.NewRequest(http.MethodGet, "/jobs/abc", nil)
	r = withUserContext(r, "dev-user")
	w := httptest.NewRecorder()
	srv.handleGetJob(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d, want 400", w.Code)
	}

	// missing job
	r = httptest.NewRequest(http.MethodGet, "/jobs/9999", nil)
	r = withUserContext(r, "dev-user")
	w = httptest.NewRecorder()
	srv.handleGetJob(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing job status = %d, want 404", w.Code)
	}

	// found
	r = httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.Itoa(int(job.ID)), nil)
	r = withUserContext(r, "dev-user")
	w = httptest.NewRecorder()
	srv.handleGetJob(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("get job status = %d; body=%s", w.Code, w.Body.String())
	}
	var got models.GameJob
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if got.ID != job.ID || got.Kind != models.JobKindConfig {
		t.Fatalf("unexpected job: %+v", got)
	}
}

func TestEnsureUserProvisionsOnFirstLogin(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Config{DefaultAge: 18})

	u, err := srv.ensureUser(Identity{Subject: "sub-1", Email: "one@example.local", Username: "one"})
	if err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if u.Role != models.RoleGamer || u.Age != 18 || u.Username != "one" {
		t.Fatalf("unexpected provisioned user: %+v", u)
	}

	// second login returns the same row, not a duplicate
	again, err := srv.ensureUser(Identity{Subject: "sub-1", Email: "one@example.local"})
	if err != nil || again.ID != u.ID {
		t.Fatalf("expected same user, got %+v err=%v", again, err)
	}
	var count int64
	db.Model(&models.User{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 user, got %d", count)
	}
}

func TestEnsureUserAdminSubjectAndPromotion(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Config{DefaultAge: 18, AdminSubjects: []string{"boss"}})

	u, err := srv.ensureUser(Identity{Subject: "boss", Email: "boss@example.local"})
	if err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if u.Role != models.RoleAdmin {
		t.Fatalf("expected admin, got %s", u.Role)
	}

	// an existing non-admin listed as admin subject is promoted
	db.Create(&models.User{Username: "promo", Email: "promo@example.local", Age: 20, Role: models.RoleGamer})
	srv.cfg.AdminSubjects = []string{"promo"}
	u, err = srv.ensureUser(Identity{Subject: "promo", Email: "promo@example.local"})
	if err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if u.Role != models.RoleAdmin {
		t.Fatalf("expected promotion to admin, got %s", u.Role)
	}
}

func TestEnsureUserAdminMatchedByUsername(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Config{DefaultAge: 18, AdminSubjects: []string{"alice"}})

	// Pocket ID subjects are opaque IDs, so the username claim identifies the admin.
	u, err := srv.ensureUser(Identity{Subject: "8f14e45f-ea1c-4a3b-9f2b-1b6f5f0a2c3d", Username: "alice"})
	if err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if u.Role != models.RoleAdmin {
		t.Fatalf("expected admin, got %s", u.Role)
	}
}

func TestCORSAllowsFrontendOrigin(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Config{FrontendOrigin: "https://domain.tld"})

	r := httptest.NewRequest(http.MethodOptions, "/games", nil)
	r.Header.Set("Origin", "https://domain.tld")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://domain.tld" {
		t.Fatalf("allow-origin = %q", got)
	}

	// a different origin gets no CORS headers
	r = httptest.NewRequest(http.MethodGet, "/games", nil)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unexpected CORS header for foreign origin")
	}
}

func TestJobRoutesAuth(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	seedGames(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
	job, _ := worker.Enqueue(db, 1, models.JobKindConfig, nil)

	// POST /games/{id}/jobs requires a token and the admin role
	r := httptest.NewRequest(http.MethodPost, "/games/1/jobs", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token enqueue status = %d, want 401", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/games/1/jobs", nil)
	r.Header.Set("Authorization", "Bearer any")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("gamer enqueue status = %d, want 403", w.Code)
	}

	// GET /jobs/{id} requires a token
	r = httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.Itoa(int(job.ID)), nil)
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token job status = %d, want 401", w.Code)
	}

	// GET /jobs/{id} with a token succeeds (dev mode accepts any token)
	r = httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.Itoa(int(job.ID)), nil)
	r.Header.Set("Authorization", "Bearer any")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("token job status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateUserRole(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	// invalid role -> 400
	r := httptest.NewRequest(http.MethodPut, "/users/2", reqBody(map[string]any{"role": "bogus"}))
	w := httptest.NewRecorder()
	srv.handleUpdateUserRole(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid role status = %d, want 400", w.Code)
	}

	// missing user -> 404
	r = httptest.NewRequest(http.MethodPut, "/users/999", reqBody(map[string]any{"role": "admin"}))
	w = httptest.NewRecorder()
	srv.handleUpdateUserRole(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing user status = %d, want 404", w.Code)
	}

	// promote child (id 2) to admin
	r = httptest.NewRequest(http.MethodPut, "/users/2", reqBody(map[string]any{"role": "admin"}))
	w = httptest.NewRecorder()
	srv.handleUpdateUserRole(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("update role status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var u models.User
	db.First(&u, 2)
	if u.Role != models.RoleAdmin {
		t.Fatalf("role = %s, want admin", u.Role)
	}

	// gamer cannot update roles (dev mode resolves any token to a gamer)
	r = httptest.NewRequest(http.MethodPut, "/users/1", reqBody(map[string]any{"role": "admin"}))
	r.Header.Set("Authorization", "Bearer any")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("gamer update role status = %d, want 403", w.Code)
	}
}

func TestAuthLoginDevModeRedirectsWithToken(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
	r := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("auth login status = %d, want 302", w.Code)
	}
	want := config.DefaultFrontendOrigin + "/#/login?token=dev-token"
	if got := w.Header().Get("Location"); got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
}

func TestAuthCallbackRejectsBadState(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
	r := httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state=y", nil)
	w := httptest.NewRecorder()
	srv.handleAuthCallback(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); !strings.Contains(got, "error=invalid+oauth+state") {
		t.Fatalf("callback redirect = %q, want invalid state error", got)
	}
}

func TestAccessLogRecordsRequest(t *testing.T) {
	db := testDB(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default()).WithLogger(logger)

	r := httptest.NewRequest(http.MethodGet, "/health?token=secret", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)

	line := buf.String()
	if !strings.Contains(line, `"msg":"http request"`) {
		t.Fatalf("access log missing: %s", line)
	}
	if !strings.Contains(line, `"path":"/health"`) {
		t.Fatalf("access log missing path: %s", line)
	}
	if strings.Contains(line, "secret") {
		t.Fatalf("access log leaked query string: %s", line)
	}
}

func TestCreateCustomVM(t *testing.T) {
	db := testDB(t)
	var cloned, configured, resized bool
	pxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes/pve-node/qemu/9000/clone":
			cloned = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": 100}})
		case "/api2/json/nodes/pve-node/qemu/100/config":
			configured = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil})
		case "/api2/json/nodes/pve-node/qemu/100/resize":
			resized = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil})
		default:
			http.Error(w, "bad path", http.StatusNotFound)
		}
	}))
	defer pxSrv.Close()

	srv := New(db, NewVerifier(OIDC{}), proxmox.New(pxSrv.URL, "pve-node", "token"), config.Default())
	r := httptest.NewRequest(http.MethodPost, "/admin/vms", reqBody(map[string]any{
		"template_id": 9000,
		"name":        "custom-vm",
		"cpu_cores":   4,
		"ram_size":    8192,
		"disk_size":   256,
		"vgpu":        "virtio",
	}))
	w := httptest.NewRecorder()
	srv.handleCreateVM(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create vm status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out["vmid"] != float64(100) {
		t.Fatalf("vmid = %v", out["vmid"])
	}
	if !cloned || !configured || !resized {
		t.Fatalf("cloned=%v configured=%v resized=%v", cloned, configured, resized)
	}
}

func TestCreateCustomVMValidation(t *testing.T) {
	db := testDB(t)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	valid := map[string]any{
		"template_id": 9000,
		"name":        "custom-vm",
		"cpu_cores":   4,
		"ram_size":    8192,
		"disk_size":   256,
		"vgpu":        "virtio",
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		status int
	}{
		{"missing template", func(m map[string]any) { delete(m, "template_id") }, http.StatusBadRequest},
		{"invalid name", func(m map[string]any) { m["name"] = "-bad name" }, http.StatusBadRequest},
		{"invalid cpu", func(m map[string]any) { m["cpu_cores"] = 0 }, http.StatusBadRequest},
		{"invalid ram", func(m map[string]any) { m["ram_size"] = 128 }, http.StatusBadRequest},
		{"invalid disk", func(m map[string]any) { m["disk_size"] = 0 }, http.StatusBadRequest},
		{"invalid vgpu", func(m map[string]any) { m["vgpu"] = "nope" }, http.StatusBadRequest},
		{"proxmox not configured", func(m map[string]any) {}, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{}
			for k, v := range valid {
				body[k] = v
			}
			tc.mutate(body)
			r := httptest.NewRequest(http.MethodPost, "/admin/vms", reqBody(body))
			w := httptest.NewRecorder()
			srv.handleCreateVM(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestCreateVMRouteRequiresAdmin(t *testing.T) {
	db := testDB(t)
	seedUsers(t, db)
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())

	// no token -> 401
	r := httptest.NewRequest(http.MethodPost, "/admin/vms", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", w.Code)
	}

	// gamer -> 403
	r = httptest.NewRequest(http.MethodPost, "/admin/vms", nil)
	r.Header.Set("Authorization", "Bearer any")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("gamer status = %d, want 403", w.Code)
	}

	// admin -> reaches the handler (503 because Proxmox is not configured)
	if err := db.Model(&models.User{}).Where("username = ?", "dev-user").Update("role", models.RoleAdmin).Error; err != nil {
		t.Fatalf("promote user: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/admin/vms", reqBody(map[string]any{
		"template_id": 9000,
		"name":        "custom-vm",
		"cpu_cores":   4,
		"ram_size":    8192,
		"disk_size":   256,
		"vgpu":        "virtio",
	}))
	r.Header.Set("Authorization", "Bearer any")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

func TestListGamesIncludesSanitizedTitle(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.Game{Title: "Hades [FitGirl Repack]", PEGIRating: "12", LibraryPath: "/games/hades"}).Error; err != nil {
		t.Fatalf("seed game: %v", err)
	}
	srv := New(db, NewVerifier(OIDC{}), nil, config.Default())
	r := httptest.NewRequest(http.MethodGet, "/games", nil)
	w := httptest.NewRecorder()
	srv.handleListGames(w, r)

	var games []struct {
		Title          string `json:"title"`
		SanitizedTitle string `json:"sanitized_title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &games); err != nil {
		t.Fatalf("decode games: %v", err)
	}
	if len(games) != 1 || games[0].Title != "Hades [FitGirl Repack]" || games[0].SanitizedTitle != "Hades" {
		t.Fatalf("unexpected games: %+v", games)
	}
}
