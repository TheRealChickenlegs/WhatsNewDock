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
  the Docker Engine API (no shell commands), with live progress and automatic
  rollback if the replacement fails to start.
- **Agent or agentless** — deploy an agent on every server, add a remote Docker
  API endpoint directly, or both; view *all* servers, stacks and containers in
  one UI.
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
                         │ HTTPS (bearer)    │ HTTP (internal network)
              ┌──────────┴────────┐    ┌─────┴──────────────┐
              │     Agent          │    │  socket-proxy      │
              │  (per remote host) │    │  (restricted API)  │
              └───────────────────┘    └─────────▲──────────┘
                                                 │ read-only socket mount
                                          ┌──────┴───────┐
                                          │ Docker host  │
                                          └──────────────┘
```

- **Server mode** (`WND_MODE=server`, the default) serves the UI/API, runs the
  update-check scheduler, and *optionally* monitors the local Docker host —
  through a **socket-proxy**, never the raw socket.
- **Agent mode** (`WND_MODE=agent`) snapshots a Docker host and reports it to a
  server, and executes update commands queued by the server. Each agent host
  runs its own socket-proxy.
- **Direct endpoints** (optional, server mode) let the server talk to a remote
  Docker Engine API itself, so a host needs no agent binary at all.
- A single binary (`whatsnewdock`) runs in either mode.

Everything is stored in an embedded **SQLite** database — no external
databases are required, so the app deploys in total isolation.

---

## Quick start

```bash
docker compose up -d
```

This brings up two containers: **whatsnewdock** and a **socket-proxy** that
holds the Docker socket read-only and exposes only the restricted API the app
needs. Then open http://localhost:8080 and sign in with the admin credentials
you set via `WND_INITIAL_ADMIN_USER` / `WND_INITIAL_ADMIN_PASSWORD` (see
`docker-compose.yml`). If you don't set a password, a random one is generated
and printed to the container logs on first run.

### Deploying agents on other hosts

1. Open the **Servers** page (sidebar) and click **Add server** (admin only).
   Enter a name and create it — the UI shows the **agent token once**, along
   with a ready-made run command. Copy the token.
2. On that host, run a **socket-proxy** (so the agent never touches the raw
   socket — same settings as the server compose):

```bash
docker run -d --name whatsnewdock-socket-proxy --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e CONTAINERS=1 -e IMAGES=1 -e INFO=1 -e PING=1 -e VERSION=1 -e POST=1 \
  -e EXEC=0 -e NETWORKS=0 -e VOLUMES=0 -e SWARM=0 -e SECRETS=0 -e BUILD=0 \
  lscr.io/linuxserver/socket-proxy:latest
```

3. Run the agent, pointed at the proxy:

```bash
docker run -d --name whatsnewdock-agent --restart unless-stopped \
  --link whatsnewdock-socket-proxy:socket-proxy \
  -e WND_MODE=agent \
  -e WND_AGENT_SERVER_URL=https://whatsnewdock.example.com \
  -e WND_AGENT_TOKEN=wnd_xxxx \
  -e WND_AGENT_NAME=homelab-nas \
  -e WND_DOCKER_HOST=tcp://socket-proxy:2375 \
  ghcr.io/therealchickenlegs/whatsnewdock:latest
```

Or use the commented compose example at the bottom of `docker-compose.yml`,
which is preferable (both services share a compose network).

### Adding a host without an agent (direct endpoint)

If the Docker API of another host is reachable from the WhatsNewDock container
(same LAN, VPN, or a shared Docker network), you can skip the agent entirely:

1. **Servers** → **Add server** → **Direct endpoint**.
2. Enter the Engine API URL, e.g. `tcp://10.0.0.5:2376`, and the **mounted file
   paths** of the CA (plus client certificate and key for mutual TLS).
3. Click **Test connection** — the server pings the daemon and shows its
   version before anything is saved.

Direct endpoints are polled every 30 s and updated in place like local
containers; updates run from this server, never through the agent queue.

Two rules are enforced when an endpoint is saved, and both exist to protect the
remote host:

- **No plaintext over the network.** `tcp://` without TLS is accepted only for
  `127.0.0.1` / `localhost` / `::1` and unix sockets — that is how the bundled
  `socket-proxy` sidecar is reached. Any other host must use TLS.
- **No PEM in the database.** Certificates are referenced by file path inside
  the WhatsNewDock container and mounted read-only; the paths are never sent
  back to the browser.

The compose and Quadlet deployments are unchanged by this: an agent-only
deployment keeps using the bearer-token queue exactly as before, and direct
endpoints are opt-in per host.

### Deploying with Podman Quadlet

The same image and environment variables also run as native systemd services
via Quadlet. Ready-made units for the server, socket-proxy and agent — with
rootful and rootless instructions — live in
[`deploy/quadlet/`](deploy/quadlet/README.md):

```bash
sudo cp deploy/quadlet/*.network deploy/quadlet/*.volume deploy/quadlet/*.container \
  /etc/containers/systemd/
sudo systemctl daemon-reload
sudo systemctl start whatsnewdock
```

See [`deploy/quadlet/README.md`](deploy/quadlet/README.md) for secrets files,
rootless setup, SELinux and volume-ownership notes, and auto-updates.

### Updating WhatsNewDock itself

The app watches for its own newer releases and, when it finds one, shows a card
at the base of the sidebar: *new version detected, v0.1.1 → v0.1.2*, with the
release notes and an **Update now** button. The check is on by default and runs
every 24 hours; both can be changed in **Settings → WhatsNewDock self-update**
(or with `WND_SELF_UPDATE` / `WND_SELF_UPDATE_INTERVAL`).

A container cannot recreate itself — the stop/rename/create/start sequence kills
the process driving it halfway through — so the work is handed to a short-lived
**helper container** started from the image that is already on the host. The
helper replaces this container with the new image and exits; the app goes down
for a few seconds and comes back on the new version, with the same name, ports,
volumes and settings. The page polls until the new version answers and then
reloads itself.

There are two things it can detect, and the card says which:

- **A newer release** (`:0.1.1` → `:v0.1.2`) — the version pair is shown, and the
  update deploys the release tag. This pins the container to that release, so
  update the tag in your compose file too: a later `docker compose up -d` would
  otherwise put the old version back.
- **A rebuilt tag** (`:latest` has moved) — a moving tag keeps the same version
  string forever, so this compares the digest you are running against the digest
  the registry now serves for the same reference, and the update re-pulls **that
  same tag**. It can never move you backwards.

Things worth knowing:

- The check only reads GitHub's releases for this repository. Nothing is
  downloaded until you press Update.
- The update needs write access to containers and images, which the bundled
  socket proxy already grants (`POST=1`). It needs no new permission.
- A locally built image has no registry digest to compare, so it is never
  offered an update — the card only appears for images pulled from a registry.
- A build described as `v0.1.1-3-gabc1234` (main, three commits past that tag)
  counts as *containing* v0.1.1, so it is never offered the older tag as an
  "update".
- If the app cannot identify its own container, or has no Docker access at all,
  the card explains to update from the host instead:
  `docker compose pull && docker compose up -d`.
- Set `WND_SELF_UPDATE_IMAGE` to pin exactly what a self-update deploys, if the
  tag in the running container is not the one you want.

### Docker Swarm (read-only by default)

Swarm hosts are monitored out of the box. Task containers carry the
`com.docker.swarm.*` labels, so WhatsNewDock groups them the way you think about
them — **stack → service → task** — and shows how many tasks of each service are
running, without needing any additional API permission.

**Updating a service is opt-in.** A swarm task belongs to a service, so it is
never recreated directly: the orchestrator owns it, and recreating one would race
the scheduler and produce a container wearing another task's labels. Instead the
update is handed to swarm, which rolls every replica. That needs the services
API, which the bundled socket proxy denies by default:

```yaml
  socket-proxy:
    environment:
      SERVICES: "1"   # was "0" — enables rolling a service onto a new image
      POST: "1"       # already required for updates
```

Restart the proxy (and WhatsNewDock) and a manager node's update button starts
rolling services. The UI says so on the server page while it is still off. With
`SERVICES=0` nothing changes: task containers are listed and never touched.

What the update does:

1. pulls the new image — a failed pull leaves the service alone;
2. calls `ServiceUpdate` with the new image, re-querying the registry so a
   floating tag picks up its new digest;
3. when the image reference has not changed at all (a plain `:latest`), bumps
   the force counter, because otherwise swarm sees an identical spec and rolls
   nothing out;
4. waits for the orchestrator to report the rollout **completed** with every
   desired task running, and only then reports success. A rollout that never
   settles is undone — server-side rollback to the previous spec — and reported
   as a failure, so a crash-looping image is never mistaken for a good update.

Requirements and limits:

- The host must be a **manager**. A worker node can be monitored but not updated;
  the UI says so rather than failing halfway.
- Granting `SERVICES` widens what the proxy exposes — service definitions include
  environment variables, secret *names* and registry configuration. That is why
  it is off by default. See `docs/SECURITY.md`.
- Agents must be at least this version. An older agent does not recognise swarm
  labels and would fall back to recreating a task container, so update the agent
  on any swarm host before using the feature.

### Updating Quadlet-managed containers

Containers started from a Quadlet unit carry the `PODMAN_SYSTEMD_UNIT` label, so
WhatsNewDock lists them with a badge showing the owning unit. Their updates work
differently from every other container, because the rename-and-recreate dance
cannot work for them: stopping such a container makes its unit tear the
container down, so by the time a recreate tried to rename the old container it
would already be gone (the `failed to rename old container` error), and a
container we created ourselves would collide with systemd's own respawn.

Instead, one-click update **hands the recreate to systemd**:

1. the new image is pulled (a failed pull leaves the running container alone);
2. the container is stopped, which is what makes the unit's `ExecStop` remove it
   and its `Restart=` policy start a fresh one from the unit definition — now on
   the newly pulled tag;
3. WhatsNewDock polls for that container to come back under the same name, with
   a **new** container id, in the `running` state, and only reports success then.
   A container that reappears but never reaches running (a crash-loop on a broken
   image) is reported as a failure, never as a successful update. So is a
   container that comes back on the *same* image, which means the unit's `Image=`
   still points at the old tag.

Two requirements follow from this:

- **The unit needs a `Restart=` policy** (`on-failure` or `always`), otherwise
  nothing brings the container back. Add it to the unit's `[Service]` section.
- **The container must be running.** A stopped Quadlet container does not exist —
  its unit is inactive — and nothing in the Docker Engine API can start a unit.
  WhatsNewDock refuses this case immediately with a `systemctl start <unit>`
  hint rather than waiting out a timeout.

If you would rather not have the app touch them at all, `AutoUpdate=registry` on
the unit plus `podman auto-update` remains a perfectly good alternative — the
app will still track and show the available updates.

Everything else — plain Docker containers, compose projects, and Podman
containers started by hand — keeps the ordinary recreate flow, and any failure
during it puts the original container back, running, under its own name.

Containers that Podman created from `podman kube play` are grouped by their pod
name, but only when no swarm or compose project label is present.

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
| `WND_DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker host URI. The bundled compose uses `tcp://socket-proxy:2375`; also accepts a TLS-protected `tcp://` daemon. |
| `WND_DOCKER_TLS` | `false` | Enable TLS for TCP hosts |
| `WND_DOCKER_TLS_CA` / `_CERT` / `_KEY` | *(empty)* | TLS material for TCP hosts |
| `WND_DOCKER_ENABLE_RECREATE` | `true` | Allow one-click container updates (set `false` for monitoring-only deployments) |

### Update checking

| Variable | Default | Description |
| --- | --- | --- |
| `WND_UPDATE_INTERVAL` | `6h` | How often update checks run |
| `WND_CHANGELOG_COUNT` | `5` | How many past release changelogs to surface |
| `WND_GITHUB_TOKEN` | *(empty)* | GitHub PAT (raises API rate limits) |
| `WND_GITLAB_TOKEN` | *(empty)* | GitLab token for private/self-hosted repos |
| `WND_INCLUDE_PRERELEASES` | `false` | Offer pre-releases as updates |
| `WND_IGNORE_IMAGES` | *(empty)* | Comma-separated image globs never offered for update |
| `WND_SELF_UPDATE` | `true` | Check for newer WhatsNewDock releases |
| `WND_SELF_UPDATE_INTERVAL` | `24h` | How often to check (minimum 1h) |
| `WND_SELF_UPDATE_REPO` | `TheRealChickenlegs/WhatsNewDock` | Repository whose releases are followed |
| `WND_SELF_UPDATE_IMAGE` | *(empty)* | Pin the image a self-update deploys, overriding the running container's tag |

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
- **Docker socket is never mounted into the app.** The bundled compose runs a
  [linuxserver.io socket-proxy](https://docs.linuxserver.io/images/docker-socket-proxy/)
  on an internal network and exposes only `containers`, `images`, `info` and
  version negotiation over `WND_DOCKER_HOST=tcp://socket-proxy:2375`. `exec`,
  `volumes`, `swarm`, `secrets`, `build` and the rest stay denied, and the
  proxy's port is never published. Set `POST=0` +
  `WND_DOCKER_ENABLE_RECREATE=false` for a fully read-only deployment.
- **Agent auth** — agents authenticate with per-server bearer tokens (stored
  hashed, never plaintext).
- **Direct endpoints are TLS-only** — a remote Engine API is refused unless TLS
  material is configured, and the certificate paths (never their contents) are
  validated at save time. `PODMAN_SYSTEMD_UNIT` containers are never recreated
  behind systemd's back.
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

### Checking the responsive layout

The UI is designed for phones, tablets and desktops. `web/scripts/responsive-audit.mjs`
drives a real browser through every page at seven viewport widths (320 px up to
1440 px) and fails if the layout regresses — a sidebar that eats the screen, a
content column squeezed to nothing, anything spilling out of its box, a
container list that scrolls sideways instead of becoming cards, changelog tables
escaping their wrapper, undersized tap targets, or a dirty browser console.

Playwright is not a project dependency; install it on demand:

```bash
cd web
npm i -D playwright && npx playwright install chromium
npm run build && (cd .. && go run ./cmd/whatsnewdock)   # or: npm run dev
node scripts/responsive-audit.mjs                       # BASE=http://127.0.0.1:8080 to target a server
```

Screenshots of every page/viewport land in `/tmp/wnd-ui/shots`.

Note that the server sends a strict CSP (`default-src 'self'`). The only
third-party origins it permits are the webfont ones — `fonts.googleapis.com`
for the stylesheet and `fonts.gstatic.com` for the files — so Inter and
JetBrains Mono load, and nothing else off-site can. To remove even that, self-host
the woff2 files under `web/public/` and replace the `<link>` tags in
`web/index.html` with `@font-face` rules.

### Testing the update flow against a real runtime

Most of the recreate logic is covered by unit tests against a fake daemon, but
the container round-trip can only really be proven against a live engine. Those
tests are skipped unless a Docker-compatible socket is provided:

```bash
podman system service --time=0 unix:///tmp/wnd-podman.sock &
WND_PODMAN_TEST_HOST=unix:///tmp/wnd-podman.sock go test ./internal/dockerx/ -run Live -v
```

They create throwaway `wnd-live-*` containers from `alpine:3.20`/`:3.21`, then
check the ordinary recreate, the refusal of a stopped Quadlet container, and the
systemd respawn path (with a stand-in for the unit). This is how the Podman
`MemorySwappiness` round-trip problem was found: Podman reports `0` for a
container that never set it, and handing that back makes crun refuse to start the
replacement on a cgroup v2 host.

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
only relevant if you bypass the bundled socket-proxy and mount the raw socket
directly. The container user isn't in the host's `docker` group (the socket is
owned by `root:docker`, mode `0660`). Grant it with `group_add`:

```yaml
    group_add:
      - "999"   # getent group docker | cut -d: -f3
```

Using the bundled socket-proxy avoids this entirely.

**Docker API errors mentioning `403 Forbidden`** — the socket-proxy is denying
an endpoint the app needs. Check the `socket-proxy` container logs for the
denied path, then enable the matching section in its environment (see
`docs/SECURITY.md` for the list). A monitoring-only deployment with `POST: "0"`
will reject updates by design.

**Updates fail with `POST` denied** — the proxy is running with `POST: "0"`.
Either set `POST: "1"` on `socket-proxy`, or set
`WND_DOCKER_ENABLE_RECREATE: "false"` on the app to run read-only.

---

## License

MIT — see [LICENSE](LICENSE).
