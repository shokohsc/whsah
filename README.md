# selfhosted-gaming-vdi

Self-hosted VDI service oriented toward gaming VMs. Private/internal only; not exposed to the public internet.

## Tech stack

- Go server + Go agent (module `selfhosted-gaming-vdi`, go 1.26.0)
- Vue 3 + Vite + Bulma frontend (built to `web/dist`)
- PostgreSQL via GORM (`gorm.io/gorm`, `gorm.io/driver/postgres`)
- Proxmox cluster (VM host)
- Kubernetes/Talos via `k8s.io/client-go`
- OIDC auth (`golang.org/x/oauth2`, `golang-jwt/jwt/v5`)
- Kyber (primary) / Apollo + Moonlight Web (fallback) streaming
- Ludusavi gamesave copy
- SMB game library mount

## Architecture boundaries

- `internal/server` — OIDC JWT verification (JWKS), RBAC middleware, REST API (`/games`, `/sessions`, `/sessions/{id}/override`, `/users`, `/config`, `/health`), PEGI age gating
- `internal/proxmox` — VM list/get/clone/start/stop/delete
- `internal/k8s` — Talos node cordon/drain/delete
- `internal/backup` — periodic backup tick + VM deletion after `last_user_session + delay`
- `internal/agent` — register, Kyber/Apollo streaming, Ludusavi gamesave copy, SMB mount (`nosuid,noexec`), idle shutdown
- `internal/firewall` — ephemeral co-op pinholes with TTL

## Quickstart

Required env var (env only, no config file):

- `VDI_DATABASE_DSN` — PostgreSQL connection string. No local DB is available; `db.Open`/`db.Migrate` cannot be exercised without one.

Optional env vars with defaults:

- `VDI_BACKUP_INTERVAL` — default `7*24h` (once per week)
- `VDI_VM_DELETION_DELAY` — default `30*24h` after `last_user_session` (1 month)
- `VDI_OIDC_ISSUER`, `VDI_OIDC_CLIENT_ID` — OIDC provider (empty issuer = dev mode, accepts any bearer token)
- `VDI_LISTEN_ADDR` — default `:8080`
- `VDI_PROXMOX_BASE`, `VDI_PROXMOX_NODE`, `VDI_PROXMOX_TOKEN` — Proxmox API (API token auth)
- `VDI_KUBECONFIG` — Talos node management via Kubernetes client

Agent env vars:

- `VDI_SERVER_URL`, `VDI_VM_ID`, `VDI_GAME_LIB`, `VDI_SMB_SHARE`, `VDI_SMB_CREDS`, `VDI_IDLE_SECS` (default `1800s`)

Build, run, frontend:

```
go build ./...
go run ./cmd/server       # HTTP API, backup scheduler/VM-deletion service, Proxmox + Kubernetes clients
go run ./cmd/agent        # runs inside a VM: registration, streaming, gamesaves, SMB mount, idle shutdown
cd web && npm install && npm run build
```

Tests: `go test ./...` (only `config` package has tests; no DB integration tests run locally).

## Data model

- `User` — roles `anonymous` / `gamer` / `admin`
- `Game` — title, metadata JSON, `library_path`, embedded `VMConfig` (`cpu_cores`, `ram_size` MB, `disk_size` GB), `pegi_rating`
- `Session` — user/game/vm, `backups`, `last_user_session`, per-session VM config overrides

All tables get `created_at`, `updated_at`, `deleted_at`; migrations via `db.Migrate` (GORM AutoMigrate).

## API endpoints

- `GET/POST /games`
- `GET/POST/DELETE /sessions`
- `POST /sessions/{id}/override`
- `GET /users` (admin)
- `GET /config`
- `GET /health`

## Security model

- OIDC/JWKS verification; empty `VDI_OIDC_ISSUER` = dev-only mode that accepts any bearer token. Do not ship with it empty.
- RBAC middleware (anonymous/gamer/admin)
- PEGI age gating
- mTLS server↔agent
- SMB mount `nosuid,noexec`
- Ephemeral co-op firewall pinholes with TTL
- X25519/age encrypted notifications

## Gotchas

- Root `main.go` is a leftover stub; it is NOT the server entrypoint. Use `cmd/server/main.go` / `cmd/agent/main.go`.
- `server` binary at repo root is a build artifact; gitignored along with `web/node_modules` and `web/dist`.
- Frontend build output goes to `web/dist`; do not commit it.
- No local DB is available, so integration tests cannot run without `VDI_DATABASE_DSN`.
