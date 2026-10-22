package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"gorm.io/gorm"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/internal/logging"
	"selfhosted-gaming-vdi/internal/proxmox"
	"selfhosted-gaming-vdi/internal/worker"
	"selfhosted-gaming-vdi/models"
)

type Server struct {
	db       *gorm.DB
	verifier *Verifier
	px       *proxmox.Client
	cfg      config.Config
	logger   *slog.Logger
}

func New(db *gorm.DB, verifier *Verifier, px *proxmox.Client, cfg config.Config) *Server {
	return &Server{db: db, verifier: verifier, px: px, cfg: cfg, logger: slog.Default()}
}

// WithLogger sets the logger used by the server.
func (s *Server) WithLogger(l *slog.Logger) *Server {
	s.logger = l
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /config", s.handleConfig)
	mux.HandleFunc("GET /auth/login", s.handleAuthLogin)
	mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	mux.HandleFunc("GET /games", s.handleListGames)
	mux.Handle("POST /games", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleCreateGame))))
	mux.Handle("PUT /games/{id}", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleUpdateGame))))
	mux.Handle("POST /games/{id}/jobs", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleEnqueueGameJob))))
	mux.Handle("GET /games/{id}/jobs", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleListGameJobs))))
	mux.Handle("POST /library/jobs", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleEnqueueLibraryJob))))
	mux.Handle("GET /jobs/{id}", s.auth(http.HandlerFunc(s.handleGetJob)))
	mux.Handle("GET /sessions", s.auth(http.HandlerFunc(s.handleListSessions)))
	mux.Handle("POST /sessions", s.auth(http.HandlerFunc(s.handleCreateSession)))
	mux.Handle("DELETE /sessions/", s.auth(http.HandlerFunc(s.handleDeleteSession)))
	mux.Handle("POST /sessions/{id}/override", s.auth(http.HandlerFunc(s.handleOverrideVMConfig)))
	mux.Handle("GET /users", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleListUsers))))
	mux.Handle("PUT /users/{id}", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleUpdateUserRole))))
	mux.Handle("GET /me", s.auth(http.HandlerFunc(s.handleMe)))

	// Agent-only endpoints: every agent-to-Proxmox interaction goes through
	// the server. Agents authenticate with the shared AgentToken.
	mux.Handle("POST /agent/vm/{vmid}/start", s.requireAgentToken(http.HandlerFunc(s.handleAgentVMStart)))
	mux.Handle("POST /agent/vm/{vmid}/stop", s.requireAgentToken(http.HandlerFunc(s.handleAgentVMStop)))
	mux.Handle("GET /agent/vm/{vmid}/config", s.requireAgentToken(http.HandlerFunc(s.handleAgentVMConfig)))
	return s.accessLog(s.withCORS(mux))
}

// statusRecorder captures the response status for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// accessLog logs one line per request. The query string is deliberately left
// out: it carries OAuth codes and tokens.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		case r.Method == http.MethodOptions || r.URL.Path == "/health":
			level = slog.LevelDebug
		}
		s.logger.Log(r.Context(), level, "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// withCORS allows the main-site browser origin to call the API hosted on the
// api.<domain> subdomain. Empty FrontendOrigin disables CORS.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); s.cfg.FrontendOrigin != "" && origin == s.cfg.FrontendOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth requires a valid OIDC identity and auto-provisions the user on first
// login, storing the identity in the request context under ctxUserKey.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.verifier.Verify(r.Context(), bearerToken(r))
		if err != nil {
			s.logger.Warn("unauthenticated request", "path", r.URL.Path, "method", r.Method, "err", err)
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		if _, err := s.ensureUser(id); err != nil {
			s.logger.Error("user provisioning failed", "subject", id.Subject, "err", err)
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey, id)))
	})
}

// ensureUser returns the stored user for an identity, creating it on first
// login. OIDC subjects in AdminSubjects are provisioned (or promoted) to admin.
func (s *Server) ensureUser(id Identity) (models.User, error) {
	var u models.User
	q := s.db.Where("username = ?", id.Subject)
	if id.Email != "" {
		q = q.Or("email = ?", id.Email)
	}
	err := q.First(&u).Error
	if err == nil {
		if s.isAdminSubject(id) && u.Role != models.RoleAdmin {
			u.Role = models.RoleAdmin
			if err := s.db.Save(&u).Error; err != nil {
				return u, err
			}
			s.logger.Info("user promoted to admin", "user_id", u.ID, "username", u.Username)
		}
		return u, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return u, err
	}
	email := id.Email
	if email == "" {
		email = id.Subject + "@oidc.local"
	}
	username := id.Username
	if username == "" {
		username = id.Subject
	}
	role := models.RoleGamer
	if s.isAdminSubject(id) {
		role = models.RoleAdmin
	}
	u = models.User{Username: username, Email: email, Age: s.cfg.DefaultAge, Role: role}
	if err := s.db.Create(&u).Error; err != nil {
		// Lost a provisioning race: return the row another request created.
		if e := s.db.Where("username = ? OR email = ?", username, email).First(&u).Error; e == nil {
			return u, nil
		}
		return u, err
	}
	s.logger.Info("user provisioned", "user_id", u.ID, "username", u.Username, "role", u.Role)
	return u, nil
}

// isAdminSubject reports whether the identity is listed as an admin. The
// subject is the provider's user ID (a UUID with Pocket ID), so the username
// and email claims are matched as well.
func (s *Server) isAdminSubject(id Identity) bool {
	for _, a := range s.cfg.AdminSubjects {
		if a == id.Subject || (id.Username != "" && a == id.Username) || (id.Email != "" && a == id.Email) {
			return true
		}
	}
	return false
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"backup_interval":   s.cfg.BackupInterval.String(),
		"vm_deletion_delay": s.cfg.VMDeletionDelay.String(),
		"oidc_issuer":       s.verifier.issuer,
		"oidc_client_id":    s.verifier.clientID,
	})
}

// oauth2Config builds the OIDC client config with the server's public redirect
// URI and (optional) confidential client secret.
func (s *Server) oauth2Config(ctx context.Context) (*oauth2.Config, error) {
	oc, err := s.verifier.OAuth2(ctx)
	if err != nil {
		return nil, err
	}
	oc.ClientSecret = s.cfg.OIDCClientSecret
	oc.RedirectURL = strings.TrimRight(s.cfg.OIDCPublicURL, "/") + "/auth/callback"
	return oc, nil
}

// frontendLogin builds the frontend login route URL. The route and its token
// travel after the URL fragment so the token is never sent to servers.
func (s *Server) frontendLogin(errMsg, token string) string {
	u := strings.TrimRight(s.cfg.FrontendOrigin, "/") + "/#/login"
	if token != "" {
		return u + "?token=" + url.QueryEscape(token)
	}
	if errMsg != "" {
		return u + "?error=" + url.QueryEscape(errMsg)
	}
	return u
}

// handleAuthLogin starts the OIDC authorization code flow. In dev mode (no
// issuer configured) it hands the frontend a dummy token directly.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.verifier.issuer == "" {
		s.logger.Warn("oidc login started in dev mode (no issuer configured)")
		http.Redirect(w, r, s.frontendLogin("", "dev-token"), http.StatusFound)
		return
	}
	state, err := randomState()
	if err != nil {
		s.logger.Error("oidc state generation failed", "err", err)
		http.Error(w, `{"error":"state error"}`, http.StatusInternalServerError)
		return
	}
	oc, err := s.oauth2Config(r.Context())
	if err != nil {
		http.Redirect(w, r, s.frontendLogin("oidc discovery failed", ""), http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "vdi_oauth_state",
		Value:    state,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.cfg.OIDCPublicURL, "https"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, oc.AuthCodeURL(state), http.StatusFound)
}

// handleAuthCallback completes the flow: it validates the state cookie,
// exchanges the code for tokens and redirects back to the frontend with the
// ID token in the URL fragment.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie("vdi_oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != state {
		s.logger.Warn("oidc callback rejected: invalid state")
		http.Redirect(w, r, s.frontendLogin("invalid oauth state", ""), http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "vdi_oauth_state", Value: "", Path: "/auth", MaxAge: -1})
	if e := r.URL.Query().Get("error"); e != "" {
		s.logger.Warn("oidc provider returned an error", "provider_error", e)
		http.Redirect(w, r, s.frontendLogin(e, ""), http.StatusFound)
		return
	}
	oc, err := s.oauth2Config(r.Context())
	if err != nil {
		http.Redirect(w, r, s.frontendLogin("oidc discovery failed", ""), http.StatusFound)
		return
	}
	token, err := oc.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		s.logger.Error("oidc token exchange failed", "err", err)
		http.Redirect(w, r, s.frontendLogin("token exchange failed", ""), http.StatusFound)
		return
	}
	idToken, _ := token.Extra("id_token").(string)
	if idToken == "" {
		s.logger.Error("oidc response carried no id_token")
		http.Redirect(w, r, s.frontendLogin("no id_token in response", ""), http.StatusFound)
		return
	}
	s.logger.Info("oidc login completed")
	http.Redirect(w, r, s.frontendLogin("", idToken), http.StatusFound)
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) handleListGames(w http.ResponseWriter, r *http.Request) {
	var games []models.Game
	if err := s.db.Find(&games).Error; err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	type gameOut struct {
		ID          uint           `json:"id"`
		Title       string         `json:"title"`
		LibraryPath string         `json:"library_path"`
		PEGIRating  string         `json:"pegi_rating"`
		VMConfig    models.VMConfig `json:"vm_config"`
		Metadata    string         `json:"metadata"`
	}
	out := make([]gameOut, len(games))
	for i, g := range games {
		out[i] = gameOut{ID: g.ID, Title: g.Title, LibraryPath: g.LibraryPath, PEGIRating: g.PEGIRating, VMConfig: g.VMConfig, Metadata: g.Metadata}
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	var g models.Game
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if g.Title == "" || g.PEGIRating == "" {
		http.Error(w, `{"error":"title and pegi_rating required"}`, http.StatusBadRequest)
		return
	}
	if err := s.db.Create(&g).Error; err != nil {
		s.logger.Error("game create failed", "title", g.Title, "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("game created", "game_id", g.ID, "title", g.Title, "pegi_rating", g.PEGIRating)
	s.writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateGame(w http.ResponseWriter, r *http.Request) {
	id, err := gameIDFromPath(r)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	var g models.Game
	if err := s.db.First(&g, id).Error; err != nil {
		http.Error(w, `{"error":"game not found"}`, http.StatusNotFound)
		return
	}
	var req struct {
		LibraryPath string `json:"library_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if req.LibraryPath == "" {
		http.Error(w, `{"error":"library_path required"}`, http.StatusBadRequest)
		return
	}
	g.LibraryPath = req.LibraryPath
	if err := s.db.Save(&g).Error; err != nil {
		s.logger.Error("game update failed", "game_id", g.ID, "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("game updated", "game_id", g.ID, "library_path", g.LibraryPath)
	s.writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleEnqueueGameJob(w http.ResponseWriter, r *http.Request) {
	id, err := gameIDFromPath(r)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	var g models.Game
	if err := s.db.First(&g, id).Error; err != nil {
		http.Error(w, `{"error":"game not found"}`, http.StatusNotFound)
		return
	}
	var req struct {
		Kind    models.JobKind  `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	var payload any
	if len(req.Payload) > 0 {
		payload = json.RawMessage(req.Payload)
	}
	job, err := worker.Enqueue(s.db, g.ID, req.Kind, payload)
	if err != nil {
		s.logger.Error("game job enqueue failed", "game_id", g.ID, "kind", req.Kind, "err", err)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}
	s.logger.Info("game job enqueued", "job_id", job.ID, "game_id", g.ID, "kind", req.Kind)
	s.writeJSON(w, http.StatusAccepted, job)
}

// handleEnqueueLibraryJob queues the processing of the game files in the
// library: a worker scans the library directory, registers the games it finds
// and queues a config and a metadata job for each of them.
func (s *Server) handleEnqueueLibraryJob(w http.ResponseWriter, r *http.Request) {
	job, err := worker.Enqueue(s.db, models.LibraryJobGameID, models.JobKindLibrary, nil)
	if err != nil {
		s.logger.Error("library job enqueue failed", "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("library job enqueued", "job_id", job.ID)
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleListGameJobs(w http.ResponseWriter, r *http.Request) {
	id, err := gameIDFromPath(r)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	var jobs []models.GameJob
	if err := s.db.Where("game_id = ?", id).Order("id").Find(&jobs).Error; err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := jobIDFromPath(r)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	var job models.GameJob
	if err := s.db.First(&job, id).Error; err != nil {
		http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

func currentUser(s *Server, r *http.Request) (models.User, bool) {
	id := userID(r)
	q := s.db.Where("username = ?", id.Subject)
	if id.Email != "" {
		q = q.Or("email = ?", id.Email)
	}
	var u models.User
	if err := q.First(&u).Error; err != nil {
		return u, false
	}
	return u, true
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUser(s, r)
	if !ok {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	q := s.db
	if u.Role != models.RoleAdmin {
		q = q.Where("user_id = ?", u.ID)
	}
	var sessions []models.Session
	if err := q.Find(&sessions).Error; err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GameID        uint  `json:"game_id"`
		VMID          string `json:"vm_id"`
		OverrideCPUCores *int `json:"cpu_cores"`
		OverrideRAMSize   *int `json:"ram_size"`
		OverrideDiskSize  *int `json:"disk_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	u, ok := currentUser(s, r)
	if !ok {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	var g models.Game
	if err := s.db.First(&g, req.GameID).Error; err != nil {
		http.Error(w, `{"error":"game not found"}`, http.StatusNotFound)
		return
	}
	if !ageAllowed(u.Age, g.PEGIRating) {
		http.Error(w, `{"error":"age rating not allowed"}`, http.StatusForbidden)
		return
	}
	now := time.Now()
	sess := models.Session{
		UserID:            u.ID,
		GameID:            g.ID,
		VMID:              req.VMID,
		Backups:           0,
		LastUserSession:   &now,
		OverrideCPUCores:  req.OverrideCPUCores,
		OverrideRAMSize:   req.OverrideRAMSize,
		OverrideDiskSize:  req.OverrideDiskSize,
	}
	if err := s.db.Create(&sess).Error; err != nil {
		s.logger.Error("session create failed", "user_id", u.ID, "game_id", g.ID, "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("session created", "session_id", sess.ID, "user_id", u.ID, "game_id", g.ID, "vm_id", sess.VMID)
	s.writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, err := sessionIDFromPath(r)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	u, ok := currentUser(s, r)
	if !ok {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	q := s.db.Where("id = ?", id)
	if u.Role != models.RoleAdmin {
		q = q.Where("user_id = ?", u.ID)
	}
	result := q.Delete(&models.Session{})
	if result.Error != nil {
		s.logger.Error("session delete failed", "session_id", id, "err", result.Error)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	s.logger.Info("session deleted", "session_id", id, "user_id", u.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOverrideVMConfig(w http.ResponseWriter, r *http.Request) {
	id, err := sessionIDFromPath(r)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	u, ok := currentUser(s, r)
	if !ok {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	var sess models.Session
	q := s.db.Where("id = ?", id)
	if u.Role != models.RoleAdmin {
		q = q.Where("user_id = ?", u.ID)
	}
	if err := q.First(&sess).Error; err != nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	var req struct {
		CPUCores *int `json:"cpu_cores"`
		RAMSize  *int `json:"ram_size"`
		DiskSize *int `json:"disk_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	updates := map[string]any{}
	if req.CPUCores != nil {
		updates["override_cpu_cores"] = *req.CPUCores
	}
	if req.RAMSize != nil {
		updates["override_ram_size"] = *req.RAMSize
	}
	if req.DiskSize != nil {
		updates["override_disk_size"] = *req.DiskSize
	}
	if len(updates) > 0 {
		if err := s.db.Model(&sess).Updates(updates).Error; err != nil {
			s.logger.Error("session vm config override failed", "session_id", sess.ID, "err", err)
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			return
		}
		s.logger.Info("session vm config overridden", "session_id", sess.ID, "fields", len(updates))
	}
	s.writeJSON(w, http.StatusOK, sess)
}

type userOut struct {
	ID       uint        `json:"id"`
	Username string      `json:"username"`
	Email    string      `json:"email"`
	Age      int         `json:"age"`
	Role     models.Role `json:"role"`
}

func newUserOut(u models.User) userOut {
	return userOut{ID: u.ID, Username: u.Username, Email: u.Email, Age: u.Age, Role: u.Role}
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	var users []models.User
	if err := s.db.Find(&users).Error; err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	out := make([]userOut, len(users))
	for i, u := range users {
		out[i] = newUserOut(u)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleUpdateUserRole lets an admin change a user's role, making the
// anonymous/gamer/admin distinction meaningful.
func (s *Server) handleUpdateUserRole(w http.ResponseWriter, r *http.Request) {
	id, err := userIDFromPath(r)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	var req struct {
		Role models.Role `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	switch req.Role {
	case models.RoleAnonymous, models.RoleGamer, models.RoleAdmin:
	default:
		http.Error(w, `{"error":"invalid role"}`, http.StatusBadRequest)
		return
	}
	var u models.User
	if err := s.db.First(&u, id).Error; err != nil {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	u.Role = req.Role
	if err := s.db.Save(&u).Error; err != nil {
		s.logger.Error("user role update failed", "user_id", u.ID, "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("user role updated", "user_id", u.ID, "username", u.Username, "role", u.Role)
	s.writeJSON(w, http.StatusOK, newUserOut(u))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUser(s, r)
	if !ok {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":       u.ID,
		"username": u.Username,
		"role":     u.Role,
		"age":      u.Age,
	})
}

func (s *Server) requireRole(roles ...models.Role) func(http.Handler) http.Handler {
	allowed := map[models.Role]bool{}
	for _, role := range roles {
		allowed[role] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := currentUser(s, r)
			if !ok || !allowed[u.Role] {
				s.logger.Warn("forbidden request", "path", r.URL.Path, "method", r.Method, "role", u.Role)
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireAgentToken authenticates agents via the shared AgentToken bearer
// token. Only agents may call the VM proxy endpoints.
func (s *Server) requireAgentToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" || token != s.cfg.AgentToken {
			s.logger.Warn("agent auth failed", "path", r.URL.Path, "method", r.Method)
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAgentVMStart(w http.ResponseWriter, r *http.Request) {
	vmid := r.PathValue("vmid")
	if vmid == "" {
		http.Error(w, `{"error":"missing vmid"}`, http.StatusBadRequest)
		return
	}
	if s.px == nil {
		http.Error(w, `{"error":"proxmox not configured"}`, http.StatusServiceUnavailable)
		return
	}
	if err := s.px.Start(vmid); err != nil {
		s.logger.Error("vm start failed", "vm_id", vmid, "err", logging.SafeError(err))
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadGateway)
		return
	}
	s.logger.Info("vm started", "vm_id", vmid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentVMStop(w http.ResponseWriter, r *http.Request) {
	vmid := r.PathValue("vmid")
	if vmid == "" {
		http.Error(w, `{"error":"missing vmid"}`, http.StatusBadRequest)
		return
	}
	if s.px == nil {
		http.Error(w, `{"error":"proxmox not configured"}`, http.StatusServiceUnavailable)
		return
	}
	if err := s.px.Stop(vmid); err != nil {
		s.logger.Error("vm stop failed", "vm_id", vmid, "err", logging.SafeError(err))
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadGateway)
		return
	}
	s.logger.Info("vm stopped", "vm_id", vmid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentVMConfig(w http.ResponseWriter, r *http.Request) {
	vmid := r.PathValue("vmid")
	if vmid == "" {
		http.Error(w, `{"error":"missing vmid"}`, http.StatusBadRequest)
		return
	}
	if s.px == nil {
		http.Error(w, `{"error":"proxmox not configured"}`, http.StatusServiceUnavailable)
		return
	}
	cfg, err := s.px.GetConfig(vmid)
	if err != nil {
		s.logger.Error("vm config fetch failed", "vm_id", vmid, "err", logging.SafeError(err))
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadGateway)
		return
	}
	s.logger.Debug("vm config fetched", "vm_id", vmid)
	s.writeJSON(w, http.StatusOK, cfg)
}

func sessionIDFromPath(r *http.Request) (uint, error) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "sessions" {
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(n), nil
}

func gameIDFromPath(r *http.Request) (uint, error) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "games" {
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(n), nil
}

func userIDFromPath(r *http.Request) (uint, error) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "users" {
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(n), nil
}

func jobIDFromPath(r *http.Request) (uint, error) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "jobs" {
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(n), nil
}

// ageAllowed reports whether a user of the given age may play a game with the
// given PEGI rating (PEGI floors: 3, 7, 12, 16, 18).
func ageAllowed(age int, pegi string) bool {
	floor := 0
	switch pegi {
	case "3":
		floor = 3
	case "7":
		floor = 7
	case "12":
		floor = 12
	case "16":
		floor = 16
	case "18":
		floor = 18
	}
	return age >= floor
}
