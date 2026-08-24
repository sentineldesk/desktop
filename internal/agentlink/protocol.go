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

// The wire between the desktop daemon and the agent runtime.
//
// # Why this is not MCP
//
// The agent already has a connection to this daemon: the MCP socket, over which
// it calls tools. Putting the chat there would have cost no new transport, and
// was rejected for one reason — the MCP vocabulary is visible to EVERY client
// on that socket. A remote Claude Code, a `-mcp-stdio` bridge, anything holding
// a session could list a `chat_next` tool and read what people type into the
// panel. A tool is a public offer; this is a private wire between two processes
// that happen to ship together.
//
// # Why the daemon listens and the runtime dials
//
// ADR-004: "It connects; the daemon listens. The party that must survive is the
// party that holds the door." The practical half of that is cheaper to state:
// the desktop has to boot with no agent at all, and when the desktop listens
// that case costs nothing — an accept queue nobody joins. Had the desktop
// dialled, "no agent" would be a retry loop with a backoff to tune, running on
// every boot of a product whose agent is optional.
//
// # Shape
//
// One JSON object per line, both directions, no request ids and no replies
// except where a field says otherwise. Line-delimited because both ends are Go,
// both ends are local, and a framing layer would buy nothing a `\n` does not.
//
// Every message carries `t`. An unknown `t` is IGNORED rather than an error, in
// both directions: that is what lets a newer runtime talk to an older daemon
// without a version negotiation neither of them would ever exercise.

import "encoding/json"

// Protocol is the version the daemon speaks. It travels in Hello so a runtime
// built against a different one can say so in a sentence rather than failing at
// the first field it does not recognise.
const Protocol = 1

// Message types, daemon -> runtime.
const (
	// Welcome answers Hello. It is the daemon's half of the handshake and the
	// runtime should not send work-related messages before it arrives.
	TypeWelcome = "welcome"

	// Say is a person's message from the chat panel. Chat names the
	// conversation it belongs to; Text is what they typed.
	TypeSay = "say"

	// Cancel stops whatever is running for one conversation. This is the chat
	// panel's stop button and NOT the room's abort: abort is a different,
	// heavier thing that also seizes the controls, and it keeps travelling the
	// path it already travels.
	TypeCancel = "cancel"

	// Sessions asks for the list of past conversations, and Transcript for one
	// of them. History lives in the runtime's own store — the same agent.db the
	// terminal reads — because a transcript kept in two places is a transcript
	// that disagrees with itself. The panel is a second reader, not a second
	// writer.
	TypeSessions   = "sessions"
	TypeTranscript = "transcript"

	// Forget removes past conversations from the runtime's store — one, or all
	// of them. It goes the same way Sessions and Transcript go, and for the
	// same reason: the history is the runtime's, and the panel is a reader of
	// it. A daemon that deleted rows itself would be a second writer to a
	// database whose whole point is that it has one.
	TypeForget = "forget"

	// TypeExport asks for one past conversation as a whole document, and carries
	// the document back.
	//
	// Separate from Transcript, which answers the panel's own reader with turns
	// it will draw. This is the file: the header, every turn, every tool call,
	// the totals — what `export` writes at the terminal, produced by the same
	// code so the two cannot describe one run differently.
	//
	// A whole document rather than something assembled by the receiver, because
	// a face that assembled it would be a second implementation of the format,
	// and the one thing an audit record must not have is two versions that
	// disagree about what happened.
	TypeExport = "export"

	// TypeCommand is a face asking the runtime to do one of the things a slash
	// command does — compact, remember, switch model.
	//
	// The answer comes back as progress, to every face, because these change
	// what the agent is rather than answering the asker: a conversation
	// compacted from a terminal is one a browser is also now looking at.
	//
	// This daemon does not interpret them. It carries what the panel sends and
	// forwards what comes back, which is the whole of its job on this plane.
	TypeCommand = "command"

	// The terminal-in-a-window: a real console session, streamed.
	//
	// # Why a face would want a whole terminal when it has a chat panel
	//
	// Because parity stops being a race. Every slash command ported to the wire
	// is one somebody can add to the terminal tomorrow and forget to port, and
	// a browser that is always one release behind is a browser people stop
	// trusting for the thing they need right now. A console session makes
	// "everything the agent can do" a property rather than a list.
	//
	// It does NOT replace the panel. The panel is the better surface for
	// everything it covers — it has names, descriptions, a picker — and this is
	// the way out for what it does not.
	//
	// # What it is NOT
	//
	// It is not a second engine. What runs behind the pseudo-terminal is this
	// binary attaching to the runtime the ordinary way, so it is one more face
	// on the same conversation: type in it and the chat panel beside it shows
	// the same exchange.
	//
	// And it is not a way around where a credential may go. A pseudo-terminal
	// carried over WebRTC is bytes carried over WebRTC — the same path a form
	// field takes, plus an echo. A session opened FOR a browser is marked
	// remote for exactly that reason; see Remote.
	TypePtyOpen   = "pty.open"
	TypePtyData   = "pty.data"
	TypePtyResize = "pty.resize"
	TypePtyClose  = "pty.close"
)

// Message types, runtime -> daemon.
const (
	// Hello registers the runtime. Until one arrives the daemon has an open
	// connection and no agent, which is not the same thing and must not be
	// reported as one.
	TypeHello = "hello"

	// Delta is a fragment of the answer as it is produced. Many per turn.
	TypeDelta = "delta"

	// Step is something the agent DID — a tool it called, and how it went. The
	// panel draws these between the prose, which is the whole difference
	// between watching an agent work and watching a spinner.
	TypeStep = "step"

	// Done ends one conversation turn, with the totals.
	TypeDone = "done"

	// Failed ends one conversation turn badly. Separate from Done rather than a
	// field on it, because a panel that has to read an error out of a success
	// message will eventually forget to.
	TypeFailed = "failed"

	// Status is the runtime volunteering what it can do right now: which
	// provider and model, and whether it has a key. Sent unprompted whenever it
	// changes, so somebody who configures a provider from the terminal sees the
	// panel come alive without reloading it.
	TypeStatus = "status"

	// TypeProgress is one of the engine's narration events, carried whole.
	//
	// The runtime's loop reports what it is doing in kinds — turn, text, call,
	// result, usage, compacting, widened — and its own terminal draws all of
	// them. That terminal used to read them from a Go channel inside the same
	// process; it is becoming a face on the runtime's socket like this daemon
	// is, and everything the engine narrates therefore has to exist on a wire.
	//
	// One type with the kind in a field, rather than one type per kind: a kind
	// added to the loop reaches every face without a second edit, and there is
	// no list of ten to forget the eleventh from.
	//
	// Delta and Step remain, and a runtime still sends them for the two kinds
	// this daemon has always understood. That is what lets a desktop and a
	// runtime be upgraded in either order.
	TypeProgress = "progress"
)

// Envelope is every message, before the type is known. Fields are a union
// rather than a `json.RawMessage` payload because the whole protocol is
// twelve types wide and a second decode per line would buy nothing.
type Envelope struct {
	T string `json:"t"`

	// Chat names a conversation. Present on everything that belongs to one:
	// say, delta, step, done, failed, cancel. Minted by the daemon when a
	// person sends the first message, so the runtime never has to guess what a
	// browser is calling this.
	Chat string `json:"chat,omitempty"`

	Text string `json:"text,omitempty"`

	// Protocol and Version identify a runtime in Hello.
	Protocol int    `json:"protocol,omitempty"`
	Version  string `json:"version,omitempty"`

	// Provider, Model and Mode ride Hello and Status. Ready is separate from
	// them and is the field that decides whether the panel is usable: a runtime
	// with a model name and no key is connected, configured, and unable to
	// answer, which is exactly the state that looks fine and is not.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Ready    bool   `json:"ready,omitempty"`

	// Why explains a Ready of false in one sentence somebody can act on. Empty
	// when Ready is true.
	Why string `json:"why,omitempty"`

	// Step fields.
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
	Turn   int    `json:"turn,omitempty"`

	// Progress. Kind is the runtime loop's own kind; the rest are its other
	// fields, and they are deliberately the SAME fields the other message types
	// use rather than a nested object — a turn count is a turn count whichever
	// message it arrives in, and two spellings of one number is how a wire
	// starts lying.
	Kind       string `json:"kind,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms,omitempty"`
	CacheWrite int    `json:"cache_write,omitempty"`
	CacheRead  int    `json:"cache_read,omitempty"`

	// Done fields. StoppedBy names the ceiling that ended the run and is empty
	// when the agent simply finished — the distinction Result.StoppedBy exists
	// to preserve on the runtime side, carried across the wire rather than
	// re-derived from the prose.
	Turns     int    `json:"turns,omitempty"`
	Calls     int    `json:"calls,omitempty"`
	InToks    int    `json:"in_toks,omitempty"`
	OutToks   int    `json:"out_toks,omitempty"`
	StoppedBy string `json:"stopped_by,omitempty"`

	// History. Session identifies one past conversation; List and Turns carry
	// the answers.
	Session  int           `json:"session,omitempty"`
	List     []SessionInfo `json:"list,omitempty"`
	Messages []HistoryTurn `json:"messages,omitempty"`

	// Models is the catalogue this runtime was built with.
	//
	// # Why this travels when the reachability list already does
	//
	// They answer different questions, and different faces need different ones.
	// A terminal attached to this runtime is the SAME BINARY, so it already has
	// the catalogue compiled in and sending it would be sending somebody their
	// own file — what it lacks is which of them this machine can reach, which
	// is Providers below.
	//
	// A browser has neither. It is a different program written in a different
	// language, and it cannot know what models exist unless it is told. Without
	// this its model picker could only ever be a text field somebody has to
	// already know the answer to type into.
	Models []ModelInfo `json:"models,omitempty"`

	// Format is which shape an export should take: "md", "text" or "json".
	// Empty means markdown, which is what somebody reading one wants.
	Format string `json:"format,omitempty"`

	// Document is a whole exported session, on the way back. A string rather
	// than bytes: it is a document meant to be read, and base64 in a transcript
	// is a thing nobody can debug by looking at it.
	Document string `json:"document,omitempty"`

	// A console session. Term is the session's id, minted by whoever opened it.
	// Bytes are base64 — a terminal emits partial UTF-8 sequences across reads,
	// and JSON cannot carry those as a string without corrupting them.
	Term  string `json:"term,omitempty"`
	Bytes string `json:"bytes,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`

	// Remote marks a console session opened on behalf of somebody who is NOT on
	// this machine — a browser, through the desktop.
	//
	// It is the whole of the credential boundary as it applies to terminals. A
	// pseudo-terminal runs on the runtime's own machine, so everything typed
	// into it arrives over the runtime's local socket and looks local; without
	// this the refusal that stops an API key crossing a room would never fire,
	// and the terminal would be a back door through the one security rule this
	// system has.
	//
	// The DESKTOP sets it, because the runtime cannot know who asked. That is
	// the one decision on this plane the daemon makes rather than carries.
	Remote bool `json:"remote,omitempty"`

	// Commands is what the runtime can be asked to do, as it announces itself.
	//
	// The engine publishes its own surface so that no face has to keep a copy
	// of it. The chat panel had one written out in TypeScript: add a command to
	// the runtime and the browser would not offer it; remove one and the
	// browser would offer something that answers "this runtime does not know
	// that". Neither failure is visible until somebody tries it.
	Commands []CommandInfo `json:"commands,omitempty"`

	// Providers names every provider this RUNTIME can actually reach right now.
	//
	// # Why it travels at all, when the catalogue does not
	//
	// The list of models is a compiled-in array and a face is the same binary,
	// so sending it would be sending somebody their own file. Which of them can
	// be reached is a different question: it depends on keys, on installed
	// CLIs, on a home directory — on the ENGINE's environment, not the face's.
	// A terminal on a laptop attached to a runtime in a container would
	// otherwise show a picker built from its own machine and get it wrong in
	// both directions.
	//
	// Ids only. What each one offers is already known to whoever receives this.
	Providers []string `json:"providers,omitempty"`

	// All is "forget every session", and it is a FIELD rather than a value of
	// Session for one reason: no accident may reach it. A sentinel — 0, or -1
	// — makes the whole-ledger wipe one uninitialised variable away from a
	// request that meant to delete a single row. There is no value of Session
	// that empties the database; it takes saying so.
	All bool `json:"all,omitempty"`

	// At is when the daemon or the runtime stamped this, in milliseconds. A
	// panel that renders in arrival order is right until two planes both emit.
	At int64 `json:"at,omitempty"`
}

// SessionInfo is one past conversation, as the list shows it.
type SessionInfo struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Turns int    `json:"turns"`

	// At is the runtime's own timestamp for the run, passed through as the
	// string its database holds rather than parsed into a number here. Parsing
	// would mean guessing a layout, and a guess that is wrong produces a date
	// that is confidently incorrect — worse than one the panel formats itself
	// or shows as it is.
	At string `json:"at,omitempty"`

	// Live marks the conversation the runtime is IN. Its twin is wireSession.Live
	// in the agent repository, and it exists because no face can work this out:
	// the id belongs to the engine's own session. A history listing uses it to
	// say which row you are looking at, and to stop offering to delete the one
	// record a Runner is still writing to.
	Live bool `json:"live,omitempty"`
}

// HistoryTurn is one line of a past conversation.
type HistoryTurn struct {
	Role string `json:"role"` // human | agent | system
	Text string `json:"text"`
	At   string `json:"at,omitempty"`
}

// encode renders one message for the wire, newline included.
//
// Here rather than at each call site so there is exactly one place where a
// message becomes bytes — the same reasoning ChatTurn.encode records in
// stream/room.go, and for the same reason: a duplicated payload builder keeps
// passing its test after the two have drifted apart.
func encode(m Envelope) ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ModelInfo is one entry of the runtime's catalogue, for a face that does not
// have it compiled in — which is every browser.
//
// Provider and ID separately rather than the joined `provider/id`, because a
// face that had to split it back apart would need to know the cut is from the
// LEFT: an OpenRouter model id contains a slash of its own. Sending the two
// parts keeps that knowledge in one place.
//
// No "reachable" field. Whether a model can be used is a property of its
// PROVIDER, and Providers already names the ones that can be reached; saying it
// again per model would be one fact in two places, free to disagree.
type ModelInfo struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Note     string `json:"note,omitempty"`
}

// CommandInfo is one thing the runtime can be asked to do.
//
// ID is stable and translatable; What is the same description as prose. A face
// that knows the id renders it in the reader's language from its own
// catalogue; a face that has never heard of the command falls back to What,
// which is at least a sentence. Sending only the id would leave a new command
// with a blank description in every existing face; sending only the prose would
// make the whole palette English.
//
// Local marks a command that may only be asked for over the runtime's own
// socket — today, storing an API key. Published so a browser can leave it out
// of its palette rather than offer something it will be refused for. The
// refusal still happens if it asks anyway: a palette is a courtesy, the check
// is the rule.
type CommandInfo struct {
	Name  string `json:"name"`
	Kind  string `json:"kind,omitempty"`
	ID    string `json:"id,omitempty"`
	What  string `json:"what,omitempty"`
	Local bool   `json:"local,omitempty"`
}
