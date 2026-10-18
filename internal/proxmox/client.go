package proxmox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a minimal Proxmox VE API client using API token authentication.
type Client struct {
	http   *http.Client
	base   string
	token  string // "userid!tokenid=secret"
	node   string
}

func New(base, node, token string) *Client {
	return &Client{
		http: &http.Client{Timeout: 30 * time.Second},
		base: base,
		node: node,
		token: token,
	}
}

func (c *Client) req(method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	url := c.base + path
	req, err := http.NewRequest(http.MethodGet, url, r)
	if err != nil {
		return nil, err
	}
	req.Method = method
	req.Header.Set("Authorization", "PVEAPIToken="+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("proxmox %s %s: %d %s", method, path, resp.StatusCode, string(out))
	}
	return out, nil
}

// VM represents a Proxmox QEMU VM summary.
type VM struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Node   string `json:"node"`
}

// ListVMs returns QEMU VMs on the configured node.
func (c *Client) ListVMs() ([]VM, error) {
	var res struct {
		Data []VM `json:"data"`
	}
	body, err := c.req(http.MethodGet, "/api2/json/nodes/"+c.node+"/qemu", nil)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return res.Data, nil
}

// Config is a VM's configuration.
type Config struct {
	Vmid   int    `json:"vmid"`
	Cores  int    `json:"cores"`
	Memory int    `json:"memory"` // MiB
	Size   int    `json:"size"`   // GiB (scsi0 size)
}

// GetConfig fetches a VM's configuration.
func (c *Client) GetConfig(vmid string) (Config, error) {
	var cfg Config
	body, err := c.req(http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/config", c.node, vmid), nil)
	if err != nil {
		return cfg, err
	}
	var res struct {
		Data Config `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return cfg, err
	}
	return res.Data, nil
}

// VMID from a Create/Clone response.
type idResp struct {
	Data struct {
		Vmid int `json:"vmid"`
	} `json:"data"`
}

// Clone creates a VM by cloning a template and applying cpu/mem/disk.
func (c *Client) Clone(template int, name string, cores, memoryMiB, diskGiB int) (int, error) {
	body, err := c.req(http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/clone", c.node, template), map[string]any{
		"name": name,
		"cores": cores,
		"memory": memoryMiB,
	})
	if err != nil {
		return 0, err
	}
	var res idResp
	if err := json.Unmarshal(body, &res); err != nil {
		return 0, err
	}
	vmid := res.Data.Vmid
	// Apply disk size via config update.
	if diskGiB > 0 {
		c.req(http.MethodPut, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", c.node, vmid), map[string]any{"scsi0": sizeSpec(diskGiB)})
	}
	return vmid, nil
}

func sizeSpec(giB int) string {
	return fmt.Sprintf("%dG", giB)
}

func (c *Client) Start(vmid string) error {
	_, err := c.req(http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/status/start", c.node, vmid), nil)
	return err
}

func (c *Client) Stop(vmid string) error {
	_, err := c.req(http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/status/stop", c.node, vmid), nil)
	return err
}

func (c *Client) Delete(vmid string) error {
	_, err := c.req(http.MethodDelete, fmt.Sprintf("/api2/json/nodes/%s/qemu/%s", c.node, vmid), nil)
	return err
}
