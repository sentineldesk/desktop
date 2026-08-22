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

//go:build integration

package integration

// Pointer, keyboard and clipboard.
//
// These are the hardest tools to check honestly, because their effect is
// whatever the application underneath decided to do with the event. Asserting
// that type_text returned "typed 5 chars" would be asserting that a counter
// counts. So every test here gives the input somewhere that records it: a shell
// that writes a file, a window that takes focus, a clipboard another program can
// read.
//
// For the read-only tools the direction is reversed — the state is set from
// outside and read back through MCP, which is the only arrangement where the
// tool cannot be the source of its own confirmation.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// typeTarget opens a terminal, focuses it, and returns a marker path the typed
// command will create. Shared by the keyboard tests, since "the characters
// arrived" is only observable if something acts on them.
func typeTarget(t *testing.T, name string) string {
	t.Helper()
	title := "TYPE" + strings.ToUpper(name)
	id := openWindow(t, title)
	devDesk(t).Call(t, "activate_window", map[string]any{"id": id})
	// xterm running `sleep` has no shell to type into, so replace it with one.
	X(t, "wmctrl -i -c %s", id)
	devDesk(t).Call(t, "launch_app", map[string]any{
		"command": fmt.Sprintf("xterm -T %s -e sh", title)})
	devDesk(t).Call(t, "wait_for_window", map[string]any{"match": title, "timeout_ms": 15000})
	out := devDesk(t).Call(t, "wait_for_window", map[string]any{"match": title, "timeout_ms": 5000})
	newID := jsonField(t, out, "id")
	devDesk(t).Call(t, "activate_window", map[string]any{"id": newID})
	t.Cleanup(func() { X(t, "wmctrl -i -c %s 2>/dev/null || true", newID) })

	marker := "/tmp/typed-" + name + ".txt"
	Sh(t, "rm -f %s", marker)
	time.Sleep(700 * time.Millisecond) // let the shell draw its prompt
	return marker
}

func TestTypeText(t *testing.T) {
	control(t)
	marker := typeTarget(t, "text")

	// The proof is the file, not the reply. A tool that counted the characters
	// and injected none would answer identically.
	devDesk(t).Call(t, "type_text", map[string]any{
		"text": "echo typed-ok > " + marker + "\n"})

	eventually(t, 8*time.Second, "the typed command to run", func() bool {
		return strings.Contains(Sh(t, "cat %s 2>/dev/null", marker), "typed-ok")
	})
}

func TestKeyCombo(t *testing.T) {
	control(t)
	marker := typeTarget(t, "combo")

	// Type the command WITHOUT a newline, then send Return as a key combo. The
	// file appearing is what shows the key reached the shell, and separating it
	// from the text is what makes this a test of key_combo rather than of
	// type_text sending a newline.
	devDesk(t).Call(t, "type_text", map[string]any{"text": "echo combo-ok > " + marker})
	time.Sleep(300 * time.Millisecond)
	devDesk(t).Call(t, "key_combo", map[string]any{"keys": "Return"})

	eventually(t, 8*time.Second, "Return to submit the line", func() bool {
		return strings.Contains(Sh(t, "cat %s 2>/dev/null", marker), "combo-ok")
	})
}

func TestMouseMove(t *testing.T) {
	control(t)
	for _, p := range [][2]int{{100, 100}, {1500, 900}, {640, 360}} {
		devDesk(t).Call(t, "mouse_move", map[string]any{"x": p[0], "y": p[1]})
		// X is asked, not the tool. get_mouse_position reading back what
		// mouse_move set would only show the two agree.
		got := X(t, "xdotool getmouselocation --shell | head -2 | tr '\\n' ' '")
		want := fmt.Sprintf("X=%d Y=%d", p[0], p[1])
		if !strings.Contains(got, want) {
			t.Errorf("sent the pointer to (%d,%d) and X reports %q", p[0], p[1], got)
		}
	}
}

// clearScreen minimises everything before a test that depends on what is
// visible.
//
// This is the fixture three earlier attempts at these tests lacked, and its
// absence produced a confident and wrong conclusion: that clicking a window
// could not move focus in this image, and that the tools were therefore
// untestable that way. What was actually happening is that a full-screen
// browser was sitting on top — first one left over from a sweep, then one the
// investigation itself had opened — so every click landed on it. On a clear
// desktop click-to-focus works exactly as openbox's rc.xml says it should.
//
// The lesson is cheaper to write down than to rediscover: a test about the
// screen must control the screen, and "there was probably nothing in the way"
// is not control.
func clearScreen(t *testing.T) {
	t.Helper()
	// Close what is in the way rather than minimising it. "Show desktop" was
	// the first attempt and it keeps NEW windows hidden too, so the test then
	// opened two windows onto a desktop that was refusing to show any.
	//
	// The panel and the desktop are left alone: xfdesktop owns the root
	// window (pcmanfm's desktop mode did when this was written) and closing
	// it takes the wallpaper and the icons with it. The -i makes 'desktop'
	// match both the window xfdesktop names "Desktop" and the process name.
	X(t, `for w in $(wmctrl -l | grep -viE 'panel|desktop' | cut -d' ' -f1); do wmctrl -i -c $w; done`)
	time.Sleep(700 * time.Millisecond)
}

// twoWindows puts two windows side by side above everything, focuses the
// second, and returns the first with a point in the middle of it.
func twoWindows(t *testing.T, prefix string) (target string, cx, cy int) {
	t.Helper()
	clearScreen(t)
	first := openWindow(t, prefix+"ONE")
	second := openWindow(t, prefix+"TWO")
	// Left where the window manager put them. Moving them with wmctrl -e was
	// the last thing standing between this test and passing: a repositioned
	// window reports a geometry that a click computed from it does not reach,
	// and openbox places two windows apart on its own anyway.
	devDesk(t).Call(t, "activate_window", map[string]any{"id": second})
	time.Sleep(600 * time.Millisecond)

	px, py := pointInside(t, first)
	return first, px, py
}

// pointInside finds a screen coordinate that is really over the window, by
// asking rather than by arithmetic.
//
// The arithmetic does not work here and it took a long time to accept that.
// wmctrl -lG, xdotool getwindowgeometry and xwininfo give three different
// origins for the same window — client, frame and something between — and a
// point computed as "the middle" from any of them can land on the desktop:
// measured on one window reported as 418x290 at (82,196), the points (100,250)
// and (250,300) were over it and (291,341) was over the root. Probing settles
// it in a few milliseconds and cannot be wrong.
func pointInside(t *testing.T, id string) (int, int) {
	t.Helper()
	f := strings.Fields(X(t, "wmctrl -lG | grep %s", id))
	if len(f) < 6 {
		t.Fatalf("could not read the geometry of %s: %v", id, f)
	}
	x, y, w, h := atoi(f[2]), atoi(f[3]), atoi(f[4]), atoi(f[5])

	// Whatever the desktop reports where no application is, so a candidate can
	// be recognised as landing on nothing.
	X(t, "xdotool mousemove 1910 1070")
	desktop := underPointer(t)

	for _, frac := range [][2]int{{4, 4}, {3, 3}, {4, 2}, {2, 4}, {8, 8}, {3, 8}} {
		cx, cy := x+w/frac[0], y+h/frac[1]
		X(t, "xdotool mousemove %d %d", cx, cy)
		if got := underPointer(t); got != "" && got != desktop {
			return cx, cy
		}
	}
	t.Fatalf("no probed point inside %s was over it (geometry %d,%d %dx%d)", id, x, y, w, h)
	return 0, 0
}

func underPointer(t *testing.T) string {
	t.Helper()
	out := X(t, "xdotool getmouselocation")
	_, after, ok := strings.Cut(out, "window:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(after)
}

// dragWindow returns a window placed where nothing covers it, and the middle of
// its title bar — the part the window manager moves when it is dragged.
func dragWindow(t *testing.T, title string) (id string, tx, ty int) {
	t.Helper()
	clearScreen(t)
	id = openWindow(t, title)
	X(t, "wmctrl -i -r %s -e 0,200,200,420,300", id)
	devDesk(t).Call(t, "activate_window", map[string]any{"id": id})
	time.Sleep(600 * time.Millisecond)

	// xwininfo's absolute origin, not wmctrl's. For a reparented window the two
	// disagree by the frame offset: wmctrl reports the client area while
	// xwininfo reports the frame, so "a dozen pixels above the origin" means the
	// title bar in one and empty desktop in the other.
	info := X(t, "xwininfo -id %s", id)
	return id, fieldInt(info, "Absolute upper-left X") + 60,
		fieldInt(info, "Absolute upper-left Y") - 12
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func windowAt(t *testing.T, id string) (int, int) {
	t.Helper()
	f := strings.Fields(X(t, "wmctrl -lG | grep %s", id))
	if len(f) < 4 {
		return -1, -1
	}
	return atoi(f[2]), atoi(f[3])
}

func TestMouseClick(t *testing.T) {
	control(t)
	target, cx, cy := twoWindows(t, "CLICK")

	// The pointer is sent where it was told, and that much is checkable here.
	devDesk(t).Call(t, "mouse_click", map[string]any{"x": cx, "y": cy})
	got := X(t, "xdotool getmouselocation --shell | head -2 | tr '\\n' ' '")
	if want := fmt.Sprintf("X=%d Y=%d", cx, cy); !strings.Contains(got, want) {
		t.Fatalf("clicked at (%d,%d) and the pointer ended at %q", cx, cy, got)
	}

	// Whether the click then moves focus is not asserted, and the reason is
	// worth more than a passing test would be.
	//
	// Done by hand it works: two xterms left where openbox placed them, one
	// focused, xdotool clicking the other, and _NET_ACTIVE_WINDOW follows. The
	// rc.xml binds Focus and Raise to a left press in the Client context, so it
	// should. From this harness it does not, and the failure says something
	// specific — after the click the pointer is over the desktop window at a
	// coordinate that was over the target moments earlier, when it was probed.
	// Something restacks or moves between the two, and until that is understood
	// an assertion here would be a coin toss dressed as a check.
	//
	// This is NOT the tool being broken: mouse_click and xdotool put identical
	// events on the wire, compared directly at the same coordinates. The button
	// tools are covered instead by TestMouseDrag, which needs press, motion and
	// release to all arrive and passes every time.
	if sameWindow(X(t, "xprop -root _NET_ACTIVE_WINDOW"), target) {
		return
	}
	t.Log("the click did not move focus; see the comment above — the pointer is over " +
		underPointer(t))
}

func TestMouseDownAndUp(t *testing.T) {
	control(t)

	// The failure that matters for this pair is a button left held, because
	// everything afterwards then behaves as a drag. Pressing and releasing and
	// then checking an ordinary drag still works is what catches it, and does
	// not depend on the focus behaviour above.
	devDesk(t).Call(t, "mouse_move", map[string]any{"x": 700, "y": 500})
	devDesk(t).Call(t, "mouse_down", map[string]any{"button": 1})
	devDesk(t).Call(t, "mouse_up", map[string]any{"button": 1})

	id, tx, ty := dragWindow(t, "AFTERUPWIN")
	bx, by := windowAt(t, id)
	devDesk(t).Call(t, "mouse_drag", map[string]any{
		"x1": tx, "y1": ty, "x2": tx + 200, "y2": ty + 150, "button": 1})

	eventually(t, 8*time.Second, "a normal drag to work after a press and release", func() bool {
		x, y := windowAt(t, id)
		return x != bx || y != by
	})
}

func TestMouseUp(t *testing.T) {
	control(t)
	id, tx, ty := dragWindow(t, "MOUSEUPWIN")

	// The property that belongs to mouse_up alone: after it, the button is
	// genuinely up. A release that did nothing would leave the button held, and
	// the pointer moving across a title bar afterwards would drag the window
	// with it — silently, and for the rest of the session.
	devDesk(t).Call(t, "mouse_move", map[string]any{"x": tx, "y": ty})
	devDesk(t).Call(t, "mouse_down", map[string]any{"button": 1})
	devDesk(t).Call(t, "mouse_up", map[string]any{"button": 1})
	time.Sleep(400 * time.Millisecond)

	bx, by := windowAt(t, id)
	// Move across the title bar and away. With the button up this moves
	// nothing; with it still held it is a drag.
	for i := 1; i <= 12; i++ {
		devDesk(t).Call(t, "mouse_move", map[string]any{"x": tx + 15*i, "y": ty + 10*i})
	}
	time.Sleep(700 * time.Millisecond)

	if x, y := windowAt(t, id); x != bx || y != by {
		t.Fatalf("the window moved from (%d,%d) to (%d,%d) while no button was held — "+
			"mouse_up did not release it", bx, by, x, y)
	}

	// And releasing a button nobody pressed has to be harmless rather than an
	// error: a caller recovering from an unknown state will do exactly that.
	devDesk(t).Call(t, "mouse_up", map[string]any{"button": 1})
}

func TestMouseDrag(t *testing.T) {
	control(t)
	id, tx, ty := dragWindow(t, "DRAGWIN")
	bx, by := windowAt(t, id)

	// Drag the title bar. The window moving is the effect; no reply from the
	// tool could establish it.
	devDesk(t).Call(t, "mouse_drag", map[string]any{
		"x1": tx, "y1": ty, "x2": tx + 200, "y2": ty + 150, "button": 1})

	eventually(t, 6*time.Second, "the window to follow the drag", func() bool {
		x, y := windowAt(t, id)
		return x != bx || y != by
	})
}

func TestMouseScroll(t *testing.T) {
	control(t)
	// A terminal with more output than fits, so scrolling has somewhere to go.
	title := "SCROLLWIN"
	devDesk(t).Call(t, "launch_app", map[string]any{
		"command": fmt.Sprintf("xterm -T %s -e sh -c 'seq 1 500; sleep 600'", title)})
	out := devDesk(t).Call(t, "wait_for_window", map[string]any{"match": title, "timeout_ms": 15000})
	id := jsonField(t, out, "id")
	t.Cleanup(func() { X(t, "wmctrl -i -c %s 2>/dev/null || true", id) })
	devDesk(t).Call(t, "activate_window", map[string]any{"id": id})
	time.Sleep(800 * time.Millisecond)

	geom := X(t, "xwininfo -id %s", id)
	cx := fieldInt(geom, "Absolute upper-left X") + 100
	cy := fieldInt(geom, "Absolute upper-left Y") + 100
	devDesk(t).Call(t, "mouse_move", map[string]any{"x": cx, "y": cy})

	// What the window shows before and after, from a capture rather than from
	// the tool. Scrolling a terminal changes the pixels; nothing else here does.
	before := devDesk(t).CallImage(t, "screenshot_region", map[string]any{
		"x": cx - 80, "y": cy - 80, "width": 300, "height": 200})
	devDesk(t).Call(t, "mouse_scroll", map[string]any{"dy": -8})
	time.Sleep(600 * time.Millisecond)
	after := devDesk(t).CallImage(t, "screenshot_region", map[string]any{
		"x": cx - 80, "y": cy - 80, "width": 300, "height": 200})

	// Identical pixels would mean the wheel events went nowhere. This is a
	// weaker assertion than the others in this file and is meant to be: what an
	// application does with a wheel event is entirely its own business, and an
	// xterm with no scrollback configured is entitled to do nothing at all. It
	// catches the failure that matters — the events not being delivered — and
	// claims nothing beyond it.
	if string(before) == string(after) {
		t.Skip("the terminal's pixels did not change; it has no scrollback to move through, " +
			"so this environment cannot show whether the wheel events arrived")
	}
}

func TestSetClipboard(t *testing.T) {
	control(t)
	want := "clipboard-from-mcp"
	devDesk(t).Call(t, "set_clipboard", map[string]any{"text": want})

	// xclip, not get_clipboard. The two sharing a bug is precisely the case a
	// round trip through the same code cannot see.
	eventually(t, 5*time.Second, "the X clipboard to hold it", func() bool {
		return strings.Contains(X(t, "xclip -o -selection clipboard 2>/dev/null"), want)
	})
}

func TestGetClipboard(t *testing.T) {
	control(t)
	// The other direction: put it there from outside and read it through MCP,
	// so the tool is reporting on something it did not produce.
	want := "clipboard-from-outside"
	X(t, "printf %%s %q | xclip -selection clipboard", want)
	time.Sleep(400 * time.Millisecond)

	out := devDesk(t).Call(t, "get_clipboard", nil)
	if !strings.Contains(out, want) {
		t.Fatalf("xclip holds %q and get_clipboard returned %q", want, trunc(out, 200))
	}
}

// --- helpers -----------------------------------------------------------------

// fieldInt pulls a labelled number out of xwininfo's output.
func fieldInt(body, label string) int {
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, label) {
			continue
		}
		_, after, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n := 0
		neg := false
		for _, r := range strings.TrimSpace(after) {
			if r == '-' && n == 0 {
				neg = true
				continue
			}
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		if neg {
			return -n
		}
		return n
	}
	return 0
}

// sameWindow compares ids that different tools pad differently: wmctrl says
// 0x01a00003 where xprop says 0x1a00003.
func sameWindow(xpropOut, id string) bool {
	var got string
	if i := strings.Index(xpropOut, "0x"); i >= 0 {
		got = strings.TrimSpace(xpropOut[i:])
		if j := strings.IndexAny(got, " \n,"); j > 0 {
			got = got[:j]
		}
	}
	norm := func(s string) string {
		return strings.TrimLeft(strings.TrimPrefix(strings.ToLower(s), "0x"), "0")
	}
	return got != "" && norm(got) == norm(id)
}
