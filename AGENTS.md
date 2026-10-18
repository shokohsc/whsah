# AGENTS.md — self-hosted gaming VDI service

## Entry points (do not guess)
- Real server binary: `cmd/server/main.go` — runs the HTTP API, backup scheduler/VM-deletion service, and Proxmox + Kubernetes clients.
- Real agent binary (runs inside a VM): `cmd/agent/main.go` — registration, streaming, gamesaves, SMB mount, idle shutdown.
- Root `main.go` is a leftover stub; it is NOT the server entrypoint.

## Commands (executable sources of truth)
- `go build ./...` — build everything
- `go vet ./...` — vet
- `go test ./...` — run tests (only `config` package has tests; no DB integration tests run locally)
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
- `VDI_PROXMOX_BASE`, `VDI_PROXMOX_NODE`, `VDI_PROXMOX_TOKEN` — Proxmox API (API token auth)
- `VDI_KUBECONFIG` — Talos node management via Kubernetes client
- Agent: `VDI_SERVER_URL`, `VDI_VM_ID`, `VDI_GAME_LIB`, `VDI_SMB_SHARE`, `VDI_SMB_CREDS`, `VDI_IDLE_SECS` (default 1800s)

External infrastructure is required to run end-to-end (PostgreSQL, Proxmox cluster, Kubernetes/Talos, OIDC provider, SMB share). Do not add integration tests that depend on these.

## Data model (single source of truth)
- `models/user.go`: `User` — roles `anonymous` / `gamer` / `admin`
- `models/game.go`: `Game` — title, metadata JSON, `library_path`, embedded `VMConfig` (`cpu_cores`, `ram_size` MB, `disk_size` GB), `pegi_rating`
- `models/session.go`: `Session` — user/game/vm, `backups`, `last_user_session` (drives deletion delay), per-session VM config overrides
- All tables get `created_at`, `updated_at`, `deleted_at`; migrations via `db.Migrate` (GORM AutoMigrate)

## Architecture boundaries
- `internal/server` — OIDC JWT verification (JWKS), RBAC middleware, REST API (`/games`, `/sessions`, `/sessions/{id}/override`, `/users`, `/config`, `/health`), PEGI age gating
- `internal/proxmox` — VM list/get/clone/start/stop/delete
- `internal/k8s` — Talos node cordon/drain/delete
- `internal/backup` — periodic backup tick + VM deletion after `last_user_session + delay`
- `internal/agent` — register, Kyber/Apollo streaming, Ludusavi gamesave copy, SMB mount (`nosuid,noexec`), idle shutdown
- `internal/firewall` — ephemeral co-op pinholes with TTL

## Gotchas
- `server` binary at repo root is a build artifact; gitignored along with `web/node_modules` and `web/dist`.
- Empty `VDI_OIDC_ISSUER` disables real OIDC verification (dev-only). Do not ship with it empty.
- Frontend build output goes to `web/dist`; do not commit it.
