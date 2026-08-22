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

import (
	"testing"
	"time"

	"github.com/sentineldesk/desktop/pkg/config"
)

// A departing agent has to give the controls back, and for a long time it did
// not. Room.LeaveAgent existed and released control correctly; nothing ever
// called it. An agent that took the desktop and then went away — the host
// quitting, `docker exec` killed, a crash mid-task — left it held by a process
// that no longer existed, and the only way to get it back was to restart the
// daemon. It was seen twice in a row before anybody looked at why.
//
// These tests are about the bookkeeping around that call rather than the call
// itself, because the bookkeeping is the part with a way to be subtly wrong:
// "the agent" is ONE identity shared by every MCP connection, so leaving on any
// close would evict an agent that another connection is still driving.

func roomServer(t *testing.T) (*Server, *movableRoom) {
	t.Helper()
	room := newMovableRoom(AgentID, "AI agent")
	s := NewServer(config.Config{Display: ":99"}, nil, nil, nil)
	s.room = room
	return s, room
}

// waitFor polls until cond holds, because the departure happens in serve()'s
// deferred call on its own goroutine — the close returns before it runs.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestClosingTheLastConnectionLeavesTheRoom(t *testing.T) {
	s, room := roomServer(t)
	c := newSession(t, s)

	// A call first, so the connection is genuinely serving rather than merely
	// accepted — the departure has to survive a connection that did some work.
	c.call("tools/list", nil)

	if got := room.leaveCount(); got != 0 {
		t.Fatalf("left the room %d times before the connection closed", got)
	}
	c.conn.Close()

	waitFor(t, "the agent to leave the room", func() bool { return room.leaveCount() == 1 })

	if ctl, _ := room.Controller(); ctl == AgentID {
		t.Fatal("the agent still holds control after its connection closed")
	}
}

// The one that matters. Two connections, one closes: the agent stays, because
// the other one may be mid-task and they are the same participant. Evicting on
// any close would take the controls away from a host that never disconnected.
func TestOneOfTwoConnectionsClosingKeepsTheAgentInTheRoom(t *testing.T) {
	s, room := roomServer(t)
	first := newSession(t, s)
	second := newSession(t, s)

	first.call("tools/list", nil)
	second.call("tools/list", nil)

	first.conn.Close()

	// Nothing to wait FOR here — the assertion is that something does not
	// happen — so give the deferred call a chance to run and be wrong.
	time.Sleep(150 * time.Millisecond)
	if got := room.leaveCount(); got != 0 {
		t.Fatalf("the agent left the room after 1 of 2 connections closed (%d times)", got)
	}
	if ctl, _ := room.Controller(); ctl != AgentID {
		t.Fatal("the agent lost control while a second connection was still open")
	}

	second.conn.Close()
	waitFor(t, "the agent to leave once the last connection closed",
		func() bool { return room.leaveCount() == 1 })
}
