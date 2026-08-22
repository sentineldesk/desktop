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

// Whether anybody can actually SEE a window, asked of X rather than inferred.
//
// Windows() already answers "what windows exist", and for a taskbar that is the
// whole question. It is not the question the agent's visibility promise rests
// on. A window can exist and be unreadable in at least five ordinary ways:
//
//	unmapped    minimised, or on a desktop the manager has unmapped
//	hidden      _NET_WM_STATE_HIDDEN, which is what a manager sets when it
//	            iconifies something
//	shaded      rolled up into its title bar — mapped, sized, and showing one
//	            row of decoration and nothing else
//	elsewhere   on a virtual desktop that is not the one being displayed
//	off-screen  moved past the edge, which nothing in X forbids
//	covered     underneath something opaque and larger
//
// Each of those was a way for a command to run where nobody could watch it while
// the daemon reported the terminal as visible, because the only test it applied
// was "tmux says a client is attached". A client is a pty. A pty is not a pair
// of eyes.
//
// So this reads the facts that decide it, in one pass, and hands them back
// without judging: the judgement is a policy question and belongs where the
// policy is, in internal/mcp. What belongs here is the X protocol.
//
// Windows are returned BOTTOM TO TOP where the manager publishes a stacking
// order, because occlusion is the one property that cannot be read off a single
// window — it is a relation between a window and everything drawn after it.

import (
	"fmt"
	"strings"

	"github.com/jezek/xgb/xproto"
)

// OnScreen is one managed window, with the facts that decide whether a person
// sharing this desktop could read it.
//
// Deliberately not WindowInfo with more fields on it. WindowInfo is the shape
// list_windows returns to the agent, and quietly growing a `mapped` field there
// would change a tool's output as a side effect of an internal need.
type OnScreen struct {
	ID      string `json:"id"`
	Class   string `json:"class"`
	PID     int    `json:"pid"`     // _NET_WM_PID; 0 when the window publishes none
	Mapped  bool   `json:"mapped"`  // MapState is Viewable
	Hidden  bool   `json:"hidden"`  // _NET_WM_STATE_HIDDEN — the manager iconified it
	Shaded  bool   `json:"shaded"`  // _NET_WM_STATE_SHADED — rolled up into its title bar
	Desktop int    `json:"desktop"` // -1 when the window is on all of them
	X       int    `json:"x"`
	Y       int    `json:"y"`
	W       int    `json:"w"`
	H       int    `json:"h"`
}

// Screen is one pass over the display: the geometry every window is judged
// against, the desktop currently being shown, and the windows themselves in
// stacking order, bottom first.
type Screen struct {
	Width   int        `json:"width"`
	Height  int        `json:"height"`
	Desktop int        `json:"desktop"` // -1 when the manager publishes none
	Windows []OnScreen `json:"windows"`
}

// ScreenState reads what X is showing right now.
//
// It returns an error rather than an empty Screen when there is no EWMH window
// manager, and the difference is the whole reason the check is here. An empty
// window list means "nothing is on the desktop", which is a definite answer and
// a definite no. A missing window manager means the properties this reads are
// not being published by anyone, so every window would come back looking absent
// — an answer that is indistinguishable from the first one and wrong. A caller
// that cannot tell those apart will eventually treat "I could not look" as "I
// looked and there was nothing", which is precisely the direction a visibility
// check must never fail in.
func (e *EWMH) ScreenState() (Screen, error) {
	scr := Screen{Desktop: -1}

	// _NET_SUPPORTING_WM_CHECK is the property an EWMH manager sets to announce
	// itself. Without it the rest of this reads a desktop nobody is managing.
	if wm, _ := e.propInts(e.root, "_NET_SUPPORTING_WM_CHECK"); len(wm) == 0 {
		return scr, fmt.Errorf("no EWMH window manager is running, so X cannot say what is on screen")
	}

	geom, err := xproto.GetGeometry(e.conn, xproto.Drawable(e.root)).Reply()
	if err != nil {
		return scr, fmt.Errorf("root geometry: %w", err)
	}
	scr.Width, scr.Height = int(geom.Width), int(geom.Height)

	if cur, _ := e.propInts(e.root, "_NET_CURRENT_DESKTOP"); len(cur) > 0 {
		scr.Desktop = int(cur[0])
	}

	// Stacking order first: it is the same set as _NET_CLIENT_LIST but ordered
	// bottom to top, which is the only ordering in which occlusion can be
	// decided. Managers that publish only the unordered list still work; the
	// caller then sees no occlusion, which errs toward "visible" for that one
	// test and is caught by all the others.
	ids, _ := e.propInts(e.root, "_NET_CLIENT_LIST_STACKING")
	if len(ids) == 0 {
		ids, _ = e.propInts(e.root, "_NET_CLIENT_LIST")
	}

	hidden, _ := e.atom("_NET_WM_STATE_HIDDEN")
	shaded, _ := e.atom("_NET_WM_STATE_SHADED")

	scr.Windows = make([]OnScreen, 0, len(ids))
	for _, id := range ids {
		w, err := e.describeOnScreen(xproto.Window(id), hidden, shaded)
		if err != nil {
			// A window that closed between the list and the query is ordinary on
			// a live desktop. Dropping it is right: it is certainly not one
			// anybody is reading.
			continue
		}
		scr.Windows = append(scr.Windows, w)
	}
	return scr, nil
}

func (e *EWMH) describeOnScreen(win xproto.Window, hidden, shaded xproto.Atom) (OnScreen, error) {
	w := OnScreen{ID: fmt.Sprintf("0x%08x", uint32(win)), Desktop: -1}

	attrs, err := xproto.GetWindowAttributes(e.conn, win).Reply()
	if err != nil {
		return w, fmt.Errorf("attributes: %w", err)
	}
	// Viewable, not Unmapped and not Unviewable. Unviewable means the window
	// itself is mapped but an ancestor is not, which for a client under a
	// reparenting manager is what an iconified window looks like from here.
	w.Mapped = attrs.MapState == xproto.MapStateViewable

	if p, _ := e.propInts(win, "_NET_WM_PID"); len(p) > 0 {
		w.PID = int(p[0])
	}
	if d, _ := e.propInts(win, "_NET_WM_DESKTOP"); len(d) > 0 && d[0] != 0xFFFFFFFF {
		w.Desktop = int(d[0])
	}
	states, _ := e.propInts(win, "_NET_WM_STATE")
	for _, st := range states {
		switch xproto.Atom(st) {
		case hidden:
			w.Hidden = true
		case shaded:
			w.Shaded = true
		}
	}
	if c, _ := e.propText(win, "WM_CLASS"); c != "" {
		parts := strings.Split(strings.TrimRight(c, "\x00"), "\x00")
		w.Class = parts[len(parts)-1]
	}

	geom, err := xproto.GetGeometry(e.conn, xproto.Drawable(win)).Reply()
	if err != nil {
		return w, fmt.Errorf("geometry: %w", err)
	}
	w.W, w.H = int(geom.Width), int(geom.Height)

	// Root-relative coordinates, and they may legitimately be negative: a
	// window dragged past the left edge is exactly the case this exists to
	// catch, and its own x/y are relative to a frame that moved with it.
	if tr, err := xproto.TranslateCoordinates(e.conn, win, e.root, 0, 0).Reply(); err == nil {
		w.X, w.Y = int(tr.DstX), int(tr.DstY)
	} else {
		w.X, w.Y = int(geom.X), int(geom.Y)
	}
	return w, nil
}
