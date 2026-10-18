package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level is a log level.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "unknown"
}

// Loki pushes log entries to a remote Loki endpoint.
type Loki struct {
	url    string
	vmID   string
	client *http.Client
	mu     sync.Mutex
	buf    []entry
}

type entry struct {
	ts   string
	line string
}

func NewLoki(url, vmID string) *Loki {
	return &Loki{url: strings.TrimRight(url, "/"), vmID: vmID, client: &http.Client{Timeout: 10 * time.Second}}
}

func (l *Loki) push(level Level, msg string) {
	if l.url == "" {
		return
	}
	l.mu.Lock()
	l.buf = append(l.buf, entry{ts: time.Now().Format(time.RFC3339Nano), line: fmt.Sprintf("[%s] %s", level, msg)})
	buf := l.buf
	l.buf = nil
	l.mu.Unlock()

	go l.send(buf)
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
	_, _ = l.client.Do(req)
}

// Logger writes to stdout and optionally to Loki.
type Logger struct {
	loki *Loki
}

func NewLogger(loki *Loki) *Logger {
	return &Logger{loki: loki}
}

func (lg *Logger) log(level Level, msg string) {
	log.Printf("%s: %s", level, msg)
	if lg.loki != nil {
		lg.loki.push(level, msg)
	}
}

func (lg *Logger) Debug(msg string) { lg.log(LevelDebug, msg) }
func (lg *Logger) Info(msg string)  { lg.log(LevelInfo, msg) }
func (lg *Logger) Warn(msg string)  { lg.log(LevelWarn, msg) }
func (lg *Logger) Error(msg string) { lg.log(LevelError, msg) }

// Agent runs inside a gaming VM: registers with the server, streams, handles
// gamesaves, mounts the game library, and shuts down on inactivity.
type Agent struct {
	serverURL string
	vmID      string
	gameLib   string // SMB library_path mount point
	agentToken string
	client    *http.Client
	logger    *Logger
}

func New(serverURL, vmID, gameLib, agentToken string) *Agent {
	return &Agent{
		serverURL:  serverURL,
		vmID:       vmID,
		gameLib:    gameLib,
		agentToken: agentToken,
		client:     &http.Client{Timeout: 10 * time.Second},
		logger:     NewLogger(nil),
	}
}

// WithLogger sets the Loki-backed logger.
func (a *Agent) WithLogger(l *Logger) *Agent {
	a.logger = l
	return a
}

// Register tells the server this VM is ready for a session.
func (a *Agent) Register(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"vm_id": a.vmID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.serverURL+"/agent/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.agentToken)
	resp, err := a.client.Do(req)
	if err != nil {
		a.logger.Error(fmt.Sprintf("register failed: %v", err))
		return err
	}
	defer resp.Body.Close()
	a.logger.Info("registered with server")
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
		return err
	}
	return copyDir(saveDir, dst)
}

// LoadGamesave copies the Ludusavi save back into the game's save dir.
func (a *Agent) LoadGamesave(saveDir string) error {
	src := filepath.Join(a.gameLib, "saves", a.vmID)
	return copyDir(src, saveDir)
}

// MountSMB mounts the SMB game library at gameLib (nosuid,noexec).
func (a *Agent) MountSMB(share, creds string) error {
	if err := os.MkdirAll(a.gameLib, 0o755); err != nil {
		return err
	}
	if err := exec.Command("mount", "-t", "cifs", "-o", "nosuid,noexec,credentials="+creds, share, a.gameLib).Run(); err != nil {
		a.logger.Error(fmt.Sprintf("smb mount failed: %v", err))
		return err
	}
	a.logger.Info(fmt.Sprintf("smb mounted %s at %s", share, a.gameLib))
	return nil
}

// ShutdownAfterInactivity powers off the VM after idleSecs with no input.
func (a *Agent) ShutdownAfterInactivity(idleSecs uint) {
	go func() {
		time.Sleep(time.Duration(idleSecs) * time.Second)
		a.logger.Info(fmt.Sprintf("idle shutdown after %ds", idleSecs))
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
		return fmt.Errorf("no agent token")
	}
	req, err := http.NewRequest(http.MethodPost, a.serverURL+"/agent/vm/"+vmid+"/"+op, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.agentToken)
	resp, err := a.client.Do(req)
	if err != nil {
		a.logger.Error(fmt.Sprintf("vm %s %s failed: %v", vmid, op, err))
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		a.logger.Error(fmt.Sprintf("vm %s %s: status %d", vmid, op, resp.StatusCode))
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	a.logger.Info(fmt.Sprintf("vm %s %s: ok", vmid, op))
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
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func copyDir(src, dst string) error {
	cmd := exec.Command("cp", "-a", src+"/.", dst)
	return cmd.Run()
}
