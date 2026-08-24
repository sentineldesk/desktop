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

// The daemon's side of the wire: it opens the door and waits.
//
// Everything here is written so that "no agent" is the cheap case. There is no
// dialling, no retry, no backoff and no timeout, because the desktop is not
// trying to reach anything — it holds a socket, and a runtime either turns up
// on it or does not. A product whose agent is optional cannot afford for the
// absent case to be the complicated one.

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNoAgent is what every send returns when nothing is connected.
//
// A named error rather than a bare string because callers act on it — the chat
// coordinator turns exactly this into "the agent is not running, here is how to
// start it" and must not turn a write failure on a live connection into the
// same message. Those are different situations and only one of them is the
// person's to fix.
var ErrNoAgent = errors.New("no agent runtime is connected")

// Status is what the daemon knows about the runtime right now.
//
// The three states are deliberate and not collapsible into one boolean.
// Connected-but-not-ready is the state that looks fine and is not: a runtime is
// there, it has a model name, and it cannot answer because nobody gave it a
// key. Reporting that as "unavailable" sends somebody to install software they
// already have; reporting it as "available" gives them a chat box that fails on
// the first message.
type Status struct {
	// Connected means a runtime holds the socket AND has said hello. An
	// accepted connection that has not introduced itself is neither, which is
	// why this is not simply "conn != nil".
	Connected bool `json:"connected"`

	// Ready means it can actually answer. False with Connected true is the
	// configuration case; Why says which.
	Ready bool `json:"ready"`

	// Why explains a false Ready in one sentence. Empty otherwise.
	Why string `json:"why,omitempty"`

	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Version  string `json:"version,omitempty"`

	// Models is the runtime's catalogue and Reachable the providers it can use.
	// Passed through untouched, for the browser: it is a different program in a
	// different language and cannot know either unless it is told.
	Models    []ModelInfo `json:"models,omitempty"`
	Reachable []string    `json:"reachable,omitempty"`

	// Commands is what the runtime says it can be asked to do. Passed through
	// so a browser builds its palette from the engine rather than from a copy.
	Commands []CommandInfo `json:"commands,omitempty"`
}

// same asks whether two statuses would draw the same panel.
//
// Written out because Status stopped being comparable when it grew the
// catalogue, and `!=` on a struct is exactly the kind of thing that goes from
// "works" to "does not compile" the moment somebody adds a slice — which is
// better than the alternative, where it would have kept compiling and started
// reporting a change on every message because two equal slices are two
// different backing arrays.
//
// The catalogue and the reachability list are compared by CONTENT for that
// reason. They almost never change; when they do — somebody signs in, a key
// lands — the panel has to hear about it, because that is the moment its model
// picker stops being wrong.
func (s Status) same(o Status) bool {
	if s.Connected != o.Connected || s.Ready != o.Ready || s.Why != o.Why ||
		s.Provider != o.Provider || s.Model != o.Model ||
		s.Mode != o.Mode || s.Version != o.Version {
		return false
	}
	if len(s.Reachable) != len(o.Reachable) || len(s.Models) != len(o.Models) ||
		len(s.Commands) != len(o.Commands) {
		return false
	}
	for i := range s.Commands {
		if s.Commands[i] != o.Commands[i] {
			return false
		}
	}
	for i := range s.Reachable {
		if s.Reachable[i] != o.Reachable[i] {
			return false
		}
	}
	for i := range s.Models {
		if s.Models[i] != o.Models[i] {
			return false
		}
	}
	return true
}

// Link is the listening socket and whatever runtime currently holds it.
type Link struct {
	path string

	mu     sync.Mutex
	conn   net.Conn
	status Status

	// onMessage receives everything the runtime sends after its hello. Set
	// before Listen and never afterwards, so no lock guards it.
	onMessage func(Envelope)

	// onChange fires whenever Status changes, so the daemon can tell the
	// browsers without polling. Called OUTSIDE the lock: it ends up in
	// Room.tellEveryone, which takes locks of its own, and holding both is how
	// this deadlocks the first time an agent reconnects while somebody joins.
	onChange func(Status)
}

// New builds a link. Callbacks are wired here rather than through setters so
// there is no window where the socket is open and nobody is listening to it.
func New(onMessage func(Envelope), onChange func(Status)) *Link {
	if onMessage == nil {
		onMessage = func(Envelope) {}
	}
	if onChange == nil {
		onChange = func(Status) {}
	}
	return &Link{onMessage: onMessage, onChange: onChange}
}

// SocketPath decides where the door goes.
//
// An explicit AGENT_SOCK wins. Otherwise it is derived from the MCP socket —
// same directory, a name of its own. That derivation is the point: whoever
// relocates the MCP socket into a shared volume is relocating "where this
// container's sockets live", and having to remember a second variable is how
// the two end up in different places, one of them inside a volume the runtime
// cannot see.
//
// An empty MCP socket and no AGENT_SOCK means no chat wire at all, which is the
// correct reading of a daemon that was not given an agent plane to begin with.
func SocketPath(agentSock, mcpSock string) string {
	if agentSock = strings.TrimSpace(agentSock); agentSock != "" {
		return agentSock
	}
	if mcpSock = strings.TrimSpace(mcpSock); mcpSock == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(mcpSock), "sentineldesk-agent.sock")
}

// Listen opens the socket and serves whatever turns up on it.
//
// Same shape as mcp.Server.Listen on purpose: a stale socket removed, 0600, and
// the accept loop on its own goroutine so the caller carries on booting. The
// desktop must never wait for an agent, not even for the length of a syscall.
func (l *Link) Listen(path string) error {
	_ = os.Remove(path) // a stale socket from an earlier run
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		log.Printf("agent: could not set 0600 on the socket: %v", err)
	}
	l.path = path
	log.Printf("agent: listening on %s for the agent runtime", path)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go l.serve(conn)
		}
	}()
	return nil
}

// Path is where the socket is, for whoever has to tell a person.
func (l *Link) Path() string { return l.path }

// Status reads the current state.
func (l *Link) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Send writes one message to the runtime.
//
// The lock is held across the write, which serialises senders. That is wanted:
// two goroutines interleaving halves of two JSON lines produces garbage on the
// far side, and the far side is a parser that would report it as a protocol
// error somewhere unrelated to the bug.
func (l *Link) Send(m Envelope) error {
	if m.At == 0 {
		m.At = time.Now().UnixMilli()
	}
	raw, err := encode(m)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil || !l.status.Connected {
		return ErrNoAgent
	}
	// A runtime that has stopped reading must not wedge the browser's input
	// path, which is where this is called from. The deadline turns a hung peer
	// into an error the panel can show instead of a chat box that never
	// responds to anything again.
	_ = l.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = l.conn.Write(raw)
	return err
}

// serve handles one runtime connection for its lifetime.
func (l *Link) serve(conn net.Conn) {
	defer conn.Close()

	// The newest connection wins, and the previous one is closed.
	//
	// Refusing the second would have been safer against a rogue process and
	// wrong against the case that actually happens: supervisord restarting the
	// runtime, where the new process can connect before the old one's socket
	// has been reaped. Refusing there would leave the chat dead until something
	// noticed — a failure mode with no error message and no way out except
	// another restart.
	l.mu.Lock()
	if old := l.conn; old != nil {
		log.Printf("agent: a second runtime connected; the previous one was dropped")
		_ = old.Close()
	}
	l.conn = conn
	// Deliberately NOT Connected yet: an accepted socket is not an agent. It
	// becomes one at hello, below.
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		mine := l.conn == conn
		if mine {
			l.conn, l.status = nil, Status{}
		}
		l.mu.Unlock()
		if mine {
			log.Printf("agent: the runtime disconnected")
			l.onChange(Status{})
		}
	}()

	// A line is one message. The buffer is generous because a delta carrying a
	// long paragraph is ordinary traffic here, and bufio's default would tear
	// it — reported, unhelpfully, as a closed connection.
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m Envelope
		if err := json.Unmarshal(line, &m); err != nil {
			// One bad line does not end the session. A runtime that has gone
			// genuinely wrong will produce a second, and the connection closing
			// is what reports that; a single malformed message is more often a
			// version skew than a broken peer.
			log.Printf("agent: could not read a message: %v", err)
			continue
		}

		switch m.T {
		case TypeHello:
			l.hello(conn, m)
		case TypeStatus:
			l.update(conn, m)
		default:
			// Everything else belongs to the coordinator. An unknown `t` reaches
			// it too and it ignores what it does not know, which is what lets a
			// newer runtime talk to an older daemon.
			l.onMessage(m)
		}
	}
}

// hello completes the handshake and answers it.
func (l *Link) hello(conn net.Conn, m Envelope) {
	if m.Protocol != 0 && m.Protocol != Protocol {
		log.Printf("agent: the runtime speaks protocol %d, this daemon speaks %d; "+
			"they may not understand each other", m.Protocol, Protocol)
	}
	l.update(conn, m)

	// Answered directly rather than through Send, because Send refuses when
	// nothing is connected and this is the message that says something is.
	raw, err := encode(Envelope{T: TypeWelcome, Protocol: Protocol,
		At: time.Now().UnixMilli()})
	if err != nil {
		return
	}
	l.mu.Lock()
	if l.conn == conn {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Write(raw)
	}
	l.mu.Unlock()
	if err != nil {
		log.Printf("agent: could not answer hello: %v", err)
	}
}

// update records what the runtime says about itself and reports the change.
func (l *Link) update(conn net.Conn, m Envelope) {
	next := Status{
		Connected: true,
		Ready:     m.Ready,
		Why:       strings.TrimSpace(m.Why),
		Provider:  m.Provider,
		Model:     m.Model,
		Mode:      m.Mode,
		Version:   m.Version,
		Models:    m.Models,
		Reachable: m.Providers,
		Commands:  m.Commands,
	}
	// A runtime that says it is not ready and does not say why leaves a panel
	// with a disabled box and no explanation, which is the shape of bug this
	// project calls a silent non-outcome. Filled in here so there is always
	// something to show.
	if !next.Ready && next.Why == "" {
		next.Why = "the agent runtime has no model configured"
	}

	l.mu.Lock()
	if l.conn != conn {
		// A message from the connection we already replaced. Dropped: acting on
		// it would let a dying runtime overwrite the status of the live one.
		l.mu.Unlock()
		return
	}
	changed := !l.status.same(next)
	l.status = next
	l.mu.Unlock()

	if changed {
		line := "agent: runtime " + describe(next)
		if next.Why != "" {
			line += " — " + next.Why
		}
		log.Print(line)
		l.onChange(next)
	}
}

// describe puts a status into a log line without a caller assembling one.
func describe(s Status) string {
	switch {
	case !s.Connected:
		return "gone"
	case s.Ready:
		return "ready on " + label(s)
	default:
		return "connected but not ready"
	}
}

func label(s Status) string {
	switch {
	case s.Provider != "" && s.Model != "":
		return s.Provider + "/" + s.Model
	case s.Model != "":
		return s.Model
	case s.Provider != "":
		return s.Provider
	}
	return "an unnamed model"
}
