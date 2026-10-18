package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"selfhosted-gaming-vdi/config"
	"selfhosted-gaming-vdi/internal/proxmox"
	"selfhosted-gaming-vdi/models"
)

type Server struct {
	db      *gorm.DB
	verifier *Verifier
	px      *proxmox.Client
	cfg     config.Config
}

func New(db *gorm.DB, verifier *Verifier, px *proxmox.Client, cfg config.Config) *Server {
	return &Server{db: db, verifier: verifier, px: px, cfg: cfg}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /config", s.handleConfig)
	mux.HandleFunc("GET /games", s.handleListGames)
	mux.Handle("POST /games", s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleCreateGame)))
	mux.Handle("PUT /games/{id}", s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleUpdateGame)))
	mux.Handle("GET /sessions", AuthMiddleware(s.verifier)(http.HandlerFunc(s.handleListSessions)))
	mux.Handle("POST /sessions", AuthMiddleware(s.verifier)(http.HandlerFunc(s.handleCreateSession)))
	mux.Handle("DELETE /sessions/", AuthMiddleware(s.verifier)(http.HandlerFunc(s.handleDeleteSession)))
	mux.Handle("POST /sessions/{id}/override", AuthMiddleware(s.verifier)(http.HandlerFunc(s.handleOverrideVMConfig)))
	mux.Handle("GET /users", AuthMiddleware(s.verifier)(s.requireRole(models.RoleAdmin)(http.HandlerFunc(s.handleListUsers))))
	mux.Handle("GET /me", AuthMiddleware(s.verifier)(http.HandlerFunc(s.handleMe)))

	// Agent-only endpoints: every agent-to-Proxmox interaction goes through
	// the server. Agents authenticate with the shared AgentToken.
	mux.Handle("POST /agent/vm/{vmid}/start", s.requireAgentToken(http.HandlerFunc(s.handleAgentVMStart)))
	mux.Handle("POST /agent/vm/{vmid}/stop", s.requireAgentToken(http.HandlerFunc(s.handleAgentVMStop)))
	mux.Handle("GET /agent/vm/{vmid}/config", s.requireAgentToken(http.HandlerFunc(s.handleAgentVMConfig)))
	return mux
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
	})
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
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
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
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, g)
}

func currentUser(s *Server, r *http.Request) (models.User, bool) {
	sub := userID(r)
	var u models.User
	if err := s.db.Where("username = ? OR email = ?", sub, sub).First(&u).Error; err != nil {
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
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
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
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	if result.RowsAffected == 0 {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
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
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			return
		}
	}
	s.writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	var users []models.User
	if err := s.db.Find(&users).Error; err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	type userOut struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
		Age      int    `json:"age"`
		Role     models.Role `json:"role"`
	}
	out := make([]userOut, len(users))
	for i, u := range users {
		out[i] = userOut{ID: u.ID, Username: u.Username, Email: u.Email, Age: u.Age, Role: u.Role}
	}
	s.writeJSON(w, http.StatusOK, out)
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
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadGateway)
		return
	}
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
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadGateway)
		return
	}
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
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadGateway)
		return
	}
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
