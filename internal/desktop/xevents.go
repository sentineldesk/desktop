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

// Being told when the desktop changes, instead of asking over and over.
//
// Every "wait until" tool in the catalogue was a loop: run wmctrl, compare,
// sleep 300ms, repeat. Fifteen seconds of waiting for a window meant fifty
// processes spawned to be told nothing happened forty-nine times, and an answer
// that could arrive a third of a second after the fact. The window manager was
// announcing the change the whole time on a property nobody was listening to.
//
// X already publishes what these tools were reconstructing. The window manager
// maintains _NET_CLIENT_LIST, _NET_ACTIVE_WINDOW and _NET_CURRENT_DESKTOP on
// the root window, and asking for PropertyChangeMask there means the server
// sends a PropertyNotify the moment any of them is rewritten. The wait becomes
// a blocking read, the answer arrives in about a millisecond, and nothing is
// spawned at all.
//
// What travels on the channel is deliberately thin: which property changed, and
// nothing about how. Callers re-read the state they care about when they are
// woken. That looks wasteful and is the opposite — it makes every event
// idempotent, so two events coalescing into one, or one being dropped under
// load, costs a caller nothing but a wake-up it would have made anyway. A
// channel carrying the actual change could not be dropped safely, and would
// have to block the fan-out to guarantee delivery.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// WatchKind names the root-window property whose change woke the caller.
type WatchKind string

const (
	WatchWindows WatchKind = "windows" // _NET_CLIENT_LIST: a window appeared or went
	WatchActive  WatchKind = "active"  // _NET_ACTIVE_WINDOW: focus moved
	WatchDesktop WatchKind = "desktop" // _NET_CURRENT_DESKTOP: the desktop switched
)

// watched maps the property to the kind reported for it. Anything else arriving
// on the root window is ignored rather than forwarded: the window manager and
// the panel write a dozen properties there that no tool waits on, and waking
// every waiter for _NET_WORKAREA would turn a precise signal back into polling
// with extra steps.
var watched = map[string]WatchKind{
	"_NET_CLIENT_LIST":     WatchWindows,
	"_NET_ACTIVE_WINDOW":   WatchActive,
	"_NET_CURRENT_DESKTOP": WatchDesktop,
}

// Watcher fans root-window property changes out to everyone waiting on one.
type Watcher struct {
	conn  *xgb.Conn
	root  xproto.Window
	kinds map[xproto.Atom]WatchKind

	mu     sync.Mutex
	subs   map[int]chan WatchKind
	nextID int
	closed bool
}

// NewWatcher opens its own connection to the display.
//
// Deliberately not EWMH's. A goroutine parked in WaitForEvent owns that
// connection's event stream, and sharing it with the request/reply path means
// window queries and the event loop contend for the same socket — with the
// added trap that events nobody drains accumulate in xgb's queue whether or not
// anything is waiting. A second connection costs one file descriptor and makes
// the two concerns independent: a wedged event loop cannot stall a window
// query, and a burst of queries cannot delay an event.
func NewWatcher(display string) (*Watcher, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("X display %s: %w", display, err)
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root

	w := &Watcher{
		conn:  conn,
		root:  root,
		kinds: make(map[xproto.Atom]WatchKind, len(watched)),
		subs:  make(map[int]chan WatchKind),
	}
	for name, kind := range watched {
		reply, err := xproto.InternAtom(conn, true, uint16(len(name)), name).Reply()
		// only-if-exists is true above, so a window manager that has not
		// published a property yet returns atom zero rather than an error. That
		// is not fatal: the atom is interned the moment the property is first
		// written, and a desktop without virtual desktops legitimately never
		// writes _NET_CURRENT_DESKTOP at all.
		if err != nil || reply.Atom == 0 {
			continue
		}
		w.kinds[reply.Atom] = kind
	}
	if len(w.kinds) == 0 {
		conn.Close()
		return nil, fmt.Errorf("no EWMH properties on the root window: not an EWMH window manager")
	}

	// Ask for property changes on the root window. This is additive in spirit
	// but not in the protocol: ChangeWindowAttributes replaces the event mask
	// for this connection only, and every client has its own, so the window
	// manager's selections are untouched.
	if err := xproto.ChangeWindowAttributesChecked(conn, root,
		xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("select property events on the root window: %w", err)
	}

	go w.run()
	return w, nil
}

// Subscribe returns a channel of changes and the function that stops it.
//
// The channel is buffered and written without blocking. A caller that is busy
// misses events rather than holding up every other waiter, which is safe here
// precisely because an event carries no information beyond "look again" — a
// caller that missed one and re-reads state afterwards reaches the same
// conclusion it would have reached with the event in hand.
//
// The returned cancel is idempotent and must be called, or the subscription
// leaks for the life of the process.
func (w *Watcher) Subscribe() (<-chan WatchKind, func()) {
	ch := make(chan WatchKind, 8)

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		close(ch) // a closed watcher hands back a dead channel rather than nil
		return ch, func() {}
	}
	id := w.nextID
	w.nextID++
	w.subs[id] = ch
	w.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			w.mu.Lock()
			if sub, ok := w.subs[id]; ok {
				delete(w.subs, id)
				close(sub)
			}
			w.mu.Unlock()
		})
	}
}

// run is the event loop. It ends when the connection closes, which is what
// Close does to unblock it — xgb offers no other way to interrupt WaitForEvent.
func (w *Watcher) run() {
	for {
		ev, err := w.conn.WaitForEvent()
		if ev == nil && err == nil {
			return // the connection is gone: Close, or X went away
		}
		if err != nil {
			continue // a malformed event is not a reason to stop listening
		}
		pn, ok := ev.(xproto.PropertyNotifyEvent)
		if !ok || pn.Window != w.root {
			continue
		}
		kind, ok := w.kinds[pn.Atom]
		if !ok {
			continue
		}
		w.broadcast(kind)
	}
}

func (w *Watcher) broadcast(kind WatchKind) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.subs {
		select {
		case ch <- kind:
		default: // full: this subscriber will re-read state anyway
		}
	}
}

// Close stops the loop and drops every subscription.
func (w *Watcher) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	for id, ch := range w.subs {
		delete(w.subs, id)
		close(ch)
	}
	w.mu.Unlock()
	w.conn.Close() // unblocks run
}

// WaitFor blocks until check reports done, the context is cancelled, or the
// deadline passes. It wakes on the given kinds, and additionally every backstop
// if that is non-zero.
//
// check runs once before anything is waited on. That ordering is the whole
// correctness argument: between a caller deciding to wait and the subscription
// being live there is a window in which the very thing being waited for can
// happen, and a wait that subscribed first and looked second would sleep
// through it until the timeout. Checking first costs one extra read and closes
// the gap.
//
// The backstop covers what a root-window watcher structurally cannot see. These
// events fire when the window manager rewrites a property on the *root* window,
// which covers a window appearing or closing and focus moving. It does not
// cover a window that is already open changing its own title — that is
// _NET_WM_NAME on the window itself, and a caller matching on title would
// otherwise sleep through an application that maps its window first and names
// it a moment later, which is exactly what a browser does while a page loads.
// Watching every window for that would mean tracking the client list, selecting
// events on each new window and handling the ones destroyed between the two, so
// for now the honest arrangement is a slow tick alongside the fast path: the
// common case answers in a millisecond, the uncommon one within the backstop,
// and neither spawns a process.
//
// Cancellation is not optional here. This server answers notifications/cancelled
// mid-call, and a wait that ignored ctx would leave the caller waiting out the
// full timeout for an answer nobody is going to read.
func (w *Watcher) WaitFor(ctx context.Context, timeout, backstop time.Duration, check func() bool, kinds ...WatchKind) bool {
	if check() {
		return true
	}
	want := map[WatchKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}

	ch, cancel := w.Subscribe()
	defer cancel()

	// Check again now that the subscription is live, in case it landed in the
	// gap above.
	if check() {
		return true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var tick <-chan time.Time
	if backstop > 0 {
		t := time.NewTicker(backstop)
		defer t.Stop()
		tick = t.C
	}

	for {
		select {
		case kind, ok := <-ch:
			if !ok {
				return check() // watcher closed: one last look, then give up
			}
			if len(want) > 0 && !want[kind] {
				continue
			}
			if check() {
				return true
			}
		case <-tick:
			if check() {
				return true
			}
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		}
	}
}
