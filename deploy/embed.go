// SentinelDesk
// A collaborative operating system for people and AI agents.
//
// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

// Package deploy embeds the distro's deployment tree into the binary itself.
//
// What is left of a once-larger idea, on purpose. The tree used to back a
// one-command installer for the standalone desktop (`-install`, install.sh);
// that mode was retired on 2026-08-14 — the front desk is the one way fleets
// are deployed, and a single docker run is the one way an individual desktop
// is. The embed stays because it still earns its place twice:
// launchers_test.go proves the desktop's configuration is really in the tree
// the image is built from, and a binary that carries its own deploy files can
// always say exactly what it was built with.
//
// packages/ is here because the Dockerfile reads the package lists at build
// time: a tree extracted without them has a Dockerfile that cannot build, and
// the failure arrives as a missing file three minutes into a build rather than
// as anything that names the cause.
//
// The front desk's own files (Dockerfile.server, its compose family) live in
// the panel's deploy/ with their own embed — a binary carries its own files
// and nobody else's.
package deploy

import "embed"

//go:embed config desktop packages
//go:embed Dockerfile
var FS embed.FS

// The compose file is NOT here, and cannot be: it lives at the repository root
// — one file, the same one somebody downloads and runs by hand — and go:embed
// refuses a path that climbs out of the package directory.
//
// There used to be a second, deploy-local one embedded here, and it was a copy
// that had already drifted: it never set MCP_SOCK, so a desktop started from it
// kept its socket inside the container where no agent could reach it. A tree
// extracted from this binary is meant to build the image; starting it is the
// root compose file's job, and it is fetched rather than unpacked.
