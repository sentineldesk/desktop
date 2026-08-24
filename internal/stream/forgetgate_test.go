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

package stream

import "testing"

// TestWhoMayDeleteTheAgentsHistory pins a rule that is easy to get wrong in
// the direction that looks safer.
//
// The obvious gate — "you must be the controller" — refuses the commonest case
// there is: one person, alone in the room, with the controls sitting free
// because nothing has needed them. They press delete and are told to take
// controls they have no other use for, which reads as a broken button rather
// than as a rule. The rule that was actually wanted is narrower: not while
// somebody ELSE is driving.
func TestWhoMayDeleteTheAgentsHistory(t *testing.T) {
	for _, c := range []struct {
		name       string
		holder, me string
		privileged bool
		want       bool
	}{
		{"nobody is driving", "", "u1", false, true},
		{"I am driving", "u1", "u1", false, true},
		{"somebody else is driving", "u2", "u1", false, false},
		{"somebody else is driving, but I hold a privileged ticket", "u2", "u1", true, true},
		{"nobody is driving and I am privileged", "", "u1", true, true},
	} {
		if got := mayForget(c.holder, c.me, c.privileged); got != c.want {
			t.Errorf("%s: mayForget(%q, %q, %v) = %v, want %v",
				c.name, c.holder, c.me, c.privileged, got, c.want)
		}
	}
}
