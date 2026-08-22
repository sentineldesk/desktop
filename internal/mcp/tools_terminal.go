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

// Running a command in the terminal the PEOPLE are looking at, and reading what
// it printed.
//
// There were two ways for an agent to run something, and they had opposite
// weaknesses:
//
//	run_command / shell_exec  — full stdout, stderr and exit code, but invisible:
//	                            it happens off-screen, and the person sharing the
//	                            desktop has no idea it happened at all.
//	typing into a terminal    — visible, collaborative, and completely blind.
//	                            `type_text` reports "typed 23 chars", which says
//	                            the keys were delivered, not that the command
//	                            worked. An agent could watch a command fail and
//	                            report success in the same breath.
//
// The blind one is precisely the collaborative one, which is the wrong way
// round. This closes it: the command goes into the real terminal so the person
// sees it happen, and the output comes back as the exact characters the
// terminal holds, not OCR of a screenshot.
//
// Both halves go through tmux, running inside the graphical terminal, rather
// than through the X server and the accessibility tree. The person sees no
// difference — tmux writes to the same pty the emulator is drawing — but three
// failure modes disappear with the old route:
//
//	typing      xdotool needed the window focused and the keyboard layout to
//	            hold every character of the command line. tmux send-keys needs
//	            neither: it writes to the pty, so a person clicking elsewhere
//	            mid-command no longer redirects half of it into another window.
//	addressing  accessibility refs are positional paths, so a closed window did
//	            not invalidate its ref — the path started resolving to whatever
//	            moved into its place and returned somebody else's text forever.
//	            A pane id is a handle: it dies with the pane it names.
//	reading     each read spawned a fresh python3, imported the AT-SPI bindings
//	            and walked the tree — 99ms, fired four times a second for the
//	            whole length of every command. capture-pane is 3.75ms.
//
// The waiting is the part that matters. Reading right after pressing Return
// returns the command line and nothing else, so this waits for the pane to go
// back to running the shell itself and for the text to stop changing.
//
// Reading somebody ELSE'S terminal still goes through accessibility, and has
// to: a terminal a person opened from the menu is not under tmux, and refusing
// to read it would give up the case this file exists for. Typing into one is a
// different matter — see terminal_run.
//
// One more thing is load-bearing and is not about tmux at all. A terminal window
// this file opens is PLACED, not merely inspected: attachWindow is the single
// path that brings one into existence, and it pins the window to the desktop the
// room is watching, clears the states that hide it, raises it, and then refuses
// the whole call if the screen does not agree. Visibility used to be a predicate
// every new tool had to remember to consult, and a predicate can be forgotten by
// whoever adds the next one; a construction path cannot. See the long comment
// above attachWindow for what happens when the window manager will not play
// along — the short version is that the call fails and nothing runs.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sentineldesk/desktop/internal/desktop"
)

// A shell prompt at the very end of the buffer: the shell is idle and waiting.
// Covers the common endings — $ or # for sh/bash, > for continuation — followed
// by optional trailing whitespace that terminals pad the last line with.
var promptTail = regexp.MustCompile(`[$#>]\s*$`)

func (s *Server) terminalTools() []toolDef {
	return []toolDef{
		{
			Name:            "terminal_run",
			Visibility:      visInjects,
			Risk:            riskDanger,
			RequiresControl: true,
			Description: "Run a command in a terminal window ON THE DESKTOP, wait " +
				"for it to finish, and return what it printed. Use this instead of " +
				"run_command when a person is watching: they see the command and its " +
				"output exactly as if you had typed it. The text returned is the " +
				"exact characters the terminal holds, not OCR.",
			InputSchema: schema(map[string]any{
				"command":    pStr("the command line to run"),
				"timeout_ms": pIntDef("how long to wait for the prompt (default 120000)", 120000),
			}, "command"),
		},
		{
			Name:            "terminal_open",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description: "Open a terminal window on the desktop, visible to anyone " +
				"watching. Every interactive shell here reports its exit status, so " +
				"terminal_run can tell a silent failure from a success — and so can " +
				"terminal_read, even for commands a PERSON typed. Use `sudo -E su` " +
				"rather than plain `sudo su` to keep that across a root shell.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name: "check_errors",
			Risk: riskRead,
			Description: "Look for anything on the desktop that is reporting a " +
				"failure: error dialogs, alerts, and message boxes, with their text " +
				"and buttons. A graphical program does not fail with an exit code — " +
				"it puts a box on the screen — so call this after launching " +
				"something, or whenever a step did not do what you expected.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name: "terminal_read",
			Risk: riskRead,
			Description: "Read what a terminal on the desktop is showing right now, " +
				"without typing anything, plus the exit status of the last command — " +
				"including one a person ran. Use this when somebody asks you to look " +
				"at an error they hit.",
			InputSchema: schema(map[string]any{
				"lines": pIntDef("how many trailing lines to return (default 40)", 40),
			}),
		},
	}
}

// terminalRefs lists every terminal in the accessibility tree, most recently
// mapped last. The count matters as much as the refs: a ref is a positional
// path, so when a window closes the path does not break — it starts resolving
// to whatever moved into its place. Watching how many terminals exist is the
// way to notice one going away.
func (s *Server) terminalRefs() ([]string, error) {
	out, err := s.a11yRaw("find", "--role", "terminal")
	if err != nil {
		return nil, err
	}
	var found struct {
		Elements []struct {
			Ref string `json:"ref"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		return nil, fmt.Errorf("accessibility bridge returned non-JSON")
	}
	refs := make([]string, 0, len(found.Elements))
	for _, e := range found.Elements {
		refs = append(refs, e.Ref)
	}
	return refs, nil
}

func (s *Server) findTerminal() (string, error) {
	refs, err := s.terminalRefs()
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		// terminal_open, not a generic launcher. Launching an emulator by name
		// opens a window nobody has placed on the desktop the room is watching
		// and that nothing here can drive; terminal_open is the one path that
		// does both, so the advice has to name it.
		return "", fmt.Errorf("no terminal window is open — call terminal_open first")
	}
	// The last one is the most recently mapped, which is the one just opened.
	return refs[len(refs)-1], nil
}

// readTerminal returns the terminal's current contents, trailing blank lines
// stripped: terminals pad the buffer to the window height and those empty rows
// would defeat any "has the output stopped changing" comparison.
func (s *Server) readTerminal(ref string) (string, error) {
	out, err := s.a11yRaw("gettext", "--ref", ref)
	if err != nil {
		return "", err
	}
	var got struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		return "", fmt.Errorf("accessibility bridge returned non-JSON")
	}
	return strings.TrimRight(got.Text, " \t\n"), nil
}

// --- the tmux control channel -----------------------------------------------

// tmuxSession is the one session every terminal opened here attaches to. A
// named session on the default socket rather than a private one (-L): a person
// who wants to join from their own shell types `tmux attach -t sentineldesk`
// and is in, which is the same collaborative property the graphical window has.
const tmuxSession = "sentineldesk"

// shellCommands are the process names that mean the pane is idle — the shell is
// the foreground process, so whatever was running has finished. This is the
// signal the old code approximated by matching a prompt against a regex, and it
// is strictly better: a command that prints something ending in `$` no longer
// reads as a returned prompt, and a prompt that does not end in one of the
// expected characters no longer reads as a command still running.
//
// It is still paired with a settled-text check below rather than trusted alone,
// because a person who types `bash` gets a pane whose foreground process is a
// shell without the previous command having finished in any meaningful sense.
var shellCommands = map[string]bool{
	"bash": true, "sh": true, "dash": true, "zsh": true, "fish": true,
}

func (s *Server) tmux(args ...string) (string, error) {
	out, err := s.output("tmux", args...)
	return strings.TrimRight(out, "\n"), err
}

// tmuxWindow is one window of the session, in the shape both the pane picker
// and the job reaper need.
type tmuxWindow struct {
	ID     string // @3 — the handle kill-window takes
	Name   string // job-j7, bash, ...
	Pane   string // %5 — this window's active pane
	Active bool   // the window the session is currently displaying
	Dead   bool   // the pane's process has exited and remain-on-exit kept it
}

// sessionWindows lists the session's windows in index order, oldest first.
//
// One call rather than a display-message per question: every caller below wants
// two or three fields of the same rows, and tmux will format them all in one
// round trip.
func (s *Server) sessionWindows() ([]tmuxWindow, error) {
	out, err := s.tmux("list-windows", "-t", tmuxSession, "-F",
		"#{window_id}\t#{window_name}\t#{pane_id}\t#{window_active}\t#{pane_dead}")
	if err != nil {
		return nil, fmt.Errorf("no terminal is open under tmux")
	}
	var wins []tmuxWindow
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) != 5 || parts[0] == "" {
			continue
		}
		wins = append(wins, tmuxWindow{
			ID: parts[0], Name: parts[1], Pane: parts[2],
			Active: parts[3] == "1", Dead: parts[4] == "1",
		})
	}
	return wins, nil
}

// pickTerminalWindow chooses the window the agent should be reading and typing
// into: the terminal, never a job.
//
// It used to be `display-message -p -t <session> '#{pane_id}'`, which is "the
// active pane of the active window" — and job_start made a job window the active
// one, so every terminal_run after a job_start typed into the JOB's pane. That
// pane's stdin belongs to job-run.sh, which is not a shell and is not reading
// it, so send-keys returned success and the command was never run by anything.
// Exactly the silent failure this file was written to close, arriving through
// the tools added beside it.
//
// So the choice is made by NAME, not by focus. A window called job-* is the
// agent's own background work and is never a terminal to type into, however
// recently it was created and whoever is looking at it. Among the rest, the
// active one wins — somebody who splits or opens a second window is working in
// the one they switched to — and a live pane beats a dead one, because a dead
// pane accepts keystrokes and does nothing with them.
func pickTerminalWindow(wins []tmuxWindow) (tmuxWindow, error) {
	var candidates []tmuxWindow
	jobsOnly := 0
	for _, w := range wins {
		if strings.HasPrefix(w.Name, jobWindowPrefix) {
			jobsOnly++
			continue
		}
		candidates = append(candidates, w)
	}
	if len(candidates) == 0 {
		if jobsOnly > 0 {
			return tmuxWindow{}, fmt.Errorf("the only windows open are %d background "+
				"job window(s), which are not shells to type into — call terminal_open "+
				"for a terminal, or job_output to read what a job printed", jobsOnly)
		}
		return tmuxWindow{}, fmt.Errorf("no terminal is open under tmux")
	}

	// Active and alive, then any alive one, then the active dead one, then the
	// first. The last two are handed back rather than refused: terminal_read on
	// a dead pane is worth doing — the output of whatever killed it is still
	// sitting there — and only terminal_run has to care.
	for _, w := range candidates {
		if w.Active && !w.Dead {
			return w, nil
		}
	}
	for _, w := range candidates {
		if !w.Dead {
			return w, nil
		}
	}
	for _, w := range candidates {
		if w.Active {
			return w, nil
		}
	}
	return candidates[0], nil
}

// terminalWindow resolves the session's terminal window.
func (s *Server) terminalWindow() (tmuxWindow, error) {
	wins, err := s.sessionWindows()
	if err != nil {
		return tmuxWindow{}, err
	}
	return pickTerminalWindow(wins)
}

// activePane is the pane an agent should read and type into. Deliberately not
// "the most recently created" and no longer "whatever tmux calls active" — see
// pickTerminalWindow for why the second one had to go.
func (s *Server) activePane() (string, error) {
	win, err := s.terminalWindow()
	if err != nil {
		return "", err
	}
	if win.Pane == "" {
		return "", fmt.Errorf("no terminal is open under tmux")
	}
	return win.Pane, nil
}

// capturePane returns what the pane holds. scrollback asks for that many lines
// of history above the visible area; 0 is the visible area alone.
//
// Trailing newlines go, for the same reason the accessibility reader stripped
// them: the pane is padded to its full height and those empty rows would defeat
// every "has the output stopped changing" comparison below.
func (s *Server) capturePane(pane string, scrollback int) (string, error) {
	args := []string{"capture-pane", "-p", "-t", pane}
	if scrollback > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(scrollback))
	}
	out, err := s.tmux(args...)
	if err != nil {
		return "", fmt.Errorf("could not read the terminal: %v", err)
	}
	return strings.TrimRight(out, " \t\n"), nil
}

// paneState reads the two things every wait below turns on: whether the pane is
// still alive, and what its foreground process is.
//
// Both in one format string because they have to be read together. tmux keeps a
// pane after its process exits when remain-on-exit is set — job panes always
// are, so a person who looked away can still read what happened — and for such a
// pane `#{pane_current_command}` keeps reporting the command it was RUNNING when
// it died. For a shell that is `bash`, which is indistinguishable from a healthy
// idle shell by that field alone. Reading `#{pane_dead}` in the same breath is
// what makes the two tellable apart.
func (s *Server) paneState(pane string) (dead bool, command string, err error) {
	out, err := s.tmux("display-message", "-p", "-t", pane,
		"#{pane_dead}\t#{pane_current_command}")
	if err != nil {
		return false, "", err
	}
	deadFlag, cmd, _ := strings.Cut(strings.TrimSpace(out), "\t")
	return deadFlag == "1", strings.TrimSpace(cmd), nil
}

// There is deliberately no paneIdle helper any more. It answered "is the
// foreground process a shell" and every caller read that as "is this pane ready
// for another command", which are the same sentence for a live pane and
// opposites for a dead one: tmux keeps reporting the shell that died. Callers
// take both fields from paneState and decide with the pair, so the question can
// no longer be asked in a form that cannot express "the pane is gone".

// --- is anybody actually looking? --------------------------------------------

// screenState is the answer to "can a person sharing this desktop see the
// terminal", and it has three values rather than two on purpose.
//
// A boolean forces the one mistake this must never make. When the check itself
// cannot run, `false` sends the caller off to open another window and `true`
// claims a promise nobody verified — and of the two, the second is the one that
// gets somebody's `apt-get install` running for five minutes behind a wall.
// Unknown is a third answer, and every caller below treats it as a refusal with
// a reason, never as a yes.
type screenState int

const (
	// screenUnknown: X could not be asked, or answered something no judgement
	// can be made from. Callers refuse and say so.
	screenUnknown screenState = iota
	// screenShowing: a window belonging to the session is mapped, on the
	// desktop being displayed, inside the screen and not buried.
	screenShowing
	// screenHidden: X answered and the answer is no.
	screenHidden
)

// minOnScreenArea is how much of a window has to be inside the screen before it
// counts as something a person can read.
//
// A fraction rather than a pixel count because the window's own size is the
// thing it should be measured against: a quarter of a maximised terminal is a
// readable amount of text, a quarter of a small one is still a legible pane, and
// a two-pixel sliver of either is a window somebody has dragged away. There is
// no principled value here — the principle is that a strictly-inside test would
// fail every terminal a person nudged past an edge, and no test at all was what
// let one be parked off-screen entirely.
const minOnScreenArea = 0.25

// terminalClasses are the WM_CLASS values that mean "a terminal emulator", used
// ONLY in the degraded path where no window on the desktop publishes a pid. It
// is a guess and is labelled as one wherever it decides anything.
var terminalClasses = map[string]bool{
	"lxterminal": true, "xterm": true, "uxterm": true, "konsole": true,
	"alacritty": true, "kitty": true, "terminator": true, "sakura": true,
	"gnome-terminal-server": true, "xfce4-terminal": true, "st": true,
}

func looksLikeTerminal(class string) bool {
	c := strings.ToLower(strings.TrimSpace(class))
	return terminalClasses[c] || strings.Contains(c, "term")
}

// sessionClientPIDs returns the pid of every process attached to the session.
//
// This is still the tmux question, and it is still worth asking — it is just no
// longer the ANSWER. Zero clients means nothing on earth is displaying the
// session, which is a definite no reached without touching X. One or more
// clients means something has a pty open, and the pid is the thread that leads
// back to a window: the client is `tmux attach`, its parent (or grandparent) is
// the emulator, and the emulator is what X knows about through _NET_WM_PID.
func (s *Server) sessionClientPIDs() []int {
	out, err := s.tmux("list-clients", "-t", tmuxSession, "-F", "#{client_pid}")
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		if n, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && n > 0 {
			pids = append(pids, n)
		}
	}
	return pids
}

// maxAncestorHops bounds the walk up the process tree. lxterminal → tmux attach
// is one hop and a login shell in between makes two; anything past a handful is
// a /proc that is lying or a cycle, and neither is worth looping on.
const maxAncestorHops = 8

// processOwners expands client pids into the set of pids that might own a
// window: each client and its ancestors.
//
// The emulator is the process X knows, and the tmux client is its child, so the
// correlation only works upwards. It is also what makes the worst case visible:
// a `tmux attach` run through `docker exec` has containerd for a parent and no
// ancestor anywhere with a window, so no window matches and the session is
// correctly reported as unwatched. That case is DOCUMENTED as a feature at the
// top of this file — a person can join the session from their own shell — and it
// used to satisfy the desktop's only proof of visibility for as long as the
// shell stayed open.
func processOwners(pids []int) map[int]bool {
	owners := map[int]bool{}
	for _, pid := range pids {
		for hop := 0; pid > 1 && hop < maxAncestorHops; hop++ {
			if owners[pid] {
				break // already walked this branch, or a cycle
			}
			owners[pid] = true
			parent := parentPID(pid)
			if parent <= 0 {
				break
			}
			pid = parent
		}
	}
	return owners
}

// parentPID reads a process's parent from /proc. Returns 0 where there is no
// /proc — a development host — which makes the owner set the clients alone and
// the visibility check fall back to its degraded path rather than crashing.
func parentPID(pid int) int {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	return parsePPID(string(raw))
}

// parsePPID pulls field 4 out of a /proc/<pid>/stat line.
//
// Split on whitespace and it is wrong, because field 2 is the executable name in
// parentheses and an executable may be called `my program (old)`. The name is
// the only field that can contain spaces or parentheses, so everything after its
// closing paren — the LAST one on the line — is safe to split.
func parsePPID(stat string) int {
	end := strings.LastIndex(stat, ")")
	if end < 0 || end+1 >= len(stat) {
		return 0
	}
	fields := strings.Fields(stat[end+1:])
	// fields[0] is the state character, fields[1] is ppid.
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}

// sessionOnScreen asks X whether the session is being displayed, and returns the
// reason alongside the verdict so every refusal below can say which of the
// half-dozen ways it failed.
func (s *Server) sessionOnScreen() (screenState, string) {
	clients := s.sessionClientPIDs()
	if len(clients) == 0 {
		// No X needed: nothing has the session open, so nothing is drawing it.
		return screenHidden, "no terminal is attached to the tmux session"
	}
	e, err := s.windows()
	if err != nil {
		return screenUnknown, fmt.Sprintf("X could not be reached to check: %v", err)
	}
	scr, err := e.ScreenState()
	if err != nil {
		return screenUnknown, fmt.Sprintf("X could not say what is on screen: %v", err)
	}
	return decideVisibility(scr, processOwners(clients))
}

// There is deliberately no sessionVisible() bool wrapper. Every caller has to
// handle three states, and the one that would be tempted to collapse them —
// "just tell me yes or no" — is the caller that then cannot say why it refused.
// The reason string is the whole difference between "no terminal window is open"
// and "the terminal is on desktop 2, and desktop 0 is the one being shown".

// publishesPIDs reports whether the correlation the visibility check rests on
// is available at all: at least one managed window naming the process that owns
// it, through _NET_WM_PID.
//
// Asked of the whole screen rather than of one window on purpose. A single
// window without a pid on a desktop where everything else has one is not
// evidence of anything — it is somebody else's window that happens to be shy —
// and sweeping it into the class heuristic would let any xterm on the desktop
// stand in for the session's own terminal.
func publishesPIDs(scr desktop.Screen) bool {
	for _, w := range scr.Windows {
		if w.PID > 0 {
			return true
		}
	}
	return false
}

// ownedBySession reports whether one window can be attributed to the tmux
// session, either for certain (pid) or as the labelled guess (class).
//
// One definition, used by both the judgement below and the placement check
// beside it. They were written a week apart and would otherwise have drifted:
// the second one only has to answer "did this call put that window there", and
// the cheap way to answer it is a slightly different rule, which is how two
// tools end up disagreeing about which window they are talking about.
func ownedBySession(w desktop.OnScreen, owners map[int]bool, byPID bool) bool {
	if byPID {
		return w.PID != 0 && owners[w.PID]
	}
	return looksLikeTerminal(w.Class)
}

// decideVisibility is the whole judgement, as a function of facts and nothing
// else — no X connection, no tmux, no clock. That is what makes it testable, and
// it needed to be: the predicate it replaces was the single visibility check in
// this codebase, was wrong in five separate ways, and had no test at all.
//
// scr.Windows is bottom-to-top, so anything after a candidate in the slice is
// drawn over it.
func decideVisibility(scr desktop.Screen, owners map[int]bool) (screenState, string) {
	if len(scr.Windows) == 0 {
		return screenHidden, "the session has a client attached but there is no " +
			"window on this desktop at all, so it is attached from somewhere else"
	}

	// Does anything here publish a pid? If nothing does, the correlation this
	// rests on is unavailable and the class heuristic is all that is left. That
	// is a weaker claim and it is reported as one.
	byPID := publishesPIDs(scr)

	matched := 0
	firstReason := ""
	for i, w := range scr.Windows {
		if !ownedBySession(w, owners, byPID) {
			continue
		}
		matched++
		ok, reason := windowReadable(w, scr, scr.Windows[i+1:])
		if ok {
			if byPID {
				return screenShowing, "a window of this session is on screen"
			}
			return screenShowing, "a terminal window is on screen — matched by " +
				"class, because no window on this desktop publishes _NET_WM_PID, so " +
				"it could not be tied to this session for certain"
		}
		if firstReason == "" {
			firstReason = reason
		}
	}

	if matched == 0 {
		if byPID {
			return screenHidden, "the session is attached, but no window on this " +
				"desktop belongs to it — something joined from outside the desktop, " +
				"such as `tmux attach` over docker exec"
		}
		return screenHidden, "no terminal window is on this desktop"
	}
	return screenHidden, "the session's window " + firstReason
}

// windowReadable decides whether one window is somewhere a person could read it.
// `above` is everything drawn over it, nearest last.
func windowReadable(w desktop.OnScreen, scr desktop.Screen, above []desktop.OnScreen) (bool, string) {
	if !w.Mapped {
		return false, "is not mapped — it is minimised, or on a desktop the window manager has put away"
	}
	if w.Hidden {
		return false, "is minimised"
	}
	if w.Shaded {
		return false, "is rolled up into its title bar, showing no output at all"
	}
	// Desktop -1 means the window is on all of them, and scr.Desktop -1 means
	// the manager does not publish one. Neither is a mismatch.
	if w.Desktop >= 0 && scr.Desktop >= 0 && w.Desktop != scr.Desktop {
		return false, fmt.Sprintf("is on desktop %d, and desktop %d is the one being shown",
			w.Desktop, scr.Desktop)
	}

	visible := w
	if scr.Width > 0 && scr.Height > 0 {
		iw, ih := overlap(w.X, w.W, 0, scr.Width), overlap(w.Y, w.H, 0, scr.Height)
		area, whole := iw*ih, w.W*w.H
		if whole <= 0 || float64(area) < minOnScreenArea*float64(whole) {
			return false, "has been moved almost entirely off the screen"
		}
		// Judge occlusion against the part that is on screen; the part outside
		// is not visible whatever is or is not drawn over it.
		visible.X, visible.W = max(w.X, 0), iw
		visible.Y, visible.H = max(w.Y, 0), ih
	}

	for _, other := range above {
		if !other.Mapped || other.Hidden || other.Shaded || other.ID == w.ID {
			continue
		}
		if other.Desktop >= 0 && scr.Desktop >= 0 && other.Desktop != scr.Desktop {
			continue
		}
		// Only a window that covers it COMPLETELY counts. Partial overlap is
		// the ordinary state of a desktop with more than one window on it, and
		// treating it as hidden would refuse to run anything on a busy screen.
		if other.X <= visible.X && other.Y <= visible.Y &&
			other.X+other.W >= visible.X+visible.W &&
			other.Y+other.H >= visible.Y+visible.H {
			return false, "is completely covered by another window"
		}
	}
	return true, ""
}

// overlap returns how much of [pos, pos+size) falls inside [lo, hi).
func overlap(pos, size, lo, hi int) int {
	start, end := max(pos, lo), min(pos+size, hi)
	if end <= start {
		return 0
	}
	return end - start
}

// --- putting the window where the room is looking -------------------------------
//
// Everything above answers "can a person see this". Everything below MAKES the
// answer yes for the one window this code is responsible for, and that ordering
// is the point.
//
// A check is a sign, and a sign only works on whoever reads it. decideVisibility
// caught five of the six ordinary ways a terminal ends up unwatched, and the
// sixth — occlusion — is the one it structurally cannot be sure of: it is not a
// property of the window but a relation to every other window, it needs a
// stacking order the manager may not publish (ScreenState then falls back to the
// unordered _NET_CLIENT_LIST and, by its own comment, errs toward "visible"), it
// only counts a SINGLE window that covers the terminal completely, and it never
// sees the override-redirect windows that are not in the client list at all.
// Nothing in a predicate fixes that, because the predicate is not the problem:
// asking was.
//
// So the window this code opens is PLACED. It is pinned to the desktop the room
// is watching, its hidden and shaded states are cleared, and it is raised —
// which is what makes the case the check could not decide impossible rather than
// undetected. Only then is decideVisibility asked, now as the confirmation that
// the placing worked rather than as the only defence.
//
// The distinction that keeps this from being rude: this places windows it
// CREATED and never windows it found. Moving somebody's own terminal to another
// desktop, un-shading it and stealing its focus is a decision about their screen
// that an agent does not get to make to satisfy its own promise.

// placement is what the screen says about the windows a single spawn brought
// into existence. Three values for the same reason screenState has three: the
// caller has to be able to tell "not yet" from "no", and a boolean would make
// a terminal that has not finished starting up indistinguishable from one the
// window manager refused to move.
type placement int

const (
	// placeWaiting: nothing new belonging to this session is on the desktop yet.
	// An emulator takes a moment to map its window; this is that moment.
	placeWaiting placement = iota
	// placeUnplaced: a window exists and is NOT mapped on the shared desktop.
	// Either the request has not been honoured yet or it will not be.
	placeUnplaced
	// placeShared: mapped, and on the desktop everybody is looking at.
	placeShared
)

// windowIDs is the set of windows already on the desktop, used to tell a window
// this call created from one that was already there.
//
// By X id rather than by pid: the emulator here is single-instance, so a second
// `lxterminal` hands its arguments to the process that is already running and
// exits — the new window belongs to a pid that predates this call, and matching
// on the process would attribute it to nobody. An X window id is minted when the
// window is, which is exactly the question being asked.
func windowIDs(scr desktop.Screen) map[string]bool {
	ids := make(map[string]bool, len(scr.Windows))
	for _, w := range scr.Windows {
		ids[w.ID] = true
	}
	return ids
}

// createdWindows returns the windows that appeared since `before` and belong to
// the session — the windows this call is responsible for, and the only ones it
// is entitled to move.
func createdWindows(scr desktop.Screen, before map[string]bool, owners map[int]bool) []desktop.OnScreen {
	byPID := publishesPIDs(scr)
	var out []desktop.OnScreen
	for _, w := range scr.Windows {
		if before[w.ID] || !ownedBySession(w, owners, byPID) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// placementOf is the requirement the forcing has to meet: mapped, and on the
// desktop the room is watching. Nothing else — the rest of "can it be read" is
// decideVisibility's job, and keeping the two apart is what lets a refusal say
// which of them failed.
//
// shared < 0 means the manager publishes no current desktop. That is missing
// information rather than a mismatch, exactly as in windowReadable: treating it
// as one would refuse to open a terminal on every desktop whose manager does not
// implement virtual desktops at all.
func placementOf(created []desktop.OnScreen, shared int) (placement, string) {
	if len(created) == 0 {
		return placeWaiting, "no window belonging to this session has appeared yet"
	}
	reason := ""
	for _, w := range created {
		// Desktop -1 is "on all of them", which satisfies any shared desktop.
		if w.Desktop >= 0 && shared >= 0 && w.Desktop != shared {
			if reason == "" {
				reason = fmt.Sprintf("the terminal opened on desktop %d and the room "+
					"is watching desktop %d", w.Desktop, shared)
			}
			continue
		}
		if !w.Mapped {
			if reason == "" {
				reason = "the terminal is not mapped — the window manager has it put away"
			}
			continue
		}
		return placeShared, ""
	}
	return placeUnplaced, reason
}

// forceAttempts bounds how many times one window is asked to move.
//
// Five is well past enough for a manager that is going to honour the request —
// openbox acts on the next event loop turn — and stopping there matters for the
// case where it will not: hammering _NET_ACTIVE_WINDOW ten times a second for
// twenty seconds would fight a person who is deliberately dragging the window
// somewhere, and would leave the desktop flickering while it lost. Past the cap
// the loop keeps looking and the call ends in a refusal that names the desktop
// the window is stuck on.
const forceAttempts = 5

// attachWindow opens a graphical terminal onto the existing session AND puts it
// where the room is looking.
//
// This is the single construction path: terminal_open, terminal_run's repair and
// job_start all arrive here, so there is one definition of what "a terminal is on
// screen" means, one place that forces it to be true, and one place that waits
// for the confirmation. A tool added next year that needs a terminal gets the
// placing for free, which a check in each caller could never promise.
//
// It refuses BEFORE spawning anything when the state is unknown, or when X
// cannot be read at all. Opening a window and then polling for a confirmation
// that cannot arrive would spend twenty seconds and leave an extra terminal on
// the desktop for a person to close, and would end in the same refusal.
//
// # When X or the window manager will not cooperate
//
// Four ways, and none of them ends in a claim that a window is visible:
//
//	no X, or no EWMH manager   ScreenState errors, sessionOnScreen answers
//	                           unknown, and this refuses before spawning.
//	                           terminal_open, terminal_run and job_start are all
//	                           disabled, which is CLAUDE.md's degradation rule
//	                           applied honestly: the feature goes, the promise
//	                           does not get downgraded to a guess.
//	no current desktop published  the pin is skipped (there is no number to pin
//	                           to) and only the clearing and the raise are sent.
//	                           placementOf then requires "mapped" alone, which is
//	                           everything such a manager can be held to.
//	the manager ignores the requests  a client message is delivered, not
//	                           acknowledged, so this cannot be detected at send
//	                           time. The confirmation below simply never sees the
//	                           window on the shared desktop and the call fails
//	                           after 20s naming where it is stuck. Nothing runs.
//	X refuses the send         the error is carried into the reason string and
//	                           the call still ends in a refusal.
func (s *Server) attachWindow(ctx context.Context) error {
	if state, why := s.sessionOnScreen(); state == screenUnknown {
		return fmt.Errorf("cannot confirm anything is on screen (%s), and this "+
			"tool's only promise is that somebody can watch", why)
	}

	// The screen as it was before anything opened. Two facts come out of it and
	// both have to be read BEFORE the spawn: which windows were already there,
	// and which desktop the room is watching.
	e, err := s.windows()
	if err != nil {
		return fmt.Errorf("X could not be reached to place a terminal (%v), so none "+
			"was opened — a window that cannot be placed cannot be promised", err)
	}
	before, err := e.ScreenState()
	if err != nil {
		return fmt.Errorf("X could not say what is on screen (%v), so no terminal "+
			"was opened", err)
	}
	existing := windowIDs(before)

	cmd := exec.Command("setsid", "lxterminal", "-e",
		"tmux", "attach", "-t", tmuxSession)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()

	// 200ms rather than the 100ms this polled at before. Each round is now a
	// tmux call plus a pass over every managed window's X properties, which is
	// worth paying for once and not worth paying for ten times a second while a
	// terminal emulator starts up.
	deadline := time.Now().Add(20 * time.Second)
	last := "no window belonging to this session appeared"
	tries := map[string]int{}
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, 200*time.Millisecond) {
			return fmt.Errorf("cancelled")
		}
		scr, err := e.ScreenState()
		if err != nil {
			last = fmt.Sprintf("X stopped answering: %v", err)
			continue
		}
		owners := processOwners(s.sessionClientPIDs())
		created := createdWindows(scr, existing, owners)

		// The shared desktop is whichever one is being displayed, read fresh
		// every round rather than snapshotted before the spawn. That is what the
		// room is streaming — the pipeline captures the root window, so
		// _NET_CURRENT_DESKTOP is what every viewer sees — and it is a live fact:
		// a person who switches desktops while the emulator starts up has moved
		// where everybody is looking, and the window follows them. There is
		// deliberately no configuration knob for it, because a fixed desktop
		// number would be wrong the moment anybody switched, which is the exact
		// complaint this answers.
		shared := scr.Desktop

		if len(created) == 0 {
			// Nothing new yet. One case is worth catching here rather than
			// waiting it out: somebody restored or switched back to the window
			// that was already there while this was starting up, which keeps the
			// promise by a route this call did not take. Accepting it is honest —
			// the session IS on screen — and refusing would be a refusal for
			// having been helped.
			if state, _ := decideVisibility(scr, owners); state == screenShowing {
				return nil
			}
			last = "no window belonging to this session appeared"
			continue
		}

		// Force, then look. In that order, and every round until it is right:
		// these are requests, not calls, so the answer arrives as a change on the
		// screen one pass later rather than as a return value here.
		for _, w := range created {
			if tries[w.ID] >= forceAttempts {
				continue
			}
			tries[w.ID]++
			win, perr := desktop.ParseWindowID(w.ID)
			if perr != nil {
				continue
			}
			if ferr := e.ShowOnDesktop(win, shared); ferr != nil {
				last = fmt.Sprintf("X would not carry the request to place the window: %v", ferr)
			}
		}

		if state, why := placementOf(created, shared); state != placeShared {
			last = why
			continue
		}
		// Placed. NOW the check earns its keep: it is the confirmation that
		// putting the window there was enough, and it still catches the ways a
		// placed window can be unreadable anyway — dragged off the screen,
		// rolled up, buried under something the raise did not clear.
		if state, why := decideVisibility(scr, owners); state != screenShowing {
			last = why
			continue
		}
		return nil
	}
	return fmt.Errorf("a terminal was opened but could not be put where the room "+
		"is looking within 20s (%s), so nothing was run", last)
}

// sessionAlive reports whether the tmux session still exists. It is how a
// command that ended the shell is told apart from one that is merely slow:
// `exit`, `logout` and anything else that closes the last pane takes the
// session with it, and there is then no prompt coming to wait for.
func (s *Server) sessionAlive() bool {
	_, err := s.tmux("has-session", "-t", tmuxSession)
	return err == nil
}

func lastLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// errorish matches the wording dialogs use when something went wrong. Roles
// alone are not enough: many toolkits report a plain "dialog" for an error and
// an "alert" for a harmless confirmation.
var errorish = regexp.MustCompile(`(?i)\b(error|failed|failure|cannot|could not|unable|denied|invalid|not found|no such|warning|problem)\b`)

// rcPath is where the instrumented shell leaves the last exit status.
//
// Out of band on purpose. Appending `; echo $?` to each command would work on
// any shell, but the person sharing the screen would watch the agent type
// bookkeeping after everything — the collaboration is worth more than the
// convenience. PROMPT_COMMAND writes it invisibly instead.
const rcPath = "/tmp/sentineldesk-rc"

// rcPathFor is where a particular pane's shell leaves its status. Panes get a
// file each because the record is last-writer-wins: with one shared file, a
// person running something in a split pane would overwrite the status of the
// command the agent just ran, and the agent would report their exit code as its
// own. That is the mute-failure class this file exists to close, so it does not
// get to come back through the side door.
//
// The unsuffixed path stays as the fallback, and is what a shell outside tmux
// still writes — a terminal opened from the panel menu keeps reporting.
func rcPathFor(pane string) string {
	if pane == "" {
		return rcPath
	}
	return rcPath + "." + strings.TrimPrefix(pane, "%")
}

// readExitCode returns the status the shell recorded, the command it belonged
// to, and whether the record is fresh enough to trust.
//
// The hook is installed for every interactive shell on the desktop, so this
// works for what a PERSON typed just as well as for what the agent typed. That
// symmetry is the whole point: somebody hits an error, asks the agent to sort it
// out, and the agent reads what actually happened rather than a retelling.
func (s *Server) readExitCode(pane string, since time.Time) (int, string, bool) {
	path := rcPathFor(pane)
	fi, err := os.Stat(path)
	if err != nil && pane != "" {
		// A shell that predates the per-pane hook, or one started outside tmux
		// and later attached. Its status is in the shared file and is still
		// worth reading; it is only ambiguous when several panes are busy.
		path = rcPath
		fi, err = os.Stat(path)
	}
	if err != nil || fi.ModTime().Before(since) {
		return 0, "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, "", false
	}
	parts := strings.SplitN(strings.TrimRight(string(raw), "\n"), "\t", 2)
	code, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, "", false
	}
	cmd := ""
	if len(parts) > 1 {
		cmd = strings.TrimSpace(parts[1])
	}
	return code, cmd, true
}

func (s *Server) callTerminal(ctx context.Context, name string, args map[string]any) (any, bool, bool) {
	switch name {
	case "terminal_open":
		// Every stale status file, not just the shared one: a pane id is reused
		// once its predecessor is gone, so a leftover file would hand the next
		// pane the exit code of a command that ran before this terminal existed.
		if stale, err := filepath.Glob(rcPath + "*"); err == nil {
			for _, f := range stale {
				_ = os.Remove(f)
			}
		}

		// Already showing? This asks X, not tmux. An attached client proves a
		// pty is open, which is not the same as a window being on the screen —
		// see sessionOnScreen. Reporting the existing one rather than stacking a
		// second window matches browser_open, and a second client would only
		// mirror the first anyway: both would be forced to the smaller of the
		// two window sizes.
		if s.sessionAlive() {
			if state, _ := s.sessionOnScreen(); state == screenShowing {
				pane, _ := s.activePane()
				return map[string]any{
					"opened": true, "already_open": true, "exit_codes": true,
					"pane": pane,
					"note": "a terminal was already open; this is it. Use `sudo -E su` " +
						"rather than `sudo su` to keep exit codes across a root shell.",
				}, false, true
			}
		} else {
			if _, err := s.tmux("new-session", "-d", "-s", tmuxSession); err != nil {
				return textContent("could not start the terminal session: %v", err), true, true
			}
			// Give the window back its title. The emulator names the window
			// after the process it was told to run, which is now `tmux` — so
			// every terminal on the desktop was called "tmux" regardless of what
			// it was doing. That is a loss for the person reading the taskbar
			// and for the agent reading list_windows, where the title is often
			// the only thing distinguishing two windows of the same class.
			//
			// Failure here is not fatal: a plainly-titled terminal still works,
			// and refusing to open one over a cosmetic setting would be worse
			// than the cosmetic problem.
			_, _ = s.tmux("set-option", "-t", tmuxSession, "set-titles", "on")
			_, _ = s.tmux("set-option", "-t", tmuxSession, "set-titles-string",
				"#{pane_current_command} — #{pane_current_path}")
		}

		// The emulator attaches to that session rather than running a shell of
		// its own. What the person sees is unchanged; what it buys is that the
		// shell is addressable by a handle instead of by its position in the
		// accessibility tree, and readable without walking that tree at all.
		if err := s.attachWindow(ctx); err != nil {
			// "on the shared desktop", not "open". The window may well have
			// opened; what failed is putting it where the room is watching, and
			// a caller told only "could not open a terminal" while one is
			// visibly on screen goes looking for the wrong fault.
			return textContent("could not put a terminal on the desktop the room "+
				"is watching: %v", err), true, true
		}

		// Wait for the shell to draw its first prompt, otherwise the caller
		// types into a window that is not ready yet. Polled at 100ms rather
		// than the 400ms this used to need: each check is one capture-pane, so
		// the loop costs less now at four times the rate.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if !sleepCtx(ctx, 100*time.Millisecond) {
				break
			}
			pane, err := s.activePane()
			if err != nil {
				continue
			}
			if txt, err := s.capturePane(pane, 0); err == nil && promptTail.MatchString(txt) {
				return map[string]any{
					"opened": true, "exit_codes": true, "pane": pane,
					"note": "exit codes are reported. Use `sudo -E su` rather than " +
						"`sudo su` to keep them across a root shell.",
				}, false, true
			}
		}
		return textContent("the terminal did not show a prompt in time"), true, true
	case "check_errors":
		// One walk of the whole tree rather than a find per role, because the
		// question cannot be answered from the dialog element alone.
		//
		// A toolkit puts the message in a child label. zenity --error --text=…
		// produces a dialog whose own name is the title and whose text is
		// empty, so an application that titles its error box with its own name
		// — which is most of them — was invisible here: the wording that says
		// it failed was one level down, in a child this never looked at.
		//
		// Refs are paths ("2/0/3"), so a descendant is a ref with the dialog's
		// as its prefix. That makes the subtree free once the tree is in hand,
		// and it is why this is one subprocess now instead of two.
		out, err := s.a11yRaw("tree", "--depth", "14", "--limit", "1200")
		if err != nil {
			return textContent("could not read the accessibility tree: %v", err), true, true
		}
		var tree struct {
			Elements []struct {
				Ref  string `json:"ref"`
				Role string `json:"role"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"elements"`
		}
		if json.Unmarshal([]byte(out), &tree) != nil {
			return textContent("the accessibility bridge did not return a tree: %s",
				strings.TrimSpace(out)), true, true
		}

		found := []map[string]any{}
		seen := map[string]bool{}
		for _, e := range tree.Elements {
			if e.Role != "alert" && e.Role != "dialog" {
				continue
			}
			if seen[e.Ref] {
				continue
			}
			// Everything printed inside this dialog, in tree order, so the
			// message reads the way it does on screen.
			var parts []string
			if t := strings.TrimSpace(e.Text); t != "" {
				parts = append(parts, t)
			}
			prefix := e.Ref + "/"
			for _, d := range tree.Elements {
				if !strings.HasPrefix(d.Ref, prefix) {
					continue
				}
				for _, s := range []string{d.Name, d.Text} {
					if s = strings.TrimSpace(s); s != "" && !slices.Contains(parts, s) {
						parts = append(parts, s)
					}
				}
			}
			body := strings.TrimSpace(e.Name + " " + strings.Join(parts, " "))
			// An alert is worth reporting whatever it says; a dialog only when
			// its wording suggests a failure, or every open file chooser would
			// look like a problem.
			if e.Role != "alert" && !errorish.MatchString(body) {
				continue
			}
			seen[e.Ref] = true
			found = append(found, map[string]any{
				"ref": e.Ref, "role": e.Role, "title": e.Name,
				"text": strings.Join(parts, "\n"),
			})
		}
		if len(found) == 0 {
			return map[string]any{
				"errors_on_screen": false,
				"note": "nothing is reporting a failure. This only sees graphical " +
					"dialogs — a command that failed silently in a terminal shows up " +
					"in terminal_read, and a program that died at launch in the " +
					"stderr that launch_app returns.",
			}, false, true
		}
		return map[string]any{
			"errors_on_screen": true,
			"dialogs":          found,
			"hint": "use ui_click with the ref of a button to dismiss one, " +
				"after reading what it says",
		}, false, true
	case "terminal_read":
		n := argInt(args, "lines")
		if n <= 0 {
			n = 40
		}

		// tmux first, accessibility second, and the fallback is not a leftover:
		// a terminal a person opened from the menu is not under tmux, and this
		// tool exists precisely for "look at the error I just hit". Refusing to
		// read a window that is plainly on the screen would be the wrong answer
		// to the question it was built to answer.
		var (
			text string
			pane string
			err  error
		)
		if pane, err = s.activePane(); err == nil {
			// One screen of scrollback beyond whatever was asked for, so a
			// request for more lines than the window is tall still gets them.
			text, err = s.capturePane(pane, n)
		}
		if err != nil {
			pane = ""
			ref, ferr := s.findTerminal()
			if ferr != nil {
				return textContent("%v", ferr), true, true
			}
			if text, err = s.readTerminal(ref); err != nil {
				return textContent("%v", err), true, true
			}
		}

		out := map[string]any{"text": lastLines(text, n)}
		if pane != "" {
			out["pane"] = pane
		}
		// Any interactive shell records this, so the last command may well be
		// one a person typed. That is deliberate.
		if code, cmd, ok := s.readExitCode(pane, time.Time{}); ok {
			out["last_command"] = cmd
			out["last_exit_code"] = code
			out["last_succeeded"] = code == 0
			if code != 0 {
				out["note"] = "the last command in this terminal failed — it may " +
					"have been run by a person, not by you"
			}
		}
		return out, false, true

	case "terminal_run":
		command := argStr(args, "command")
		if strings.TrimSpace(command) == "" {
			return textContent("`command` is missing"), true, true
		}
		timeout := time.Duration(argInt(args, "timeout_ms")) * time.Millisecond
		if timeout <= 0 {
			timeout = 120 * time.Second
		}

		pane, err := s.activePane()
		if err != nil {
			// Say which of the two situations this is. "No terminal is open"
			// while one is plainly on the screen reads as a broken tool and
			// sends the model looking in the wrong place; the real answer is
			// that this one is not ours to type into.
			if refs, ferr := s.terminalRefs(); ferr == nil && len(refs) > 0 {
				return textContent("there is a terminal on the desktop, but it was " +
					"not opened by terminal_open, so there is no reliable way to type " +
					"into it or read its exit codes. Call terminal_open for one this " +
					"can drive, or terminal_read to see what that one is showing."), true, true
			}
			return textContent("no terminal window is open — call terminal_open first"), true, true
		}
		// Not on screen: the session is alive and nothing is displaying it, for
		// any of the reasons sessionOnScreen distinguishes. Put a window back
		// rather than running into the void — the caller asked for the tool that
		// is watched, and the honest options are to make it watched again or to
		// refuse. Reopening keeps the shell, its history and the exit-code hook,
		// which refusing would throw away for no gain.
		//
		// A window that is merely minimised or on another desktop lands here
		// too, and gets a second window rather than an un-minimise. That is
		// deliberate: raising somebody's window, moving their desktop or
		// stealing their focus is a person's decision about their own screen,
		// and an agent that rearranges it to satisfy its own promise has helped
		// itself rather than them.
		//
		// The second window is a different matter, and attachWindow does place
		// that one — pinned to the desktop the room is watching, un-shaded and
		// raised. The line is ownership rather than politeness: never rearrange a
		// window that was found, always place one that was made.
		if state, why := s.sessionOnScreen(); state != screenShowing {
			if err := s.attachWindow(ctx); err != nil {
				return textContent("nobody can see this terminal (%s) and a new "+
					"window could not be opened, so the command was NOT run: %v. Use "+
					"run_command or job_start if you meant to run it anyway.",
					why, err), true, true
			}
			if p, err := s.activePane(); err == nil {
				pane = p
			}
		}

		// A dead pane accepts keystrokes and does nothing with them. tmux keeps
		// a pane after its process exits when remain-on-exit is set, send-keys
		// into it returns success, and the text simply lands nowhere — so
		// without this the wait below runs the caller's whole timeout against a
		// pane that was never going to answer, and then returns the pane's stale
		// contents as the command's output. Failing here costs a call; failing
		// there cost five minutes and produced a plausible wrong answer.
		if dead, _, err := s.paneState(pane); err == nil && dead {
			return textContent("pane %s is dead — the process that was running in "+
				"it has exited and tmux is only keeping the text on screen. Nothing "+
				"typed into it will run. Call terminal_open for a live shell.",
				pane), true, true
		}

		before, err := s.capturePane(pane, 0)
		if err != nil {
			return textContent("%v", err), true, true
		}
		started := time.Now()

		// send-keys rather than xdotool: it writes to the pty instead of the X
		// server, so the command does not depend on the window holding focus or
		// on the keyboard layout having a key for every character in it. Both
		// were real failure modes — a person clicking another window mid-command
		// used to split it across two applications.
		//
		// -l sends the text literally, which matters more than it looks: without
		// it tmux reads its arguments as key names, so a command containing the
		// word `Enter` or a token like `C-c` would be delivered as keystrokes
		// rather than as the characters somebody asked for.
		if _, err := s.tmux("send-keys", "-t", pane, "-l", "--", command); err != nil {
			return textContent("could not send the command: %v", err), true, true
		}
		if _, err := s.tmux("send-keys", "-t", pane, "Enter"); err != nil {
			return textContent("could not press Return: %v", err), true, true
		}

		// Wait for the shell to be the foreground process again AND for the text
		// to settle. Neither alone is enough: the pane is briefly running the
		// shell in the instant between Return and the command starting, and the
		// text is briefly unchanged during any pause in a command's output.
		//
		// Polled at 100ms rather than 250ms. Each round is a capture-pane and a
		// display-message — a few milliseconds against the ~99ms an accessibility
		// read cost — so the loop is both finer-grained and cheaper than the one
		// it replaces.
		deadline := time.Now().Add(timeout)
		var last string
		stable := 0
		settled := false
		closed := false
		for time.Now().Before(deadline) {
			if !sleepCtx(ctx, 100*time.Millisecond) {
				break
			}

			now, err := s.capturePane(pane, 0)
			if err != nil {
				// The pane is gone. Some commands end the shell — `exit`,
				// `logout`, anything that closes the last pane — and those leave
				// no prompt to wait for; without noticing, this would spend the
				// whole timeout waiting for one that is never coming.
				//
				// A pane id is a handle, so its disappearance IS the signal.
				// The accessibility route could not do this: refs are positional
				// paths, so a closed window did not produce an error, it started
				// resolving to whatever moved into its place — which is why the
				// old code had to count terminals on a side channel instead.
				if !s.sessionAlive() {
					closed = true
					break
				}
				continue
			}
			if now == last {
				stable++
			} else {
				stable = 0
				last = now
			}
			if stable < 2 {
				continue
			}
			// Two quiet rounds — a fifth of a second of nothing happening, which
			// a finished command always produces — and only then the one tmux
			// question that decides between the three ways quiet can end.
			//
			// Dead is checked here rather than beside the capture error above
			// because a dead pane is still capturable: `exit` in a pane whose
			// window has remain-on-exit (every job window, and any window a
			// person set it on) leaves the text on screen forever, so the
			// handle never disappears and this would otherwise sit out the whole
			// timeout waiting for a prompt from a shell that is gone.
			dead, foreground, err := s.paneState(pane)
			if err != nil {
				continue
			}
			if dead {
				closed = true
				break
			}
			if shellCommands[foreground] && now != before {
				settled = true
				break
			}
		}

		output := strings.TrimPrefix(last, before)
		output = strings.TrimSpace(output)
		res := map[string]any{
			"command":  command,
			"output":   output,
			"finished": settled,
		}
		if code, _, ok := s.readExitCode(pane, started); ok {
			res["exit_code"] = code
			res["succeeded"] = code == 0
		}
		switch {
		case closed:
			// Not a timeout, and not a failure either: the command did what it
			// was asked and the shell it was asked in no longer exists. Saying
			// which of the two happened is the whole point — "may still be
			// running" would be false, and silence would be worse.
			res["terminal_closed"] = true
			res["note"] = "the shell ended while the command ran, which is what " +
				"`exit`, `logout` and anything that closes the last pane do. The " +
				"window may have gone with it or may still be on screen holding the " +
				"text — either way what it printed is in `output`, and there is no " +
				"shell left to confirm against, so `finished` stays false."
		case !settled:
			res["note"] = "timed out waiting for the command to finish; it may still " +
				"be running. Call terminal_read to check on it."
		}
		if _, ok := res["exit_code"]; !ok && !closed {
			// Without instrumentation the text is all there is, and "no error
			// message" is not the same as success. Say so rather than let it
			// pass for one.
			//
			// Not when the shell closed, though. A shell that exits never runs
			// its prompt hook again, so there is no status to find and that is
			// expected — printing "this terminal was not opened with
			// terminal_open" there is simply false, and a false explanation is
			// worse than none: it sends the caller to fix something that is not
			// broken. The `note` above already says what happened.
			res["hint"] = "No exit code available — this terminal was not opened " +
				"with terminal_open, so judge by the output alone."
		}
		return res, false, true
	}
	return nil, false, false
}

// bringToTheRoom forces a set of windows onto the desktop the room is looking at
// and waits until it has actually happened.
//
// This is the general form of what attachWindow does for a terminal, extracted
// because the terminal was never the only door. launch_app and open_app_and_wait
// put windows on the same shared screen and neither placed what it opened: an
// application whose window came up on another desktop, or shaded, or behind
// everything, was reported as launched and could not be seen by anybody. Same
// invariant, same failure, and no reason for one of them to defend it alone.
//
// open_app_and_wait's own attempt was `wmctrl -i -a`, which is worse than
// nothing here: activate moves the VIEWER to the window's desktop rather than
// the window to the viewer's. On a shared screen that yanks everybody watching
// to wherever the application happened to open, which is the reverse of the
// promise — the room does not follow the window, the window joins the room.
//
// Forcing is a request, not a call. The answer arrives as a change on screen a
// pass later, so this asks and looks, repeatedly, and reports only what it can
// see is true.
func (s *Server) bringToTheRoom(ctx context.Context, ids []string, within time.Duration) (bool, string) {
	if len(ids) == 0 {
		return false, "no window to place"
	}
	e, err := s.windows()
	if err != nil {
		return false, fmt.Sprintf("X is not reachable, so nothing could be placed: %v", err)
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}

	deadline := time.Now().Add(within)
	last := "the window never reached the shared desktop"
	for time.Now().Before(deadline) {
		scr, serr := e.ScreenState()
		if serr != nil {
			last = fmt.Sprintf("X stopped answering: %v", serr)
			if !sleepCtx(ctx, 200*time.Millisecond) {
				return false, "cancelled"
			}
			continue
		}
		var mine []desktop.OnScreen
		for _, w := range scr.Windows {
			if wanted[w.ID] {
				mine = append(mine, w)
			}
		}
		if len(mine) == 0 {
			last = "the window is no longer managed — it may have closed"
			if !sleepCtx(ctx, 200*time.Millisecond) {
				return false, "cancelled"
			}
			continue
		}

		for _, w := range mine {
			win, perr := desktop.ParseWindowID(w.ID)
			if perr != nil {
				continue
			}
			if ferr := e.ShowOnDesktop(win, scr.Desktop); ferr != nil {
				last = fmt.Sprintf("X would not carry the request: %v", ferr)
			}
		}
		if state, why := placementOf(mine, scr.Desktop); state == placeShared {
			return true, ""
		} else {
			last = why
		}
		if !sleepCtx(ctx, 200*time.Millisecond) {
			return false, "cancelled"
		}
	}
	return false, last
}

// windowsOf returns the managed windows belonging to a process, itself or any
// descendant.
//
// The pid matters rather than the window title because a launcher is often not
// the process that opens the window: `sh -c chromium` forks, a .desktop entry
// execs something else, an application re-execs itself after reading its
// configuration. Matching on _NET_WM_PID alone would miss all of those, which is
// why this walks the process tree the same way the terminal's owner check does.
func (s *Server) windowsOf(pid int) []string {
	e, err := s.windows()
	if err != nil {
		return nil
	}
	scr, err := e.ScreenState()
	if err != nil {
		return nil
	}
	owners := processOwners([]int{pid})
	var out []string
	for _, w := range scr.Windows {
		if w.PID > 0 && owners[w.PID] {
			out = append(out, w.ID)
		}
	}
	return out
}
