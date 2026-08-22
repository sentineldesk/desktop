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

// MCP_POLICY=approve — the level between the two answers safe and full force
// a deployment to choose from. What these tests hold up:
//
//   - approve ALLOWS at the policy gate what safe refuses; the difference is
//     the approval gate in handleToolCall, not a different refusal.
//   - every way the question can end without a yes is a no: a person
//     declining, nobody answering, nobody present, no room at all.
//   - a yes is on the record: the action log entry carries approved:true.
//   - Restrict cannot climb: approve is below full and above safe.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func approvePolicy(t *testing.T) *Policy {
	t.Helper()
	return &Policy{level: "approve", risk: buildRiskIndex(catalogue(t))}
}

func TestApproveAllowsWhatSafeRefuses(t *testing.T) {
	approve := approvePolicy(t)
	safe := &Policy{level: "safe", risk: approve.risk}

	for _, tool := range catalogue(t) {
		if got, why := approve.Allowed(tool.Name, nil); !got {
			t.Errorf("approve refused %s at the policy gate: %s — the approval "+
				"gate is where danger waits, not here", tool.Name, why)
		}
		wantApproval := tool.Risk == riskDanger
		if got := approve.RequiresApproval(tool.Name, nil); got != wantApproval {
			t.Errorf("RequiresApproval(%s) = %v, want %v (risk %s)",
				tool.Name, got, wantApproval, tool.Risk)
		}
		// The approval set is exactly what safe refuses by risk.
		if gotSafe, _ := safe.Allowed(tool.Name, nil); !gotSafe != wantApproval {
			t.Errorf("%s: safe refuses %v but approve gates %v — the two levels "+
				"disagree about what is dangerous", tool.Name, !gotSafe, wantApproval)
		}
	}

	// Escalation makes a harmless tool dangerous: as_root needs approval on a
	// tool whose own risk would not.
	if !approve.RequiresApproval("read_file", map[string]any{"as_root": true}) {
		t.Error("read_file with as_root:true escaped the approval gate")
	}
	if approve.RequiresApproval("read_file", map[string]any{"as_root": false}) {
		t.Error("read_file with as_root:false was gated for no reason")
	}
}

func TestRestrictCannotClimbThroughApprove(t *testing.T) {
	risk := buildRiskIndex(catalogue(t))

	// Down is allowed: a full daemon can hand out an approve connection.
	if got := (&Policy{level: "full", risk: risk}).Restrict("approve", "", ""); got.level != "approve" {
		t.Errorf("full restricted to approve gave %q", got.level)
	}
	// Up is not: an approve daemon asked for full stays approve.
	if got := (&Policy{level: "approve", risk: risk}).Restrict("full", "", ""); got.level != "approve" {
		t.Errorf("approve asked for full gave %q", got.level)
	}
	// And approve can still drop further.
	if got := (&Policy{level: "approve", risk: risk}).Restrict("readonly", "", ""); got.level != "readonly" {
		t.Errorf("approve restricted to readonly gave %q", got.level)
	}
}

// callDanger restricts the connection to approve and calls service_control —
// dangerous, and harmless to dispatch on a machine with no supervisor: the
// interesting part is whether the call REACHES dispatch, and the denial kind
// says so.
func callDanger(t *testing.T, room Rooms) (map[string]any, string) {
	t.Helper()
	s := testServer(t)
	if room != nil {
		s.SetRoom(room, "AI agent")
	}
	c := newSession(t, s)
	c.call("sentineldesk/policy", map[string]any{"level": "approve"})
	res := c.call("tools/call", map[string]any{
		"name": "service_control", "arguments": map[string]any{"action": "status"},
	})
	meta, _ := res["_meta"].(map[string]any)
	kind, _ := meta["sentineldesk/denial"].(string)
	return res, kind
}

// approvingRoom answers the room prompt the way a person clicking a button
// does, and remembers what was asked.
type approvingRoom struct{ *movableRoom }

func newApprovingRoom(reply string, err error) *approvingRoom {
	r := &approvingRoom{movableRoom: newMovableRoom("", "")}
	r.reply, r.replyErr = reply, err
	return r
}

// emptyRoom is a room with the lights on and nobody home.
type emptyRoom struct{ *movableRoom }

func (r *emptyRoom) HumansPresent() bool { return false }

func TestAnAllowedCallRunsAndTheTrailSaysWhoLetIt(t *testing.T) {
	room := newApprovingRoom("Allow", nil)
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.call("sentineldesk/policy", map[string]any{"level": "approve"})
	res := c.call("tools/call", map[string]any{
		"name": "service_control", "arguments": map[string]any{"action": "status"},
	})

	// The call may fail — there is no supervisor on a test machine — but it
	// must fail as a TOOL, past the approval gate, not as a refusal.
	meta, _ := res["_meta"].(map[string]any)
	if kind, _ := meta["sentineldesk/denial"].(string); kind == string(denialApproval) {
		t.Fatalf("an allowed call was refused as unapproved: %v", res)
	}

	// The person read the server's sentence, not the agent's: tool name and
	// arguments, composed in approveDanger.
	room.mu.Lock()
	asked, options := room.asked, room.askedOptions
	room.mu.Unlock()
	if !strings.Contains(asked, "service_control") {
		t.Errorf("the prompt does not name the tool: %q", asked)
	}
	if len(options) != 2 || options[0] != "Allow" || options[1] != "Deny" {
		t.Errorf("the prompt's options are %v, want [Allow Deny]", options)
	}

	// The yes is beside the act in the trail.
	entries := s.actions.Tail(0, "service_control")
	if len(entries) == 0 {
		t.Fatal("no action log entry for the approved call")
	}
	if !entries[len(entries)-1].Approved {
		t.Error("the trail does not record that a person approved this call")
	}
}

func TestADeclinedCallIsRefusedOnce(t *testing.T) {
	res, kind := callDanger(t, newApprovingRoom("Deny", nil))
	if kind != string(denialApproval) {
		t.Fatalf("a declined call came back as %q, want %q: %v", kind, denialApproval, res)
	}
	if text := fmt.Sprint(res["content"]); !strings.Contains(text, "declined") {
		t.Errorf("the refusal does not say a person declined: %v", text)
	}
}

func TestNoAnswerIsANo(t *testing.T) {
	res, kind := callDanger(t, newApprovingRoom("", errors.New("nobody answered in 1m0s")))
	if kind != string(denialApproval) {
		t.Fatalf("a timed-out approval came back as %q: %v", kind, res)
	}
}

func TestAnEmptyRoomIsANo(t *testing.T) {
	res, kind := callDanger(t, &emptyRoom{newMovableRoom("", "")})
	if kind != string(denialApproval) {
		t.Fatalf("an empty room came back as %q, want %q: %v", kind, denialApproval, res)
	}
	if text := fmt.Sprint(res["content"]); !strings.Contains(text, "nobody") {
		t.Errorf("the refusal does not say the room was empty: %v", text)
	}
}

func TestNoRoomAtAllIsANo(t *testing.T) {
	_, kind := callDanger(t, nil)
	if kind != string(denialApproval) {
		t.Fatalf("a roomless build under approve came back as %q, want %q", kind, denialApproval)
	}
}

// A read under approve costs nobody any attention: no prompt, no refusal.
func TestReadsAreNotGatedUnderApprove(t *testing.T) {
	room := newApprovingRoom("", errors.New("this prompt should never fire"))
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.call("sentineldesk/policy", map[string]any{"level": "approve"})
	res := c.call("tools/call", map[string]any{"name": "room_state", "arguments": map[string]any{}})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("room_state failed under approve: %v", res)
	}
	room.mu.Lock()
	asked := room.asked
	room.mu.Unlock()
	if asked != "" {
		t.Errorf("a read-only call raised an approval prompt: %q", asked)
	}
}
