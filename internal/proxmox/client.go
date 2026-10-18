package proxmox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// AuthMode selects how the client authenticates to Proxmox VE.
type AuthMode string

const (
	// AuthToken uses a PVE API token (PVEAPIToken header).
	AuthToken AuthMode = "token"
	// AuthPassword uses username/password (ticket + CSRF token).
	AuthPassword AuthMode = "password"
)

// Credentials holds Proxmox authentication credentials.
type Credentials struct {
	// Mode is AuthToken or AuthPassword.
	Mode AuthMode
	// Token is the API token secret (token mode): "userid!tokenid=secret".
	Token string
	// User is the Proxmox VE username (password mode).
	User string
	// Password is the Proxmox VE password (password mode).
	Password string
}

// Client is a Proxmox VE API client supporting token or password auth.
type Client struct {
	http   *http.Client
	base   string
	node   string

	// Token auth
	token string

	// Password auth
	creds     Credentials
	ticket    string
	csrfToken string
	ticketMu  sync.Mutex
}

// New creates a Client using API token authentication.
func New(base, node, token string) *Client {
	return &Client{
		http:  &http.Client{Timeout: 30 * time.Second},
		base:  base,
		node:  node,
		token: token,
	}
}

// NewPassword creates a Client using username/password authentication.
func NewPassword(base, node, user, password string) *Client {
	return &Client{
		http:  &http.Client{Timeout: 30 * time.Second},
		base:  base,
		node:  node,
		creds: Credentials{Mode: AuthPassword, User: user, Password: password},
	}
}

func (c *Client) isTokenAuth() bool {
	return c.creds.Mode != AuthPassword
}

// ensureTicket fetches a Proxmox ticket and CSRF token for password auth.
func (c *Client) ensureTicket() error {
	c.ticketMu.Lock()
	defer c.ticketMu.Unlock()
	if c.ticket != "" && c.csrfToken != "" {
		return nil
	}
	data := url.Values{}
	data.Set("username", c.creds.User)
	data.Set("password", c.creds.Password)
	req, err := http.NewRequest(http.MethodPost, c.base+"/api2/json/access/ticket", bytes.NewBufferString(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("proxmox ticket: %d %s", resp.StatusCode, string(body))
	}
	var res struct {
		Data struct {
			Ticket               string `json:"ticket"`
			CSRFPreventionToken  string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return err
	}
	c.ticket = res.Data.Ticket
	c.csrfToken = res.Data.CSRFPreventionToken
	return nil
}

func (c *Client) setAuthHeaders(req *http.Request, write bool) error {
	if c.isTokenAuth() {
		req.Header.Set("Authorization", "PVEAPIToken="+c.token)
		return nil
	}
	if err := c.ensureTicket(); err != nil {
		return err
	}
	req.Header.Set("Cookie", "PVEAuthCookie="+c.ticket)
	if write {
		req.Header.Set("CSRFPreventionToken", c.csrfToken)
	}
	return nil
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
	write := method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete
	if err := c.setAuthHeaders(req, write); err != nil {
		return nil, err
	}
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
		if resp.StatusCode == http.StatusUnauthorized && !c.isTokenAuth() {
			c.ticketMu.Lock()
			c.ticket = ""
			c.csrfToken = ""
			c.ticketMu.Unlock()
			return c.retryReq(method, path, body)
		}
		return nil, fmt.Errorf("proxmox %s %s: %d %s", method, path, resp.StatusCode, string(out))
	}
	return out, nil
}

func (c *Client) retryReq(method, path string, body any) ([]byte, error) {
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
	write := method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete
	if err := c.setAuthHeaders(req, write); err != nil {
		return nil, err
	}
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

// idResp from a Create/Clone response.
type idResp struct {
	Data struct {
		Vmid int `json:"vmid"`
	} `json:"data"`
}

// Clone creates a VM by cloning a template and applying cpu/mem/disk.
func (c *Client) Clone(template int, name string, cores, memoryMiB, diskGiB int) (int, error) {
	body, err := c.req(http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/clone", c.node, template), map[string]any{
		"name":   name,
		"cores":  cores,
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
