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

// One desktop, one history.
//
// # The asymmetry this closes
//
// Everything the agent did was already on disk: every tool call, with arguments,
// duration, outcome and the connection it came in on, appended to a rotating
// JSONL trail that outlives the process. Everything a PERSON did was in a
// one-slot file per terminal pane that the next prompt overwrote.
//
// That is backwards in the situation it matters most. A person stops the agent
// mid-task — the panic button — fixes something by hand, and says carry on. The
// agent is then told to go and look at what changed, and its instruments were:
// a screenshot, which shows the end state and not how it got there, and
// capture-pane, which shows whatever has not yet scrolled away in the one pane
// it thought to look at. Meanwhile its own afternoon was fully recorded.
//
// So both planes write to a history now, and both read the same one. The shell
// side is appended by shell-report.sh from every interactive shell on the
// desktop, so it catches a person typing regardless of which terminal they
// opened or whether anybody was watching.
//
// # What is NOT here, and why that is mostly fine
//
// There is no browser recorder. It looked necessary and turned out not to be:
// a window title IS the page title, so `chromium — Gmail` becoming `chromium —
// AWS Console` is a navigation, and the window events already carry it. A
// dedicated CDP poller would buy the URL rather than the title, at the cost of a
// background connection to somebody's browser that runs whether or not anybody
// asked. That trade is worth making when somebody needs the URL, and not before.
//
// Pointer MOVEMENT is absent, and its absence makes the log better rather than
// worse. Movement arrives at whatever rate a browser can send; a file holding
// every intermediate coordinate is larger, slower to read and less informative
// than one holding "clicked at 415,301", because the trail of pixels leading to
// an act buries the act. Clicks, scrolls, drags and keys are events; movement is
// context that travels with them.
//
// Keystrokes are COUNTED, never captured, and that is the one deliberate hole.
// A desktop is where people type sudo passwords, SSH passphrases and bank
// logins, and this history is read by an agent that sends what it reads to a
// model API — a verbatim keystroke log would put a password in a third party's
// request within a day of somebody enabling it, and no retention policy fixes
// that, because the copy has already left. What is written instead is "typed 14
// keys over 3s", which is the understanding an agent actually needs. Where the
// text genuinely matters it is already there verbatim: a shell command is a
// public act on a shared screen in a way a password field is not, and
// shell-report.sh records it in full.
package mcp

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sentineldesk/desktop/pkg/config"
)

// The two files the people's side of the history is written to. The writers are
// in other places — deploy/desktop/shell-report.sh and internal/stream/witness.go
// — and all three have to agree on the paths and on the tab-separated shape.
//
// Two files rather than one because they have two writers with nothing in
// common: a bash function in whatever shell somebody opened, and a Go process
// holding a WebRTC connection. Making them share a descriptor would mean a lock
// they cannot share; making them share a file without one means interleaved
// half-lines. They are merged at READ time instead, which costs a sort nobody
// notices and cannot corrupt anything.
const (
	shellLogPath   = "/tmp/sentineldesk/shell.log"
	witnessLogPath = "/tmp/sentineldesk/input.log"
)

// How long the desktop remembers, and how much.
//
// Both limits, not either, because they fail in opposite directions. Age alone
// lets a busy afternoon fill a disk inside the window; size alone lets a machine
// that nobody has touched for a month keep a record of the last thing that
// happened on it, which is a privacy cost with no operational value — an
// activity log is read to understand something recent.
//
// A day is the default because that is the span of "what happened while I was
// away", which is the question this answers. ACTIVITY_RETENTION_HOURS moves it;
// 0 keeps everything until the size cap bites, for somebody who wants a longer
// audit and accepts what that means.
const activityMaxBytes = 4 << 20

func activityRetention() time.Duration {
	return time.Duration(config.Int("ACTIVITY_RETENTION_HOURS", 24)) * time.Hour
}

// activityEntry is one thing that happened, from either side.
type activityEntry struct {
	Time string `json:"time"`

	// Actor is who did it, in the terms a reader thinks in: a username for a
	// person at a terminal, the client name for an agent connection.
	Actor string `json:"actor"`

	// Source says where it came from, and it is the field to read before
	// trusting Actor:
	//
	//	terminal  a command typed at a shell, with its exit code
	//	person    a click, a scroll, a burst of typing, control taken or given
	//	agent     a tool call over the MCP socket
	//
	// A command the agent typed with terminal_run appears on BOTH sides: once as
	// a tool call, once as a shell line under whatever user the desktop runs as.
	// That is not double counting to be cleaned up, it is the same act seen from
	// two places, and collapsing them would mean guessing which shell lines the
	// agent caused.
	Source string `json:"source"`

	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
	Exit   *int   `json:"exit_code,omitempty"`
}

// readLog parses one of the two tab-separated files.
//
// The two shapes differ — the shell records an exit code, the input recorder
// records what kind of act it was — so the parser is told which it is reading
// rather than guessing from the field count, which would misread a shell
// command containing a tab as an input event.
func readLog(path, source string) ([]activityEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Not an error. It means nobody has done that kind of thing since
			// the desktop started, which is an ordinary state and must not read
			// as a broken history — an empty answer and a failed answer lead a
			// caller to opposite conclusions.
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []activityEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if source == "terminal" {
			// time, user, pane, exit code, command
			parts := strings.SplitN(line, "\t", 5)
			if len(parts) != 5 {
				continue
			}
			e := activityEntry{Time: parts[0], Actor: parts[1], Source: "terminal",
				What: "ran", Detail: parts[4]}
			if code, err := parseInt(parts[3]); err == nil {
				e.Exit = &code
				ok := code == 0
				e.OK = &ok
			}
			out = append(out, e)
			continue
		}
		// time, actor, what, detail
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		out = append(out, activityEntry{Time: parts[0], Actor: parts[1],
			Source: "person", What: parts[2], Detail: parts[3]})
	}
	return out, nil
}

// enforceRetention drops what the desktop has agreed to forget.
//
// Rewritten in place rather than appended to, because both limits need the whole
// file: age needs every line's timestamp and size needs the total. It runs when
// somebody READS the history, not on a timer — the writers are on the hot path
// of a person typing, and a log that tidies itself while nobody is looking is
// paying for housekeeping nobody asked for.
//
// The consequence, stated rather than discovered: a desktop nobody queries keeps
// its files until the next read. That is the intended trade — the cost is disk
// on an idle machine, and the alternative is a background goroutine rewriting
// files on a desktop where nothing is happening.
func enforceRetention(path string) {
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	keepFor := activityRetention()
	if st.Size() <= activityMaxBytes && keepFor <= 0 {
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	if keepFor > 0 {
		cutoff := time.Now().UTC().Add(-keepFor).Format(time.RFC3339)
		// Both writers stamp RFC3339 in UTC, so string comparison is time
		// comparison and the file is already in order. The first line that is
		// recent enough is where the survivors start.
		first := sort.Search(len(lines), func(i int) bool {
			t, _, found := strings.Cut(lines[i], "\t")
			return found && t >= cutoff
		})
		lines = lines[first:]
	}

	// Size is the backstop for a day busy enough to fill the disk inside the
	// window. Halving rather than emptying: a history that vanishes the moment
	// it gets long is worse than one that forgets its oldest half.
	for len(lines) > 1 && sizeOf(lines) > activityMaxBytes {
		lines = lines[len(lines)/2:]
	}

	// Through a temporary file and a rename, so a shell appending at this exact
	// moment cannot land in the middle of a truncation.
	tmp := path + ".trim"
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if os.WriteFile(tmp, []byte(body), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func sizeOf(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	return n
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}

// activity merges the histories into one, oldest first.
func (s *Server) activity(n int, source string) ([]activityEntry, error) {
	enforceRetention(shellLogPath)
	enforceRetention(witnessLogPath)

	var out []activityEntry

	if source != "agent" {
		shell, err := readLog(shellLogPath, "terminal")
		if err != nil {
			return nil, err
		}
		out = append(out, shell...)

		people, err := readLog(witnessLogPath, "person")
		if err != nil {
			return nil, err
		}
		out = append(out, people...)
	}

	if source != "person" && source != "terminal" && s.actions != nil {
		for _, e := range s.actions.Tail(0, "") {
			actor := e.Client
			if actor == "" {
				actor = "agent"
			}
			ok := e.OK
			entry := activityEntry{
				Time: e.Time, Actor: actor, Source: "agent",
				What: e.Tool, Detail: e.Args, OK: &ok,
			}
			if e.Denied != "" {
				entry.Detail = strings.TrimSpace(entry.Detail + " — refused: " + e.Denied)
			}
			out = append(out, entry)
		}
	}

	// Every writer stamps UTC in RFC3339 to the second, so string order is time
	// order. Stable, so two things in the same second keep the order they were
	// gathered in rather than being shuffled on every call.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })

	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

func (s *Server) buildActivityTools() []toolDef {
	return []toolDef{
		{
			Name: "activity",
			Risk: riskRead,
			Description: "What has happened on this desktop, oldest first: the commands " +
				"people typed in any terminal AND the tools you called, in one timeline " +
				"with who did each. This is how you find out what somebody did while you " +
				"were not looking — after being interrupted, after a person took the " +
				"controls, or when something changed and you did not change it. Reading " +
				"it is cheaper and far more reliable than guessing from a screenshot. " +
				"source: both (default), person for what the people here did, agent " +
				"for your own.",
			InputSchema: schema(map[string]any{
				"limit":  pIntDef("how many entries, most recent (default 40)", 40),
				"source": pStr("both | person | agent (default both)"),
			}),
		},
	}
}

func (s *Server) dispatchActivity(name string, args map[string]any) ([]map[string]any, bool, bool) {
	if name != "activity" {
		return nil, false, false
	}
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 40
	}
	entries, err := s.activity(limit, argStr(args, "source"))
	if err != nil {
		return textContent("could not read the desktop's history: %v", err), true, true
	}
	if len(entries) == 0 {
		// Said explicitly rather than returning an empty list. "Nothing has
		// happened" and "I could not find out what happened" are opposite
		// answers, and an empty array reads as whichever one the caller already
		// believed.
		return textContent("nothing recorded yet: no commands typed in a terminal " +
			"and no tool calls on this desktop since it started"), false, true
	}
	return jsonContent(map[string]any{
		"entries": entries,
		"note": "source=terminal and source=person are people at keyboards, unless " +
			"you typed it yourself with terminal_run — then the same command also " +
			"appears as a tool call. Keystrokes are counted, never captured: this " +
			"desktop is where people type passwords.",
	}), false, true
}

// shellLogDir is exported through nothing; it exists so the daemon can make the
// directory at startup. The shell creates it too, but the first reader should
// not depend on somebody having opened a terminal first.
func ensureActivityDir() {
	_ = os.MkdirAll(filepath.Dir(shellLogPath), 0o755)
}
