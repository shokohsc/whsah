package firewall

import (
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// iptables runs the iptables command; replaced by tests with a no-op.
var iptables = func(args ...string) error { return exec.Command("iptables", args...).Run() }

// Rule is an ephemeral co-op firewall pinhole tied to a session with a TTL.
type Rule struct {
	SessionID string
	CIDR      string
	Port      uint16
	Expires   time.Time
}

// Manager tracks ephemeral rules and applies them via iptables.
type Manager struct {
	mu   sync.Mutex
	rules map[string]Rule
}

func New() *Manager {
	return &Manager{rules: map[string]Rule{}}
}

// Add inserts a default-deny pinhole for the session, auto-removed after ttl.
func (m *Manager) Add(sessionID, cidr string, port uint16, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = iptables("-I", "INPUT", "-s", cidr, "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "ACCEPT")
	m.rules[sessionID] = Rule{SessionID: sessionID, CIDR: cidr, Port: port, Expires: time.Now().Add(ttl)}
	go m.removeAfter(sessionID, ttl)
	return nil
}

// Remove deletes the pinhole for a session.
func (m *Manager) Remove(sessionID string) {
	m.mu.Lock()
	r, ok := m.rules[sessionID]
	delete(m.rules, sessionID)
	m.mu.Unlock()
	if !ok {
		return
	}
	_ = iptables("-D", "INPUT", "-s", r.CIDR, "-p", "tcp", "--dport", fmt.Sprintf("%d", r.Port), "-j", "ACCEPT")
}

func (m *Manager) removeAfter(sessionID string, ttl time.Duration) {
	time.Sleep(ttl)
	m.Remove(sessionID)
}
