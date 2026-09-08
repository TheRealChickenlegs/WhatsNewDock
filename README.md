# WhatsNewDock

[![CI](https://github.com/TheRealChickenlegs/WhatsNewDock/actions/workflows/ci.yml/badge.svg)](https://github.com/TheRealChickenlegs/WhatsNewDock/actions/workflows/ci.yml)

A self-hosted **Docker container update monitor** with a focus on *changelogs*.
WhatsNewDock watches every container across your Docker hosts, tells you when a
newer image is available, and — crucially — shows you **what actually changed**
by aggregating release notes from GitHub, GitLab, Gitea/Forgejo and registry
tag listings. A single, sleek web UI gives you a unified view of all servers,
stacks and containers, with one-click image updates.

## Features

- **Update detection** — compares each container's tag against upstream
  releases, and reports how many versions you are behind.
- **Changelog aggregation** — pulls release notes from as many sources as
  possible:
  - GitHub Releases (including `ghcr.io` images and
    `org.opencontainers.image.source` labels)
  - GitLab Releases (`registry.gitlab.com`)
  - Gitea / Forgejo releases (self-hosted)
  - Docker Hub and other registry tag listings (fallback, no notes)
- **History** — browse the last **N** releases (default 5, configurable), so you
  can see the changelog of each skipped version.
- **One-click updates** — recreate a container with the newer image using only
  the Docker Engine API (no shell commands). The old container is kept as a
  stopped rollback backup.
- **Agent architecture** — deploy an agent on every server and view *all*
  servers, stacks and containers in one UI.
- **Unified & hierarchical views** — filter the unified container list by
  server, stack, state, registry, update availability and free-text; or drill
  server → stack → container.
- **Authentication** — local username/password accounts **and** optional OIDC
  (works with PocketID or any OIDC provider), with `admin` / `viewer` roles.
- **Hardened** — memory-safe Go backend, non-root distroless image, signed
  session cookies, CSRF protection, no `docker` CLI / shell usage.

---

## Architecture

```
                 ┌────────────────────────────────────────┐
                 │            WhatsNewDock Server          │
                 │  (web UI + API + auth + update checker) │
                 └───────▲───────────────────▲─────────────┘
                         │ HTTPS (bearer)    │ local Docker socket (optional)
              ┌──────────┴────────┐          │
              │     Agent          │          │
              │  (per remote host) │          │
              └───────────────────┘          │
```

- **Server mode** (`WND_MODE=server`, the default) serves the UI/API, runs the
  update-check scheduler, and *optionally* monitors the local Docker socket.
- **Agent mode** (`WND_MODE=agent`) snapshots a Docker host and reports it to a
  server, and executes update commands queued by the server.
- A single binary (`whatsnewdock`) runs in either mode.

Everything is stored in an embedded **SQLite** database — no external
databases are required, so the app deploys in total isolation.

---

## Quick start

```bash
docker compose up -d
```

Then open http://localhost:8080 and sign in with the admin credentials you set
via `WND_INITIAL_ADMIN_USER` / `WND_INITIAL_ADMIN_PASSWORD` (see
`docker-compose.yml`). If you don't set a password, a random one is generated
and printed to the container logs on first run.

### Deploying agents on other hosts

1. In the UI, go to **Settings → Servers → Add server** (or the **Servers**
   page) and create a server. Copy the one-time agent token.
2. Run the agent on that host:

```bash
docker run -d --name whatsnewdock-agent \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e WND_MODE=agent \
  -e WND_AGENT_SERVER_URL=https://whatsnewdock.example.com \
  -e WND_AGENT_TOKEN=wnd_xxxx \
  -e WND_AGENT_NAME=homelab-nas \
  ghcr.io/therealchickenlegs/whatsnewdock:latest
```

---

## Configuration

All settings can be set via **environment variables** (prefix `WND_`), an
optional **YAML config file** (`--config /path/to/config.yaml` or
`WND_CONFIG`), or command-line flags (`--mode`, `--host`, `--port`,
`--data-dir`). Precedence: flags > environment > config file > defaults.

| Variable | Default | Description |
| --- | --- | --- |
| `WND_MODE` | `server` | `server` or `agent` |
| `WND_HOST` / `WND_PORT` | `0.0.0.0` / `8080` | Listen address |
| `WND_DATA_DIR` | `/data` | SQLite database directory |
| `WND_BASE_URL` | *(empty)* | External URL (for OIDC redirects, secure cookies) |
| `WND_TRUSTED_PROXIES` | *(empty)* | Comma-separated CIDRs trusted for `X-Forwarded-*` |
| `WND_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

### Docker

| Variable | Default | Description |
| --- | --- | --- |
| `WND_DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker host URI (also `tcp://` or a socket-proxy URL) |
| `WND_DOCKER_TLS` | `false` | Enable TLS for TCP hosts |
| `WND_DOCKER_TLS_CA` / `_CERT` / `_KEY` | *(empty)* | TLS material for TCP hosts |
| `WND_DOCKER_ENABLE_RECREATE` | `true` | Allow one-click container updates |

### Update checking

| Variable | Default | Description |
| --- | --- | --- |
| `WND_UPDATE_INTERVAL` | `6h` | How often update checks run |
| `WND_CHANGELOG_COUNT` | `5` | How many past release changelogs to surface |
| `WND_GITHUB_TOKEN` | *(empty)* | GitHub PAT (raises API rate limits) |
| `WND_GITLAB_TOKEN` | *(empty)* | GitLab token for private/self-hosted repos |
| `WND_INCLUDE_PRERELEASES` | `false` | Offer pre-releases as updates |
| `WND_IGNORE_IMAGES` | *(empty)* | Comma-separated image globs never offered for update |

### Authentication

| Variable | Default | Description |
| --- | --- | --- |
| `WND_AUTH_MODE` | `local` | `local`, `oidc` or `both` |
| `WND_INITIAL_ADMIN_USER` | `admin` | First-run admin username |
| `WND_INITIAL_ADMIN_PASSWORD` | *(random)* | First-run admin password |
| `WND_AUTH_SESSION_SECRET` | *(auto)* | Session signing secret |
| `WND_AUTH_SESSION_TTL` | `24h` | Web session lifetime |

### OIDC (e.g. PocketID)

| Variable | Description |
| --- | --- |
| `WND_OIDC_ISSUER` | OIDC issuer URL (e.g. `https://pocketid.example.com`) |
| `WND_OIDC_CLIENT_ID` | Client ID |
| `WND_OIDC_CLIENT_SECRET` | Client secret |
| `WND_OIDC_REDIRECT_URL` | Callback URL (defaults to `<BASE_URL>/api/auth/oidc/callback`) |
| `WND_OIDC_SCOPES` | Comma-separated (default `openid,profile,email`) |
| `WND_OIDC_USERNAME_CLAIM` | ID-token claim for the display name (default `preferred_username`) |
| `WND_OIDC_DEFAULT_ROLE` | `admin` or `viewer` (default `viewer`) |

### Agent mode

| Variable | Description |
| --- | --- |
| `WND_AGENT_SERVER_URL` | URL of the central server |
| `WND_AGENT_TOKEN` | Token shown once when adding the server in the UI |
| `WND_AGENT_NAME` | Optional display name |
| `WND_AGENT_INTERVAL` | Report interval (default `30s`) |

---

## Changelog sources — how resolution works

For each container, WhatsNewDock determines a changelog source in this order:

1. **Registry inference** — `ghcr.io/owner/repo` → GitHub `owner/repo`;
   `registry.gitlab.com/...` → GitLab project.
2. **Configured hints** — custom registries/repo maps in the config file.
3. **Image labels** — the `org.opencontainers.image.source` label (set by most
   well-behaved images) is parsed to detect GitHub/GitLab/Gitea.
4. **Registry fallback** — Docker Hub / GHCR / GCR / ECR / Quay tag listings
   (detects *that* a new tag exists, without release notes).

For floating tags (`latest`, `stable`, `lts`, …), WhatsNewDock compares the
running image's digest against the registry's current digest, so it only
reports an update when the tag actually moved.

### Custom registry/repo mapping

In a config file:

```yaml
updates:
  registries:
    registry.example.com:
      source: gitea
      base_url: https://registry.example.com
      repo_map:
        "team/app": "team/app"   # image repo -> upstream owner/repo
```

---

## Security

See [`docs/SECURITY.md`](docs/SECURITY.md) for the full model. Highlights:

- **No shell commands** — all Docker operations use the Engine API client.
- **Non-root, distroless** runtime image with a read-only-by-default container.
- **Docker socket exposure** — mounting the socket gives root-equivalent
  access to that host. For untrusted/hardened deployments, point
  `WND_DOCKER_HOST` at a **docker-socket-proxy** (e.g.
  [`Tecnativa/docker-socket-proxy`](https://github.com/Tecnativa/docker-socket-proxy))
  exposing only the minimal API surface, or use TCP + TLS with client certs.
- **Agent auth** — agents authenticate with per-server bearer tokens (stored
  hashed, never plaintext).
- **Web auth** — bcrypt password hashing, HttpOnly `SameSite` session cookies,
  CSRF header checks on mutations, OIDC state validation.
- **CI security gates** — `gosec`, `govulncheck`, `npm audit` and a Trivy scan
  of published images run on every push.

---

## Reverse proxy

WhatsNewDock serves plain HTTP and is designed to sit behind a reverse proxy
(nginx, Traefik, Caddy, …). A full nginx example is in
[`deploy/nginx.conf.example`](deploy/nginx.conf.example).

The essentials:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

Then set `WND_BASE_URL=https://whatsnewdock.example.com` and (recommended)
`WND_TRUSTED_PROXIES` to your proxy's CIDR so forwarded headers are honoured.

---

## Development

Requirements: **Go 1.27+** and **Node 22+**.

```bash
# Full build (frontend + backend, embeds the UI)
make build

# Run tests (Go + frontend typecheck)
make test

# Vet + format check
make lint

# Run the server with a local data dir
WND_DATA_DIR=./data go run ./cmd/whatsnewdock

# Run the frontend dev server (proxies /api to :8080)
cd web && npm run dev
```

The backend serves the UI from embedded assets; `make build` compiles the
frontend into `internal/webui/dist` before building the Go binary.

---

## CI / CD

- **`.github/workflows/ci.yml`** — on every push/PR: Go format/vet/tests,
  `gosec` static analysis, `govulncheck` vulnerability scan, and frontend
  typecheck/build/`npm audit`.
- **`.github/workflows/build-publish.yml`** — on tags and `main`: builds
  multi-arch (`linux/amd64`, `linux/arm64`) images, pushes them to **GHCR**, and
  runs a **Trivy** scan.

> The published image is
> `ghcr.io/therealchickenlegs/whatsnewdock` (GHCR lowercases the repository
> name). `docker-compose.yml` and the agent run command already reference it.

The CI status badge works automatically — no setup required.

---

## Troubleshooting

**`unable to open database file (14)`** — the container runs as a non-root
user (uid 65532), and the `/data` directory isn't writable by it. If you bind
mount a host path, make it writable:

```bash
sudo chown -R 65532:65532 /path/to/whatsnewdock-data
```

or run the container as your host user with `user: "${UID}:${GID}"` in the
compose service. The named volume in `docker-compose.yml` already handles this
automatically.

**`permission denied while trying to connect to the Docker daemon socket`** —
the container user isn't in the host's `docker` group (the socket is owned by
`root:docker`, mode `0660`). Grant it with `group_add`:

```yaml
    group_add:
      - "999"   # getent group docker | cut -d: -f3
```

or point `WND_DOCKER_HOST` at a docker-socket-proxy, which sidesteps the
socket permission entirely and is the hardened option.

---

## License

MIT — see [LICENSE](LICENSE).
