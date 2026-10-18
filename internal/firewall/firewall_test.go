package firewall

import (
	"strings"
	"testing"
	"time"
)

func TestAddAndRemoveRule(t *testing.T) {
	var calls []string
	iptables = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	defer func() { iptables = func(args ...string) error { return nil } }()

	m := New()
	if err := m.Add("sess-1", "10.0.0.0/24", 1234, time.Hour); err != nil {
		t.Fatalf("add: %v", err)
	}

	if len(calls) != 1 || !strings.Contains(calls[0], "-I") || !strings.Contains(calls[0], "10.0.0.0/24") || !strings.Contains(calls[0], "1234") {
		t.Fatalf("unexpected iptables add call: %q", calls)
	}

	m.Remove("sess-1")
	if len(calls) != 2 || !strings.Contains(calls[1], "-D") {
		t.Fatalf("expected iptables delete call, got: %q", calls)
	}
}

func TestRemoveUnknownSessionIsNoop(t *testing.T) {
	iptables = func(args ...string) error { return nil }
	defer func() { iptables = func(args ...string) error { return nil } }()

	m := New()
	m.Remove("nonexistent") // should not panic
}

func TestTTLRemoval(t *testing.T) {
	iptables = func(args ...string) error { return nil }
	defer func() { iptables = func(args ...string) error { return nil } }()

	m := New()
	// removeAfter calls Remove after ttl; use a tiny ttl
	m.Add("sess-ttl", "10.0.0.1/32", 80, 10*time.Millisecond)

	// give the goroutine time to fire
	time.Sleep(50 * time.Millisecond)

	if _, ok := m.rules["sess-ttl"]; ok {
		t.Fatal("rule should be auto-removed after TTL")
	}
}
