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

// Permission policy and action log for the MCP server.
//
// With root available the MCP has total power over the container. That is fine
// when a person is driving it and wrong when an unsupervised agent is. Almost
// every MCP server in the ecosystem grants unrestricted access with no middle
// ground; this offers four levels and two lists.
//
//	MCP_POLICY=full      (default) everything is allowed
//	MCP_POLICY=approve   dangerous tools wait for a person in the room to allow
//	                     each call; everything else runs as under full
//	MCP_POLICY=safe      everything except what changes the system or runs code
//	MCP_POLICY=readonly  observation only: see the screen, read the tree, list
//
//	MCP_DENY=run_command,ssh_*     additionally deny these (suffix * for prefix)
//	MCP_ALLOW=screenshot,ui_*      when set, ONLY these
//
// `approve` is the level between the two answers `safe` and `full` force a
// deployment to choose from. Under safe the agent is refused everything
// dangerous outright and works around it or stalls; under full it is trusted
// with all of it, all the time. approve turns the refusal into a question put
// to the people sharing the desktop — the same doctrine as the rest of the
// room: you act by being there. The gate itself lives in handleToolCall
// beside the other gates; this file only answers whether a call needs it.
//
// The log records every call with its timestamp. While a recording is running it
// also stores the position within the video, so the .mp4 ends up indexed by
// action and what happened can be audited or replayed.

import (
	"encoding/json"
	"fmt"
	"github.com/sentineldesk/desktop/internal/media"
	"os"
	"strings"
	"sync"
	"time"
)

// --- tool classification -----------------------------------------------------
//
// There used to be two hand-written maps here, readOnlyTools and dangerousTools,
// listing tool names by risk. They were three hundred lines from the toolDefs
// they described and nothing tied one to the other, so the catalogue grew and
// the maps did not: by the time they were compared, forty-six of the hundred and
// fourteen tools appeared in neither, which meant refused under readonly and
// allowed under safe with no way to notice either. terminal_run was one of them.
//
// The classification now lives on the toolDef, in registry.go, and Policy reads
// it through the index below. Nothing else changed about how the levels behave.

// Policy decides whether a tool may run.
type Policy struct {
	level string   // full | approve | safe | readonly
	deny  []string // patterns (prefix match with *)
	allow []string // when non-empty, an exclusive allow-list

	// risk is the catalogue's classification, injected once the tools are
	// built. A Policy with no index cannot answer the level questions, so it
	// refuses them rather than guessing — see Allowed.
	risk riskIndex
}

func NewPolicy() *Policy {
	p := &Policy{level: strings.ToLower(strings.TrimSpace(os.Getenv("MCP_POLICY")))}
	switch p.level {
	case "safe", "readonly", "full", "approve":
	case "":
		p.level = "full"
	default:
		fmt.Fprintf(os.Stderr, "mcp: unknown MCP_POLICY=%q, using full\n", p.level)
		p.level = "full"
	}
	p.deny = splitPatterns(os.Getenv("MCP_DENY"))
	p.allow = splitPatterns(os.Getenv("MCP_ALLOW"))
	return p
}

func splitPatterns(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func matchPattern(pat, name string) bool {
	if strings.HasSuffix(pat, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pat, "*"))
	}
	return pat == name
}

// Allowed reports whether the tool may run and, when it may not, why.
func (p *Policy) Allowed(name string, args map[string]any) (bool, string) {
	for _, pat := range p.deny {
		if matchPattern(pat, name) {
			return false, fmt.Sprintf("%q is in MCP_DENY", name)
		}
	}
	if len(p.allow) > 0 {
		ok := false
		for _, pat := range p.allow {
			if matchPattern(pat, name) {
				ok = true
				break
			}
		}
		if !ok {
			return false, fmt.Sprintf("MCP_ALLOW is set and does not include %q", name)
		}
	}
	if p.level == "full" {
		return true, ""
	}

	// Everything below full is a question about risk, and risk comes from the
	// catalogue. A name that is not in it gets refused rather than waved
	// through: dispatch would reject it anyway, and of the two ways to be wrong
	// here, only one of them grants something.
	risk, known := p.risk[name]
	if !known {
		return false, fmt.Sprintf("%q is not in the tool catalogue", name)
	}

	switch p.level {
	case "readonly":
		if risk != riskRead {
			return false, fmt.Sprintf("MCP_POLICY=readonly: %q changes the system", name)
		}
	case "safe":
		if risk == riskDanger {
			return false, fmt.Sprintf("MCP_POLICY=safe: %q runs code or touches the system", name)
		}
		// as_root escalates even when the tool itself is harmless.
		if v, ok := args["as_root"].(bool); ok && v {
			return false, "MCP_POLICY=safe: as_root is disabled"
		}
	case "approve":
		// Nothing is refused HERE: what safe refuses, approve routes through a
		// person. The unknown-name check above still applies — a name the
		// catalogue does not know cannot be approved into existence.
	}
	return true, ""
}

// RequiresApproval reports whether this call has to be allowed by a person in
// the room before it runs — the `approve` level's whole difference from full.
//
// The set is exactly what `safe` refuses: dangerous tools, plus as_root on the
// tools that take it, because escalation makes a harmless tool dangerous and a
// level that asked about run_command while waving through read_file-as-root
// would be gating the label instead of the power.
func (p *Policy) RequiresApproval(name string, args map[string]any) bool {
	if p.level != "approve" {
		return false
	}
	if v, ok := args["as_root"].(bool); ok && v {
		return true
	}
	return p.risk[name] == riskDanger
}

// levelRank orders the levels from most to least permissive.
var levelRank = map[string]int{"full": 3, "approve": 2, "safe": 1, "readonly": 0}

// Restrict returns a policy equal to or STRICTER than the current one, never
// more permissive: a client may give up permissions, not grant itself any. That
// is what makes it possible to hand an agent a read-only connection to the very
// same daemon you are using with full access.
func (p *Policy) Restrict(level, deny, allow string) *Policy {
	out := &Policy{level: p.level, risk: p.risk}
	out.deny = append(append([]string{}, p.deny...), splitPatterns(deny)...)

	if level = strings.ToLower(strings.TrimSpace(level)); level != "" {
		if r, ok := levelRank[level]; ok && r < levelRank[p.level] {
			out.level = level
		}
	}

	// Intersect the allow-lists: if the daemon already limited things to a set,
	// the connection may only keep a subset of it.
	req := splitPatterns(allow)
	switch {
	case len(p.allow) == 0:
		out.allow = req
	case len(req) == 0:
		out.allow = p.allow
	default:
		for _, r := range req {
			for _, base := range p.allow {
				if matchPattern(base, strings.TrimSuffix(r, "*")) || base == r {
					out.allow = append(out.allow, r)
					break
				}
			}
		}
		if out.allow == nil {
			// Nothing in common, so nothing is allowed. Saying so explicitly
			// beats letting an empty list read as "no restrictions".
			out.allow = []string{"\x00none"}
		}
	}
	return out
}

// Filter keeps only the tools this policy allows, so that tools/list never
// advertises something the server is going to refuse.
func (p *Policy) Filter(tools []toolDef) []toolDef {
	if p.level == "full" && len(p.deny) == 0 && len(p.allow) == 0 {
		return tools
	}
	out := make([]toolDef, 0, len(tools))
	for _, t := range tools {
		if ok, _ := p.Allowed(t.Name, nil); ok {
			out = append(out, t)
		}
	}
	return out
}

func (p *Policy) Describe() map[string]any {
	return map[string]any{"level": p.level, "deny": p.deny, "allow": p.allow}
}

// --- action log --------------------------------------------------------------

type actionEntry struct {
	Time    string `json:"time"`
	Tool    string `json:"tool"`
	Args    string `json:"args,omitempty"`
	OK      bool   `json:"ok"`
	Millis  int64  `json:"ms"`
	VideoAt string `json:"video_at,omitempty"` // mm:ss within the recording
	Denied  string `json:"denied,omitempty"`
	// Kind is Denied in a form a program can branch on: policy, room,
	// unknown_tool, tool_error, cancelled or emergency. See denialKind in
	// registry.go.
	Kind string `json:"kind,omitempty"`

	// Approved marks a call that ran because a person in the room allowed it —
	// the `approve` level's elicitation gate. Beside the act in the trail,
	// because an audit of something dangerous should show the permission next
	// to what it permitted rather than leave the reader inferring it from the
	// policy level in force at the time.
	Approved bool `json:"approved,omitempty"`

	// Conn and Client name the connection the call came in on. Every MCP
	// connection shares the room identity `agent`, which is what lets a runtime
	// run several sub-agents under one claim on the desktop — and what makes
	// "the agent did this" useless in an audit once there is more than one.
	Conn   uint64 `json:"conn,omitempty"`
	Client string `json:"client,omitempty"`

	// Task groups the calls a runtime considers one job, supplied by the client
	// in _meta as "sentineldesk/taskId".
	//
	// Without it the trail is per-call, and per-call provenance is not a trail:
	// a session that a person would describe as "install nginx and show me"
	// appears here as seven unrelated rows across as many connections, because
	// a bridge process is spawned per call. Only the runtime knows where a job
	// starts and ends, so only the runtime can say — the server's part is to
	// keep what it is told.
	Task string `json:"task,omitempty"`

	// Goal is what the runtime was trying to do, also from _meta. The server
	// knows what was called; it cannot know why, and an audit missing the why
	// is a list of commands rather than an account of a decision.
	Goal string `json:"goal,omitempty"`

	// Result is a bounded summary of what came back.
	//
	// Arguments alone answer "how did you install it" and not "what did it
	// say", and for an audit the second is usually the more interesting half —
	// it is where a warning that nobody acted on turns out to have been
	// printed. Bounded because a screenshot's base64 or a directory listing
	// would otherwise be the whole log.
	Result string `json:"result,omitempty"`

	// Workroom and Runtime say which desktop this entry came from, stamped at
	// the source from WORKROOM_ID and RUNTIME_ID. One daemon is one room, so
	// within a single file they never vary — the point is the day the files
	// are collected: a fleet's merged trail has to be self-identifying,
	// because reconstructing provenance from filenames is how audit trails
	// get misattributed. Absent for a standalone desktop, which is nobody's
	// fleet member.
	Workroom string `json:"workroom,omitempty"`
	Runtime  string `json:"runtime,omitempty"`
}

// ActionLog keeps the most recent actions in memory and appends them to a JSONL
// file.
//
// Both, not either. The ring answers the action_log tool quickly and bounds
// memory; the file is what makes the trail outlive the process. Persistence
// used to be opt-in through a bare environment variable that was set nowhere,
// so in practice every audit died at the next restart.
type ActionLog struct {
	mu      sync.Mutex
	entries []actionEntry
	max     int
	file    *os.File
	path    string
	maxSize int64 // rotate past this; 0 disables rotation
	size    int64
}

// NewActionLog opens the trail at path, creating the file but not its parent.
//
// A path that cannot be written degrades to the in-memory ring with a line on
// stderr, following the same rule as every other optional capability here: no
// XFixes disables cursor tracking, no XShape disables peer pointers, and an
// unwritable log disables persistence. Refusing to boot a desktop because its
// audit file is in the wrong place would be the wrong trade.
func NewActionLog(path string, maxMB int) *ActionLog {
	l := &ActionLog{max: 2000, path: path}
	if maxMB > 0 {
		l.maxSize = int64(maxMB) * 1024 * 1024
	}
	if path == "" {
		return l
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: action log not persisted, %s: %v\n", path, err)
		return l
	}
	if st, err := f.Stat(); err == nil {
		l.size = st.Size()
	}
	l.file = f
	return l
}

// Add records one call. videoAt is empty when no recording is running.
func (l *ActionLog) Add(e actionEntry) {
	b, err := json.Marshal(e)

	l.mu.Lock()
	l.entries = append(l.entries, e)
	if len(l.entries) > l.max {
		l.entries = l.entries[len(l.entries)-l.max:]
	}
	// Written under the lock, unlike before. The unlocked write raced two calls
	// on the same descriptor and could interleave halves of two JSON objects
	// into one line — which is invisible until something tries to parse the
	// file, i.e. exactly when the audit is being read.
	if l.file != nil && err == nil {
		n, _ := l.file.Write(append(b, '\n'))
		l.size += int64(n)
		l.rotateLocked()
	}
	l.mu.Unlock()
}

// rotateLocked moves the trail aside once it passes the size cap, keeping one
// previous generation. The caller holds the lock.
//
// One generation rather than a numbered series: this bounds the disk at twice
// the cap, which is the property that matters, and a desktop is not the place
// for a log-rotation policy that somebody has to tune. Anything wanting long
// retention should ship the file somewhere that does retention properly.
func (l *ActionLog) rotateLocked() {
	if l.maxSize <= 0 || l.size < l.maxSize || l.path == "" {
		return
	}
	l.file.Close()
	l.file = nil
	l.size = 0
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: could not rotate the action log: %v\n", err)
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: action log stopped after rotation: %v\n", err)
		return
	}
	l.file = f
}

// Tail returns the last n entries, optionally filtered by tool name.
func (l *ActionLog) Tail(n int, filter string) []actionEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.entries) {
		n = len(l.entries)
	}
	src := l.entries[len(l.entries)-n:]
	if filter == "" {
		out := make([]actionEntry, len(src))
		copy(out, src)
		return out
	}
	var out []actionEntry
	for _, e := range src {
		if strings.Contains(e.Tool, filter) {
			out = append(out, e)
		}
	}
	return out
}

func (l *ActionLog) Clear() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.entries)
	l.entries = nil
	return n
}

// summarizeArgs keeps the arguments readable without dumping a whole file into
// the log.
func summarizeArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}

// summarizeContent renders what a tool returned into one bounded line for the
// audit trail.
//
// Bounded hard, and image data dropped rather than truncated: a screenshot is
// forty kilobytes of base64 whose first two hundred characters say nothing at
// all, so keeping a prefix of it would cost the same as keeping the text of
// four real results and be worth nothing. What the audit wants from a
// screenshot is that one was taken.
func summarizeContent(content []map[string]any) string {
	var parts []string
	for _, c := range content {
		switch c["type"] {
		case "text":
			if s, _ := c["text"].(string); s != "" {
				parts = append(parts, s)
			}
		case "image":
			mime, _ := c["mimeType"].(string)
			if mime == "" {
				mime = "image"
			}
			parts = append(parts, "["+mime+"]")
		}
	}
	s := strings.Join(parts, " ")
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// videoOffset returns mm:ss within the running recording, or "".
func videoOffset(rec *media.Recorder) string {
	if rec == nil {
		return ""
	}
	st := rec.Status()
	active, _ := st["recording"].(bool)
	if !active {
		return ""
	}
	secs, _ := st["seconds"].(int)
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}

func nowStamp() string { return time.Now().Format("2006-01-02T15:04:05.000Z07:00") }
