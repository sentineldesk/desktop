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

// Regressions for Delivery.Deliver across the four states the room can be in.
//
// Both defects these cover were live: a member without a Session took the whole
// daemon down, and (in the HTTP-ticket era these predate) one single-use ticket
// shared between browsers meant one download and the rest dead links — the
// distinct-id rule survives in the offers. Neither needed WebRTC to happen, and
// neither needs it to test — a Session with no data channel counts as a
// recipient and sends nothing, which is exactly the part under test.

import (
	"testing"
)

// room builds a room without going through NewRoom, which would try to open the
// X display for peer pointers. Members are added directly because Join wants a
// real Session with tracks, and none of that is what these tests are about.
func testRoom(controller string, humans int, withAgent bool) *Room {
	r := &Room{members: map[string]*roomMember{}, controller: controller}
	for i := 1; i <= humans; i++ {
		id := string(rune('a' + i - 1))
		r.members[id] = &roomMember{id: id, session: &Session{}}
		r.order = append(r.order, id)
	}
	if withAgent {
		// The agent as the room really holds it: a member with no Session.
		r.members[agentID] = &roomMember{id: agentID, agent: true}
		r.order = append(r.order, agentID)
	}
	return r
}

func testDelivery(t *testing.T, r *Room) *Delivery {
	t.Helper()
	return NewDelivery(NewFileServer(t.TempDir()), r)
}

// offers counts the channel-pull offers Deliver left across every session —
// what the ticket count used to measure before the web door closed.
func offers(r *Room) int {
	n := 0
	for _, m := range r.members {
		if m.session == nil {
			continue
		}
		m.session.transfersMu.Lock()
		n += len(m.session.deliveries)
		m.session.transfersMu.Unlock()
	}
	return n
}

func TestDeliverToHumanController(t *testing.T) {
	// "a" holds control with a second person watching: the file is theirs.
	r := testRoom("a", 2, true)
	d := testDelivery(t, r)
	if got := d.Deliver("/tmp/shot.png", ""); got != 1 {
		t.Fatalf("delivered to %d, want 1 (the controller)", got)
	}
	if n := offers(r); n != 1 {
		t.Errorf("minted %d offers, want 1", n)
	}
}

// TestDeliverWithAgentControlling is the panic. The agent holds the controls and
// has no Session, so a loop that sends to the controller dereferences nil.
func TestDeliverWithAgentControlling(t *testing.T) {
	r := testRoom(agentID, 2, true)
	d := testDelivery(t, r)
	got := d.Deliver("/tmp/shot.png", "")
	// The agent asked, but "download" can only mean the people watching.
	if got != 2 {
		t.Fatalf("delivered to %d, want 2 (both humans)", got)
	}
	if n := offers(r); n != 2 {
		t.Errorf("minted %d offers, want 2 — one per recipient", n)
	}
}

// TestDeliverWithControlFree is the same panic by an easier route, and the one
// the earlier audit missed: with no controller the loop reaches every member,
// so the agent only has to be present.
func TestDeliverWithControlFree(t *testing.T) {
	r := testRoom("", 3, true)
	d := testDelivery(t, r)
	if got := d.Deliver("/tmp/shot.png", ""); got != 3 {
		t.Fatalf("delivered to %d, want 3 (every browser)", got)
	}
	if n := offers(r); n != 3 {
		t.Errorf("minted %d offers, want 3 — one per recipient", n)
	}
}

// TestDeliverWithNobodyWatching covers the room holding only the agent. There is
// no browser to tell, so nothing should be offered: an offer nobody can pull
// would sit on a session map for the rest of its life.
func TestDeliverWithNobodyWatching(t *testing.T) {
	for _, tc := range []struct {
		name string
		room *Room
	}{
		{"agent alone, controlling", testRoom(agentID, 0, true)},
		{"agent alone, control free", testRoom("", 0, true)},
		{"empty room", testRoom("", 0, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDelivery(t, tc.room)
			if got := d.Deliver("/tmp/shot.png", ""); got != 0 {
				t.Errorf("delivered to %d, want 0", got)
			}
			if n := offers(tc.room); n != 0 {
				t.Errorf("minted %d offers with nobody watching, want 0", n)
			}
		})
	}
}

// TestDeliverOffersTheChannelPull pins the WebRTC half of a delivery: every
// recipient's session ends up holding an offer for the real path, under a
// distinct id — since the web door closed, the channel pull is the ONLY way
// a delivery reaches any client, panel and embedded alike.
func TestDeliverOffersTheChannelPull(t *testing.T) {
	r := testRoom("", 3, false)
	d := testDelivery(t, r)
	if got := d.Deliver("/tmp/shot.png", "shot.png"); got != 3 {
		t.Fatalf("delivered to %d, want 3", got)
	}
	seen := map[string]bool{}
	for id, m := range r.members {
		s := m.session
		s.transfersMu.Lock()
		offers := s.deliveries
		s.transfersMu.Unlock()
		if len(offers) != 1 {
			t.Fatalf("member %s holds %d offers, want 1", id, len(offers))
		}
		for oid, df := range offers {
			if seen[oid] {
				t.Errorf("offer id %s repeated across sessions — a shared id is the shared-ticket bug reborn", oid)
			}
			seen[oid] = true
			if df.path != "/tmp/shot.png" || df.name != "shot.png" {
				t.Errorf("member %s's offer is {%q %q}", id, df.path, df.name)
			}
		}
	}
}

func TestDeliverNilReceiver(t *testing.T) {
	var d *Delivery
	if got := d.Deliver("/tmp/shot.png", ""); got != 0 {
		t.Errorf("nil Delivery returned %d", got)
	}
}
