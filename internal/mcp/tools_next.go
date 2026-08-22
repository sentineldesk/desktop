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

// Second-generation tools: resolution, smart waits, macro-actions, semantic
// screen diffing, restore points and the action log.
//
// The common idea is cutting round trips. Opening an app and waiting for it used
// to be four calls and a `wait` guessed by eye; seeing what changed on screen
// used to mean sending a whole screenshot every time. Here each of those is one
// call that also returns far less data.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/sentineldesk/desktop/internal/desktop"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// homeDir returns the desktop user's home. The path is derived rather than
// hard-coded: the username changed once already, and a fixed path failed
// silently — tarring a directory that no longer existed and producing an empty
// snapshot that restored nothing.
func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Clean(h)
	}
	return "/home/sentineldesk"
}

func snapshotDirPath() string { return filepath.Join(homeDir(), ".sentineldesk-snapshots") }

func (s *Server) buildNextTools() []toolDef {
	return []toolDef{
		{
			Name:            "set_resolution",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Change the desktop resolution WITHOUT restarting anything. Use a smaller one for vision tasks (screenshots above ~1280 wide lose detail when the model rescales them) and the full size for real work. It can only shrink below the size the X server reserved at boot; get_screen_info reports that maximum.",
			InputSchema: schema(map[string]any{
				"width": pInt("width in pixels"), "height": pInt("height in pixels"),
			}, "width", "height"),
		},
		{
			Name:        "wait_for_idle",
			Risk:        riskRead,
			Description: "Wait until the desktop actually settles: the screen stops changing AND the CPU calms down. Use this instead of guessing a `wait` after launching an app, loading a page or starting an install — it returns as soon as things are quiet, and reports why it stopped.",
			InputSchema: schema(map[string]any{
				"timeout_ms": pIntDef("give up after this long (default 15000)", 15000),
				"quiet_ms":   pIntDef("how long the screen must stay still (default 1200)", 1200),
				"max_cpu":    pIntDef("consider the CPU calm below this percent (default 40)", 40),
				"ignore_cpu": pBool("only look at the screen, not the CPU (default false)"),
			}),
		},
		{
			Name:            "open_app_and_wait",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Launch a program, wait for its window to appear, focus it and wait for it to finish drawing — all in ONE call instead of launch_app + wait_for_window + activate_window + wait. Returns the window that appeared.",
			InputSchema: schema(map[string]any{
				"command":    pStr("command line, e.g. 'lxterminal' or 'chromium https://example.com'"),
				"match":      pStr("window title or class to wait for (default: derived from the command)"),
				"timeout_ms": pIntDef("give up after this long (default 25000)", 25000),
				"as_root":    pBool("launch with root privileges (default false)"),
			}, "command"),
		},
		{
			Name:            "fill_form",
			Visibility:      visInjects,
			Risk:            riskWrite,
			RequiresControl: true,
			Description: "Fill several fields of a dialog or form in one call, by the " +
				"label printed next to each one — no clicking or tabbing between them. " +
				"Optionally press a button at the end. Far more reliable than typing " +
				"blind. Works on native dialogs as well as pages: a toolkit that keeps " +
				"the caption in a separate label is followed through the accessibility " +
				"relation, so ask for what a person would read on screen.",
			InputSchema: schema(map[string]any{
				"fields": map[string]any{
					"type":                 "object",
					"description":          "field label -> value, e.g. {\"Username\":\"admin\",\"Password\":\"secret\"}",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"submit": pStr("name of the button to press afterwards, e.g. 'Sign in'"),
				"app":    pStr("optional: restrict to one application, so a common label like 'Name' is not matched in the wrong window"),
			}, "fields"),
		},
		{
			Name:        "ui_diff",
			Risk:        riskRead,
			Description: "Report ONLY what changed in the accessibility tree since the last call: widgets that appeared, vanished, or whose text or state changed. Use it instead of re-reading the whole tree (or taking a screenshot) after every action — the answer is a fraction of the size, so you can check the screen constantly instead of occasionally. The first call just records the baseline.",
			InputSchema: schema(map[string]any{
				"reset": pBool("discard the baseline and start over (default false)"),
			}),
		},
		{
			Name:        "action_log",
			Risk:        riskRead,
			Description: "Read the log of MCP calls made so far: time, tool, arguments, whether it succeeded and how long it took. While a recording is running each entry also carries its position inside the video, so the .mp4 is indexed by action and can be audited or replayed.",
			InputSchema: schema(map[string]any{
				"limit":  pIntDef("how many recent entries (default 50)", 50),
				"filter": pStr("only tools whose name contains this"),
				"clear":  pBool("empty the log after reading (default false)"),
			}),
		},
		{
			Name:        "snapshot_create",
			Visibility:  visHidden,
			Risk:        riskWrite,
			Description: "Save a restore point of the desktop: the home directory plus the list of installed packages. Take one before anything risky — installing a driver, editing /etc, running an installer — so snapshot_restore can undo it.",
			InputSchema: schema(map[string]any{
				"name": pStr("short name, e.g. 'before-driver'"),
				"note": pStr("what you were about to do"),
			}, "name"),
		},
		{
			Name:        "snapshot_list",
			Risk:        riskRead,
			Description: "List the restore points with their size, date and note.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "snapshot_restore",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Roll the home directory back to a restore point. Reports which packages were installed after the snapshot so you can remove them too. Does NOT touch files outside the home.",
			InputSchema: schema(map[string]any{
				"name": pStr("snapshot name"),
			}, "name"),
		},
		{
			Name:        "snapshot_delete",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Delete a restore point.",
			InputSchema: schema(map[string]any{"name": pStr("snapshot name")}, "name"),
		},
	}
}

func (s *Server) dispatchNext(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	case "set_resolution":
		return s.toolSetResolution(ctx, args)
	case "wait_for_idle":
		return s.toolWaitForIdle(ctx, args)
	case "open_app_and_wait":
		return s.toolOpenAppAndWait(ctx, args)
	case "fill_form":
		return s.toolFillForm(ctx, args)
	case "ui_diff":
		return s.toolUIDiff(args)
	case "action_log":
		return s.toolActionLog(args)
	case "snapshot_create":
		return s.toolSnapshotCreate(ctx, args)
	case "snapshot_list":
		return s.toolSnapshotList()
	case "snapshot_restore":
		return s.toolSnapshotRestore(ctx, args)
	case "snapshot_delete":
		return s.toolSnapshotDelete(args)
	}
	return nil, false, false
}

// --- resolution --------------------------------------------------------------

func (s *Server) toolSetResolution(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	w, h := argInt(args, "width"), argInt(args, "height")
	if w < 320 || h < 240 {
		return textContent("invalid resolution: %dx%d", w, h), true, true
	}
	mode := fmt.Sprintf("%dx%d", w, h)

	// Order matters. Shrinking the framebuffer while the output still occupies
	// the old size gives BadValue, so move the output to the new mode first —
	// adding that mode if it does not exist — and only then resize the buffer.
	steps := []string{
		fmt.Sprintf("xrandr --newmode %s 0 %d 0 0 0 %d 0 0 0 2>/dev/null", mode, w, h),
		fmt.Sprintf("xrandr --addmode screen %s 2>/dev/null", mode),
		fmt.Sprintf("xrandr --output screen --mode %s 2>/dev/null", mode),
		fmt.Sprintf("xrandr --fb %s", mode),
	}
	out, _ := s.runElevated(ctx, strings.Join(steps, "; ")+"; xrandr --query | head -2", false, 15000)

	// Check what actually happened rather than trusting the exit code: xrandr
	// complains on stderr about things it went ahead and applied anyway.
	got, _ := s.output("sh", "-c", `xdpyinfo | awk '/dimensions:/{print $2}'`)
	got = strings.TrimSpace(got)
	if got != mode {
		return jsonContent(map[string]any{
			"applied": false, "requested": mode, "current": got,
			"hint":   "it can only shrink below the size reserved at start (DISPLAY_WIDTH/DISPLAY_HEIGHT)",
			"xrandr": strings.TrimSpace(fmt.Sprint(out["stdout"])),
		}), true, true
	}
	return jsonContent(map[string]any{"applied": true, "resolution": got}), false, true
}

// --- waiting -----------------------------------------------------------------

// screenFingerprint returns a hash of the screen.
//
// Only reached when the DAMAGE extension is unavailable, because it is not
// cheap in the least: a full framebuffer grab, a PNG encode, a write to disk, a
// read back and a hash, all to answer a yes-or-no question X will answer for
// free. Sampled every 200ms it made the tool that detects quiet the busiest
// thing on the desktop while it ran.
func (s *Server) screenFingerprint() string {
	tmp := filepath.Join(os.TempDir(), "sentineldesk-idle.png")
	if err := desktop.GrabToFile(s.display, tmp, 0, 0, 0, 0); err != nil {
		return ""
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// cpuSampler reports how busy the machine is, from /proc/stat.
//
// It replaces summing `ps -eo pcpu`, which was both expensive and measuring
// something else. That column is a process's average over its whole lifetime,
// not its use now, so a daemon that worked hard at startup went on contributing
// to the total for as long as it stayed alive — on a desktop up for an hour the
// figure had almost nothing to do with the present. wait_for_idle then reported
// that number as its reason for refusing to call the desktop idle.
//
// /proc/stat counts jiffies by state since boot, so busy-ness is the change in
// non-idle over the change in total between two reads. That is a real
// measurement of an interval rather than an average of averages, and it costs
// one file read instead of three processes.
type cpuSampler struct {
	idle, total uint64
	primed      bool
}

// parseCPULine splits the aggregate "cpu" line of /proc/stat into idle and
// total jiffies. Separate from the file read so it can be tested on a machine
// that has no /proc at all, which is every machine this project's tests run on
// besides the container.
func parseCPULine(line string) (idle, total uint64, ok bool) {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	for i, v := range f[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			break
		}
		total += n
		// Fields 4 and 5 of the cpu line are idle and iowait. Waiting on disk
		// is not the CPU being busy, and counting it as such would keep a
		// desktop that is merely reading a file from ever looking settled.
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return idle, total, total > 0
}

func (c *cpuSampler) read() (idle, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return parseCPULine(line)
}

// prime takes the first sample. Without it the first percent() would compare
// against zero and report the machine's entire uptime as one busy interval.
func (c *cpuSampler) prime() {
	c.idle, c.total, c.primed = c.read()
}

// percent returns busy-ness over the interval since the previous call.
func (c *cpuSampler) percent() float64 {
	idle, total, ok := c.read()
	if !ok || !c.primed || total <= c.total {
		return 0
	}
	dTotal := total - c.total
	dIdle := idle - c.idle
	c.idle, c.total = idle, total
	if dIdle > dTotal {
		return 0
	}
	return float64(dTotal-dIdle) * 100 / float64(dTotal)
}

func (s *Server) toolWaitForIdle(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	timeout := argInt(args, "timeout_ms")
	if timeout <= 0 {
		timeout = 15000
	}
	quiet := argInt(args, "quiet_ms")
	if quiet <= 0 {
		quiet = 1200
	}
	maxCPU := argInt(args, "max_cpu")
	if maxCPU <= 0 {
		maxCPU = 40
	}
	ignoreCPU, _ := args["ignore_cpu"].(bool)

	start := time.Now()
	deadline := start.Add(time.Duration(timeout) * time.Millisecond)
	quietFor := time.Duration(quiet) * time.Millisecond
	lastCPU := 0.0

	var cpu cpuSampler
	cpu.prime()

	// The event path. X reports paint through DAMAGE, so the screen is never
	// captured, encoded or hashed — a timestamp moves when the server says
	// something was drawn, and the wait sleeps exactly as long as the answer
	// could still change.
	if d, err := s.damage(); err == nil {
		// Give the screen a real chance to settle before deciding it never
		// will. A third of the budget, because a page that was going to load
		// has done it by then and returned above — probing at the START would
		// see a page mid-load repainting hard and call it video.
		probeAfter := start.Add(time.Duration(timeout) * time.Millisecond / 3)
		probed := false

		for time.Now().Before(deadline) {
			budget := time.Until(deadline)
			if !probed && time.Now().Before(probeAfter) {
				budget = time.Until(probeAfter)
			}
			if !d.QuietFor(ctx, quietFor, budget) {
				// Still moving. Once, and only once, find out WHY before
				// spending the rest of the budget on it.
				if !probed && time.Now().Before(deadline) {
					probed = true
					if samples := sampleRepaints(ctx, d, 600*time.Millisecond); repaintingSteadily(samples, repaintInterval, quietFor) {
						return jsonContent(map[string]any{
							"idle": false, "waited_ms": time.Since(start).Milliseconds(),
							"cpu_percent": int(cpu.percent()),
							"reason": "the screen is being redrawn on a steady clock — something is " +
								"playing or animating, so it will NEVER go idle and the remaining " +
								"wait was skipped. Wait for a specific element instead " +
								"(browser_wait_for), or read the state you need directly " +
								"(browser_eval, ui_find).",
						}), false, true
					}
					continue // sporadic, so waiting may still pay off
				}
				break // the screen never settled, or the call was cancelled
			}
			lastCPU = cpu.percent()
			if ignoreCPU || lastCPU <= float64(maxCPU) {
				return jsonContent(map[string]any{
					"idle": true, "waited_ms": time.Since(start).Milliseconds(),
					"cpu_percent": int(lastCPU), "reason": "the screen went still and the CPU settled",
				}), false, true
			}
			// Still is not settled: the picture stopped but the machine is
			// working. Give it an interval and ask again — which also gives the
			// sampler a window to measure, since it reports change since the
			// previous call.
			if !sleepCtx(ctx, 250*time.Millisecond) {
				break
			}
		}
		// Take a reading before explaining the failure. Reporting whatever the
		// loop happened to leave behind meant a wait that gave up on a moving
		// screen — never reaching the CPU check at all — announced 0% and
		// blamed the CPU for it, which is both halves of the sentence wrong.
		lastCPU = cpu.percent()
		return jsonContent(map[string]any{
			"idle": false, "waited_ms": time.Since(start).Milliseconds(),
			"cpu_percent": int(lastCPU),
			// LastChange is the authority on when the picture last moved. The
			// loop's own bookkeeping is not: it only advances on the passes
			// that got as far as the CPU check.
			"reason": idleFailureReason(d.LastChange(), quietFor, lastCPU),
		}), false, true
	}

	// No DAMAGE: fall back to taking the picture, the way this always worked.
	last := ""
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		fp := s.screenFingerprint()
		if fp != last {
			last = fp
			stableSince = time.Now()
		}
		lastCPU = cpu.percent()
		screenQuiet := time.Since(stableSince) >= quietFor
		cpuQuiet := ignoreCPU || lastCPU <= float64(maxCPU)
		if screenQuiet && cpuQuiet {
			return jsonContent(map[string]any{
				"idle": true, "waited_ms": time.Since(start).Milliseconds(),
				"cpu_percent": int(lastCPU), "reason": "the screen went still and the CPU settled",
			}), false, true
		}
		if !sleepCtx(ctx, 200*time.Millisecond) {
			break
		}
	}
	return jsonContent(map[string]any{
		"idle": false, "waited_ms": time.Since(start).Milliseconds(),
		"cpu_percent": int(lastCPU),
		"reason":      idleFailureReason(stableSince, quietFor, lastCPU),
	}), false, true
}

// idleFailureReason distinguishes the two ways this tool gives up, because they
// call for opposite responses: a screen that never stopped moving means the
// caller should wait longer or look at what is animating, while a settled
// screen over a busy machine means the work is happening off-screen and the
// picture will not tell them when it is done.
func idleFailureReason(stableSince time.Time, quiet time.Duration, cpu float64) string {
	if time.Since(stableSince) >= quiet {
		return fmt.Sprintf("the screen went still but the CPU is still at %d%%", int(cpu))
	}
	return stillChangingReason
}

// stillChangingReason is what the tool says when the picture never stopped.
//
// It used to be the first clause alone, which is accurate and useless. A run
// watching a YouTube page called this twice and spent thirty of its hundred and
// twenty-four seconds being told, correctly, that the screen was still moving —
// and did nothing differently the second time, because nothing in the answer
// suggested anything to do differently. A tool that reports a fact the caller
// cannot act on has spent its budget and taught nothing.
const stillChangingReason = "timed out with the screen still changing. " +
	"If something is playing or animating, this will NEVER go idle and waiting " +
	"longer cannot help — wait for a specific element instead (browser_wait_for), " +
	"or read the state you actually need (browser_eval, ui_find)."

// sampleRepaints reads when the screen last changed, repeatedly, for a window.
//
// It asks a timestamp rather than capturing anything: the whole reason this
// tool moved to DAMAGE was to stop encoding PNGs five times a second, and a
// diagnostic that reintroduced that cost would be worse than the ambiguity it
// resolves.
// repaintInterval is how often LastChange is read during the probe. Under a
// 30fps frame, so no repaint can hide between two reads and be counted as
// stillness that was never there.
const repaintInterval = 40 * time.Millisecond

func sampleRepaints(ctx context.Context, d *desktop.DamageWatcher, window time.Duration) []time.Time {
	var out []time.Time
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		out = append(out, d.LastChange())
		if !sleepCtx(ctx, repaintInterval) {
			break
		}
	}
	return out
}

// longestStillness is the longest gap between repaints in a series of readings
// of when the screen last changed, taken every `every`.
//
// The readings are VALUES, not the moments they were taken: LastChange repeats
// while nothing is drawn, so consecutive equal values are one event seen twice
// rather than two events, and the difference between two distinct values is a
// real gap between repaints.
//
// `every` is needed for one thing the values cannot express. A screen that
// stopped drawing halfway through the window reads the same timestamp for the
// rest of it, and that stretch is stillness happening NOW — the most important
// kind, since it is the one about to satisfy the wait. Measuring it means
// counting how many readings repeated and multiplying by the interval, because
// the value itself stopped moving by definition. Without that, a screen that
// went quiet a second ago reports its last busy gap and looks like it is still
// working.
func longestStillness(samples []time.Time, every time.Duration) time.Duration {
	var longest time.Duration
	var prev time.Time
	lastChangeAt := 0
	for i, s := range samples {
		if i == 0 {
			prev = s
			continue
		}
		if s.Equal(prev) {
			continue
		}
		if gap := s.Sub(prev); gap > longest {
			longest = gap
		}
		prev = s
		lastChangeAt = i
	}
	if tail := time.Duration(len(samples)-1-lastChangeAt) * every; tail > longest {
		longest = tail
	}
	return longest
}

// repaintingSteadily reports whether these readings show a screen being redrawn
// at a rate that will never leave a quiet window of `quiet`.
//
// The distinction is between a page that is busy and a picture that is moving.
// Both keep the screen changing, and they call for opposite responses: waiting
// helps the first and can never help the second. A loading page paints in
// bursts with real gaps between them; video paints on a frame clock and its
// longest gap is one frame — at 30fps, 33ms, which no sane quiet window is
// under.
//
// The margin is deliberately wide. Half the quiet window would still be a
// screen that never settles, but calling it "steady" on that evidence risks
// telling somebody their page will never load when it was about to. Being wrong
// here costs a wait; being wrong the other way costs a wrong instruction.
func repaintingSteadily(samples []time.Time, every, quiet time.Duration) bool {
	if len(samples) < 4 {
		return false // too little to say anything
	}
	return longestStillness(samples, every) < quiet/4
}

// --- macro actions ------------------------------------------------------------

func (s *Server) toolOpenAppAndWait(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	command := strings.TrimSpace(argStr(args, "command"))
	if command == "" {
		return textContent("`command` is missing"), true, true
	}
	timeout := argInt(args, "timeout_ms")
	if timeout <= 0 {
		timeout = 25000
	}
	match := strings.ToLower(argStr(args, "match"))
	if match == "" {
		// With no explicit hint, the binary's name is usually in the WM class.
		bin := strings.Fields(command)[0]
		match = strings.ToLower(filepath.Base(bin))
	}
	asRoot, _ := args["as_root"].(bool)

	before := map[string]bool{}
	for _, w := range s.listWindows() {
		before[w.ID] = true
	}

	if content, isErr := s.toolLaunchApp(command, asRoot); isErr {
		return content, true, true
	}

	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, 300*time.Millisecond) {
			break
		}
		for _, w := range s.listWindows() {
			if before[w.ID] {
				continue // already there: not the window we opened
			}
			haystack := strings.ToLower(w.Class + " " + w.Title)
			if !strings.Contains(haystack, match) {
				continue
			}
			// Move the window to the room, not the room to the window.
			//
			// This was `wmctrl -i -a`, and activate does the opposite of what is
			// wanted on a shared screen: it switches to the DESKTOP the window
			// happens to be on. An application that opened on another desktop
			// therefore dragged everybody watching over to it. Placing the window
			// instead leaves the room where it was and brings the thing that was
			// just opened into view, which is the promise this tool makes.
			placed, why := s.bringToTheRoom(ctx, []string{w.ID}, 8*time.Second)
			// Let it finish drawing before handing control back to the caller.
			s.toolWaitForIdle(ctx, map[string]any{
				"timeout_ms": 6000, "quiet_ms": 700, "ignore_cpu": true,
			})
			out := map[string]any{
				"opened": true, "window": w,
				"waited_ms": timeout - int(time.Until(deadline).Milliseconds()),
				"on_screen": placed,
			}
			if !placed {
				// Reported, not swallowed. The window exists and the launch did
				// happen, so this is not a failed call — but "opened" without
				// "and you can see it" is the half-truth this whole area of the
				// code exists to stop being told.
				out["warning"] = "the window opened but could not be put where the room " +
					"is looking: " + why
			}
			return jsonContent(out), false, true
		}
	}
	return jsonContent(map[string]any{
		"opened": false,
		"hint": fmt.Sprintf("no new window appeared containing %q; "+
			"try an explicit `match`, or check whether the program failed to start", match),
	}), true, true
}

func (s *Server) toolFillForm(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	raw, ok := args["fields"].(map[string]any)
	if !ok || len(raw) == 0 {
		return textContent("`fields` is missing (an object of name -> value)"), true, true
	}
	// A stable order matters: otherwise each run fills the fields in a different
	// sequence, and forms that validate as you type behave differently.
	names := make([]string, 0, len(raw))
	for k := range raw {
		names = append(names, k)
	}
	sort.Strings(names)

	// Which application, when the caller says. Without it a name is matched
	// across every open window, and "Name" is not a rare caption.
	app := argStr(args, "app")
	scope := func(cmd ...string) []string {
		if app != "" {
			cmd = append(cmd, "--app", app)
		}
		return cmd
	}

	results := make([]map[string]any, 0, len(names))
	failed := 0
	for _, name := range names {
		value := fmt.Sprint(raw[name])
		out, err := s.a11yRaw(scope("settext", "--name", name, "--text", value)...)
		entry := map[string]any{"field": name}
		// The bridge answers in JSON either way, so the reply is read rather
		// than scanned for the word "error" — which used to mean a field whose
		// own label contained it reported itself as having failed.
		var res struct {
			OK    bool   `json:"ok"`
			Ref   string `json:"ref"`
			Error string `json:"error"`
			Hint  string `json:"hint"`
		}
		switch {
		case err != nil:
			entry["ok"] = false
			entry["error"] = err.Error()
			failed++
		case json.Unmarshal([]byte(out), &res) != nil:
			entry["ok"] = false
			entry["error"] = strings.TrimSpace(out)
			failed++
		case !res.OK:
			entry["ok"] = false
			entry["error"] = res.Error
			if res.Hint != "" {
				entry["hint"] = res.Hint
			}
			failed++
		default:
			entry["ok"] = true
			// The caller named a label, not an element. Saying which one was
			// written to is the difference between a report and a claim.
			entry["ref"] = res.Ref
		}
		results = append(results, entry)
	}

	res := map[string]any{"fields": results, "filled": len(names) - failed, "failed": failed}
	if submit := argStr(args, "submit"); submit != "" {
		out, err := s.a11yRaw(scope("click", "--name", submit)...)
		var clicked struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err != nil || json.Unmarshal([]byte(out), &clicked) != nil || !clicked.OK {
			res["submitted"] = false
			res["submit_error"] = strings.TrimSpace(clicked.Error + " " + out)
		} else {
			res["submitted"] = true
			s.toolWaitForIdle(ctx, map[string]any{"timeout_ms": 8000, "quiet_ms": 800})
		}
	}
	return jsonContent(res), failed > 0, true
}

// --- semantic screen diff ----------------------------------------------------

// uiNode is what gets compared between calls: the minimum that makes a widget
// recognisable and that changes when something interesting happens.
type uiNode struct {
	Role  string `json:"role"`
	Name  string `json:"name"`
	Text  string `json:"text,omitempty"`
	State string `json:"state,omitempty"`
}

// flattenUI walks whatever a11y.py returned and flattens it to ref -> node.
func flattenUI(v any, out map[string]uiNode) {
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			flattenUI(item, out)
		}
	case map[string]any:
		ref, _ := t["ref"].(string)
		if ref != "" {
			n := uiNode{}
			n.Role, _ = t["role"].(string)
			n.Name, _ = t["name"].(string)
			n.Text, _ = t["text"].(string)
			if st, ok := t["states"].([]any); ok {
				parts := make([]string, 0, len(st))
				for _, s := range st {
					parts = append(parts, fmt.Sprint(s))
				}
				sort.Strings(parts)
				n.State = strings.Join(parts, ",")
			}
			out[ref] = n
		}
		// a11y.py returns {"count":N,"elements":[…]} with the list ALREADY
		// flattened; the other keys cover nested shapes should the bridge
		// change. Missing "elements" here once made every diff come back empty.
		for _, key := range []string{"elements", "children", "apps", "windows", "nodes"} {
			if child, ok := t[key]; ok {
				flattenUI(child, out)
			}
		}
	}
}

func (s *Server) toolUIDiff(args map[string]any) ([]map[string]any, bool, bool) {
	// An explicit, high limit: a diff is only meaningful if both snapshots cover
	// the same ground. With the default limit of 200 a busy screen gets
	// truncated, and phantom changes appear every time the cut-off moves.
	out, err := s.a11yRaw("tree", "--limit", "4000", "--depth", "14")
	if err != nil {
		return textContent("ui_diff: could not read the tree: %v", err), true, true
	}
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return textContent("ui_diff: non-JSON reply from the accessibility bridge"), true, true
	}
	current := map[string]uiNode{}
	flattenUI(parsed, current)

	s.uiMu.Lock()
	previous := s.uiLast
	if reset, _ := args["reset"].(bool); reset {
		previous = nil
	}
	s.uiLast = current
	s.uiMu.Unlock()

	if previous == nil {
		return jsonContent(map[string]any{
			"baseline": true, "nodes": len(current),
			"note": "first call: the reference snapshot was stored; the next one returns only the changes",
		}), false, true
	}

	var appeared, vanished, changed []map[string]any
	for ref, now := range current {
		before, existed := previous[ref]
		if !existed {
			appeared = append(appeared, map[string]any{
				"ref": ref, "role": now.Role, "name": now.Name, "text": now.Text,
			})
			continue
		}
		delta := map[string]any{}
		if before.Name != now.Name {
			delta["name"] = []string{before.Name, now.Name}
		}
		if before.Text != now.Text {
			delta["text"] = []string{before.Text, now.Text}
		}
		if before.State != now.State {
			delta["state"] = []string{before.State, now.State}
		}
		if len(delta) > 0 {
			delta["ref"] = ref
			delta["role"] = now.Role
			changed = append(changed, delta)
		}
	}
	for ref, before := range previous {
		if _, ok := current[ref]; !ok {
			vanished = append(vanished, map[string]any{
				"ref": ref, "role": before.Role, "name": before.Name,
			})
		}
	}

	total := len(appeared) + len(vanished) + len(changed)
	return jsonContent(map[string]any{
		"changes": total, "nodes": len(current),
		"appeared": appeared, "vanished": vanished, "changed": changed,
	}), false, true
}

// --- action log --------------------------------------------------------------

func (s *Server) toolActionLog(args map[string]any) ([]map[string]any, bool, bool) {
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 50
	}
	entries := s.actions.Tail(limit, argStr(args, "filter"))
	res := map[string]any{"count": len(entries), "entries": entries}
	if rec := videoOffset(s.recorder); rec != "" {
		res["recording_at"] = rec
	}
	if clear, _ := args["clear"].(bool); clear {
		res["cleared"] = s.actions.Clear()
	}
	return jsonContent(res), false, true
}

// --- restore points ----------------------------------------------------------

// safeName keeps a snapshot name from escaping its directory.
func safeName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" {
		return "", fmt.Errorf("the name is missing")
	}
	if strings.ContainsAny(n, "/\\.\x00 '\"$`;&|<>") {
		return "", fmt.Errorf("invalid name %q: letters, digits, - and _ only", n)
	}
	return n, nil
}

func (s *Server) toolSnapshotCreate(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	name, err := safeName(argStr(args, "name"))
	if err != nil {
		return textContent("%v", err), true, true
	}
	dir := snapshotDirPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return textContent("could not create %s: %v", dir, err), true, true
	}
	tarPath := filepath.Join(dir, name+".tar.gz")
	pkgPath := filepath.Join(dir, name+".packages")

	home := homeDir()
	// Exclude the snapshot directory itself: otherwise each snapshot swallows
	// every earlier one and they grow quadratically.
	cmd := fmt.Sprintf(
		"tar czf %q --warning=no-file-changed --exclude=%q -C %q %q 2>/dev/null; "+
			"dpkg-query -W -f='${Package}\\n' > %q; true",
		tarPath, ".sentineldesk-snapshots",
		filepath.Dir(home), filepath.Base(home), pkgPath)
	if _, err := s.runElevated(ctx, cmd, false, 600000); err != nil {
		return textContent("snapshot failed: %v", err), true, true
	}
	info, err := os.Stat(tarPath)
	if err != nil {
		return textContent("the snapshot was not written: %v", err), true, true
	}
	// A tar of a real home weighs kilobytes at minimum. If it comes out absurdly
	// small then nothing was packed, and failing loudly beats storing a "restore
	// point" that turns out to restore nothing.
	if info.Size() < 512 {
		os.Remove(tarPath)
		return textContent("the snapshot came out empty (%d bytes): could not archive %s",
			info.Size(), home), true, true
	}
	if note := argStr(args, "note"); note != "" {
		os.WriteFile(filepath.Join(dir, name+".note"), []byte(note), 0o644)
	}
	return jsonContent(map[string]any{
		"created": name, "size": info.Size(), "path": tarPath,
		"note": argStr(args, "note"),
	}), false, true
}

func (s *Server) toolSnapshotList() ([]map[string]any, bool, bool) {
	dir := snapshotDirPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return jsonContent(map[string]any{"snapshots": []any{}}), false, true
	}
	var out []map[string]any
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".tar.gz")
		info, err := e.Info()
		if err != nil {
			continue
		}
		item := map[string]any{
			"name": name, "size": info.Size(),
			"created": info.ModTime().Format(time.RFC3339),
		}
		if note, err := os.ReadFile(filepath.Join(dir, name+".note")); err == nil {
			item["note"] = strings.TrimSpace(string(note))
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["created"]) > fmt.Sprint(out[j]["created"])
	})
	return jsonContent(map[string]any{"snapshots": out, "dir": dir}), false, true
}

func (s *Server) toolSnapshotRestore(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	name, err := safeName(argStr(args, "name"))
	if err != nil {
		return textContent("%v", err), true, true
	}
	dir := snapshotDirPath()
	tarPath := filepath.Join(dir, name+".tar.gz")
	if _, err := os.Stat(tarPath); err != nil {
		return textContent("no such snapshot %q", name), true, true
	}

	// Which packages arrived after the snapshot. They are not removed
	// automatically — they may well have been installed on purpose — but they
	// are reported so whoever decides knows what is extra.
	var added []string
	if before, err := os.ReadFile(filepath.Join(dir, name+".packages")); err == nil {
		had := map[string]bool{}
		for _, p := range strings.Fields(string(before)) {
			had[p] = true
		}
		if now, err := exec.Command("dpkg-query", "-W", "-f=${Package}\n").Output(); err == nil {
			for _, p := range strings.Fields(string(now)) {
				if !had[p] {
					added = append(added, p)
				}
			}
		}
	}

	// The tar is unpacked over /home; --overwrite so that modified files
	// modificados vuelvan al estado guardado.
	res, err := s.runElevated(ctx,
		fmt.Sprintf("tar xzf %q -C /home --overwrite 2>&1 | tail -5; true", tarPath),
		true, 600000)
	if err != nil {
		return textContent("restore failed: %v", err), true, true
	}
	out := map[string]any{
		"restored": name,
		"note":     "only /home/sentineldesk was restored; changes outside the home are still there",
		"log":      strings.TrimSpace(fmt.Sprint(res["stdout"])),
	}
	if len(added) > 0 {
		sort.Strings(added)
		if len(added) > 40 {
			out["packages_added_since"] = append(added[:40], fmt.Sprintf("… and %d more", len(added)-40))
		} else {
			out["packages_added_since"] = added
		}
		out["hint"] = "those packages were installed after the snapshot; use remove_packages to take them out"
	}
	return jsonContent(out), false, true
}

func (s *Server) toolSnapshotDelete(args map[string]any) ([]map[string]any, bool, bool) {
	name, err := safeName(argStr(args, "name"))
	if err != nil {
		return textContent("%v", err), true, true
	}
	found := false
	for _, ext := range []string{".tar.gz", ".packages", ".note"} {
		if os.Remove(filepath.Join(snapshotDirPath(), name+ext)) == nil {
			found = true
		}
	}
	if !found {
		return textContent("no such snapshot %q", name), true, true
	}
	return jsonContent(map[string]any{"deleted": name}), false, true
}
