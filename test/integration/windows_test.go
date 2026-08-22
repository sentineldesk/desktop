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

// Windows, desktops and the waits.
//
// Every state here is one X property away from the truth, so each test reads
// _NET_WM_STATE or _NET_CURRENT_DESKTOP with xprop rather than asking the tool
// that set it. window_properties is the one exception and is tested the other
// way round: the property is written from outside and read back through MCP.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// testWindow opens a window for the test and cleans it up.
func testWindow(t *testing.T, title string) string {
	t.Helper()
	return openWindow(t, title)
}

// wmState is what X says about a window's state, which is the only authority.
func wmState(t *testing.T, id string) string {
	t.Helper()
	return X(t, "xprop -id %s _NET_WM_STATE", id)
}

func TestMoveWindow(t *testing.T) {
	control(t)
	id := testWindow(t, "MOVEWIN")

	devDesk(t).Call(t, "move_window", map[string]any{"id": id, "x": 300, "y": 260})

	// The FRAME origin, which is what was asked for: xwininfo's absolute is the
	// client and its relative is the offset inside the frame, so the difference
	// is the number the window manager was given.
	eventually(t, 6*time.Second, "the window to arrive", func() bool {
		info := X(t, "xwininfo -id %s", id)
		fx := fieldInt(info, "Absolute upper-left X") - fieldInt(info, "Relative upper-left X")
		fy := fieldInt(info, "Absolute upper-left Y") - fieldInt(info, "Relative upper-left Y")
		return abs(fx-300) <= 8 && abs(fy-260) <= 8
	})
}

func TestResizeWindow(t *testing.T) {
	control(t)
	id := testWindow(t, "RESIZEWIN")

	devDesk(t).Call(t, "resize_window", map[string]any{"id": id, "width": 600, "height": 380})

	// A terminal resizes in character cells, not pixels, so it lands on the
	// nearest cell boundary rather than exactly where it was sent — a cell here
	// is about six by thirteen. The tolerance is for that negotiation between
	// the application's size hints and the window manager, not slack for a tool
	// that missed: twenty pixels cannot hide a resize that did not happen, and
	// the window starts at 484x316.
	eventually(t, 6*time.Second, "the window to take the new size", func() bool {
		info := X(t, "xwininfo -id %s", id)
		return abs(fieldInt(info, "Width")-600) <= 20 && abs(fieldInt(info, "Height")-380) <= 20
	})
}

func TestMinimizeWindow(t *testing.T) {
	control(t)
	id := testWindow(t, "MINWIN")

	devDesk(t).Call(t, "minimize_window", map[string]any{"id": id})

	eventually(t, 6*time.Second, "the window to be hidden", func() bool {
		return strings.Contains(wmState(t, id), "_NET_WM_STATE_HIDDEN")
	})
}

func TestMaximizeWindow(t *testing.T) {
	control(t)
	id := testWindow(t, "MAXWIN")

	devDesk(t).Call(t, "maximize_window", map[string]any{"id": id})

	eventually(t, 6*time.Second, "both maximized states to be set", func() bool {
		st := wmState(t, id)
		return strings.Contains(st, "MAXIMIZED_HORZ") && strings.Contains(st, "MAXIMIZED_VERT")
	})
}

func TestRestoreWindow(t *testing.T) {
	control(t)
	id := testWindow(t, "RESTOREWIN")
	devDesk(t).Call(t, "maximize_window", map[string]any{"id": id})
	eventually(t, 6*time.Second, "it to be maximized first", func() bool {
		return strings.Contains(wmState(t, id), "MAXIMIZED_HORZ")
	})

	// It un-maximizes. It does NOT un-minimize, whatever the name suggests —
	// the sweep described it that way for a long time and tested it on a
	// window it had just hidden, where removing the maximized states means
	// nothing at all.
	devDesk(t).Call(t, "restore_window", map[string]any{"id": id})

	eventually(t, 6*time.Second, "the maximized states to go", func() bool {
		st := wmState(t, id)
		return !strings.Contains(st, "MAXIMIZED_HORZ") && !strings.Contains(st, "MAXIMIZED_VERT")
	})
}

func TestFullscreenWindow(t *testing.T) {
	control(t)
	id := testWindow(t, "FULLWIN")

	// add and remove rather than toggle, which is what the action argument was
	// added for: reading the state and guessing which way a toggle will go is
	// the thing it removes.
	devDesk(t).Call(t, "fullscreen_window", map[string]any{"id": id, "action": "add"})
	eventually(t, 6*time.Second, "fullscreen to be set", func() bool {
		return strings.Contains(wmState(t, id), "FULLSCREEN")
	})

	devDesk(t).Call(t, "fullscreen_window", map[string]any{"id": id, "action": "remove"})
	eventually(t, 6*time.Second, "fullscreen to be cleared", func() bool {
		return !strings.Contains(wmState(t, id), "FULLSCREEN")
	})
}

func TestWindowSetState(t *testing.T) {
	control(t)
	id := testWindow(t, "STATEWIN")

	devDesk(t).Call(t, "window_set_state", map[string]any{
		"id": id, "state": "above", "action": "add"})
	eventually(t, 6*time.Second, "above to be set", func() bool {
		return strings.Contains(wmState(t, id), "ABOVE")
	})

	devDesk(t).Call(t, "window_set_state", map[string]any{
		"id": id, "state": "above", "action": "remove"})
	eventually(t, 6*time.Second, "above to be cleared", func() bool {
		return !strings.Contains(wmState(t, id), "ABOVE")
	})
}

func TestActivateWindow(t *testing.T) {
	control(t)
	first := testWindow(t, "ACTONE")
	second := testWindow(t, "ACTTWO")
	devDesk(t).Call(t, "activate_window", map[string]any{"id": second})
	time.Sleep(500 * time.Millisecond)

	devDesk(t).Call(t, "activate_window", map[string]any{"id": first})

	eventually(t, 6*time.Second, "focus to move to the first window", func() bool {
		return sameWindow(X(t, "xprop -root _NET_ACTIVE_WINDOW"), first)
	})
}

func TestCloseWindow(t *testing.T) {
	control(t)
	id := testWindow(t, "CLOSEWIN")

	devDesk(t).Call(t, "close_window", map[string]any{"id": id})

	// Gone from the window list, and the process behind it gone too — a close
	// that only unmapped the window would leave an xterm running forever.
	eventually(t, 8*time.Second, "the window to close", func() bool {
		return !strings.Contains(X(t, "wmctrl -l"), "CLOSEWIN")
	})
	eventually(t, 8*time.Second, "its process to exit", func() bool {
		return atoi(Sh(t, "pgrep -f CLOSEWIN | wc -l")) <= 1
	})
}

func TestWindowProperties(t *testing.T) {
	control(t)
	id := testWindow(t, "PROPSWIN")
	// Written from outside, so the tool is reading something it did not set.
	X(t, "xprop -id %s -f IT_MARKER 8s -set IT_MARKER integration-marker", id)

	out := devDesk(t).Call(t, "window_properties", map[string]any{"id": id})
	if !strings.Contains(out, "integration-marker") {
		t.Fatalf("a property set with xprop is missing from the reply:\n%s", trunc(out, 400))
	}
	if !strings.Contains(out, "PROPSWIN") {
		t.Errorf("the window's own title is missing:\n%s", trunc(out, 300))
	}
}

func TestWindowHierarchy(t *testing.T) {
	control(t)
	testWindow(t, "HIERWIN")

	out := devDesk(t).Call(t, "window_hierarchy", nil)
	if !strings.Contains(out, "HIERWIN") {
		t.Fatalf("the window is open and the hierarchy does not have it:\n%s", trunc(out, 400))
	}
}

func TestSwitchDesktop(t *testing.T) {
	control(t)
	total := atoi(X(t, "xprop -root _NET_NUMBER_OF_DESKTOPS | grep -oE '[0-9]+$'"))
	if total < 2 {
		t.Skip("this desktop has only one workspace to switch between")
	}
	t.Cleanup(func() { X(t, "wmctrl -s 0") })

	devDesk(t).Call(t, "switch_desktop", map[string]any{"desktop": 1})
	eventually(t, 6*time.Second, "the current desktop to become 1", func() bool {
		return atoi(X(t, "xprop -root _NET_CURRENT_DESKTOP | grep -oE '[0-9]+$'")) == 1
	})

	devDesk(t).Call(t, "switch_desktop", map[string]any{"desktop": 0})
	eventually(t, 6*time.Second, "and back to 0", func() bool {
		return atoi(X(t, "xprop -root _NET_CURRENT_DESKTOP | grep -oE '[0-9]+$'")) == 0
	})
}

func TestSetWindowDesktop(t *testing.T) {
	control(t)
	total := atoi(X(t, "xprop -root _NET_NUMBER_OF_DESKTOPS | grep -oE '[0-9]+$'"))
	if total < 2 {
		t.Skip("this desktop has only one workspace to move a window to")
	}
	id := testWindow(t, "SENDWIN")

	devDesk(t).Call(t, "set_window_desktop", map[string]any{"id": id, "desktop": 1})

	eventually(t, 6*time.Second, "the window to move workspace", func() bool {
		return atoi(X(t, "xprop -id %s _NET_WM_DESKTOP | grep -oE '[0-9]+$'", id)) == 1
	})
}

func TestWaitForWindow(t *testing.T) {
	control(t)
	title := "LATEWINDOW"
	X(t, "wmctrl -c %s 2>/dev/null || true", title)

	// Something that appears two seconds from now, so the wait has a future to
	// wait for rather than a present to notice.
	ShUser(t, "setsid sh -c 'sleep 2; DISPLAY=:0 xterm -T %s -e sleep 120' </dev/null >/dev/null 2>&1 &", title)
	t.Cleanup(func() { X(t, "wmctrl -c %s 2>/dev/null || true", title) })

	start := time.Now()
	out := devDesk(t).Call(t, "wait_for_window", map[string]any{
		"match": title, "timeout_ms": 20000})
	waited := time.Since(start)

	if !strings.Contains(out, title) {
		t.Fatalf("the window opened and the wait did not report it: %s", trunc(out, 200))
	}
	if waited < time.Second {
		t.Errorf("returned in %v for a window that appears after two seconds", waited)
	}
	// It answers on the X event now rather than polling, so it should arrive
	// promptly after the window does — well inside the old 300ms tick plus the
	// program's own start-up.
	if waited > 8*time.Second {
		t.Errorf("took %v to notice a window that opened at two seconds", waited)
	}
	// And something that never opens must time out rather than claim success.
	devDesk(t).CallErr(t, "wait_for_window", map[string]any{
		"match": "NEVEROPENSWINDOW", "timeout_ms": 2000})
}

func TestWaitForIdle(t *testing.T) {
	control(t)
	// A quiet desktop settles; the call has to come back saying so.
	out := devDesk(t).Call(t, "wait_for_idle", map[string]any{
		"timeout_ms": 12000, "quiet_ms": 800, "ignore_cpu": true})
	if !strings.Contains(out, "\"idle\": true") {
		t.Fatalf("a still desktop did not settle: %s", trunc(out, 300))
	}

	// Now with something repainting continuously, it must NOT claim quiet.
	ShUser(t, "setsid sh -c 'DISPLAY=:0 xterm -T BUSYWIN -e sh -c \"while :; do ls -R /usr; done\"' </dev/null >/dev/null 2>&1 &")
	t.Cleanup(func() { X(t, "wmctrl -c BUSYWIN 2>/dev/null || true") })
	eventually(t, 15*time.Second, "the busy window to appear", func() bool {
		return strings.Contains(X(t, "wmctrl -l"), "BUSYWIN")
	})
	time.Sleep(1500 * time.Millisecond)

	busy := devDesk(t).Call(t, "wait_for_idle", map[string]any{
		"timeout_ms": 4000, "quiet_ms": 1500, "ignore_cpu": true})
	if strings.Contains(busy, "\"idle\": true") {
		t.Errorf("the screen is repainting continuously and it reported idle: %s", trunc(busy, 300))
	}
	// The reason has to name the right failure, which it did not before: a wait
	// that gave up on a moving screen used to blame the CPU it never sampled.
	if strings.Contains(busy, "CPU is still at") {
		t.Errorf("it blamed the CPU for a screen that never stopped changing: %s", trunc(busy, 300))
	}
}

func TestWait(t *testing.T) {
	start := time.Now()
	devDesk(t).Call(t, "wait", map[string]any{"ms": 1200})
	waited := time.Since(start)

	// It has to actually sleep — a tool that returned at once would let a model
	// believe it had given something time to happen.
	if waited < 1100*time.Millisecond {
		t.Fatalf("asked for 1200ms and it returned after %v", waited)
	}
	if waited > 4*time.Second {
		t.Errorf("asked for 1200ms and it took %v", waited)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

var _ = fmt.Sprintf
