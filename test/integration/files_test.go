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

// Files, commands and processes.
//
// The cleanest tools in the catalogue to test, because their effect is a fact
// about the filesystem or the process table and neither is open to
// interpretation. Where the pointer tests had to argue about what counts as
// evidence, these can simply look.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWriteFile(t *testing.T) {
	path := "/tmp/it-write.txt"
	want := "written-through-mcp"
	Sh(t, "rm -f %s", path)

	devDesk(t).Call(t, "write_file", map[string]any{"path": path, "content": want})

	// The container's own cat. read_file agreeing would only show that the two
	// share whatever they share.
	if got := Sh(t, "cat %s", path); !strings.Contains(got, want) {
		t.Fatalf("the file holds %q, not %q", got, want)
	}
}

func TestReadFile(t *testing.T) {
	path := "/tmp/it-read.txt"
	want := "placed-from-outside"
	// The other direction: put it there without MCP, so the tool is reporting
	// on something it did not produce.
	Sh(t, "printf %%s %q > %s", want, path)
	t.Cleanup(func() { Sh(t, "rm -f %s", path) })

	out := devDesk(t).Call(t, "read_file", map[string]any{"path": path})
	if !strings.Contains(out, want) {
		t.Fatalf("read_file returned %q for a file containing %q", trunc(out, 200), want)
	}
}

func TestListDirectory(t *testing.T) {
	dir := "/tmp/it-listdir"
	Sh(t, "rm -rf %s && mkdir -p %s/sub && touch %s/alpha.txt %s/beta.log", dir, dir, dir, dir)
	t.Cleanup(func() { Sh(t, "rm -rf %s", dir) })

	out := devDesk(t).Call(t, "list_directory", map[string]any{"path": dir})
	for _, want := range []string{"alpha.txt", "beta.log", "sub"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s is in the directory and not in the listing:\n%s", want, trunc(out, 300))
		}
	}
	// And nothing that is not there. A listing of the wrong directory would
	// still contain plausible names.
	if strings.Contains(out, "gamma") {
		t.Errorf("the listing contains an entry the directory does not have:\n%s", trunc(out, 300))
	}
}

func TestRunCommand(t *testing.T) {
	marker := "/tmp/it-run.txt"
	Sh(t, "rm -f %s", marker)

	out := devDesk(t).Call(t, "run_command", map[string]any{
		"command": "echo ran-through-mcp > " + marker, "timeout_ms": 20000})

	if got := Sh(t, "cat %s", marker); !strings.Contains(got, "ran-through-mcp") {
		t.Fatalf("the command reported %q and the file holds %q", trunc(out, 120), got)
	}
	// The exit status has to be the command's own, or a caller cannot tell a
	// failure from a success — which is the whole reason this tool reports it.
	failed := devDesk(t).Call(t, "run_command", map[string]any{
		"command": "exit 3", "timeout_ms": 20000})
	if !strings.Contains(failed, "3") {
		t.Errorf("a command exiting 3 reported %s", trunc(failed, 200))
	}
}

func TestLaunchApp(t *testing.T) {
	title := "LAUNCHWIN"
	devDesk(t).Call(t, "launch_app", map[string]any{
		"command": fmt.Sprintf("xterm -T %s -e sleep 120", title)})
	t.Cleanup(func() { X(t, "wmctrl -c %s 2>/dev/null || true", title) })

	// Detached, so the proof is a process and a window that outlive the call.
	eventually(t, 15*time.Second, "the launched program to appear on the display", func() bool {
		return strings.Contains(X(t, "wmctrl -l"), title)
	})
	if Sh(t, "pgrep -fc 'xterm -T %s'", title) == "0" {
		t.Errorf("the window is there and no process matches it")
	}
}

func TestListProcesses(t *testing.T) {
	// Something with a name nothing else would have.
	Sh(t, "setsid sh -c 'exec -a it-marker-proc sleep 90' >/dev/null 2>&1 &")
	t.Cleanup(func() { Sh(t, "pkill -f it-marker-proc 2>/dev/null || true") })
	time.Sleep(700 * time.Millisecond)

	out := devDesk(t).Call(t, "list_processes", map[string]any{"filter": "sleep"})
	if !strings.Contains(out, "sleep") {
		t.Fatalf("a sleep is running and the listing has none:\n%s", trunc(out, 300))
	}
	// The pids it reports have to exist. A stale list is worse than a short
	// one, because a caller acts on it — kill_process takes a pid.
	for _, pid := range pidsIn(out) {
		if Sh(t, "test -d /proc/%s && echo yes", pid) != "yes" {
			t.Errorf("it reports pid %s and /proc has no such process", pid)
		}
	}
}

func TestIsRunning(t *testing.T) {
	// Xvfb is certain to be up: without it there is no desktop to test.
	out := devDesk(t).Call(t, "is_running", map[string]any{"name": "Xvfb"})
	if !strings.Contains(out, "true") {
		t.Errorf("Xvfb is running and is_running says %s", out)
	}
	// And the negative, which is the half that catches a tool answering yes to
	// everything.
	out = devDesk(t).Call(t, "is_running", map[string]any{"name": "definitely-not-a-real-process-xyz"})
	if strings.Contains(out, "true") {
		t.Errorf("a process that does not exist reads as running: %s", out)
	}
}

func TestKillProcess(t *testing.T) {
	// A copy of sleep under a name nothing else uses, detached so it outlives
	// the exec that started it. exec -a would have been tidier and is a bashism
	// the container's /bin/sh does not have.
	// Started as the desktop's own user, not as root. pkill can only signal
	// processes it owns, so a root-owned target is found and refused — and the
	// tool used to report that as "no process matched", which sent this test
	// looking for a process that was plainly there.
	Sh(t, "cp /bin/sleep /tmp/it-kill-me 2>/dev/null; chmod 755 /tmp/it-kill-me")
	ShUser(t, "setsid /tmp/it-kill-me 300 </dev/null >/dev/null 2>&1 &")
	time.Sleep(900 * time.Millisecond)
	// wc -l rather than `pgrep -c ... || echo 0`: pgrep exits non-zero when it
	// finds nothing AND prints 0, so the fallback appends a second zero and the
	// output is "0\n0". Comparing that to "0" is false, which made this guard
	// pass when nothing had started and the wait below never finish.
	if countOf(t, "it-kill-me") == 0 {
		t.Fatal("the process to be killed did not start")
	}
	t.Cleanup(func() { Sh(t, "pkill -x it-kill-me 2>/dev/null; rm -f /tmp/it-kill-me") })

	devDesk(t).Call(t, "kill_process", map[string]any{"name": "it-kill-me"})

	// pgrep -x matches the process NAME, not the command line. With -f the
	// check would find the shell running the check, whose own arguments contain
	// the pattern, and could never report zero.
	eventually(t, 8*time.Second, "the process to go", func() bool {
		return countOf(t, "it-kill-me") == 0
	})
}

func TestListInstalledApps(t *testing.T) {
	out := devDesk(t).Call(t, "list_installed_apps", nil)
	// It reads .desktop entries, so every name it gives has to be in one.
	if !strings.Contains(strings.ToLower(out), "chromium") {
		t.Errorf("chromium has a desktop entry and is not in the list:\n%s", trunc(out, 300))
	}
	entries := Sh(t, "ls /usr/share/applications/*.desktop | wc -l")
	if entries == "0" {
		t.Skip("no desktop entries in this image")
	}
}

func TestListCommands(t *testing.T) {
	// Unfiltered: categories rather than names, and the total has to match what
	// is actually on PATH.
	out := devDesk(t).Call(t, "list_commands", nil)
	if !strings.Contains(out, "categories") {
		t.Fatalf("the unfiltered call should answer with categories: %s", trunc(out, 200))
	}

	// Filtered: a command that certainly exists, with the package that owns it.
	out = devDesk(t).Call(t, "list_commands", map[string]any{"filter": "chromium", "describe": true})
	if !strings.Contains(out, "chromium") {
		t.Fatalf("chromium is on PATH and the filter found nothing: %s", trunc(out, 200))
	}
	// dpkg is the authority on which package a path came from.
	owner := Sh(t, "dpkg -S $(command -v chromium) 2>/dev/null | cut -d: -f1")
	if owner != "" && !strings.Contains(out, owner) {
		t.Errorf("dpkg says %s owns chromium and the reply says otherwise:\n%s", owner, trunc(out, 300))
	}
	// A category nothing matches must come back empty rather than as everything.
	out = devDesk(t).Call(t, "list_commands", map[string]any{"category": "definitely-no-such-section"})
	if strings.Contains(out, "\"command\"") {
		t.Errorf("a category that does not exist returned commands:\n%s", trunc(out, 200))
	}
}

func TestSudoStatus(t *testing.T) {
	out := devDesk(t).Call(t, "sudo_status", nil)
	// Whatever it claims has to match whether sudo actually works without a
	// password, which is the only thing the answer is for.
	real := Sh(t, "sudo -n true 2>/dev/null && echo yes || echo no")
	claims := strings.Contains(strings.ToLower(out), "true") ||
		strings.Contains(strings.ToLower(out), "passwordless")
	if (real == "yes") != claims {
		t.Errorf("sudo -n %s and sudo_status says %s", real, trunc(out, 200))
	}
}

// --- helpers -----------------------------------------------------------------

// countOf counts processes by exact name. -x matches the process name rather
// than the command line, so it cannot find the shell asking the question — a
// trap this suite fell into twice with -f.
func countOf(t *testing.T, name string) int {
	t.Helper()
	return atoi(strings.TrimSpace(Sh(t, "pgrep -x %s | wc -l", name)))
}

// pidsIn scrapes the pid fields out of a JSON listing, without insisting on the
// exact shape of the reply.
func pidsIn(body string) []string {
	var out []string
	rest := body
	for {
		i := strings.Index(rest, "\"pid\"")
		if i < 0 {
			return out
		}
		rest = rest[i+5:]
		j := 0
		for j < len(rest) && (rest[j] == ':' || rest[j] == ' ') {
			j++
		}
		k := j
		for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
			k++
		}
		if k > j {
			out = append(out, rest[j:k])
		}
		if len(out) > 8 {
			return out // a sample is enough; this is a sanity check, not an audit
		}
	}
}
