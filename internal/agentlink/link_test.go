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

// These run against a real Unix socket with a fake runtime on the other end,
// because the properties worth pinning here are all about the socket: what
// happens before hello, what happens when the peer goes away, and what happens
// when two of them turn up. None of that is observable from a mock.

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sentineldesk/desktop/internal/stream"
)

// fakeRuntime is the far side of the wire: it dials, and it reads lines.
type fakeRuntime struct {
	conn net.Conn
	sc   *bufio.Scanner
}

func dial(t *testing.T, path string) *fakeRuntime {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("could not reach the daemon's socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	return &fakeRuntime{conn: conn, sc: sc}
}

func (f *fakeRuntime) send(t *testing.T, m Envelope) {
	t.Helper()
	raw, err := encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Write(raw); err != nil {
		t.Fatalf("could not write to the daemon: %v", err)
	}
}

// next reads one message, failing the test rather than hanging forever.
func (f *fakeRuntime) next(t *testing.T) Envelope {
	t.Helper()
	_ = f.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !f.sc.Scan() {
		t.Fatalf("nothing arrived from the daemon: %v", f.sc.Err())
	}
	var m Envelope
	if err := json.Unmarshal(f.sc.Bytes(), &m); err != nil {
		t.Fatalf("the daemon sent something unreadable: %v", err)
	}
	return m
}

// listening starts a Link on a socket in the test's own directory.
func listening(t *testing.T) (*Link, string, *watcher) {
	t.Helper()
	w := &watcher{}
	l := New(w.message, w.change)
	// A short directory: a Unix socket path has a hard length limit well below
	// PATH_MAX, and t.TempDir() under a long TMPDIR has overrun it here before.
	path := filepath.Join(t.TempDir(), "a.sock")
	if err := l.Listen(path); err != nil {
		t.Fatalf("could not open the socket: %v", err)
	}
	return l, path, w
}

// watcher records the callbacks so a test can assert on them.
type watcher struct {
	mu       sync.Mutex
	messages []Envelope
	statuses []Status
}

func (w *watcher) message(m Envelope) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = append(w.messages, m)
}

func (w *watcher) change(s Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statuses = append(w.statuses, s)
}

func (w *watcher) seen() ([]Envelope, []Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Envelope(nil), w.messages...), append([]Status(nil), w.statuses...)
}

// until polls a condition, so a test never sleeps a fixed amount and never
// hangs. The handshake crosses a goroutine boundary; the alternative is a
// sleep long enough to be slow and short enough to be flaky.
func until(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// TestAnAcceptedConnectionIsNotYetAnAgent is the distinction the whole status
// model rests on. A socket somebody opened is not a runtime that can answer,
// and reporting it as one gives the panel a chat box that fails on the first
// message.
func TestAnAcceptedConnectionIsNotYetAnAgent(t *testing.T) {
	l, path, _ := listening(t)
	dial(t, path)

	// Deliberately no hello. Give the accept goroutine time to have run, so
	// this is not passing merely because nothing has happened yet.
	time.Sleep(50 * time.Millisecond)

	if got := l.Status(); got.Connected {
		t.Errorf("a connection with no hello reads as connected: %+v", got)
	}
	if err := l.Send(Envelope{T: TypeSay, Text: "hello?"}); err != ErrNoAgent {
		t.Errorf("sending before hello gave %v, want ErrNoAgent", err)
	}
}

// TestHelloIsAnsweredAndMakesItAnAgent.
func TestHelloIsAnsweredAndMakesItAnAgent(t *testing.T) {
	l, path, w := listening(t)
	rt := dial(t, path)

	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Version: "test",
		Provider: "anthropic", Model: "claude-opus-5", Mode: "auto", Ready: true})

	if got := rt.next(t); got.T != TypeWelcome {
		t.Fatalf("hello was answered with %q, want %q", got.T, TypeWelcome)
	}
	until(t, "the status to settle", func() bool { return l.Status().Connected })

	got := l.Status()
	if !got.Ready || got.Model != "claude-opus-5" || got.Provider != "anthropic" {
		t.Errorf("the runtime introduced itself and the status says %+v", got)
	}
	if _, statuses := w.seen(); len(statuses) == 0 {
		t.Error("nobody was told the agent arrived")
	}
}

// TestANotReadyRuntimeAlwaysSaysWhy.
//
// A disabled chat box with no explanation is the shape of failure this project
// ranks below a crash: the person can see something is wrong and has nothing to
// act on. The daemon fills one in rather than trusting every runtime to.
func TestANotReadyRuntimeAlwaysSaysWhy(t *testing.T) {
	l, path, _ := listening(t)
	rt := dial(t, path)

	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: false})
	rt.next(t)
	until(t, "the status to settle", func() bool { return l.Status().Connected })

	if got := l.Status(); got.Why == "" {
		t.Error("a runtime that is not ready and did not say why left the panel with nothing to show")
	}
}

// TestTheNewestRuntimeWins. supervisord restarting the runtime can have the new
// process connected before the old one's socket is reaped. Refusing the second
// would leave the chat dead with no error and no way out but another restart.
func TestTheNewestRuntimeWins(t *testing.T) {
	l, path, _ := listening(t)

	first := dial(t, path)
	first.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: true, Model: "old"})
	first.next(t)
	until(t, "the first runtime", func() bool { return l.Status().Model == "old" })

	second := dial(t, path)
	second.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: true, Model: "new"})
	second.next(t)
	until(t, "the second runtime", func() bool { return l.Status().Model == "new" })

	// And the loser cannot overwrite the winner's status on its way out.
	//
	// The write is EXPECTED to fail — the daemon closed that connection when it
	// accepted the second — and that is the stronger guarantee, so the error is
	// tolerated rather than asserted either way. What must hold is the status,
	// which is what a dying runtime could otherwise drag down with it.
	raw, err := encode(Envelope{T: TypeStatus, Ready: false, Model: "old", Why: "dying"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = first.conn.Write(raw)

	time.Sleep(50 * time.Millisecond)
	if got := l.Status(); got.Model != "new" || !got.Ready {
		t.Errorf("the replaced runtime changed the live one's status: %+v", got)
	}
}

// TestADisconnectIsReported, because everything downstream of it — ending the
// exchanges left hanging, telling the browsers — happens on this callback.
func TestADisconnectIsReported(t *testing.T) {
	l, path, w := listening(t)
	rt := dial(t, path)
	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: true})
	rt.next(t)
	until(t, "the agent to arrive", func() bool { return l.Status().Connected })

	_ = rt.conn.Close()
	until(t, "the agent to be reported gone", func() bool { return !l.Status().Connected })

	_, statuses := w.seen()
	if last := statuses[len(statuses)-1]; last.Connected {
		t.Errorf("the last status reported was %+v, want a disconnection", last)
	}
	if err := l.Send(Envelope{T: TypeSay, Text: "still there?"}); err != ErrNoAgent {
		t.Errorf("sending after the runtime left gave %v, want ErrNoAgent", err)
	}
}

// TestAnUnreadableLineDoesNotEndTheSession. A single malformed message is more
// often a version skew than a broken peer, and dropping the connection would
// turn a cosmetic mismatch into a dead chat panel.
func TestAnUnreadableLineDoesNotEndTheSession(t *testing.T) {
	l, path, w := listening(t)
	rt := dial(t, path)
	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: true})
	rt.next(t)
	until(t, "the agent to arrive", func() bool { return l.Status().Connected })

	if _, err := rt.conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	rt.send(t, Envelope{T: TypeDelta, Chat: "c-1", Text: "still here"})

	until(t, "the message after the bad line", func() bool {
		msgs, _ := w.seen()
		for _, m := range msgs {
			if m.T == TypeDelta && m.Text == "still here" {
				return true
			}
		}
		return false
	})
	if !l.Status().Connected {
		t.Error("one unreadable line took the runtime offline")
	}
}

// TestSocketPathIsDerivedFromTheMcpOne.
//
// The derivation is the point: whoever relocates MCP_SOCK into a shared volume
// is relocating "where this container's sockets live", and a second variable
// they must remember is how the two end up in different places — one of them
// inside a volume the runtime cannot see.
func TestSocketPathIsDerivedFromTheMcpOne(t *testing.T) {
	for _, tc := range []struct{ agent, mcp, want string }{
		{"", "/run/sentineldesk/mcp.sock", "/run/sentineldesk/sentineldesk-agent.sock"},
		{"", "/run/user/1000/sentineldesk-mcp.sock", "/run/user/1000/sentineldesk-agent.sock"},
		{"/tmp/mine.sock", "/run/sentineldesk/mcp.sock", "/tmp/mine.sock"},
		// No MCP plane means no agent plane, which is the correct reading of a
		// daemon that was never given one rather than a default to invent.
		{"", "", ""},
		{"  ", "  ", ""},
	} {
		if got := SocketPath(tc.agent, tc.mcp); got != tc.want {
			t.Errorf("SocketPath(%q, %q) = %q, want %q", tc.agent, tc.mcp, got, tc.want)
		}
	}
}

// panelSpy records what the room would have been told.
type panelSpy struct {
	mu      sync.Mutex
	status  []stream.AgentAvailability
	turns   []stream.ChatTurn
	ends    []stream.AgentEnd
	deltas  []string
	steps   []string
	exports []string
	console []string
}

func (p *panelSpy) AgentStatus(a stream.AgentAvailability) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status = append(p.status, a)
}
func (p *panelSpy) AgentChat(t stream.ChatTurn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns = append(p.turns, t)
}
func (p *panelSpy) AgentDelta(chat, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deltas = append(p.deltas, text)
}
func (p *panelSpy) AgentStep(chat, tool, detail string, turn int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = append(p.steps, tool+" "+detail)
}
func (p *panelSpy) AgentHistory(session int, chat string, list []stream.AgentSession, messages []stream.AgentHistoryTurn) {
}
func (p *panelSpy) AgentConsole(memberID, term, bytes string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.console = append(p.console, memberID+":"+term+":"+bytes)
}
func (p *panelSpy) AgentConsoleClosed(memberID, term, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.console = append(p.console, "closed:"+memberID+":"+term)
}
func (p *panelSpy) AgentExport(session int, format, document string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exports = append(p.exports, document)
}
func (p *panelSpy) AgentEnd(e stream.AgentEnd) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ends = append(p.ends, e)
}

func (p *panelSpy) read() ([]stream.AgentAvailability, []stream.ChatTurn, []stream.AgentEnd) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]stream.AgentAvailability(nil), p.status...),
		append([]stream.ChatTurn(nil), p.turns...),
		append([]stream.AgentEnd(nil), p.ends...)
}

// TestWhatSomebodyTypedSurvivesAnAbsentRuntime.
//
// The worst failure available here is a person's own words disappearing because
// the agent was not running: they cannot tell whether it was sent, so they type
// it again. The transcript records what was said; whether it was answered is a
// separate message.
func TestWhatSomebodyTypedSurvivesAnAbsentRuntime(t *testing.T) {
	spy := &panelSpy{}
	c := NewChat(spy)

	chat, err := c.Send("", "someone", "open the browser")
	if err == nil {
		t.Fatal("sending with no runtime succeeded")
	}
	if chat == "" {
		t.Error("no conversation id came back, so the panel cannot place the failure")
	}
	_, turns, _ := spy.read()
	if len(turns) != 1 || turns[0].Text != "open the browser" || turns[0].Role != "human" {
		t.Errorf("the transcript holds %+v, want the person's own message", turns)
	}
}

// TestAnAbsentRuntimeIsToldApartFromAnUnconfiguredOne.
//
// They need different remedies — one is a download, the other is a command —
// and offering the wrong one wastes somebody's time in the most annoying way
// available: telling them to install what they already have.
func TestAnAbsentRuntimeIsToldApartFromAnUnconfiguredOne(t *testing.T) {
	c := NewChat(&panelSpy{})

	// Nothing connected.
	gone := c.Availability()
	if gone.Present || gone.Ready || gone.Remedy == "" {
		t.Errorf("with no runtime the panel is told %+v", gone)
	}

	// Connected, no model. Forced through the link's own state rather than set
	// by hand, so this asserts the production path.
	path := filepath.Join(t.TempDir(), "a.sock")
	if err := c.Listen(path); err != nil {
		t.Fatal(err)
	}
	rt := dial(t, path)
	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: false,
		Why: "no model is configured"})
	rt.next(t)
	until(t, "the runtime to register", func() bool { return c.Availability().Present })

	half := c.Availability()
	if !half.Present || half.Ready {
		t.Fatalf("a runtime with no model reads as %+v", half)
	}
	if half.Reason == "" || half.Remedy == "" {
		t.Errorf("nothing actionable was offered: %+v", half)
	}
	if half.Remedy == gone.Remedy {
		t.Errorf("both states offer the same remedy %q", half.Remedy)
	}
}

// TestARuntimeThatDiesMidAnswerEndsTheExchange.
//
// Otherwise the panel spins forever on an answer that is never coming — a
// silent non-outcome, which this codebase ranks as worse than a crash because
// nothing anywhere says something went wrong.
func TestARuntimeThatDiesMidAnswerEndsTheExchange(t *testing.T) {
	spy := &panelSpy{}
	c := NewChat(spy)
	path := filepath.Join(t.TempDir(), "a.sock")
	if err := c.Listen(path); err != nil {
		t.Fatal(err)
	}

	rt := dial(t, path)
	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: true})
	rt.next(t)
	until(t, "the runtime to register", func() bool { return c.Availability().Ready })

	chat, err := c.Send("", "someone", "do something slow")
	if err != nil {
		t.Fatalf("the send failed: %v", err)
	}
	rt.send(t, Envelope{T: TypeDelta, Chat: chat, Text: "working on"})
	_ = rt.conn.Close()

	until(t, "the exchange to be closed", func() bool {
		_, _, ends := spy.read()
		return len(ends) > 0
	})
	_, _, ends := spy.read()
	if ends[0].Chat != chat || ends[0].Ok || ends[0].Text == "" {
		t.Errorf("the exchange ended as %+v, want a failure naming the reason", ends[0])
	}
}

// TestAFinishedAnswerLandsOnTheTranscript, so a panel opened after the run —
// or reloaded during it — has the whole reply rather than only the fragments it
// was present for.
func TestAFinishedAnswerLandsOnTheTranscript(t *testing.T) {
	spy := &panelSpy{}
	c := NewChat(spy)
	c.fromRuntime(Envelope{T: TypeDone, Chat: "c-1", Text: "done, the browser is open",
		Turns: 3, Calls: 2})

	_, turns, ends := spy.read()
	if len(turns) != 1 || turns[0].Role != "agent" || turns[0].ID != "c-1" {
		t.Errorf("the transcript holds %+v, want the agent's answer", turns)
	}
	if len(ends) != 1 || !ends[0].Ok || ends[0].Turns != 3 {
		t.Errorf("the exchange closed as %+v", ends)
	}
}

// TestAModelConfiguredLaterReachesThePanel is the other half of a dead end.
//
// The panel disables its composer when the runtime reports no model —
// correctly, since there is nowhere for a message to go. The way out is
// `/connect` in a terminal, which writes the runtime's config file from ANOTHER
// process. The runtime notices and sends a status; this is the daemon's side of
// that, and without it the panel stays disabled forever, waiting for a message
// nobody is allowed to type.
func TestAModelConfiguredLaterReachesThePanel(t *testing.T) {
	spy := &panelSpy{}
	c := NewChat(spy)
	path := filepath.Join(t.TempDir(), "a.sock")
	if err := c.Listen(path); err != nil {
		t.Fatal(err)
	}

	rt := dial(t, path)
	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: false,
		Why: "no model is configured"})
	rt.next(t)
	until(t, "the runtime to register", func() bool { return c.Availability().Present })

	if got := c.Availability(); got.Ready {
		t.Fatalf("a runtime with no model reads as ready: %+v", got)
	}

	// Somebody ran /connect somewhere else.
	rt.send(t, Envelope{T: TypeStatus, Ready: true,
		Provider: "anthropic", Model: "claude-opus-5"})

	until(t, "the panel to be told", func() bool { return c.Availability().Ready })

	got := c.Availability()
	if got.Model != "claude-opus-5" || got.Reason != "" || got.Remedy != "" {
		t.Errorf("after a model was configured the panel is told %+v; it should "+
			"have nothing left to remedy", got)
	}

	// And the room was told, not just the daemon's own state updated — the
	// broadcast is what actually re-enables somebody's composer.
	status, _, _ := spy.read()
	if len(status) == 0 || !status[len(status)-1].Ready {
		t.Errorf("the room's last status was %+v, want a ready one", status)
	}
}

// TestAModelGoingAwayAlsoReachesThePanel. The reverse is the same mechanism and
// the same stake: a key revoked mid-session must disable the box rather than
// leave somebody typing into a runtime that will refuse every message.
func TestAModelGoingAwayAlsoReachesThePanel(t *testing.T) {
	spy := &panelSpy{}
	c := NewChat(spy)
	path := filepath.Join(t.TempDir(), "a.sock")
	if err := c.Listen(path); err != nil {
		t.Fatal(err)
	}

	rt := dial(t, path)
	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: true,
		Provider: "anthropic", Model: "claude-opus-5"})
	rt.next(t)
	until(t, "the runtime to register", func() bool { return c.Availability().Ready })

	rt.send(t, Envelope{T: TypeStatus, Ready: false, Why: "the key was rejected"})
	until(t, "the panel to be told", func() bool { return !c.Availability().Ready })

	got := c.Availability()
	if !got.Present {
		t.Error("a runtime that lost its model reads as absent; it is still running")
	}
	if got.Reason == "" || got.Remedy == "" {
		t.Errorf("nothing actionable was offered: %+v", got)
	}
}

// --- what a browser is shown of the runtime's narration ------------------------

// TestProseIsNotDrawnTwice.
//
// A runtime sends its whole narration as progress AND sends delta and step for
// the two kinds a desktop has always understood, so that either side can be
// upgraded first. Acting on both would put every fragment of every answer into
// the panel twice — which is exactly the bug the panel already had once, from a
// different cause, and it took somebody staring at two identical bubbles to
// find it.
func TestProseIsNotDrawnTwice(t *testing.T) {
	panel := &panelSpy{}
	c := &Chat{panel: panel}

	// What a runtime actually sends for one turn of prose and one tool call.
	c.fromRuntime(Envelope{T: TypeProgress, Chat: "c-1", Kind: "text", Detail: "hello"})
	c.fromRuntime(Envelope{T: TypeDelta, Chat: "c-1", Text: "hello"})
	c.fromRuntime(Envelope{T: TypeProgress, Chat: "c-1", Kind: "call", Tool: "screenshot"})
	c.fromRuntime(Envelope{T: TypeStep, Chat: "c-1", Tool: "screenshot"})

	panel.mu.Lock()
	defer panel.mu.Unlock()
	if len(panel.deltas) != 1 {
		t.Errorf("the panel was given %d copies of one answer: %v", len(panel.deltas), panel.deltas)
	}
	if len(panel.steps) != 1 {
		t.Errorf("the panel was given %d copies of one tool call: %v", len(panel.steps), panel.steps)
	}
}

// TestTheBrowserSeesWhatCommandsDid.
//
// Before this, the answers to `/compact` and `/memory` existed only for faces
// attached to the runtime directly — so typing `/compact` in a browser would
// have worked and said nothing, which this project treats as worse than not
// working at all.
func TestTheBrowserSeesWhatCommandsDid(t *testing.T) {
	panel := &panelSpy{}
	c := &Chat{panel: panel}

	for _, kind := range []string{"note", "error", "compacting", "compacted", "widened", "interrupted", "thinking"} {
		c.fromRuntime(Envelope{T: TypeProgress, Chat: "c-1", Kind: kind, Detail: "something"})
	}

	panel.mu.Lock()
	defer panel.mu.Unlock()
	if len(panel.steps) != 7 {
		t.Errorf("the panel was shown %d of 7 things that happened: %v", len(panel.steps), panel.steps)
	}
}

// TestTheTerminalsArithmeticStaysInTheTerminal.
//
// Turn boundaries and per-turn token counts are what a session strip is built
// from, and a panel has no row for any of them — it shows the totals once, in
// its footer, from agent_end. Forwarding them would be a line of noise per turn
// in a transcript somebody is reading.
//
// `result` is deliberately NOT in this list, and used to be. See the next test.
func TestTheTerminalsArithmeticStaysInTheTerminal(t *testing.T) {
	panel := &panelSpy{}
	c := &Chat{panel: panel}

	for _, kind := range []string{"turn", "usage", "done"} {
		c.fromRuntime(Envelope{T: TypeProgress, Chat: "c-1", Kind: kind, Detail: "x"})
	}
	// And a progress frame with no kind at all, which is a runtime sending
	// something this build cannot place.
	c.fromRuntime(Envelope{T: TypeProgress, Chat: "c-1"})

	panel.mu.Lock()
	defer panel.mu.Unlock()
	if len(panel.steps) != 0 {
		t.Errorf("the panel was shown the terminal's bookkeeping: %v", panel.steps)
	}
}

// TestTheBrowserSeesWhatToolsAnswered.
//
// A tool's RESULT reaches the panel, and for a while it did not — a defensible
// reading of "the panel shows what the agent did" that left a column of tool
// names with no way to see what any of them returned.
//
// A trace you cannot read the answers out of is a list. The whole reason to
// watch an agent work is to see what it found, and "it called browser_element"
// without the answer says nothing about whether the video had ended.
func TestTheBrowserSeesWhatToolsAnswered(t *testing.T) {
	panel := &panelSpy{}
	c := &Chat{panel: panel}

	c.fromRuntime(Envelope{T: TypeProgress, Chat: "c-1", Kind: "call",
		Tool: "browser_element", Detail: "selector=video"})
	c.fromRuntime(Envelope{T: TypeStep, Chat: "c-1", Tool: "browser_element"})
	c.fromRuntime(Envelope{T: TypeProgress, Chat: "c-1", Kind: "result",
		Tool: "browser_element", Detail: `{"ended":true}`})

	panel.mu.Lock()
	defer panel.mu.Unlock()
	var sawAnswer bool
	for _, s := range panel.steps {
		if strings.Contains(s, "ended") {
			sawAnswer = true
		}
	}
	if !sawAnswer {
		t.Errorf("the panel was shown the calls and not their answers: %v", panel.steps)
	}
}

// --- a terminal, and who it belongs to ------------------------------------------

// TestAConsoleIsAlwaysOpenedAsRemote.
//
// The single most important line in this file, and the one somebody
// simplifying would delete as redundant.
//
// A pseudo-terminal runs on the RUNTIME's machine, so everything typed into it
// arrives over the runtime's own local socket and looks exactly like a person
// at that keyboard — which is the test that decides whether an API key may be
// stored. Every console opened through this daemon was opened for a browser,
// and saying so is what stops the terminal being a back door through the one
// security rule this system has.
func TestAConsoleIsAlwaysOpenedAsRemote(t *testing.T) {
	c := NewChat(&panelSpy{})
	path := filepath.Join(t.TempDir(), "a.sock")
	if err := c.Listen(path); err != nil {
		t.Fatal(err)
	}
	rt := dial(t, path)
	rt.send(t, Envelope{T: TypeHello, Protocol: Protocol, Ready: true})
	rt.next(t)
	until(t, "the runtime to register", func() bool { return c.Availability().Ready })

	if err := c.ConsoleOpen("u1", "t1", 100, 30); err != nil {
		t.Fatalf("%v", err)
	}
	got := rt.next(t)
	if got.T != TypePtyOpen {
		t.Fatalf("opening a console sent %q", got.T)
	}
	if !got.Remote {
		t.Fatal("a console opened for a browser was not marked remote — " +
			"a key pasted into it would be stored")
	}
	if got.Cols != 100 || got.Rows != 30 {
		t.Errorf("the console was opened at %dx%d", got.Cols, got.Rows)
	}
}

// TestAConsoleWithNoIdIsRefused, because its bytes would come back addressed to
// nobody and be dropped, which looks exactly like a terminal that will not open.
func TestAConsoleWithNoIdIsRefused(t *testing.T) {
	c := NewChat(&panelSpy{})
	if err := c.ConsoleOpen("u1", "  ", 80, 24); err == nil {
		t.Fatal("a console with no id was opened")
	}
}

// TestAConsolesBytesGoToTheBrowserThatOpenedIt.
//
// Everything else on this plane is broadcast, deliberately. A terminal is the
// exception, and the line is between what it SAYS and what it SHOWS: anything
// typed into it that reaches the agent comes back as ordinary chat and fans out
// to everyone, while the keystrokes and the half-typed lines are a person
// working rather than a decision the room needs.
func TestAConsolesBytesGoToTheBrowserThatOpenedIt(t *testing.T) {
	spy := &panelSpy{}
	c := NewChat(spy)
	// Remembered on the way out even though there is nothing to send to: the
	// mapping is this process's own bookkeeping, and a console whose open never
	// reached the runtime still has an owner for the error that comes back.
	_ = c.ConsoleOpen("u1", "t1", 80, 24)
	c.fromRuntime(Envelope{T: TypePtyData, Term: "t1", Bytes: "aGk="})

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.console) != 1 || !strings.HasPrefix(spy.console[0], "u1:t1:") {
		t.Errorf("the bytes were addressed to %v, want the browser that opened it", spy.console)
	}
}

// TestAConsoleFromNobodyIsAddressedToNobody.
//
// What a runtime that restarted under an open terminal looks like: bytes for a
// session this process never opened. Addressed to "", which the room drops —
// rather than guessing an owner and putting one person's terminal in front of
// another.
func TestAConsoleFromNobodyIsAddressedToNobody(t *testing.T) {
	spy := &panelSpy{}
	c := NewChat(spy)
	c.fromRuntime(Envelope{T: TypePtyData, Term: "ghost", Bytes: "aGk="})

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.console) != 1 || !strings.HasPrefix(spy.console[0], ":ghost:") {
		t.Errorf("an unowned console was addressed to %v", spy.console)
	}
}
