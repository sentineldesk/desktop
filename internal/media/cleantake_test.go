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

package media

// A recording that came out wrong is the failure this file guards, and it has a
// shape worth naming: it does not fail. The pipeline runs, the file is written,
// the size is right, and nobody finds out until somebody sits down to watch it —
// minutes later, and usually not the person who recorded it.
//
// So the assertions are on the pipeline description rather than on a running
// gst-launch: what a test can check here is that the option somebody passed
// reached the thing that decides what ends up in the frame.

import (
	"os/exec"
	"strings"
	"testing"
)

func desc(o RecordOpts) string {
	return pipelineFor(":0", "mic", "mp4mux", "/tmp/x.mp4", "x264enc", "faac", o)
}

// TestTheCursorIsInTheTakeUnlessAskedOtherwise.
//
// show-pointer was hard-coded true. That is right for a record of somebody
// working — the pointer is half of what they did — and wrong for a video meant
// to be watched, where it sits over the picture for the whole take with nothing
// to be done about it.
//
// Default unchanged on purpose: the common use is witnessing work.
func TestTheCursorIsInTheTakeUnlessAskedOtherwise(t *testing.T) {
	if got := desc(RecordOpts{FPS: 30}); !strings.Contains(got, "show-pointer=true") {
		t.Errorf("an ordinary recording lost the pointer:\n%s", got)
	}
	if got := desc(RecordOpts{FPS: 30, Clean: true}); !strings.Contains(got, "show-pointer=false") {
		t.Errorf("a clean take still draws the cursor:\n%s", got)
	}
}

// TestOneWindowInsteadOfTheWholeScreen.
//
// The answer to "something might pop up in the middle of my recording" that
// does not depend on predicting what. What is not inside that window cannot be
// in the frame.
func TestOneWindowInsteadOfTheWholeScreen(t *testing.T) {
	whole := desc(RecordOpts{FPS: 30})
	if strings.Contains(whole, "xid=") {
		t.Errorf("a full-screen recording named a window:\n%s", whole)
	}
	one := desc(RecordOpts{FPS: 30, XID: 0x1800003})
	if !strings.Contains(one, "xid=25165827") {
		t.Errorf("the window never reached the source:\n%s", one)
	}
	// And it is still the same pipeline in every other respect — a window
	// recording that quietly lost its audio would be a different bug wearing
	// this one's clothes.
	if !strings.Contains(desc(RecordOpts{FPS: 30, XID: 7, Audio: true}), "pulsesrc") {
		t.Error("recording one window dropped the audio branch")
	}
}

// overlaySpy stands in for the who-is-driving name tags.
type overlaySpy struct{ hidden, shown int }

func (o *overlaySpy) Hide() { o.hidden++ }
func (o *overlaySpy) Show() { o.shown++ }

// TestTheNameTagsComeBackByThemselves.
//
// Hiding them is a change to what EVERYBODY sees, made for the duration of one
// recording. A recording can end in ways nobody asked for — a stop from the
// toolbar, a run cut off part-way — so putting them back cannot be left to
// whoever asked for the take: it belongs to whatever ends the recording.
//
// Driven through a real Stop against a stand-in process, because the thing
// worth pinning is the ORDER and the ownership inside Stop, and a test that
// set the fields and called the restore itself would pass with Stop deleted.
func TestTheNameTagsComeBackByThemselves(t *testing.T) {
	spy := &overlaySpy{}
	r := &Recorder{Overlays: spy}

	// A stop with nothing running must not touch them.
	if _, _, err := r.Stop(); err == nil {
		t.Error("stopping nothing was accepted")
	}
	if spy.shown != 0 {
		t.Errorf("a stop with no recording showed the tags %d time(s)", spy.shown)
	}

	// A recording that hid them puts them back.
	stopping(t, r, true)
	if _, _, err := r.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if spy.shown != 1 {
		t.Errorf("the tags were restored %d time(s), want exactly 1 — "+
			"a temporary request left a permanent change", spy.shown)
	}

	// A recording that did NOT hide them must not reveal them on the way out.
	// Somebody else may have hidden them for their own reasons, and this
	// recording ending is not a reason to undo that.
	spy.shown = 0
	stopping(t, r, false)
	if _, _, err := r.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if spy.shown != 0 {
		t.Errorf("an ordinary recording revealed the tags %d time(s)", spy.shown)
	}
}

// stopping puts the recorder into the state Stop expects, around a stand-in
// process that answers a signal the way gst-launch does.
func stopping(t *testing.T, r *Recorder, cleaned bool) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("no sleep to stand in for gst: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	r.mu.Lock()
	r.cmd, r.done, r.path, r.cleaned = cmd, done, "/tmp/x.mp4", cleaned
	r.mu.Unlock()
}
