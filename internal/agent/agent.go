package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Agent runs inside a gaming VM: registers with the server, streams, handles
// gamesaves, mounts the game library, and shuts down on inactivity.
type Agent struct {
	serverURL string
	vmID      string
	gameLib   string // SMB library_path mount point
	client    *http.Client
}

func New(serverURL, vmID, gameLib string) *Agent {
	return &Agent{serverURL: serverURL, vmID: vmID, gameLib: gameLib, client: &http.Client{Timeout: 10 * time.Second}}
}

// Register tells the server this VM is ready for a session.
func (a *Agent) Register(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"vm_id": a.vmID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.serverURL+"/agent/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// StartStreaming launches the streaming backend: Kyber primary, Apollo +
// Moonlight Web fallback.
func (a *Agent) StartStreaming() error {
	if _, err := exec.LookPath("kyber"); err == nil {
		go exec.Command("kyber", "server").Start()
		return nil
	}
	if _, err := exec.LookPath("apollo"); err == nil {
		go exec.Command("apollo", "server").Start()
		return nil
	}
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
	return exec.Command("mount", "-t", "cifs", "-o", "nosuid,noexec,credentials="+creds, share, a.gameLib).Run()
}

// ShutdownAfterInactivity powers off the VM after idleSecs with no input.
func (a *Agent) ShutdownAfterInactivity(idleSecs uint) {
	go func() {
		time.Sleep(time.Duration(idleSecs) * time.Second)
		_ = exec.Command("systemctl", "poweroff").Run()
	}()
}

func copyDir(src, dst string) error {
	cmd := exec.Command("cp", "-a", src+"/.", dst)
	return cmd.Run()
}
