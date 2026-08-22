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

// The conversation record: the half of ask_human that does NOT wait.
//
// What these cover is the difference between sending and displaying. A question
// answered in the agent's own terminal still has to reach the room so a chat
// panel can render it, and reaching the room must not be able to hurt the
// conversation it describes: it cannot block, it cannot fail the caller, and it
// cannot fall over on the one member guaranteed to be present — the agent,
// which has no Session at all. That last one is not hypothetical;
// delivery_test.go records the same nil dereference taking the whole daemon
// down once already.
//
// No WebRTC anywhere here, for the reason delivery_test.go gives: a Session
// with no data channel is a valid recipient that sends nothing, which is
// exactly the part under test. The send itself vanishes into a DataChannel a
// test cannot watch, so the assertions land on the two things that have gone
// wrong before and can be seen — which members were selected, and what the
// message says.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// chatRoom builds a room holding some browser members and optionally the agent,
// without NewRoom, which would try to open the X display for peer pointers.
func chatRoom(humans int, withAgent bool) *Room {
	r := &Room{members: map[string]*roomMember{}, controller: ControlFree}
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

// TestAgentChatSurvivesAMemberWithoutASession is the regression, stated first
// because it is the one that used to be fatal. The agent is always in the room
// when it is talking, and it never has a Session.
func TestAgentChatSurvivesAMemberWithoutASession(t *testing.T) {
	for _, tc := range []struct {
		name string
		room *Room
	}{
		{"agent alone", chatRoom(0, true)},
		{"agent and two browsers", chatRoom(2, true)},
		{"nobody at all", chatRoom(0, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A panic fails this by crashing it, which is the outcome wanted: the
			// assertion is not on a value, it is that the daemon survives writing
			// its own transcript.
			tc.room.AgentChat(ChatTurn{ID: "ask-1", Role: "agent",
				Via: "console", Text: "which invoice did you mean?"})

			for i, s := range tc.room.receivers() {
				if s == nil {
					t.Fatalf("receiver %d is nil — this is the dereference that "+
						"took the daemon down", i)
				}
			}
		})
	}
}

// TestAgentChatReachesEveryLiveSession. A record that went only to the
// controller would show the conversation to one person and hide it from
// everyone else watching the same desktop — and the controller is frequently
// the agent, which cannot read it at all.
func TestAgentChatReachesEveryLiveSession(t *testing.T) {
	r := chatRoom(3, true)

	got := len(r.receivers())
	if got != 3 {
		t.Fatalf("the record would reach %d members, want 3 (every browser, "+
			"not the agent)", got)
	}
	r.AgentChat(ChatTurn{ID: "ask-7", Role: "human", Via: "console",
		Text: "the second one"})

	// And with nobody in a browser it reaches nobody, which is the correct
	// outcome rather than a failure: a transcript exists to be read.
	if got := len(chatRoom(0, true).receivers()); got != 0 {
		t.Errorf("%d receivers in a room holding only the agent, want 0", got)
	}
}

// TestAgentChatWireShape pins what a browser actually parses, by encoding
// through the production path rather than a copy of it.
//
// The type is "agent_chat" and NOT "question", which is the entire point of the
// change: a client that has never heard of this ignores an unknown `t` and
// draws nothing, whereas reusing "question" would have popped the dialog this
// exists to stop — one with nothing waiting on it, so nothing would ever close
// it either.
func TestAgentChatWireShape(t *testing.T) {
	got := decodeTurn(t, ChatTurn{
		ID: "ask-3", Role: "agent", Via: "console",
		Text: "which invoice did you mean?", Options: []string{"first", "second"},
	})

	if got["t"] != AgentChatType {
		t.Errorf("type is %v, want %q", got["t"], AgentChatType)
	}
	if got["t"] == "question" {
		t.Error("the record reuses the type the ask dialog listens for; every " +
			"existing client would draw a modal nothing can close")
	}
	for field, want := range map[string]any{
		"id": "ask-3", "role": "agent", "via": "console",
		"text": "which invoice did you mean?",
	} {
		if got[field] != want {
			t.Errorf("%s is %v, want %v", field, got[field], want)
		}
	}
	opts, _ := got["options"].([]any)
	if len(opts) != 2 || opts[0] != "first" || opts[1] != "second" {
		t.Errorf("options came out as %v", got["options"])
	}
	if at, _ := got["at"].(float64); at <= 0 {
		t.Errorf("no usable timestamp on the record: %v", got)
	}
}

// TestAgentChatOmitsWhatIsNotThere. A free-text answer has no buttons, and a
// client checking `msg.options.length` should not have to know the difference
// between an absent field and a null one.
func TestAgentChatOmitsWhatIsNotThere(t *testing.T) {
	got := decodeTurn(t, ChatTurn{ID: "ask-4", Role: "human", Text: "yes"})
	if raw, present := got["options"]; present {
		t.Errorf("options is present as %v for a free-text turn", raw)
	}
	if raw, present := got["via"]; present {
		t.Errorf("via is present as %v when unset", raw)
	}
}

// TestAgentChatCarriesTheSystemRole. A question that timed out has no human
// turn, and a panel showing the question alone would leave the reader waiting
// for an answer that is never coming — the silent failure this project ranks
// above a crash.
func TestAgentChatCarriesTheSystemRole(t *testing.T) {
	got := decodeTurn(t, ChatTurn{ID: "ask-5", Role: "system", Via: "room",
		Text: "nobody answered in 2m0s"})
	if got["role"] != "system" {
		t.Errorf("role is %v, want system", got["role"])
	}
	if got["text"] != "nobody answered in 2m0s" {
		t.Errorf("the reason did not survive: %v", got["text"])
	}
}

// TestAgentChatDoesNotBlock is the property that makes this safe to call from
// the middle of a conversation. Notice has it for the same reason: what is being
// described has already happened, and holding the agent until somebody
// acknowledges the description would punish the person for reading it.
func TestAgentChatDoesNotBlock(t *testing.T) {
	r := chatRoom(4, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			r.AgentChat(ChatTurn{ID: "ask-1", Role: "agent", Text: "still here?"})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AgentChat blocked; it is supposed to be fire and forget")
	}
}

// TestAgentChatIsSafeWhileTheRoomChanges. The record is emitted from the
// goroutine handling a tool call while browsers join and leave on their own.
// Reading the member list needs the lock — the defect this change also fixed in
// Notice, which was snapshotting without one. Run under -race to mean anything.
func TestAgentChatIsSafeWhileTheRoomChanges(t *testing.T) {
	r := chatRoom(2, true)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			id := string(rune('m' + i%8))
			r.mu.Lock()
			r.members[id] = &roomMember{id: id, session: &Session{}}
			r.mu.Unlock()
			r.mu.Lock()
			delete(r.members, id)
			r.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			r.AgentChat(ChatTurn{ID: "ask-9", Role: "system",
				Text: "nobody answered"})
			r.Notice("credential", []string{"api key"})
		}
		close(stop)
	}()
	wg.Wait()
}

// --- what the record must NOT have changed ---------------------------------------

// TestAskStillRefusesAnEmptyRoomAtOnce.
//
// The record travels the same path as the question and it would have been easy
// to make asking succeed just because somebody could be told about it. Nobody in
// a browser is still nobody to ask, and it has to fail immediately rather than
// after the full timeout — the run that started all of this burned 120 seconds
// on a room where the refusal was the right answer from the first millisecond.
func TestAskStillRefusesAnEmptyRoomAtOnce(t *testing.T) {
	r := chatRoom(0, true)

	start := time.Now()
	answer, err := r.AskHuman("shall I carry on?", nil, 30*time.Second)
	took := time.Since(start)

	if err == nil {
		t.Fatalf("asking an empty room succeeded with %q", answer)
	}
	if took > 5*time.Second {
		t.Errorf("the refusal took %v; nobody being here is known immediately", took)
	}
}

// TestAskStillReportsATimeoutAsATimeout. A default returned here would make
// "nobody was looking" indistinguishable from "somebody chose this", which is
// the whole reason the tool exists.
func TestAskStillReportsATimeoutAsATimeout(t *testing.T) {
	// One browser member, which is alive as far as the room can tell — a Session
	// with no PeerConnection cannot be declared dead. That is precisely the
	// scenario behind the original defect: a tab open, nobody in front of it.
	r := chatRoom(1, true)

	answer, err := r.AskHuman("shall I delete it?", []string{"yes", "no"}, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("an unanswered question came back as the answer %q", answer)
	}
	if answer != "" {
		t.Errorf("a timeout carried an answer: %q", answer)
	}
	// And the room is left ready for the next question rather than stuck on the
	// one that expired.
	r.mu.RLock()
	stuck := r.asking != nil
	r.mu.RUnlock()
	if stuck {
		t.Error("the expired question is still holding the room's single slot")
	}
}

// decodeTurn runs a turn through the real encoder and hands back what a browser
// would see.
func decodeTurn(t *testing.T, turn ChatTurn) map[string]any {
	t.Helper()
	raw, err := turn.encode()
	if err != nil {
		t.Fatalf("the record does not marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the record is not JSON: %v\n%s", err, raw)
	}
	return out
}
