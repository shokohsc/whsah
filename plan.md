# Self-Hosted Gaming VDI Service — Plan

Self-hosted VDI service oriented toward gaming VMs. Private/internal only; not exposed to the public internet.

## Tech Stack
- Backend: Golang (server/agent binary)
- Frontend: Vue.js + Vite, Bulma CSS
- Database: PostgreSQL (ORM library TBD)
- Cache & messaging: PostgreSQL (no Valkey/NATS)
- Streaming: Kyber (primary), Apollo + Moonlight Web (fallback)
- Gamesaves: Ludusavi
- Auth: OIDC
- Notifications: SMTP client
- Infrastructure: Kubernetes client

## Architecture
- Golang server/agent binary + Vue.js frontend.
- PostgreSQL database.
- Event-based.
- Proxmox cluster hosts both Talos worker VMs and gaming VMs.
- The server polls the Proxmox API (vGPU availability) and manages Talos nodes (cordon, drain, delete) via the Kubernetes client.

## Frontend
- Two themes, light and dark; defaults to the system theme.
- Unauthenticated users see games at the lowest PEGI rating, sorted by most recent release date.
- Logged-in users see games whose PEGI rating is within their age.
- Users search for games within their age.

## Roles & Permissions
- `anonymous` (default): browse games at the lowest PEGI rating.
- `gamer`: create game sessions with the related age rating.
- `admin`: manage games, sessions, and users.
- `admin`: notify users.

## Data Model
- All main tables get `created_at`, `updated_at`, and `deleted_at` timestamp fields.
- `games`: title, metadata, `library_path`, vm config (TBD).
- `sessions`: vm, game, user, backups, ...
- `users`: age, username, email, role, ...

## Game Metadata
- Cached from: RAWG, VGDB, IGDB, PCGamingWiki, NexusMods, Twitch, Kick, YouTube, Steam, FitGirl, IGG-Games, Kaos, Dodi, Online-Fix, reviews, SensCritique, Kotaku.
- Fields: age rating (PEGI; ESRB fallback), rating, genre, tags, release date, PC specs, platform, Metacritic score, SensCritique, developer, publisher, live streams, video replays, reviews.

## VM Lifecycle
- VM is created from a VM clone; edits CPU, RAM, vGPU, disk size (TBD).
- VM sessions are backed up every X time.
- VM is deleted after X time once backed up.

## Agent
- Starts as a system service.
- Starts streaming backend (Kyber; Apollo + Moonlight Web as fallback).
- Saves/loads user gamesave (Ludusavi, copy).
- Mounts SMB share to game `library_path` (read/exec).
- Registers VM to server.
- Replaces background with a game-related image from the wallpaper remote service.
- Shows a templated readme (how to install, play, and update the game) at first connection.
- Shuts down the VM after an inactivity period via user prompt.
- Logs everything to the remote log server in JSON format (Loki gateway).

## Server
- Sends encrypted notifications to authenticated users.
- Polls the Proxmox API for vGPU availability.
- RBAC for Talos node cordon, drain, and delete actions.

## User Flows
- Browse games by metadata (tag, genre, studio, publisher, platform, rating).
- Play games within their age; defaults to the lowest PEGI rating.
- Connect to a VM once authenticated, via browser behind a reverse proxy (Kyber; Apollo + Moonlight Web fallback).
- Override game specs / VM config (CPU, RAM, vGPU, disk size).
- Invite another user for co-op play via link notification (updates firewall rules).
- Delete own backups.

## VM Networking & Hardening
- Default allow-FQDN egress list and deny egress firewall rules; can connect to explicit URLs only.
- Strict ingress IP and port rules; accepts connections from a trusted CIDR.
- Private CA and software installed (Ludusavi, Kyber, Apollo, Firefox, Edge, ...).
- SMB mount: `nosuid,noexec`; copy and verify (hash/signature) before running; per-VM credentials.
- Egress allowlist: pin allowlisted FQDN IPs in the firewall, force DNS through one controlled resolver, deny direct-IP egress, TLS-inspect at the proxy via the private CA.
- Notifications: encrypt to a per-user public key (X25519/age); keys in a secret manager and rotated.
- Co-op firewall rules: ephemeral, default-deny, TTL tied to the session, audited, auto-removed on session end.
- Private CA: offline/HW-backed CA key, short-lived per-VM leaf certs, no wildcard reuse, CRL/OCSP.
- Talos RBAC: namespace-scoped least-privilege service account, actions audited.
- Server ↔ agent: mTLS.
- Logging: allowlisted fields with secret redaction; never log tokens/URLs; secret scanning in CI.

## Open Questions
- ORM library choice.
- VM config fields exposed to `games` and to user overrides.
- Backup interval `X`.
- VM deletion delay `X` after backup.
- Full software list to preinstall in the VM.
