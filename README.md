# SentinelDesk — a collaborative desktop for people and AI agents

A complete Linux desktop that runs **inside a Docker container with no physical
monitor**, streams to the browser over **WebRTC**, and is driven at the same
time by people and by an AI agent that sees and acts on the *same* X display.

**This is a standalone, self-contained product.** One `docker run` gives a full
desktop that people and an agent share — no control plane, no database, nothing
else to stand up. It is for anyone who wants a collaboration environment for
humans and machines and nothing more.

> Running a *fleet* of these desktops behind a single front desk — a room
> manager with accounts, scheduling and per-room lifecycle — is a **separate
> project in its own repository**. This one does not need it and does not depend
> on it; the two are being kept apart on purpose while the split is finished.

**📖 Documentation** — the full user guide and reference are published from this
repository with GitHub Pages: **<https://sentineldesk.github.io/desktop/docs/guide/index.html>**
(English, Spanish and Portuguese). It is where the **Docs** button in the
desktop's rail goes.

```mermaid
flowchart LR
    subgraph browser["Browser (people)"]
        rail["control rail<br/>+ WebRTC"]
    end
    subgraph host["AI host (agent)"]
        stdio["sentineldesk -mcp-stdio"]
    end
    subgraph container["One container — one shared X display"]
        daemon["sentineldesk<br/>(single Go binary)"]
        sock["Unix socket 0600<br/>MCP — 134 tools"]
        x["Xvfb :0 · Openbox · XFCE panel<br/>Chromium · VLC · Remmina · terminals"]
        gst["GStreamer in-process<br/>NVENC → VA-API → x264 → VP8"]
        daemon --- sock
        daemon --- gst
        daemon --- x
    end
    rail <-->|"WS + DataChannel"| daemon
    stdio <-->|"JSON-RPC"| sock
    gst -->|"one capture, fanned out"| rail
```

## Why it exists

The AI agent is a **participant**, not an API bolted to the side. It shares the
screen people are looking at:

- **They take turns.** Control is claimed, never assumed — by anybody, the agent
  included, and whether or not somebody is watching. Nobody holds the controls
  until they ask; asking while someone is driving puts a prompt on their screen
  with a timer, and no answer means no.
- **They read the same state.** Every command runs in a terminal window on the
  shared screen and reports its exit status, whoever typed it — so a person can
  hit an error, ask the agent to look, and the agent reads what actually
  happened rather than being told about it.
- **Nothing runs off-screen.** The agent's only reach is the MCP socket, and
  everything it does lands in a window the room can see. A cage nobody is
  looking into is just a smaller room.
- **One capture, many observers.** The screen is encoded once and fanned out to
  every browser, the recorder and any live stream. A second viewer costs
  bandwidth, not CPU.

People drive it through a control layer in the browser. The agent drives it
through a local Unix socket with **134 MCP tools**. Neither is a guest of the
other.

## Quick start

You do **not** need to clone this repository to use it — the image is published
on Docker Hub as `cnsoluciones/sentineldesk`, and it runs the same way
everywhere: a VPS, a Raspberry Pi (arm64), or your laptop. The only thing on
the host is Docker.

### Easiest: the installer

On a Linux host it installs Docker if it is missing, pulls the image, and starts
the desktop in the background:

```bash
curl -fsSL https://raw.githubusercontent.com/sentineldesk/desktop/main/install.sh | sudo bash
# add --full for the heavier apps, --pass <p> to set the password,
# --ip <addr> on a public VPS. Run with --help to see all options.
```

### docker run

```bash
docker run -d --name sentineldesk \
  -p 8080:8080 \
  -p 3478:3478/udp \
  -p 59000-59049:59000-59049/udp \
  -e AUTH_USER=admin -e AUTH_PASS=change-me \
  -v sentineldesk-home:/home/sentineldesk \
  -v sentineldesk-run:/run/sentineldesk \
  -e MCP_SOCK=/run/sentineldesk/mcp.sock \
  --shm-size=2g \
  cnsoluciones/sentineldesk:latest
# → open http://localhost:8080 and log in
```

The `sentineldesk-run` volume and `MCP_SOCK` expose the agent's MCP socket
outside the container, so an agent on the host — Claude Code, or
`sentineldesk-agent` — can drive the desktop directly, without `docker exec`.
Leave them out and the socket stays private to the container.

The UDP ports are the WebRTC media range and the embedded STUN responder;
publish them so the stream reaches you from another machine. The volume keeps
the home (browser profiles, files, the audit log) across restarts. Leave
`AUTH_USER` / `AUTH_PASS` empty for no login (local use only). The WebSocket is
the only authentication gate; no HTTP endpoint returns secrets.

### docker compose

Prefer compose? Grab the one that uses the published image (no build) and bring
it up:

```bash
curl -fsSLO https://raw.githubusercontent.com/sentineldesk/desktop/main/docker-compose.yml
AUTH_PASS=change-me HOST_IP=<your-ip> docker compose up -d
```

### lite or full

The tag chooses how much desktop you get:

- **`:latest`** (alias **`:lite`**) — the everyday desktop: Chromium, VLC,
  terminals, a file manager, the remote-desktop clients. Smaller image.
- **`:full`** — everything in lite plus the heavier applications: LibreOffice,
  Firefox, GIMP, Wireshark, and more (see [docs/packages.md](docs/packages.md)).
  Larger, but nothing to install afterwards.

Swap the tag to switch: `cnsoluciones/sentineldesk:full`.

### On a server or your LAN

To reach the desktop from another machine, tell it the address ICE should
advertise and serve HTTPS (WebRTC needs a secure context off localhost). This is
a real, working invocation on a Linux host at `172.17.0.17`:

```bash
docker run -d --name sentineldesk \
  -p 8080:8080 \
  -p 3478:3478/udp \
  -p 59000-59049:59000-59049/udp \
  -e AUTH_USER=admin -e AUTH_PASS=change-me \
  -e WEBRTC_MIN_PORT=59000 -e WEBRTC_MAX_PORT=59049 \
  -e NAT1TO1_IP=172.17.0.17 \
  -e TLS_SELFSIGNED=1 -e TLS_HOSTS=172.17.0.17 \
  -v sentineldesk-home:/home/sentineldesk \
  -e MCP_SOCK=/run/sentineldesk/mcp.sock \
  --shm-size=2g \
  cnsoluciones/sentineldesk:latest
# → open https://172.17.0.17:8080 (self-signed cert; accept the warning once)
```

- `NAT1TO1_IP` — the host's reachable IP, so ICE advertises an address a remote
  browser can actually connect to (set it to your public IP behind a router).
- `TLS_SELFSIGNED=1` + `TLS_HOSTS` — serve HTTPS with a self-signed certificate
  for those hosts. The certificate persists in the home volume.
- `WEBRTC_MIN_PORT` / `WEBRTC_MAX_PORT` — must match the published UDP range.

**Built-in VPN (optional):** the desktop can dial an OpenVPN connection. To
allow it, add `--cap-add NET_ADMIN --device /dev/net/tun`.

### From source

If you *are* working on the code, from this directory with Docker:

```bash
make up        # build the image and start the dev harness → https://localhost:8080
make logs      # follow the desktop's logs
make shell     # a root shell inside the running container
make down      # stop it
```

`make up` is the development harness (`deploy/docker-compose.dev.yml`) — one
container, HTTP on localhost, no authentication by default (self-signed cert;
accept the warning once). It is also the way to really verify a change: a green
`make test` says the tool catalogue is consistent and delivery does not panic,
and nothing more. Real verification means `make up` and exercising it.

## Requirements

- **To run:** Docker. Nothing else — the image carries the whole desktop.
  Optional: an NVIDIA or VA-API GPU is used automatically for encoding if
  present, and falls back to x264/VP8 in software if not.
- **To build the image:** Docker only. The React client and the Go binary are
  built inside the image's own stages, so no Node or Go toolchain is needed on
  the host.
- **To type-check or hack on the Go locally:** Go 1.26 and the GStreamer
  development libraries (CGO links against them). On macOS the code compiles
  against Homebrew GStreamer but only *runs* on Linux with X11 and PulseAudio.

## What is in it

| Layer | What it uses |
|---|---|
| Desktop | Xvfb, Openbox, xfce4-panel, xfdesktop; Chromium, VLC, lxterminal, Remmina, TigerVNC, FreeRDP — see [docs/packages.md](docs/packages.md) |
| Capture | `ximagesrc` with X DAMAGE tracking, one shared pipeline |
| Video | NVENC → VA-API → x264 → VP8, chosen by a real probe at startup |
| Audio | PulseAudio null sink → `opusenc`; a remapped source so the browser's microphone appears as a real input device |
| Transport | Pion WebRTC v4, GCC congestion control over TWCC, PLI keyframes, NACK, Opus in-band FEC; an embedded STUN responder |
| Human control | WebSocket signalling + DataChannel, XTEST injection, XFixes cursors, XShape peer pointers, EWMH |
| Agent control | MCP over a `0600` Unix socket, 134 tools, each classified by risk |
| Reading the screen | AT-SPI accessibility tree, Chrome DevTools Protocol — structure, not pixels |
| Recording & streaming | screenshots; record to MP4 / WebM / MKV; restream to RTMP (YouTube, Twitch, Facebook) or a custom sink — side pipelines run as `gst-launch` children so a bad one can never take the desktop down |
| Remote desktops | RDP, VNC and SPICE opened onto the shared screen via Remmina (or the direct clients as a fallback) |
| Files | a two-pane file manager and drag-and-drop, both directions over the DataChannel; downloads use one-use 60-second tickets, never tokens in URLs |
| Web UI | React (Vite), built to `internal/webui/assets` and `go:embed`'d, served with content ETags |
| Configuration | environment-only, read through `pkg/config` — no config file, by design |

## The two planes

**Plane 1 — people, over WebSocket.** `WS /ws` is the only door. The first frame
must authenticate; until it validates there is no SDP offer, no ICE, no
DataChannel. Input arrives on a DataChannel named `input` and goes into X
through XTEST. The screen is encoded **once** by the shared `Room` and every RTP
packet is fanned out to all participants; the shared encoder's bitrate is the
minimum of every participant's congestion estimate.

**Plane 2 — the agent, over a local Unix socket.** The daemon listens on a Unix
socket (mode `0600`), `MCP_SOCK`, which defaults to
`/run/user/1000/sentineldesk-mcp.sock`. Point it at a mounted directory
(`-e MCP_SOCK=/run/sentineldesk/mcp.sock` with `-v sentineldesk-run:/run/sentineldesk`)
and an agent on the host connects to the socket file directly — no `docker exec`.
Left at the default it stays private to the container, reached through
`sentineldesk -mcp-stdio` under `docker exec`, a thin stdin/stdout ↔ socket
JSON-RPC pipe: either way, killing the agent never takes the desktop down. The
desktop outlives whatever went wrong beside it.

**Control is claimed by either plane.** Every tool that puts input into X passes
through the same arbitration a person does — the agent never takes the controls
implicitly, not even with the room empty. The **panic button** (`Room.Abort`)
fires from any participant, kills the running jobs and takes the controls, but
deliberately leaves the agent connected: it keeps its eyes and loses its hands,
so it can read what happened.

## The 134 MCP tools

The agent reaches the desktop through one catalogue, split by theme under
[`internal/mcp/tools*.go`](internal/mcp). Every tool declares a **risk level**
(`read` / `write` / `danger`) and whether it **requires control**; a tool
missing either is a startup failure, not a default nobody chose. Highlights:

- **Pointer, keyboard, windows, desktops** — the raw acts, and EWMH underneath.
- **Terminal & jobs** — every command runs in a tmux window on the shared
  screen, stdout and stderr kept apart on disk, the exit code written only after
  the output has landed.
- **Reading the screen** — the AT-SPI tree and Chrome DevTools, structure rather
  than pixels.
- **Shell & SSH** — persistent shells; SSH sessions, transfers, key generation,
  local and remote tunnels (all inside the process).
- **Remote desktops** — RDP, VNC and SPICE opened as a window on the shared
  screen (Remmina, or the direct clients as a fallback), the graphical
  counterpart to headless SSH.
- **Files, packages, capture, the room** — read/write, apt, screenshots,
  recording and restreaming, presence and control.

Full reference: [docs/mcp.md](docs/mcp.md). The catalogue is versioned and its
count is pinned by a test against [docs/mcp-changelog.md](docs/mcp-changelog.md),
so it cannot fall behind by being forgotten. `tools/mcp-cli.py` speaks the same
protocol as an AI host, for exercising a tool without configuring anything.

## Layout

```
cmd/sentineldesk/     wiring only: flags, HTTP mux, WS upgrade, MCP socket
internal/desktop/     X11: XTEST injection, XFixes cursor, clipboard, peer pointers
internal/media/       GStreamer: pipelines, encoder selection, recording, restream, mic
internal/stream/      WS sessions, the shared Room, auth, TLS, file manager, STUN
internal/mcp/         the MCP server, its 134 tools, the risk registry and policy
internal/webui/       the browser client, embedded with go:embed (build output)
pkg/capability/       the room's verbs defined once: Def, risk/visibility, the control gate
pkg/config/           environment-only configuration
pkg/ratelimit/        per-origin token bucket + ban ledger
app/                  the React client (Vite + TS); built into internal/webui/assets
deploy/               the image: Dockerfile, desktop config, supervisor, package lists
docs/                 MCP reference, package list, and the user guide (en/es/pt)
```

The module is `github.com/sentineldesk/desktop`. Nothing under `internal/` is
importable from outside — the compiler enforces the public surface, which is
`pkg/`.

## Building

`make build` type-checks the tree. It links CGO against GStreamer, so it needs
the GStreamer development libraries locally; on macOS with Homebrew GStreamer it
compiles (with harmless warnings) but the binary only *runs* on Linux with X11
and PulseAudio. CGO also means plain cross-compilation is impossible:
`make release-binaries` builds each Linux architecture inside the Debian build
stage so a release binary is byte-for-byte what the container runs.

| Command | What it does |
|---|---|
| `make up` / `make down` / `make logs` / `make shell` | the dev harness |
| `make build` | type-check (needs local GStreamer dev libs) |
| `make app` | build the React client into `internal/webui/assets` |
| `make test` | the runtime test suites (see the caveat above) |
| `make image` / `make image-full` | build the container image (lite / full) |
| `make fmt` / `make vet` | gofmt and `go vet` over the module |
| `make push` / `make release` | publish the multi-arch image / cut a release |

Configuration is **environment-only**, read through `pkg/config`. There is no
config file, by design; every knob is a documented environment variable.

## Documentation

- **[User guide](https://sentineldesk.github.io/desktop/docs/guide/index.html)** —
  for the people using the desktop, in English, Spanish and Portuguese
  (published with GitHub Pages).
- **[MCP tool reference](https://github.com/sentineldesk/desktop/blob/main/docs/mcp.md)** —
  all 134 tools.
- **[Packages](https://github.com/sentineldesk/desktop/blob/main/docs/packages.md)** —
  what is in the lite and full images, and why.

## License

Licensed under the **Apache License 2.0** — see [LICENSE](LICENSE). Copyright
2026 Federico Pereira; every source file carries the license header the license
requires to be preserved in redistributions.

**Trademark**: "SentinelDesk" and its logo are trademarks of Federico Pereira
and are *not* covered by the code license (Apache 2.0 §6). You may fork, modify
and redistribute the code freely; you may not call the result SentinelDesk or
use its branding without permission.

A note on distribution: the Docker image bundles GStreamer plugins that include
GPL components (x264). The SentinelDesk source remains Apache 2.0; the combined
image, as distributed, is subject to those components' terms as well.

---

Created and maintained by **Federico Pereira** &lt;fpereira@cnsoluciones.com&gt;.
