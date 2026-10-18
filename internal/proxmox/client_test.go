package proxmox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL, "pve-node", "testuser!tokenid=secret")
}

func TestListVMs(t *testing.T) {
	client := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/qemu" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []VM{
			{VMID: 100, Name: "vm1", Status: "running", Node: "pve-node"},
			{VMID: 101, Name: "vm2", Status: "stopped", Node: "pve-node"},
		}})
	})

	vms, err := client.ListVMs()
	if err != nil {
		t.Fatalf("list vms: %v", err)
	}
	if len(vms) != 2 || vms[0].VMID != 100 || vms[1].Name != "vm2" {
		t.Fatalf("unexpected vms: %+v", vms)
	}
}

func TestGetConfig(t *testing.T) {
	client := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/qemu/100/config" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": Config{Vmid: 100, Cores: 4, Memory: 8192, Size: 128}})
	})

	cfg, err := client.GetConfig("100")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg.Cores != 4 || cfg.Memory != 8192 || cfg.Size != 128 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestClone(t *testing.T) {
	client := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/qemu/9000/clone" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": 100}})
	})

	vmid, err := client.Clone(9000, "clone-vm", 2, 4096, 64)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if vmid != 100 {
		t.Fatalf("expected vmid 100, got %d", vmid)
	}
}

func TestStartStopDelete(t *testing.T) {
	var started, stopped, deleted []string
	client := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// path: api2/json/nodes/pve-node/qemu/{vmid}/status/{start|stop}
		// or:   api2/json/nodes/pve-node/qemu/{vmid}
		vmid := ""
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] == "qemu" && i+1 < len(parts) {
				vmid = parts[i+1]
				break
			}
		}
		switch {
		case strings.Contains(r.URL.Path, "/status/start"):
			started = append(started, vmid)
		case strings.Contains(r.URL.Path, "/status/stop"):
			stopped = append(stopped, vmid)
		case strings.Contains(r.URL.Path, "/qemu/") && r.Method == http.MethodDelete:
			deleted = append(deleted, vmid)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil})
	})

	if err := client.Start("100"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := client.Stop("100"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := client.Delete("100"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(started) != 1 || started[0] != "100" {
		t.Fatalf("unexpected started: %v", started)
	}
	if len(stopped) != 1 || stopped[0] != "100" {
		t.Fatalf("unexpected stopped: %v", stopped)
	}
	if len(deleted) != 1 || deleted[0] != "100" {
		t.Fatalf("unexpected deleted: %v", deleted)
	}
}
