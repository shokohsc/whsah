package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if err := db.AutoMigrate(&models.Game{}, &models.GameJob{}); err != nil {
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
