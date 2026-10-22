# selfhosted-gaming-vdi

Self-hosted VDI service oriented toward gaming VMs. Private/internal only; not exposed to the public internet.

## Tech stack

- Go server + Go agent + Go worker (module `selfhosted-gaming-vdi`, go 1.26.0)
- Vue 3 + Vite + Bulma frontend (built to `web/dist`)
- PostgreSQL via GORM (`gorm.io/gorm`, `gorm.io/driver/postgres`)
- Proxmox cluster (VM host)
- Kubernetes/Talos via `k8s.io/client-go`
- OIDC auth (`golang.org/x/oauth2`, `golang-jwt/jwt/v5`)
- Kyber (primary) / Apollo + Moonlight Web (fallback) streaming
- Ludusavi gamesave copy
- SMB game library mount
- Loki remote logging for agent logs
- Structured JSON logs (`log/slog`) on stdout, level set by `VDI_LOG_LEVEL`

## Architecture boundaries

- `internal/server` — OIDC login (`/auth/login`, `/auth/callback`), JWT verification (JWKS), first-login user provisioning and admin-subject promotion, CORS, RBAC middleware, REST API (`/games`, `/games/{id}/jobs`, `/library/jobs`, `/jobs/{id}`, `/sessions`, `/sessions/{id}/override`, `/users`, `/users/{id}`, `/config`, `/health`, `/me`, `/agent/vm/{vmid}/start|stop|config`), PEGI age gating, agent token auth
- `internal/proxmox` — VM list/get/clone/start/stop/delete; token (PVEAPIToken) or username/password (ticket+CSRF) auth
- `internal/k8s` — Talos node cordon/drain/delete; in-cluster service account or kubeconfig auth
- `internal/backup` — periodic backup tick + VM deletion after `last_user_session + delay`
- `internal/agent` — register, Kyber/Apollo streaming, Ludusavi gamesave copy, SMB mount (`nosuid,noexec`), idle shutdown, Loki logging, VM ops via server only (never direct Proxmox)
- `internal/firewall` — ephemeral co-op pinholes with TTL
- `internal/worker` — PostgreSQL-backed job queue: claims pending game jobs (config generation, metadata retrieval, library scan) and writes results back to `Game`

## Quickstart

Required env var (env only, no config file):

- `VDI_DATABASE_DSN` — PostgreSQL connection string. No local DB is available; `db.Open`/`db.Migrate` cannot be exercised without one.

Server optional env vars with defaults:

- `VDI_BACKUP_INTERVAL` — default `7*24h` (once per week)
- `VDI_VM_DELETION_DELAY` — default `30*24h` after `last_user_session` (1 month)
- `VDI_OIDC_ISSUER`, `VDI_OIDC_CLIENT_ID` — OIDC provider (empty issuer = dev mode, accepts any bearer token). Endpoints are read from the provider's discovery document (`{issuer}/.well-known/openid-configuration`), so Pocket ID, Keycloak, Authentik, ... all work.
- `VDI_OIDC_PUBLIC_URL` — public API base URL used to build the OIDC redirect URI (default `https://api.domain.tld`; must match `VDI_OIDC_PUBLIC_URL`/auth/callback registered at the provider)
- `VDI_OIDC_CLIENT_SECRET` — OIDC confidential client secret (optional)
- `VDI_FRONTEND_ORIGIN` — frontend origin allowed by CORS and used as the login redirect target (default `https://domain.tld`)
- `VDI_ADMIN_SUBJECTS` — comma-separated OIDC subjects, usernames or emails provisioned (or promoted) to `admin` on login
- `VDI_DEFAULT_AGE` — age assigned to first-time users (default `18`)
- `VDI_LISTEN_ADDR` — default `:8080`
- `VDI_IDLE_SECS` — agent idle shutdown in seconds (default `1800`)
- `VDI_PROXMOX_BASE`, `VDI_PROXMOX_NODE` — Proxmox API base URL + node name
- `VDI_PROXMOX_AUTH_MODE` — `token` (default) or `password`
- `VDI_PROXMOX_TOKEN` — API token secret `userid!tokenid=secret` (token mode)
- `VDI_PROXMOX_USER`, `VDI_PROXMOX_PASSWORD` — username/password (password mode)
- `VDI_LIBRARY_PATH` — default game library mount path advertised to agents
- `VDI_LOKI_URL` — remote Loki endpoint for agent logs (empty = disabled)
- `VDI_KUBERNETES_MODE` — `auto` (default), `incluster`, or `outofcluster`
- `VDI_KUBECONFIG` — kubeconfig path (out-of-cluster mode)
- `VDI_AGENT_TOKEN` — shared bearer token agents use to authenticate to the server
- `VDI_LOG_LEVEL` — log level for all binaries: `debug`, `info` (default), `warn`, `error`. Logs are JSON on stdout.

Agent env vars:

- `VDI_SERVER_URL` — server base URL (default `http://localhost:8080`)
- `VDI_VM_ID` — VM identifier this agent represents
- `VDI_GAME_LIB` — game library path (falls back to `VDI_LIBRARY_PATH`)
- `VDI_SMB_SHARE`, `VDI_SMB_CREDS` — SMB share + credentials (optional mount)
- `VDI_IDLE_SECS` — idle shutdown seconds (default 1800)
- `VDI_AGENT_TOKEN` — bearer token sent to server agent endpoints
- `VDI_LOKI_URL` — remote Loki endpoint for agent logs (optional)

Worker env vars (`cmd/worker`):
- `VDI_DATABASE_DSN` — PostgreSQL connection string (queue + game state; required)
- `VDI_METADATA_URL` — HTTP metadata source for `metadata` jobs: `GET {VDI_METADATA_URL}/games?title={title}` returns the metadata JSON body (empty = metadata retrieval fails as "not configured")
- `VDI_LIBRARY_PATH` — directory holding the game files; `library` jobs scan it (empty = library jobs fail as "not configured")

Build, run, frontend:

```
go build ./...
go run ./cmd/server       # HTTP API, backup scheduler/VM-deletion service, Proxmox + Kubernetes clients
go run ./cmd/worker       # consumes game job queue (config generation, metadata retrieval) from PostgreSQL
go run ./cmd/agent        # runs inside a VM: registration, streaming, gamesaves, SMB mount, idle shutdown
cd web && npm install && npm run build
```

Tests: `go test ./...`

## OIDC provider

The server does not hardcode provider URLs: the authorization and token endpoints and the JWKS URL are read from the provider's discovery document (`{VDI_OIDC_ISSUER}/.well-known/openid-configuration`). Any provider that serves one works — Pocket ID, Keycloak, Authentik, ...

### Pocket ID

1. In Pocket ID, add an OIDC client: callback URL `https://api.domain.tld/auth/callback` (exactly the `VDI_OIDC_PUBLIC_URL` of the server), client type confidential, then allow the users who may sign in.
2. Configure the server with the client credentials from the client's page:

```
VDI_OIDC_ISSUER=https://id.domain.tld
VDI_OIDC_CLIENT_ID=<client id>
VDI_OIDC_CLIENT_SECRET=<client secret>
VDI_OIDC_PUBLIC_URL=https://api.domain.tld
```

3. List an admin in `VDI_ADMIN_SUBJECTS` — the Pocket ID user name or email works, the `sub` claim is the opaque user ID.

The scopes requested are `openid email profile`; Pocket ID puts `preferred_username` (the user name) and `email` into the ID token, and the server uses them for the user name and email address.

## Kubernetes deployment

See `deploy/k8s/` for example YAML manifests: server Deployment + Service + ServiceAccount with RBAC for in-cluster K8s access, a Gateway API ListenerSet + HTTPRoute exposing the API at `api.domain.tld`, an example agent Deployment, and a worker Deployment that consumes the PostgreSQL game job queue. The server autodetects in-cluster auth via `KUBERNETES_SERVICE_HOST` when `VDI_KUBERNETES_MODE=auto`; set `VDI_KUBERNETES_MODE=incluster` to force service-account auth or `outofcluster` with `VDI_KUBECONFIG` to use a kubeconfig.

Apply the server stack:

```
kubectl apply -f deploy/k8s/server.yaml
```

Apply the game job worker (shares the same PostgreSQL database as the server):

```
kubectl apply -f deploy/k8s/worker.yaml
```

Create a secret for Proxmox credentials and the agent token before applying:

```
kubectl create secret generic vdi-proxmox \
  --from-literal=user=root@pam \
  --from-literal=password='<proxmox-password>'
kubectl create secret generic vdi-agent \
  --from-literal=agent-token='<shared-agent-token>'
```

## Data model

- `User` — roles `anonymous` / `gamer` / `admin`
- `Game` — title, metadata JSON, `library_path`, embedded `VMConfig` (`cpu_cores`, `ram_size` MB, `disk_size` GB), `pegi_rating`
- `Session` — user/game/vm, `backups`, `last_user_session`, per-session VM config overrides
- `GameJob` — queued work on a game: `kind` (`config` / `metadata` / `library`), `status` (`pending` / `processing` / `done` / `failed`), `payload`, `result`, `error`, `attempts`. The queue lives in PostgreSQL, so any number of workers can share it.

All tables get `created_at`, `updated_at`, `deleted_at`; migrations via `db.Migrate` (GORM AutoMigrate).

## API endpoints

The API is served at the `api.domain.tld` subdomain (see `deploy/k8s/gateway.yaml`), not at a `domain.tld/api` path prefix. The frontend derives the API base URL at runtime from the domain it is served from (`api.<host>`), so one image works on any domain.

- `GET /auth/login` — redirects to the OIDC provider (dev mode: straight back to the frontend `/login` route with a token); `GET /auth/callback` completes the flow and returns the ID token to the frontend
- `GET/POST /games` — `PUT /games/{id}` (admin) updates `library_path`
- `PUT /users/{id}` (admin) — change a user's `role` (`anonymous` / `gamer` / `admin`)
- `POST /games/{id}/jobs` (admin) — enqueue a game job (`kind`: `config` or `metadata`, optional `payload`); returns `202` with the queued job
- `POST /library/jobs` (admin) — start the workers on the game files in the library; returns `202` with the queued `library` job
- `GET /games/{id}/jobs` (admin) — list a game's jobs
- `GET /jobs/{id}` — poll a job's `status`, `result`, `error`
- `GET /me` — current user id/username/role/age
- `GET/POST/DELETE /sessions`
- `POST /sessions/{id}/override` — VM config override
- `GET /users` (admin)
- `GET /config`
- `GET /health`
- `POST /agent/vm/{vmid}/start`, `POST /agent/vm/{vmid}/stop`, `GET /agent/vm/{vmid}/config` (agent token auth)

## Game job queue

Games are configured and enriched asynchronously through a PostgreSQL-backed queue, so slow or external work never blocks the API:

1. An admin enqueues a job: `POST /games/{id}/jobs` with `kind` = `config` or `metadata` (and an optional `payload`).
2. One or more `cmd/worker` processes claim pending jobs atomically (`status: pending` → `processing`) directly from PostgreSQL, so workers can be scaled horizontally.
3. `config` jobs generate the game's `VMConfig` — from the job `payload` when provided, otherwise derived from the PEGI rating — and write it back to the `Game` row.
4. `metadata` jobs retrieve metadata from `VDI_METADATA_URL` (`GET {VDI_METADATA_URL}/games?title={title}`) and write it to `Game.metadata`.
5. Results land in `GameJob.result`, failures in `GameJob.error`; clients poll `GET /jobs/{id}`.

### Processing the library

An admin starts the workers on the game files in the library with `POST /library/jobs`. The queued `library` job is claimed by a worker, which scans `VDI_LIBRARY_PATH`: every directory in it is a game, registered with the library path as its `library_path` and queued for a `config` and a `metadata` job. A game an admin already created keeps its PEGI rating, only its library path is refreshed. Games found in the library get the strictest PEGI rating (`18`) until an admin sets the real one.

The worker needs the library mounted at `VDI_LIBRARY_PATH` (mount the share or a PVC with the game files into the worker pod).

## Security model

- OIDC/JWKS verification; empty `VDI_OIDC_ISSUER` = dev-only mode that accepts any bearer token. Do not ship with it empty.
- First login provisions the user as `gamer` (age `VDI_DEFAULT_AGE`); identities (subject, user name or email) listed in `VDI_ADMIN_SUBJECTS` are provisioned — or promoted — to `admin`
- CORS allows only `VDI_FRONTEND_ORIGIN`
- RBAC middleware (anonymous/gamer/admin)
- Agent token auth (`Bearer VDI_AGENT_TOKEN`) on `/agent/vm/*` endpoints — agents never talk to Proxmox directly
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
- Empty `VDI_OIDC_ISSUER` disables real OIDC verification (dev-only). Do not ship with it empty.
- The OIDC provider must serve a discovery document at `{VDI_OIDC_ISSUER}/.well-known/openid-configuration`; login and token verification fail without it.
- When running the server in Kubernetes with `VDI_KUBERNETES_MODE=incluster`, the pod's ServiceAccount needs RBAC to list/patch Talos nodes.
- Every agent–VM interaction on the Proxmox side goes through the server (`/agent/vm/*`); agents must not call Proxmox directly.
