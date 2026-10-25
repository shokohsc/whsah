# AGENTS.md — self-hosted gaming VDI service

## Entry points (do not guess)
- Real server binary: `cmd/server/main.go` — runs the HTTP API, backup scheduler/VM-deletion service, and Proxmox + Kubernetes clients.
- Real agent binary (runs inside a VM): `cmd/agent/main.go` — registration, streaming, gamesaves, SMB mount, idle shutdown.
- Real worker binary: `cmd/worker/main.go` — consumes the PostgreSQL game job queue (config generation, metadata retrieval, library scan).
- Root `main.go` is a leftover stub; it is NOT the server entrypoint.

## Commands (executable sources of truth)
- `go build ./...` — build everything
- `go vet ./...` — vet
- `go test ./...` — run tests
- `cd web && npm install` then `npm run build` — frontend (Vite + Vue 3 + Bulma). The API base is derived at runtime from the domain the frontend is served from (`https://api.<host>`); in dev it is empty, where the dev server proxies the API paths to `http://localhost:8080`.

Go module: `selfhosted-gaming-vdi`, go 1.26.0.

## Configuration (env only, no config file)
Required for a real run:
- `VDI_DATABASE_DSN` — PostgreSQL connection string (no local DB available; `db.Open`/`db.Migrate` cannot be exercised here without one)

Optional with defaults:
- `VDI_BACKUP_INTERVAL` — default `7*24h` (once per week)
- `VDI_VM_DELETION_DELAY` — default `30*24h` after `last_user_session` (1 month)
- `VDI_OIDC_ISSUER`, `VDI_OIDC_CLIENT_ID` — OIDC provider. The server refuses to start without both; endpoints (authorize, token, JWKS) come from the provider's discovery document, so Pocket ID, Keycloak, ... all work
- `VDI_DEV_MODE` — set `true` only for local development; allows an empty `VDI_OIDC_ISSUER` (any non-empty bearer token is accepted)
- `VDI_OIDC_PUBLIC_URL` — public API base used for the OIDC redirect URI (default `https://api.domain.tld`)
- `VDI_OIDC_CLIENT_SECRET` — OIDC confidential client secret (optional)
- `VDI_FRONTEND_ORIGIN` — CORS-allowed frontend origin / login redirect target (default `https://domain.tld`)
- `VDI_ADMIN_SUBJECTS` — comma-separated OIDC `sub` values promoted to `admin` on login; removing one demotes an env-promoted admin
- `VDI_DEFAULT_AGE` — age for first-time users (default `18`)
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
- `VDI_AGENT_TOKENS` — per-VM agent tokens, `vmid=token,vmid=token`. A token only authorizes its own VM id
- `VDI_VM_TEMPLATE_ID` — Proxmox template cloned for each game session (0 disables provisioning)
- `VDI_BACKUP_STORAGE` — Proxmox storage for vzdump backups; empty disables backups and VM deletion
- `VDI_LOG_LEVEL` — log level for all binaries: `debug`, `info` (default), `warn`, `error` (logs are JSON on stdout)

Worker env vars (`cmd/worker`):
- `VDI_DATABASE_DSN` — PostgreSQL connection string (queue + game state; required)
- `VDI_LIBRARY_PATH` — legacy fallback scanned by `library` jobs when no paths are configured in the UI
- `VDI_METADATA_URL` — custom HTTP metadata source: `GET {VDI_METADATA_URL}/games?title={title}` returns a metadata JSON body (empty = disabled)
- `VDI_RAWG_API_KEY` — rawg.io API key (empty = RAWG disabled)
- `VDI_NEXUSMODS_API_KEY` — Nexus Mods personal API key (empty = NexusMods disabled)
- `VDI_BROWSERLESS_URL`, `VDI_BROWSERLESS_TOKEN` — remote browserless instance for FitGirl scraping (empty URL = FitGirl disabled)
- `VDI_FITGIRL_SEARCH_URL` — FitGirl search endpoint with a `{query}` placeholder
- `VDI_YOUTUBE_API_KEY` — YouTube Data API key for live streams and video replays
- `VDI_IGDB_CLIENT_ID`, `VDI_IGDB_CLIENT_SECRET` — Twitch application credentials for IGDB metadata
- `VDI_TWITCH_CLIENT_ID`, `VDI_TWITCH_CLIENT_SECRET` — Twitch application credentials for live streams and video replays
- Steam and PCGamingWiki are always enabled (public APIs)

Agent env vars:
- `VDI_SERVER_URL`, `VDI_VM_ID`, `VDI_GAME_LIB`, `VDI_SMB_SHARE`, `VDI_SMB_CREDS`, `VDI_IDLE_SECS` (default 1800s), `VDI_AGENT_TOKEN`, `VDI_LOKI_URL`

External infrastructure is required to run end-to-end (PostgreSQL, Proxmox cluster, Kubernetes/Talos, OIDC provider, SMB share). Do not add integration tests that depend on these.

## Data model (single source of truth)
- `models/user.go`: `User` — roles `anonymous` / `gamer` / `admin`
- `models/game.go`: `Game` — title, metadata JSON, `library_path`, `category` (`repacks` / `portables` / `isos` / `setups`), embedded `VMConfig` (`cpu_cores`, `ram_size` MB, `disk_size` GB), `pegi_rating`
- `models/library.go`: `LibraryPath` — admin-configured game directory: `category` + `path`
- `models/session.go`: `Session` — user/game/vm, `backups`, `last_user_session` (drives deletion delay), per-session VM config overrides
- `models/gamejob.go`: `GameJob` — queued work on a game: `kind` (`config` / `metadata` / `library`), `status` (`pending` / `processing` / `done` / `failed`), `payload`, `result`, `error`, `attempts`. The queue lives in PostgreSQL, so any number of workers can share it. Library-wide jobs are not bound to a game and use `models.LibraryJobGameID` (0).
- All tables get `created_at`/`updated_at` (soft-deletable tables also `deleted_at`); migrations via `db.Migrate` (GORM AutoMigrate)

## Architecture boundaries
- `internal/server` — OIDC login (`/auth/login`, `/auth/callback`), provider discovery (`/.well-known/openid-configuration`), JWT verification (JWKS), first-login provisioning + admin promotion, CORS, RBAC middleware, REST API (`/games`, `/games/{id}/jobs`, `/library/jobs`, `/library/paths`, `DELETE /library`, `/jobs/{id}` (admin), `/sessions`, `/sessions/{id}/override`, `/sessions/{id}/heartbeat`, `/users`, `/users/{id}`, `/config`, `/health`, `/me`, `POST /admin/vms`, `/agent/vm/{vmid}/start|stop|config`), PEGI age gating. `GET /games` filters by the caller's age (anonymous sees PEGI 3) and only admins receive `library_path`
- `deploy/k8s/gateway.yaml` — Gateway API ListenerSet + HTTPRoute exposing the API at `api.domain.tld`; the frontend routes between the app and `/login` via a hash router (`web/src/main.js`)
- `internal/proxmox` — VM list/get/clone/start/stop/delete; token (PVEAPIToken) or username/password (ticket+CSRF) auth
- `internal/k8s` — Talos node cordon/drain/delete; in-cluster service account or kubeconfig auth
- `internal/backup` — periodic backup tick + VM deletion after `last_user_session + delay`
- `internal/agent` — register, Kyber/Apollo streaming, Ludusavi gamesave copy, SMB mount (`nosuid,noexec`), idle shutdown, Loki logging, VM ops via server only (never direct Proxmox)
- `internal/firewall` — ephemeral co-op pinholes with TTL
- `internal/worker` — PostgreSQL-backed job queue: atomically claims pending game jobs (config generation, metadata retrieval, library scan) and writes results back to `Game`
- `internal/metadata` — metadata providers (custom HTTP, RAWG, NexusMods, IGDB, Steam, PCGamingWiki, FitGirl via browserless, Twitch, YouTube) and the aggregator that cross-references/merges them; `metadata` jobs write the merged JSON to `Game.metadata` and the PEGI (or ESRB-derived PEGI) to `Game.pegi_rating`

## Gotchas
- `server` binary at repo root is a build artifact; gitignored along with `web/node_modules` and `web/dist`.
- Empty `VDI_OIDC_ISSUER` is only accepted when `VDI_DEV_MODE=true`; the server refuses to start otherwise. Never ship with dev mode on.
- The OIDC provider must serve a discovery document at `{VDI_OIDC_ISSUER}/.well-known/openid-configuration`; endpoints are never hardcoded (Pocket ID uses `/authorize` + `/api/oidc/token`, Keycloak `/protocol/openid-connect/*`).
- Frontend build output goes to `web/dist`; do not commit it.
- Agent–VM interaction on the Proxmox side goes through the server (`/agent/vm/*`); agents must not call Proxmox directly.
- The library scan runs in the worker, so the worker needs every configured library path mounted; `VDI_LIBRARY_PATH` is only a fallback when no paths are configured in the UI.
- When running the server in Kubernetes with `VDI_KUBERNETES_MODE=incluster`, the pod's ServiceAccount needs RBAC to list/patch Talos nodes.
