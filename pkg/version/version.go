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

// Package version carries what this binary IS: version, commit and build date.
//
// The values are stamped at link time with -ldflags -X, so they cannot drift
// from the code they describe — there is no version constant to forget to bump,
// and a binary found on a server months later can always say where it came
// from. The defaults below are what a plain `go build` produces: honest about
// being a development build rather than pretending to be a release.
package version

import "fmt"

var (
	Version   = "0.0.0"
	GitHash   = "development"
	BuildDate = "unknown"
)

// String is the one-line identity: "v1.2.3 (081b14d) · build 20260804-174100".
func String() string {
	return fmt.Sprintf("v%s (%s) · build %s", Version, GitHash, BuildDate)
}

// Short is what fits in a corner of the interface.
func Short() string {
	return fmt.Sprintf("v%s (%s)", Version, GitHash)
}
