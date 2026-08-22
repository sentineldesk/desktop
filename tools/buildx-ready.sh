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
# buildx-ready.sh — leave a builder that can produce PLATFORMS, and print its
# name. `make push` and `make release-binaries` call it before they build.
#
# The problem it solves: the same `make push` works on a Mac and fails on a
# plain Linux server, and the difference is NOT the operating system. Docker
# Desktop ships a VM that already has QEMU registered and a builder that can
# push a manifest list; a bare dockerd on Debian has neither until somebody
# installs them. There are Linux hosts with Docker Desktop where it works and
# Macs on colima where it does not, so branching on `uname` would be guessing
# at the cause from a correlation.
#
# So this asks the only two questions that decide it:
#
#   1. Is there a builder whose driver can PUSH a manifest list? The default
#      `docker` driver usually cannot — it exports one image to the local store,
#      and a multi-arch push needs an index. `docker-container` always can, on
#      every platform, which is why it is what gets created here.
#   2. Does it report every platform asked for? A foreign architecture appears
#      only when the kernel has a binfmt handler for it. Without one the build
#      does not fail at the check — it fails deep inside the first RUN, with
#      `exec /bin/sh: exec format error`, twenty minutes in.
#
# Everything readable goes to stderr; stdout carries the builder name alone, so
# the caller can capture it.
#
#   builder="$(tools/buildx-ready.sh linux/amd64,linux/arm64)"

set -euo pipefail

PLATFORMS="${1:-linux/amd64,linux/arm64}"
BUILDER="${BUILDER:-sentineldesk}"
DOCKER="${DOCKER:-docker}"
# The image that registers QEMU handlers with the kernel. Pinned by name only:
# it is Docker's own, and its `--install` is the documented way to do this.
BINFMT_IMAGE="${BINFMT_IMAGE:-tonistiigi/binfmt}"

say()  { printf '\033[36m▸\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

# The platforms a builder reports, comma-separated and without spaces.
# --bootstrap starts the builder first: a stopped one reports nothing, which
# would read exactly like a builder that cannot do the job.
platforms_of() {
    $DOCKER buildx inspect --bootstrap "$1" 2>/dev/null \
        | sed -n 's/^ *[Pp]latforms: *//p' | tr -d ' ' | tr '\n' ','
}

driver_of() {
    $DOCKER buildx inspect "$1" 2>/dev/null | sed -n 's/^[Dd]river: *//p' | head -1
}

# Every requested platform present in that list. Substring matching is not
# enough — linux/arm64 must not be satisfied by linux/arm64/v8 only, and
# linux/arm is not linux/arm64 — so each one is compared whole.
supports() {
    local have=",$(platforms_of "$1")" p
    for p in ${2//,/ }; do
        case "$have" in
            *",$p,"*) ;;
            *) return 1 ;;
        esac
    done
    return 0
}

# --- 1. a builder that can push a manifest list ------------------------------
current="$($DOCKER buildx inspect 2>/dev/null | sed -n 's/^[Nn]ame: *//p' | head -1 || true)"
chosen=""

case "$(driver_of "${current:-__none__}")" in
    docker-container|kubernetes|remote)
        # Already the right kind of builder: keep it, and keep its cache.
        chosen="$current"
        ;;
esac

if [ -z "$chosen" ]; then
    if $DOCKER buildx inspect "$BUILDER" >/dev/null 2>&1; then
        chosen="$BUILDER"
    else
        say "creating the '$BUILDER' builder (docker-container: the driver that can push a manifest list)…"
        $DOCKER buildx create --name "$BUILDER" --driver docker-container --bootstrap >/dev/null \
            || die "could not create a buildx builder — is the Docker daemon reachable?"
        chosen="$BUILDER"
    fi
fi

# --- 2. the architectures it can actually execute ----------------------------
if ! supports "$chosen" "$PLATFORMS"; then
    have="$(platforms_of "$chosen")"
    say "'$chosen' does not offer $PLATFORMS yet (it has ${have%,}) — registering QEMU…"
    # binfmt_misc belongs to the KERNEL, so this is privileged, and on Docker
    # Desktop it lands inside the VM rather than on the Mac. Same command
    # either way; only the failure needs a different sentence.
    if ! $DOCKER run --privileged --rm "$BINFMT_IMAGE" --install all >/dev/null 2>&1; then
        case "$(uname -s)" in
            Darwin)
                die "QEMU registration failed. In Docker Desktop check Settings ▸ General ▸ 'Use Rosetta / VirtioFS', and that the VM is running." ;;
            *)
                die "QEMU registration failed. It needs to run privileged: 'docker run --privileged --rm $BINFMT_IMAGE --install all'. On a host that forbids that, install the qemu-user-static and binfmt-support packages instead." ;;
        esac
    fi
    # BuildKit reads the handler list when it starts, so a builder that was
    # already running will keep reporting the old set until it is restarted.
    $DOCKER buildx stop "$chosen" >/dev/null 2>&1 || true
    supports "$chosen" "$PLATFORMS" \
        || die "'$chosen' still cannot build $PLATFORMS (it has $(platforms_of "$chosen" | sed 's/,$//'))"
fi

# --- 3. credentials, as a warning only ---------------------------------------
# Never fatal: the credential store is a keychain on macOS and a helper on many
# Linux setups, so "no auth blob in config.json" is not evidence of anything.
# A push with no credentials fails in seconds with a clear message of its own.
if [ -f "${DOCKER_CONFIG:-$HOME/.docker}/config.json" ]; then
    grep -q '"auths"[[:space:]]*:[[:space:]]*{[[:space:]]*}' \
        "${DOCKER_CONFIG:-$HOME/.docker}/config.json" 2>/dev/null \
        && warn "no registry credentials in the Docker config — 'docker login' if the push is refused"
fi

say "builder: $chosen ($(driver_of "$chosen")) · $(platforms_of "$chosen" | sed 's/,$//')"
printf '%s\n' "$chosen"
