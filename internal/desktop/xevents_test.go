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

package desktop

// The waiting logic, tested without an X server.
//
// NewWatcher needs a display and so cannot run here, but everything that
// decides whether a caller wakes, keeps waiting or gives up is independent of
// where the events came from. Building the struct directly — legal inside the
// package — exercises that logic anywhere, which is the bar the rest of this
// project's tests hold to: no X, no pipeline, no container.
//
// What is deliberately not covered: that PropertyNotify on the root window
// actually arrives. That is X's contract, not this code's, and asserting it
// would need the display these tests exist to avoid.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newTestWatcher builds a Watcher with no connection. Every method used below
// touches only the mutex and the subscriber map; Close is never called, because
// that is the one path that would dereference the nil connection.
func newTestWatcher() *Watcher {
	return &Watcher{subs: make(map[int]chan WatchKind)}
}

func TestWaitForReturnsBeforeSubscribing(t *testing.T) {
	w := newTestWatcher()
	// A caller whose condition already holds must not wait for an event that
	// has, by definition, already happened. Before this ordering existed the
	// wait would have hung until the timeout on a window that was already open.
	start := time.Now()
	ok := w.WaitFor(context.Background(), time.Minute, 0, func() bool { return true }, WatchWindows)
	if !ok {
		t.Fatal("WaitFor returned false for a condition that was already true")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %v for a condition that was already true", elapsed)
	}
	if len(w.subs) != 0 {
		t.Fatalf("left %d subscriptions behind", len(w.subs))
	}
}

func TestWaitForWakesOnEvent(t *testing.T) {
	w := newTestWatcher()
	var mu sync.Mutex
	ready := false

	go func() {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		ready = true
		mu.Unlock()
		w.broadcast(WatchWindows)
	}()

	ok := w.WaitFor(context.Background(), 5*time.Second, 0, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return ready
	}, WatchWindows)
	if !ok {
		t.Fatal("WaitFor did not wake on the event it was waiting for")
	}
}

func TestWaitForIgnoresOtherKinds(t *testing.T) {
	w := newTestWatcher()
	checks := 0
	var mu sync.Mutex

	go func() {
		// Focus moving is not a window appearing. A waiter asking only about
		// WatchWindows must sleep through this rather than re-reading the
		// window list every time the pointer crosses a title bar.
		for range 5 {
			w.broadcast(WatchActive)
			time.Sleep(10 * time.Millisecond)
		}
	}()

	w.WaitFor(context.Background(), 200*time.Millisecond, 0, func() bool {
		mu.Lock()
		checks++
		mu.Unlock()
		return false
	}, WatchWindows)

	mu.Lock()
	defer mu.Unlock()
	// Two: once before subscribing, once after. Any more means an unrelated
	// kind woke the caller.
	if checks > 2 {
		t.Fatalf("checked %d times; WatchActive events woke a WatchWindows waiter", checks)
	}
}

func TestWaitForHonoursCancellation(t *testing.T) {
	w := newTestWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// The timeout is a minute. If cancellation is ignored this test hangs for
	// that long, which is exactly what a cancelled tool call would do to the
	// caller waiting on an answer nobody will read.
	ok := w.WaitFor(ctx, time.Minute, 0, func() bool { return false }, WatchWindows)
	if ok {
		t.Fatal("WaitFor reported success after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v to take effect", elapsed)
	}
}

func TestWaitForBackstopCatchesSilentChange(t *testing.T) {
	w := newTestWatcher()
	var mu sync.Mutex
	ready := false
	go func() {
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		ready = true
		mu.Unlock()
		// Deliberately no broadcast: this stands in for a window that renames
		// itself, which the root-window watcher cannot see at all.
	}()

	ok := w.WaitFor(context.Background(), 5*time.Second, 20*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return ready
	}, WatchWindows)
	if !ok {
		t.Fatal("the backstop did not catch a change that fired no event")
	}
}

func TestWaitForTimesOut(t *testing.T) {
	w := newTestWatcher()
	if w.WaitFor(context.Background(), 60*time.Millisecond, 0, func() bool { return false }, WatchWindows) {
		t.Fatal("WaitFor reported success on a condition that never held")
	}
	if len(w.subs) != 0 {
		t.Fatalf("left %d subscriptions behind after a timeout", len(w.subs))
	}
}

func TestSubscribeCancelIsIdempotent(t *testing.T) {
	w := newTestWatcher()
	_, cancel := w.Subscribe()
	if len(w.subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(w.subs))
	}
	cancel()
	// A second cancel closing an already-closed channel would panic and take
	// the daemon with it. defer cancel() alongside an early explicit one is an
	// ordinary enough pattern that this must be safe.
	cancel()
	if len(w.subs) != 0 {
		t.Fatalf("cancel left %d subscriptions", len(w.subs))
	}
}

func TestBroadcastDoesNotBlockOnAFullSubscriber(t *testing.T) {
	w := newTestWatcher()
	_, cancel := w.Subscribe()
	defer cancel()

	// Nobody is reading. Past the buffer, every further event must be dropped
	// rather than stalling the loop: one wedged waiter cannot be allowed to
	// stop every other waiter on the desktop from being told anything.
	done := make(chan struct{})
	go func() {
		for range 100 {
			w.broadcast(WatchWindows)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a subscriber that was not reading")
	}
}

func TestSubscribeAfterCloseIsUsable(t *testing.T) {
	w := newTestWatcher()
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()

	ch, cancel := w.Subscribe()
	defer cancel()
	// A closed channel rather than a nil one: a caller doing <-ch on nil blocks
	// forever, which is the opposite of what a shut-down watcher should cause.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("a closed watcher delivered an event")
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe on a closed watcher returned a channel that blocks")
	}
}
