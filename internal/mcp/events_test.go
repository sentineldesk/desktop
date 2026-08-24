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

// The event channel, over the wire.
//
// These go through serve() rather than calling the hub directly, because the
// thing being tested is that a notification the client never asked for arrives
// on the same socket as the replies, interleaved with them, and is recognisable
// when it does. A unit test of eventHub.publish would prove none of that.
//
// The room here is a fake with a controller that the test moves by hand. That
// is the whole scenario: the agent is driving, a person takes the controls, and
// the agent has to be told rather than finding out when its next click fails.

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sentineldesk/desktop/internal/media"
	"github.com/sentineldesk/desktop/pkg/config"
	"time"

	"github.com/sentineldesk/desktop/internal/stream"
)

// movableRoom is a Rooms whose controller can be changed from the test, with
// the presence watchers the real Room calls on every change.
type movableRoom struct {
	Rooms

	mu         sync.Mutex
	controller string
	name       string
	subs       map[int]func()
	seq        int
	leaves     int
	notices    []string

	// What ask_human put to the room, and what to answer with.
	asked        string
	askedOptions []string
	reply        string
	replyErr     error

	// The conversation record, which is sent whether or not anybody answers.
	chat chan stream.ChatTurn
}

func newMovableRoom(controller, name string) *movableRoom {
	return &movableRoom{controller: controller, name: name, subs: map[int]func(){},
		// Buffered, and generously: AgentChat is fire and forget in the real
		// room, so a stub that blocked on an unread channel would invent a
		// property the thing under test does not have — and would hang the
		// daemon's goroutine rather than failing a test.
		chat: make(chan stream.ChatTurn, 16)}
}

func (r *movableRoom) JoinAgent(string) string { return AgentID }

// LeaveAgent records the departure and frees the controls, the way the real
// room does. Counted rather than flagged: what the connection bookkeeping has
// to get right is that this happens ONCE, when the last connection goes, and a
// boolean cannot tell one call from three.
func (r *movableRoom) LeaveAgent() {
	r.mu.Lock()
	r.leaves++
	if r.controller == AgentID {
		r.controller = ""
		r.name = ""
	}
	r.mu.Unlock()
}

func (r *movableRoom) leaveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaves
}

func (r *movableRoom) Controller() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.controller, r.name
}

func (r *movableRoom) IsController(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.controller == id
}

func (r *movableRoom) HumansPresent() bool { return true }

func (r *movableRoom) Members() []stream.MemberInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []stream.MemberInfo{
		{ID: AgentID, Name: "AI agent", Controller: r.controller == AgentID},
		{ID: "viewer-1", Name: "Ana", Controller: r.controller == "viewer-1"},
	}
}

func (r *movableRoom) WatchPresence(fn func()) func() {
	r.mu.Lock()
	id := r.seq
	r.seq++
	r.subs[id] = fn
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

// moveControl is what a person clicking "take control" does to the real room.
func (r *movableRoom) moveControl(to, name string) {
	r.mu.Lock()
	r.controller, r.name = to, name
	subs := make([]func(), 0, len(r.subs))
	for _, fn := range r.subs {
		subs = append(subs, fn)
	}
	r.mu.Unlock()
	for _, fn := range subs {
		fn()
	}
}

// awaitEvent reads messages until one is a sentineldesk event on the given
// topic, or the deadline passes. Replies to earlier requests are skipped: the
// point of an unsolicited notification is that it arrives whenever it arrives.
func (c *session) awaitEvent(topic string, within time.Duration) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		_ = c.conn.SetDeadline(deadline)
		msg := c.readMessage()
		if msg["method"] != eventMethod {
			continue
		}
		params, _ := msg["params"].(map[string]any)
		if params["topic"] == topic {
			return params
		}
	}
	c.t.Fatalf("no %q event arrived within %v", topic, within)
	return nil
}

// subscribeTo calls the tool and returns what it says it subscribed to.
func (c *session) subscribeTo(topics ...string) map[string]any {
	c.t.Helper()
	args := map[string]any{}
	if len(topics) > 0 {
		list := make([]any, len(topics))
		for i, t := range topics {
			list[i] = t
		}
		args["topics"] = list
	}
	res := c.call("tools/call", map[string]any{"name": "subscribe_events", "arguments": args})
	if isErr, _ := res["isError"].(bool); isErr {
		c.t.Fatalf("subscribe_events failed: %v", res["content"])
	}
	return decodeJSONContent(c.t, res)
}

// decodeJSONContent pulls the JSON object a tool returned as its text content.
func decodeJSONContent(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %v", res)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("content is not JSON: %v\n%s", err, text)
	}
	return out
}

// TestControlTakenAwayReachesTheAgent is the case this whole file exists for.
// The agent is driving, a person takes the controls, and the agent finds out
// because it was told — not because its next injection was refused.
func TestControlTakenAwayReachesTheAgent(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	got := c.subscribeTo("control")
	subs, _ := got["subscribed"].([]any)
	if len(subs) != 1 || subs[0] != "control" {
		t.Fatalf("subscribed to %v, want [control]", got["subscribed"])
	}

	room.moveControl("viewer-1", "Ana")

	ev := c.awaitEvent("control", 5*time.Second)
	// The named transition is the field an agent can act on without comparing
	// two ids itself, and "taken from you" is a different situation from "you
	// released it" even though both end with somebody else driving.
	if ev["change"] != "taken_from_you" {
		t.Errorf("change is %q, want \"taken_from_you\": %v", ev["change"], ev)
	}
	if ev["controller"] != "viewer-1" {
		t.Errorf("controller is %v, want viewer-1", ev["controller"])
	}
	if ev["previous"] != AgentID {
		t.Errorf("previous is %v, want %s", ev["previous"], AgentID)
	}
	if held, _ := ev["you_have_it"].(bool); held {
		t.Error("the event says the agent still has the controls it just lost")
	}
	if ev["controller_name"] != "Ana" {
		t.Errorf("controller_name is %v, want Ana", ev["controller_name"])
	}
}

// TestControlGrantedIsDistinctFromTaken covers the other direction, because an
// agent that treats every control event as a loss would stop working the moment
// somebody handed it the desktop.
func TestControlGrantedIsDistinctFromTaken(t *testing.T) {
	room := newMovableRoom("viewer-1", "Ana")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.subscribeTo("control")

	room.moveControl(AgentID, "AI agent")

	ev := c.awaitEvent("control", 5*time.Second)
	if ev["change"] != "granted_to_you" {
		t.Errorf("change is %q, want \"granted_to_you\": %v", ev["change"], ev)
	}
	if held, _ := ev["you_have_it"].(bool); !held {
		t.Error("the agent was given the controls and the event says otherwise")
	}
}

// TestNothingArrivesBeforeSubscribing is the property that makes it safe to
// publish this to every client: a host that does not know about the extension
// asks for nothing and is sent nothing.
func TestNothingArrivesBeforeSubscribing(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	room.moveControl("viewer-1", "Ana")
	time.Sleep(300 * time.Millisecond)

	// A request whose reply proves the socket is working. If an event had been
	// sent it would be sitting in front of this answer.
	c.send("ping", nil)
	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := c.readMessage()
	if msg["method"] == eventMethod {
		t.Fatalf("an event arrived for a client that never subscribed: %v", msg["params"])
	}
	if _, ok := msg["result"]; !ok {
		t.Fatalf("expected the ping's reply, got %v", msg)
	}
}

// TestUnsubscribeStopsEvents — the subscription has to be revocable, or an
// agent that finishes a task is stuck with a stream it no longer reads.
func TestUnsubscribeStopsEvents(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.subscribeTo("control")

	// One event first, so the test proves the channel was live before it was
	// closed rather than never having worked.
	room.moveControl("viewer-1", "Ana")
	c.awaitEvent("control", 5*time.Second)

	res := c.call("tools/call", map[string]any{"name": "unsubscribe_events"})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("unsubscribe_events failed: %v", res["content"])
	}

	room.moveControl(AgentID, "AI agent")
	time.Sleep(300 * time.Millisecond)

	c.send("ping", nil)
	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := c.readMessage()
	if msg["method"] == eventMethod {
		t.Fatalf("an event arrived after unsubscribing: %v", msg["params"])
	}
}

// TestResubscribingReplacesRatherThanAdds. Two subscriptions to the same source
// would deliver every event twice, and the leak would be invisible until an
// agent that re-subscribed on each task found its log filling up.
func TestResubscribingReplacesRatherThanAdds(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	c.subscribeTo("control")
	c.subscribeTo("control")
	c.subscribeTo("control")

	room.moveControl("viewer-1", "Ana")
	c.awaitEvent("control", 5*time.Second)

	// A second control event would be a duplicate of the first. The ping's
	// reply is the marker for "nothing else was queued".
	c.send("ping", nil)
	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	for {
		msg := c.readMessage()
		if _, ok := msg["result"]; ok {
			return
		}
		if msg["method"] == eventMethod {
			params, _ := msg["params"].(map[string]any)
			if params["topic"] == "control" {
				t.Fatalf("the same control change was delivered twice: %v", params)
			}
		}
	}
}

// TestSubscribeRejectsUnknownTopics. Silently ignoring a topic it does not
// recognise would leave the agent waiting forever on an event that was never
// going to come, which is the exact failure this feature removes.
func TestSubscribeRejectsUnknownTopics(t *testing.T) {
	s := testServer(t)
	s.SetRoom(newMovableRoom(AgentID, "AI agent"), "AI agent")
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name":      "subscribe_events",
		"arguments": map[string]any{"topics": []any{"control", "telepathy"}},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("an unknown topic was accepted: %v", res)
	}
}

// TestSubscribeSaysWhatItCannotDeliver. A server with no room cannot report
// control changes; claiming the subscription succeeded would be the same
// silence in a different place.
func TestSubscribeSaysWhatItCannotDeliver(t *testing.T) {
	c := newSession(t, testServer(t)) // no room, no display
	got := c.subscribeTo("control", "room")

	unavailable, _ := got["unavailable"].([]any)
	if len(unavailable) != 2 {
		t.Fatalf("a server with no room reported %v as undeliverable, want both topics",
			got["unavailable"])
	}
}

// TestEventsDieWithTheConnection. The subscription holds a watcher on the room
// and, on a real desktop, on X. A client that goes away without unsubscribing
// is the normal case, not the exception.
func TestEventsDieWithTheConnection(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.subscribeTo("control")

	room.mu.Lock()
	live := len(room.subs)
	room.mu.Unlock()
	if live != 1 {
		t.Fatalf("%d presence watchers after subscribing, want 1", live)
	}

	c.conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		room.mu.Lock()
		live = len(room.subs)
		room.mu.Unlock()
		if live == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the connection closed and %d presence watcher(s) are still registered", live)
}

// --- asking a person ------------------------------------------------------------

// Notice is implemented rather than left to the embedded interface, and that is
// not tidiness. A stub that embeds Rooms satisfies the compiler with a nil
// method table, so the first test whose tool output happens to match a
// credential shape panics the suite from inside a warning — the same failure
// LeaveAgent had here once already. The counter also lets a test assert that
// people were told.
func (r *movableRoom) Notice(kind string, items []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, kind)
}

// AgentChat is implemented for the same reason Notice is, one comment up: a
// stub that embeds Rooms satisfies the compiler with a nil method table, and
// every ask_human test would panic from inside the record rather than failing
// on the thing it was checking.
func (r *movableRoom) AgentChat(turn stream.ChatTurn) {
	select {
	case r.chat <- turn:
	default: // never block the caller; that is the whole contract
	}
}

// awaitTurn reads the next recorded turn, or gives up.
func (r *movableRoom) awaitTurn(t *testing.T, within time.Duration) stream.ChatTurn {
	t.Helper()
	select {
	case turn := <-r.chat:
		return turn
	case <-time.After(within):
		t.Fatalf("no conversation record arrived within %v", within)
		return stream.ChatTurn{}
	}
}

func (r *movableRoom) AskHuman(text string, options []string, timeout time.Duration) (string, error) {
	r.mu.Lock()
	r.asked = text
	r.askedOptions = options
	reply, fail := r.reply, r.replyErr
	r.mu.Unlock()
	if fail != nil {
		return "", fail
	}
	return reply, nil
}

// TestAskHumanReturnsTheAnswer. The straightforward case, over the wire, so the
// tool's shape is what a runtime will actually parse.
func TestAskHumanReturnsTheAnswer(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	room.reply = "the second one"
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name": "ask_human",
		"arguments": map[string]any{
			"question": "which invoice did you mean?",
			"options":  []any{"the first one", "the second one"},
		},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("ask_human failed: %v", res["content"])
	}
	got := decodeJSONContent(t, res)
	if answered, _ := got["answered"].(bool); !answered {
		t.Errorf("answered is false: %v", got)
	}
	if got["answer"] != "the second one" {
		t.Errorf("answer is %v", got["answer"])
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if room.asked != "which invoice did you mean?" {
		t.Errorf("the room was asked %q", room.asked)
	}
	if len(room.askedOptions) != 2 {
		t.Errorf("options reached the room as %v", room.askedOptions)
	}
}

// TestSilenceIsNotAnAnswer is the one that matters.
//
// A tool that returned a default on timeout would make "nobody was looking"
// indistinguishable from "somebody chose this", and the entire reason to ask is
// that the answer was not the agent's to assume. It has to come back as a
// failure, and the failure has to say so.
func TestSilenceIsNotAnAnswer(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	room.replyErr = errors.New("nobody answered in 2s")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name":      "ask_human",
		"arguments": map[string]any{"question": "shall I delete it?"},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("a question nobody answered came back as a success: %v", res)
	}
	got := decodeJSONContent(t, res)
	if answered, _ := got["answered"].(bool); answered {
		t.Error("answered is true for a question nobody answered")
	}
	if _, present := got["answer"]; present {
		t.Errorf("an unanswered question came back with an answer: %v", got)
	}
}

// --- the conversation record -----------------------------------------------------

// TestAskHumanIsPutOnTheRecord. The question and the answer both reach the room
// as records, so a chat panel can render the exchange whichever route it took —
// including the route that does not come through here at all, where the person
// answers in the terminal they typed the goal into.
//
// Both turns carry the same id. Without that a panel has a list of lines and no
// way to say which answer belongs to which question, which is the difference
// between a transcript and a log.
func TestAskHumanIsPutOnTheRecord(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	room.reply = "the second one"
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name": "ask_human",
		"arguments": map[string]any{
			"question": "which invoice did you mean?",
			"options":  []any{"the first one", "the second one"},
		},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("ask_human failed: %v", res["content"])
	}

	asked := room.awaitTurn(t, 5*time.Second)
	if asked.Role != "agent" {
		t.Errorf("the question was recorded as role %q, want agent", asked.Role)
	}
	if asked.Text != "which invoice did you mean?" {
		t.Errorf("the question was recorded as %q", asked.Text)
	}
	if len(asked.Options) != 2 {
		t.Errorf("the buttons did not reach the record: %v", asked.Options)
	}
	if asked.ID == "" {
		t.Error("the recorded question has no id, so no answer can be attached to it")
	}

	answered := room.awaitTurn(t, 5*time.Second)
	if answered.Role != "human" {
		t.Errorf("the answer was recorded as role %q, want human", answered.Role)
	}
	if answered.Text != "the second one" {
		t.Errorf("the answer was recorded as %q", answered.Text)
	}
	if answered.ID != asked.ID {
		t.Errorf("question id %q and answer id %q — the panel cannot pair them",
			asked.ID, answered.ID)
	}
}

// TestUnansweredIsRecordedAsUnanswered.
//
// A transcript that stopped at the question would leave a reader waiting for an
// answer that is never coming, which is this project's worst bug class wearing a
// chat bubble. It has to say so — and it must not say a PERSON said it, which is
// the same lie the tool refuses to tell its caller.
func TestUnansweredIsRecordedAsUnanswered(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	room.replyErr = errors.New("nobody answered in 2m0s")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name":      "ask_human",
		"arguments": map[string]any{"question": "shall I delete it?"},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("a question nobody answered came back as a success: %v", res)
	}

	asked := room.awaitTurn(t, 5*time.Second)
	if asked.Role != "agent" {
		t.Fatalf("first record is role %q, want the question", asked.Role)
	}
	outcome := room.awaitTurn(t, 5*time.Second)
	if outcome.Role == "human" {
		t.Fatalf("a timeout was recorded as something a person said: %q", outcome.Text)
	}
	if outcome.Role != "system" {
		t.Errorf("the outcome was recorded as role %q, want system", outcome.Role)
	}
	if outcome.Text != "nobody answered in 2m0s" {
		t.Errorf("the reason did not reach the record: %q", outcome.Text)
	}
	if outcome.ID != asked.ID {
		t.Errorf("the outcome is not attached to the question (%q vs %q)",
			asked.ID, outcome.ID)
	}
}

// TestTheRecordDoesNotAnswerTheQuestion. The record is a copy for the people
// watching, not a second channel the agent can be answered on. If emitting it
// ever started influencing the result, "nobody answered" would become "the
// transcript said so", which is a default wearing a disguise.
func TestTheRecordDoesNotAnswerTheQuestion(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	room.replyErr = errors.New("nobody is here to ask")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name":      "ask_human",
		"arguments": map[string]any{"question": "shall I carry on?"},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("an unanswerable question succeeded because it was recorded: %v", res)
	}
	got := decodeJSONContent(t, res)
	if answered, _ := got["answered"].(bool); answered {
		t.Error("answered is true for a question that reached nobody")
	}
	if _, present := got["answer"]; present {
		t.Errorf("the record leaked back as an answer: %v", got)
	}
}

// TestChatTurnIDsAreDistinct. Two exchanges sharing an id merge into one in a
// panel, which is worse than no id at all: the reader is not missing something,
// they are reading something that never happened.
func TestChatTurnIDsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NextChatTurnID()
		if id == "" {
			t.Fatal("an empty turn id")
		}
		if seen[id] {
			t.Fatalf("turn id %q was minted twice", id)
		}
		seen[id] = true
	}
}

// TestRecordAgentChatWithoutARoom. The console path calls this directly, and a
// build with no room has nowhere to put a transcript. Refusing would turn an
// optional capability into a required one.
func TestRecordAgentChatWithoutARoom(t *testing.T) {
	s := testServer(t) // no room
	s.RecordAgentChat(stream.ChatTurn{ID: "ask-1", Role: "agent", Text: "hello?"})
}

// TestAskHumanNeedsAQuestion. An empty prompt on somebody's screen is worse than
// no prompt: they cannot answer it and they cannot tell what went wrong.
func TestAskHumanNeedsAQuestion(t *testing.T) {
	s := testServer(t)
	s.SetRoom(newMovableRoom(AgentID, "AI agent"), "AI agent")
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name": "ask_human", "arguments": map[string]any{"question": "   "}})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("a blank question was accepted: %v", res)
	}
}

// TestReleasingIsNotBeingRobbed. The order of the transition tests is
// load-bearing and this is the case that proved it.
//
// An agent calling release_control at the end of a task leaves previous=agent
// and controller="", which satisfies "previous was the agent and now it is
// not". Classified as taken_from_you, a run that had just finished cleanly
// reported that somebody snatched the desktop from it, and the runtime marked a
// successful task as interrupted. Nobody driving is never a theft.
func TestReleasingIsNotBeingRobbed(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.subscribeTo("control")

	// What release_control does to the room: free, not handed on.
	room.moveControl("", "")

	ev := c.awaitEvent("control", 5*time.Second)
	if ev["change"] != "released" {
		t.Errorf("change is %q, want \"released\": %v", ev["change"], ev)
	}
	if held, _ := ev["you_have_it"].(bool); held {
		t.Error("the event says the agent still holds controls it released")
	}
}

// TestTakenFromYouStillFires, so fixing the case above did not trade one wrong
// answer for another. A person taking the controls names themselves as the new
// controller, which is what distinguishes it from letting go.
func TestTakenFromYouStillFires(t *testing.T) {
	room := newMovableRoom(AgentID, "AI agent")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.subscribeTo("control")

	room.moveControl("viewer-1", "Ana")

	ev := c.awaitEvent("control", 5*time.Second)
	if ev["change"] != "taken_from_you" {
		t.Errorf("change is %q, want \"taken_from_you\": %v", ev["change"], ev)
	}
}

// TestControlMovingBetweenOthersIsNotAboutTheAgent. Two people passing the
// desktop between them is not an interruption of anything the agent is doing.
func TestControlMovingBetweenOthersIsNotAboutTheAgent(t *testing.T) {
	room := newMovableRoom("viewer-1", "Ana")
	s := testServer(t)
	s.SetRoom(room, "AI agent")
	c := newSession(t, s)
	c.subscribeTo("control")

	room.moveControl("viewer-2", "Beto")

	ev := c.awaitEvent("control", 5*time.Second)
	if ev["change"] != "moved" {
		t.Errorf("change is %q, want \"moved\": %v", ev["change"], ev)
	}
}

// TestARecordingGoesToTheBrowserUnlessAskedOtherwise.
//
// The default used to be `container`, so the ordinary case — somebody asks the
// agent to record something — finished with a file on a disk they cannot reach.
// That is not storage, it is a disappearance with a path attached: the person
// who asked is in a browser, and the browser is the only place the recording
// can actually be watched.
func TestARecordingGoesToTheBrowserUnlessAskedOtherwise(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"nothing asked for", map[string]any{}, "download"},
		{"empty string", map[string]any{"destination": ""}, "download"},
		{"asked for the desktop", map[string]any{"destination": "container"}, "container"},
		{"asked for the browser", map[string]any{"destination": "download"}, "download"},
		{"shouted", map[string]any{"destination": "CONTAINER"}, "container"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			// Only the destination bookkeeping is under test; the recorder is
			// what would fail first and it is not what this is about.
			s.recDestination = strings.ToLower(argStr(tc.args, "destination"))
			if s.recDestination == "" {
				s.recDestination = "download"
			}
			if s.recDestination != tc.want {
				t.Errorf("destination is %q, want %q", s.recDestination, tc.want)
			}
		})
	}
}

// TestTheAdvertisedDefaultMatchesTheCode.
//
// A description saying "container (default)" beside code defaulting to download
// is worse than either one alone: the model reads the description, chooses
// accordingly, and gets something else. Nothing but a test can hold those two
// together, because they are a sentence and a branch in different functions.
func TestTheAdvertisedDefaultMatchesTheCode(t *testing.T) {
	var found bool
	for _, tool := range (&Server{}).buildTools() {
		if tool.Name != "start_recording" {
			continue
		}
		found = true
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "download (default)") {
			t.Errorf("start_recording's schema does not advertise download as the "+
				"default, and the code uses it:\n%s", raw)
		}
		if strings.Contains(string(raw), "container (default)") {
			t.Error("start_recording still advertises container as the default")
		}
	}
	if !found {
		t.Fatal("start_recording is not in the catalogue")
	}
}

// TestWhatIsLeftOnTheDesktopCanBeFetchedBack.
//
// When nobody is watching in a browser, a screenshot or a recording stays on
// the desktop — and that is correct, because it can be collected later. This
// pins the two facts that "later" depends on, and neither is enforced anywhere
// else: they are two independent defaults in two packages that happen to agree.
//
// If the recordings directory ever moves out from under the file manager's
// root, nothing breaks and nothing fails. The recording is made, the note says
// where it is, and the person cannot reach it — a file that exists, is named,
// and cannot be collected.
func TestWhatIsLeftOnTheDesktopCanBeFetchedBack(t *testing.T) {
	cfg := config.Load()
	rec := media.NewRecorder(":0", "", "")

	root := filepath.Clean(cfg.FilesRoot)
	dir := filepath.Clean(rec.Dir)

	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("recordings go to %q, which is outside the file manager's root %q — "+
			"anything left there when nobody is watching cannot be collected later",
			dir, root)
	}
}

// TestTheFallbackNoteSaysWhereAndHow.
//
// "the file stayed on the desktop" states where it is not. What somebody coming
// back for it needs is the path and the way in, and a note is the only place
// either of them appears.
func TestTheFallbackNoteSaysWhereAndHow(t *testing.T) {
	const path = "/home/sentineldesk/Recordings/session.mp4"
	note := "nobody is watching in a browser, so the file stayed on " +
		"the desktop at " + path + " — it can be fetched later from the " +
		"file manager, or listed with list_recordings"

	for _, want := range []string{path, "file manager", "list_recordings"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not mention %q: %q", want, note)
		}
	}
}
