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

// The three decisions that stand between an agent and a command nobody can see.
//
// Each of these covers a defect that returned `ok`. That is the reason they are
// worth writing down: a user asked for gimp to be installed, watched five
// minutes of nothing on a shared desktop, and every layer involved reported
// success. Asserting that terminal_run returned without an error would have
// asserted nothing at all, which is the house rule — so what is asserted here is
// the judgement itself, fed the facts a real X server and a real tmux produced
// in each of the failing cases.
//
// Nothing here needs a display, a tmux binary or a container, for the same
// reason internal/desktop/xevents_test.go needs none: the decisions were
// deliberately factored out of the code that gathers the facts. decideVisibility
// takes a Screen and a pid set, pickTerminalWindow takes a window list, and
// parsePPID takes a line of text.
//
// What is deliberately NOT covered: that X reports _NET_WM_STATE_HIDDEN when
// openbox iconifies a window, and that tmux sets pane_dead when a process under
// remain-on-exit exits. Those are X's contract and tmux's contract, not this
// code's, and asserting them needs the display these tests exist to avoid.

import (
	"strings"
	"testing"

	"github.com/sentineldesk/desktop/internal/desktop"
)

// --- fixtures -----------------------------------------------------------------

const (
	screenW = 1920
	screenH = 1080
)

// termPID is the pid of the emulator showing the session in every fixture below;
// otherPID is something else on the desktop.
const (
	termPID  = 4242
	otherPID = 99
)

// ownsTerminal is the set processOwners would build from a tmux client whose
// ancestor is the emulator.
func ownsTerminal() map[int]bool { return map[int]bool{termPID: true, 777: true} }

// aTerminal is a healthy, mapped, on-screen terminal window.
func aTerminal() desktop.OnScreen {
	return desktop.OnScreen{
		ID: "0x00000001", Class: "Lxterminal", PID: termPID,
		Mapped: true, Desktop: 0,
		X: 100, Y: 100, W: 800, H: 600,
	}
}

func screenWith(wins ...desktop.OnScreen) desktop.Screen {
	return desktop.Screen{Width: screenW, Height: screenH, Desktop: 0, Windows: wins}
}

func assertVisible(t *testing.T, scr desktop.Screen, owners map[int]bool) {
	t.Helper()
	state, why := decideVisibility(scr, owners)
	if state != screenShowing {
		t.Fatalf("expected the terminal to count as visible, got state %v: %s", state, why)
	}
}

// assertHidden checks both the verdict and that the reason names the cause. The
// reason is not decoration: it is what the agent is told, and "no terminal is
// open" while one is plainly minimised on the screen sends a model looking in
// entirely the wrong place.
func assertHidden(t *testing.T, scr desktop.Screen, owners map[int]bool, mentions string) {
	t.Helper()
	state, why := decideVisibility(scr, owners)
	if state != screenHidden {
		t.Fatalf("expected hidden, got state %v: %s", state, why)
	}
	if !strings.Contains(why, mentions) {
		t.Fatalf("reason %q does not mention %q", why, mentions)
	}
}

// --- A: is anybody looking ------------------------------------------------------

func TestVisibleWhenTheWindowIsOnScreen(t *testing.T) {
	assertVisible(t, screenWith(aTerminal()), ownsTerminal())
}

func TestHiddenWhenMinimised(t *testing.T) {
	// Openbox unmaps an iconified window and sets _NET_WM_STATE_HIDDEN. Both are
	// checked, because "unmapped" is also what an ancestor being unmapped looks
	// like and a manager that only sets the property would otherwise pass.
	w := aTerminal()
	w.Mapped = false
	assertHidden(t, screenWith(w), ownsTerminal(), "not mapped")

	w = aTerminal()
	w.Hidden = true
	assertHidden(t, screenWith(w), ownsTerminal(), "minimised")
}

func TestHiddenWhenShaded(t *testing.T) {
	// Rolled up into its title bar: mapped, full size, on the current desktop,
	// and showing not one line of what the command printed. Every geometric test
	// passes it, which is why the state has to be read explicitly.
	w := aTerminal()
	w.Shaded = true
	assertHidden(t, screenWith(w), ownsTerminal(), "rolled up")
}

func TestHiddenWhenOnAnotherDesktop(t *testing.T) {
	w := aTerminal()
	w.Desktop = 2
	assertHidden(t, screenWith(w), ownsTerminal(), "desktop 2")
}

func TestStickyWindowCountsOnAnyDesktop(t *testing.T) {
	// -1 is "on every desktop". A sticky terminal is visible whichever desktop
	// is being shown, and reading -1 as a desktop number would have made it
	// invisible on all of them.
	w := aTerminal()
	w.Desktop = -1
	scr := screenWith(w)
	scr.Desktop = 3
	assertVisible(t, scr, ownsTerminal())
}

func TestUnknownCurrentDesktopIsNotAMismatch(t *testing.T) {
	// A manager that does not publish _NET_CURRENT_DESKTOP leaves Screen.Desktop
	// at -1. That is missing information, not a disagreement, and treating it as
	// one would refuse to run on every non-EWMH-complete desktop.
	scr := screenWith(aTerminal())
	scr.Desktop = -1
	assertVisible(t, scr, ownsTerminal())
}

func TestHiddenWhenMovedOffScreen(t *testing.T) {
	// Dragged past the right edge with a hand's width still showing. X forbids
	// none of this and reports the window as perfectly mapped.
	w := aTerminal()
	w.X = screenW - 40
	assertHidden(t, screenWith(w), ownsTerminal(), "off the screen")

	// And past the left edge, where the coordinate goes negative — the case a
	// naive intersection gets wrong by treating -1800 as a large positive width.
	w = aTerminal()
	w.X = -760
	assertHidden(t, screenWith(w), ownsTerminal(), "off the screen")
}

func TestPartlyOffScreenStillCounts(t *testing.T) {
	// Half a terminal is a readable amount of terminal. Refusing here would fail
	// every window a person nudged past an edge, and a check that fires on the
	// ordinary case gets turned off.
	w := aTerminal()
	w.X = screenW - 400
	assertVisible(t, screenWith(w), ownsTerminal())
}

func TestHiddenWhenCompletelyCovered(t *testing.T) {
	// The stacking order is bottom-to-top, so the browser is drawn over the
	// terminal. This is the occlusion case, and it is the one that cannot be
	// decided from the terminal's own properties at all.
	browser := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 0,
		X: 0, Y: 0, W: screenW, H: screenH,
	}
	assertHidden(t, screenWith(aTerminal(), browser), ownsTerminal(), "covered")
}

func TestPartialOverlapDoesNotCount(t *testing.T) {
	// An ordinary busy desktop. Treating any overlap as occlusion would refuse
	// to run anything the moment a second window existed.
	other := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 0,
		X: 400, Y: 400, W: 900, H: 600,
	}
	assertVisible(t, screenWith(aTerminal(), other), ownsTerminal())
}

func TestCoveringWindowBelowDoesNotCount(t *testing.T) {
	// Same geometry, drawn UNDERNEATH. Order in the slice is the whole
	// difference, and getting it backwards would hide every terminal that
	// happens to be on top of something maximised.
	browser := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 0,
		X: 0, Y: 0, W: screenW, H: screenH,
	}
	assertVisible(t, screenWith(browser, aTerminal()), ownsTerminal())
}

func TestCoveringWindowOnAnotherDesktopDoesNotCount(t *testing.T) {
	// A maximised window parked on desktop 2 is above in the stacking list and
	// is not being drawn. Counting it would hide the terminal on desktop 0.
	elsewhere := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 2,
		X: 0, Y: 0, W: screenW, H: screenH,
	}
	assertVisible(t, screenWith(aTerminal(), elsewhere), ownsTerminal())
}

func TestMinimisedWindowDoesNotOcclude(t *testing.T) {
	browser := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: false, Hidden: true, Desktop: 0,
		X: 0, Y: 0, W: screenW, H: screenH,
	}
	assertVisible(t, screenWith(aTerminal(), browser), ownsTerminal())
}

// TestHiddenWhenAttachedFromDockerExec is the worst of the lot, and the reason
// the tmux answer had to be thrown away rather than tightened.
//
// `tmux attach -t sentineldesk` from `docker exec` is DOCUMENTED at the top of
// tools_terminal.go as a feature — a person joining the session from their own
// shell. Its client satisfies `list-clients` forever, and while that shell stays
// open every visibility check in the daemon returned true. Nothing correlates it
// to a window because there is no window: its ancestors are the container
// runtime, not an emulator.
func TestHiddenWhenAttachedFromDockerExec(t *testing.T) {
	// The desktop is not empty — a panel and a browser are on it, both
	// publishing pids — but nothing on it belongs to the attached client.
	panel := desktop.OnScreen{
		ID: "0x00000003", Class: "Lxpanel", PID: 31,
		Mapped: true, Desktop: 0, X: 0, Y: 1050, W: screenW, H: 30,
	}
	browser := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 0, X: 0, Y: 0, W: 1200, H: 800,
	}
	assertHidden(t, screenWith(panel, browser), map[int]bool{5150: true}, "docker exec")
}

func TestHiddenWhenTheDesktopIsEmpty(t *testing.T) {
	// A client attached and not one managed window anywhere. Definite, and
	// reachable without any pid correlation at all.
	assertHidden(t, screenWith(), ownsTerminal(), "no window on this desktop")
}

func TestFallsBackToClassWhenNothingPublishesAPID(t *testing.T) {
	// A toolkit or manager that does not set _NET_WM_PID leaves nothing to
	// correlate on. Refusing outright would disable terminal_run permanently on
	// such a desktop over a property nobody looks at; claiming visibility with
	// no evidence is the failure this whole change exists to end. So it falls
	// back to "is a terminal-shaped window genuinely on screen", which is a
	// weaker claim, is still strictly stronger than what tmux could say, and is
	// labelled as a guess in the reason it returns.
	w := aTerminal()
	w.PID = 0
	state, why := decideVisibility(screenWith(w), ownsTerminal())
	if state != screenShowing {
		t.Fatalf("expected the class fallback to find the terminal, got %v: %s", state, why)
	}
	if !strings.Contains(why, "class") {
		t.Fatalf("the fallback did not admit it was guessing: %q", why)
	}
}

func TestClassFallbackStillAppliesTheGeometry(t *testing.T) {
	// The degraded path is weaker about WHICH window it found, not about
	// whether that window can be seen.
	w := aTerminal()
	w.PID = 0
	w.Hidden = true
	assertHidden(t, screenWith(w), ownsTerminal(), "minimised")
}

func TestClassFallbackIgnoresNonTerminals(t *testing.T) {
	w := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: 0,
		Mapped: true, Desktop: 0, X: 0, Y: 0, W: 1200, H: 800,
	}
	assertHidden(t, screenWith(w), ownsTerminal(), "no terminal window")
}

func TestAWindowWithoutAPIDIsNotMatchedWhenOthersHaveOne(t *testing.T) {
	// Mixed desktop: the correlation is available, so a pid-less window is not
	// evidence of anything and must not be swept up by the class heuristic.
	stray := desktop.OnScreen{
		ID: "0x00000004", Class: "XTerm", PID: 0,
		Mapped: true, Desktop: 0, X: 0, Y: 0, W: 600, H: 400,
	}
	browser := desktop.OnScreen{
		ID: "0x00000002", Class: "Chromium", PID: otherPID,
		Mapped: true, Desktop: 0, X: 0, Y: 0, W: 1200, H: 800,
	}
	assertHidden(t, screenWith(stray, browser), ownsTerminal(), "docker exec")
}

func TestAnyOneVisibleWindowIsEnough(t *testing.T) {
	// Two clients of the same session, one minimised. The session is visible if
	// ANY of its windows is; requiring all of them would refuse whenever
	// somebody left a second terminal iconified.
	buried := aTerminal()
	buried.ID = "0x00000005"
	buried.Hidden = true
	assertVisible(t, screenWith(buried, aTerminal()), ownsTerminal())
}

func TestNoScreenGeometryDisablesOnlyTheGeometryTest(t *testing.T) {
	// Width 0 means the root geometry could not be read. Everything else about
	// the window is still known, so the other tests still run and only the
	// off-screen one is skipped.
	scr := screenWith(aTerminal())
	scr.Width, scr.Height = 0, 0
	assertVisible(t, scr, ownsTerminal())

	w := aTerminal()
	w.Hidden = true
	scr = screenWith(w)
	scr.Width, scr.Height = 0, 0
	assertHidden(t, scr, ownsTerminal(), "minimised")
}

// --- the process walk that ties a tmux client to a window ------------------------

func TestParsePPIDHandlesNamesWithSpacesAndParens(t *testing.T) {
	// Field 2 of /proc/<pid>/stat is the executable name in parentheses and is
	// the only field that may contain spaces or parens. Splitting the line on
	// whitespace reads the wrong field for anything called `my program (old)`,
	// and the pid it returns then belongs to another process entirely — which,
	// in this file, is a window matched to the wrong session.
	cases := []struct {
		name string
		stat string
		want int
	}{
		{"plain", "4242 (lxterminal) S 1 4242 4242 0 -1 4194560 1234 0", 1},
		{"space in name", "77 (my program) S 4242 77 77 0 -1 4194304 0 0", 4242},
		{"paren in name", "78 (weird (x)) S 4242 78 78 0 -1 4194304 0 0", 4242},
		{"truncated", "78 (bash)", 0},
		{"garbage", "not a stat line", 0},
		{"empty", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parsePPID(c.stat); got != c.want {
				t.Fatalf("parsePPID(%q) = %d, want %d", c.stat, got, c.want)
			}
		})
	}
}

func TestProcessOwnersAlwaysIncludesTheClientItself(t *testing.T) {
	// There is no /proc on a development host, so parentPID returns 0 and the
	// walk stops at the client. The set must still contain it rather than come
	// back empty — an empty owner set matches nothing and would report every
	// desktop as unwatched.
	owners := processOwners([]int{4242, 4243})
	if !owners[4242] || !owners[4243] {
		t.Fatalf("processOwners dropped a client pid: %v", owners)
	}
}

func TestProcessOwnersIgnoresInit(t *testing.T) {
	// pid 1 is every process's ancestor. Including it would match anything on
	// the desktop that reparented to init — which, in a container, is most
	// things that outlived their launcher.
	if processOwners([]int{1})[1] {
		t.Fatal("processOwners walked up to pid 1, which owns nothing in particular")
	}
}

// --- B: a job must not steal the pane ---------------------------------------------

func TestPickTerminalWindowIgnoresJobWindows(t *testing.T) {
	// The exact shape job_start used to leave behind: the job window created
	// last and therefore active. terminal_run resolved this to the job's pane,
	// send-keys returned rc=0, the text landed in job-run.sh's stdin, and
	// nothing ran. Every layer reported success.
	wins := []tmuxWindow{
		{ID: "@1", Name: "bash", Pane: "%1", Active: false},
		{ID: "@2", Name: jobWindowPrefix + "j7", Pane: "%2", Active: true},
	}
	win, err := pickTerminalWindow(wins)
	if err != nil {
		t.Fatal(err)
	}
	if win.Pane != "%1" {
		t.Fatalf("picked %s (%s); a job window is not a shell to type into", win.Pane, win.Name)
	}
}

func TestPickTerminalWindowPrefersTheActiveTerminal(t *testing.T) {
	// Somebody opened a second terminal and is working in it. That is the one an
	// agent should read and type into — the original reason the picker followed
	// tmux's idea of active at all, and a property worth keeping while dropping
	// the part that let jobs hijack it.
	wins := []tmuxWindow{
		{ID: "@1", Name: "bash", Pane: "%1", Active: false},
		{ID: "@2", Name: jobWindowPrefix + "j7", Pane: "%2", Active: false},
		{ID: "@3", Name: "zsh", Pane: "%3", Active: true},
	}
	win, err := pickTerminalWindow(wins)
	if err != nil {
		t.Fatal(err)
	}
	if win.Pane != "%3" {
		t.Fatalf("picked %s, want the active terminal %%3", win.Pane)
	}
}

func TestPickTerminalWindowPrefersALiveTerminalOverTheActiveDeadOne(t *testing.T) {
	// A dead pane accepts keystrokes and discards them. Given a choice, the live
	// one wins even when the person is looking at the corpse.
	wins := []tmuxWindow{
		{ID: "@1", Name: "bash", Pane: "%1", Active: true, Dead: true},
		{ID: "@2", Name: "bash", Pane: "%2", Active: false},
	}
	win, err := pickTerminalWindow(wins)
	if err != nil {
		t.Fatal(err)
	}
	if win.Pane != "%2" {
		t.Fatalf("picked the dead pane %s over a live one", win.Pane)
	}
}

func TestPickTerminalWindowStillReturnsADeadPane(t *testing.T) {
	// When it is all there is, hand it back with Dead set rather than refuse.
	// terminal_read on a dead pane is worth doing — whatever killed the shell
	// printed its reason there — and terminal_run is the only caller that has to
	// care, which it does by checking the flag.
	wins := []tmuxWindow{{ID: "@1", Name: "bash", Pane: "%1", Active: true, Dead: true}}
	win, err := pickTerminalWindow(wins)
	if err != nil {
		t.Fatal(err)
	}
	if win.Pane != "%1" || !win.Dead {
		t.Fatalf("got %+v, want the dead pane with its flag intact", win)
	}
}

func TestPickTerminalWindowRefusesWhenOnlyJobsAreOpen(t *testing.T) {
	// And says which situation it is. "No terminal is open" is true but useless
	// when three job windows are visibly on the screen; the caller needs to be
	// told that those are not shells.
	wins := []tmuxWindow{
		{ID: "@2", Name: jobWindowPrefix + "j7", Pane: "%2", Active: true},
		{ID: "@3", Name: jobWindowPrefix + "j8", Pane: "%3"},
	}
	_, err := pickTerminalWindow(wins)
	if err == nil {
		t.Fatal("picked a job window as a terminal")
	}
	if !strings.Contains(err.Error(), "job") {
		t.Fatalf("error %q does not say the open windows are jobs", err)
	}
}

func TestPickTerminalWindowRefusesWhenNothingIsOpen(t *testing.T) {
	if _, err := pickTerminalWindow(nil); err == nil {
		t.Fatal("picked a terminal out of an empty session")
	}
}
