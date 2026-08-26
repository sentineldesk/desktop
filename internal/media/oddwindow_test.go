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

// The zero-byte recording, and the lie that covered for it.
//
// A window 1271 pixels wide has no representation in I420 — one chroma sample
// per 2x2 block needs even sides — so x264 refused to initialise and the
// pipeline died two milliseconds after PLAYING. That alone would have been a
// bug; what made it an incident is that Start had already returned a path and
// no error, Status went on saying `recording: true`, and an agent relayed both
// to a person as fact. Three zero-byte files in one afternoon, each reported
// as a recording in progress.
//
// The failure this project ranks above a crash is the tool that reports
// success for work it did not do. These tests hold the two doors shut: the
// pipeline must carry the caps that make any window size encodable, and Start
// must not survive a pipeline that did not.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAnyWindowSizeIsEncodable.
//
// The caps carry a step of 2 so videoscale negotiates the nearest even size —
// 1271 becomes 1270 — for ANY window, including one resized mid-take, without
// the pipeline ever knowing a geometry. Asserted on the description because
// dropping the caps would not fail any run on an even screen; it would wait
// for the next odd window.
func TestAnyWindowSizeIsEncodable(t *testing.T) {
	for _, o := range []RecordOpts{
		{FPS: 30},
		{FPS: 30, XID: 27262979}, // the Chromium window that produced the zero-byte files
	} {
		if got := desc(o); !strings.Contains(got, evenSize) {
			t.Errorf("the pipeline no longer forces even dimensions (xid=%d):\n%s", o.XID, got)
		}
	}
	// videoscale is what turns the caps from a refusal into a resize. Caps
	// without it would trade "dies on odd windows" for "dies on odd windows,
	// with a different message".
	if got := desc(RecordOpts{FPS: 30}); !strings.Contains(got, "videoscale") {
		t.Errorf("the caps have nothing to satisfy them — videoscale is gone:\n%s", got)
	}
}

// TestStartDoesNotReportARecordingThatDied.
//
// cmd.Start() reports the fork, not the pipeline. The health check waits long
// enough to catch the immediate death — measured at ~2ms — and returns the
// reason gst printed instead of a path. gst-launch-1.0 is not on the machines
// that run this test, which is exactly the failure shape: the child exits at
// once, and before the fix this Start returned success anyway.
func TestStartDoesNotReportARecordingThatDied(t *testing.T) {
	r := NewRecorder(":0", "mic", t.TempDir())
	_, err := r.Start(RecordOpts{})
	if err == nil {
		t.Fatal("Start reported a recording on a machine where the recorder cannot run")
	}
	if s := r.Status(); s["recording"] == true {
		t.Errorf("Status still claims a recording after Start refused: %v", s)
	}
}

// TestStatusNoticesARecorderThatExited.
//
// `recording: true` used to mean "Start once succeeded". It has to mean "the
// process is writing frames right now", because get_recording_status is the
// one call an agent has for CHECKING — and a check that cannot come back false
// converts a careful caller into a confident wrong one.
func TestStatusNoticesARecorderThatExited(t *testing.T) {
	r := NewRecorder(":0", "mic", t.TempDir())
	// A recorder mid-recording whose process has already gone: the done
	// channel is closed, as the reaper closes it the moment Wait returns.
	closed := make(chan struct{})
	close(closed)
	r.done = closed
	r.path = "/tmp/x.mp4"
	r.container = "mp4"
	r.startedAt = time.Now()
	// cmd only has to be non-nil to get past the not-recording branch; the
	// liveness check reads the channel, never the process.
	r.cmd = exec.Command("true")
	s := r.Status()
	if s["recording"] != false {
		t.Errorf("a dead recorder still reports recording: %v", s)
	}
	if s["stopped"] == nil {
		t.Errorf("the status does not say the recorder exited: %v", s)
	}
}

// TestADeadRecorderDoesNotBlockTheNextOne.
//
// Found live, minutes after the honest Status shipped: gst killed behind the
// recorder's back, Status correctly answering `recording: false` — and Start
// still refusing with "a recording is already in progress". Two answers to one
// question, and the second one blocked every future take behind a process that
// no longer existed, because the only cleanup lived in Stop and a self-
// inflicted death never gets a Stop.
func TestADeadRecorderDoesNotBlockTheNextOne(t *testing.T) {
	r := NewRecorder(":0", "mic", t.TempDir())
	closed := make(chan struct{})
	close(closed)
	r.cmd = exec.Command("true")
	r.done = closed
	r.path = "/tmp/x.mp4"
	// Start must reap the corpse and get as far as trying to record — which on
	// a machine without gst fails for that reason, not with "in progress".
	_, err := r.Start(RecordOpts{})
	if err == nil {
		t.Fatal("Start succeeded on a machine with no recorder — the test premise is gone")
	}
	if strings.Contains(err.Error(), "already in progress") {
		t.Errorf("a dead recorder still blocks the next recording: %v", err)
	}
}

// TestAStatusCheckRestoresWhatACleanTakeHid.
//
// The name tags a clean take hides are real windows on the shared screen. The
// Show lived only in Stop, so a recorder that died mid-take left them hidden
// for everyone until somebody happened to start another recording. Reading the
// status is enough to notice the death, so it is enough to undo it.
func TestAStatusCheckRestoresWhatACleanTakeHid(t *testing.T) {
	r := NewRecorder(":0", "mic", t.TempDir())
	ov := &fakeOverlays{}
	r.Overlays = ov
	closed := make(chan struct{})
	close(closed)
	r.cmd = exec.Command("true")
	r.done = closed
	r.path = "/tmp/x.mp4"
	r.cleaned = true
	if s := r.Status(); s["recording"] != false {
		t.Fatalf("the dead recorder went unnoticed: %v", s)
	}
	// The restore is deliberately asynchronous — Show under the recorder's
	// lock is the deadlock shape the started event actually had — so the
	// assertion waits for it rather than racing it.
	deadline := time.Now().Add(2 * time.Second)
	for !ov.wasShown() {
		if time.Now().After(deadline) {
			t.Fatal("the pointers a clean take hid were not restored")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := r.Start(RecordOpts{}); err != nil &&
		strings.Contains(err.Error(), "already in progress") {
		t.Errorf("the reap did not clear the state: %v", err)
	}
}

type fakeOverlays struct {
	mu    sync.Mutex
	shown bool
}

func (f *fakeOverlays) Hide() {}
func (f *fakeOverlays) Show() {
	f.mu.Lock()
	f.shown = true
	f.mu.Unlock()
}
func (f *fakeOverlays) wasShown() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shown
}

// TestStartedEventDoesNotDeadlockTheRecorder is the regression for a bug that
// every unit test missed and the first real recording found: the "started"
// event was emitted with a defer, defers run before the unlock deferred above
// them, and tell takes the lock the caller still held. The whole recorder hung
// — every recording tool on the desktop — behind a mutex nobody was going to
// release.
//
// The test recreates the REAL path: a fake gst-launch-1.0 on PATH that stays
// alive past the health check, a watcher that re-enters the recorder the way
// the event hub does, and a deadline, because the failure mode is not an error
// but an eternity.
func TestStartedEventDoesNotDeadlockTheRecorder(t *testing.T) {
	shim := t.TempDir()
	fake := filepath.Join(shim, "gst-launch-1.0")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := NewRecorder(":99", "mic", t.TempDir())
	started := make(chan map[string]any, 1)
	r.Watch(func(kind string, detail map[string]any) {
		if kind != "started" {
			return
		}
		// Re-enter the recorder from the watcher, exactly as the event hub's
		// subscriber may: with tell called under the lock this line is the
		// deadlock, and without the timeout below it would hang the suite.
		_ = r.Status()
		started <- detail
	})

	done := make(chan error, 1)
	go func() {
		_, err := r.Start(RecordOpts{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the fake pipeline did not start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start never returned — the started event deadlocked the recorder")
	}
	select {
	case ev := <-started:
		if ev["path"] == nil {
			t.Errorf("the started event carries no path: %v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the started event never arrived")
	}
	if _, _, err := r.Stop(); err != nil {
		t.Errorf("stopping the fake recording failed: %v", err)
	}
}
