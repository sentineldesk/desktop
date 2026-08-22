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

// The decisions behind PLACING a terminal window, as opposed to asking whether
// one happens to be visible.
//
// terminal_visibility_test.go covers the judgement; this covers the half that
// makes the judgement come out yes. Two questions, both pure, both fed the facts
// a real X server produces in the cases that used to end with a command running
// where nobody could see it:
//
//	createdWindows  which windows did THIS call bring into existence — the only
//	                ones it is entitled to move. Getting this wrong in the
//	                permissive direction means an agent rearranging a person's
//	                own desktop to satisfy its promise.
//	placementOf     did the window land on the desktop the room is watching. This
//	                is the requirement the forcing has to meet, and the one the
//	                project owner asked for in as many words: a terminal that
//	                opens on desktop 1 while everybody is looking at desktop 0 is
//	                not a terminal anybody is looking at.
//
// What is deliberately NOT covered here: that openbox honours _NET_WM_DESKTOP,
// _NET_WM_STATE and _NET_ACTIVE_WINDOW. Those are the window manager's contract,
// not this code's, and they are also precisely why the code confirms rather than
// trusts — a manager that ignores all three produces no error anywhere, only a
// window that never appears where it was asked to go, which is the timeout path
// below the confirmation loop.

import (
	"os"
	"strings"
	"testing"

	"github.com/sentineldesk/desktop/internal/desktop"
)

// --- which windows are ours to move ---------------------------------------------

func TestCreatedWindowsIgnoresWhatWasAlreadyThere(t *testing.T) {
	// The person's own terminal, on the desktop before this call ran. It belongs
	// to the session by every test createdWindows applies — same emulator, same
	// pid — and it is still not this call's to pin, un-shade or raise.
	theirs := aTerminal()
	before := windowIDs(screenWith(theirs))

	ours := aTerminal()
	ours.ID = "0x00000009"

	got := createdWindows(screenWith(theirs, ours), before, ownsTerminal())
	if len(got) != 1 || got[0].ID != ours.ID {
		t.Fatalf("createdWindows returned %+v, want only the window this call opened", got)
	}
}

func TestCreatedWindowsIgnoresOtherPeoplesNewWindows(t *testing.T) {
	// Somebody opened a browser in the same two seconds. It is new, and it is
	// not ours: forcing it onto another desktop and raising it would be an agent
	// rearranging a person's screen for its own convenience.
	browser := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 0, X: 0, Y: 0, W: 1200, H: 800,
	}
	got := createdWindows(screenWith(browser), map[string]bool{}, ownsTerminal())
	if len(got) != 0 {
		t.Fatalf("createdWindows claimed %+v, which belongs to somebody else", got)
	}
}

func TestCreatedWindowsFallsBackToClassWhenNothingPublishesAPID(t *testing.T) {
	// Same degraded path decideVisibility has, and it has to be the same rule:
	// two functions that disagree about which window they are talking about
	// would place one window and confirm another.
	w := aTerminal()
	w.PID = 0
	got := createdWindows(screenWith(w), map[string]bool{}, ownsTerminal())
	if len(got) != 1 {
		t.Fatalf("the class fallback did not find the new terminal: %+v", got)
	}
}

func TestCreatedWindowsIgnoresPIDLessWindowsWhenOthersHaveOne(t *testing.T) {
	// The correlation is available, so a window that publishes no pid is not
	// evidence of anything and must not be swept up by the class heuristic.
	stray := desktop.OnScreen{
		ID: "0x00000004", Class: "XTerm", PID: 0,
		Mapped: true, Desktop: 0, X: 0, Y: 0, W: 600, H: 400,
	}
	browser := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 0, X: 0, Y: 0, W: 1200, H: 800,
	}
	if got := createdWindows(screenWith(stray, browser), map[string]bool{}, ownsTerminal()); len(got) != 0 {
		t.Fatalf("createdWindows adopted a pid-less stranger: %+v", got)
	}
}

func TestWindowIDsSnapshotsEveryWindow(t *testing.T) {
	// The snapshot is what "already there" means. Missing one window from it
	// makes that window look created, and the next thing that happens to it is
	// being moved to another desktop.
	other := desktop.OnScreen{ID: "0x00000002", Class: "Chromium", PID: otherPID}
	ids := windowIDs(screenWith(aTerminal(), other))
	if !ids["0x00000001"] || !ids["0x00000002"] || len(ids) != 2 {
		t.Fatalf("windowIDs = %v, want both windows", ids)
	}
}

// --- did it land where the room is looking ----------------------------------------

func TestPlacementWaitsWhileNothingHasAppeared(t *testing.T) {
	// An emulator takes a moment to map its window. "Not yet" must be tellable
	// from "no": collapsing them either refuses a terminal that was about to
	// appear, or waits out the full timeout on one that never will.
	state, why := placementOf(nil, 0)
	if state != placeWaiting {
		t.Fatalf("expected placeWaiting for an empty desktop, got %v: %s", state, why)
	}
}

// TestPlacementRefusesAWindowOnAnotherDesktop is the case the project owner
// described: the terminal opens on desktop 1 and everybody is watching desktop
// 0. Before the forcing existed this ended as a twenty-second wait and a
// refusal; now it is the signal that drives the pin, and it still has to be
// refused for as long as the window has not moved.
func TestPlacementRefusesAWindowOnAnotherDesktop(t *testing.T) {
	w := aTerminal()
	w.Desktop = 1
	state, why := placementOf([]desktop.OnScreen{w}, 0)
	if state != placeUnplaced {
		t.Fatalf("expected placeUnplaced, got %v: %s", state, why)
	}
	// The reason names both desktops. An agent told only "not visible" looks for
	// a closed window; told "desktop 1, and the room is on 0" it knows what
	// happened and so does the person reading the log.
	if !strings.Contains(why, "desktop 1") || !strings.Contains(why, "desktop 0") {
		t.Fatalf("reason %q does not name both desktops", why)
	}
}

func TestPlacementRefusesAnUnmappedWindow(t *testing.T) {
	w := aTerminal()
	w.Mapped = false
	state, why := placementOf([]desktop.OnScreen{w}, 0)
	if state != placeUnplaced {
		t.Fatalf("expected placeUnplaced, got %v: %s", state, why)
	}
	if !strings.Contains(why, "not mapped") {
		t.Fatalf("reason %q does not say the window is not mapped", why)
	}
}

func TestPlacementAcceptsAWindowOnTheSharedDesktop(t *testing.T) {
	if state, why := placementOf([]desktop.OnScreen{aTerminal()}, 0); state != placeShared {
		t.Fatalf("expected placeShared, got %v: %s", state, why)
	}
}

func TestPlacementAcceptsAStickyWindow(t *testing.T) {
	// -1 is "on every desktop", so it is on the shared one whatever that is.
	// Reading it as a desktop number would refuse a window that is by definition
	// never on the wrong one.
	w := aTerminal()
	w.Desktop = -1
	if state, why := placementOf([]desktop.OnScreen{w}, 3); state != placeShared {
		t.Fatalf("expected a sticky window to count as placed, got %v: %s", state, why)
	}
}

func TestPlacementDoesNotRequireADesktopNumberNobodyPublishes(t *testing.T) {
	// A manager that publishes no _NET_CURRENT_DESKTOP leaves shared at -1.
	// There is nothing to pin to and nothing to disagree with, so the
	// requirement drops to "mapped", which is everything such a manager can be
	// held to — and the pin is skipped rather than sent as a negative, which
	// would ask for 0xFFFFFFFF and make the window sticky instead.
	w := aTerminal()
	w.Desktop = 2
	if state, why := placementOf([]desktop.OnScreen{w}, -1); state != placeShared {
		t.Fatalf("expected placeShared with no current desktop published, got %v: %s", state, why)
	}

	w.Mapped = false
	if state, _ := placementOf([]desktop.OnScreen{w}, -1); state != placeUnplaced {
		t.Fatalf("an unmapped window passed the one test left, got %v", state)
	}
}

func TestPlacementTakesAnyOneWindowThatLanded(t *testing.T) {
	// Two windows came up — a stale spawn, or a person's second attach in the
	// same instant. One of them being where the room is looking is the promise;
	// requiring all of them would refuse over a window nobody is waiting for.
	stuck := aTerminal()
	stuck.ID = "0x00000007"
	stuck.Desktop = 2
	if state, why := placementOf([]desktop.OnScreen{stuck, aTerminal()}, 0); state != placeShared {
		t.Fatalf("expected placeShared when one window landed, got %v: %s", state, why)
	}
}

func TestPlacementIsNotTheWholeVisibilityTest(t *testing.T) {
	// A window can be mapped on the right desktop and still be unreadable —
	// rolled up, dragged off the edge, buried. placementOf says yes to all of
	// those on purpose: it answers "was it put where the room is looking", and
	// decideVisibility answers "can it be read", and a refusal has to be able to
	// say which of the two failed. Collapsing them into one predicate is how the
	// reason string stops being useful.
	shaded := aTerminal()
	shaded.Shaded = true
	if state, _ := placementOf([]desktop.OnScreen{shaded}, 0); state != placeShared {
		t.Fatal("placementOf started judging readability, which is the other function's job")
	}
	assertHidden(t, screenWith(shaded), ownsTerminal(), "rolled up")
}

// --- the other doors onto the shared screen ----------------------------------
//
// The terminal was never the only way a window reaches the desktop everybody is
// watching. launch_app and open_app_and_wait open one too, and neither placed
// what it opened: an application whose window came up on another desktop, or
// shaded, or behind everything, was reported as launched all the same. Same
// invariant, same failure, and no reason for one path to defend it alone.

// The rule the extracted helper has to keep is the one attachWindow already
// kept: a window is placed when it is on the desktop the room is looking at.
// These run the same decision that launch_app and open_app_and_wait now depend
// on, so the two doors cannot drift from the one that was fixed first.
func TestAWindowFromAnyDoorIsJudgedByTheSameRule(t *testing.T) {
	w := aTerminal()
	w.Desktop = 2
	if state, _ := placementOf([]desktop.OnScreen{w}, 2); state != placeShared {
		t.Error("a window on the shared desktop was not accepted")
	}
	w.Desktop = 5
	state, why := placementOf([]desktop.OnScreen{w}, 2)
	if state == placeShared {
		t.Error("a window on ANOTHER desktop was accepted as placed — which is exactly " +
			"the case that made an installation happen where nobody could see it")
	}
	if why == "" {
		t.Error("the refusal says nothing, so a caller cannot report why")
	}
}

// Nothing to place is not a success. Returning true for an empty set would let
// a caller that found no windows report the screen as correct.
func TestPlacingNothingIsNotSuccess(t *testing.T) {
	if state, _ := placementOf(nil, 0); state == placeShared {
		t.Error("an empty set of windows was reported as placed")
	}
}

// open_app_and_wait used `wmctrl -i -a`, and activate is the opposite of what a
// shared screen wants when something has just OPENED: it switches to the desktop
// the window is on, dragging everybody watching to wherever the application
// happened to come up. The room does not follow the window; the window joins the
// room.
//
// activate_window, in tools.go, is deliberately not covered. There the caller
// named a window and asked for it — going to it is the request, not a side
// effect of having launched something. The distinction is who chose the window:
// a person or agent pointing at one, versus a window that appeared on its own
// and has to be brought into view.
func TestNothingActivatesItsWayOntoTheSharedScreen(t *testing.T) {
	for _, path := range []string{"tools_next.go", "tools_terminal.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, `"wmctrl", "-i", "-a"`) {
				t.Errorf("%s still activates a window onto the shared screen:\n  %s\n\n"+
					"activate moves the VIEWER to the window's desktop. Use bringToTheRoom, "+
					"which moves the window to the viewer's.", path, strings.TrimSpace(line))
			}
		}
	}
}

// Both doors have to actually call the placement, and this is the assertion that
// notices if one of them quietly stops. It reads the source because the thing
// being checked is that a call EXISTS on a path that needs a live X server to
// exercise — the alternative is no test at all, which is how the gap lasted.
func TestBothDoorsPlaceWhatTheyOpen(t *testing.T) {
	for path, what := range map[string]string{
		"tools.go":      "launch_app",
		"tools_next.go": "open_app_and_wait",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !strings.Contains(string(raw), "bringToTheRoom") {
			t.Errorf("%s (%s) opens a window on the shared screen and never places it: "+
				"it would be reported as launched wherever the window manager put it", path, what)
		}
	}
}
