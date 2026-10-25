package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
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
	mux.Handle("POST /admin/vms", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleCreateVM))))
	mux.Handle("PUT /games/{id}", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleUpdateGame))))
	mux.Handle("POST /games/{id}/jobs", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleEnqueueGameJob))))
	mux.Handle("GET /games/{id}/jobs", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleListGameJobs))))
	mux.Handle("POST /library/jobs", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleEnqueueLibraryJob))))
	mux.Handle("DELETE /library", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleResetLibrary))))
	mux.Handle("GET /library/paths", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleListLibraryPaths))))
	mux.Handle("POST /library/paths", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleCreateLibraryPath))))
	mux.Handle("DELETE /library/paths/{id}", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleDeleteLibraryPath))))
	mux.Handle("GET /jobs/{id}", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleGetJob))))
	mux.Handle("GET /sessions", s.auth(http.HandlerFunc(s.handleListSessions)))
	mux.Handle("POST /sessions", s.auth(http.HandlerFunc(s.handleCreateSession)))
	mux.Handle("DELETE /sessions/", s.auth(http.HandlerFunc(s.handleDeleteSession)))
	mux.Handle("POST /sessions/{id}/override", s.auth(http.HandlerFunc(s.handleOverrideVMConfig)))
	mux.Handle("POST /sessions/{id}/heartbeat", s.auth(http.HandlerFunc(s.handleSessionHeartbeat)))
	mux.Handle("GET /users", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleListUsers))))
	mux.Handle("PUT /users/{id}", s.auth(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleUpdateUserRole))))
	mux.Handle("GET /me", s.auth(http.HandlerFunc(s.handleMe)))

	// Agent-only endpoints: every agent-to-Proxmox interaction goes through
	// the server. Each agent authenticates with the token for its own VM.
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
// login. The OIDC subject is the only identity key: usernames and emails can
// be mutable at some providers. Admins are matched by subject only, and a user
// promoted by VDI_ADMIN_SUBJECTS is demoted when removed from it.
func (s *Server) ensureUser(id Identity) (models.User, error) {
	var u models.User
	err := s.db.Where("subject = ?", id.Subject).First(&u).Error
	if err == nil {
		return s.syncAdmin(u, id)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return u, err
	}
	email := id.Subject + "@oidc.local"
	if id.EmailVerified && id.Email != "" {
		email = id.Email
	}
	username := id.Username
	if username == "" {
		username = id.Subject
	}
	u = models.User{Subject: id.Subject, Username: username, Email: email, Age: s.cfg.DefaultAge, Role: models.RoleGamer}
	if s.isAdminSubject(id) {
		u.Role = models.RoleAdmin
		u.EnvAdmin = true
	}
	if err := s.db.Create(&u).Error; err != nil {
		// Lost a provisioning race: return the row another request created.
		if e := s.db.Where("subject = ?", id.Subject).First(&u).Error; e == nil {
			return s.syncAdmin(u, id)
		}
		return u, err
	}
	s.logger.Info("user provisioned", "user_id", u.ID, "username", u.Username, "role", u.Role)
	return u, nil
}

// syncAdmin applies the VDI_ADMIN_SUBJECTS list to an existing user. Only
// users promoted by that list are demoted; manually assigned admins stay.
func (s *Server) syncAdmin(u models.User, id Identity) (models.User, error) {
	if s.isAdminSubject(id) {
		if u.Role != models.RoleAdmin || !u.EnvAdmin {
			u.Role = models.RoleAdmin
			u.EnvAdmin = true
			if err := s.db.Model(&u).Updates(map[string]any{"role": u.Role, "env_admin": true}).Error; err != nil {
				return u, err
			}
			s.logger.Info("user promoted to admin", "user_id", u.ID, "username", u.Username)
		}
		return u, nil
	}
	if u.EnvAdmin {
		u.Role = models.RoleGamer
		u.EnvAdmin = false
		if err := s.db.Model(&u).Updates(map[string]any{"role": u.Role, "env_admin": false}).Error; err != nil {
			return u, err
		}
		s.logger.Info("user demoted from admin", "user_id", u.ID, "username", u.Username)
	}
	return u, nil
}

// isAdminSubject reports whether the identity's immutable subject is listed
// as an admin. Usernames and emails are not matched: both can be user-editable
// at some providers.
func (s *Server) isAdminSubject(id Identity) bool {
	for _, a := range s.cfg.AdminSubjects {
		if a == id.Subject {
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

// handleListGames returns the catalog filtered by the caller's age. Anonymous
// callers only see the lowest PEGI rating. The internal library path is only
// returned to admins.
func (s *Server) handleListGames(w http.ResponseWriter, r *http.Request) {
	age, admin := 3, false
	id := userID(r)
	if id.Subject == "" {
		if token := bearerToken(r); token != "" {
			if verified, err := s.verifier.Verify(r.Context(), token); err == nil {
				id = verified
			}
		}
	}
	if u, ok := s.userBySubject(id.Subject); ok {
		age, admin = u.Age, u.Role == models.RoleAdmin
	}
	var games []models.Game
	if err := s.db.Find(&games).Error; err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	type gameOut struct {
		ID             uint                   `json:"id"`
		Title          string                 `json:"title"`
		SanitizedTitle string                 `json:"sanitized_title"`
		LibraryPath    string                 `json:"library_path,omitempty"`
		Category       models.LibraryCategory `json:"category"`
		PEGIRating     string                 `json:"pegi_rating"`
		VMConfig       models.VMConfig        `json:"vm_config"`
		Metadata       string                 `json:"metadata"`
	}
	out := make([]gameOut, 0, len(games))
	for _, g := range games {
		if !admin && !ageAllowed(age, g.PEGIRating) {
			continue
		}
		o := gameOut{ID: g.ID, Title: g.Title, SanitizedTitle: g.SanitizedName(), Category: g.Category, PEGIRating: g.PEGIRating, VMConfig: g.VMConfig, Metadata: g.Metadata}
		if admin {
			o.LibraryPath = g.LibraryPath
		}
		out = append(out, o)
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	var g models.Game
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if g.Title == "" || !models.ValidPEGIRating(g.PEGIRating) {
		http.Error(w, `{"error":"title and a valid pegi_rating (3, 7, 12, 16, 18) required"}`, http.StatusBadRequest)
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

var (
	vmNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
	vmVGAs   = map[string]bool{"": true, "std": true, "cirrus": true, "vmware": true, "qxl": true, "virtio": true, "none": true}
)

// handleCreateVM clones a Proxmox template with an admin-chosen CPU, RAM,
// disk size and VGA type, then returns the new VM id.
func (s *Server) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TemplateID int    `json:"template_id"`
		Name       string `json:"name"`
		CPUCores   int    `json:"cpu_cores"`
		RAMSize    int    `json:"ram_size"`
		DiskSize   int    `json:"disk_size"`
		VGPU       string `json:"vgpu"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.VGPU = strings.ToLower(strings.TrimSpace(req.VGPU))
	switch {
	case req.TemplateID <= 0:
		http.Error(w, `{"error":"template_id required"}`, http.StatusBadRequest)
		return
	case !vmNameRE.MatchString(req.Name):
		http.Error(w, `{"error":"name must start with a letter and contain only letters, digits and hyphens"}`, http.StatusBadRequest)
		return
	case !vmVGAs[req.VGPU]:
		http.Error(w, `{"error":"invalid vgpu"}`, http.StatusBadRequest)
		return
	}
	if msg := validateVMConfig(req.CPUCores, req.RAMSize, req.DiskSize); msg != "" {
		http.Error(w, `{"error":"`+msg+`"}`, http.StatusBadRequest)
		return
	}
	if s.px == nil {
		http.Error(w, `{"error":"proxmox not configured"}`, http.StatusServiceUnavailable)
		return
	}
	vmid, err := s.px.Clone(req.TemplateID, req.Name, req.CPUCores, req.RAMSize, req.DiskSize, req.VGPU)
	if err != nil {
		s.logger.Error("custom vm clone failed", "template_id", req.TemplateID, "name", req.Name, "err", logging.SafeError(err))
		http.Error(w, `{"error":"proxmox request failed"}`, http.StatusBadGateway)
		return
	}
	s.logger.Info("custom vm created", "vm_id", vmid, "template_id", req.TemplateID, "name", req.Name, "cpu_cores", req.CPUCores, "ram_size", req.RAMSize, "disk_size", req.DiskSize, "vgpu", req.VGPU)
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"vmid":        vmid,
		"template_id": req.TemplateID,
		"name":        req.Name,
		"cpu_cores":   req.CPUCores,
		"ram_size":    req.RAMSize,
		"disk_size":   req.DiskSize,
		"vgpu":        req.VGPU,
	})
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

// handleListLibraryPaths returns the admin-configured library paths.
func (s *Server) handleListLibraryPaths(w http.ResponseWriter, r *http.Request) {
	var paths []models.LibraryPath
	if err := s.db.Order("id").Find(&paths).Error; err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, paths)
}

// handleCreateLibraryPath adds a directory holding games, categorized as
// repacks, portables, isos or setups.
func (s *Server) handleCreateLibraryPath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Category models.LibraryCategory `json:"category"`
		Path     string                 `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if !models.ValidLibraryCategory(req.Category) {
		http.Error(w, `{"error":"invalid category"}`, http.StatusBadRequest)
		return
	}
	req.Path = filepath.Clean(strings.TrimSpace(req.Path))
	if req.Path == "" || req.Path == "." {
		http.Error(w, `{"error":"path required"}`, http.StatusBadRequest)
		return
	}
	var existing models.LibraryPath
	err := s.db.Where("category = ? AND path = ?", req.Category, req.Path).First(&existing).Error
	if err == nil {
		http.Error(w, `{"error":"library path already configured"}`, http.StatusConflict)
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	lib := models.LibraryPath{Category: req.Category, Path: req.Path}
	if err := s.db.Create(&lib).Error; err != nil {
		s.logger.Error("library path create failed", "category", req.Category, "path", req.Path, "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("library path created", "library_path_id", lib.ID, "category", lib.Category, "path", lib.Path)
	s.writeJSON(w, http.StatusCreated, lib)
}

func (s *Server) handleDeleteLibraryPath(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	result := s.db.Delete(&models.LibraryPath{}, id)
	if result.Error != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, `{"error":"library path not found"}`, http.StatusNotFound)
		return
	}
	s.logger.Info("library path deleted", "library_path_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// handleResetLibrary deletes all games and queued jobs so the admin can
// rescan the configured library paths from scratch. The path configuration is
// kept.
func (s *Server) handleResetLibrary(w http.ResponseWriter, r *http.Request) {
	var games, jobs int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		jobRes := tx.Unscoped().Where("1 = 1").Delete(&models.GameJob{})
		if jobRes.Error != nil {
			return jobRes.Error
		}
		jobs = jobRes.RowsAffected
		gameRes := tx.Unscoped().Where("1 = 1").Delete(&models.Game{})
		if gameRes.Error != nil {
			return gameRes.Error
		}
		games = gameRes.RowsAffected
		return nil
	})
	if err != nil {
		s.logger.Error("library reset failed", "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("library reset", "games", games, "jobs", jobs)
	s.writeJSON(w, http.StatusOK, map[string]int64{"games": games, "jobs": jobs})
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

func (s *Server) userBySubject(subject string) (models.User, bool) {
	var u models.User
	if subject == "" {
		return u, false
	}
	if err := s.db.Where("subject = ?", subject).First(&u).Error; err != nil {
		return u, false
	}
	return u, true
}

func currentUser(s *Server, r *http.Request) (models.User, bool) {
	return s.userBySubject(userID(r).Subject)
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

// handleCreateSession clones a VM server-side and binds it to the session. The
// client never chooses a VM id, so it cannot point the backup/deletion paths at
// someone else's VM.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GameID           uint `json:"game_id"`
		OverrideCPUCores *int `json:"cpu_cores"`
		OverrideRAMSize  *int `json:"ram_size"`
		OverrideDiskSize *int `json:"disk_size"`
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
	cores, ram, disk := g.VMConfig.CPUCores, g.VMConfig.RAMSize, g.VMConfig.DiskSize
	if req.OverrideCPUCores != nil {
		cores = *req.OverrideCPUCores
	}
	if req.OverrideRAMSize != nil {
		ram = *req.OverrideRAMSize
	}
	if req.OverrideDiskSize != nil {
		disk = *req.OverrideDiskSize
	}
	if msg := validateVMConfig(cores, ram, disk); msg != "" {
		http.Error(w, `{"error":"`+msg+`"}`, http.StatusBadRequest)
		return
	}
	if s.px == nil || s.cfg.VMTemplateID <= 0 {
		http.Error(w, `{"error":"vm provisioning not configured"}`, http.StatusServiceUnavailable)
		return
	}
	now := time.Now()
	name := fmt.Sprintf("vdi-g%d-u%d-%d", g.ID, u.ID, now.Unix())
	vmid, err := s.px.Clone(s.cfg.VMTemplateID, name, cores, ram, disk, "")
	if err != nil {
		s.logger.Error("session vm clone failed", "user_id", u.ID, "game_id", g.ID, "err", logging.SafeError(err))
		http.Error(w, `{"error":"vm provisioning failed"}`, http.StatusBadGateway)
		return
	}
	sess := models.Session{
		UserID:           u.ID,
		GameID:           g.ID,
		VMID:             strconv.Itoa(vmid),
		Backups:          0,
		LastUserSession:  &now,
		OverrideCPUCores: req.OverrideCPUCores,
		OverrideRAMSize:  req.OverrideRAMSize,
		OverrideDiskSize: req.OverrideDiskSize,
	}
	if err := s.db.Create(&sess).Error; err != nil {
		_ = s.px.Delete(strconv.Itoa(vmid))
		s.logger.Error("session create failed", "user_id", u.ID, "game_id", g.ID, "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.logger.Info("session created", "session_id", sess.ID, "user_id", u.ID, "game_id", g.ID, "vm_id", sess.VMID)
	s.writeJSON(w, http.StatusCreated, sess)
}

// validateVMConfig returns an error message when the values are out of range.
func validateVMConfig(cores, ram, disk int) string {
	switch {
	case cores < 1 || cores > 128:
		return "cpu_cores must be between 1 and 128"
	case ram < 512 || ram > 1048576:
		return "ram_size must be between 512 and 1048576 MB"
	case disk < 1 || disk > 65536:
		return "disk_size must be between 1 and 65536 GB"
	}
	return ""
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

// handleSessionHeartbeat refreshes the activity timestamp that drives VM
// deletion, so an active session is never reaped.
func (s *Server) handleSessionHeartbeat(w http.ResponseWriter, r *http.Request) {
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
	var sess models.Session
	if err := q.First(&sess).Error; err != nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	now := time.Now()
	if err := s.db.Model(&sess).Update("last_user_session", now).Error; err != nil {
		s.logger.Error("session heartbeat failed", "session_id", sess.ID, "err", err)
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	sess.LastUserSession = &now
	s.writeJSON(w, http.StatusOK, sess)
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
	if req.CPUCores != nil && (*req.CPUCores < 1 || *req.CPUCores > 128) {
		http.Error(w, `{"error":"cpu_cores must be between 1 and 128"}`, http.StatusBadRequest)
		return
	}
	if req.RAMSize != nil && (*req.RAMSize < 512 || *req.RAMSize > 1048576) {
		http.Error(w, `{"error":"ram_size must be between 512 and 1048576 MB"}`, http.StatusBadRequest)
		return
	}
	if req.DiskSize != nil && (*req.DiskSize < 1 || *req.DiskSize > 65536) {
		http.Error(w, `{"error":"disk_size must be between 1 and 65536 GB"}`, http.StatusBadRequest)
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
	u.EnvAdmin = false
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

// requireAgentToken authenticates an agent with the token issued for the VM in
// the path. A token for VM A cannot be used to control VM B.
func (s *Server) requireAgentToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("vmid"))
		if err != nil || n <= 0 {
			http.Error(w, `{"error":"invalid vmid"}`, http.StatusBadRequest)
			return
		}
		vmid := strconv.Itoa(n)
		r.SetPathValue("vmid", vmid)
		expected, ok := s.cfg.AgentTokens[vmid]
		token := bearerToken(r)
		if !ok || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
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
		http.Error(w, `{"error":"proxmox request failed"}`, http.StatusBadGateway)
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
		http.Error(w, `{"error":"proxmox request failed"}`, http.StatusBadGateway)
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
		http.Error(w, `{"error":"proxmox request failed"}`, http.StatusBadGateway)
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
// given PEGI rating. Unknown ratings are denied rather than treated as 0.
func ageAllowed(age int, pegi string) bool {
	floor, ok := models.PEGIFloor(pegi)
	if !ok {
		return false
	}
	return age >= floor
}
