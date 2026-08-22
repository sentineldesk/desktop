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

package mcp

import "testing"

// The sample is a real apt-get -s dist-upgrade transcript shape from Debian:
// the simulation banner, the prose apt prints for people, and the Inst/Conf
// lines the parser is actually after — including the two edge cases that
// matter: a package pulled in NEW (no [old version] bracket) and the trailing
// "[]" apt appends when a package's dependants are also on the list.
const aptSimulationSample = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Reading package lists...
Building dependency tree...
Calculating upgrade...
The following NEW packages will be installed:
  linux-image-6.1.0-22-amd64
The following packages will be upgraded:
  base-files libssl3 tzdata
3 upgraded, 1 newly installed, 0 to remove and 0 not upgraded.
Inst base-files [12.4+deb12u5] (12.4+deb12u6 Debian:12.6/stable [amd64])
Inst libssl3 [3.0.11-1~deb12u2] (3.0.13-1~deb12u1 Debian-Security:12/stable-security [amd64])
Inst tzdata [2024a-0+deb12u1] (2024b-0+deb12u1 Debian:12.7/stable [amd64]) []
Inst linux-image-6.1.0-22-amd64 (6.1.94-1 Debian-Security:12/stable-security [amd64])
Conf base-files (12.4+deb12u6 Debian:12.6/stable [amd64])
Conf libssl3 (3.0.13-1~deb12u1 Debian-Security:12/stable-security [amd64])
`

func TestTheAptSimulationIsReadCorrectly(t *testing.T) {
	pending, security := parseAptSimulation(aptSimulationSample)

	if len(pending) != 4 {
		t.Fatalf("parsed %d upgrades, want 4: %+v", len(pending), pending)
	}
	if security != 2 {
		t.Errorf("counted %d security updates, want 2", security)
	}

	// The ordinary case: name, both versions, not security.
	base := pending[0]
	if base.name != "base-files" || base.from != "12.4+deb12u5" || base.to != "12.4+deb12u6" || base.security {
		t.Errorf("base-files parsed as %+v", base)
	}

	// A security suite is recognised from the origin, not the version string.
	if ssl := pending[1]; ssl.name != "libssl3" || !ssl.security || ssl.to != "3.0.13-1~deb12u1" {
		t.Errorf("libssl3 parsed as %+v", ssl)
	}

	// A NEW package has no [old version]; that must read as empty, not as a
	// misparse that eats the new version.
	if kernel := pending[3]; kernel.from != "" || kernel.to != "6.1.94-1" || !kernel.security {
		t.Errorf("the newly-installed kernel parsed as %+v", kernel)
	}

	// Conf lines describe the same packages again and must not double-count.
	for _, u := range pending {
		if u.name == "" {
			t.Errorf("an upgrade with no name got through: %+v", u)
		}
	}
}

func TestNothingPendingParsesAsNothing(t *testing.T) {
	pending, security := parseAptSimulation(
		"Reading package lists...\nCalculating upgrade...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n")
	if len(pending) != 0 || security != 0 {
		t.Fatalf("an empty upgrade parsed as %d pending, %d security", len(pending), security)
	}
}
