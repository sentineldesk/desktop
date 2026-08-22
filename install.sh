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
# Docker and socat are there, pull the published image from Docker Hub, and
# start the desktop in the background so a browser can reach it. It never builds
# anything and never needs a copy of this repository — cloning the source is for
# people who want to change it, which is a different path (see the README).
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
        --no-pull)  PULL=0 ;;
        -h|--help)  sed -n '14,35p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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

# --- Replace any previous instance -------------------------------------------
if docker ps -a --format '{{.Names}}' | grep -qx "$NAME"; then
    log "an existing '$NAME' container is here — replacing it (the home volume is kept)…"
    docker rm -f "$NAME" >/dev/null 2>&1 || true
fi

[ "$PULL" -eq 1 ] && { log "pulling $IMAGE:$TAG …"; docker pull "$IMAGE:$TAG"; }

# --- Run ---------------------------------------------------------------------
log "starting SentinelDesk…"
RUN=(docker run -d --name "$NAME" --restart unless-stopped
    -p "${HTTP_PORT}:8080"
    -p "${STUN_PORT}:3478/udp"
    -p "${WEBRTC_MIN}-${WEBRTC_MAX}:${WEBRTC_MIN}-${WEBRTC_MAX}/udp"
    -e AUTH_USER="$AUTH_USER" -e AUTH_PASS="$AUTH_PASS"
    -e WEBRTC_MIN_PORT="$WEBRTC_MIN" -e WEBRTC_MAX_PORT="$WEBRTC_MAX"
    -e NAT1TO1_IP="$HOST_IP"
    -e TLS_SELFSIGNED=1 -e TLS_HOSTS="$HOST_IP"
    -v "${NAME}-home":/home/sentineldesk
    # Both volumes are named after the CONTAINER, so a second desktop started
    # with --name is a second desktop: with the names fixed, two instances on
    # one host shared a home and — silently, and much worse — bound the same
    # mcp.sock, so the newer daemon took over the older one's control socket.
    # With the default NAME these are the same 'sentineldesk-home' and
    # 'sentineldesk-run' as before, so nothing is orphaned by the change.
    #
    # The run volume is what lets an agent on the host — Claude Code, or
    # sentineldesk-agent — drive the desktop directly, without `docker exec`.
    -v "${NAME}-run":/run/sentineldesk
    -e MCP_SOCK=/run/sentineldesk/mcp.sock
    --shm-size=2g)
if [ "$ENABLE_VPN" -eq 1 ]; then
    RUN+=(--cap-add NET_ADMIN --device /dev/net/tun)
fi
RUN+=("$IMAGE:$TAG")
"${RUN[@]}" >/dev/null

# --- Done --------------------------------------------------------------------
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

  MCP socket (for a host agent — Claude Code, sentineldesk-agent):
    in the '${NAME}-run' volume as mcp.sock. Its host path:
    docker volume inspect -f '{{.Mountpoint}}/mcp.sock' ${NAME}-run

    Point an AI host at it with socat (the sudo is for the directory Docker
    keeps volumes in, not for the socket itself):
    {"mcpServers":{"sentineldesk":{"command":"sudo","args":["socat","STDIO",
     "UNIX-CONNECT:${MCP_SOCK_HOST}"]}}}

  Logs:     docker logs -f ${NAME}
  Stop:     docker rm -f ${NAME}      (the home volume '${NAME}-home' is kept)
  Upgrade:  re-run this installer

EOF
