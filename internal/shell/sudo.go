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

package shell

// Privilege escalation used by the shell sessions.
//
// It lives here rather than in the MCP tools because opening a root terminal is
// a shell concern; the tools only expose it.

import (
	"os/exec"
	"sync"
)

var (
	sudoOnce sync.Once
	sudoOK   bool
)

// SudoAvailable reports whether this container can escalate without a password.
// The answer cannot change while the process runs, so it is resolved once.
func SudoAvailable() bool {
	sudoOnce.Do(func() {
		if _, err := exec.LookPath("sudo"); err != nil {
			return
		}
		// -n makes sudo fail instead of prompting on a terminal that is not there.
		sudoOK = exec.Command("sudo", "-n", "true").Run() == nil
	})
	return sudoOK
}
