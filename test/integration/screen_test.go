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

// Reading the desktop: what is on it, where, and what colour.
//
// Each test names the tool it is for, and uses whatever else it needs to put the
// desktop in a state worth reading. get_pixel_color cannot be tested without
// something of a known colour on screen, so it paints one; that the painting
// used four other tools does not make it a test of those.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGetScreenInfo(t *testing.T) {
	out := devDesk(t).Call(t, "get_screen_info", nil)

	// X is the authority on its own geometry, so the answer is checked against
	// xdpyinfo rather than against the resolution the daemon was configured
	// with — a tool reporting its own configuration back would pass that.
	dims := X(t, "xdpyinfo | awk '/dimensions:/{print $2}'")
	w, h, _ := strings.Cut(dims, "x")
	if !strings.Contains(out, w) || !strings.Contains(out, h) {
		t.Fatalf("get_screen_info says %s, X says %s", out, dims)
	}
}

func TestGetDesktopInfo(t *testing.T) {
	out := devDesk(t).Call(t, "get_desktop_info", nil)
	for _, want := range []string{"display", "resolution"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in %s", want, out)
		}
	}
	// The window manager it names has to be the one actually running.
	if strings.Contains(out, "openbox") && Sh(t, "pgrep -c openbox") == "0" {
		t.Error("it reports openbox and openbox is not running")
	}
}

func TestGetMousePosition(t *testing.T) {
	control(t)
	// Move somewhere known first: reading the pointer wherever it happens to be
	// proves nothing, because any answer would look plausible.
	devDesk(t).Call(t, "mouse_move", map[string]any{"x": 640, "y": 480})

	out := devDesk(t).Call(t, "get_mouse_position", nil)
	x, y := jsonField(t, out, "x"), jsonField(t, out, "y")
	if x != "640" || y != "480" {
		t.Fatalf("the pointer reads (%s,%s) after being sent to (640,480)", x, y)
	}
	// And X agrees, which is the point of asking twice.
	if got := X(t, "xdotool getmouselocation --shell | head -2 | tr '\\n' ' '"); !strings.Contains(got, "X=640") {
		t.Fatalf("X puts the pointer at %q", got)
	}
}

func TestGetPixelColor(t *testing.T) {
	control(t)
	// Something of a known colour, and a window rather than the root.
	//
	// Two earlier versions read the wrong thing and both were the test's fault.
	// The first sampled a corner and got #333344, a browser page left open
	// across the display. The second painted the root with xsetroot and got the
	// wallpaper: the desktop (pcmanfm then, xfdesktop today) owns the root
	// window, so anything drawn underneath it is never seen. A window of a
	// stated colour avoids both, because it is on top and it is ours.
	devDesk(t).Call(t, "launch_app", map[string]any{
		// A named colour: the launcher splits on whitespace and a leading
		// # is a comment to the shell underneath, so "-bg #00ff00" never
		// reached xterm at all.
		"command": "xterm -T PIXELWIN -bg green -e sleep 120"})
	out := devDesk(t).Call(t, "wait_for_window", map[string]any{
		"match": "PIXELWIN", "timeout_ms": 15000})
	id := jsonField(t, out, "id")
	t.Cleanup(func() { X(t, "wmctrl -i -c %s 2>/dev/null || true", id) })
	X(t, "wmctrl -i -r %s -b add,above", id)
	time.Sleep(800 * time.Millisecond)

	f := strings.Fields(X(t, "wmctrl -lG | grep PIXELWIN"))
	if len(f) < 6 {
		t.Fatalf("could not find the window's geometry: %v", f)
	}
	cx := atoiScreen(f[2]) + atoiScreen(f[4])/2
	cy := atoiScreen(f[3]) + atoiScreen(f[5])/2

	got := devDesk(t).Call(t, "get_pixel_color", map[string]any{"x": cx, "y": cy})
	if !strings.Contains(strings.ToLower(got), "00ff00") {
		t.Fatalf("the middle of a green window reads %s", strings.Join(strings.Fields(got), " "))
	}
}

func atoiScreen(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestScreenshot(t *testing.T) {
	out := devDesk(t).Call(t, "screenshot", map[string]any{"destination": "file"})
	path := firstPath(out, ".png")
	if path == "" {
		t.Fatalf("no file path in %s", out)
	}
	// A PNG that decodes at the display's size. Checking only that the file
	// exists would pass for an empty one, which is how a broken capture looks
	// from the outside.
	info := Sh(t, "ffprobe -v error -show_entries stream=width,height,codec_name -of default=nw=1 %s", path)
	if !strings.Contains(info, "png") {
		t.Fatalf("%s is not a decodable PNG: %s", path, info)
	}
	dims := X(t, "xdpyinfo | awk '/dimensions:/{print $2}'")
	w, _, _ := strings.Cut(dims, "x")
	if !strings.Contains(info, "width="+w) {
		t.Fatalf("the capture is %s, the display is %s", info, dims)
	}
}

func TestScreenshotRegion(t *testing.T) {
	// It answers with the image inline rather than writing a file, so the bytes
	// are the answer and the only way to check the crop happened is to read
	// them. A tool that captured the whole screen and cropped nothing would
	// still hand back a perfectly valid PNG.
	raw := devDesk(t).CallImage(t, "screenshot_region", map[string]any{
		"x": 0, "y": 0, "width": 320, "height": 200})
	w, h := pngSize(t, raw)
	if w != 320 || h != 200 {
		t.Fatalf("asked for 320x200 and the image is %dx%d", w, h)
	}
}

func TestReadScreenText(t *testing.T) {
	title := "SCREENTEXTWIN"
	openWindow(t, title)

	out := devDesk(t).Call(t, "read_screen_text", nil)
	// The window's title is on screen, so it has to appear. Which source
	// answered is reported, and either is acceptable — the point is that the
	// reply describes this desktop and not a generic one.
	if !strings.Contains(out, "via") {
		t.Fatalf("the reply does not say where it read from: %s", out)
	}
	if !strings.Contains(out, title) {
		t.Errorf("a window titled %s is open and the text does not mention it:\n%s", title, trunc(out, 400))
	}
}

func TestFindText(t *testing.T) {
	title := "FINDTEXTWIN"
	openWindow(t, title)

	out := devDesk(t).Call(t, "find_text", map[string]any{"text": title})
	if strings.Contains(out, "no match") {
		t.Fatalf("find_text cannot find the title of a window that is open: %s", out)
	}
	// Coordinates a click could use, inside the screen.
	cx := jsonFirstNumber(out, "center_x")
	cy := jsonFirstNumber(out, "center_y")
	if cx <= 0 || cy <= 0 || cx > 1920 || cy > 1080 {
		t.Fatalf("the coordinates (%d,%d) are not on the screen: %s", cx, cy, trunc(out, 300))
	}
}

func TestListWindows(t *testing.T) {
	title := "LISTWINDOWSWIN"
	id := openWindow(t, title)

	out := devDesk(t).Call(t, "list_windows", nil)
	if !strings.Contains(out, title) {
		t.Errorf("the window just opened is missing from the list:\n%s", trunc(out, 400))
	}
	// Every id it reports must exist in X. A stale entry is worse than a
	// missing one: the caller acts on it.
	if !strings.Contains(X(t, "wmctrl -l"), strings.TrimPrefix(id, "0x0")) &&
		!strings.Contains(X(t, "wmctrl -l"), id) {
		t.Errorf("list_windows reports %s and X does not have it", id)
	}
}

func TestGetActiveWindow(t *testing.T) {
	control(t)
	title := "ACTIVEWIN"
	id := openWindow(t, title)
	devDesk(t).Call(t, "activate_window", map[string]any{"id": id})

	eventually(t, 5*time.Second, "the window to take focus", func() bool {
		return strings.Contains(devDesk(t).Call(t, "get_active_window", nil), title)
	})
	// And X's own idea of the active window is the same one.
	active := X(t, "xprop -root _NET_ACTIVE_WINDOW")
	if !strings.Contains(strings.ToLower(active), strings.ToLower(strings.TrimPrefix(id, "0x0"))) {
		t.Errorf("_NET_ACTIVE_WINDOW is %q, the tool says %s", strings.TrimSpace(active), id)
	}
}

func TestListDesktops(t *testing.T) {
	out := devDesk(t).Call(t, "list_desktops", nil)
	// One entry per desktop X has. The count coming from the same place twice
	// would not catch the parsing fault that once glued a work-area size onto a
	// desktop's name.
	want := X(t, "xprop -root _NET_NUMBER_OF_DESKTOPS | grep -oE '[0-9]+$'")
	n, _ := strconv.Atoi(want)
	if n > 0 && strings.Count(out, "\"number\"") != n {
		t.Errorf("X reports %d desktops, the tool lists %d:\n%s",
			n, strings.Count(out, "\"number\""), trunc(out, 300))
	}
	if strings.Contains(out, "x1044") || strings.Contains(out, "x1080") {
		t.Errorf("a desktop name contains a geometry, which is the old parsing fault: %s", out)
	}
}

// --- helpers used across this file -------------------------------------------

func firstPath(body, suffix string) string {
	for _, field := range strings.FieldsFunc(body, func(r rune) bool {
		return r == ' ' || r == '"' || r == '\n' || r == ':' || r == ',' || r == ']' || r == '['
	}) {
		if strings.HasPrefix(field, "/") && strings.HasSuffix(field, suffix) {
			return field
		}
	}
	return ""
}

func jsonFirstNumber(body, key string) int {
	i := strings.Index(body, "\""+key+"\"")
	if i < 0 {
		return 0
	}
	rest := body[i+len(key)+2:]
	rest = strings.TrimLeft(rest, ": \t")
	end := 0
	for end < len(rest) && (rest[end] == '-' || (rest[end] >= '0' && rest[end] <= '9')) {
		end++
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
