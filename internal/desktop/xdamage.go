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

// Knowing the screen changed without looking at it.
//
// wait_for_idle answered "has the picture stopped moving?" by taking the
// picture: every 200ms it grabbed the whole framebuffer, PNG-encoded it, wrote
// it to a file, read the file back and hashed it. Fifteen seconds of waiting
// meant seventy-five of those, and the tool that exists to detect quiet was
// itself the busiest thing on the desktop while it ran — the CPU reading it
// took was CPU the same call then reported as a reason the desktop was not
// idle.
//
// X has kept this answer the whole time. The DAMAGE extension exists to tell a
// client which parts of a drawable have been painted since it last asked, and
// asking for ReportLevelNonEmpty reduces that to the only question this tool
// has: did anything at all change. The screen is never read, never encoded and
// never hashed; a timestamp is updated when the server says something moved.
//
// The subtlety worth knowing about DAMAGE is that the region accumulates. One
// notification arrives when the region goes from empty to non-empty, and no
// further notification comes until the region is subtracted back to empty. A
// watcher that forgets to subtract sees exactly one event and then silence
// forever, which reads as a permanently still desktop — the failure mode is
// silent and says the opposite of the truth, so the subtract in run() is the
// load-bearing line in this file.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/damage"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

// DamageWatcher records when the screen last changed.
type DamageWatcher struct {
	conn *xgb.Conn
	dmg  damage.Damage

	mu     sync.Mutex
	last   time.Time
	closed bool
}

// NewDamageWatcher starts watching the root window for paint.
//
// Its own connection, for the same reason the property watcher has one: a
// goroutine parked in WaitForEvent owns that connection's event stream. Damage
// on a busy desktop is also the highest-frequency event source here, and
// sharing a socket with request/reply would put a screenful of repaint between
// a caller and its answer.
func NewDamageWatcher(display string) (*DamageWatcher, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("X display %s: %w", display, err)
	}
	if err := damage.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("DAMAGE extension: %w", err)
	}
	// Negotiate the version before anything else, which the protocol requires
	// and which is not optional in the way "query the version" usually sounds:
	// without it the server answers the very next request with BadRequest.
	// Init only asks whether the extension exists, so the failure lands on
	// Create instead, several lines from its cause — and because the watcher
	// degrades to capturing the screen when it cannot start, the symptom was
	// not an error anywhere but wait_for_idle quietly staying expensive.
	if _, err := damage.QueryVersion(conn, 1, 1).Reply(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("negotiate DAMAGE version: %w", err)
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root

	id, err := damage.NewDamageId(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("allocate a damage id: %w", err)
	}
	// NonEmpty rather than the rectangle levels: this tool never asks *where*
	// the screen changed, only whether it did, and the rectangle levels would
	// deliver a geometry list per repaint for nobody to read.
	if err := damage.CreateChecked(conn, id, xproto.Drawable(root),
		damage.ReportLevelNonEmpty).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("watch the root window for damage: %w", err)
	}

	d := &DamageWatcher{conn: conn, dmg: id, last: time.Now()}
	go d.run()
	return d, nil
}

// run drains damage notifications, subtracting each so the next one can arrive.
func (d *DamageWatcher) run() {
	for {
		ev, err := d.conn.WaitForEvent()
		if ev == nil && err == nil {
			return // connection closed: Close, or X went away
		}
		if err != nil {
			continue
		}
		if _, ok := ev.(damage.NotifyEvent); !ok {
			continue
		}
		// Empty the region so the server will report the next paint. Both
		// regions are None: nothing here wants the damaged area handed back,
		// only the fact that there was one.
		damage.Subtract(d.conn, d.dmg, xfixes.Region(0), xfixes.Region(0))

		d.mu.Lock()
		d.last = time.Now()
		d.mu.Unlock()
	}
}

// LastChange is when the screen was last painted, as far as X has said.
func (d *DamageWatcher) LastChange() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// Close stops watching.
func (d *DamageWatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.mu.Unlock()
	d.conn.Close() // unblocks run
}

// QuietFor waits until the screen has been still for quiet, and reports whether
// it got there before timeout.
//
// It sleeps for exactly as long as the answer could still change rather than
// waking on a fixed interval. If the screen last moved 200ms ago and the caller
// wants 1200ms of quiet, the earliest possible success is a second away, so
// that is how long it sleeps; anything painted meanwhile pushes LastChange
// forward and the next pass computes a new, longer wait. A desktop that is
// already still answers in one sleep, and a desktop under constant repaint
// wakes once per quiet period rather than five times a second.
func (d *DamageWatcher) QuietFor(ctx context.Context, quiet, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		still := time.Since(d.LastChange())
		if still >= quiet {
			return true
		}
		wait := quiet - still
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		if wait <= 0 {
			return false
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return false
		}
	}
}
