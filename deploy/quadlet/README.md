# Running WhatsNewDock with Podman Quadlet

[Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)
lets Podman run containers as native **systemd** services: each `.container`
file below becomes a systemd unit. This is the Podman-native alternative to the
Docker Compose deployment at the repository root — the same image and the same
environment variables are used either way.

## Files

| File | Purpose |
| --- | --- |
| `whatsnewdock.network` | shared network for the containers |
| `whatsnewdock-data.volume` | persistent `/data` volume (SQLite database) |
| `whatsnewdock-socket-proxy.container` | restricted Docker/Podman API proxy (the only unit that sees the socket) |
| `whatsnewdock.container` | the **server**: web UI, API, update checker, local monitoring |
| `whatsnewdock-agent.container` | the **agent**: deploy this instead of the server on extra hosts |

## Prerequisites

- Podman 4.4+ (`quadlet` support) with the Quadlet generator available.
- The `podman.socket` API socket enabled (the proxy mounts it):
  - rootful: `sudo systemctl enable --now podman.socket`
  - rootless: `systemctl --user enable --now podman.socket`

## Install (rootful Podman)

```bash
sudo cp deploy/quadlet/*.network deploy/quadlet/*.volume deploy/quadlet/*.container \
  /etc/containers/systemd/

# Optional: secrets (see below)
sudo install -m 600 /dev/null /etc/whatsnewdock.env
sudo nano /etc/whatsnewdock.env

sudo systemctl daemon-reload
sudo systemctl start whatsnewdock
```

The `[Install] WantedBy=multi-user.target` in each unit makes Quadlet start it
at boot automatically — no separate `systemctl enable` is required.

Then open `http://<host>:8080`.

## Install (rootless Podman)

Rootless units live in your home directory and run under `systemctl --user`.

```bash
mkdir -p ~/.config/containers/systemd
cp deploy/quadlet/*.network deploy/quadlet/*.volume deploy/quadlet/*.container \
  ~/.config/containers/systemd/
```

Two edits are needed for rootless:

1. In `whatsnewdock-socket-proxy.container`, use the rootless socket path:
   ```ini
   Volume=%t/podman/podman.sock:/var/run/docker.sock:ro
   ```
2. In every unit, change the install target:
   ```ini
   [Install]
   WantedBy=default.target
   ```

Then:

```bash
systemctl --user daemon-reload
systemctl --user start whatsnewdock
loginctl enable-linger "$USER"   # keep running while logged out
```

## Secrets

Keep credentials out of the unit files in an `EnvironmentFile`. The server unit
reads `-`-prefixed `/etc/whatsnewdock.env` (optional), so it starts fine
without one:

```ini
# /etc/whatsnewdock.env  (chmod 600)
WND_INITIAL_ADMIN_USER=admin
WND_INITIAL_ADMIN_PASSWORD=change-me-please
# WND_OIDC_ISSUER=https://pocketid.example.com
# WND_OIDC_CLIENT_ID=whatsnewdock
# WND_OIDC_CLIENT_SECRET=...
# WND_BASE_URL=https://whatsnewdock.example.com
# WND_TRUSTED_PROXIES=10.0.0.0/8
# WND_GITHUB_TOKEN=ghp_...
```

```ini
# /etc/whatsnewdock-agent.env  (chmod 600)
WND_AGENT_SERVER_URL=https://whatsnewdock.example.com
WND_AGENT_TOKEN=wnd_xxxx
WND_AGENT_NAME=homelab-nas
```

If you don't set an admin password, a random one is generated and printed to
the service log on first start (`journalctl -u whatsnewdock`).

## Managing the services

```bash
# rootful
sudo systemctl status whatsnewdock
sudo journalctl -u whatsnewdock -f
sudo systemctl restart whatsnewdock

# rootless
systemctl --user status whatsnewdock
journalctl --user -u whatsnewdock -f
systemctl --user restart whatsnewdock
```

After editing any unit, run `systemctl daemon-reload` again so Quadlet
regenerates the service.

## Updating the image

Either pull and restart manually:

```bash
sudo podman pull ghcr.io/therealchickenlegs/whatsnewdock:latest
sudo systemctl restart whatsnewdock
```

…or let Podman do it. Add `AutoUpdate=registry` to the `[Container]` section of
the unit and enable the timer:

```ini
[Container]
AutoUpdate=registry
```

```bash
sudo systemctl enable --now podman-auto-update.timer   # rootless: --user
```

`podman auto-update` then pulls a newer image and restarts the unit when it
changes. Note the asymmetry with the app itself: for containers **inside**
WhatsNewDock you can use the built-in one-click update, but for the
WhatsNewDock container the host's systemd/auto-update owns it.

## Multi-host

Quadlet runs on the host it is installed on. To monitor several hosts, install
the socket-proxy **and** `whatsnewdock-agent.container` on each additional host
and point them at the central server:

1. In the server UI, open **Servers → Add server** and copy the one-time token.
2. On the extra host, put the token in `/etc/whatsnewdock-agent.env` and:
   ```bash
   sudo systemctl start whatsnewdock-socket-proxy whatsnewdock-agent
   ```

The agent only makes **outbound** connections — no inbound ports and no exposed
API on the monitored hosts. (A single-deployment, agentless mode is not part of
this change; the agent remains the recommended way to add hosts.)

## Notes and gotchas

- **Do not** add `PublishPort` to the socket-proxy unit. It is socket-equivalent
  for the API sections it enables and must stay on the internal network.
- **SELinux** (Fedora/RHEL): the proxy unit sets `SecurityLabelDisable=true` so
  it can read the mounted socket. Remove it if your host doesn't enforce SELinux.
- **Volume ownership**: the data volume is mounted with `:U`, which takes
  ownership for the image's non-root user (uid 65532). If you bind-mount a host
  path instead, `chown -R 65532:65532` it or the app cannot open its database.
- **Read-only mode**: set `POST=0` in the proxy unit and
  `WND_DOCKER_ENABLE_RECREATE=false` in the server unit to remove all write
  access. See [`docs/SECURITY.md`](../../docs/SECURITY.md).
- **API version**: if Podman's Docker-compatible API mis-negotiates, pin it with
  `Environment=WND_DOCKER_API_VERSION=1.41` in the server unit.
