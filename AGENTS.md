# AGENTS.md — self-hosted gaming VDI service

## Entry points (do not guess)
- Real server binary: `cmd/server/main.go` — runs the HTTP API, backup scheduler/VM-deletion service, and Proxmox + Kubernetes clients.
- Real agent binary (runs inside a VM): `cmd/agent/main.go` — registration, streaming, gamesaves, SMB mount, idle shutdown.
- Root `main.go` is a leftover stub; it is NOT the server entrypoint.

## Commands (executable sources of truth)
- `go build ./...` — build everything
- `go vet ./...` — vet
- `go test ./...` — run tests
- `cd web && npm install` then `npm run build` — frontend (Vite + Vue 3 + Bulma). Dev server proxies `/api` to `http://localhost:8080`.

Go module: `selfhosted-gaming-vdi`, go 1.26.0.

## Configuration (env only, no config file)
Required for a real run:
- `VDI_DATABASE_DSN` — PostgreSQL connection string (no local DB available; `db.Open`/`db.Migrate` cannot be exercised here without one)

Optional with defaults:
- `VDI_BACKUP_INTERVAL` — default `7*24h` (once per week)
- `VDI_VM_DELETION_DELAY` — default `30*24h` after `last_user_session` (1 month)
- `VDI_OIDC_ISSUER`, `VDI_OIDC_CLIENT_ID` — OIDC provider (empty issuer = dev mode, accepts any bearer token)
- `VDI_LISTEN_ADDR` — default `:8080`
- `VDI_IDLE_SECS` — agent idle shutdown seconds (default 1800)
- `VDI_PROXMOX_BASE`, `VDI_PROXMOX_NODE` — Proxmox API base URL + node name
- `VDI_PROXMOX_AUTH_MODE` — `token` (default) or `password`
- `VDI_PROXMOX_TOKEN` — API token secret `userid!tokenid=secret` (token mode)
- `VDI_PROXMOX_USER`, `VDI_PROXMOX_PASSWORD` — username/password (password mode)
- `VDI_LIBRARY_PATH` — default game library mount path advertised to agents
- `VDI_LOKI_URL` — remote Loki endpoint for agent logs (empty = disabled)
- `VDI_KUBERNETES_MODE` — `auto` (default), `incluster`, or `outofcluster`
- `VDI_KUBECONFIG` — kubeconfig path (out-of-cluster mode)
- `VDI_AGENT_TOKEN` — shared bearer token agents use to authenticate to server agent endpoints

Agent env vars:
- `VDI_SERVER_URL`, `VDI_VM_ID`, `VDI_GAME_LIB`, `VDI_SMB_SHARE`, `VDI_SMB_CREDS`, `VDI_IDLE_SECS` (default 1800s), `VDI_AGENT_TOKEN`, `VDI_LOKI_URL`

External infrastructure is required to run end-to-end (PostgreSQL, Proxmox cluster, Kubernetes/Talos, OIDC provider, SMB share). Do not add integration tests that depend on these.

## Data model (single source of truth)
- `models/user.go`: `User` — roles `anonymous` / `gamer` / `admin`
- `models/game.go`: `Game` — title, metadata JSON, `library_path`, embedded `VMConfig` (`cpu_cores`, `ram_size` MB, `disk_size` GB), `pegi_rating`
- `models/session.go`: `Session` — user/game/vm, `backups`, `last_user_session` (drives deletion delay), per-session VM config overrides
- All tables get `created_at`, `updated_at`, `deleted_at`; migrations via `db.Migrate` (GORM AutoMigrate)

## Architecture boundaries
- `internal/server` — OIDC JWT verification (JWKS), RBAC middleware, REST API (`/games`, `/sessions`, `/sessions/{id}/override`, `/users`, `/config`, `/health`, `/me`, `/agent/vm/{vmid}/start|stop|config`), PEGI age gating
- `internal/proxmox` — VM list/get/clone/start/stop/delete; token (PVEAPIToken) or username/password (ticket+CSRF) auth
- `internal/k8s` — Talos node cordon/drain/delete; in-cluster service account or kubeconfig auth
- `internal/backup` — periodic backup tick + VM deletion after `last_user_session + delay`
- `internal/agent` — register, Kyber/Apollo streaming, Ludusavi gamesave copy, SMB mount (`nosuid,noexec`), idle shutdown, Loki logging, VM ops via server only (never direct Proxmox)
- `internal/firewall` — ephemeral co-op pinholes with TTL

## Gotchas
- `server` binary at repo root is a build artifact; gitignored along with `web/node_modules` and `web/dist`.
- Empty `VDI_OIDC_ISSUER` disables real OIDC verification (dev-only). Do not ship with it empty.
- Frontend build output goes to `web/dist`; do not commit it.
- Agent–VM interaction on the Proxmox side goes through the server (`/agent/vm/*`); agents must not call Proxmox directly.
- When running the server in Kubernetes with `VDI_KUBERNETES_MODE=incluster`, the pod's ServiceAccount needs RBAC to list/patch Talos nodes.
