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
//go:embed docker-compose.dev.yml
var FS embed.FS
