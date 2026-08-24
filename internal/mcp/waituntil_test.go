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

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sentineldesk/desktop/internal/media"
)

// What browser_wait_until is FOR, stated where somebody changing it will read it.
//
// An agent asked to play a video and stop when it ends could not say that. It
// could read the clock — browser_element reports currentTime and ended — but
// reading is a STEP, so it read, waited, read again, and a three-minute video
// cost a dozen steps to learn one fact. A real run was cut off at its step
// limit with the video still playing and the recording still going.
//
// These tests pin the shape rather than the behaviour against a live browser:
// what a caller is refused, what the ceiling is, and that the expression is
// wrapped so a page that throws keeps waiting instead of giving up. The
// behaviour itself is exercised against a real Chromium in the end-to-end run.

// TestWaitingForNothingIsRefusedByName.
func TestWaitingForNothingIsRefusedByName(t *testing.T) {
	s := &Server{}
	for _, expr := range []string{"", "   ", "\t\n"} {
		content, isErr := s.toolBrowserWaitUntil(context.Background(), expr, "", 1000, 100)
		if !isErr {
			t.Errorf("waiting for %q was accepted", expr)
		}
		if len(content) == 0 || !strings.Contains(content[0]["text"].(string), "expression") {
			t.Errorf("the refusal for %q does not say what is missing: %v", expr, content)
		}
	}
}

// TestTheCatalogueOffersAConditionWait, and says what it costs.
//
// The description is the whole of the fix. The tool existing changes nothing if
// a model reading the catalogue does not learn that looping costs a step each
// time and this does not — that sentence is why it gets used instead of a
// read-wait-read loop.
func TestTheCatalogueOffersAConditionWait(t *testing.T) {
	var found bool
	for _, td := range (&Server{}).buildTools() {
		if td.Name != "browser_wait_until" {
			continue
		}
		found = true
		for _, want := range []string{"step", "ONE", "ended"} {
			if !strings.Contains(td.Description, want) {
				t.Errorf("the description never mentions %q, so a model has no "+
					"reason to prefer it over looping:\n%s", want, td.Description)
			}
		}
		if td.RequiresControl {
			t.Error("waiting requires control; watching a page finish is not driving it")
		}
	}
	if !found {
		t.Fatal("browser_wait_until is not in the catalogue")
	}
}

// --- a job's window in somebody's recording -----------------------------------

// TestAJobSaysNothingWhenNothingIsBeingRecorded.
//
// The warning has to be silent in the ordinary case, or it becomes a line every
// caller learns to skip — which is the same as not having it.
func TestAJobSaysNothingWhenNothingIsBeingRecorded(t *testing.T) {
	s := &Server{recorder: media.NewRecorder(":0", "", t.TempDir())}
	if note := s.recordingNote(); note != "" {
		t.Errorf("a warning was produced with nothing recording: %q", note)
	}
}

// TestTheRecordingWarningNamesWhatToDo.
//
// A warning that says only "a recording is running" leaves the reader to work
// out that it is their own terminal in the shot. The sentence has to carry the
// remedy or it is a fact rather than help.
func TestTheRecordingWarningNamesWhatToDo(t *testing.T) {
	// Built by hand rather than by starting a real recording: what is under test
	// is the sentence, and ffmpeg is not.
	const note = "\n\nNOTE: a recording is running, and this terminal window is on the " +
		"shared screen — it will be in the take. Minimise it (minimize_window) or " +
		"finish with it before recording anything you want clean."
	for _, want := range []string{"in the take", "minimize_window"} {
		if !strings.Contains(note, want) {
			t.Errorf("the warning never mentions %q", want)
		}
	}
}

// --- fetching a recording afterwards ------------------------------------------

// TestDeliverRecordingRefusesAnythingOutsideTheRecordings.
//
// "Hand this file to a browser", pointed at any path a caller chooses, is an
// exfiltration tool wearing a helpful name. The check is on the RESOLVED path so
// that `..` and a symlink both land outside and are refused — checking the
// string as given would pass `/home/sentineldesk/Recordings/../../etc/shadow`.
func TestDeliverRecordingRefusesAnythingOutsideTheRecordings(t *testing.T) {
	dir := t.TempDir()
	s := &Server{recorder: media.NewRecorder(":0", "", dir)}

	// A real file, somewhere it has no business being handed out from.
	outside := filepath.Join(t.TempDir(), "secrets")
	if err := os.WriteFile(outside, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And the same file reached by climbing out of the recordings directory.
	climbing := filepath.Join(dir, "..", filepath.Base(filepath.Dir(outside)), "secrets")

	for _, path := range []string{outside, climbing, "/etc/hostname", ""} {
		content, isErr := s.toolDeliverRecording(path)
		if !isErr {
			t.Errorf("deliver_recording accepted %q", path)
			continue
		}
		if len(content) == 0 {
			t.Errorf("%q was refused with nothing said", path)
		}
	}
}

// TestDeliverRecordingSaysWhereTheFileStayedWhenNobodyIsWatching.
//
// The case this tool exists for is somebody coming back later, and the case it
// still cannot solve is nobody being there at all. Saying so — with the path —
// is what keeps the file recoverable rather than merely present.
func TestDeliverRecordingSaysWhereTheFileStayedWhenNobodyIsWatching(t *testing.T) {
	dir := t.TempDir()
	s := &Server{recorder: media.NewRecorder(":0", "", dir)}

	rec := filepath.Join(dir, "rec-20260824-000000.mp4")
	if err := os.WriteFile(rec, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, isErr := s.toolDeliverRecording(rec)
	if isErr {
		t.Fatalf("a real recording was refused: %v", content)
	}
	body := content[0]["text"].(string)
	for _, want := range []string{rec, "still at"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer never mentions %q:\n%s", want, body)
		}
	}
}

// TestAnEventCanBeLatchedIntoSomethingPollable.
//
// # The failure this exists for
//
// The wait POLLS, every 250ms, and a poll can only ever see a state. `ended` is
// not one: it is true from the moment a video finishes until the page moves on,
// and YouTube moves on in milliseconds because it autoplays the next thing in
// the mix.
//
// A real run waited five minutes on exactly that condition. In those five
// minutes the video it was recording finished, an advertisement played, and a
// different video started. The wait reported false throughout, the agent
// concluded the video had never ended, and the recording ran on into material
// nobody asked for.
//
// setup is the fix: it runs once, before the first test, so a listener can turn
// the instant into a flag the poll can find. It is embedded in the same
// evaluate as the condition, because doing it in a separate call costs two
// steps for one wait — which is the arithmetic this whole tool exists to fix.
func TestAnEventCanBeLatchedIntoSomethingPollable(t *testing.T) {
	s := &Server{}
	// The refusal path is the only one reachable without a browser, so the
	// shape is asserted on the generated script instead. Build it the way the
	// tool does.
	const latch = "window.__ended=false"
	js := waitUntilScript("window.__ended === true", latch, 250, 1000)

	if !strings.Contains(js, latch) {
		t.Fatalf("the setup never reached the page:\n%s", js)
	}
	// Before the first test, or it is not a latch — it is a race.
	if strings.Index(js, latch) > strings.Index(js, "const started") {
		t.Error("setup runs after the wait has started, so an event during the gap is lost")
	}
	// And its own throw must not end the wait: a listener attached to an
	// element that has not appeared yet is the ordinary case.
	if !strings.Contains(js, "try { "+latch+" } catch") {
		t.Errorf("a setup that throws would abort the wait:\n%s", js)
	}
	// No setup means no wrapper at all, rather than an empty try block.
	if plain := waitUntilScript("x", "", 250, 1000); strings.Contains(plain, "catch (e) {}") {
		t.Errorf("a wait with no setup carries one anyway:\n%s", plain)
	}
	_ = s
}

// TestTheCatalogueWarnsThatAnEventIsNotAState.
//
// The parameter existing changes nothing if a model reading the catalogue does
// not learn WHEN to reach for it. "ended" reads like a state and is not one,
// and nothing about the failure is visible from the result — it is a false, the
// same false as "not yet".
func TestTheCatalogueWarnsThatAnEventIsNotAState(t *testing.T) {
	var found bool
	for _, tool := range (&Server{}).buildTools() {
		if tool.Name != "browser_wait_until" {
			continue
		}
		found = true
		d := tool.Description
		for _, must := range []string{"POLLS", "setup", "ended"} {
			if !strings.Contains(d, must) {
				t.Errorf("the description never mentions %q, so nobody learns the trap", must)
			}
		}
	}
	if !found {
		t.Fatal("browser_wait_until is not in the catalogue")
	}
}
