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

// What the people did, written down so the agent can read it.
//
// The desktop already recorded one side of itself in full — every tool call the
// agent made, with arguments and outcome — and almost nothing of the other. The
// agent could be stopped by a person, told "look at what I did instead", and had
// only a screenshot of the end state to work from.
//
// # Aggregated, not sampled
//
// A pointer emits events at the rate the browser can send them. Writing each one
// down produces a file that is enormous, unreadable, and — this is the part that
// matters — LESS informative than a summary: "moved to 412,300; moved to
// 413,301; moved to 415,301" tells a reader nothing that "clicked at 415,301 on
// Files" does not, and buries it.
//
// So movement is not an event. It is context that gets attached to the events
// that are: clicks, drags, scrolls, keys, the clipboard. A burst of typing
// becomes one line when it stops. This is what makes the log worth opening.
//
// # Keystrokes are counted, not captured
//
// The one deliberate hole, and it is deliberate: this records THAT somebody
// typed and how much, never what. A desktop is where people type sudo passwords,
// SSH passphrases and bank logins, and this log is read by an agent that sends
// what it reads to a model API. A verbatim keystroke log would put a password in
// a third party's request within a day of somebody enabling it, and no retention
// policy fixes that — the copy has left.
//
// The loss is smaller than it looks. What an agent needs is "the person typed 14
// characters into a window called Authentication Required", which is exactly the
// understanding required, and is what is written. Where the text genuinely
// matters — commands — it is already captured verbatim by shell-report.sh,
// because a shell command is a public act on a shared screen in a way a password
// field is not.
package stream

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// witnessPath is the file internal/mcp merges into the desktop's timeline. The
// two have to agree on it and on the tab-separated shape below.
const witnessPath = "/tmp/sentineldesk/input.log"

// keyBurstGap is how long a pause has to be before typing counts as finished.
//
// Half a second: long enough that ordinary typing stays one line, short enough
// that two separate things a person typed do not merge into one. It is also the
// worst case for how stale the log can be, which is why it is not longer.
const keyBurstGap = 500 * time.Millisecond

// Witness records what people do, in a form somebody would actually read.
type Witness struct {
	mu   sync.Mutex
	file *os.File

	// The open typing burst: who, where, how many keys, and when the last one
	// landed. Flushed by the next non-key event, by a change of actor, or by the
	// sweeper when the pause is long enough.
	burstWho   string
	burstKeys  int
	burstAt    time.Time
	burstFirst time.Time
}

// NewWitness opens the log, degrading to a recorder that writes nothing.
//
// Same rule as every other optional capability here: no XShape disables peer
// pointers, an unwritable log disables the log. Refusing to start a desktop
// because its history file is in the wrong place would be the wrong trade — but
// it says so on stderr, because a silently absent audit trail is worse than an
// absent one somebody was told about.
func NewWitness() *Witness {
	w := &Witness{}
	if err := os.MkdirAll(filepath.Dir(witnessPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "stream: activity not recorded, %s: %v\n", witnessPath, err)
		return w
	}
	f, err := os.OpenFile(witnessPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stream: activity not recorded, %s: %v\n", witnessPath, err)
		return w
	}
	w.file = f
	go w.sweep()
	return w
}

// write appends one line: time, actor, what, detail.
func (w *Witness) write(who, what, detail string) {
	if w == nil || w.file == nil {
		return
	}
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n",
		time.Now().UTC().Format(time.RFC3339), sanitise(who), what, sanitise(detail))
	_, _ = w.file.WriteString(line)
}

// sanitise keeps one record on one line. A window title can contain anything,
// including a newline, and a title that splits a record in two makes every
// entry after it unparseable.
func sanitise(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}

// Note records something that is already one event.
func (w *Witness) Note(who, what, detail string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.flushLocked()
	w.mu.Unlock()
	w.write(who, what, detail)
}

// Key folds one keystroke into the open burst.
func (w *Witness) Key(who string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.burstWho != who {
		w.flushLocked()
		w.burstWho, w.burstFirst = who, time.Now()
	}
	if w.burstKeys == 0 {
		w.burstFirst = time.Now()
	}
	w.burstKeys++
	w.burstAt = time.Now()
}

// Pointer records a click, drag or scroll. Movement between them is not an
// event and is not recorded; the coordinates here are where the act landed.
func (w *Witness) Pointer(who, what string, x, y int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.flushLocked()
	w.mu.Unlock()
	w.write(who, what, fmt.Sprintf("at %d,%d", x, y))
}

// flushLocked closes an open typing burst. The caller holds the lock.
func (w *Witness) flushLocked() {
	if w.burstKeys == 0 {
		return
	}
	keys, who := w.burstKeys, w.burstWho
	secs := int(w.burstAt.Sub(w.burstFirst).Seconds())
	w.burstKeys = 0

	// Written without the lock would be nicer, but the file write is a single
	// append to a buffered descriptor and the alternative is an ordering bug:
	// a burst flushed after the event that flushed it reads as having happened
	// afterwards, which reverses cause and effect in the one log somebody
	// consults to work out what caused what.
	w.write(who, "typed",
		fmt.Sprintf("%d keys over %ds — the characters are deliberately not recorded", keys, secs))
}

// sweep closes a burst that ended without anything following it — somebody who
// typed and then walked away. Without this their last line would sit unwritten
// until the next click, which could be tomorrow.
func (w *Witness) sweep() {
	ticker := time.NewTicker(keyBurstGap)
	defer ticker.Stop()
	for range ticker.C {
		w.mu.Lock()
		if w.burstKeys > 0 && time.Since(w.burstAt) >= keyBurstGap {
			w.flushLocked()
		}
		w.mu.Unlock()
	}
}
