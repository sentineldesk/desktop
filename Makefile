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

# The DISTRO's Makefile: the desktop image and the binary inside it. It does
# not build the agent — that is its own repository, releasing on its own
# schedule, and it reaches this desktop over the MCP socket like any other
# client. Nothing here knows the front desk exists either — that is the point
# of this directory. The panel's Makefile lives at the repository
# root and calls into this one for the room image.
#
# One constraint shapes everything here: the binary uses CGO (go-gst links
# GStreamer), so it CANNOT be cross-compiled with a bare GOOS/GOARCH the way a
# static Go project can. Every Linux binary is built inside Docker on a Debian
# 13 base — the same image the desktop runs on — and buildx provides the other
# architecture through emulation. What comes out of `release-binaries` is
# byte-for-byte the binary the container runs, which is exactly what a native
# install on Debian 13 or Raspberry Pi OS wants.

BINARY  := sentineldesk
DIST    := dist
GO      := go
DOCKER  ?= docker
# ONE name, and everything else derived from it. Override REGISTRY_IMAGE for
# another registry and every tag below follows.
#
# It used to be two names — `sentineldesk:*` locally and
# `cnsoluciones/sentineldesk:*` on push — and the split cost more than it
# bought. The compose files name the registry image, so what `make image` built
# was never what `docker compose up` ran: it pulled a published build while a
# freshly compiled one sat in the daemon unused, and the only sign was a version
# in the footer that did not match the source on disk. Same name for both, and
# that cannot happen.
#
# Two variants from one Dockerfile: lite is the desktop plus the tools people
# need in it; full adds what is too large or too niche to hand everybody. Both
# carry the version in the tag, because "latest" answers no question worth
# asking six months from now — and both keep a moving tag so the compose files
# do not have to be edited on every build.
REGISTRY_IMAGE ?= cnsoluciones/sentineldesk
IMAGE          ?= $(REGISTRY_IMAGE):latest
IMAGE_LITE     ?= $(REGISTRY_IMAGE):lite
IMAGE_FULL     ?= $(REGISTRY_IMAGE):full
# The architectures published, and the builder that produces them. BUILDER is
# only a NAME: tools/buildx-ready.sh keeps whatever capable builder is already
# selected (Docker Desktop's, a remote node) and creates this one only when
# there is none. See that script for why the choice is not made by OS.
PLATFORMS ?= linux/amd64,linux/arm64
BUILDER   ?= sentineldesk
# ONE compose file, the same one a person downloads and runs by hand.
#
# There were two — this and a deploy/docker-compose.dev.yml with a `build:`
# stanza — and the split was a leftover from when a local build and a published
# one had different image names. They do not any more: `make image` tags
# cnsoluciones/sentineldesk:latest, which is exactly what the file below names,
# so `make up` builds and then starts the same thing `docker compose up -d`
# starts.
#
# Keeping both had a cost that was not obvious. The dev file never set MCP_SOCK,
# so `make up` produced a desktop whose socket stayed inside the container and
# no agent could reach — a difference nobody would look for, between two files
# that were supposed to describe one system.
#
# No -p here on purpose: the file pins `name: sentineldesk` itself, and passing
# the project on the command line would let the two disagree again.
COMPOSE ?= $(DOCKER) compose -f docker-compose.yml

# ─── Version, derived from git ───────────────────────────────────────────────
# The patch auto-increments (with carry: .9 bumps the minor) every time the git
# hash changes, persisted in version.txt — local and git-ignored, so each
# machine counts its own builds. Stamped into the binary with -ldflags -X;
# `sentineldesk -version` prints it and the rail shows it.
#
# THE MAJOR NEVER AUTO-INCREMENTS WHILE IT IS 0 — see the repository root's
# Makefile for the full story; the rule is the same here because the two
# halves version independently on purpose.
VERSION_PKG     := github.com/sentineldesk/desktop/pkg/version
INITIAL_VERSION ?= 0.5.0
VERSION_FILE    := version.txt

git_hash   := $(shell git rev-parse --short HEAD 2>/dev/null || echo development)
build_date := $(shell date +%Y%m%d-%H%M%S)

ifeq ($(wildcard $(VERSION_FILE)),)
  last_version  := $(INITIAL_VERSION)
  last_git_hash :=
else
  last_version  := $(shell awk -F': ' '/initial_version:/ {print $$2}' $(VERSION_FILE) | xargs)
  last_git_hash := $(shell awk -F': ' '/git_hash:/ {print $$2}' $(VERSION_FILE) | xargs)
endif

ifeq ($(strip $(last_git_hash)),)
  next_version := $(last_version)
else ifeq ($(strip $(git_hash)),$(strip $(last_git_hash)))
  next_version := $(last_version)
else
  next_version := $(shell \
    major=$$(echo $(last_version) | awk -F. '{print $$1}'); \
    minor=$$(echo $(last_version) | awk -F. '{print $$2}'); \
    patch=$$(echo $(last_version) | awk -F. '{print $$3}'); \
    if [ "$$patch" -ge 9 ]; then \
      if [ "$$minor" -ge 9 ] && [ "$$major" -gt 0 ]; then echo "$$((major + 1)).0.0"; \
      else echo "$$major.$$((minor + 1)).0"; fi; \
    else echo "$$major.$$minor.$$((patch + 1))"; fi)
endif

VERSION_LDFLAGS := -X $(VERSION_PKG).Version=$(next_version) \
                   -X $(VERSION_PKG).GitHash=$(git_hash) \
                   -X $(VERSION_PKG).BuildDate=$(build_date)
LDFLAGS := -s -w $(VERSION_LDFLAGS)

VERSION_ARGS := --build-arg VERSION=$(next_version) \
                --build-arg GIT_HASH=$(git_hash) \
                --build-arg BUILD_DATE=$(build_date)

.PHONY: app build image image-lite image-full up down logs shell test fmt vet help \
        _version version release-binaries checksums push release \
        ssh-peer ssh-peer-down test-integration check-secrets

# _version persists version.txt and prints the version. One target, so make
# runs it once even when several builds depend on it.
_version:
	@echo "initial_version: $(next_version)" > $(VERSION_FILE)
	@echo "git_hash: $(git_hash)" >> $(VERSION_FILE)
	@echo "▶ version: v$(next_version) ($(git_hash)) · build $(build_date)"

## version: show the version a build would get, without building
version:
	@echo "v$(next_version) ($(git_hash)) · build $(build_date)"

## app: build the web client into the embed directory (needs node)
#
# The client is React now (app/), and the embed directory is its BUILD
# OUTPUT — vite writes internal/webui/assets, go:embed carries it. npm ci
# runs only when node_modules is missing, so the everyday loop is one vite
# build (~1s), not a dependency install.
app:
	@cd app && ([ -d node_modules ] || npm ci --no-audit --no-fund) && npm run build
	@# vite's emptyOutDir sweeps .gitkeep away with the previous build; put it
	@# back so the one tracked file in assets/ — the one that makes a fresh
	@# clone's go:embed succeed before npm has ever run — never shows deleted.
	@touch internal/webui/assets/.gitkeep

## build: compile for the host — a fast type check (needs local GStreamer + node)
build: _version app
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" ./...

## image: build the container image, version stamped in
image: _version
	@echo "▶ lite…"
	$(DOCKER) build $(VERSION_ARGS) -f deploy/Dockerfile --target desktop \
	  -t $(IMAGE) -t $(IMAGE_LITE) -t $(REGISTRY_IMAGE):$(next_version) \
	  -t $(REGISTRY_IMAGE):$(next_version)-lite .
	@echo "▶ full…"
	$(DOCKER) build $(VERSION_ARGS) -f deploy/Dockerfile --target full \
	  -t $(IMAGE_FULL) -t $(REGISTRY_IMAGE):$(next_version)-full .
	@echo "✓ $(REGISTRY_IMAGE):$(next_version)-lite  (also :latest, :lite)"
	@echo "✓ $(REGISTRY_IMAGE):$(next_version)-full  (also :full)"

## image-lite: only the lite variant, when the full one is not needed
image-lite: _version
	$(DOCKER) build $(VERSION_ARGS) -f deploy/Dockerfile --target desktop \
	  -t $(IMAGE) -t $(IMAGE_LITE) -t $(REGISTRY_IMAGE):$(next_version)-lite .

## image-full: only the full variant
image-full: _version
	$(DOCKER) build $(VERSION_ARGS) -f deploy/Dockerfile --target full \
	  -t $(IMAGE_FULL) -t $(REGISTRY_IMAGE):$(next_version)-full .

## up: build the image and start the desktop (the development harness)
up: image
	$(COMPOSE) up -d

## down: stop everything
down:
	$(COMPOSE) down --remove-orphans

## logs: follow the desktop's logs
logs:
	$(COMPOSE) logs -f sentineldesk

## shell: a root shell inside the running desktop
shell:
	$(DOCKER) exec -it -u root sentineldesk bash

test: check-secrets
	$(GO) test ./...
	@# The client's own tests, when its dependencies are installed. Skipped
	@# rather than failed when they are not: a Go developer running `make test`
	@# on a fresh clone should not be stopped by an npm install they did not
	@# ask for, and CI installs them. Silence here would be worse than either —
	@# a test suite that quietly does not run is one nobody notices is gone.
	@if [ -d app/node_modules ]; then \
	  echo "▶ client tests"; cd app && npm test --silent; \
	else \
	  echo "⚠ app/node_modules is missing, so the client tests did NOT run — \
`cd app && npm install` to include them"; \
	fi

## check-secrets: refuse to build if a credential ever reaches the tree
#
# The first line of defence is that keys live in ~/.sentineldesk, outside any
# checkout, so there is nothing to commit. This is the second: it catches a key
# pasted into a config file, a test fixture or a comment while somebody was
# getting something working, and then forgotten.
#
# It scans what git tracks under THIS directory, not the working directory,
# because an ignored file holding a key is a file doing its job.
check-secrets:
	@git ls-files -z | xargs -0 grep -lE '(sk-ant-[A-Za-z0-9_-]{16,}|sk-[A-Za-z0-9]{32,}|AIza[A-Za-z0-9_-]{20,})' 2>/dev/null \
		| grep -vE '(_test\.go|\.test\.tsx?)$$' > /tmp/sd-secrets.$$$$ || true; \
	if [ -s /tmp/sd-secrets.$$$$ ]; then \
		echo "\033[31mA credential-shaped string is in a tracked file:\033[0m"; \
		cat /tmp/sd-secrets.$$$$; \
		echo "Keys belong in ~/.sentineldesk/<provider>.key, never in the repository."; \
		rm -f /tmp/sd-secrets.$$$$; exit 1; \
	fi; \
	rm -f /tmp/sd-secrets.$$$$; \
	echo "✓ no credentials in tracked files"

## test-integration: drive a RUNNING desktop through MCP and check what happened
#
# Behind a build tag so `make test` keeps its property: it runs anywhere, with no
# X, no GStreamer and no container, and a green run means the logic is sound
# rather than that the machine was set up. These need a desktop — start it with
# make up — and each check reads the container directly rather than confirming
# one tool with another, which would only establish that the two agree.
test-integration:
	go test -tags integration -count=1 -v ./test/integration/...

## ssh-peer: a second host on the desktop's network, for testing the ssh_* tools
#
# The sweep can start sshd inside the desktop and connect to 127.0.0.1, which
# runs the code without proving much: a loopback session cannot show that a file
# crossed a machine boundary, and a tunnel to your own host forwards to where you
# already are. Against a real peer ssh_exec returns the OTHER hostname and a
# local forward hands back the peer's own banner — and it was that difference
# which exposed a tunnel reporting success for a forward the server had refused.
ssh-peer:
	@tools/ssh-peer.sh up
	@tools/ssh-peer.sh forward

## ssh-peer-down: remove it
ssh-peer-down:
	@tools/ssh-peer.sh down

## release-binaries: Linux amd64 + arm64 binaries into dist/, named with the version
#
# Built INSIDE the Docker build stage (Go on Debian 13) and exported with
# buildx, one platform at a time. The foreign architecture runs under QEMU —
# slow, but it is a real build against real Debian 13 libraries, which is the
# only kind CGO permits. The result runs on any Debian 13: a VPS, a Raspberry
# Pi 5 on Raspberry Pi OS (trixie), a cloud instance.
release-binaries: _version
	@mkdir -p $(DIST)
	@builder="$$(BUILDER=$(BUILDER) DOCKER=$(DOCKER) tools/buildx-ready.sh $(PLATFORMS))" || exit 1; \
	for arch in amd64 arm64; do \
	  echo "▶ linux/$$arch…"; \
	  $(DOCKER) buildx build --builder "$$builder" $(VERSION_ARGS) \
	    --platform linux/$$arch --target bin \
	    --output type=local,dest=$(DIST)/.stage-$$arch \
	    -f deploy/Dockerfile . || exit 1; \
	  cp $(DIST)/.stage-$$arch/sentineldesk \
	     $(DIST)/$(BINARY)-v$(next_version)-linux-$$arch || exit 1; \
	  rm -rf $(DIST)/.stage-$$arch; \
	  echo "✓ $(DIST)/$(BINARY)-v$(next_version)-linux-$$arch"; \
	done

## checksums: SHA256SUMS.txt over the binaries in dist/
#
# One file with every hash in it, so a downloader fetches one thing and no
# binary can be verified against a checksum list that does not know about it.
#
# It used to cover the agent as well, because sentineldesk-agent-* matches
# sentineldesk-*. The agent is its own repository now and signs its own
# release; this glob is back to meaning exactly what it says.
checksums:
	@cd $(DIST) && if command -v sha256sum >/dev/null 2>&1; \
	  then sha256sum $(BINARY)-* > SHA256SUMS.txt; \
	  else shasum -a 256 $(BINARY)-* > SHA256SUMS.txt; fi
	@echo "✓ $(DIST)/SHA256SUMS.txt"

## push: build and push the multi-arch image (:latest and :<version>)
#
# One buildx invocation for both platforms, so the registry holds a single
# manifest list and `docker pull` picks the right architecture on its own.
# Needs `docker login` first; override REGISTRY_IMAGE for another registry.
#
# tools/buildx-ready.sh runs first and leaves a builder that can actually do it
# — the docker driver cannot push a manifest list, and a Linux host without
# QEMU registered fails inside the first arm64 RUN rather than at the check.
# That is why the same command has always worked on a Mac and not on a server.
push: _version
	@builder="$$(BUILDER=$(BUILDER) DOCKER=$(DOCKER) tools/buildx-ready.sh $(PLATFORMS))" || exit 1; \
	$(DOCKER) buildx build --builder "$$builder" $(VERSION_ARGS) \
	  --platform $(PLATFORMS) \
	  -f deploy/Dockerfile --target desktop \
	  -t $(REGISTRY_IMAGE):latest -t $(REGISTRY_IMAGE):$(next_version) \
	  -t $(REGISTRY_IMAGE):lite -t $(REGISTRY_IMAGE):$(next_version)-lite \
	  --push . || exit 1; \
	$(DOCKER) buildx build --builder "$$builder" $(VERSION_ARGS) \
	  --platform $(PLATFORMS) \
	  -f deploy/Dockerfile --target full \
	  -t $(REGISTRY_IMAGE):full -t $(REGISTRY_IMAGE):$(next_version)-full \
	  --push . || exit 1
	@echo "✓ pushed $(REGISTRY_IMAGE) :latest :lite :full and $(next_version){,-lite,-full}"

## release: binaries + checksums + GitHub Release (tag v<version>), via gh
release: release-binaries checksums
	@command -v gh >/dev/null 2>&1 || { echo "✗ gh CLI not installed/authenticated: https://cli.github.com"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "✗ uncommitted changes — commit before releasing:"; git status --short; exit 1; }
	gh release create v$(next_version) \
	  $(DIST)/$(BINARY)-v$(next_version)-linux-amd64 \
	  $(DIST)/$(BINARY)-v$(next_version)-linux-arm64 \
	  $(DIST)/SHA256SUMS.txt \
	  --title "SentinelDesk v$(next_version)" \
	  --notes "commit $(git_hash), built $(build_date)"
	@echo "✓ released v$(next_version)"

fmt:
	gofmt -w cmd internal pkg deploy test

vet:
	$(GO) vet ./...

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
