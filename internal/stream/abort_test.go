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

// The panic button, which has to work when everything else is going wrong.
//
// Two halves that are easy to build separately and useless separately: killing
// the running work stops what is happening now, taking the controls stops what
// happens next. An abort that only did the first leaves the agent free to start
// the same thing a second later, which is a pause it did not agree to rather
// than a stop.
//
// And it has to be pressable by somebody who is NOT driving, because the person
// watching an agent work is by definition not the one holding the controls. A
// stop only the controller can issue is not a stop, it is a privilege.

import (
	"sync"
	"testing"

	"github.com/sentineldesk/desktop/pkg/config"
)

func abortRoom(t *testing.T) *Room {
	t.Helper()
	r := &Room{
		cfg:      config.Config{MaxViewers: 4},
		members:  map[string]*roomMember{},
		bitrates: map[string]int{},
	}
	r.members["u1"] = &roomMember{id: "u1", name: "Ana"}
	r.members["u2"] = &roomMember{id: "u2", name: "Beto"}
	r.members[agentMemberID] = &roomMember{id: agentMemberID, name: "AI agent", agent: true}
	r.order = []string{"u1", "u2", agentMemberID}
	r.controller = agentMemberID
	return r
}

// agentMemberID is the room identity the MCP plane joins under. Named here so
// this test does not depend on the mcp package, which imports stream.
const agentMemberID = "agent"

func TestAbortTakesTheControlsFromTheAgent(t *testing.T) {
	r := abortRoom(t)
	if got, _ := r.Controller(); got != agentMemberID {
		t.Fatalf("the fixture does not start with the agent driving: %q", got)
	}

	// Beto is watching, not driving. That is the case that matters.
	r.Abort("u2")

	got, _ := r.Controller()
	if got == agentMemberID {
		t.Error("the agent still holds the controls after an abort — killing the " +
			"work without taking the wheel leaves it free to start again")
	}
	if got != "u2" {
		t.Errorf("controller is %q, want u2: whoever pressed it gets the desktop", got)
	}
}

func TestAnyoneMayPressIt(t *testing.T) {
	for _, who := range []string{"u1", "u2"} {
		r := abortRoom(t)
		r.Abort(who)
		if got, _ := r.Controller(); got != who {
			t.Errorf("%s pressed abort and did not get the controls (%q) — a stop "+
				"only the controller can issue is not a stop", who, got)
		}
	}
}

func TestAbortReachesItsListenersWithAName(t *testing.T) {
	r := abortRoom(t)

	var mu sync.Mutex
	var told []string
	stop := r.OnAbort(func(who string) {
		mu.Lock()
		defer mu.Unlock()
		told = append(told, who)
	})

	r.Abort("u1")

	mu.Lock()
	got := append([]string{}, told...)
	mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("listeners called %d times, want 1", len(got))
	}
	// The NAME, not the id. The warning is read by a person and by a model, and
	// neither of them knows what u1 is.
	if got[0] != "Ana" {
		t.Errorf("the listener was told %q, want Ana", got[0])
	}

	// Unsubscribing has to work, or a reconnecting client leaks a listener per
	// reconnect and every abort fans out to a growing list of dead ones.
	stop()
	r.Abort("u2")
	mu.Lock()
	defer mu.Unlock()
	if len(told) != 1 {
		t.Errorf("the listener fired after unsubscribing: %v", told)
	}
}

// TestAbortFromOutsideTheRoomStillStopsTheWork. The listener has to run even
// when the presser is not a member — the work is still running, and refusing to
// stop it because the caller could not be given the controls would be the wrong
// half to skip.
func TestAbortFromOutsideTheRoomStillStopsTheWork(t *testing.T) {
	r := abortRoom(t)
	fired := false
	r.OnAbort(func(string) { fired = true })

	r.Abort("somebody-who-left")

	if !fired {
		t.Error("nothing was stopped because the presser was not in the room")
	}
	if got, _ := r.Controller(); got != agentMemberID {
		t.Errorf("controller became %q — a non-member cannot be handed the "+
			"desktop, so the agent keeps it and only the work stops", got)
	}
}
