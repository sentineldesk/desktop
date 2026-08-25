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

// What the chat panel is told, on top of what agent_chat already says.
//
// agent_chat carries whole turns and predates this: it is the transcript, and
// it is what ask_human writes into. It stays exactly as it is. What a chat
// panel needs beyond a transcript is the middle of a turn — the answer arriving
// a fragment at a time, and the tools being called between the prose — plus one
// message saying whether there is an agent to talk to at all.
//
// Each of those is its own `t` rather than a field on agent_chat, for the
// reason agent_chat is not called "question": a client that has never heard of
// a type ignores it and draws nothing, whereas overloading a type somebody
// already handles makes an old client draw the wrong thing confidently.

import (
	"encoding/json"
	"time"
)

// The types a browser matches on. Named, because they are a contract with the
// web client and a literal in one function is not a contract.
const (
	// AgentStatusType says whether the agent plane is usable, and if not, why.
	// Sent on join and whenever it changes, so a panel never has to ask.
	AgentStatusType = "agent_status"

	// AgentDeltaType is a fragment of an answer being written. Many per turn,
	// each appended to whatever the panel already has for that chat id.
	AgentDeltaType = "agent_delta"

	// AgentStepType is a tool the agent called. Drawn between the prose, which
	// is the difference between watching an agent work and watching a spinner.
	AgentStepType = "agent_step"

	// AgentEndType closes one exchange, successfully or not. It carries the
	// totals a panel shows in the footer and the reason a run stopped short.
	AgentEndType = "agent_end"
)

// AgentAvailability is what the panel needs to decide what to draw.
//
// Three states, not two. "Installed and running but with no model configured"
// is neither available nor absent, and collapsing it into either one sends the
// person to the wrong remedy: told it is unavailable they reinstall software
// they already have; told it is available they get a box that fails on the
// first message. Reason and Remedy are what make the third state actionable.
type AgentAvailability struct {
	// Present means a runtime is connected. Ready means it can answer.
	Present bool `json:"present"`
	Ready   bool `json:"ready"`

	// Reason states what is wrong, in a sentence. Empty when Ready.
	Reason string `json:"reason,omitempty"`

	// Remedy is the command that fixes it, when one does. Kept apart from
	// Reason so the panel can render it as a copyable line rather than
	// hunting for a command inside a sentence — the same rule this codebase
	// applies to facts generally: carried as data, never parsed back out of
	// display text.
	Remedy string `json:"remedy,omitempty"`

	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Mode     string `json:"mode,omitempty"`

	// Models is the runtime's catalogue, and Reachable the providers it can
	// actually use right now. Both are carried for the browser's sake: it is a
	// different program in a different language and cannot know either unless
	// it is told, so without them its model picker could only be a text field
	// somebody has to already know the answer to type into.
	Models    []AgentModel `json:"models,omitempty"`
	Reachable []string     `json:"reachable,omitempty"`

	// Commands is what the runtime says it can be asked to do. The panel builds
	// its palette from this rather than from a list of its own, so a command
	// added to the runtime appears in the browser with no new build of it.
	Commands []AgentCommand `json:"commands,omitempty"`
}

// AgentCommand is one thing the runtime offers.
//
// ID names the description for translation, What is the same description as
// prose for a panel that has never heard of this one. Local marks a command a
// browser must not offer — see the runtime's connect.key.
type AgentCommand struct {
	Name  string `json:"name"`
	Kind  string `json:"kind,omitempty"`
	ID    string `json:"id,omitempty"`
	What  string `json:"what,omitempty"`
	Local bool   `json:"local,omitempty"`
}

// AgentModel is one entry of the runtime's catalogue.
//
// Provider and ID separately, because a face that had to split `provider/id`
// apart would need to know the cut is from the LEFT — an OpenRouter model id
// contains a slash of its own.
type AgentModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Note     string `json:"note,omitempty"`
}

// AgentStatus tells the room whether it has an agent to talk to.
//
// Fire and forget, like every other broadcast here: nobody in the room means
// nobody is told, which is correct rather than a failure.
func (r *Room) AgentStatus(a AgentAvailability) {
	r.tellEveryone(jsonLine(map[string]any{
		"t": AgentStatusType, "present": a.Present, "ready": a.Ready,
		"reason": a.Reason, "remedy": a.Remedy,
		"provider": a.Provider, "model": a.Model, "mode": a.Mode,
		"models": a.Models, "reachable": a.Reachable, "commands": a.Commands,
		"at": time.Now().UnixMilli(),
	}))
}

// AgentDelta appends a fragment to an answer in progress.
func (r *Room) AgentDelta(chat, text string) {
	if text == "" {
		return
	}
	r.tellEveryone(jsonLine(map[string]any{
		"t": AgentDeltaType, "chat": chat, "text": text,
		"at": time.Now().UnixMilli(),
	}))
}

// AgentStep records one thing the agent did.
func (r *Room) AgentStep(chat, tool, detail string, turn int) {
	r.tellEveryone(jsonLine(map[string]any{
		"t": AgentStepType, "chat": chat, "tool": tool,
		"detail": detail, "turn": turn, "at": time.Now().UnixMilli(),
	}))
}

// AgentEnd closes an exchange.
//
// Ok is a field rather than two message types because the panel does the same
// thing either way — stops the spinner, draws the footer — and the difference
// is what it writes in it. Where the distinction changes behaviour, as between
// Done and Failed on the runtime wire, it gets its own type; here it does not.
type AgentEnd struct {
	Chat      string
	Ok        bool
	Text      string
	Turns     int
	Calls     int
	InToks    int
	OutToks   int
	StoppedBy string
}

// AgentEnd broadcasts the close of one exchange.
func (r *Room) AgentEnd(e AgentEnd) {
	payload := map[string]any{
		"t": AgentEndType, "chat": e.Chat, "ok": e.Ok,
		"turns": e.Turns, "calls": e.Calls,
		"in_toks": e.InToks, "out_toks": e.OutToks,
		"at": time.Now().UnixMilli(),
	}
	// Omitted rather than sent empty, so a panel checking for the field does
	// not have to know the difference between "finished" and "the field is
	// there and blank".
	if e.Text != "" {
		payload["text"] = e.Text
	}
	if e.StoppedBy != "" {
		payload["stopped_by"] = e.StoppedBy
	}
	r.tellEveryone(jsonLine(payload))
}

// jsonLine encodes one broadcast, returning "" if it cannot.
//
// The empty string is deliberate and safe: tellEveryone sending nothing is a
// message that does not arrive, which is the same outcome as the marshal
// failing and is reached without a caller having to handle an error it cannot
// do anything about. Every payload here is a map of strings and ints, so the
// failure is unreachable in practice — this exists so that stays true silently
// rather than by five copies of the same `if err != nil { return }`.
func jsonLine(payload map[string]any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

// AgentDesk is the chat coordinator as the room needs it.
//
// Declared here rather than imported because the coordinator imports THIS
// package: it builds stream.ChatTurn and stream.AgentEnd, so the dependency
// runs one way and an interface is what lets the room call back across it. The
// room knows there is something that can carry a message to an agent; it does
// not know there is a Unix socket, and that is the boundary worth keeping.
type AgentDesk interface {
	// Send carries one person's message. The returned id names the exchange —
	// minted here when the caller passes an empty one, so a browser opening a
	// new conversation does not have to invent an id the runtime will accept.
	Send(chat, who, text string) (string, error)

	// Cancel stops one exchange's work. Not the room's abort, which is heavier
	// and also seizes the controls; this is the chat panel's stop button.
	Cancel(chat string) error

	// History asks for the list of past conversations, or for one transcript.
	History(session int) error

	// Forget removes past conversations from the runtime's store: one, or all
	// of them. The answer is the refreshed list, arriving the History way.
	Forget(session int, all bool) error

	// Rename gives one conversation the person's own name; empty clears it.
	// The answer is the refreshed list, arriving the History way.
	Rename(session int, title string) error

	// Export asks for one past conversation as a whole document.
	Export(session int, format string) error

	// A console session: a terminal, opened on behalf of one browser and
	// streamed back to it. memberID is who asked, so its bytes can go to them
	// and to nobody else.
	ConsoleOpen(memberID, term string, cols, rows int) error
	ConsoleData(term, bytes string) error
	ConsoleResize(term string, cols, rows int) error
	ConsoleClose(term string) error

	// Command asks the runtime for one of the things a slash command does.
	// What they mean is the runtime's business; this room only carries them.
	Command(chat, kind, text string) error

	// Availability is what to draw when there is no agent, or no model.
	Availability() AgentAvailability
}

// AgentExportType is the `t` a browser matches on for a finished export.
const AgentExportType = "agent_export"

// AgentExport hands one exported session to the room.
//
// To EVERYONE, like every other broadcast here, and that is worth stating
// because a document feels private in a way a delta does not. It is not: this
// is the record of what the agent did on a shared desktop, and everybody in the
// room watched it happen. A download addressed to whoever clicked would also
// need a reply channel this plane does not have — see the note on Notice.
func (r *Room) AgentExport(session int, format, document string) {
	r.tellEveryone(jsonLine(map[string]any{
		"t": AgentExportType, "session": session, "format": format,
		"document": document, "at": time.Now().UnixMilli(),
	}))
}

// The console session's message types, as a browser matches on them.
const (
	AgentConsoleDataType  = "agent_console_data"
	AgentConsoleCloseType = "agent_console_close"
)

// AgentConsole hands one console session's bytes back to the browser that
// opened it, and to nobody else.
//
// # Why this is the one thing on this plane that is NOT broadcast
//
// Everything else here goes to the whole room, deliberately: the desktop is
// shared, the conversation is shared, and a panel showing a different
// transcript from the one beside it would make the word "session" mean two
// things.
//
// A terminal is different, and the line worth drawing is between what it SAYS
// and what it SHOWS. Anything typed into it that reaches the agent comes back
// as ordinary chat and fans out to everyone, exactly as it should — the room
// still sees what was asked and what was done. What does not fan out is the
// terminal's own picture: the keystrokes, the half-typed lines, the scrollback
// somebody is reading. That is a person working, not a decision the room needs.
//
// Practically it is also what makes two consoles possible at once. Broadcast,
// every browser would receive every session's bytes and have to throw away the
// ones that are not its own — which works, and means a person's typing arrives
// at a machine that had no reason to see it.
func (r *Room) AgentConsole(memberID, term, bytes string) {
	r.sendTo(memberID, jsonLine(map[string]any{
		"t": AgentConsoleDataType, "term": term, "bytes": bytes,
		"at": time.Now().UnixMilli(),
	}))
}

// AgentConsoleClosed says a console session has ended, with a reason when there
// was one.
func (r *Room) AgentConsoleClosed(memberID, term, why string) {
	r.sendTo(memberID, jsonLine(map[string]any{
		"t": AgentConsoleCloseType, "term": term, "text": why,
		"at": time.Now().UnixMilli(),
	}))
}

// sendTo delivers one message to one member, if they are still here.
//
// A member who has left is not an error and not worth a log line: a console
// whose browser closed goes on producing output until the runtime notices, and
// the correct thing to do with those bytes is drop them.
func (r *Room) sendTo(memberID, msg string) {
	if msg == "" || memberID == "" {
		return
	}
	r.mu.RLock()
	m, ok := r.members[memberID]
	var sess *Session
	if ok {
		sess = m.session
	}
	r.mu.RUnlock()
	if sess != nil {
		sess.sendOnChannel(msg)
	}
}

// SetAgentDesk wires the chat plane in. Without it the room still works and the
// panel reports the agent as absent, which is the correct reading of a daemon
// built or configured without one.
func (r *Room) SetAgentDesk(d AgentDesk) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentDesk = d
}

// AgentDeskOrNil returns the chat plane, or nil when there is none.
//
// Named for what it can return, because the alternative — a caller assuming a
// non-nil and dereferencing it in a build without an agent — is the exact shape
// of the bug the room already had once with callRoom.
func (r *Room) AgentDeskOrNil() AgentDesk {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agentDesk
}

// AgentHistoryType carries past conversations to the panel.
//
// Its contents come from the runtime's own database — the same agent.db the
// terminal reads — and travel through this process without being stored in it.
// A transcript kept in two places is a transcript that disagrees with itself
// the moment somebody uses the other one, so the panel is a second reader of
// one record rather than a second writer of a copy.
const AgentHistoryType = "agent_history"

// AgentSession is one past conversation, as the list shows it.
type AgentSession struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Turns int    `json:"turns"`
	At    string `json:"at,omitempty"`

	// Live marks the conversation the runtime is in. The panel shows which row
	// that is and stops offering to delete it — the runtime refuses, and a
	// button whose only outcome is a refusal is one nobody should be given.
	Live bool `json:"live,omitempty"`
}

// AgentHistoryTurn is one line of a past conversation.
type AgentHistoryTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
	At   string `json:"at,omitempty"`

	// Steps is the turn's tool calls, one entry each plus results, so the
	// panel folds a restored conversation the way it folds the live one.
	Steps []AgentHistoryStep `json:"steps,omitempty"`
}

// AgentHistoryStep is one tool call, or its result, inside a transcript turn.
type AgentHistoryStep struct {
	Tool   string `json:"tool"`
	Detail string `json:"detail,omitempty"`
}

// AgentHistory delivers either the list of conversations or one transcript.
//
// Both ride one message with one of the two fields filled, rather than two
// message types, because the panel does the same thing with each: replaces
// what it is showing. Session says which transcript arrived, and is zero for
// the list.
// chat, when set, means this transcript is the conversation the runtime is IN
// rather than one out of the archive — a terminal that just attached asking to
// be shown what is going on, or a past conversation somebody chose to continue.
// The panel adopts the id, because from that moment its next message has to
// arrive under the conversation the runtime actually opened; minting its own
// would start a third one and quietly throw the restored history away.
func (r *Room) AgentHistory(session int, chat string, list []AgentSession, messages []AgentHistoryTurn) {
	r.tellEveryone(jsonLine(agentHistoryPayload(session, chat, list, messages)))
}

// agentHistoryPayload is the frame itself, built apart from sending it.
//
// Split out because the decision inside it is invisible when it goes wrong and
// unreachable once it has been handed to a DataChannel: a live conversation
// labelled as the list empties the history drawer, and the list labelled as a
// transcript replaces what somebody is reading with nothing. Neither throws.
func agentHistoryPayload(session int, chat string, list []AgentSession, messages []AgentHistoryTurn) map[string]any {
	payload := map[string]any{
		"t": AgentHistoryType, "session": session,
		"at": time.Now().UnixMilli(),
	}
	// Which of the two this is, decided by which the caller filled rather than
	// by the session number. It used to be `session == 0 ? list : transcript`,
	// and that reading has no room for the case this comment exists for: the
	// LIVE conversation is a transcript whose session is 0.
	//
	// Sent as empty arrays rather than omitted: a panel replacing what it shows
	// has to be able to tell "no conversations yet" from "this message is not
	// about the list", and a missing field cannot say the first.
	if messages != nil {
		if chat != "" {
			payload["chat"] = chat
		}
		payload["messages"] = messages
	} else {
		if list == nil {
			list = []AgentSession{}
		}
		payload["sessions"] = list
	}
	return payload
}
