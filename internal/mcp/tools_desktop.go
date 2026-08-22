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

// Advanced MCP tools: windows, desktops, processes, fine-grained mouse,
// screen, OCR, files, audio and re-streaming.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jezek/xgb/xproto"
	"github.com/sentineldesk/desktop/internal/desktop"
	"github.com/sentineldesk/desktop/internal/media"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (s *Server) buildAdvancedTools() []toolDef {
	return []toolDef{
		// ---- windows ----
		{
			Name:        "get_active_window",
			Risk:        riskRead,
			Description: "Get the currently focused window: id, title, class and geometry.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "move_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Move a window to absolute screen coordinates.",
			InputSchema: schema(map[string]any{
				"id": pStr("window id from list_windows"), "x": pInt("X"), "y": pInt("Y"),
			}, "id", "x", "y"),
		},
		{
			Name:            "resize_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Resize a window to the given width and height in pixels.",
			InputSchema: schema(map[string]any{
				"id": pStr("window id"), "width": pInt("width"), "height": pInt("height"),
			}, "id", "width", "height"),
		},
		{
			Name:            "close_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Close a window gracefully (like clicking its X button).",
			InputSchema:     schema(map[string]any{"id": pStr("window id")}, "id"),
		},
		{
			Name:            "minimize_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Minimize (iconify) a window.",
			InputSchema:     schema(map[string]any{"id": pStr("window id")}, "id"),
		},
		{
			Name:            "maximize_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Maximize a window (both directions).",
			InputSchema:     schema(map[string]any{"id": pStr("window id")}, "id"),
		},
		{
			Name:            "restore_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Un-maximize a window back to its previous size.",
			InputSchema:     schema(map[string]any{"id": pStr("window id")}, "id"),
		},
		{
			Name:            "fullscreen_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description: "Put a window full screen, take it out, or toggle. " +
				"action defaults to toggle, which is what this used to do and " +
				"only that — say add or remove when you know which you want, " +
				"rather than reading the state first and guessing.",
			InputSchema: schema(map[string]any{
				"id":     pStr("window id"),
				"action": pStr("add | remove | toggle (default toggle)"),
			}, "id"),
		},
		{
			Name:            "set_window_desktop",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Move a window to another virtual desktop (workspace).",
			InputSchema: schema(map[string]any{
				"id": pStr("window id"), "desktop": pInt("desktop number, 0-based"),
			}, "id", "desktop"),
		},
		{
			Name:        "wait_for_window",
			Risk:        riskRead,
			Description: "Wait until a window whose title or class contains the given text appears. Returns its info, or an error on timeout. Use after launch_app instead of guessing a wait time.",
			InputSchema: schema(map[string]any{
				"match": pStr("substring of the title or class"), "timeout_ms": pIntDef("timeout (default 15000)", 15000),
			}, "match"),
		},
		// ---- desktops ----
		{
			Name:        "list_desktops",
			Risk:        riskRead,
			Description: "List the virtual desktops (workspaces): number, name and which one is current.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "switch_desktop",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Switch to another virtual desktop (workspace) by number.",
			InputSchema:     schema(map[string]any{"desktop": pInt("desktop number, 0-based")}, "desktop"),
		},
		// ---- processes ----
		{
			Name:        "list_processes",
			Risk:        riskRead,
			Description: "List running processes with pid, cpu%, mem% and command. Optionally filter by a substring.",
			InputSchema: schema(map[string]any{"filter": pStr("optional substring to filter by")}),
		},
		{
			Name:            "kill_process",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Terminate a process by pid, or every process matching a name.",
			InputSchema: schema(map[string]any{
				"pid": pInt("process id"), "name": pStr("process name (pkill)"), "force": pBool("send SIGKILL instead of SIGTERM"),
			}),
		},
		{
			Name:        "is_running",
			Risk:        riskRead,
			Description: "Check whether a process matching the given name is running; returns the matching pids.",
			InputSchema: schema(map[string]any{"name": pStr("process name")}, "name"),
		},
		{
			Name:        "list_installed_apps",
			Risk:        riskRead,
			Description: "List the graphical applications installed on the desktop (from .desktop entries): name and command. For command-line programs rather than menu entries, use list_commands.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name: "list_commands",
			Risk: riskRead,
			Description: "List the command-line programs available to run, from the executables on PATH. " +
				"Called with nothing it returns the categories and how many commands are in each — the " +
				"packaging system's own sections (net, vcs, admin, text, editors, devel) rather than " +
				"hundreds of bare names. Then ask for one category, or filter by name. Each command " +
				"comes back with its package and category, and is marked when it also has a desktop " +
				"entry, so the graphical and command-line halves of what is installed can be told apart.",
			InputSchema: schema(map[string]any{
				"filter":   pStr("substring to match against the command name"),
				"category": pStr("list one category, from the categories the unfiltered call returns"),
				"describe": pBool("include the one-line description of the package each command came from"),
				"limit":    pIntDef("max results (default 100)", 100),
			}),
		},
		// ---- fine pointer control ----
		{
			Name:            "mouse_drag",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Drag with the mouse from one point to another (press, move, release).",
			InputSchema: schema(map[string]any{
				"x1": pInt("start X"), "y1": pInt("start Y"), "x2": pInt("end X"), "y2": pInt("end Y"),
				"button": pIntDef("button, default 1", 1),
			}, "x1", "y1", "x2", "y2"),
		},
		{
			Name:            "mouse_scroll",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Scroll the mouse wheel. Positive dy scrolls down, negative up.",
			InputSchema: schema(map[string]any{
				"dy": pInt("vertical clicks"), "dx": pInt("horizontal clicks"),
			}),
		},
		{
			Name:        "get_mouse_position",
			Risk:        riskRead,
			Description: "Get the current mouse pointer position.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "mouse_down",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Press and hold a mouse button (pair with mouse_up).",
			InputSchema:     schema(map[string]any{"button": pInt("1=left 2=middle 3=right")}),
		},
		{
			Name:            "mouse_up",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Release a mouse button.",
			InputSchema:     schema(map[string]any{"button": pInt("1=left 2=middle 3=right")}),
		},
		// ---- screen ----
		{
			Name:        "screenshot_region",
			Risk:        riskRead,
			Description: "Capture only a rectangular region of the screen as PNG. Cheaper than a full screenshot when you only need part of the screen.",
			InputSchema: schema(map[string]any{
				"x": pInt("left"), "y": pInt("top"), "width": pInt("width"), "height": pInt("height"),
			}, "x", "y", "width", "height"),
		},
		{
			Name:        "get_screen_info",
			Risk:        riskRead,
			Description: "Get screen resolution, colour depth and the number of virtual desktops.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "get_pixel_color",
			Risk:        riskRead,
			Description: "Read the RGB colour of a single pixel on screen (useful to assert UI state cheaply).",
			InputSchema: schema(map[string]any{"x": pInt("X"), "y": pInt("Y")}, "x", "y"),
		},
		// ---- OCR ----
		{
			Name: "read_screen_text",
			Risk: riskRead,
			Description: "Read the text on screen (or in a region) without sending an image. " +
				"Takes it from the accessibility tree where the application exposes one and " +
				"falls back to OCR where it does not; the reply says which, because OCR is a " +
				"guess and the tree is not.",
			InputSchema: schema(map[string]any{
				"x": pInt("optional region left"), "y": pInt("optional region top"),
				"width": pInt("optional region width"), "height": pInt("optional region height"),
				"lang":   pStr("tesseract language, default eng — OCR only"),
				"source": pStr("auto (default) | accessibility | ocr"),
			}),
		},
		{
			Name: "find_text",
			Risk: riskRead,
			Description: "Find text on screen and return its coordinates, so you can click on it. " +
				"Prefers the accessibility tree, where the position is exact, and falls back to " +
				"OCR with a per-word confidence. The reply says which answered.",
			InputSchema: schema(map[string]any{
				"text": pStr("text to look for (case-insensitive)"),
				"x":    pInt("optional region left"), "y": pInt("optional region top"),
				"width": pInt("optional region width"), "height": pInt("optional region height"),
				"lang":   pStr("tesseract language, default eng — OCR only"),
				"source": pStr("auto (default) | accessibility | ocr"),
			}, "text"),
		},
		// ---- files ----
		{
			Name:        "read_file",
			Risk:        riskRead,
			Description: "Read a text file from the desktop filesystem. Set as_root:true for files the desktop user cannot read (/etc/shadow, another user's home).",
			InputSchema: schema(map[string]any{
				"path": pStr("absolute path"), "max_bytes": pIntDef("truncate after N bytes (default 100000)", 100000),
				"as_root": pBool("read with root privileges (default false)"),
			}, "path"),
		},
		{
			Name:        "write_file",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Write (or create) a text file on the desktop filesystem. Set as_root:true to write outside the home directory (/etc, /usr/local/bin, a systemd or supervisor unit).",
			InputSchema: schema(map[string]any{
				"path": pStr("absolute path"), "content": pStr("file content"), "append": pBool("append instead of overwrite"),
				"as_root": pBool("write with root privileges (default false)"),
				"mode":    pStr("permissions in octal to apply afterwards, e.g. 0755 (only with as_root)"),
			}, "path", "content"),
		},
		{
			Name:        "list_directory",
			Risk:        riskRead,
			Description: "List the entries of a directory with size and type. Set as_root:true for directories the desktop user cannot enter.",
			InputSchema: schema(map[string]any{
				"path": pStr("absolute path"), "as_root": pBool("list with root privileges (default false)"),
			}, "path"),
		},
		// ---- audio ----
		{
			Name: "get_audio_state",
			Risk: riskRead,
			Description: "Get the default audio sink, its volume and whether it is " +
				"muted. `volume_percent` is the number set_volume takes, so read it " +
				"before changing the volume if you intend to put it back.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "set_volume",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Set the desktop output volume (0-150 percent) and/or mute state.",
			InputSchema: schema(map[string]any{
				"percent": pInt("volume percent"), "mute": pBool("mute on/off"),
			}),
		},
		// ---- re-streaming ----
		{
			Name:            "start_restream",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description: "Also send the desktop to an external destination: rtmp:// or rtmps:// " +
				"for YouTube/Twitch/Facebook, srt:// or udp:// for a VLC or OBS you run " +
				"yourself. The broadcast runs its own capture beside the live session — " +
				"pointer included, at its own bitrate — so going live never interrupts " +
				"or degrades what the room is watching. " +
				"This publishes what is on screen — ask the people in the room first.",
			InputSchema: schema(map[string]any{
				"url":      pStr("destination, e.g. rtmp://a.rtmp.youtube.com/live2/KEY"),
				"platform": pStr("youtube | twitch | facebook | custom (default custom)"),
				"audio":    pBool("include audio, default true"),
				"keyframes": pBool("force a keyframe every 2s. The platforms need this and get " +
					"it regardless; for a custom destination leave it off unless viewers " +
					"join mid-stream, because keyframes cost bitrate that would otherwise " +
					"go to keeping text sharp."),
				"bitrate": pInt("standalone fallback only, ignored when a room is running"),
				"fps":     pInt("standalone fallback only, ignored when a room is running"),
			}, "url"),
		},
		{
			Name:            "stop_restream",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Stop sending to an external destination.",
			InputSchema: schema(map[string]any{
				"id": pStr("which destination (its platform name); omit to stop them all"),
			}),
		},
		{
			Name: "list_restreams",
			Risk: riskRead,
			Description: "Which external destinations this desktop is currently being sent to. " +
				"Stream keys come back redacted.",
			InputSchema: schema(map[string]any{}),
		},
		// ---- info ----
		{
			Name:        "get_desktop_info",
			Risk:        riskRead,
			Description: "Overall desktop status: window manager, resolution, uptime, load, memory, video encoder in use and whether a recording is running.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name: "desktop_state",
			Risk: riskRead,
			Description: "Where things stand, in one call: every window with its " +
				"geometry, which one has focus, the virtual desktops, the screen " +
				"size, and who is in the room and holds control. Prefer this over " +
				"calling list_windows, get_active_window, list_desktops and " +
				"room_state separately — those are four answers from four different " +
				"moments, and this is one answer from one.",
			InputSchema: schema(map[string]any{}),
		},
	}
}

// dispatchAdvanced runs the window, process, OCR, file and streaming
// tools. The third return value says whether the name was recognised, so that
// dispatch() can fall through to the next group when it was not.
func (s *Server) dispatchAdvanced(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	// ---- windows ----
	case "get_active_window":
		c, e := s.toolActiveWindow()
		return c, e, true
	case "move_window":
		// -1 for the pair being left alone: MoveResize turns that into a flag
		// saying the field is absent, rather than a size the manager ignores.
		return s.winOp(args, "moved", func(e *desktop.EWMH, w xproto.Window) error {
			return e.MoveResize(w, argInt(args, "x"), argInt(args, "y"), -1, -1)
		}, "wmctrl-geom-move")
	case "resize_window":
		return s.winOp(args, "resized", func(e *desktop.EWMH, w xproto.Window) error {
			return e.MoveResize(w, -1, -1, argInt(args, "width"), argInt(args, "height"))
		}, "wmctrl-geom-resize")
	case "close_window":
		return s.winOp(args, "closed window", func(e *desktop.EWMH, w xproto.Window) error {
			return e.CloseWindow(w)
		}, "wmctrl", "-i", "-c", argStr(args, "id"))
	case "minimize_window":
		return s.winOp(args, "minimized", func(e *desktop.EWMH, w xproto.Window) error {
			return e.Minimize(w)
		}, "xdotool", "windowminimize", argStr(args, "id"))
	case "maximize_window":
		// Both axes in one message: one state change the manager can act on,
		// rather than two it has to reconcile.
		return s.winOp(args, "maximized", func(e *desktop.EWMH, w xproto.Window) error {
			return e.SetState(w, desktop.StateAdd, "maximized_vert", "maximized_horz")
		}, "wmctrl", "-i", "-r", argStr(args, "id"), "-b", "add,maximized_vert,maximized_horz")
	case "restore_window":
		return s.winOp(args, "restored", func(e *desktop.EWMH, w xproto.Window) error {
			return e.SetState(w, desktop.StateRemove, "maximized_vert", "maximized_horz")
		}, "wmctrl", "-i", "-r", argStr(args, "id"), "-b", "remove,maximized_vert,maximized_horz")
	case "fullscreen_window":
		// action defaults to toggle, which is what this always did. Naming it
		// lets a caller say what it wants instead of reading the state first
		// and guessing which way a toggle will go.
		act := desktop.StateToggle
		verb := "toggled fullscreen"
		switch strings.ToLower(argStr(args, "action")) {
		case "add", "on", "true":
			act, verb = desktop.StateAdd, "fullscreen on"
		case "remove", "off", "false":
			act, verb = desktop.StateRemove, "fullscreen off"
		}
		return s.winOp(args, verb, func(e *desktop.EWMH, w xproto.Window) error {
			return e.SetState(w, act, "fullscreen")
		}, "wmctrl", "-i", "-r", argStr(args, "id"), "-b", "toggle,fullscreen")
	case "set_window_desktop":
		return s.winOp(args, "moved to desktop", func(e *desktop.EWMH, w xproto.Window) error {
			return e.SetWindowDesktop(w, argInt(args, "desktop"))
		}, "wmctrl", "-i", "-r", argStr(args, "id"), "-t", strconv.Itoa(argInt(args, "desktop")))
	case "wait_for_window":
		c, e := s.toolWaitForWindow(ctx, argStr(args, "match"), argInt(args, "timeout_ms"))
		return c, e, true
	// ---- desktops ----
	case "list_desktops":
		c, e := s.toolListDesktops()
		return c, e, true
	case "switch_desktop":
		if e, err := s.windows(); err == nil {
			if err := e.SwitchDesktop(argInt(args, "desktop")); err == nil {
				return textContent("switched desktop"), false, true
			}
		}
		c, e := s.simpleRun("switched desktop", "wmctrl", "-s", strconv.Itoa(argInt(args, "desktop")))
		return c, e, true
	// ---- processes ----
	case "list_processes":
		c, e := s.toolListProcesses(argStr(args, "filter"))
		return c, e, true
	case "kill_process":
		c, e := s.toolKillProcess(args)
		return c, e, true
	case "is_running":
		c, e := s.toolIsRunning(argStr(args, "name"))
		return c, e, true
	case "list_commands":
		c, e := s.toolListCommands(args)
		return c, e, true
	case "list_installed_apps":
		c, e := s.toolListInstalledApps()
		return c, e, true
	// ---- mouse ----
	case "mouse_drag":
		c, e := s.toolMouseDrag(args)
		return c, e, true
	case "mouse_scroll":
		s.injector.Wheel(argInt(args, "dy"), argInt(args, "dx"))
		return textContent("scrolled dy=%d dx=%d", argInt(args, "dy"), argInt(args, "dx")), false, true
	case "get_mouse_position":
		x, y, err := s.injector.Pointer()
		if err != nil {
			return textContent("pointer failed: %v", err), true, true
		}
		return jsonContent(map[string]int{"x": x, "y": y}), false, true
	case "mouse_down", "mouse_up":
		btn := argInt(args, "button")
		if btn == 0 {
			btn = 1
		}
		s.injector.Button(btn, name == "mouse_down")
		return textContent("%s button %d", name, btn), false, true
	// ---- screen ----
	case "screenshot_region":
		b64, err := desktop.GrabRegionPNG(s.display, argInt(args, "x"), argInt(args, "y"), argInt(args, "width"), argInt(args, "height"))
		if err != nil {
			return textContent("screenshot_region failed: %v", err), true, true
		}
		return imageContent(b64, "image/png"), false, true
	case "get_screen_info":
		c, e := s.toolScreenInfo()
		return c, e, true
	case "get_pixel_color":
		r, g, b, err := s.injector.Pixel(argInt(args, "x"), argInt(args, "y"))
		if err != nil {
			return textContent("get_pixel_color failed: %v", err), true, true
		}
		return jsonContent(map[string]any{
			"r": r, "g": g, "b": b, "hex": fmt.Sprintf("#%02x%02x%02x", r, g, b),
		}), false, true
	// ---- OCR ----
	case "read_screen_text":
		c, e := s.toolReadScreenText(args)
		return c, e, true
	case "find_text":
		c, e := s.toolFindText(args)
		return c, e, true
	// ---- files ----
	case "read_file":
		asRoot, _ := args["as_root"].(bool)
		c, e := s.toolReadFile(argStr(args, "path"), argInt(args, "max_bytes"), asRoot)
		return c, e, true
	case "write_file":
		c, e := s.toolWriteFile(args)
		return c, e, true
	case "list_directory":
		asRoot, _ := args["as_root"].(bool)
		c, e := s.toolListDirectory(argStr(args, "path"), asRoot)
		return c, e, true
	// ---- audio ----
	case "get_audio_state":
		c, e := s.toolAudioState()
		return c, e, true
	case "set_volume":
		c, e := s.toolSetVolume(args)
		return c, e, true
	// ---- re-streaming ----
	case "start_restream":
		c, e := s.toolStartRestream(args)
		return c, e, true
	case "stop_restream":
		c, e := s.toolStopRestream(args)
		return c, e, true
	case "list_restreams":
		c, e := s.toolListRestreams()
		return c, e, true
	// ---- info ----
	case "get_desktop_info":
		c, e := s.toolDesktopInfo()
		return c, e, true
	case "desktop_state":
		c, e := s.toolDesktopState()
		return c, e, true
	}
	return nil, false, false
}

// --- implementations --------------------------------------------------------

func (s *Server) simpleRun(okMsg, bin string, args ...string) ([]map[string]any, bool) {
	if err := s.run(bin, args...); err != nil {
		return textContent("%s failed: %v", bin, err), true
	}
	return textContent("%s", okMsg), false
}

// wmctrlGeom moves and/or resizes a window (-1 = leave unchanged).
func (s *Server) wmctrlGeom(id string, x, y, w, h int, verb string) ([]map[string]any, bool) {
	geom := fmt.Sprintf("0,%d,%d,%d,%d", x, y, w, h)
	if err := s.run("wmctrl", "-i", "-r", id, "-e", geom); err != nil {
		return textContent("%s failed: %v", verb, err), true
	}
	return textContent("%s %s (%s)", verb, id, geom), false
}

func (s *Server) toolActiveWindow() ([]map[string]any, bool) {
	// One property read where this used to be three xdotool processes, and the
	// geometry comes back as numbers rather than as the paragraph xdotool
	// prints.
	if e, err := s.windows(); err == nil {
		info, ok, err := e.ActiveWindow()
		if err == nil {
			if !ok {
				// Nothing focused is an answer. It used to be reported as an
				// error, which left a caller unable to tell "the desktop is
				// idle" from "the query broke".
				return jsonContent(map[string]any{
					"active": nil,
					"note":   "no window currently has focus",
				}), false
			}
			return jsonContent(info), false
		}
	}

	id, err := s.output("xdotool", "getactivewindow")
	if err != nil {
		return textContent("no active window: %v", err), true
	}
	idNum := strings.TrimSpace(id)
	n, _ := strconv.Atoi(idNum)
	hexID := fmt.Sprintf("0x%08x", n)
	name, _ := s.output("xdotool", "getwindowname", idNum)
	geom, _ := s.output("xdotool", "getwindowgeometry", idNum)
	return jsonContent(map[string]any{
		"id":       hexID,
		"id_dec":   idNum,
		"title":    strings.TrimSpace(name),
		"geometry": strings.TrimSpace(geom),
	}), false
}

// toolWaitForWindow blocks until a window whose title or class contains match
// exists, or the timeout passes.
//
// This used to run wmctrl every 300ms — about fifty processes across a
// fifteen-second wait, to be told nothing had happened forty-nine times, with
// the answer arriving up to a third of a second after the fact. The window
// manager publishes the change on _NET_CLIENT_LIST the moment it happens, so
// the wait is now a blocking read woken by X, answered in about a millisecond,
// spawning nothing.
//
// The polling path is kept for a display with no EWMH window manager, where
// there is no property to watch and no event to wait for. It reads through the
// same matcher, so the two paths cannot disagree about what counts as a match.
func (s *Server) toolWaitForWindow(ctx context.Context, match string, timeoutMs int) ([]map[string]any, bool) {
	if timeoutMs <= 0 {
		timeoutMs = 15000
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	needle := strings.ToLower(match)

	// found is the whole test, shared by both paths. It reads windows through
	// EWMH rather than parsing wmctrl's columns, which is also what stopped
	// list_desktops mistaking a work-area size for a desktop name.
	var hit map[string]any
	found := func() bool {
		e, err := s.windows()
		if err != nil {
			return false
		}
		wins, err := e.Windows()
		if err != nil {
			return false
		}
		for _, w := range wins {
			if strings.Contains(strings.ToLower(w.Title), needle) ||
				strings.Contains(strings.ToLower(w.Class), needle) {
				hit = map[string]any{
					"id": w.ID, "class": w.Class, "title": w.Title, "found": true,
				}
				return true
			}
		}
		return false
	}

	if w, err := s.watch(); err == nil {
		// One second rather than 300ms for the backstop: it exists only for
		// title changes on an already-mapped window, which the root watcher
		// cannot see, and every other case is answered by the event.
		if w.WaitFor(ctx, timeout, time.Second, found, desktop.WatchWindows, desktop.WatchActive) {
			return jsonContent(hit), false
		}
		if ctx.Err() != nil {
			return textContent("cancelled while waiting for a window matching %q", match), true
		}
		return textContent("timeout: no window matching %q after %d ms", match, timeoutMs), true
	}

	// No watcher: fall back to asking repeatedly, the way this always worked.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if found() {
			return jsonContent(hit), false
		}
		if !sleepCtx(ctx, 300*time.Millisecond) {
			break
		}
	}
	return textContent("timeout: no window matching %q after %d ms", match, timeoutMs), true
}

func (s *Server) toolListDesktops() ([]map[string]any, bool) {
	// _NET_DESKTOP_NAMES rather than the column arithmetic below, which assumed
	// the name began at field 8 and lost a desktop called "Build 2 of 3".
	if e, err := s.windows(); err == nil {
		if list, err := e.Desktops(); err == nil {
			return jsonContent(list), false
		}
	}

	out, err := s.output("wmctrl", "-d")
	if err != nil {
		return textContent("list_desktops failed: %v", err), true
	}
	var desks []map[string]any
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		num, _ := strconv.Atoi(f[0])
		desks = append(desks, map[string]any{
			"number": num, "current": f[1] == "*", "name": strings.Join(f[min(len(f), 8):], " "),
		})
	}
	return jsonContent(desks), false
}

func (s *Server) toolListProcesses(filter string) ([]map[string]any, bool) {
	out, err := s.output("ps", "-eo", "pid,pcpu,pmem,comm,args", "--sort=-pcpu")
	if err != nil {
		return textContent("list_processes failed: %v", err), true
	}
	var procs []map[string]any
	needle := strings.ToLower(filter)
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i == 0 {
			continue // encabezado
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		cmdline := strings.Join(f[4:], " ")
		if len(cmdline) > 120 {
			cmdline = cmdline[:120] + "…"
		}
		procs = append(procs, map[string]any{
			"pid": pid, "cpu": f[1], "mem": f[2], "name": f[3], "command": cmdline,
		})
		if len(procs) >= 60 {
			break
		}
	}
	return jsonContent(procs), false
}

func (s *Server) toolKillProcess(args map[string]any) ([]map[string]any, bool) {
	force, _ := args["force"].(bool)
	sig := "-TERM"
	if force {
		sig = "-KILL"
	}
	if pid := argInt(args, "pid"); pid > 0 {
		if err := s.run("kill", sig, strconv.Itoa(pid)); err != nil {
			return textContent("kill %d failed: %v", pid, err), true
		}
		return textContent("killed pid %d", pid), false
	}
	if name := argStr(args, "name"); name != "" {
		if err := s.run("pkill", sig, "-f", name); err != nil {
			// pkill exits 1 for "nothing matched" and non-zero for "matched and
			// could not signal", and reporting both as nothing matched sends a
			// caller looking for a process that is plainly running. The usual
			// cause is ownership: this daemon is not root, and pkill only
			// signals what its user owns.
			if out, lookErr := s.output("pgrep", "-f", name); lookErr == nil && strings.TrimSpace(out) != "" {
				return textContent(
					"%q matched %d process(es) and none could be signalled — "+
						"they belong to another user",
					name, len(strings.Fields(out))), true
			}
			return textContent("no process matched %q", name), true
		}
		return textContent("killed processes matching %q", name), false
	}
	return textContent("give a pid or a name"), true
}

func (s *Server) toolIsRunning(name string) ([]map[string]any, bool) {
	out, err := s.output("pgrep", "-f", name)
	pids := []int{}
	for _, line := range strings.Fields(out) {
		if n, e := strconv.Atoi(line); e == nil {
			pids = append(pids, n)
		}
	}
	return jsonContent(map[string]any{"running": err == nil && len(pids) > 0, "pids": pids}), false
}

func (s *Server) toolListInstalledApps() ([]map[string]any, bool) {
	dirs := []string{"/usr/share/applications", "/usr/local/share/applications"}
	var apps []map[string]any
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".desktop") {
				continue
			}
			data, err := os.ReadFile(dir + "/" + e.Name())
			if err != nil {
				continue
			}
			var appName, execCmd string
			noDisplay := false
			for _, line := range strings.Split(string(data), "\n") {
				switch {
				case strings.HasPrefix(line, "Name=") && appName == "":
					appName = strings.TrimPrefix(line, "Name=")
				case strings.HasPrefix(line, "Exec=") && execCmd == "":
					execCmd = strings.TrimPrefix(line, "Exec=")
				case strings.HasPrefix(line, "NoDisplay=true"):
					noDisplay = true
				}
			}
			if appName != "" && execCmd != "" && !noDisplay {
				apps = append(apps, map[string]any{"name": appName, "exec": execCmd})
			}
		}
	}
	return jsonContent(apps), false
}

func (s *Server) toolMouseDrag(args map[string]any) ([]map[string]any, bool) {
	btn := argInt(args, "button")
	if btn == 0 {
		btn = 1
	}
	x1, y1 := argInt(args, "x1"), argInt(args, "y1")
	x2, y2 := argInt(args, "x2"), argInt(args, "y2")
	s.injector.Move(x1, y1)
	time.Sleep(60 * time.Millisecond)
	s.injector.Button(btn, true)
	time.Sleep(60 * time.Millisecond)
	// Move in steps: many applications ignore an instantaneous jump, because
	// they are watching for motion events rather than a final position.
	const steps = 12
	for i := 1; i <= steps; i++ {
		s.injector.Move(x1+(x2-x1)*i/steps, y1+(y2-y1)*i/steps)
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	s.injector.Button(btn, false)
	return textContent("dragged (%d,%d) -> (%d,%d)", x1, y1, x2, y2), false
}

func (s *Server) toolScreenInfo() ([]map[string]any, bool) {
	w, h := s.injector.Screen()
	desks, _ := s.output("wmctrl", "-d")
	return jsonContent(map[string]any{
		"width": w, "height": h, "display": s.display,
		"desktops": len(strings.Split(strings.TrimRight(desks, "\n"), "\n")),
	}), false
}

// --- OCR ------------------------------------------------------------------

// ocrImage captures — upscaled 2x, which is what makes tesseract reliable on
// small UI text — and runs OCR over it. mode "" gives plain text, "tsv" adds
// coordinates.
func (s *Server) ocrImage(args map[string]any, mode string) (string, error) {
	x, y := argInt(args, "x"), argInt(args, "y")
	w, h := argInt(args, "width"), argInt(args, "height")
	lang := argStr(args, "lang")
	if lang == "" {
		lang = "eng"
	}
	tmp, err := os.CreateTemp("", "sentineldesk-ocr-*.png")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	screenW, screenH := s.injector.Screen()
	if err := desktop.GrabForOCR(s.display, path, x, y, w, h, screenW, screenH); err != nil {
		return "", err
	}
	cmd := exec.Command("tesseract", path, "stdout", "-l", lang, mode)
	if mode == "" {
		cmd = exec.Command("tesseract", path, "stdout", "-l", lang)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return string(out), nil
}

// commandOrigin is what the packaging system knows about one executable.
type commandOrigin struct {
	Package string
	Section string
	Summary string // the package's one-line description
}

// packageIndex maps an executable's absolute path to the package that shipped
// it and the section that package belongs to.
//
// The section is the point. Debian curates it — net, vcs, admin, text, editors,
// devel — so grouping by it produces categories somebody chose rather than ones
// guessed from a name. Nothing else available here is as good: man page
// sections separate user commands from admin ones and stop there, and the
// filename tells you nothing at all.
//
// Built once and kept. The section for every package is one 800KB read, but the
// path-to-package direction lives in a file per package and totals eleven
// megabytes, which is fine to do once in a long-lived daemon and not fine to do
// per call. Only paths already known to be executables on PATH are kept, so
// what stays in memory is a few hundred entries rather than the whole
// filesystem manifest.
func (s *Server) packages(wanted map[string]bool) map[string]commandOrigin {
	s.pkgOnce.Do(func() {
		s.pkgIndex = buildPackageIndex(wanted)
	})
	return s.pkgIndex
}

func buildPackageIndex(wanted map[string]bool) map[string]commandOrigin {
	// Package -> section and summary, from the one file that has every
	// installed package.
	//
	// The summary comes from here rather than from whatis, which was the
	// obvious choice and does not work: this image strips documentation through
	// dpkg path-exclude rules, so /usr/share/man/man1 is empty and whatis
	// answers "nothing appropriate" for everything. dpkg's own Description
	// field is already in the file being read for the section, needs no index
	// to have been built, and survives an image with no manuals at all. It
	// describes the package rather than the command, so git and git-shell share
	// one line — which is why the field is named for the package.
	section, summary := map[string]string{}, map[string]string{}
	if data, err := os.ReadFile("/var/lib/dpkg/status"); err == nil {
		var pkg string
		for _, line := range strings.Split(string(data), "\n") {
			if name, ok := strings.CutPrefix(line, "Package: "); ok {
				pkg = strings.TrimSpace(name)
			} else if sec, ok := strings.CutPrefix(line, "Section: "); ok && pkg != "" {
				section[pkg] = strings.TrimSpace(sec)
			} else if d, ok := strings.CutPrefix(line, "Description: "); ok && pkg != "" {
				// Only the synopsis: the continuation lines that follow are the
				// long description, and a paragraph per command is not what a
				// listing is for.
				summary[pkg] = strings.TrimSpace(d)
			}
		}
	}

	out := map[string]commandOrigin{}
	entries, err := os.ReadDir("/var/lib/dpkg/info")
	if err != nil {
		return out
	}
	for _, e := range entries {
		name, ok := strings.CutPrefix(e.Name(), "")
		if !ok || !strings.HasSuffix(name, ".list") {
			continue
		}
		pkg := strings.TrimSuffix(name, ".list")
		// A package installed for one architecture is listed as pkg:arch; the
		// section is filed under the bare name.
		if base, _, found := strings.Cut(pkg, ":"); found {
			pkg = base
		}
		data, err := os.ReadFile(filepath.Join("/var/lib/dpkg/info", e.Name()))
		if err != nil {
			continue
		}
		for _, path := range strings.Split(string(data), "\n") {
			if !wanted[path] {
				continue
			}
			out[path] = commandOrigin{Package: pkg, Section: section[pkg], Summary: summary[pkg]}
		}
	}
	return out
}

// toolListCommands lists the executables on PATH.
//
// list_installed_apps answers half the question of what is installed — the half
// with a menu entry. The other half had no tool at all, and the only way to
// find out whether a command existed was to run one, which is riskDanger. So an
// agent under MCP_POLICY=readonly or safe could inventory the graphical desktop
// and was blind to the command line: it could not establish whether git was
// present without permission to execute something. Reading directories is a
// read, and classifying it as one is what closes that.
//
// The unfiltered case deliberately does not return every name. This container
// has 902 executables, most of them plumbing nobody asked about —
// git-upload-archive, git-receive-pack — and handing a model seven kilobytes of
// those is the same mistake as returning a screenful of image placeholders: an
// answer that is complete and useless. Counts and directories say what is there
// and how to ask again.
func (s *Server) toolListCommands(args map[string]any) ([]map[string]any, bool) {
	filter := strings.ToLower(strings.TrimSpace(argStr(args, "filter")))
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 100
	}
	describe, _ := args["describe"].(bool)

	// Which commands also appear in the menu, so the two halves can be told
	// apart in one answer rather than by calling both tools and joining them.
	desktopCmds := map[string]bool{}
	if entries, err := os.ReadDir("/usr/share/applications"); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".desktop") {
				continue
			}
			data, err := os.ReadFile(filepath.Join("/usr/share/applications", e.Name()))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				exec, ok := strings.CutPrefix(line, "Exec=")
				if !ok {
					continue
				}
				exec, _, _ = strings.Cut(strings.TrimSpace(exec), " ")
				desktopCmds[filepath.Base(exec)] = true
				break
			}
		}
	}

	seen := map[string]string{} // name -> the directory it was found in
	perDir := map[string]int{}
	var dirs []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		dirs = append(dirs, dir)
		for _, e := range entries {
			name := e.Name()
			// Earlier directories on PATH win, which is what the shell would
			// actually run.
			if _, dup := seen[name]; dup {
				continue
			}
			info, err := e.Info()
			if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
				continue
			}
			seen[name] = dir
			perDir[dir]++
		}
	}

	// Ask the packaging system what each executable is for. Only the paths
	// found above are looked up, so the index stays small.
	wanted := make(map[string]bool, len(seen))
	for name, dir := range seen {
		wanted[filepath.Join(dir, name)] = true
	}
	origins := s.packages(wanted)

	category := strings.ToLower(strings.TrimSpace(argStr(args, "category")))

	if filter == "" && category == "" {
		// Sections rather than names: a category with a count tells a caller
		// what is here and what to ask for next, where 902 names tell them
		// only that they asked the wrong question.
		bySection := map[string]int{}
		for name, dir := range seen {
			sec := origins[filepath.Join(dir, name)].Section
			if sec == "" {
				sec = "unpackaged"
			}
			bySection[sec]++
		}
		return jsonContent(map[string]any{
			"total":        len(seen),
			"with_desktop": len(desktopCmds),
			"categories":   bySection,
			"directories":  perDir,
			"searched":     dirs,
			"note":         "pass category to list one group, or filter to search by name",
		}), false
	}

	var names []string
	for name, dir := range seen {
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}
		if category != "" {
			sec := origins[filepath.Join(dir, name)].Section
			if sec == "" {
				sec = "unpackaged"
			}
			if !strings.EqualFold(sec, category) {
				continue
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	matched := len(names)
	if len(names) > limit {
		names = names[:limit]
	}

	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		path := filepath.Join(seen[name], name)
		o := origins[path]
		entry := map[string]any{"command": name, "path": path}
		if o.Package != "" {
			entry["package"] = o.Package
			if o.Section != "" {
				entry["category"] = o.Section
			}
		}
		if desktopCmds[name] {
			entry["desktop_entry"] = true
		}
		if describe && o.Summary != "" {
			entry["description"] = o.Summary
		}
		out = append(out, entry)
	}
	res := map[string]any{"matched": matched, "of": len(seen), "commands": out}
	if matched > len(names) {
		res["note"] = fmt.Sprintf("showing %d of %d; raise limit or narrow the filter", len(names), matched)
	}
	return jsonContent(res), false
}

// cleanA11yText strips what the accessibility tree contributes but nobody can
// read, and returns "" for anything that was only that.
//
// An image inside a run of text appears as U+FFFC, the object replacement
// character. A toolbar of icons therefore comes back as a line of them, and a
// browser window produces a dozen such lines — pure cost to a model reading the
// screen, carrying not one bit about what is there. The same goes for the zero
// width characters layout code leaves behind.
func cleanA11yText(s string) string {
	// Written as escapes rather than as themselves: the last of them is U+FEFF,
	// which a Go source file beginning with it would be read as a byte order
	// mark, and none of the others can be seen in a diff anyway.
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\uFFFC', // object replacement: an image or widget inside text
			'\u200B', '\u200C', '\u200D', // zero-width space, non-joiner, joiner
			'\uFEFF': // zero-width no-break space
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// ocrWord is one row of tesseract's TSV: a word and the box it occupies.
type ocrWord struct {
	text                     string
	left, top, width, height int
	conf                     float64
}

// ocrLineMatches finds a phrase in tesseract's word-level output.
//
// The TSV numbers every word with the block, paragraph and line it belongs to,
// which is enough to put the line back together. Searching the reassembled line
// is what makes a phrase findable at all; mapping the match position back onto
// the words it covers is what keeps the coordinates usable, since a caller
// wants to click the phrase and not its first word.
//
// Coordinates come back in screen space: the capture was at 2x and may have
// been of a sub-region, so every box is halved and then shifted by the region's
// origin. Getting that wrong sends a click to the wrong part of the screen,
// which is worse than not finding the text.
func ocrLineMatches(tsv, needle string, offX, offY int) []map[string]any {
	type lineKey struct{ block, par, line string }
	var order []lineKey
	words := map[lineKey][]ocrWord{}

	for i, row := range strings.Split(tsv, "\n") {
		if i == 0 {
			continue // the TSV header
		}
		f := strings.Split(row, "\t")
		if len(f) < 12 {
			continue
		}
		text := strings.TrimSpace(f[11])
		if text == "" {
			continue
		}
		k := lineKey{f[2], f[3], f[4]}
		if _, seen := words[k]; !seen {
			order = append(order, k)
		}
		left, _ := strconv.Atoi(f[6])
		top, _ := strconv.Atoi(f[7])
		width, _ := strconv.Atoi(f[8])
		height, _ := strconv.Atoi(f[9])
		conf, _ := strconv.ParseFloat(f[10], 64)
		words[k] = append(words[k], ocrWord{text, left, top, width, height, conf})
	}

	var hits []map[string]any
	for _, k := range order {
		ws := words[k]
		// Rebuild the line, remembering where each word starts so a match
		// position can be traced back to the words underneath it.
		var sb strings.Builder
		starts := make([]int, len(ws))
		for i, w := range ws {
			if i > 0 {
				sb.WriteByte(' ')
			}
			starts[i] = sb.Len()
			sb.WriteString(w.text)
		}
		line := sb.String()
		lower := strings.ToLower(line)

		from := 0
		for {
			at := strings.Index(lower[from:], needle)
			if at < 0 {
				break
			}
			start := from + at
			end := start + len(needle)
			from = start + 1 // overlapping occurrences still count

			// Union of every word the match touches.
			x0, y0 := 1<<31-1, 1<<31-1
			x1, y1 := 0, 0
			minConf := 100.0
			touched := false
			for i, w := range ws {
				wStart, wEnd := starts[i], starts[i]+len(w.text)
				if wEnd <= start || wStart >= end {
					continue
				}
				touched = true
				x0, y0 = min(x0, w.left), min(y0, w.top)
				x1, y1 = max(x1, w.left+w.width), max(y1, w.top+w.height)
				// The weakest word governs: a phrase is only as trustworthy as
				// its least certain part, and averaging would hide the one
				// character that sends a click somewhere else.
				if w.conf < minConf {
					minConf = w.conf
				}
			}
			if !touched {
				continue
			}
			x, y := offX+x0/2, offY+y0/2
			w, h := (x1-x0)/2, (y1-y0)/2
			hits = append(hits, map[string]any{
				"text": strings.TrimSpace(line[start:end]),
				"line": line,
				"x":    x, "y": y, "width": w, "height": h,
				"center_x": x + w/2, "center_y": y + h/2,
				"confidence": minConf,
			})
		}
	}
	return hits
}

// a11yElement is the part of the accessibility bridge's output these two tools
// need: what an element says, and where it is.
type a11yElement struct {
	Role   string   `json:"role"`
	Name   string   `json:"name"`
	Text   string   `json:"text"`
	X      int      `json:"x"`
	Y      int      `json:"y"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	State  []string `json:"state"`
}

func (e a11yElement) showing() bool {
	for _, st := range e.State {
		if st == "showing" {
			return true
		}
	}
	return false
}

// within reports whether the element overlaps the requested region. A zero
// width or height means the whole screen, which is how both tools already treat
// an absent region.
func (e a11yElement) within(x, y, w, h int) bool {
	if w <= 0 || h <= 0 {
		return true
	}
	return e.X < x+w && e.X+e.Width > x && e.Y < y+h && e.Y+e.Height > y
}

// screenElements reads the accessibility tree and returns what is on screen.
//
// This is the source read_screen_text and find_text should have been asking
// first. Where an application exposes its interface, the tree gives the text
// exactly as the program wrote it, with the real position of every string —
// against OCR, which reads pixels of anti-aliased 11px type and returns its
// best guess with a box around it.
func (s *Server) screenElements() ([]a11yElement, error) {
	out, err := s.a11yRaw("tree", "--limit", "4000", "--depth", "14")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Elements []a11yElement `json:"elements"`
		Error    string        `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("accessibility tree: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("%s", parsed.Error)
	}
	return parsed.Elements, nil
}

// toolReadScreenText reads what is on screen, preferring structure to pixels.
//
// OCR is the fallback rather than the method. It earns its place — it is the
// only thing that works on an application exposing no accessibility at all —
// but it is guessing, and the earlier sweeps show what that looks like:
// "SAVRANaAAAA SS", a whole desktop returned as "H ee ee we P| = ~ be ~ po".
// A caller cannot tell a misread from a reading, which is why the answer now
// says where it came from.
func (s *Server) toolReadScreenText(args map[string]any) ([]map[string]any, bool) {
	source := strings.ToLower(argStr(args, "source"))
	x, y := argInt(args, "x"), argInt(args, "y")
	w, h := argInt(args, "width"), argInt(args, "height")

	if source != "ocr" {
		els, err := s.screenElements()
		if err == nil {
			// Reading order rather than tree order: a caller is looking at a
			// screen, and the tree's idea of sequence is the application's.
			var visible []a11yElement
			for _, e := range els {
				if e.showing() && e.within(x, y, w, h) {
					visible = append(visible, e)
				}
			}
			sort.SliceStable(visible, func(i, j int) bool {
				if visible[i].Y != visible[j].Y {
					return visible[i].Y < visible[j].Y
				}
				return visible[i].X < visible[j].X
			})

			var lines []string
			seen := map[string]bool{}
			for _, e := range visible {
				for _, str := range []string{e.Text, e.Name} {
					str = cleanA11yText(str)
					// A label and its own accessible name are usually the same
					// string, and every container repeats its children's, so
					// without this the output is mostly echoes.
					if str == "" || seen[str] {
						continue
					}
					seen[str] = true
					lines = append(lines, str)
				}
			}
			if len(lines) > 0 {
				return jsonContent(map[string]any{
					"via": "accessibility", "text": strings.Join(lines, "\n"),
					"elements": len(visible),
				}), false
			}
		}
		if source == "accessibility" {
			return textContent("nothing on screen exposes text through accessibility"), true
		}
	}

	text, err := s.ocrImage(args, "")
	if err != nil {
		return textContent("read_screen_text failed: %v", err), true
	}
	return jsonContent(map[string]any{
		"via": "ocr", "text": strings.TrimSpace(text),
		"note": "read from pixels, so this is a guess — prefer ui_tree or browser_text where they apply",
	}), false
}

func (s *Server) toolFindText(args map[string]any) ([]map[string]any, bool) {
	needle := strings.ToLower(strings.TrimSpace(argStr(args, "text")))
	if needle == "" {
		return textContent("no text given"), true
	}
	source := strings.ToLower(argStr(args, "source"))

	// The accessibility tree first, where the answer is exact rather than
	// probable. This matters more here than in read_screen_text: these
	// coordinates are what a caller feeds to mouse_click, and a single
	// misread character in OCR produces a confident box around the wrong
	// thing, with nothing in the result to say so.
	if source != "ocr" {
		if els, err := s.screenElements(); err == nil {
			rx, ry := argInt(args, "x"), argInt(args, "y")
			rw, rh := argInt(args, "width"), argInt(args, "height")
			var hits []map[string]any
			for _, e := range els {
				if !e.showing() || !e.within(rx, ry, rw, rh) {
					continue
				}
				hay := strings.ToLower(e.Text + " " + e.Name)
				if !strings.Contains(hay, needle) {
					continue
				}
				label := strings.TrimSpace(e.Text)
				if label == "" {
					label = strings.TrimSpace(e.Name)
				}
				hits = append(hits, map[string]any{
					"text": label, "role": e.Role,
					"x": e.X, "y": e.Y, "width": e.Width, "height": e.Height,
					"center_x": e.X + e.Width/2, "center_y": e.Y + e.Height/2,
					// Not a confidence score dressed up as one: the tree either
					// contains the string or it does not.
					"exact": true,
				})
			}
			if len(hits) > 0 {
				return jsonContent(map[string]any{"via": "accessibility", "matches": hits}), false
			}
		}
		if source == "accessibility" {
			return textContent("no match for %q in the accessibility tree", needle), false
		}
	}

	tsv, err := s.ocrImage(args, "tsv")
	if err != nil {
		return textContent("find_text failed: %v", err), true
	}
	// OCR ran on a 2x capture, possibly of a sub-region. The coordinates handed
	// back must be SCREEN coordinates, or a mouse_click built from them lands in
	// the wrong place.
	offX, offY := argInt(args, "x"), argInt(args, "y")

	// Match against whole lines, not single words.
	//
	// tesseract's TSV is one row per word, and this used to test the needle
	// against each row on its own — so "Save changes" could never match
	// anything, because no row ever contains a space. Every multi-word search
	// came back "no match for … on screen", which reads as the text not being
	// there rather than as the tool only being able to look for one word at a
	// time. Reassembling each line from its words and searching that makes a
	// phrase findable, and the box is the union of the words it spans.
	hits := ocrLineMatches(tsv, needle, offX, offY)
	if len(hits) == 0 {
		return textContent("no match for %q on screen", needle), false
	}
	return jsonContent(map[string]any{
		"via": "ocr", "matches": hits,
		"note": "coordinates come from reading pixels; check confidence before clicking",
	}), false
}

// --- files --------------------------------------------------------------

func (s *Server) toolReadFile(path string, maxBytes int, asRoot bool) ([]map[string]any, bool) {
	if maxBytes <= 0 {
		maxBytes = 100000
	}
	var data []byte
	var err error
	if asRoot {
		// `cat` under sudo rather than os.ReadFile: the daemon runs unprivileged.
		data, err = rootRead(path)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return textContent("read_file failed: %v", err), true
	}
	truncated := false
	if len(data) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}
	txt := string(data)
	if truncated {
		txt += "\n…[truncated]"
	}
	return textContent("%s", txt), false
}

func (s *Server) toolWriteFile(args map[string]any) ([]map[string]any, bool) {
	path := argStr(args, "path")
	content := argStr(args, "content")
	appendMode, _ := args["append"].(bool)
	if asRoot, _ := args["as_root"].(bool); asRoot {
		n, err := rootWrite(path, content, appendMode, argStr(args, "mode"))
		if err != nil {
			return textContent("write_file failed: %v", err), true
		}
		return textContent("wrote %d bytes to %s (as root)", n, path), false
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return textContent("write_file failed: %v", err), true
	}
	defer f.Close()
	n, err := f.WriteString(content)
	if err != nil {
		return textContent("write_file failed: %v", err), true
	}
	return textContent("wrote %d bytes to %s", n, path), false
}

func (s *Server) toolListDirectory(path string, asRoot bool) ([]map[string]any, bool) {
	if asRoot {
		items, err := rootList(path)
		if err != nil {
			return textContent("list_directory failed: %v", err), true
		}
		return jsonContent(items), false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return textContent("list_directory failed: %v", err), true
	}
	var items []map[string]any
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		items = append(items, map[string]any{
			"name": e.Name(), "type": kind, "size": info.Size(),
			"modified": info.ModTime().Format(time.RFC3339),
		})
	}
	return jsonContent(items), false
}

// --- audio -----------------------------------------------------------------

// volumePercent pulls the first percentage out of what `pactl get-sink-volume`
// prints, which is a sentence rather than a value:
//
//	Volume: front-left: 65536 / 100% / 0.00 dB,   front-right: 65536 / 100% / 0.00 dB
//
// The first channel is enough: set_volume moves every channel together, so the
// only way they diverge is somebody setting them apart by hand outside this
// server — and the raw line is still reported for exactly that case.
var volumePct = regexp.MustCompile(`(\d+)%`)

func (s *Server) toolAudioState() ([]map[string]any, bool) {
	sink, _ := s.output("pactl", "get-default-sink")
	vol, _ := s.output("pactl", "get-sink-volume", "@DEFAULT_SINK@")
	mute, _ := s.output("pactl", "get-sink-mute", "@DEFAULT_SINK@")
	raw := strings.TrimSpace(strings.SplitN(vol, "\n", 2)[0])
	out := map[string]any{
		"sink":   strings.TrimSpace(sink),
		"volume": raw,
		"mute":   strings.Contains(mute, "yes"),
	}
	// The number as well as the prose. Without it this pair does not close:
	// set_volume takes an integer percent, so anything that changes the volume
	// and means to put it back had to regex a sentence to find out what to put
	// it back TO — and a tool that cannot undo itself is one an agent should
	// hesitate to use at all.
	if m := volumePct.FindStringSubmatch(raw); m != nil {
		if pct, err := strconv.Atoi(m[1]); err == nil {
			out["volume_percent"] = pct
		}
	}
	return jsonContent(out), false
}

func (s *Server) toolSetVolume(args map[string]any) ([]map[string]any, bool) {
	var msgs []string
	if p, ok := args["percent"]; ok {
		pct := 0
		switch v := p.(type) {
		case float64:
			pct = int(v)
		case string:
			pct, _ = strconv.Atoi(v)
		}
		if pct < 0 {
			pct = 0
		}
		if pct > 150 {
			pct = 150
		}
		if err := s.run("pactl", "set-sink-volume", "@DEFAULT_SINK@", strconv.Itoa(pct)+"%"); err != nil {
			return textContent("set_volume failed: %v", err), true
		}
		msgs = append(msgs, fmt.Sprintf("volume %d%%", pct))
	}
	if m, ok := args["mute"].(bool); ok {
		val := "0"
		if m {
			val = "1"
		}
		if err := s.run("pactl", "set-sink-mute", "@DEFAULT_SINK@", val); err != nil {
			return textContent("set mute failed: %v", err), true
		}
		msgs = append(msgs, "mute="+val)
	}
	if len(msgs) == 0 {
		return textContent("nothing to set"), true
	}
	return textContent("%s", strings.Join(msgs, ", ")), false
}

// --- re-streaming ----------------------------------------------------------

func (s *Server) toolStartRestream(args map[string]any) ([]map[string]any, bool) {
	url := argStr(args, "url")
	if url == "" {
		return textContent("no url given"), true
	}
	audio := true
	if v, ok := args["audio"].(bool); ok {
		audio = v
	}

	// The room already has this desktop encoded and on the wire. Forwarding
	// that is the whole point of the tee: a destination costs a mux and a
	// socket instead of a second encoder competing with the session people are
	// watching.
	if s.room != nil {
		platform := strings.ToLower(strings.TrimSpace(argStr(args, "platform")))
		if platform == "" {
			platform = "custom"
		}
		kf, _ := args["keyframes"].(bool)
		if !s.room.CanRestream() {
			return textContent("this session is encoding VP8, which no streaming " +
				"destination accepts; restart the desktop with ENCODER=x264"), true
		}
		t := media.RestreamTarget{
			ID: platform, Platform: platform, URL: url, Audio: audio,
			KeyframeSec: restreamKeyframes(platform, kf),
		}
		if err := s.room.StartRestream(t); err != nil {
			return textContent("start_restream failed: %v", err), true
		}
		return textContent("streaming to %s (%s, audio=%v) — its own capture beside "+
			"the live session, pointer included", platform, redactKey(url), audio), false
	}

	// No room: nothing is being captured, so there is nothing to reuse and a
	// capture of our own is the only option. This is the standalone bridge
	// process, where nobody is watching a session to interrupt.
	s.restreamMu.Lock()
	defer s.restreamMu.Unlock()
	if s.restream != nil {
		return textContent("a re-stream is already running to %s", s.restreamURL), true
	}
	kbps := argInt(args, "bitrate")
	if kbps <= 0 {
		kbps = 3000
	}
	fps := argInt(args, "fps")
	if fps <= 0 {
		fps = 30
	}
	sink := "rtmpsink"
	if strings.HasPrefix(url, "srt://") {
		sink = "srtsink"
	}
	desc := fmt.Sprintf(
		"flvmux name=mux streamable=true ! %s location=%s "+
			"ximagesrc display-name=%s show-pointer=true use-damage=0 "+
			"! video/x-raw,framerate=%d/1 ! videoconvert ! queue "+
			"! x264enc tune=zerolatency speed-preset=veryfast bitrate=%d key-int-max=%d "+
			"! h264parse ! mux.",
		sink, url, s.display, fps, kbps, fps*2)
	if audio {
		desc += fmt.Sprintf(" pulsesrc device=%s ! audioconvert ! audioresample ! queue ! avenc_aac bitrate=128000 ! aacparse ! mux.", s.cfg.AudioDevice)
	}
	cmd := exec.Command("gst-launch-1.0", media.SplitArgs(desc)...)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return textContent("start_restream failed: %v", err), true
	}
	go cmd.Wait()
	s.restream = cmd
	s.restreamURL = url
	return textContent("re-streaming to %s (%d kbps, %d fps)", url, kbps, fps), false
}

// restreamKeyframes mirrors the toolbar's rule so the agent and a person
// starting the same destination get the same stream. See platformKeyframes in
// the stream package for why the platforms are not asked.
func restreamKeyframes(platform string, wanted bool) int {
	switch platform {
	case "youtube", "twitch", "facebook":
		return 2
	}
	if wanted {
		return 2
	}
	return 0
}

// redactKey keeps the stream key out of the transcript. It is a credential:
// whoever reads it can broadcast to that channel.
func redactKey(raw string) string {
	i := strings.LastIndex(raw, "/")
	if i < 0 || i == len(raw)-1 {
		return raw
	}
	return raw[:i+1] + "•••"
}

func (s *Server) toolListRestreams() ([]map[string]any, bool) {
	if s.room == nil {
		s.restreamMu.Lock()
		defer s.restreamMu.Unlock()
		if s.restream == nil {
			return textContent("not streaming anywhere"), false
		}
		return textContent("streaming to %s (standalone capture)", redactKey(s.restreamURL)), false
	}
	list := s.room.Restreams()
	if len(list) == 0 {
		return textContent("not streaming anywhere"), false
	}
	lines := make([]string, 0, len(list))
	for _, d := range list {
		lines = append(lines, fmt.Sprintf("%s → %s (audio=%v, %ds)",
			d.Platform, d.URL, d.Audio, d.Seconds))
	}
	return textContent("%s", strings.Join(lines, "\n")), false
}

func (s *Server) toolStopRestream(args map[string]any) ([]map[string]any, bool) {
	if s.room != nil {
		list := s.room.Restreams()
		if len(list) == 0 {
			return textContent("no re-stream running"), true
		}
		if id := argStr(args, "id"); id != "" {
			if err := s.room.StopRestream(id); err != nil {
				return textContent("%v", err), true
			}
			return textContent("stopped streaming to %s", id), false
		}
		for _, d := range list {
			_ = s.room.StopRestream(d.ID)
		}
		return textContent("stopped %d destination(s)", len(list)), false
	}

	s.restreamMu.Lock()
	defer s.restreamMu.Unlock()
	if s.restream == nil {
		return textContent("no re-stream running"), true
	}
	_ = s.restream.Process.Signal(syscall.SIGINT)
	time.Sleep(500 * time.Millisecond)
	if s.restream.ProcessState == nil || !s.restream.ProcessState.Exited() {
		_ = s.restream.Process.Kill()
	}
	url := s.restreamURL
	s.restream = nil
	s.restreamURL = ""
	return textContent("stopped re-stream to %s", url), false
}

// --- info ------------------------------------------------------------------

func (s *Server) toolDesktopInfo() ([]map[string]any, bool) {
	w, h := s.injector.Screen()
	wm, _ := s.output("wmctrl", "-m")
	wmName := ""
	for _, line := range strings.Split(wm, "\n") {
		if strings.HasPrefix(line, "Name:") {
			wmName = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		}
	}
	uptime, _ := s.output("uptime")
	free, _ := s.output("sh", "-c", "free -m | awk 'NR==2{print $3\"/\"$2\" MB\"}'")
	s.restreamMu.Lock()
	restreaming := s.restream != nil
	s.restreamMu.Unlock()
	return jsonContent(map[string]any{
		"window_manager": wmName,
		"resolution":     fmt.Sprintf("%dx%d", w, h),
		"display":        s.display,
		"encoder":        s.cfg.Encoder,
		"uptime":         strings.TrimSpace(uptime),
		"memory_used":    strings.TrimSpace(free),
		"recording":      s.recorder.Status()["recording"],
		"restreaming":    restreaming,
	}), false
}

// toolDesktopState answers "where do things stand" once, instead of four times.
//
// The four tools it replaces — list_windows, get_active_window, list_desktops
// and room_state — each return something true, and an agent that calls all four
// assembles a picture that was never true all at once: a window can close
// between the first and the second, control can change hands between the third
// and the fourth. The gaps are small and the consequences are not, because what
// gets built on top of that picture is a decision about whether to act.
//
// Everything here comes off one EWMH reader and one room, so the snapshot is as
// close to a single instant as this can be without freezing the X server. The
// room is read last on purpose: it is the part that governs whether the agent
// may act at all, so it should be the freshest thing in the answer.
func (s *Server) toolDesktopState() ([]map[string]any, bool) {
	state := map[string]any{}

	e, err := s.windows()
	if err != nil {
		// No EWMH is not "no answer". The room half still decides whether the
		// agent may act, and reporting it beats failing the whole call over the
		// half that broke — the alternative sends a caller back to the four
		// separate tools, three of which would fail the same way.
		state["windows_error"] = fmt.Sprintf("cannot read the window list: %v", err)
	} else {
		if wins, err := e.Windows(); err == nil {
			state["windows"] = wins
		} else {
			state["windows_error"] = err.Error()
		}
		// The id alone rather than a second copy of the record: it is already in
		// `windows`, and two copies are two things that can be read as
		// disagreeing.
		if info, ok, err := e.ActiveWindow(); err == nil && ok {
			state["active"] = info.ID
		} else {
			// Nothing focused is a state, not a failure — it is what a desktop
			// reports between the last window closing and the next one mapping.
			state["active"] = nil
		}
		if desks, err := e.Desktops(); err == nil {
			state["desktops"] = desks
			for _, d := range desks {
				if d.Current {
					state["current_desktop"] = d.Number
				}
			}
		}
	}

	w, h := s.injector.Screen()
	state["screen"] = map[string]any{"width": w, "height": h}

	if s.room == nil {
		// Said out loud rather than left absent. A missing key reads as "no
		// information about the room"; this build genuinely has no arbitration,
		// which means the agent may act, and that is worth stating.
		state["room"] = nil
		state["you_have_control"] = true
		state["note"] = "this build has no room attached; input is unarbitrated"
		return jsonContent(state), false
	}

	s.room.JoinAgent(s.agentName)
	members := s.room.Members()
	people := make([]map[string]any, 0, len(members))
	for _, m := range members {
		people = append(people, map[string]any{
			"id": m.ID, "name": m.Name,
			"controller": m.Controller, "agent": m.Agent,
			"seconds": m.Seconds,
		})
	}
	ctlID, ctlName := s.room.Controller()
	state["room"] = map[string]any{
		"participants":   people,
		"humans_present": s.room.HumansPresent(),
		"controller":     ctlName,
		"controller_id":  ctlID,
	}
	// Lifted out of the room object as well as inside it: this is the one field
	// that decides whether the next call is allowed, and it should not be
	// reachable only by walking into a nested object.
	state["you_have_control"] = s.room.IsController(AgentID)
	if !s.room.IsController(AgentID) {
		state["note"] = "Control is always claimed, never assumed — even with " +
			"nobody else here. Call request_control before anything that moves " +
			"the pointer or types."
	}
	return jsonContent(state), false
}

// --- utilities ---------------------------------------------------------------

func floatSlice(v any) []float64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		if f, ok := e.(float64); ok {
			out = append(out, f)
		} else {
			out = append(out, 0)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = json.Marshal

// winOp runs a window operation through the native EWMH path, falling back to
// the external command when the display cannot be reached.
//
// The fallback is the same principle as everything optional here — degrade,
// do not fail — and it is why the wmctrl arguments are still passed in. When
// they read "wmctrl-geom-move" there is no single command to fall back to,
// because the old path built a geometry string; those two lose the fallback
// rather than reconstruct it, and say so.
func (s *Server) winOp(args map[string]any, verb string,
	native func(*desktop.EWMH, xproto.Window) error,
	fallback ...string) ([]map[string]any, bool, bool) {

	id := argStr(args, "id")
	if id == "" {
		return textContent("no window id"), true, true
	}
	e, err := s.windows()
	if err == nil {
		win, perr := desktop.ParseWindowID(id)
		if perr != nil {
			return textContent("%v", perr), true, true
		}
		if err := native(e, win); err == nil {
			return textContent("%s", verb), false, true
		} else if len(fallback) > 0 && !strings.HasPrefix(fallback[0], "wmctrl-geom") {
			// Fall through to the external command below.
			_ = err
		} else {
			return textContent("%s failed: %v", verb, err), true, true
		}
	}
	if len(fallback) == 0 || strings.HasPrefix(fallback[0], "wmctrl-geom") {
		return textContent("%s failed: no display", verb), true, true
	}
	c, isErr := s.simpleRun(verb, fallback[0], fallback[1:]...)
	return c, isErr, true
}
