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
# Docker is there, pull the published image from Docker Hub, and start the
# desktop in the background so a browser can reach it. It never builds anything
# and never needs a copy of this repository — cloning the source is for people
# who want to change it, which is a different path (see the README).
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
        -h|--help)  sed -n '14,37p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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
if [ -z "$AUTH_PASS" ]; then
    AUTH_PASS="$(openssl rand -base64 12 2>/dev/null | tr -d '/+=' | cut -c1-16 || true)"
    [ -z "$AUTH_PASS" ] && AUTH_PASS="sentineldesk"
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
    -v sentineldesk-home:/home/sentineldesk
    # Expose the MCP socket so an agent on the host — Claude Code, or
    # sentineldesk-agent — can drive the desktop directly, without `docker exec`.
    -v sentineldesk-run:/run/sentineldesk
    -e MCP_SOCK=/run/sentineldesk/mcp.sock
    --shm-size=2g)
if [ "$ENABLE_VPN" -eq 1 ]; then
    RUN+=(--cap-add NET_ADMIN --device /dev/net/tun)
fi
RUN+=("$IMAGE:$TAG")
"${RUN[@]}" >/dev/null

# --- Done --------------------------------------------------------------------
cat <<EOF

  SentinelDesk is running.

    URL       https://${HOST_IP}:${HTTP_PORT}   (self-signed cert — accept it once)
    user      ${AUTH_USER}
    password  ${AUTH_PASS}${GENERATED:+   (generated — save it)}
    image     ${IMAGE}:${TAG}

  MCP socket (for a host agent — Claude Code, sentineldesk-agent):
    in the 'sentineldesk-run' volume as mcp.sock. Its host path:
    docker volume inspect -f '{{.Mountpoint}}/mcp.sock' sentineldesk-run

  Logs:     docker logs -f ${NAME}
  Stop:     docker rm -f ${NAME}      (the home volume 'sentineldesk-home' is kept)
  Upgrade:  re-run this installer

EOF
