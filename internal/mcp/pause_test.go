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

// The asymmetry IS the pause.
//
// Holding everything would be a halt, and a halted agent cannot do the one
// thing a pause is usually for: look at what somebody wants to show it. Holding
// nothing would be a label. What makes it useful is the line between reading and
// changing, so that is what these assert — from both sides, because a gate that
// blocks too much fails as quietly as one that blocks too little.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAPausedAgentMayStillLook(t *testing.T) {
	s := testServer(t)
	s.PauseAll("Ana")
	c := newSession(t, s)

	// list_windows is riskRead. It needs no display to be REFUSED or not, which
	// is what this measures — the answer may well be an error about X, and an
	// error about X is not a refusal.
	raw, _ := json.Marshal(c.call("tools/call", map[string]any{
		"name": "list_windows", "arguments": map[string]any{}}))
	if strings.Contains(string(raw), "PAUSED") {
		t.Errorf("a read was refused while paused. An agent that cannot look is "+
			"not paused, it is deaf — and looking is usually why somebody "+
			"paused it:\n%s", raw)
	}
}

func TestAPausedAgentMayNotChangeAnything(t *testing.T) {
	s := testServer(t)
	s.PauseAll("Ana")
	c := newSession(t, s)

	raw, _ := json.Marshal(c.call("tools/call", map[string]any{
		"name": "write_file", "arguments": map[string]any{
			"path": "/tmp/should-not-happen", "content": "x"}}))
	body := string(raw)

	if !strings.Contains(body, "PAUSED") {
		t.Fatalf("a write went through while paused:\n%s", body)
	}
	// The wording carries the work. A refusal reads as "try another way" unless
	// it says otherwise, and another way is exactly what a paused agent must
	// not find.
	for _, want := range []string{"Ana", "Do not look for another way", "Wait"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, body)
		}
	}
	// And it has to say what IS still possible, or the pause reads as a dead end.
	if !strings.Contains(body, "READ everything") {
		t.Errorf("the refusal does not tell the agent it can still read:\n%s", body)
	}
}

func TestResumingLetsWritesThroughAgain(t *testing.T) {
	s := testServer(t)
	s.PauseAll("Ana")
	if _, held := s.paused(); !held {
		t.Fatal("PauseAll did not take")
	}
	s.ResumeAll("Ana")
	if by, held := s.paused(); held {
		t.Errorf("still paused by %q after resume", by)
	}

	c := newSession(t, s)
	raw, _ := json.Marshal(c.call("tools/call", map[string]any{
		"name": "write_file", "arguments": map[string]any{
			"path": t.TempDir() + "/ok", "content": "x"}}))
	if strings.Contains(string(raw), "PAUSED") {
		t.Errorf("still refusing after resume:\n%s", raw)
	}
}

// TestAForgottenPauseLiftsItself. Somebody pauses, closes the tab and goes to
// lunch. Without this the agent waits forever and the desktop looks broken
// rather than held — and "broken" is what gets a container restarted.
func TestAForgottenPauseLiftsItself(t *testing.T) {
	s := testServer(t)
	s.PauseAll("Ana")

	s.pause.mu.Lock()
	s.pause.since = time.Now().Add(-pauseExpiry - time.Minute)
	s.pause.mu.Unlock()

	if by, held := s.paused(); held {
		t.Errorf("an expired pause is still in force, held by %q", by)
	}
}

// TestResumingWhatWasNotPausedIsHarmless. Two people can press "let it go on"
// at the same moment, and a page that trusts its own button can send a resume
// for a pause that already ended.
func TestResumingWhatWasNotPausedIsHarmless(t *testing.T) {
	s := testServer(t)
	if woken := s.ResumeAll("Ana"); woken != 0 {
		t.Errorf("resuming an unpaused desktop woke %d jobs", woken)
	}
	if _, held := s.paused(); held {
		t.Error("resuming an unpaused desktop paused it")
	}
}

// TestAnAbortDuringAPauseIsDeliveredAsAnAbort. The two can be in force at once,
// and they say opposite things: pause means "wait, this is fine", abort means
// "this was wrong". The stronger statement has to win, or somebody who pressed
// the panic button gets told the agent is merely being held.
func TestAnAbortDuringAPauseIsDeliveredAsAnAbort(t *testing.T) {
	s := testServer(t)
	s.PauseAll("Ana")
	s.jobs.abortNote = buildAbortNote("Beto", nil)

	c := newSession(t, s)
	raw, _ := json.Marshal(c.call("tools/call", map[string]any{
		"name": "write_file", "arguments": map[string]any{
			"path": "/tmp/x", "content": "x"}}))
	body := string(raw)

	if !strings.Contains(body, "INTERRUPTED") {
		t.Errorf("the abort was masked by the pause:\n%s", body)
	}
	if strings.Contains(body, "PAUSED by") {
		t.Errorf("the pause answered instead of the abort:\n%s", body)
	}
}
