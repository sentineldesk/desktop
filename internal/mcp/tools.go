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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sentineldesk/desktop/internal/desktop"
	"github.com/sentineldesk/desktop/internal/media"
	"github.com/sentineldesk/desktop/pkg/capability"
)

// toolDef is one entry in the MCP catalogue. It IS the capability definition —
// an alias, not a copy, and the distinction is the whole point: the definition
// moved to internal/capability so the DataChannel could read the same card
// (§4.6), and this name survives because twenty thousand lines of catalogue
// literals and their tests already speak it. The classification axes, the
// validation and the long comments explaining them live with the type now.
type toolDef = capability.Def

type riskLevel = capability.Risk

const (
	riskUnset  = capability.RiskUnset
	riskRead   = capability.RiskRead
	riskWrite  = capability.RiskWrite
	riskDanger = capability.RiskDanger
)

type visibility = capability.Visibility

const (
	visUnset   = capability.VisUnset
	visHidden  = capability.VisHidden
	visVisible = capability.VisVisible
	visInjects = capability.VisInjects
)

// --- JSON Schema helpers -------------------------------------------------
//
// Forwarders, so a catalogue file reads the same as it always has. The
// implementations moved with the definition type they build.

func schema(props map[string]any, required ...string) json.RawMessage {
	return capability.Schema(props, required...)
}
func pStr(desc string) map[string]any             { return capability.PStr(desc) }
func pInt(desc string) map[string]any             { return capability.PInt(desc) }
func pIntDef(desc string, def int) map[string]any { return capability.PIntDef(desc, def) }
func pBool(desc string) map[string]any            { return capability.PBool(desc) }

// --- catalogue -------------------------------------------------------------

func (s *Server) buildTools() []toolDef {
	base := []toolDef{
		{
			Name:        "screenshot",
			Risk:        riskRead,
			Description: "Capture the current desktop screen. destination: inline (default) returns the PNG to you; container writes it to a file on the desktop; download makes the browser of whoever is watching save it on their own machine. The capture is identical in all three — it comes straight from the X framebuffer, with no compression loss.",
			InputSchema: schema(map[string]any{
				"destination": pStr("inline | container | download (default inline)"),
				"path":        pStr("where to write it, with destination container/download (default the recordings directory)"),
			}),
		},
		{
			Name:            "mouse_move",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Move the mouse pointer to absolute screen coordinates (x, y).",
			InputSchema:     schema(map[string]any{"x": pInt("X coordinate"), "y": pInt("Y coordinate")}, "x", "y"),
		},
		{
			Name:            "mouse_click",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Click a mouse button. Optionally move to (x, y) first. button: 1=left (default), 2=middle, 3=right. Set double=true for a double click.",
			InputSchema: schema(map[string]any{
				"x": pInt("optional X to move to first"), "y": pInt("optional Y to move to first"),
				"button": pInt("1=left, 2=middle, 3=right"), "double": pBool("double click"),
			}),
		},
		{
			Name:            "type_text",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Type a string of text into the focused window (handles any character, including accents).",
			InputSchema:     schema(map[string]any{"text": pStr("text to type")}, "text"),
		},
		{
			Name:            "key_combo",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Press a key or key combination using X keysym names, e.g. 'Return', 'Escape', 'ctrl+c', 'alt+Tab', 'super+d', 'ctrl+shift+t'.",
			InputSchema:     schema(map[string]any{"keys": pStr("key or combo, e.g. ctrl+c")}, "keys"),
		},
		{
			Name:            "launch_app",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Launch a program on the desktop (runs detached, does not block). Pass the command line, e.g. 'firefox-esr', 'lxterminal', 'chromium https://example.com'. Set as_root:true for administration GUIs that need privileges (a file manager on /etc, gparted, synaptic).",
			InputSchema: schema(map[string]any{
				"command": pStr("command line to run"),
				"as_root": pBool("launch as root via sudo (default false)"),
			}, "command"),
		},
		{
			Name:        "list_windows",
			Risk:        riskRead,
			Description: "List all open windows: window id, desktop, geometry (x,y,w,h), class and title. Use the id with activate_window/close_window.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "activate_window",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Focus and raise a window by its id (from list_windows).",
			InputSchema:     schema(map[string]any{"id": pStr("window id, e.g. 0x02000007")}, "id"),
		},
		{
			Name:            "run_command",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Run a shell command inside the desktop and wait for it, returning stdout, stderr and the exit code. It runs in a terminal window on the shared screen, where the people here can watch it and stop it. Set as_root:true for passwordless sudo (edit /etc, manage services, install things). If it has not finished within timeout_ms this returns what there is so far and the command KEEPS RUNNING as a job — use job_wait on the returned job_id rather than running it again. For anything you expect to be slow, prefer job_start.",
			InputSchema: schema(map[string]any{
				"command": pStr("shell command"), "timeout_ms": pIntDef("how long to wait before handing back a job id (default 15000)", 15000),
				"as_root": pBool("run as root via sudo (default false)"),
			}, "command"),
		},
		{
			Name:        "wait",
			Risk:        riskRead,
			Description: "Sleep for the given number of milliseconds (give the UI time to react before the next action).",
			InputSchema: schema(map[string]any{"ms": pInt("milliseconds to wait")}, "ms"),
		},
		{
			Name:        "start_recording",
			Visibility:  visHidden,
			Risk:        riskWrite,
			Description: "Start recording the screen (and optionally audio) to a video file, in parallel with the live stream. container: mp4 (default, H.264+AAC), webm (VP8+Opus) or mkv. Returns the output path.",
			InputSchema: schema(map[string]any{
				"container":   pStr("mp4 | webm | mkv (default mp4)"),
				"fps":         pIntDef("frames per second (default 30)", 30),
				"bitrate":     pIntDef("video bitrate in kbps (default 4000)", 4000),
				"audio":       pBool("also record audio (default true)"),
				"destination": pStr("container (default) keeps the file on the desktop; download makes the watching browser save it when the recording stops"),
			}),
		},
		{
			Name:        "stop_recording",
			Visibility:  visHidden,
			Risk:        riskWrite,
			Description: "Stop the current recording, finalize the file cleanly and return its path and size in bytes. destination overrides the one chosen at start: download hands the finished file to the browser of whoever is watching.",
			InputSchema: schema(map[string]any{
				"destination": pStr("container | download"),
			}),
		},
		{
			Name:        "get_recording_status",
			Risk:        riskRead,
			Description: "Report whether a recording is in progress, with elapsed seconds, current size and path.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "list_recordings",
			Risk:        riskRead,
			Description: "List the recorded video files (path, size, modified time).",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "get_clipboard",
			Risk:        riskRead,
			Description: "Read the desktop clipboard (X CLIPBOARD selection) as text.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "set_clipboard",
			Visibility:  visHidden,
			Risk:        riskWrite,
			Description: "Write text to the desktop clipboard (so it can be pasted with Ctrl+V).",
			InputSchema: schema(map[string]any{"text": pStr("text to place on the clipboard")}, "text"),
		},
	}
	base = append(base, s.buildRegistryTools()...)
	base = append(base, s.buildEventTools()...)
	base = append(base, s.buildAdvancedTools()...)
	base = append(base, s.buildUITools()...)
	base = append(base, s.buildSysTools()...)
	base = append(base, s.buildRootTools()...)
	base = append(base, s.buildNextTools()...)
	base = append(base, s.roomTools()...)
	base = append(base, s.terminalTools()...)
	base = append(base, s.buildJobTools()...)
	base = append(base, s.buildSleepTools()...)
	base = append(base, s.buildActivityTools()...)
	base = append(base, s.buildSecretTools()...)
	base = append(base, s.buildRemoteTools()...)
	return append(base, s.buildBrowserTools()...)
}

// --- despacho -------------------------------------------------------------

// dispatch runs a tool and returns its MCP content plus an error flag.
func (s *Server) dispatch(ctx context.Context, name string, rawArgs json.RawMessage, policy *Policy) ([]map[string]any, bool) {
	args := map[string]any{}
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &args)
	}
	// The catalogue asking about itself. It comes first because it is the one
	// tool whose answer depends on the caller's policy rather than the desktop.
	if content, isErr, handled := s.dispatchRegistry(ctx, name, args, policy); handled {
		return content, isErr
	}
	// Subscribing to events: like tool_search, an answer about this connection
	// rather than about the desktop.
	if content, isErr, handled := s.dispatchEvents(ctx, name, args); handled {
		return content, isErr
	}
	// Sharing the desktop: these answer about the room rather than touching it.
	if out, isErr, handled := s.callTerminal(ctx, name, args); handled {
		if content, ok := out.([]map[string]any); ok {
			return content, isErr
		}
		return jsonContent(out), isErr
	}
	if out, isErr, handled := s.callRoom(ctx, name, args); handled {
		if content, ok := out.([]map[string]any); ok {
			return content, isErr
		}
		return jsonContent(out), isErr
	}

	switch name {
	case "screenshot":
		return s.toolScreenshot(args)
	case "mouse_move":
		return s.toolMouseMove(argInt(args, "x"), argInt(args, "y"))
	case "mouse_click":
		return s.toolMouseClick(args)
	case "type_text":
		return s.toolTypeText(argStr(args, "text"))
	case "key_combo":
		return s.toolKeyCombo(argStr(args, "keys"))
	case "launch_app":
		asRoot, _ := args["as_root"].(bool)
		return s.toolLaunchApp(argStr(args, "command"), asRoot)
	case "list_windows":
		return s.toolListWindows()
	case "activate_window":
		return s.toolActivateWindow(argStr(args, "id"))
	case "run_command":
		asRoot, _ := args["as_root"].(bool)
		return s.toolRunCommand(ctx, argStr(args, "command"), argInt(args, "timeout_ms"), asRoot)
	case "wait":
		return s.toolWait(ctx, argInt(args, "ms"))
	case "start_recording":
		return s.toolStartRecording(args)
	case "stop_recording":
		return s.toolStopRecording(args)
	case "get_recording_status":
		return jsonContent(s.recorder.Status()), false
	case "list_recordings":
		return s.toolListRecordings()
	case "get_clipboard":
		text, _ := s.clip.Get()
		return textContent("%s", text), false
	case "set_clipboard":
		if err := s.clip.Set(argStr(args, "text")); err != nil {
			return textContent("could not set the clipboard: %v", err), true
		}
		return textContent("clipboard set"), false
	}
	// Advanced tools: windows, processes, OCR, files, streaming
	if content, isErr, handled := s.dispatchAdvanced(ctx, name, args); handled {
		return content, isErr
	}
	// Accessibility tools: operate by structure rather than by pixels
	if content, isErr, handled := s.dispatchUI(ctx, name, args); handled {
		return content, isErr
	}
	// Browser tools over CDP, against the real DOM
	if content, isErr, handled := s.dispatchBrowser(ctx, name, args); handled {
		return content, isErr
	}
	// Names of secrets, never values.
	if content, isErr, handled := s.dispatchSecrets(name, args); handled {
		return content, isErr
	}
	// The desktop's own history — both planes, one timeline.
	if content, isErr, handled := s.dispatchActivity(name, args); handled {
		return content, isErr
	}
	// Background jobs: everything the agent runs, run where it can be watched
	// and stopped. Early, because run_command below is now one of them.
	// A deliberate wait is a job like any other — witnessed on the shared
	// screen, and taken down by the same panic button.
	if content, isErr, handled := s.dispatchSleep(ctx, name, args); handled {
		return content, isErr
	}
	if content, isErr, handled := s.dispatchJobs(ctx, name, args); handled {
		return content, isErr
	}
	// Graphical remote sessions (RDP/VNC/SPICE) onto the shared screen
	if content, isErr, handled := s.dispatchRemote(ctx, name, args); handled {
		return content, isErr
	}
	// Persistent terminal, SSH and low-level windows
	if content, isErr, handled := s.dispatchSys(ctx, name, args); handled {
		return content, isErr
	}
	// Administration: privileges, packages and services
	if content, isErr, handled := s.dispatchRoot(ctx, name, args); handled {
		return content, isErr
	}
	// Resolution, smart waits, macro-actions, diffing, snapshots, action log
	if content, isErr, handled := s.dispatchNext(ctx, name, args); handled {
		return content, isErr
	}
	return textContent("unknown tool: %s", name), true
}

func argStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
func argInt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// --- implementations (P0) ---------------------------------------------------

// toolScreenshot captures the screen. Where the picture ends up is the caller's
// choice: inline (the agent looks at it), on the desktop's disk, or downloaded
// by whoever is watching in a browser. The capture itself is the same in all
// three cases — it comes from the X framebuffer, with no compression loss.
func (s *Server) toolScreenshot(args map[string]any) ([]map[string]any, bool) {
	dest := strings.ToLower(argStr(args, "destination"))
	if dest == "" || dest == "inline" {
		b64, err := desktop.GrabScreenshotPNG(s.display)
		if err != nil {
			return textContent("screenshot failed: %v", err), true
		}
		return imageContent(b64, "image/png"), false
	}

	path := argStr(args, "path")
	if path == "" {
		path = filepath.Join(s.recorder.Dir,
			"screenshot-"+time.Now().Format("20060102-150405")+".png")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return textContent("screenshot failed: %v", err), true
	}
	if err := desktop.GrabToFile(s.display, path, 0, 0, 0, 0); err != nil {
		return textContent("screenshot failed: %v", err), true
	}

	res := map[string]any{"path": path}
	if fi, err := os.Stat(path); err == nil {
		res["size_bytes"] = fi.Size()
	}
	if dest == "download" {
		res["delivered_to"] = s.deliver(path, filepath.Base(path))
		if res["delivered_to"] == 0 {
			res["note"] = "nobody is watching in a browser: the file stayed on the desktop"
		}
	}
	return jsonContent(res), false
}

func (s *Server) toolMouseMove(x, y int) ([]map[string]any, bool) {
	s.injector.Move(x, y)
	s.reportPointer(x, y)
	return textContent("moved to (%d, %d)", x, y), false
}

func (s *Server) toolMouseClick(args map[string]any) ([]map[string]any, bool) {
	if _, ok := args["x"]; ok {
		s.injector.Move(argInt(args, "x"), argInt(args, "y"))
		s.reportPointer(argInt(args, "x"), argInt(args, "y"))
	}
	btn := argInt(args, "button")
	if btn == 0 {
		btn = 1
	}
	clicks := 1
	if b, _ := args["double"].(bool); b {
		clicks = 2
	}
	for i := 0; i < clicks; i++ {
		s.injector.Button(btn, true)
		s.injector.Button(btn, false)
	}
	return textContent("clicked button %d x%d", btn, clicks), false
}

func (s *Server) toolTypeText(text string) ([]map[string]any, bool) {
	if text == "" {
		return textContent("nothing to type"), false
	}
	if err := s.xdo("type", "--clearmodifiers", "--", text); err != nil {
		return textContent("type failed: %v", err), true
	}
	return textContent("typed %d chars", len([]rune(text))), false
}

func (s *Server) toolKeyCombo(keys string) ([]map[string]any, bool) {
	if keys == "" {
		return textContent("no keys given"), true
	}
	if err := s.xdo("key", "--clearmodifiers", keys); err != nil {
		return textContent("key failed: %v", err), true
	}
	return textContent("pressed %s", keys), false
}

func (s *Server) toolLaunchApp(command string, asRoot bool) ([]map[string]any, bool) {
	if command == "" {
		return textContent("no command given"), true
	}
	// setsid detaches the process from the daemon, so closing the MCP
	// connection does not take the application down with it.
	args := []string{"sh", "-c", command}
	if asRoot {
		if !sudoAvailable {
			return textContent("this image has no passwordless sudo"), true
		}
		// -E preserves DISPLAY. Xvfb runs with -ac, so root can open windows on
		// :0 without having to fight the xauth cookie.
		args = append([]string{"sudo", "-n", "-E"}, args...)
	}
	cmd := exec.Command("setsid", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display)
	// Capture stderr. Detached applications write their failures there and
	// nowhere else, so without this a missing library or a bad configuration
	// disappears completely and the agent is told the launch worked.
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return textContent("launch failed: %v", err), true
	}

	// Start() only reports that the fork succeeded. A program that exits a
	// moment later — no such binary inside the shell, a missing .so, no
	// display — used to be indistinguishable from one that started fine.
	// Waiting briefly turns "launched" into something worth believing.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		stderr := strings.TrimSpace(errBuf.String())
		if len(stderr) > 1200 {
			stderr = stderr[len(stderr)-1200:]
		}
		out := map[string]any{
			"command": command,
			"running": false,
			"error":   fmt.Sprintf("the program exited immediately: %v", err),
		}
		if stderr != "" {
			out["stderr"] = stderr
		} else {
			out["hint"] = "it printed nothing to stderr; check the command name " +
				"with list_installed_apps, or run it through terminal_run to see " +
				"the shell's own error"
		}
		return jsonContent(out), true

	case <-time.After(700 * time.Millisecond):
		// Still alive. Keep reaping in the background so it does not zombie.
		go func() { <-done }()

		// And follow it onto the shared desktop.
		//
		// This tool is the second door onto the screen everybody is watching,
		// and until now it was the undefended one: the terminal path forces its
		// window to where the room is looking and refuses the call if it cannot,
		// while an application launched here appeared wherever the window
		// manager felt like putting it — another desktop, shaded, behind
		// everything — and was reported as launched all the same. Same
		// invariant, same failure, and no reason for one path to keep it alone.
		//
		// In the BACKGROUND, because this tool's contract is that it returns
		// promptly and a window may take longer than that to exist; making it
		// wait would turn it into open_app_and_wait, which is a different tool
		// that already exists. So the placement is best-effort and says so,
		// rather than being a promise made in the return value and kept
		// somewhere the caller cannot see.
		pid := cmd.Process.Pid
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// Windows do not exist the instant a process does. Look repeatedly
			// rather than once, and stop at the first set that belongs to it.
			for {
				if ids := s.windowsOf(pid); len(ids) > 0 {
					s.bringToTheRoom(ctx, ids, 8*time.Second)
					return
				}
				if !sleepCtx(ctx, 300*time.Millisecond) {
					return // the context expired: it never opened one
				}
			}
		}()

		out := map[string]any{
			"command": command, "running": true, "pid": cmd.Process.Pid,
			"as_root": asRoot,
			"note": "still running after 700 ms. Any window it opens is being moved " +
				"onto the desktop the room is watching, in the background — use " +
				"open_app_and_wait when you need that CONFIRMED before carrying on.",
		}
		return jsonContent(out), false
	}
}

type windowInfo struct {
	ID      string `json:"id"`
	Desktop int    `json:"desktop"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Class   string `json:"class"`
	Title   string `json:"title"`
}

// listWindows returns the open windows, already parsed. Besides the tool itself,
// the macro-actions (open_app_and_wait) use it to spot the window that appeared.
func (s *Server) listWindows() []windowInfo {
	// wmctrl -l -G -x: id desktop x y w h wm_class host title
	//                   0   1      2 3 4 5 6        7    8+
	out, err := s.output("wmctrl", "-l", "-G", "-x")
	if err != nil {
		return nil
	}
	var wins []windowInfo
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		w := windowInfo{ID: f[0], Class: f[6]}
		w.Desktop, _ = strconv.Atoi(f[1])
		w.X, _ = strconv.Atoi(f[2])
		w.Y, _ = strconv.Atoi(f[3])
		w.W, _ = strconv.Atoi(f[4])
		w.H, _ = strconv.Atoi(f[5])
		w.Title = strings.Join(f[8:], " ")
		wins = append(wins, w)
	}
	return wins
}

func (s *Server) toolListWindows() ([]map[string]any, bool) {
	// Straight from X. The wmctrl path below is kept as a fallback for a build
	// where the display cannot be opened, on the same principle as everything
	// else optional here: degrade, do not fail.
	if e, err := s.windows(); err == nil {
		if list, err := e.Windows(); err == nil {
			return jsonContent(list), false
		}
	}
	return jsonContent(s.listWindows()), false
}

func (s *Server) toolActivateWindow(id string) ([]map[string]any, bool) {
	if id == "" {
		return textContent("no window id"), true
	}
	if e, err := s.windows(); err == nil {
		if win, perr := desktop.ParseWindowID(id); perr == nil {
			if err := e.Activate(win); err == nil {
				return textContent("activated window %s", id), false
			}
		}
	}
	if err := s.run("wmctrl", "-i", "-a", id); err != nil {
		return textContent("activate failed: %v", err), true
	}
	return textContent("activated window %s", id), false
}

// toolRunCommand runs a command where it can be seen, and waits for it.
//
// It used to be exec.Command with a deadline, and the rewrite is the point of
// tools_jobs.go rather than an implementation detail of it. What changed for the
// caller is smaller than it looks — the same three fields come back — but two
// things that were true are no longer:
//
//	The command is ON SCREEN. Somebody sharing this desktop sees a terminal open
//	and watches the output arrive. That is not a courtesy; a supervisor who
//	cannot observe an action cannot decide to stop it, and there is now a button
//	that stops it.
//
//	The timeout no longer KILLS anything. It bounds how long this call waits,
//	and the work continues as a job with an id. `curl -O` on something large was
//	previously a command that could not be expressed at all: it did not fail, it
//	was executed and then destroyed at fifteen seconds, and the report said
//	timed_out as though the machine had been slow rather than as though the tool
//	had thrown the work away.
func (s *Server) toolRunCommand(ctx context.Context, command string, timeoutMs int, asRoot bool) ([]map[string]any, bool) {
	if command == "" {
		return textContent("no command"), true
	}
	if timeoutMs <= 0 {
		timeoutMs = 15000
	}
	rec, err := s.startJob(ctx, command, asRoot)
	if err != nil {
		return textContent("%v", err), true
	}

	// Progress notifications, kept alive across the rewrite. They used to come
	// from a MultiWriter on the command's own pipe; there is no pipe now, so the
	// last line is polled off the file the command is writing. Slightly behind
	// and unable to lose anything, which is the correct trade for a heartbeat.
	tail := &tailWriter{}
	feeding := make(chan struct{})
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-feeding:
				return
			case <-ticker.C:
				if line, err := jobOutput(rec.ID, "out", 1); err == nil && line != "" {
					_, _ = tail.Write([]byte(line + "\n"))
				}
			}
		}
	}()
	stopReporting := reportWhileRunning(ctx, "running", tail)

	final, finished := s.waitForJob(ctx, rec.ID, time.Duration(timeoutMs)*time.Millisecond)
	stopReporting()
	close(feeding)

	// A cancelled call really does stop the work, and that is worth stating
	// because the timeout beside it deliberately does not.
	//
	// They are different questions wearing the same shape. A timeout means "I
	// have waited long enough" — the answer is still wanted, so killing the
	// command would throw away work somebody is going to ask for again. A
	// cancellation means "stop": the caller has withdrawn the request, and
	// leaving a download running for an answer nobody will read is how a
	// container fills its disk with results of abandoned questions.
	if ctx.Err() != nil && !finished {
		if aborted, err := s.abortJob(rec.ID, "the caller", "the tool call was cancelled"); err == nil {
			final = aborted
		}
	}

	stdout, _ := jobOutput(rec.ID, "out", 0)
	stderr, _ := jobOutput(rec.ID, "err", 0)

	out := map[string]any{
		"job_id":  rec.ID,
		"stdout":  stdout,
		"stderr":  stderr,
		"as_root": asRoot,
		// Kept for the callers and prompts that read it, but it now means "this
		// call stopped waiting", not "the command was killed" — which is why it
		// no longer travels alone. still_running says which of those happened.
		"timed_out":     !finished,
		"still_running": !finished,
	}
	if final.ExitCode != nil {
		out["exit_code"] = *final.ExitCode
	}
	if final.AbortedBy != "" {
		out["aborted_by"] = final.AbortedBy
	}
	if !finished {
		out["note"] = "still running as job " + rec.ID + " — this is the output so far, " +
			"not the end of it. Call job_wait with this job_id. Do NOT run the command " +
			"again: it was not cancelled."
	}
	// A command run here IS a job, so it carries the same link every other job
	// tool carries. Especially when it timed out: that is the case where a
	// person wants to watch the rest of it happen and the agent has nothing else
	// to hand them.
	return jsonContent(s.withLogLinks(out, rec.ID)), false
}

func (s *Server) toolWait(ctx context.Context, ms int) ([]map[string]any, bool) {
	if ms < 0 {
		ms = 0
	}
	if ms > 60000 {
		ms = 60000
	}
	if !sleepCtx(ctx, time.Duration(ms)*time.Millisecond) {
		return textContent("wait interrupted"), true
	}
	return textContent("waited %d ms", ms), false
}

// progressInterval is how often a running command reports that it is still
// going. A variable rather than a constant so tests need not wait it out.
var progressInterval = 2 * time.Second

// tailWriter keeps the most recent non-empty line written through it.
//
// A long command's own output is the only honest progress it has: apt does not
// know what fraction of the way through it is, but "Setting up python3…" tells
// somebody watching a great deal more than a spinner does. It is written to
// alongside the buffer that collects the real output, not instead of it.
type tailWriter struct {
	mu   sync.Mutex
	last string
	buf  []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(w.buf[:i])); line != "" {
			w.last = line
		}
		w.buf = w.buf[i+1:]
	}
	// A command that writes megabytes without a newline should not be able to
	// grow this without bound.
	if len(w.buf) > 4096 {
		w.buf = w.buf[len(w.buf)-4096:]
	}
	return len(p), nil
}

func (w *tailWriter) line() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// reportWhileRunning ticks out progress notifications until the returned
// function is called. It costs nothing when the client asked for no progress:
// progressOf hands back a no-op and the goroutine ticks into it.
//
// The stop function WAITS for the reporter to be gone before returning. That is
// not tidiness: without it the goroutine outlives the call that started it, and
// can still be reading state the caller believes it has finished with. The race
// detector found exactly that — a reporter from a finished command reading the
// interval while the next thing changed it.
func reportWhileRunning(ctx context.Context, what string, tail *tailWriter) func() {
	report := progressOf(ctx)
	interval := progressInterval
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(interval)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				msg := fmt.Sprintf("%s, %ds elapsed", what, int(time.Since(start).Seconds()))
				if line := tail.line(); line != "" {
					msg += ": " + line
				}
				report(msg, 0)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}

// sleepCtx waits for d, or until the call is cancelled, and reports whether the
// wait finished. False means the caller should stop and return.
//
// Every polling loop in this package used a bare time.Sleep, which is why a
// cancelled ui_wait_for or browser_wait_for went on polling for its full
// timeout: the loop had no way to hear. Use this instead of time.Sleep in
// anything that repeats.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Server) toolStartRecording(args map[string]any) ([]map[string]any, bool) {
	audio := true
	if v, ok := args["audio"].(bool); ok {
		audio = v
	}
	path, err := s.recorder.Start(media.RecordOpts{
		Container: argStr(args, "container"),
		FPS:       argInt(args, "fps"),
		Kbps:      argInt(args, "bitrate"),
		Audio:     audio,
	})
	if err != nil {
		return textContent("start_recording failed: %v", err), true
	}
	// Remember where this recording should go, because stop_recording is the
	// call that has a finished file to hand over.
	s.recDestination = strings.ToLower(argStr(args, "destination"))
	return textContent("recording to %s", path), false
}

func (s *Server) toolStopRecording(args map[string]any) ([]map[string]any, bool) {
	path, size, err := s.recorder.Stop()
	if err != nil {
		return textContent("stop_recording failed: %v", err), true
	}
	res := map[string]any{"path": path, "size_bytes": size}

	// The destination given here wins; otherwise the one chosen at start.
	dest := strings.ToLower(argStr(args, "destination"))
	if dest == "" {
		dest = s.recDestination
	}
	s.recDestination = ""
	if dest == "download" {
		res["delivered_to"] = s.deliver(path, filepath.Base(path))
		if res["delivered_to"] == 0 {
			res["note"] = "nobody is watching in a browser: the file stayed on the desktop"
		}
	}
	return jsonContent(res), false
}

func (s *Server) toolListRecordings() ([]map[string]any, bool) {
	entries, err := os.ReadDir(s.recorder.Dir)
	if err != nil {
		return textContent("list_recordings failed: %v", err), true
	}
	var recs []map[string]any
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		recs = append(recs, map[string]any{
			"path":       s.recorder.Dir + "/" + e.Name(),
			"size_bytes": fi.Size(),
			"modified":   fi.ModTime().Format(time.RFC3339),
		})
	}
	return jsonContent(recs), false
}

// --- execution helpers that carry DISPLAY ---------------------------------

func (s *Server) xdo(args ...string) error {
	return s.run("xdotool", args...)
}

func (s *Server) run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display)
	return cmd.Run()
}

func (s *Server) output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

var _ = fmt.Sprintf
