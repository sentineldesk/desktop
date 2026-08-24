#!/usr/bin/env bash
# SentinelDesk
# A collaborative operating system for people and AI agents.
#
# Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
#
# Licensed under the Apache License, Version 2.0.
#
# This product's name and logo are trademarks of Federico Pereira and are not
# covered by the license above. See the README for the trademark policy.
#
# SPDX-License-Identifier: Apache-2.0
#
# install.sh — the one-command way to run SentinelDesk on a VPS, a Raspberry Pi
# or any Linux host.
#
# It does the whole job an end user wants and nothing they do not: make sure
# Docker and socat are there, write a docker-compose.yml, and bring up the two
# services — the desktop and the agent beside it — so a browser can reach them.
# It never builds anything and never needs a copy of this repository — cloning
# the source is for people who want to change it, which is a different path
# (see the README).
#
# It writes compose rather than running `docker run`, and that is the point
# rather than a style choice. This is TWO containers joined by one volume, and a
# pair of `docker run` lines is a deployment nobody can inspect, upgrade or stop
# as a unit afterwards. What this leaves behind is a file: `docker compose ps`
# answers what is running, `docker compose pull && up -d` upgrades it, and the
# person who inherits the machine can read what was decided.
#
#   curl -fsSL https://raw.githubusercontent.com/sentineldesk/desktop/main/install.sh | sudo bash
#
# Flags (or the matching environment variables):
#   --full            the full image (LibreOffice, Firefox, GIMP…); default is lite
#   --pass <p>        the login password        (AUTH_PASS; default: generated)
#   --user <u>        the login username        (AUTH_USER; default: admin)
#   --ip <addr>       the address browsers reach this host at, for WebRTC + TLS
#                     (HOST_IP; default: autodetected — set it on a public VPS)
#   --port <n>        the web port              (HTTP_PORT; default: 8080)
#   --name <n>        the container name        (NAME; default: sentineldesk)
#   --vpn             allow the built-in OpenVPN client (adds NET_ADMIN + tun)
#   --no-agent        the desktop only; no agent container
#   --dir <path>      where to write docker-compose.yml (default: /opt/sentineldesk)
#   --no-pull         do not pull; use the image already on the host
#   -h, --help        this help

set -euo pipefail

IMAGE="${IMAGE:-cnsoluciones/sentineldesk}"
TAG="${TAG:-latest}"                     # latest == lite
NAME="${NAME:-sentineldesk}"
HTTP_PORT="${HTTP_PORT:-8080}"
STUN_PORT="${STUN_PORT:-3478}"
WEBRTC_MIN="${WEBRTC_MIN:-59000}"
WEBRTC_MAX="${WEBRTC_MAX:-59049}"
AUTH_USER="${AUTH_USER:-admin}"
AUTH_PASS="${AUTH_PASS:-}"
HOST_IP="${HOST_IP:-}"
ENABLE_VPN="${ENABLE_VPN:-0}"
AGENT_IMAGE="${AGENT_IMAGE:-cnsoluciones/sentineldesk-agent}"
AGENT_TAG="${AGENT_TAG:-latest}"
WITH_AGENT="${WITH_AGENT:-1}"
# Where the compose file lands. /opt is the conventional place for something an
# operator installed rather than something a distribution shipped, and it has to
# be a real directory on disk: a compose file in $PWD would be lost the moment
# somebody ran the installer from their home and later looked in /opt.
STACK_DIR="${STACK_DIR:-/opt/sentineldesk}"
PULL=1

log()  { printf '\033[36m▸\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
    case "$1" in
        --full)     TAG="full" ;;
        --lite)     TAG="latest" ;;
        --tag)      TAG="$2"; shift ;;
        --pass)     AUTH_PASS="$2"; shift ;;
        --user)     AUTH_USER="$2"; shift ;;
        --ip)       HOST_IP="$2"; shift ;;
        --port)     HTTP_PORT="$2"; shift ;;
        --name)     NAME="$2"; shift ;;
        --vpn)      ENABLE_VPN=1 ;;
        --no-agent) WITH_AGENT=0 ;;
        --dir)      STACK_DIR="$2"; shift ;;
        --no-pull)  PULL=0 ;;
        -h|--help)  sed -n '14,44p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *)          die "unknown option: $1 (try --help)" ;;
    esac
    shift
done

# --- Docker ------------------------------------------------------------------
# On a fresh VPS there is usually no Docker. Install it the way Docker itself
# recommends, non-interactively, then make sure the service is up.
if ! command -v docker >/dev/null 2>&1; then
    log "Docker is not installed — installing it (get.docker.com)…"
    if [ "$(id -u)" -ne 0 ]; then
        die "installing Docker needs root; re-run with sudo, or install Docker yourself first"
    fi
    curl -fsSL https://get.docker.com | sh || die "Docker installation failed"
    systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true
fi
docker info >/dev/null 2>&1 || die "Docker is installed but not running — start it and try again"

# --- socat -------------------------------------------------------------------
# The MCP socket is put on a host volume below, so an AI host on this machine
# (Claude Code, sentineldesk-agent) can reach it through a plain stdio↔socket
# relay instead of `docker exec` — one socket rather than the whole Docker
# daemon, which is a far larger permission than reading a 0600 file. socat is
# that relay and it weighs about two megabytes, so it goes in now rather than
# being discovered missing at the moment somebody configures their agent.
#
# NEVER fatal: the desktop does not need socat to run, and plenty of hosts will
# never point an agent at this one. A warning is the whole remedy.
install_socat() {
    if   command -v apt-get >/dev/null 2>&1; then
        DEBIAN_FRONTEND=noninteractive apt-get update -qq \
            && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq socat
    elif command -v dnf    >/dev/null 2>&1; then dnf install -y socat
    elif command -v yum    >/dev/null 2>&1; then yum install -y socat
    elif command -v zypper >/dev/null 2>&1; then zypper --non-interactive install socat
    elif command -v apk    >/dev/null 2>&1; then apk add --no-cache socat
    elif command -v pacman >/dev/null 2>&1; then pacman -Sy --noconfirm socat
    else return 1
    fi
}
if ! command -v socat >/dev/null 2>&1; then
    if [ "$(id -u)" -eq 0 ]; then
        log "installing socat (the MCP bridge for an agent on this host)…"
        install_socat >/dev/null 2>&1 || true
    fi
    command -v socat >/dev/null 2>&1 \
        || warn "socat could not be installed — install it by hand before pointing an AI host at the MCP socket"
fi

# --- Where browsers reach this host ------------------------------------------
# WebRTC hands the browser an address to connect back on; on a server that must
# be the host's real address, not the container's bridge IP. Autodetect the
# primary one, but say so — a public VPS behind a cloud NAT needs its PUBLIC IP,
# which only the operator knows, so --ip / HOST_IP wins.
if [ -z "$HOST_IP" ]; then
    HOST_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{print $7; exit}')"
    [ -z "$HOST_IP" ] && HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
    [ -z "$HOST_IP" ] && HOST_IP="localhost"
    warn "no --ip given; using autodetected $HOST_IP. On a public VPS pass --ip <public-ip>."
fi

# --- Credentials -------------------------------------------------------------
# A password nobody chose has to be a password nobody can guess. openssl is the
# first try and /dev/urandom the second, because a minimal image (a slim base, a
# CI container) often has the kernel and not the package — that is not an exotic
# host, it is the ordinary one for an installer piped from curl.
#
# If neither works this FAILS. It used to fall back to the constant
# "sentineldesk" and still announce it as "(generated — save it)", which is the
# worst of both: a predictable login on a published desktop, described to the
# person as a unique one. An installer that cannot generate a secret must say so
# rather than choose a bad one quietly.
if [ -z "$AUTH_PASS" ]; then
    AUTH_PASS="$(openssl rand -base64 12 2>/dev/null | tr -d '/+=' | cut -c1-16 || true)"
    [ -z "$AUTH_PASS" ] && AUTH_PASS="$(head -c 24 /dev/urandom 2>/dev/null | base64 2>/dev/null | tr -d '/+=' | cut -c1-16 || true)"
    [ -z "$AUTH_PASS" ] && die "could not generate a password (no openssl, no /dev/urandom) — pass one with --pass"
    GENERATED=1
fi

# --- Compose v2 --------------------------------------------------------------
# The plugin, not the old standalone `docker-compose` binary. get.docker.com
# ships it, but a host with Docker already installed from a distribution package
# may not have it — and finding that out at `up` time, after the file is
# written, is the wrong moment.
if ! docker compose version >/dev/null 2>&1; then
    die "Docker Compose v2 is missing (\`docker compose version\` fails).
    On Debian/Ubuntu: apt-get install docker-compose-plugin
    Elsewhere: https://docs.docker.com/compose/install/"
fi

# --- Retire anything started the old way -------------------------------------
# Earlier versions of this installer used `docker run`, so an upgrade meets
# containers compose did not create and will not adopt. Remove them: the VOLUMES
# are what hold the state and they are kept, so this loses nothing but the
# container objects.
for old in "$NAME" "${NAME}-agent"; do
    if docker ps -a --format '{{.Names}}' | grep -qx "$old"; then
        log "removing the existing '$old' container (its volumes are kept)…"
        docker rm -f "$old" >/dev/null 2>&1 || true
    fi
done

# --- Write the stack ---------------------------------------------------------
mkdir -p "$STACK_DIR" || die "could not create $STACK_DIR"
COMPOSE="$STACK_DIR/docker-compose.yml"

# A previous file is kept, once, under a dated name. Overwriting somebody's
# hand-edited compose without a copy is the kind of loss an installer has no
# right to cause — and a re-run of this script is the commonest way it would
# happen.
if [ -f "$COMPOSE" ]; then
    BACKUP="$COMPOSE.$(date +%Y%m%d-%H%M%S).bak"
    cp -a "$COMPOSE" "$BACKUP"
    warn "an existing $COMPOSE was kept as $(basename "$BACKUP")"
fi

log "writing $COMPOSE …"
{
cat <<YAML
# Written by install.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ). Safe to edit and
# re-apply with: docker compose -f $COMPOSE up -d
#
# Two services joined by ONE volume, sentineldesk-run, which is a directory of
# Unix sockets. The agent has no ports and no network of its own, so nothing
# about the conversation leaves this host — and the agent can only do what that
# socket grants, which is why there is one security boundary here and not two.
name: ${NAME}

services:
  sentineldesk:
    image: ${IMAGE}:${TAG}
    container_name: ${NAME}
    restart: unless-stopped
    ports:
      - "${HTTP_PORT}:8080"
      - "${STUN_PORT}:3478/udp"
      - "${WEBRTC_MIN}-${WEBRTC_MAX}:${WEBRTC_MIN}-${WEBRTC_MAX}/udp"
    environment:
      - AUTH_USER=${AUTH_USER}
      - AUTH_PASS=${AUTH_PASS}
      - WEBRTC_MIN_PORT=${WEBRTC_MIN}
      - WEBRTC_MAX_PORT=${WEBRTC_MAX}
      # The address browsers reach this host at. Off localhost this is required,
      # or WebRTC advertises the container's bridge IP and the video stays black.
      - NAT1TO1_IP=${HOST_IP}
      - TLS_SELFSIGNED=1
      - TLS_HOSTS=${HOST_IP}
      # The socket lives in a mounted directory so an agent can reach it.
      - MCP_SOCK=/run/sentineldesk/mcp.sock
    volumes:
      - ${NAME}-home:/home/sentineldesk
      - ${NAME}-run:/run/sentineldesk
      - ${NAME}-audit:/var/log/sentineldesk
      - ${NAME}-work:/tmp/sentineldesk
    shm_size: "2gb"
YAML

if [ "$ENABLE_VPN" -eq 1 ]; then
cat <<'YAML'
    # --vpn was given: the built-in OpenVPN client needs both of these.
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun
YAML
fi

if [ "$WITH_AGENT" -eq 1 ]; then
cat <<YAML

  # The agent: the brain that talks to the model providers and drives the
  # desktop. depends_on is deliberately absent — it waits for the desktop and
  # reconnects when it comes back, so the desktop runs perfectly well with this
  # service never started at all:  docker compose up -d sentineldesk
  agent:
    image: ${AGENT_IMAGE}:${AGENT_TAG}
    container_name: ${NAME}-agent
    restart: unless-stopped
    environment:
      - MCP_SOCK=/run/sentineldesk/mcp.sock
    volumes:
      - ${NAME}-run:/run/sentineldesk
      # The agent's whole home: its model choice, its keys, its history — and
      # any vendor CLI it drives a model through, which installs into
      # ~/.local/bin with credentials in its own dotfiles. A volume covering
      # only .sentineldesk would keep the preference and lose the tool.
      - ${NAME}-agent:/home/agent
YAML
fi

# The names are PINNED. Compose otherwise prefixes them with the project name,
# and an agent on the HOST looks for a volume literally called
# '${NAME}-run' to find the socket.
cat <<YAML

volumes:
  ${NAME}-home:
    name: ${NAME}-home
  ${NAME}-run:
    name: ${NAME}-run
  ${NAME}-audit:
    name: ${NAME}-audit
  ${NAME}-work:
    name: ${NAME}-work
YAML

if [ "$WITH_AGENT" -eq 1 ]; then
cat <<YAML
  ${NAME}-agent:
    name: ${NAME}-agent
YAML
fi
} > "$COMPOSE"

# --- Bring it up -------------------------------------------------------------
[ "$PULL" -eq 1 ] && { log "pulling images…"; docker compose -f "$COMPOSE" pull -q || true; }

log "starting SentinelDesk…"
docker compose -f "$COMPOSE" up -d >/dev/null

# --- Done ---------------------------------------------------------------------
# What to say about the agent depends on whether one was started, and saying
# nothing when it was is how somebody never finds out they have to pick a model.
if [ "$WITH_AGENT" -eq 1 ]; then
    AGENT_NOTE="    agent     ${AGENT_IMAGE}:${AGENT_TAG}  (container ${NAME}-agent)

  The agent has no model yet — that is deliberate, the key is not something an
  installer should hold. Open a session in its container and type /connect:

    docker exec -it ${NAME}-agent sentineldesk-agent

  The chat panel in the browser goes from amber to green on its own. /help in
  that session lists everything you can type."
else
    AGENT_NOTE="    agent     not started (--no-agent). Add one later by removing that flag
              and re-running this installer, or run sentineldesk-agent -serve
              on this host — it finds the socket in the ${NAME}-run volume."
fi

# Ask Docker where the volume landed rather than assuming /var/lib/docker:
# rootless Docker, a custom data-root and snap packages all put it elsewhere.
# The default is only the fallback for the case where the question fails.
MCP_SOCK_HOST="$(docker volume inspect -f '{{.Mountpoint}}/mcp.sock' "${NAME}-run" 2>/dev/null || true)"
[ -z "$MCP_SOCK_HOST" ] && MCP_SOCK_HOST="/var/lib/docker/volumes/${NAME}-run/_data/mcp.sock"

cat <<EOF

  SentinelDesk is running.

    URL       https://${HOST_IP}:${HTTP_PORT}   (self-signed cert — accept it once)
    user      ${AUTH_USER}
    password  ${AUTH_PASS}${GENERATED:+   (generated — save it)}
    image     ${IMAGE}:${TAG}
${AGENT_NOTE}
  MCP socket (for a host agent — Claude Code, sentineldesk-agent):
    in the '${NAME}-run' volume as mcp.sock. Its host path:
    docker volume inspect -f '{{.Mountpoint}}/mcp.sock' ${NAME}-run

    Point an AI host at it with socat (the sudo is for the directory Docker
    keeps volumes in, not for the socket itself):
    {"mcpServers":{"sentineldesk":{"command":"sudo","args":["socat","STDIO",
     "UNIX-CONNECT:${MCP_SOCK_HOST}"]}}}

  Logs:     docker compose -f ${COMPOSE} logs -f
  Status:   docker compose -f ${COMPOSE} ps
  Stop:     docker compose -f ${COMPOSE} down        (the volumes are kept)
  Upgrade:  docker compose -f ${COMPOSE} pull && docker compose -f ${COMPOSE} up -d

  The stack file is ${COMPOSE} — edit it and re-apply with \`up -d\`.
EOF
