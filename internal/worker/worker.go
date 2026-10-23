package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"selfhosted-gaming-vdi/internal/logging"
	"selfhosted-gaming-vdi/internal/metadata"
	"selfhosted-gaming-vdi/models"
)

// DefaultPollInterval is how often a worker checks for pending jobs.
const DefaultPollInterval = 2 * time.Second

// MetadataFetcher retrieves a game's metadata from an external source.
type MetadataFetcher interface {
	FetchMetadata(ctx context.Context, title string) (string, error)
}

// HTTPFetcher fetches metadata from an HTTP metadata source:
// GET {base}/games?title={title} returns the metadata JSON body.
type HTTPFetcher struct {
	BaseURL string
	Client  *http.Client
}

func (f *HTTPFetcher) FetchMetadata(ctx context.Context, title string) (string, error) {
	if f.BaseURL == "" {
		return "", errors.New("metadata source not configured")
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	u := strings.TrimRight(f.BaseURL, "/") + "/games?title=" + url.QueryEscape(title)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("metadata source: %d %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

// Service is the PostgreSQL-backed worker queue for game jobs. Any number of
// workers may run against the same database.
type Service struct {
	db          *gorm.DB
	fetcher     MetadataFetcher
	libraryPath string
	interval    time.Duration
	logger      *slog.Logger
}

func New(db *gorm.DB, fetcher MetadataFetcher, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if fetcher == nil {
		fetcher = &HTTPFetcher{}
	}
	return &Service{db: db, fetcher: fetcher, interval: interval, logger: slog.Default()}
}

// WithLogger sets the logger used by the service.
func (s *Service) WithLogger(l *slog.Logger) *Service {
	s.logger = l
	return s
}

// WithLibraryPath sets the directory holding the game files. Library jobs
// scan it for games.
func (s *Service) WithLibraryPath(path string) *Service {
	s.libraryPath = path
	return s
}

// Enqueue queues a job for a game. payload is marshalled to JSON (optional).
func Enqueue(db *gorm.DB, gameID uint, kind models.JobKind, payload any) (*models.GameJob, error) {
	switch kind {
	case models.JobKindConfig, models.JobKindMetadata, models.JobKindLibrary:
	default:
		return nil, fmt.Errorf("unknown job kind %q", kind)
	}
	var p string
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		p = string(b)
	}
	job := models.GameJob{
		GameID:  gameID,
		Kind:    kind,
		Status:  models.JobStatusPending,
		Payload: p,
	}
	if err := db.Create(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// Run loops until ctx is cancelled, claiming and processing pending jobs.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.logger.Info("worker started", "poll_interval", s.interval.String())
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("worker stopped")
			return
		case <-ticker.C:
			if err := s.tick(ctx); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				s.logger.Error("worker tick failed", "err", err)
			}
		}
	}
}

// tick claims and processes at most one pending job.
func (s *Service) tick(ctx context.Context) error {
	job, err := s.claim(ctx)
	if err != nil {
		return err
	}
	s.process(ctx, job)
	return nil
}

// claim atomically moves one pending job to processing, so concurrent workers
// never process the same job twice.
func (s *Service) claim(ctx context.Context) (*models.GameJob, error) {
	for {
		var job models.GameJob
		if err := s.db.WithContext(ctx).Where("status = ?", models.JobStatusPending).Order("id").First(&job).Error; err != nil {
			return nil, err
		}
		res := s.db.WithContext(ctx).Model(&models.GameJob{}).
			Where("id = ? AND status = ?", job.ID, models.JobStatusPending).
			Updates(map[string]any{
				"status":   models.JobStatusProcessing,
				"attempts": gorm.Expr("attempts + 1"),
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 1 {
			job.Status = models.JobStatusProcessing
			job.Attempts++
			s.logger.Debug("job claimed", "job_id", job.ID, "game_id", job.GameID, "kind", job.Kind, "attempt", job.Attempts)
			return &job, nil
		}
		// Lost the race to another worker; try the next pending job.
	}
}

func (s *Service) process(ctx context.Context, job *models.GameJob) {
	if job.Kind == models.JobKindLibrary {
		result, err := s.applyLibrary(ctx)
		s.finish(ctx, job, result, err)
		return
	}
	var game models.Game
	if err := s.db.WithContext(ctx).First(&game, job.GameID).Error; err != nil {
		s.finish(ctx, job, "", fmt.Errorf("game %d: %w", job.GameID, err))
		return
	}
	var (
		result string
		err    error
	)
	switch job.Kind {
	case models.JobKindConfig:
		result, err = s.applyConfig(ctx, &game, job)
	case models.JobKindMetadata:
		result, err = s.applyMetadata(ctx, &game)
	default:
		err = fmt.Errorf("unknown job kind %q", job.Kind)
	}
	s.finish(ctx, job, result, err)
}

func (s *Service) finish(ctx context.Context, job *models.GameJob, result string, err error) {
	updates := map[string]any{"result": result}
	if err != nil {
		updates["status"] = models.JobStatusFailed
		updates["error"] = err.Error()
		s.logger.Error("job failed", "job_id", job.ID, "game_id", job.GameID, "kind", job.Kind, "attempt", job.Attempts, "err", logging.SafeError(err))
	} else {
		updates["status"] = models.JobStatusDone
		updates["error"] = ""
		s.logger.Info("job done", "job_id", job.ID, "game_id", job.GameID, "kind", job.Kind, "attempt", job.Attempts)
	}
	if uerr := s.db.WithContext(ctx).Model(&models.GameJob{}).Where("id = ?", job.ID).Updates(updates).Error; uerr != nil {
		s.logger.Error("job finalize failed", "job_id", job.ID, "err", uerr)
	}
}

// applyConfig generates the game's VM configuration and persists it to the
// game row. An explicit config in the job payload overrides the PEGI-derived
// default.
func (s *Service) applyConfig(ctx context.Context, game *models.Game, job *models.GameJob) (string, error) {
	cfg := vmConfigFromPEGI(game.PEGIRating)
	if job.Payload != "" {
		var explicit models.VMConfig
		if err := json.Unmarshal([]byte(job.Payload), &explicit); err != nil {
			return "", fmt.Errorf("decode payload: %w", err)
		}
		if explicit.CPUCores > 0 || explicit.RAMSize > 0 || explicit.DiskSize > 0 {
			cfg = explicit
		}
	}
	if err := s.db.WithContext(ctx).Model(&models.Game{}).Where("id = ?", game.ID).Updates(map[string]any{
		"cpu_cores": cfg.CPUCores,
		"ram_size":  cfg.RAMSize,
		"disk_size": cfg.DiskSize,
	}).Error; err != nil {
		return "", err
	}
	b, _ := json.Marshal(cfg)
	s.logger.Debug("config applied", "game_id", game.ID, "cpu_cores", cfg.CPUCores, "ram_size", cfg.RAMSize, "disk_size", cfg.DiskSize)
	return string(b), nil
}

// applyMetadata retrieves the game's metadata and persists it to the game row.
func (s *Service) applyMetadata(ctx context.Context, game *models.Game) (string, error) {
	query := game.SanitizedName()
	if query == "" {
		query = game.Title
	}
	meta, err := s.fetcher.FetchMetadata(ctx, query)
	if err != nil {
		s.logger.Warn("metadata fetch failed", "game_id", game.ID, "err", logging.SafeError(err))
		return "", err
	}
	s.logger.Debug("metadata fetched", "game_id", game.ID, "bytes", len(meta))
	updates := map[string]any{"metadata": meta}
	var parsed metadata.Metadata
	if err := json.Unmarshal([]byte(meta), &parsed); err == nil && parsed.PEGIRating != "" {
		updates["pegi_rating"] = parsed.PEGIRating
	}
	if err := s.db.WithContext(ctx).Model(&models.Game{}).Where("id = ?", game.ID).Updates(updates).Error; err != nil {
		return "", err
	}
	return meta, nil
}


// defaultPEGIRating is the rating a game discovered in the library gets until
// an admin sets the real one: the strictest rating keeps it out of reach.
const defaultPEGIRating = "18"

// libraryResult summarizes a library scan.
type libraryResult struct {
	Paths   int `json:"paths"`
	Games   int `json:"games"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Jobs    int `json:"jobs"`
}

// libraryPaths returns the admin-configured library paths, falling back to
// the legacy VDI_LIBRARY_PATH when none are configured.
func (s *Service) libraryPaths(ctx context.Context) ([]models.LibraryPath, error) {
	var paths []models.LibraryPath
	if err := s.db.WithContext(ctx).Order("id").Find(&paths).Error; err != nil {
		return nil, err
	}
	if len(paths) == 0 && s.libraryPath != "" {
		paths = []models.LibraryPath{{Category: models.CategoryRepacks, Path: s.libraryPath}}
	}
	return paths, nil
}

// applyLibrary processes the game files in every configured library path:
// every directory is a game, which is registered under the path's category and
// queued for a config and a metadata job.
func (s *Service) applyLibrary(ctx context.Context) (string, error) {
	paths, err := s.libraryPaths(ctx)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", errors.New("library path not configured")
	}
	res := libraryResult{Paths: len(paths)}
	for _, lib := range paths {
		entries, err := os.ReadDir(lib.Path)
		if err != nil {
			return "", fmt.Errorf("read library %s: %w", lib.Path, err)
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			path := filepath.Join(lib.Path, name)
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				continue
			}
			game, created, err := s.upsertGame(ctx, name, path, lib.Category)
			if err != nil {
				return "", err
			}
			res.Games++
			if created {
				res.Created++
			} else {
				res.Updated++
			}
			for _, kind := range []models.JobKind{models.JobKindConfig, models.JobKindMetadata} {
				if _, err := Enqueue(s.db, game.ID, kind, nil); err != nil {
					return "", err
				}
				res.Jobs++
			}
			s.logger.Info("library game processed", "title", name, "game_id", game.ID, "library_path", path, "category", lib.Category)
		}
	}
	b, _ := json.Marshal(res)
	return string(b), nil
}

// upsertGame registers the game found in the library, keeping the row an
// admin already created and refreshing its library path and category.
func (s *Service) upsertGame(ctx context.Context, title, path string, category models.LibraryCategory) (*models.Game, bool, error) {
	var game models.Game
	err := s.db.WithContext(ctx).Where("title = ?", title).First(&game).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		game = models.Game{Title: title, PEGIRating: defaultPEGIRating, LibraryPath: path, Category: category}
		if err := s.db.WithContext(ctx).Create(&game).Error; err != nil {
			return nil, false, err
		}
		return &game, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	game.LibraryPath = path
	game.Category = category
	if err := s.db.WithContext(ctx).Save(&game).Error; err != nil {
		return nil, false, err
	}
	return &game, false, nil
}

// vmConfigFromPEGI maps a PEGI rating to a default VM configuration.
func vmConfigFromPEGI(pegi string) models.VMConfig {
	switch pegi {
	case "3", "7":
		return models.VMConfig{CPUCores: 2, RAMSize: 2048, DiskSize: 64}
	case "12", "16":
		return models.VMConfig{CPUCores: 4, RAMSize: 8192, DiskSize: 128}
	case "18":
		return models.VMConfig{CPUCores: 8, RAMSize: 16384, DiskSize: 256}
	default:
		return models.VMConfig{CPUCores: 2, RAMSize: 4096, DiskSize: 128}
	}
}
