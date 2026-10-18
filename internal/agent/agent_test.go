package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegister(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/register" || r.Method != http.MethodPost {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]string
		_ = json.Unmarshal(body, &m)
		received = m["vm_id"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
	}))
	defer srv.Close()

	a := New(srv.URL, "vm-42", "/tmp/gamelib", "")
	if err := a.Register(testCtxAgent()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if received != "vm-42" {
		t.Fatalf("expected server to receive vm_id vm-42, got %q", received)
	}
}

func TestGamesaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	saveDir := filepath.Join(dir, "game-save")
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		t.Fatalf("mkdir save: %v", err)
	}
	if err := os.WriteFile(filepath.Join(saveDir, "save.dat"), []byte("save-data"), 0o644); err != nil {
		t.Fatalf("write save: %v", err)
	}

	a := New("http://x", "vm-7", dir, "")

	if err := a.SaveGamesave(saveDir); err != nil {
		t.Fatalf("save gamesave: %v", err)
	}
	// clear original to prove it's loaded from backup
	if err := os.RemoveAll(saveDir); err != nil {
		t.Fatalf("remove save: %v", err)
	}
	if err := a.LoadGamesave(saveDir); err != nil {
		t.Fatalf("load gamesave: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(saveDir, "save.dat"))
	if err != nil {
		t.Fatalf("read restored save: %v", err)
	}
	if string(data) != "save-data" {
		t.Fatalf("expected save-data, got %q", string(data))
	}
}

func testCtxAgent() context.Context { return context.Background() }

func TestLokiPayload(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	loki := NewLoki(srv.URL, "vm-42")
	loki.push(LevelInfo, "hello world")
	// wait for async send
	time.Sleep(100 * time.Millisecond)

	var parsed struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(captured, &parsed); err != nil {
		t.Fatalf("decode loki payload: %v", err)
	}
	if len(parsed.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(parsed.Streams))
	}
	if parsed.Streams[0].Stream["vm_id"] != "vm-42" {
		t.Fatalf("unexpected vm_id label: %v", parsed.Streams[0].Stream)
	}
	if len(parsed.Streams[0].Values) != 1 {
		t.Fatalf("expected 1 value, got %d", len(parsed.Streams[0].Values))
	}
	if !strings.Contains(parsed.Streams[0].Values[0][1], "[info] hello world") {
		t.Fatalf("unexpected log line: %q", parsed.Streams[0].Values[0][1])
	}
}

func TestLoggerLevels(t *testing.T) {
	lg := NewLogger(nil)
	lg.Debug("debug msg")
	lg.Info("info msg")
	lg.Warn("warn msg")
	lg.Error("error msg")
}
