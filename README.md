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

- `internal/server` — OIDC login (`/auth/login`, `/auth/callback`), JWT verification (JWKS), first-login user provisioning and admin-subject promotion, CORS, RBAC middleware, REST API (`/games`, `/games/{id}/jobs`, `/library/jobs`, `/library/paths`, `DELETE /library`, `/jobs/{id}`, `/sessions`, `/sessions/{id}/override`, `/users`, `/users/{id}`, `/config`, `/health`, `/me`, `/agent/vm/{vmid}/start|stop|config`), PEGI age gating, agent token auth
- `internal/proxmox` — VM list/get/clone/start/stop/delete; token (PVEAPIToken) or username/password (ticket+CSRF) auth
- `internal/k8s` — Talos node cordon/drain/delete; in-cluster service account or kubeconfig auth
- `internal/backup` — periodic backup tick + VM deletion after `last_user_session + delay`
- `internal/agent` — register, Kyber/Apollo streaming, Ludusavi gamesave copy, SMB mount (`nosuid,noexec`), idle shutdown, Loki logging, VM ops via server only (never direct Proxmox)
- `internal/firewall` — ephemeral co-op pinholes with TTL
- `internal/worker` — PostgreSQL-backed job queue: claims pending game jobs (config generation, metadata retrieval, library scan) and writes results back to `Game`
- `internal/metadata` — remote metadata providers (RAWG, Steam, PCGamingWiki, FitGirl through browserless, YouTube, custom HTTP) and the aggregator that cross-references and merges them

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
- `VDI_LIBRARY_PATH` — legacy fallback directory scanned by `library` jobs when no paths are configured in the UI (DB-configured paths take precedence)
- `VDI_METADATA_URL` — custom HTTP metadata source: `GET {VDI_METADATA_URL}/games?title={title}` returns a metadata JSON body (empty = disabled; checked first)
- `VDI_RAWG_API_KEY` — rawg.io API key (empty = RAWG disabled)
- `VDI_NEXUSMODS_API_KEY` — Nexus Mods personal API key (empty = NexusMods disabled)
- `VDI_BROWSERLESS_URL`, `VDI_BROWSERLESS_TOKEN` — remote browserless instance used to scrape FitGirl (empty URL = FitGirl disabled)
- `VDI_FITGIRL_SEARCH_URL` — FitGirl search endpoint; `{query}` is replaced with the URL-escaped title (default `https://fitgirl-repacks.site/?s={query}`)
- `VDI_YOUTUBE_API_KEY` — YouTube Data API key used for live streams and video replays (empty = YouTube disabled)
- `VDI_IGDB_CLIENT_ID`, `VDI_IGDB_CLIENT_SECRET` — Twitch application client id/secret used by IGDB (empty = IGDB disabled)
- `VDI_TWITCH_CLIENT_ID`, `VDI_TWITCH_CLIENT_SECRET` — Twitch application client id/secret used for live streams and video replays (empty = Twitch disabled)

Steam and PCGamingWiki are queried by default; they need no credentials. IGDB and Twitch share the same Twitch application credentials.

Additional sources from the plan are intentionally not implemented: VGDB, Kick, IGG-Games, Kaos, Dodi, Online-Fix, SensCritique and Kotaku have no stable public game-metadata API, so they would require site-specific scraping credentials/selectors. Steam reviews already cover the reviews field.

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

### Live development with Skaffold

Connect to a cluster or set the current context, replace `REPLACE_OWNER` in `skaffold.yaml` and `deploy/k8s/*.yaml` with your registry namespace, then:

```
skaffold dev        # watch source, rebuild server/agent/worker images, redeploy, port-forward the API
skaffold run        # one-shot build + deploy
```

`skaffold dev` forwards `localhost:8080` to the `vdi-server` Service. Go changes rebuild and restart the affected pod. The web frontend is not part of `deploy/k8s` (there is no frontend Deployment/Service and its image is a static `scratch` image), so it is not live-synced here.

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
- `Game` — title, metadata JSON, `library_path`, `category` (`repacks` / `portables` / `isos` / `setups`), embedded `VMConfig` (`cpu_cores`, `ram_size` MB, `disk_size` GB), `pegi_rating`
- `LibraryPath` — admin-configured game directory: `category` + `path`
- `Session` — user/game/vm, `backups`, `last_user_session`, per-session VM config overrides
- `GameJob` — queued work on a game: `kind` (`config` / `metadata` / `library`), `status` (`pending` / `processing` / `done` / `failed`), `payload`, `result`, `error`, `attempts`. The queue lives in PostgreSQL, so any number of workers can share it.

All tables get `created_at` and `updated_at` (soft-deletable tables also `deleted_at`); migrations via `db.Migrate` (GORM AutoMigrate).

## API endpoints

The API is served at the `api.domain.tld` subdomain (see `deploy/k8s/gateway.yaml`), not at a `domain.tld/api` path prefix. The frontend derives the API base URL at runtime from the domain it is served from (`api.<host>`), so one image works on any domain.

- `GET /auth/login` — redirects to the OIDC provider (dev mode: straight back to the frontend `/login` route with a token); `GET /auth/callback` completes the flow and returns the ID token to the frontend
- `GET/POST /games` — `PUT /games/{id}` (admin) updates `library_path`
- `PUT /users/{id}` (admin) — change a user's `role` (`anonymous` / `gamer` / `admin`)
- `POST /games/{id}/jobs` (admin) — enqueue a game job (`kind`: `config` or `metadata`, optional `payload`); returns `202` with the queued job
- `POST /library/jobs` (admin) — start the workers on the game files in the library; returns `202` with the queued `library` job
- `GET/POST /library/paths` (admin) — list/add game directories, each categorized as `repacks`, `portables`, `isos` or `setups`
- `DELETE /library/paths/{id}` (admin) — remove a configured game directory
- `DELETE /library` (admin) — reset the library: delete all games and queued jobs, keeping the configured paths
- `GET /games/{id}/jobs` (admin) — list a game's jobs
- `GET /jobs/{id}` — poll a job's `status`, `result`, `error`
- `GET /me` — current user id/username/role/age
- `GET/POST/DELETE /sessions`
- `POST /sessions/{id}/override` — VM config override
- `GET /users` (admin)
- `GET /config`
- `GET /health`
- `POST /admin/vms` (admin) — clone a Proxmox template with `template_id`, `name`, `cpu_cores`, `ram_size` (MB), `disk_size` (GB) and `vgpu` (`std`, `cirrus`, `vmware`, `qxl`, `virtio`, `none`); returns `201` with the new `vmid`
- `POST /agent/vm/{vmid}/start`, `POST /agent/vm/{vmid}/stop`, `GET /agent/vm/{vmid}/config` (agent token auth)

## Game job queue

Games are configured and enriched asynchronously through a PostgreSQL-backed queue, so slow or external work never blocks the API:

1. An admin enqueues a job: `POST /games/{id}/jobs` with `kind` = `config` or `metadata` (and an optional `payload`).
2. One or more `cmd/worker` processes claim pending jobs atomically (`status: pending` → `processing`) directly from PostgreSQL, so workers can be scaled horizontally.
3. `config` jobs generate the game's `VMConfig` — from the job `payload` when provided, otherwise derived from the PEGI rating — and write it back to the `Game` row.
4. `metadata` jobs query every configured metadata provider in priority order, cross-reference the results (first non-empty value wins, lists and sources are merged) and write the merged JSON to `Game.metadata`. A PEGI rating from the metadata also updates `Game.pegi_rating`; when a source only carries ESRB, its PEGI equivalent is used.
5. Results land in `GameJob.result`, failures in `GameJob.error`; clients poll `GET /jobs/{id}`.

### Metadata sources

The worker merges these sources in order (custom HTTP first, then RAWG, NexusMods, IGDB, Steam, PCGamingWiki, FitGirl, Twitch, YouTube). A failing source does not fail the job as long as another source succeeds; every stored record lists its `sources` with URLs.

| Source | Enabled by | Fields |
| --- | --- | --- |
| Custom HTTP | `VDI_METADATA_URL` | already-aggregated metadata JSON |
| RAWG | `VDI_RAWG_API_KEY` | rating, genres, tags, release date, platforms, Metacritic, developer, publisher, ESRB, video replays |
| NexusMods | `VDI_NEXUSMODS_API_KEY` | genres, mod page URL |
| IGDB | `VDI_IGDB_CLIENT_ID`, `VDI_IGDB_CLIENT_SECRET` | rating, genres, release date, platforms, developer, publisher, PEGI/ESRB |
| Steam | always (public API) | genres, release date, platforms, Metacritic, PC specs, developer, publisher, PEGI/ESRB, video replays, reviews |
| PCGamingWiki | always (public API) | genres, tags, release date, platforms, Metacritic, PC specs, developer, publisher, PEGI/ESRB |
| FitGirl | `VDI_BROWSERLESS_URL` | genres/tags, developer, PC specs, PEGI/ESRB |
| Twitch | `VDI_TWITCH_CLIENT_ID`, `VDI_TWITCH_CLIENT_SECRET` | live streams, video replays |
| YouTube | `VDI_YOUTUBE_API_KEY` | live streams, video replays |

Sites from `plan.md` that have no stable public game-metadata API or would need site-specific scraping credentials/selectors (VGDB, Kick, IGG-Games, Kaos, Dodi, Online-Fix, SensCritique, Kotaku) can be plugged in as additional `metadata.Provider` implementations; use the FitGirl browserless provider as the reference for rendered pages.

### Processing the library

Admins configure game directories in the UI (`GET/POST /library/paths`), each categorized as `repacks`, `portables`, `isos` or `setups`. An admin then starts the workers with `POST /library/jobs`. The queued `library` job is claimed by a worker, which scans every configured path: every directory in it is a game, registered with the directory as its `library_path`, the path's category as its `category`, and queued for a `config` and a `metadata` job. A game an admin already created keeps its PEGI rating, only its library path and category are refreshed. Games found in the library get the strictest PEGI rating (`18`) until an admin sets the real one.

When no paths are configured, `library` jobs fall back to `VDI_LIBRARY_PATH`. The worker needs every configured library mounted (mount the shares or PVCs with the game files into the worker pod).

`DELETE /library` (the Reset library button) deletes all games and queued jobs so the admin can rebuild the library from scratch; the configured paths are kept.

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
