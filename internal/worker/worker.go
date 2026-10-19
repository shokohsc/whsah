package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

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
	db       *gorm.DB
	fetcher  MetadataFetcher
	interval time.Duration
}

func New(db *gorm.DB, fetcher MetadataFetcher, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if fetcher == nil {
		fetcher = &HTTPFetcher{}
	}
	return &Service{db: db, fetcher: fetcher, interval: interval}
}

// Enqueue queues a job for a game. payload is marshalled to JSON (optional).
func Enqueue(db *gorm.DB, gameID uint, kind models.JobKind, payload any) (*models.GameJob, error) {
	if kind != models.JobKindConfig && kind != models.JobKindMetadata {
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.tick(ctx); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				log.Printf("worker tick: %v", err)
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
			return &job, nil
		}
		// Lost the race to another worker; try the next pending job.
	}
}

func (s *Service) process(ctx context.Context, job *models.GameJob) {
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
		log.Printf("job %d (game %d, %s): %v", job.ID, job.GameID, job.Kind, err)
	} else {
		updates["status"] = models.JobStatusDone
		updates["error"] = ""
	}
	if uerr := s.db.WithContext(ctx).Model(&models.GameJob{}).Where("id = ?", job.ID).Updates(updates).Error; uerr != nil {
		log.Printf("job %d finalize: %v", job.ID, uerr)
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
	return string(b), nil
}

// applyMetadata retrieves the game's metadata and persists it to the game row.
func (s *Service) applyMetadata(ctx context.Context, game *models.Game) (string, error) {
	meta, err := s.fetcher.FetchMetadata(ctx, game.Title)
	if err != nil {
		return "", err
	}
	if err := s.db.WithContext(ctx).Model(&models.Game{}).Where("id = ?", game.ID).Update("metadata", meta).Error; err != nil {
		return "", err
	}
	return meta, nil
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
