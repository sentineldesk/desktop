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

package agentlink

// The one test that defends a deliberate duplication.
//
// This protocol has a twin in the agent repository, at
// cmd/sentineldesk-agent/chatwire.go, and it is a COPY on purpose: desktop and
// agent are separate repositories, so a shared Go package would couple them at
// compile time and an agent built today could no longer be pointed at a desktop
// built last month. See the note at the top of protocol.go.
//
// What a copy needs is something that fails when the two drift. Neither repo
// can import the other, so the shared thing is this string: the same fully
// populated message, marshalled, written out by hand in both places. Change a
// json tag on either side and one of the two tests goes red — which is the
// whole of the safety net, and is why the literal below must be edited only
// together with its twin.

import (
	"encoding/json"
	"testing"
)

// goldenWire is the shape both sides must produce. Its twin lives in
// TestTheWireShapeMatchesTheDesktop in the agent repository.
const goldenWire = `{"t":"say","chat":"c-1","text":"hello","protocol":1,` +
	`"version":"v","provider":"anthropic","model":"m","mode":"auto",` +
	`"ready":true,"why":"w","tool":"screenshot","detail":"d","turn":2,` +
	`"kind":"usage","elapsed_ms":1500,"cache_write":30,"cache_read":40,` +
	`"turns":3,"calls":4,"in_toks":5,"out_toks":6,"stopped_by":"s",` +
	`"session":7,"list":[{"id":8,"title":"t","turns":9,"at":"a","live":true}],` +
	`"messages":[{"role":"human","text":"x","at":"b"}],` +
	`"models":[{"provider":"anthropic","id":"m","note":"n"}],"format":"md","document":"d",` +
	`"term":"t1","bytes":"aGk=","cols":80,"rows":24,"remote":true,` +
	`"commands":[{"name":"/compact","kind":"compact","id":"cmd.compact","what":"w","local":true}],` +
	`"providers":["anthropic"],"all":true,"at":10}`

// TestTheWireShapeMatchesTheRuntime.
func TestTheWireShapeMatchesTheRuntime(t *testing.T) {
	raw, err := json.Marshal(Envelope{
		T: "say", Chat: "c-1", Text: "hello", Protocol: 1, Version: "v",
		Provider: "anthropic", Model: "m", Mode: "auto", Ready: true, Why: "w",
		Tool: "screenshot", Detail: "d", Turn: 2,
		Kind: "usage", ElapsedMS: 1500, CacheWrite: 30, CacheRead: 40,
		Turns: 3, Calls: 4, InToks: 5, OutToks: 6, StoppedBy: "s",
		Session:  7,
		List:     []SessionInfo{{ID: 8, Title: "t", Turns: 9, At: "a", Live: true}},
		Messages: []HistoryTurn{{Role: "human", Text: "x", At: "b"}},
		Format:   "md", Document: "d",
		Commands: []CommandInfo{{Name: "/compact", Kind: "compact", ID: "cmd.compact",
			What: "w", Local: true}},
		Models: []ModelInfo{{Provider: "anthropic", ID: "m", Note: "n"}},
		Term:   "t1", Bytes: "aGk=", Cols: 80, Rows: 24, Remote: true,
		Providers: []string{"anthropic"},
		All:       true,
		At:        10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != goldenWire {
		t.Errorf("the wire shape changed.\n got: %s\nwant: %s\n\n"+
			"If this was deliberate, the twin in the agent repository "+
			"(cmd/sentineldesk-agent/chatwire.go and its test) has to change with it.",
			raw, goldenWire)
	}
}

// TestProtocolIsPinned. The version is what turns a drift into a sentence at
// handshake instead of a field that silently reads as zero, so bumping it is a
// deliberate act on both sides rather than something a refactor can do.
func TestProtocolIsPinned(t *testing.T) {
	if Protocol != 1 {
		t.Errorf("the protocol version is %d; the agent repository has to agree", Protocol)
	}
}

// TestEveryMessageTypeIsSpelledOnce, because a `t` that differs by a character
// between the two sides is a message silently ignored rather than an error —
// the unknown-type rule that makes version skew survivable is the same rule
// that would hide a typo.
func TestEveryMessageTypeIsSpelledOnce(t *testing.T) {
	want := map[string]string{
		"welcome": TypeWelcome, "say": TypeSay, "cancel": TypeCancel,
		"sessions": TypeSessions, "transcript": TypeTranscript,
		"forget": TypeForget, "export": TypeExport, "progress": TypeProgress, "command": TypeCommand,
		"hello": TypeHello, "delta": TypeDelta, "step": TypeStep,
		"done": TypeDone, "failed": TypeFailed, "status": TypeStatus,
		"pty.open": TypePtyOpen, "pty.data": TypePtyData,
		"pty.resize": TypePtyResize, "pty.close": TypePtyClose,
	}
	for spelling, constant := range want {
		if spelling != constant {
			t.Errorf("the constant for %q holds %q", spelling, constant)
		}
	}
}
