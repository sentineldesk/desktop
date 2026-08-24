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

// The coordinator: it owns the translation between two protocols that must not
// know about each other.
//
// On one side is the browser's DataChannel, where a chat panel sends what
// somebody typed and expects fragments and steps back. On the other is the
// runtime wire in this package. Neither side should have to learn the other's
// vocabulary, and putting the translation in a third place is what keeps the
// web client free of any notion of a Unix socket and the runtime free of any
// notion of a Room.
//
// # What this deliberately does NOT do
//
// It does not gate tool calls. The agent works in auto mode and the only door
// it has to knock on is the controls: if a person is driving, the runtime asks
// for them through request_control exactly as it does today, over MCP, and this
// wire never sees it. Adding an approval round-trip here would have meant two
// mechanisms for one question, disagreeing the first time somebody answered in
// the wrong one.

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sentineldesk/desktop/internal/stream"
)

// Panel is what the coordinator needs from the room. Narrow on purpose: it is
// the whole of the coupling between the agent plane and the people's plane, and
// a narrow interface is what keeps that visible.
type Panel interface {
	AgentStatus(stream.AgentAvailability)
	AgentChat(stream.ChatTurn)
	AgentDelta(chat, text string)
	AgentStep(chat, tool, detail string, turn int)
	AgentEnd(stream.AgentEnd)
	AgentHistory(session int, chat string, list []stream.AgentSession, messages []stream.AgentHistoryTurn)
	AgentExport(session int, format, document string)
	AgentConsole(memberID, term, bytes string)
	AgentConsoleClosed(memberID, term, why string)
}

// Chat ties the room to the runtime.
type Chat struct {
	link  *Link
	panel Panel

	// binary is where sentineldesk-agent was found, empty when it is not
	// installed. Resolved once at start rather than per message: a person who
	// installs it mid-session gets the right answer as soon as it connects,
	// which is the event that actually matters, and probing the filesystem on
	// every status broadcast would be work done to answer a question the
	// connection already answers.
	binary string

	seq atomic.Int64

	mu sync.Mutex
	// consoles maps a terminal to the browser that opened it, so its bytes go
	// back to that browser and to nobody else. See Room.AgentConsole for why a
	// terminal is the one thing on this plane that is not broadcast.
	consoles map[string]string
	// live is the conversations with work outstanding. Kept so a runtime that
	// dies mid-answer produces an ending rather than a spinner that never
	// stops — a silent non-outcome being the failure this project ranks below
	// a crash.
	live map[string]time.Time
}

// NewChat builds the coordinator and opens the door.
//
// The link is constructed here rather than handed in because its two callbacks
// are this type's methods, and wiring them anywhere else would leave a window
// where the socket is accepting connections whose messages go nowhere.
func NewChat(panel Panel) *Chat {
	c := &Chat{panel: panel, live: map[string]time.Time{}}
	c.link = New(c.fromRuntime, c.statusChanged)
	c.binary = findAgent()
	return c
}

// Listen opens the socket. A failure is reported and not fatal: the desktop
// runs without an agent, and it must run without one when the agent's own
// plumbing is what failed.
func (c *Chat) Listen(path string) error { return c.link.Listen(path) }

// Availability is what the panel is told, assembled from what the link knows
// and what is on disk.
func (c *Chat) Availability() stream.AgentAvailability {
	s := c.link.Status()
	a := stream.AgentAvailability{
		Present: s.Connected, Ready: s.Ready,
		Provider: s.Provider, Model: s.Model, Mode: s.Mode,
		Reachable: s.Reachable,
		Models:    toPanelModels(s.Models),
		Commands:  toPanelCommands(s.Commands),
	}
	switch {
	case s.Connected && s.Ready:
		// Nothing to explain.
	case s.Connected:
		// Running, no model. The remedy is configuration, and it can be done
		// from either side — which is why the command is offered rather than
		// the panel claiming to be the only way.
		a.Reason = s.Why
		a.Remedy = "sentineldesk-agent"
	case c.binary != "":
		// A binary beside this daemon, not running. Naming the path matters:
		// somebody with two copies on their machine needs to know which one
		// this daemon is waiting for, and the socket is the other half of that
		// answer.
		a.Reason = "the agent is installed here but not running"
		a.Remedy = c.binary + " -serve"
	default:
		// Not installed, and the remedy is NOT an installer script.
		//
		// The agent ships as its own image, so there is nothing to download and
		// nothing to build: what is missing is a container that was never
		// started. An install command here would send somebody to fetch
		// software they can start with one line — and the first version of this
		// did exactly that, pointing at a script that does not exist.
		a.Reason = "the agent is not running"
		a.Remedy = "docker compose up -d agent"
	}
	return a
}

// Send is the browser's way in: one person's message, from the chat panel.
//
// It returns the conversation id so the Session can tell that browser which
// exchange its message opened, and an error when there is nothing to send to.
// The error is the panel's cue to draw the unavailable state — which it also
// already has from the last agent_status, so this is a guard rather than the
// primary route.
func (c *Chat) Send(chat, who, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("there is nothing to send")
	}
	if chat = strings.TrimSpace(chat); chat == "" {
		chat = fmt.Sprintf("chat-%d", c.seq.Add(1))
	}

	// Echoed to the room BEFORE the send, and echoed even if the send fails.
	//
	// A person's own words disappearing because the runtime was not there is
	// the worst of the failure modes available here: they cannot tell whether
	// it was sent, and the natural response is to type it again. The
	// transcript records what was said; whether it was answered is a separate
	// message.
	c.panel.AgentChat(stream.ChatTurn{
		ID: chat, Role: "human", Via: "panel", Text: text,
	})

	if err := c.link.Send(Envelope{T: TypeSay, Chat: chat, Text: text}); err != nil {
		return chat, err
	}

	c.mu.Lock()
	c.live[chat] = time.Now()
	c.mu.Unlock()
	_ = who
	return chat, nil
}

// Cancel stops one conversation's work.
func (c *Chat) Cancel(chat string) error {
	return c.link.Send(Envelope{T: TypeCancel, Chat: chat})
}

// History asks the runtime for its past conversations, or for one of them.
//
// The runtime answers, not this process. History lives in agent.db, which the
// terminal reads and writes; a copy kept here would be a second transcript that
// disagrees with the first the moment somebody uses the other one. The panel is
// a second reader of one record, which is what "todo lo que puede hacer el
// binario se puede hacer desde la interfaz" has to mean if the two are ever to
// show the same thing.
func (c *Chat) History(session int) error {
	if session > 0 {
		return c.link.Send(Envelope{T: TypeTranscript, Session: session})
	}
	return c.link.Send(Envelope{T: TypeSessions})
}

// Forget removes past conversations from the runtime's store: one of them, or
// all of them when all is true.
//
// It sends and returns; there is no acknowledgement to wait for. The runtime
// answers a forget with the sessions list, which arrives through the ordinary
// history route and lands in every panel in the room rather than only in the
// one that asked — which is what it should do, because the history is shared
// and a row deleted in one browser has not disappeared for the person watching
// in another until their list says so too.
func (c *Chat) Forget(session int, all bool) error {
	if all {
		return c.link.Send(Envelope{T: TypeForget, All: true})
	}
	if session <= 0 {
		return fmt.Errorf("there is no session %d to forget", session)
	}
	return c.link.Send(Envelope{T: TypeForget, Session: session})
}

// statusChanged is the link's callback. It republishes availability and, when
// the runtime has gone, ends whatever it left hanging.
func (c *Chat) statusChanged(s Status) {
	if !s.Connected {
		c.endAllLive("the agent runtime stopped")
	}
	c.panel.AgentStatus(c.Availability())
}

// coveredByOlderSpellings is the two progress kinds that also arrive as their
// own message type.
//
// A runtime sends its whole narration as progress AND sends delta and step for
// these two, so that a desktop built before progress existed keeps working.
// Drawing both would put every fragment of every answer into the panel twice —
// which is precisely the bug the panel already had once, from a different
// cause, and it took somebody staring at two identical bubbles to find it.
//
// A map rather than a switch because the rule is data: this is the list of
// things said twice, and when the oldest runtime in the world stops saying them
// twice, the list empties and nothing else here changes.
var coveredByOlderSpellings = map[string]bool{"text": true, "call": true}

// progress forwards the narration a browser can use.
//
// Most of it is for the terminal — turn boundaries, per-turn token counts, the
// arithmetic a session strip does — and a panel has no row for any of it. What
// it does need is everything that is neither prose nor a tool call and yet
// happened: a conversation being compacted, a fact remembered, a model
// switched, a sign-in link, a refusal.
//
// Before this, those existed only for faces attached to the runtime directly,
// so `/compact` typed in a browser would have worked and said nothing — which
// is the failure mode this project treats as worse than not working at all.
//
// Drawn as a STEP, deliberately. A step is already "something happened during
// this exchange, and here is what", which is exactly what these are; giving
// them a message type of their own would mean a panel that has to learn a new
// shape to say a thing it can already say.
func (c *Chat) progress(m Envelope) {
	if m.Kind == "" || coveredByOlderSpellings[m.Kind] {
		return
	}
	switch m.Kind {
	case "turn", "usage", "done":
		// The terminal's arithmetic. A panel shows the totals once, in the
		// footer, from agent_end, and a row per turn would be a line of noise
		// in a transcript somebody is reading.
		//
		// `result` used to be in this list and is not any more. It was a
		// defensible reading of "the panel shows what the agent DID" — and it
		// left the panel showing a column of tool NAMES with no way to see what
		// any of them answered. A trace you cannot read the answers out of is a
		// list, not a trace, and the whole reason to watch an agent work is to
		// see what it found.
		return
	}
	c.panel.AgentStep(m.Chat, m.Kind, m.Detail, m.Turn)
}

// Export asks for one past conversation as a whole document. Session 0 means
// the one open now, which the runtime resolves — it is the one that knows.
func (c *Chat) Export(session int, format string) error {
	return c.link.Send(Envelope{T: TypeExport, Session: session, Format: format})
}

// ConsoleOpen asks the runtime for a terminal, on behalf of one browser.
//
// # The one decision this daemon makes rather than carries
//
// Remote is set here, always, and it is not a parameter. A pseudo-terminal runs
// on the RUNTIME's machine, so everything typed into it arrives over the
// runtime's own local socket and looks exactly like a person at that keyboard —
// which is the test that decides whether an API key may be stored. Without this
// the terminal would be a back door through the one security rule this system
// has, wearing a friendly window.
//
// The runtime cannot work it out for itself: it sees a local connection. This
// process is the only one that knows a browser asked, so this is where the
// answer comes from — and it is hard-coded rather than passed, because there is
// no console opened through this daemon that is not opened for a browser.
func (c *Chat) ConsoleOpen(memberID, term string, cols, rows int) error {
	if strings.TrimSpace(term) == "" {
		return fmt.Errorf("a console needs an id")
	}
	c.mu.Lock()
	if c.consoles == nil {
		c.consoles = map[string]string{}
	}
	c.consoles[term] = memberID
	c.mu.Unlock()

	return c.link.Send(Envelope{T: TypePtyOpen, Term: term,
		Cols: cols, Rows: rows, Remote: true})
}

// ConsoleData carries keystrokes to a terminal, base64 as they arrived.
func (c *Chat) ConsoleData(term, bytes string) error {
	return c.link.Send(Envelope{T: TypePtyData, Term: term, Bytes: bytes})
}

// ConsoleResize tells a terminal how big its window is now.
func (c *Chat) ConsoleResize(term string, cols, rows int) error {
	return c.link.Send(Envelope{T: TypePtyResize, Term: term, Cols: cols, Rows: rows})
}

// ConsoleClose ends a terminal.
func (c *Chat) ConsoleClose(term string) error {
	return c.link.Send(Envelope{T: TypePtyClose, Term: term})
}

// ownerOf is the browser a console belongs to, or "" when nothing here opened
// it — which is what a runtime restarting under an open console looks like.
func (c *Chat) ownerOf(term string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consoles[term]
}

func (c *Chat) forgetConsole(term string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.consoles, term)
}

// Command asks the runtime to do one of the things a slash command does.
//
// Carried, never interpreted. What the commands mean is the runtime's business
// — this daemon does not know what compacting a conversation is, and the day it
// starts knowing is the day there are two implementations to disagree.
func (c *Chat) Command(chat, kind, text string) error {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return fmt.Errorf("there is no command to send")
	}
	return c.link.Send(Envelope{T: TypeCommand, Chat: chat, Kind: kind, Text: text})
}

// toPanelModels is the catalogue in the shape the room broadcasts.
//
// A translation rather than a shared type, because the two sides of this daemon
// are two protocols: what a runtime says and what a browser is told. They agree
// today and there is no reason they must forever — the room's wire is a
// contract with a web client that can be older than this build.
func toPanelModels(in []ModelInfo) []stream.AgentModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]stream.AgentModel, 0, len(in))
	for _, m := range in {
		out = append(out, stream.AgentModel{Provider: m.Provider, ID: m.ID, Note: m.Note})
	}
	return out
}

// toPanelCommands is the runtime's announced surface, in the shape the room
// broadcasts. A translation rather than a shared type, for the reason
// toPanelModels is: these are two protocols that agree today.
//
// A command marked local is DROPPED rather than forwarded with its flag. The
// panel would only have to filter it out again, and a browser that is never
// told about a command it may not use cannot offer it by mistake — the refusal
// in the runtime is still the rule, this is just not putting the temptation on
// screen.
func toPanelCommands(in []CommandInfo) []stream.AgentCommand {
	var out []stream.AgentCommand
	for _, c := range in {
		if c.Local {
			continue
		}
		out = append(out, stream.AgentCommand{
			Name: c.Name, Kind: c.Kind, ID: c.ID, What: c.What,
		})
	}
	return out
}

// endAllLive closes every outstanding exchange with a reason.
func (c *Chat) endAllLive(why string) {
	c.mu.Lock()
	open := make([]string, 0, len(c.live))
	for id := range c.live {
		open = append(open, id)
	}
	c.live = map[string]time.Time{}
	c.mu.Unlock()

	for _, id := range open {
		c.panel.AgentEnd(stream.AgentEnd{Chat: id, Ok: false, Text: why})
	}
}

// fromRuntime is everything the runtime sends that is not a handshake.
func (c *Chat) fromRuntime(m Envelope) {
	switch m.T {
	case TypeProgress:
		c.progress(m)

	case TypeDelta:
		c.panel.AgentDelta(m.Chat, m.Text)

	case TypeStep:
		c.panel.AgentStep(m.Chat, m.Tool, m.Detail, m.Turn)

	case TypeDone:
		c.closed(m.Chat)
		// The answer goes onto the transcript as a turn of its own, so a panel
		// that joined mid-run — or one reloaded after it — has the whole reply
		// rather than only the fragments it happened to be present for.
		if strings.TrimSpace(m.Text) != "" {
			c.panel.AgentChat(stream.ChatTurn{
				ID: m.Chat, Role: "agent", Via: "panel", Text: m.Text,
			})
		}
		c.panel.AgentEnd(stream.AgentEnd{
			Chat: m.Chat, Ok: true, Turns: m.Turns, Calls: m.Calls,
			InToks: m.InToks, OutToks: m.OutToks, StoppedBy: m.StoppedBy,
		})

	case TypeFailed:
		c.closed(m.Chat)
		c.panel.AgentEnd(stream.AgentEnd{Chat: m.Chat, Ok: false, Text: m.Text})

	case TypeExport:
		c.panel.AgentExport(m.Session, m.Format, m.Document)

	case TypePtyData:
		c.panel.AgentConsole(c.ownerOf(m.Term), m.Term, m.Bytes)

	case TypePtyClose:
		who := c.ownerOf(m.Term)
		c.forgetConsole(m.Term)
		c.panel.AgentConsoleClosed(who, m.Term, m.Text)

	case TypeSessions:
		// Answered straight back to the room. History is a read, so everyone
		// watching gets the same list rather than only whoever asked — which
		// is also what makes a second browser opening the panel show the same
		// thing without asking again.
		list := make([]stream.AgentSession, 0, len(m.List))
		for _, s := range m.List {
			list = append(list, stream.AgentSession{
				ID: s.ID, Title: s.Title, Turns: s.Turns, At: s.At, Live: s.Live})
		}
		c.panel.AgentHistory(m.Session, "", list, nil)

	case TypeTranscript:
		// The two are kept apart here rather than folded together on the
		// session number, because a transcript of session 0 is a real thing —
		// it is the conversation the runtime is in right now, and the runtime
		// says which one that is in Chat.
		msgs := make([]stream.AgentHistoryTurn, 0, len(m.Messages))
		for _, h := range m.Messages {
			msgs = append(msgs, stream.AgentHistoryTurn{
				Role: h.Role, Text: h.Text, At: h.At})
		}
		c.panel.AgentHistory(m.Session, m.Chat, nil, msgs)

	default:
		// Unknown types are ignored, which is what lets a newer runtime talk to
		// an older daemon. Logged once at low volume rather than silently, so a
		// version skew is findable.
		log.Printf("agent: ignoring an unknown message type %q", m.T)
	}
}

// closed forgets a conversation that has ended.
func (c *Chat) closed(chat string) {
	c.mu.Lock()
	delete(c.live, chat)
	c.mu.Unlock()
}

// findAgent looks for the runtime binary, so "not installed" and "not running"
// can be told apart.
//
// They need different remedies — one is a download, the other is a command —
// and a panel that offers the wrong one wastes somebody's time in the most
// annoying way available: telling them to install what they have.
func findAgent() string {
	if path, err := exec.LookPath("sentineldesk-agent"); err == nil {
		return path
	}
	// The container installs it here; PATH may not include it for the daemon,
	// which runs under supervisord with an environment of its own.
	for _, p := range []string{
		"/usr/local/bin/sentineldesk-agent",
		"/usr/bin/sentineldesk-agent",
	} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}
