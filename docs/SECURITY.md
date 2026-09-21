# Security model

WhatsNewDock was designed with security in mind throughout. This document
describes the threat model and the protections in place.

## Trust boundaries

1. **Web UI ↔ server** — authenticated via local accounts (bcrypt) and/or OIDC,
   with signed session cookies.
2. **Agent ↔ server** — authenticated via per-server bearer tokens over HTTPS
   (or, optionally, TLS with client certificates).
3. **Server/agent ↔ Docker Engine** — over the Docker API (socket or TCP+TLS).
4. **Server ↔ upstream registries/Git hosts** — read-only HTTPS, optionally
   authenticated with a scoped PAT.

## Docker access — the most important consideration

Access to the raw Docker socket is **root-equivalent on that host**: anything
that can talk to it can start privileged containers, mount the host filesystem,
or read every secret on the machine.

WhatsNewDock therefore **does not mount the raw socket**. The bundled
`docker-compose.yml` runs a
[linuxserver.io socket-proxy](https://docs.linuxserver.io/images/docker-socket-proxy/)
that exposes only a restricted subset of the Docker API over HTTP on the
internal compose network. The app talks to the proxy:

```yaml
WND_DOCKER_HOST: tcp://socket-proxy:2375
```

### What the proxy is allowed to do

| Section | Setting | Why |
| --- | --- | --- |
| `CONTAINERS` | `1` | list/inspect containers; recreate on update |
| `INFO` | `1` | daemon version, OS, arch, resources |
| `IMAGES` | `1` | pull the updated image |
| `PING` / `VERSION` | `1` | API version negotiation |
| `POST` | `1` | required for any write (pull/recreate) |
| `SERVICES` | `0` | **opt-in**: swarm service updates. See below |
| everything else | `0` | `EXEC`, `VOLUMES`, `SWARM`, `SECRETS`, `BUILD`, `NETWORKS`, `PLUGINS`, `SYSTEM`, `EVENTS`, `AUTH`, … |

Everything not listed is denied, so a compromise of the app cannot reach
`/exec`, container volumes, swarm services, secrets, or the build API even
though it can recreate containers.

### Monitoring-only (read-only) mode

For the tightest posture, run the proxy with `POST: "0"` and set
`WND_DOCKER_ENABLE_RECREATE: "false"` on the app. The proxy then rejects every
non-`GET` request, so the app can read container state but cannot modify
anything — even if it is fully compromised.

### Rules for the proxy

- **Never publish its port.** `2375` is socket-equivalent for the enabled
  sections; keep it on the internal compose network and reach it by service
  name. The bundled compose deliberately has no `ports:` on `socket-proxy`.
- **Never expose it to a public network** or a shared/untrusted bridge.
- Mount the real socket into the proxy **read-only** (`:ro`).
- Put the proxy on the same Docker network as the app only.

### Agent deployments

Each agent host runs its own socket-proxy with the same settings; the agent
points at `tcp://socket-proxy:2375` instead of mounting the socket. See the
commented example at the bottom of `docker-compose.yml`.

### Direct (agentless) endpoints

A server of kind `direct` makes the WhatsNewDock server itself a Docker API
client of a remote host. That is a deliberate expansion of the trust boundary —
the server now holds credentials that grant container control on that host — so
it is guarded accordingly:

- **TLS is mandatory off-loopback.** Endpoint validation refuses plaintext
  `tcp://` unless the target is `127.0.0.1`, `localhost`, `::1` or a unix
  socket. Mutual TLS (CA + client certificate + key) is supported and
  recommended; a CA alone gives server-authenticated TLS.
- **Credential material stays on disk.** Certificates and keys are stored as
  **file paths mounted into the container**, never as PEM in the database, and
  the API never returns the paths. Validation `stat`s each path at save time so
  a typo fails immediately rather than silently at poll time.
- **Endpoint testing is authenticated.** `POST /api/v1/servers/test` and
  `.../{id}/test` require the `admin` role like every other server mutation, and
  reuse the same validation, so the endpoint cannot be used to probe arbitrary
  hosts from a viewer session.
- **Least privilege still applies.** Point a direct endpoint at a socket-proxy
  with the same restricted API surface (`POST=0` for monitoring-only) rather
  than at an exposed daemon; the server only ever needs `info`, `ping`,
  `containers`, `images` and — when one-click updates are enabled — container
  create/start/stop/rename and image pull.

### Docker Swarm

Swarm **monitoring** needs no new permission: task containers carry the
`com.docker.swarm.*` labels, which the app already stores, so stacks, services
and tasks are listed with the same access the container listing already has.

Swarm **updates** are opt-in and off by default. Granting the proxy
`SERVICES=1` exposes `/services`, which returns full service definitions —
environment variables (including inline secrets), secret and config *names*,
registry credentials configuration, and the mounts each service uses. Anything
that can reach the proxy can therefore read more of your deployment than
container inspection alone reveals. Enable it only if you want the app to roll
services, and keep the proxy on an internal network either way.

With the permission granted, the app still only ever calls `ServiceInspect`,
`ServiceList` and `ServiceUpdate`, and only for a service it has already seen a
task for. It never calls `SwarmInit`, `SwarmJoin`, `NodeUpdate`,
`ServiceRemove` or the secrets and configs APIs. A rollout that does not settle
is rolled back server-side to the previous spec.

Task containers are never recreated directly, by the same reasoning as Quadlet:
the orchestrator owns them, and a container we created would collide with
swarm's own replacement.

### Podman / Quadlet containers

Containers labelled `PODMAN_SYSTEMD_UNIT` belong to a systemd unit. They cannot
be recreated the ordinary way — a stop makes the unit tear the container down, so
renaming the old container fails and a container created by us would collide
with systemd's respawn. Instead the update is handed to systemd: the image is
pulled first, the container is stopped so the unit's `Restart=` policy brings it
back on the new tag, and the result is only reported as successful once a
container with a **new id** is observed `running` under the exact same name. A
crash-looping or unchanged replacement is a reported failure. A stopped Quadlet
container is refused up front, since the Engine API cannot start a unit.

Two details matter for safety here:

- **Exact-name matching.** The container list `name` filter is regex-contains on
  both Docker and Podman, so a lookup for `web` also matches `web-prev-1234` (the
  rollback name this project creates) and `my-web`. Every name lookup is
  confirmed against the full name, so the update can never be "verified" against
  the wrong container.
- **No silent workload outage.** In the ordinary recreate flow, every failure
  after the container is stopped restores it — its name and, if it was running,
  its running state — regardless of its restart policy.

### Alternatives

- **TCP + mutual TLS** — point `WND_DOCKER_HOST` at a TLS-protected daemon and
  set `WND_DOCKER_TLS=1` plus `WND_DOCKER_TLS_CA`/`_CERT`/`_KEY`.
- **Raw socket (not recommended)** — if you must mount
  `/var/run/docker.sock` directly, the container user must be in the host's
  `docker` group (the socket is `root:docker`, mode `0660`):

  ```yaml
  group_add:
    - "999"   # getent group docker | cut -d: -f3
  ```

  Note that `:ro` on a socket mount grants no protection — the API is still
  fully reachable.

**No shell commands are ever executed.** Every operation — listing, inspecting,
pulling and recreating containers — goes through the Docker Engine API client.
The "update" action recreates a container from its existing configuration
(never `docker compose`/`docker exec`), and restores the previous container if
the replacement fails to start.

## Authentication

- **Local accounts** — passwords hashed with bcrypt; roles `admin` (full
  control) and `viewer` (read-only). Update/pin/server/user mutations require
  `admin`.
- **Sessions** — HMAC-SHA256 JWTs in `HttpOnly`, `SameSite=Lax` cookies, with
  `Secure` enabled when behind TLS. The signing secret is auto-generated and
  persisted, or supplied via `WND_AUTH_SESSION_SECRET`.
- **CSRF** — mutating API requests require an `X-Requested-With: whatsnewdock`
  header (unsettable cross-origin), plus `SameSite` cookies as a second layer.
- **OIDC** — state-parameter validation, `id_token` verification via the
  provider's JWKS, and configurable role mapping.

## Agent tokens

Each remote agent has a unique token generated by the server and shown **once**.
Tokens are stored as SHA-256 hashes — never in plaintext. All agent traffic
uses a `Authorization: Bearer` header and should be over HTTPS (terminated at
the server or a reverse proxy).

Direct endpoints have no token: their credential is the TLS client certificate
referenced by `tls_cert`/`tls_key` (see *Direct (agentless) endpoints* above).

## Input & data handling

- HTTP bodies are size-limited and decoded with strict JSON handling.
- SQL is parameterised throughout (no string concatenation) — SQL injection is
  not possible.
- Changelog bodies are **not** sanitised to HTML; they are rendered with
  `react-markdown` (no raw HTML), so malicious release notes cannot inject
  markup or scripts.
- A strict `Content-Security-Policy` is set, and the UI disables framing via
  `X-Frame-Options: DENY`.

## Dependency & supply chain

- Backend dependencies are a small, curated set (Docker SDK, OIDC, JWT, bcrypt,
  semver, pure-Go SQLite).
- CI runs `govulncheck` (known vulnerabilities), `gosec` (static analysis),
  `npm audit`, and a `trivy` scan of every published image.
- Images are built on distroless and run as a **non-root** user with no shell.

### Known non-exploitable advisories

`govulncheck` flags two advisories in `github.com/docker/docker` (Moby) that are
**not reachable from this codebase**, and which have **no upstream fix yet**:

- **GO-2026-4887** — Moby AuthZ plugin bypass with oversized request bodies.
- **GO-2026-4883** — off-by-one in Moby plugin privilege validation.

Both live in the Docker **daemon's** authorization/legacy-plugin subsystems.
WhatsNewDock imports only the Docker Engine API **client** SDK
(`github.com/docker/docker/client`) and never runs, configures or reaches the
daemon's plugin code. The govulncheck traces pass through package-level
`init()` functions because the Moby module bundles client and daemon together.

The CI `govulncheck` step therefore allowlists exactly these two IDs and still
fails on any other reachable vulnerability. Remove them from the allowlist if
the code ever embeds or links the Moby daemon.

## Secrets guidance

- Prefer environment variables or a Docker secret for `WND_INITIAL_ADMIN_PASSWORD`,
  `WND_OIDC_CLIENT_SECRET` and `WND_AUTH_SESSION_SECRET`.
- GitHub/GitLab tokens are optional and only increase API rate limits; use
  least-privilege, fine-grained PATs scoped to read public repositories.
