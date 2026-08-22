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

// The recorder's two jobs, and they pull against each other.
//
// It has to be complete enough that an agent reading it afterwards knows what a
// person did, and quiet enough that anybody opens it. A line per pointer sample
// satisfies the first and destroys the second, which is why the aggregation
// below is the substance of this file rather than an optimisation of it.
//
// The keystroke count is asserted the other way round: what must NOT be there.
// A desktop is where people type passwords, and this log is read by an agent
// that forwards what it reads to a model API.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recorderIn builds a Witness writing to a scratch file.
func recorderIn(t *testing.T) (*Witness, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	// No sweeper: the tests drive the flush themselves, so nothing races them.
	return &Witness{file: f}, path
}

func linesOf(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// TestABurstOfTypingIsOneLine is the aggregation, which is what makes the log
// readable at all. Forty keystrokes is one thing somebody did.
func TestABurstOfTypingIsOneLine(t *testing.T) {
	w, path := recorderIn(t)
	for i := 0; i < 40; i++ {
		w.Key("Viewer 1")
	}
	// A non-key event closes the burst.
	w.Pointer("Viewer 1", "clicked", 415, 301)

	got := linesOf(t, path)
	if len(got) != 2 {
		t.Fatalf("got %d lines for 40 keys and one click, want 2:\n%s",
			len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "typed") || !strings.Contains(got[0], "40 keys") {
		t.Errorf("the burst did not record its size: %s", got[0])
	}
	// Ordering: the typing happened BEFORE the click and has to be written
	// first, or the log reverses cause and effect in the one record somebody
	// reads to work out what caused what.
	if !strings.Contains(got[1], "clicked") {
		t.Errorf("the click was written before the typing it interrupted:\n%s",
			strings.Join(got, "\n"))
	}
}

// TestTheCharactersAreNeverWritten. The one deliberate hole in the history, and
// the reason it is deliberate: a verbatim keystroke log would put a sudo
// password in a model provider's request the first time somebody used sudo.
func TestTheCharactersAreNeverWritten(t *testing.T) {
	w, path := recorderIn(t)
	// Whatever the caller passes as an actor, the KEYS themselves never reach
	// this API — there is no parameter to put them in, which is the point. This
	// asserts that the shape of the record cannot be changed to carry them
	// without somebody noticing here.
	for i := 0; i < 8; i++ {
		w.Key("Viewer 1")
	}
	w.Note("Viewer 1", "took control", "")

	body := strings.Join(linesOf(t, path), "\n")
	if !strings.Contains(body, "8 keys") {
		t.Errorf("the count is missing, so an agent cannot tell typing happened: %s", body)
	}
	if !strings.Contains(body, "not recorded") {
		t.Errorf("the record does not say the characters were withheld, so a "+
			"reader cannot tell an empty log from a redacted one: %s", body)
	}
}

// TestADifferentPersonClosesTheBurst. Two people typing must not merge into one
// line attributed to whoever happened to be first.
func TestADifferentPersonClosesTheBurst(t *testing.T) {
	w, path := recorderIn(t)
	w.Key("Viewer 1")
	w.Key("Viewer 1")
	w.Key("Viewer 2")
	w.Note("Viewer 2", "took control", "")

	got := linesOf(t, path)
	if len(got) < 3 {
		t.Fatalf("got %d lines, want at least 3:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "Viewer 1") || !strings.Contains(got[0], "2 keys") {
		t.Errorf("the first person's burst is wrong: %s", got[0])
	}
	if !strings.Contains(got[1], "Viewer 2") {
		t.Errorf("the second person's burst is not theirs: %s", got[1])
	}
}

// TestOneRecordStaysOnOneLine. A window title can hold anything, including a
// newline, and a title that splits a record in two makes every entry after it
// unparseable — the reader is a tab-separated split, so it would silently start
// misreading fields rather than failing.
func TestOneRecordStaysOnOneLine(t *testing.T) {
	w, path := recorderIn(t)
	w.Note("Viewer 1", "focused", "a window\ntitled\tsomething\rodd")

	if got := linesOf(t, path); len(got) != 1 {
		t.Errorf("a title with newlines produced %d records:\n%s",
			len(got), strings.Join(got, "\n"))
	}
}

// TestALongDetailIsTruncated, so one pathological title cannot push the file
// past its retention cap on its own.
func TestALongDetailIsTruncated(t *testing.T) {
	w, path := recorderIn(t)
	w.Note("Viewer 1", "focused", strings.Repeat("x", 5000))

	got := linesOf(t, path)
	if len(got) != 1 {
		t.Fatalf("got %d lines", len(got))
	}
	if len(got[0]) > 400 {
		t.Errorf("a 5000-character title was written whole: %d bytes", len(got[0]))
	}
}

// TestTheSweepClosesAnAbandonedBurst. Somebody who types and then walks away
// would otherwise have their last line sitting unwritten until the next click,
// which could be tomorrow.
func TestTheSweepClosesAnAbandonedBurst(t *testing.T) {
	w, path := recorderIn(t)
	w.Key("Viewer 1")
	w.Key("Viewer 1")

	if got := linesOf(t, path); len(got) != 0 {
		t.Fatalf("the burst was written before it ended: %v", got)
	}

	// Reach in the way the sweeper does, rather than waiting out the real
	// interval: the behaviour under test is the flush, not the ticker.
	w.mu.Lock()
	w.burstAt = time.Now().Add(-time.Second)
	if w.burstKeys > 0 && time.Since(w.burstAt) >= keyBurstGap {
		w.flushLocked()
	}
	w.mu.Unlock()

	got := linesOf(t, path)
	if len(got) != 1 || !strings.Contains(got[0], "2 keys") {
		t.Errorf("an abandoned burst was never written: %v", got)
	}
}

// TestANilWitnessIsHarmless. Every capability here degrades instead of failing —
// no XShape disables peer pointers, an unwritable log disables the log — and
// the input path must not be the exception, because it runs on every mouse
// event of every session.
func TestANilWitnessIsHarmless(t *testing.T) {
	var w *Witness
	w.Key("Viewer 1")
	w.Pointer("Viewer 1", "clicked", 1, 2)
	w.Note("Viewer 1", "took control", "")

	// And one that exists but could not open its file.
	broken := &Witness{}
	broken.Key("Viewer 1")
	broken.Pointer("Viewer 1", "clicked", 1, 2)
	broken.Note("Viewer 1", "took control", "")
}
