package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"selfhosted-gaming-vdi/internal/logging"
)

// Loki pushes log entries to a remote Loki endpoint.
type Loki struct {
	url    string
	vmID   string
	client *http.Client
	level  slog.Level
	sink   *lokiSink
	attrs  []slog.Attr
	groups []string
}

// lokiSink holds the push buffer shared by every handler derived from a Loki.
type lokiSink struct {
	mu  sync.Mutex
	buf []entry
}

type entry struct {
	ts   string
	line string
}

// NewLoki returns a slog.Handler shipping records to a Loki endpoint. An
// empty URL disables the sink.
func NewLoki(url, vmID string, level slog.Level) *Loki {
	return &Loki{
		url:    strings.TrimRight(url, "/"),
		vmID:   vmID,
		level:  level,
		client: &http.Client{Timeout: 10 * time.Second},
		sink:   &lokiSink{},
	}
}

// Enabled implements slog.Handler: records below the configured level, or
// any record at all when no Loki URL is configured, are dropped.
func (l *Loki) Enabled(_ context.Context, level slog.Level) bool {
	return l.url != "" && level >= l.level
}

// Handle implements slog.Handler: the record is rendered as JSON and pushed
// to Loki in the background.
func (l *Loki) Handle(ctx context.Context, r slog.Record) error {
	if l.url == "" {
		return nil
	}
	var buf bytes.Buffer
	var h slog.Handler = slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	if len(l.attrs) > 0 {
		h = h.WithAttrs(l.attrs)
	}
	for _, g := range l.groups {
		h = h.WithGroup(g)
	}
	if err := h.Handle(ctx, r); err != nil {
		return err
	}
	l.sink.mu.Lock()
	l.sink.buf = append(l.sink.buf, entry{ts: r.Time.Format(time.RFC3339Nano), line: strings.TrimSpace(buf.String())})
	entries := l.sink.buf
	l.sink.buf = nil
	l.sink.mu.Unlock()

	go l.send(entries)
	return nil
}

// WithAttrs implements slog.Handler.
func (l *Loki) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *l
	c.attrs = append(slices.Clip(l.attrs), attrs...)
	return &c
}

// WithGroup implements slog.Handler.
func (l *Loki) WithGroup(name string) slog.Handler {
	c := *l
	c.groups = append(slices.Clip(l.groups), name)
	return &c
}

func (l *Loki) send(entries []entry) {
	values := make([][]string, len(entries))
	for i, e := range entries {
		values[i] = []string{e.ts, e.line}
	}
	body := map[string]any{
		"streams": []map[string]any{
			{
				"stream": map[string]string{"job": "vdi-agent", "vm_id": l.vmID},
				"values": values,
			},
		},
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, l.url+"/loki/api/v1/push", bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := l.client.Do(req); err != nil {
		fmt.Fprintf(os.Stderr, "loki push failed: %v\n", logging.SafeError(err))
	}
}

// Agent runs inside a gaming VM: registers with the server, streams, handles
// gamesaves, mounts the game library, and shuts down on inactivity.
type Agent struct {
	serverURL  string
	vmID       string
	gameLib    string // SMB library_path mount point
	agentToken string
	client     *http.Client
	logger     *slog.Logger
}

func New(serverURL, vmID, gameLib, agentToken string) *Agent {
	return &Agent{
		serverURL:  serverURL,
		vmID:       vmID,
		gameLib:    gameLib,
		agentToken: agentToken,
		client:     &http.Client{Timeout: 10 * time.Second},
		logger:     slog.Default(),
	}
}

// WithLogger sets the logger used by the agent.
func (a *Agent) WithLogger(l *slog.Logger) *Agent {
	a.logger = l
	return a
}

// Register tells the server this VM is ready for a session.
func (a *Agent) Register(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"vm_id": a.vmID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.serverURL+"/agent/register", bytes.NewReader(body))
	if err != nil {
		a.logger.Error("register request failed", "vm_id", a.vmID, "err", logging.SafeError(err))
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.agentToken)
	resp, err := a.client.Do(req)
	if err != nil {
		a.logger.Error("register failed", "vm_id", a.vmID, "err", logging.SafeError(err))
		return err
	}
	defer resp.Body.Close()
	a.logger.Info("registered with server", "vm_id", a.vmID, "status", resp.StatusCode)
	return nil
}

// StartStreaming launches the streaming backend: Kyber primary, Apollo +
// Moonlight Web fallback.
func (a *Agent) StartStreaming() error {
	if _, err := exec.LookPath("kyber"); err == nil {
		go exec.Command("kyber", "server").Start()
		a.logger.Info("streaming: kyber")
		return nil
	}
	if _, err := exec.LookPath("apollo"); err == nil {
		go exec.Command("apollo", "server").Start()
		a.logger.Info("streaming: apollo")
		return nil
	}
	a.logger.Error("no streaming backend found (kyber/apollo)")
	return fmt.Errorf("no streaming backend found (kyber/apollo)")
}

// SaveGamesave copies the local game save to the Ludusavi-managed location.
func (a *Agent) SaveGamesave(saveDir string) error {
	dst := filepath.Join(a.gameLib, "saves", a.vmID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		a.logger.Error("gamesave save failed", "vm_id", a.vmID, "err", err)
		return err
	}
	if err := copyDir(saveDir, dst); err != nil {
		a.logger.Error("gamesave save failed", "vm_id", a.vmID, "err", err)
		return err
	}
	a.logger.Info("gamesave saved", "vm_id", a.vmID, "path", dst)
	return nil
}

// LoadGamesave copies the Ludusavi save back into the game's save dir.
func (a *Agent) LoadGamesave(saveDir string) error {
	src := filepath.Join(a.gameLib, "saves", a.vmID)
	if err := copyDir(src, saveDir); err != nil {
		a.logger.Error("gamesave load failed", "vm_id", a.vmID, "err", err)
		return err
	}
	a.logger.Info("gamesave loaded", "vm_id", a.vmID, "path", saveDir)
	return nil
}

// MountSMB mounts the SMB game library at gameLib (nosuid,noexec).
func (a *Agent) MountSMB(share, creds string) error {
	if err := os.MkdirAll(a.gameLib, 0o755); err != nil {
		a.logger.Error("smb mount failed", "vm_id", a.vmID, "err", err)
		return err
	}
	if err := exec.Command("mount", "-t", "cifs", "-o", "nosuid,noexec,credentials="+creds, share, a.gameLib).Run(); err != nil {
		a.logger.Error("smb mount failed", "vm_id", a.vmID, "err", err)
		return err
	}
	a.logger.Info("smb mounted", "vm_id", a.vmID, "path", a.gameLib)
	return nil
}

// ShutdownAfterInactivity powers off the VM after idleSecs with no input.
func (a *Agent) ShutdownAfterInactivity(idleSecs uint) {
	a.logger.Info("idle shutdown scheduled", "vm_id", a.vmID, "idle_secs", idleSecs)
	go func() {
		time.Sleep(time.Duration(idleSecs) * time.Second)
		a.logger.Info("idle shutdown", "vm_id", a.vmID)
		_ = exec.Command("systemctl", "poweroff").Run()
	}()
}

// StartVM asks the server to start the VM on Proxmox (agent never talks to
// Proxmox directly).
func (a *Agent) StartVM(vmid string) error {
	return a.vmOp(http.MethodPost, vmid, "start")
}

// StopVM asks the server to stop the VM on Proxmox.
func (a *Agent) StopVM(vmid string) error {
	return a.vmOp(http.MethodPost, vmid, "stop")
}

// GetVMConfig asks the server for the VM's Proxmox configuration.
func (a *Agent) GetVMConfig(vmid string) ([]byte, error) {
	return a.vmGet(vmid, "config")
}

func (a *Agent) vmOp(method, vmid, op string) error {
	if a.agentToken == "" {
		a.logger.Error("vm op rejected: no agent token", "vmid", vmid, "op", op)
		return fmt.Errorf("no agent token")
	}
	req, err := http.NewRequest(http.MethodPost, a.serverURL+"/agent/vm/"+vmid+"/"+op, nil)
	if err != nil {
		a.logger.Error("vm op request failed", "vmid", vmid, "op", op, "err", logging.SafeError(err))
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.agentToken)
	resp, err := a.client.Do(req)
	if err != nil {
		a.logger.Error("vm op failed", "vmid", vmid, "op", op, "err", logging.SafeError(err))
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		a.logger.Error("vm op rejected", "vmid", vmid, "op", op, "status", resp.StatusCode)
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	a.logger.Info("vm op ok", "vmid", vmid, "op", op)
	return nil
}

func (a *Agent) vmGet(vmid, op string) ([]byte, error) {
	if a.agentToken == "" {
		return nil, fmt.Errorf("no agent token")
	}
	req, err := http.NewRequest(http.MethodGet, a.serverURL+"/agent/vm/"+vmid+"/"+op, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.agentToken)
	resp, err := a.client.Do(req)
	if err != nil {
		a.logger.Error("vm get failed", "vmid", vmid, "op", op, "err", logging.SafeError(err))
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func copyDir(src, dst string) error {
	cmd := exec.Command("cp", "-a", src+"/.", dst)
	return cmd.Run()
}
