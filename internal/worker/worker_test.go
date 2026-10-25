package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

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
	if err := db.AutoMigrate(&models.Game{}, &models.GameJob{}, &models.LibraryPath{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

type fakeFetcher struct {
	meta string
	err  error
}

func (f *fakeFetcher) FetchMetadata(ctx context.Context, title string) (string, error) {
	return f.meta, f.err
}

func TestEnqueueUnknownKind(t *testing.T) {
	db := testDB(t)
	if _, err := Enqueue(db, 1, models.JobKind("bogus"), nil); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestProcessConfigFromPEGI(t *testing.T) {
	db := testDB(t)
	db.Create(&models.Game{Title: "Adult Game", PEGIRating: "18", LibraryPath: "/smb/adult"})
	svc := New(db, nil, time.Millisecond)

	job, err := Enqueue(db, 1, models.JobKindConfig, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var game models.Game
	if err := db.First(&game, 1).Error; err != nil {
		t.Fatalf("game: %v", err)
	}
	if game.VMConfig != (models.VMConfig{CPUCores: 8, RAMSize: 16384, DiskSize: 256}) {
		t.Fatalf("unexpected vm config: %+v", game.VMConfig)
	}

	var done models.GameJob
	if err := db.First(&done, job.ID).Error; err != nil {
		t.Fatalf("job: %v", err)
	}
	if done.Status != models.JobStatusDone || done.Attempts != 1 {
		t.Fatalf("expected done/1, got %s/%d", done.Status, done.Attempts)
	}
}

func TestProcessConfigExplicitPayload(t *testing.T) {
	db := testDB(t)
	db.Create(&models.Game{Title: "Teen Game", PEGIRating: "12", LibraryPath: "/smb/teen"})
	svc := New(db, nil, time.Millisecond)

	job, err := Enqueue(db, 1, models.JobKindConfig, models.VMConfig{CPUCores: 16, RAMSize: 32768, DiskSize: 512})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var game models.Game
	if err := db.First(&game, 1).Error; err != nil {
		t.Fatalf("game: %v", err)
	}
	if game.VMConfig != (models.VMConfig{CPUCores: 16, RAMSize: 32768, DiskSize: 512}) {
		t.Fatalf("expected explicit config to win, got %+v", game.VMConfig)
	}

	var done models.GameJob
	if err := db.First(&done, job.ID).Error; err != nil {
		t.Fatalf("job: %v", err)
	}
	if done.Status != models.JobStatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}
}

func TestProcessMetadata(t *testing.T) {
	db := testDB(t)
	db.Create(&models.Game{Title: "Kids Game", PEGIRating: "3", LibraryPath: "/smb/kids"})
	svc := New(db, &fakeFetcher{meta: `{"genre":"puzzle"}`}, time.Millisecond)

	if _, err := Enqueue(db, 1, models.JobKindMetadata, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var game models.Game
	if err := db.First(&game, 1).Error; err != nil {
		t.Fatalf("game: %v", err)
	}
	if game.Metadata != `{"genre":"puzzle"}` {
		t.Fatalf("unexpected metadata: %q", game.Metadata)
	}
}

func TestProcessMetadataUpdatesPEGI(t *testing.T) {
	db := testDB(t)
	db.Create(&models.Game{Title: "Teen Game", PEGIRating: "18", LibraryPath: "/smb/teen"})
	svc := New(db, &fakeFetcher{meta: `{"pegi_rating":"12","genres":["puzzle"]}`}, time.Millisecond)

	if _, err := Enqueue(db, 1, models.JobKindMetadata, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var game models.Game
	if err := db.First(&game, 1).Error; err != nil {
		t.Fatalf("game: %v", err)
	}
	if game.PEGIRating != "12" {
		t.Fatalf("expected metadata PEGI to win, got %q", game.PEGIRating)
	}
	if game.Metadata != `{"pegi_rating":"12","genres":["puzzle"]}` {
		t.Fatalf("unexpected metadata: %q", game.Metadata)
	}
}

func TestProcessMetadataFailure(t *testing.T) {
	db := testDB(t)
	db.Create(&models.Game{Title: "Kids Game", PEGIRating: "3", LibraryPath: "/smb/kids"})
	svc := New(db, &fakeFetcher{err: errors.New("source down")}, time.Millisecond)

	job, err := Enqueue(db, 1, models.JobKindMetadata, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var failed models.GameJob
	if err := db.First(&failed, job.ID).Error; err != nil {
		t.Fatalf("job: %v", err)
	}
	if failed.Status != models.JobStatusFailed {
		t.Fatalf("expected failed, got %s", failed.Status)
	}
	if failed.Error != "source down" {
		t.Fatalf("unexpected error text: %q", failed.Error)
	}
}

func TestProcessMetadataFailureRedactsCredentials(t *testing.T) {
	db := testDB(t)
	db.Create(&models.Game{Title: "Kids Game", PEGIRating: "3", LibraryPath: "/smb/kids"})
	svc := New(db, &fakeFetcher{err: errors.New("GET https://api.example/games?apikey=SECRET failed")}, time.Millisecond)

	job, err := Enqueue(db, 1, models.JobKindMetadata, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var failed models.GameJob
	if err := db.First(&failed, job.ID).Error; err != nil {
		t.Fatalf("job: %v", err)
	}
	if strings.Contains(failed.Error, "SECRET") {
		t.Fatalf("credential leaked into job error: %q", failed.Error)
	}
}

func TestProcessMissingGame(t *testing.T) {
	db := testDB(t)
	svc := New(db, nil, time.Millisecond)

	job, err := Enqueue(db, 999, models.JobKindConfig, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var failed models.GameJob
	if err := db.First(&failed, job.ID).Error; err != nil {
		t.Fatalf("job: %v", err)
	}
	if failed.Status != models.JobStatusFailed {
		t.Fatalf("expected failed, got %s", failed.Status)
	}
}

func TestProcessLibrary(t *testing.T) {
	db := testDB(t)
	lib := t.TempDir()
	for _, dir := range []string{"Kids Game", "Adult Game"} {
		if err := os.Mkdir(filepath.Join(lib, dir), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	// A file and a hidden directory are not games.
	if err := os.WriteFile(filepath.Join(lib, "setup.exe"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(lib, ".hidden"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	svc := New(db, nil, time.Millisecond).WithLibraryPath(lib)
	job, err := Enqueue(db, models.LibraryJobGameID, models.JobKindLibrary, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var done models.GameJob
	if err := db.First(&done, job.ID).Error; err != nil {
		t.Fatalf("job: %v", err)
	}
	if done.Status != models.JobStatusDone {
		t.Fatalf("expected done, got %s (%s)", done.Status, done.Error)
	}

	var games []models.Game
	if err := db.Find(&games).Error; err != nil {
		t.Fatalf("games: %v", err)
	}
	if len(games) != 2 {
		t.Fatalf("expected 2 games, got %d", len(games))
	}
	for _, g := range games {
		if g.LibraryPath != filepath.Join(lib, g.Title) {
			t.Fatalf("unexpected library path for %q: %q", g.Title, g.LibraryPath)
		}
		if g.PEGIRating == "" {
			t.Fatalf("game %q has no pegi rating", g.Title)
		}
	}

	// Every game is queued for a config and a metadata job.
	var jobs []models.GameJob
	if err := db.Where("kind IN ?", []models.JobKind{models.JobKindConfig, models.JobKindMetadata}).Find(&jobs).Error; err != nil {
		t.Fatalf("jobs: %v", err)
	}
	if len(jobs) != 4 {
		t.Fatalf("expected 4 jobs, got %d", len(jobs))
	}
}

func TestProcessLibraryCategories(t *testing.T) {
	db := testDB(t)
	repacks := t.TempDir()
	isos := t.TempDir()
	if err := os.Mkdir(filepath.Join(repacks, "Packed Game"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(isos, "Disc Game"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db.Create(&models.LibraryPath{Category: models.CategoryRepacks, Path: repacks})
	db.Create(&models.LibraryPath{Category: models.CategoryISOs, Path: isos})

	svc := New(db, nil, time.Millisecond)
	if _, err := Enqueue(db, models.LibraryJobGameID, models.JobKindLibrary, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	want := map[string]models.LibraryCategory{
		"Packed Game": models.CategoryRepacks,
		"Disc Game":   models.CategoryISOs,
	}
	var games []models.Game
	db.Find(&games)
	if len(games) != len(want) {
		t.Fatalf("expected %d games, got %d", len(want), len(games))
	}
	for _, g := range games {
		if g.Category != want[g.Title] {
			t.Fatalf("game %q category = %q, want %q", g.Title, g.Category, want[g.Title])
		}
	}
}

func TestProcessLibraryKeepsExistingGame(t *testing.T) {
	db := testDB(t)
	game := models.Game{Title: "Kids Game", PEGIRating: "3", LibraryPath: "/smb/kids"}
	if err := db.Create(&game).Error; err != nil {
		t.Fatalf("create game: %v", err)
	}
	lib := t.TempDir()
	if err := os.Mkdir(filepath.Join(lib, "Kids Game"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	svc := New(db, nil, time.Millisecond).WithLibraryPath(lib)
	if _, err := Enqueue(db, models.LibraryJobGameID, models.JobKindLibrary, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var count int64
	db.Model(&models.Game{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 game, got %d", count)
	}
	var got models.Game
	db.First(&got, game.ID)
	if got.PEGIRating != "3" {
		t.Fatalf("rating changed to %q", got.PEGIRating)
	}
	if got.LibraryPath != filepath.Join(lib, "Kids Game") {
		t.Fatalf("library path = %q", got.LibraryPath)
	}
}

func TestProcessLibraryWithoutPath(t *testing.T) {
	db := testDB(t)
	svc := New(db, nil, time.Millisecond)

	job, err := Enqueue(db, models.LibraryJobGameID, models.JobKindLibrary, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var failed models.GameJob
	if err := db.First(&failed, job.ID).Error; err != nil {
		t.Fatalf("job: %v", err)
	}
	if failed.Status != models.JobStatusFailed {
		t.Fatalf("expected failed, got %s", failed.Status)
	}
}

func TestClaimConsumesPendingJob(t *testing.T) {
	db := testDB(t)
	db.Create(&models.Game{Title: "G", PEGIRating: "3", LibraryPath: "/smb/g"})
	svc := New(db, nil, time.Millisecond)

	if _, err := Enqueue(db, 1, models.JobKindConfig, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	job, err := svc.claim(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if job.Status != models.JobStatusProcessing || job.Attempts != 1 {
		t.Fatalf("expected processing/1, got %s/%d", job.Status, job.Attempts)
	}

	// No more pending jobs.
	if _, err := svc.claim(context.Background()); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound, got %v", err)
	}
}

func TestHTTPFetcherNotConfigured(t *testing.T) {
	f := &HTTPFetcher{}
	if _, err := f.FetchMetadata(context.Background(), "x"); err == nil {
		t.Fatal("expected error when base url is empty")
	}
}

func TestHTTPFetcherSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("title") != "Teen Game" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"genre":"rpg"}`))
	}))
	defer srv.Close()

	f := &HTTPFetcher{BaseURL: srv.URL}
	meta, err := f.FetchMetadata(context.Background(), "Teen Game")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta != `{"genre":"rpg"}` {
		t.Fatalf("unexpected meta: %q", meta)
	}
}
