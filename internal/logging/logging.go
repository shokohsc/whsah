// Package logging provides the shared structured logger used by every
// binary. Logs are JSON on stdout so they can be shipped to Loki or parsed
// by the cluster log collector without extra tooling.
package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var (
	credentialRE = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|passwd|key)=([^&\s"']+)`)
	urlQueryRE   = regexp.MustCompile(`\?[^\s"']+`)
)

// Redact removes credentials and query strings from an error string before it
// is persisted or returned to a client.
func Redact(s string) string {
	s = credentialRE.ReplaceAllString(s, `$1=REDACTED`)
	return urlQueryRE.ReplaceAllString(s, "?REDACTED")
}

// LevelEnv is the environment variable holding the minimum log level.
const LevelEnv = "VDI_LOG_LEVEL"

// ParseLevel converts a level name into a slog.Level. Accepted names are
// debug, info, warn (warning) and error.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", name)
	}
}

// Level returns the configured minimum level, falling back to info when
// VDI_LOG_LEVEL is unset or unusable.
func Level() slog.Level {
	name := os.Getenv(LevelEnv)
	if name == "" {
		return slog.LevelInfo
	}
	lvl, err := ParseLevel(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid %s=%q: %v (using info)\n", LevelEnv, name, err)
		return slog.LevelInfo
	}
	return lvl
}

// Setup returns a JSON logger on stdout tagged with the component name,
// fanning out to any extra handlers (e.g. the agent's Loki sink).
func Setup(component string, extra ...slog.Handler) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: Level()})
	handlers := append([]slog.Handler{base}, extra...)
	return slog.New(Multi(handlers...)).With("component", component)
}

// SafeError strips the URL from *url.Error so a logged error never carries a
// (possibly credential-bearing) URL.
func SafeError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// Multi returns a handler that fans every record out to all given handlers.
func Multi(handlers ...slog.Handler) slog.Handler { return multi(handlers) }

type multi []slog.Handler

func (m multi) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multi) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m {
		// Clone: handlers consume the record.
		_ = h.Handle(ctx, r.Clone())
	}
	return nil
}

func (m multi) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multi, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multi) WithGroup(name string) slog.Handler {
	out := make(multi, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}
