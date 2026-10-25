package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		" warn ":  slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for name, want := range cases {
		got, err := ParseLevel(name)
		if err != nil {
			t.Fatalf("ParseLevel(%q): %v", name, err)
		}
		if got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", name, got, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Fatal("expected an error for an unknown level")
	}
}

func TestLevelFromEnv(t *testing.T) {
	t.Setenv(LevelEnv, "debug")
	if got := Level(); got != slog.LevelDebug {
		t.Fatalf("Level() = %v, want debug", got)
	}
	t.Setenv(LevelEnv, "nonsense")
	if got := Level(); got != slog.LevelInfo {
		t.Fatalf("Level() = %v, want info fallback", got)
	}
}

func TestMultiFansOutToEveryHandler(t *testing.T) {
	var first, second bytes.Buffer
	logger := slog.New(Multi(
		slog.NewJSONHandler(&first, nil),
		slog.NewJSONHandler(&second, nil),
	))
	logger.Info("hello", "key", "value")

	for i, buf := range []*bytes.Buffer{&first, &second} {
		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatalf("handler %d: decode: %v", i, err)
		}
		if rec["msg"] != "hello" || rec["key"] != "value" {
			t.Fatalf("handler %d: unexpected record %v", i, rec)
		}
	}
}

func TestRedact(t *testing.T) {
	in := `GET https://api.example/games?title=Hades&key=SECRET123: 500 boom password=hunter2`
	out := Redact(in)
	for _, secret := range []string{"SECRET123", "hunter2", "title=Hades"} {
		if strings.Contains(out, secret) {
			t.Fatalf("Redact left %q in %q", secret, out)
		}
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("Redact did not mark anything: %q", out)
	}
}

func TestLevelFiltersLowerRecords(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(Multi(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	logger.Info("suppressed")
	logger.Warn("kept")

	out := buf.String()
	if strings.Contains(out, "suppressed") {
		t.Fatalf("info record was not filtered: %s", out)
	}
	if !strings.Contains(out, "kept") {
		t.Fatalf("warn record was dropped: %s", out)
	}
}
