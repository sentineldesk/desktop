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

// The catalogue's own consistency check.
//
// NewServer already refuses to start on an unclassified tool, but a startup
// error is found by whoever runs the desktop next, which may be a user. These
// tests find it at `make test`, on a machine with no X display and no
// GStreamer pipeline, because buildTools only assembles literals — the one part
// of this program that can be exercised without a desktop underneath it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sentineldesk/desktop/pkg/config"
)

// catalogue builds the tool list the way NewServer does, without needing any of
// the desktop plumbing a real Server holds.
func catalogue(t *testing.T) []toolDef {
	t.Helper()
	tools := (&Server{}).buildTools()
	if len(tools) == 0 {
		t.Fatal("buildTools returned nothing")
	}
	return tools
}

// TestEveryToolIsClassified is the check this whole refactor exists for. Before
// it, risk lived in two maps in another file and 46 of 114 tools were in
// neither — silently refused under readonly and silently allowed under safe.
func TestEveryToolIsClassified(t *testing.T) {
	if err := validateCatalogue(catalogue(t)); err != nil {
		t.Fatalf("%v", err)
	}
}

// TestPolicyLevelsFollowRisk pins the meaning of the levels to the
// classification, so that a tool reclassified by accident shows up here rather
// than as a permission somebody did not intend to grant. The `approve` level
// has its own file — approval_test.go — because its meaning is a flow, not a
// filter.
func TestPolicyLevelsFollowRisk(t *testing.T) {
	tools := catalogue(t)
	idx := buildRiskIndex(tools)

	readonly := &Policy{level: "readonly", risk: idx}
	safe := &Policy{level: "safe", risk: idx}
	full := &Policy{level: "full", risk: idx}

	for _, tool := range tools {
		gotRO, _ := readonly.Allowed(tool.Name, nil)
		if want := tool.Risk == riskRead; gotRO != want {
			t.Errorf("readonly allowed %s = %v, want %v (risk %s)", tool.Name, gotRO, want, tool.Risk)
		}
		gotSafe, _ := safe.Allowed(tool.Name, nil)
		if want := tool.Risk != riskDanger; gotSafe != want {
			t.Errorf("safe allowed %s = %v, want %v (risk %s)", tool.Name, gotSafe, want, tool.Risk)
		}
		if ok, why := full.Allowed(tool.Name, nil); !ok {
			t.Errorf("full refused %s: %s", tool.Name, why)
		}
	}
}

// TestUnknownToolIsRefusedBelowFull covers the fail-closed direction: a name
// that is not in the catalogue must not slip through the level checks.
func TestUnknownToolIsRefusedBelowFull(t *testing.T) {
	idx := buildRiskIndex(catalogue(t))
	for _, level := range []string{"readonly", "safe"} {
		p := &Policy{level: level, risk: idx}
		if ok, _ := p.Allowed("no_such_tool", nil); ok {
			t.Errorf("%s allowed a tool that does not exist", level)
		}
	}
}

// TestRestrictCarriesTheRiskIndex guards a failure that would look like a
// tightening and behave like a lockout: a restricted policy with no index
// refuses everything below full, including the reads it is supposed to permit.
func TestRestrictCarriesTheRiskIndex(t *testing.T) {
	idx := buildRiskIndex(catalogue(t))
	base := &Policy{level: "full", risk: idx}
	got := base.Restrict("readonly", "", "")
	if got.risk == nil {
		t.Fatal("Restrict dropped the risk index")
	}
	if ok, why := got.Allowed("screenshot", nil); !ok {
		t.Errorf("restricted readonly refused screenshot: %s", why)
	}
	if ok, _ := got.Allowed("run_command", nil); ok {
		t.Error("restricted readonly allowed run_command")
	}
}

// TestAnnotationsMatchRisk checks the wire form, since a host reading
// readOnlyHint is trusting it in the same way MCP_POLICY does.
func TestAnnotationsMatchRisk(t *testing.T) {
	for _, tool := range catalogue(t) {
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		var wire struct {
			Name        string `json:"name"`
			InputSchema struct {
				Type string `json:"type"`
			} `json:"inputSchema"`
			Annotations struct {
				ReadOnly    bool `json:"readOnlyHint"`
				Destructive bool `json:"destructiveHint"`
			} `json:"annotations"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if wire.Name != tool.Name {
			t.Errorf("marshalled name %q, want %q", wire.Name, tool.Name)
		}
		// The schema must survive the custom marshaller — it is the part a
		// model needs in order to call anything at all.
		if wire.InputSchema.Type != "object" {
			t.Errorf("%s: inputSchema did not survive marshalling", tool.Name)
		}
		if want := tool.Risk == riskRead; wire.Annotations.ReadOnly != want {
			t.Errorf("%s: readOnlyHint %v, want %v", tool.Name, wire.Annotations.ReadOnly, want)
		}
		if want := tool.Risk == riskDanger; wire.Annotations.Destructive != want {
			t.Errorf("%s: destructiveHint %v, want %v", tool.Name, wire.Annotations.Destructive, want)
		}
	}
}

// TestCoreToolsExist stops discovery mode from advertising a name that is not
// in the catalogue — which would leave a host with a tool it cannot call and no
// hint that the list was wrong rather than the desktop.
func TestCoreToolsExist(t *testing.T) {
	have := map[string]bool{}
	for _, tool := range catalogue(t) {
		have[tool.Name] = true
	}
	for name := range coreTools {
		if !have[name] {
			t.Errorf("coreTools lists %q, which is not a tool", name)
		}
	}
	if !coreTools["tool_search"] {
		t.Error("discovery mode without tool_search hides the catalogue with no way back")
	}
}

// TestInjectingToolsAreNotReadOnly cross-checks the two classifications that
// exist for different reasons: RequiresControl decides who may drive right now,
// Risk decides what the agent may ever do. Nothing that puts events into X can
// honestly be called read-only.
func TestInjectingToolsAreNotReadOnly(t *testing.T) {
	for _, tool := range catalogue(t) {
		if tool.RequiresControl && tool.Risk == riskRead {
			t.Errorf("%s requires control but is classified read", tool.Name)
		}
	}
}

// gatedBeforeTheRefactor is the switch statement that used to live in mcp.go,
// frozen here verbatim.
//
// Moving the list onto the toolDefs was a mechanical change and had to stay one:
// which tools the room arbitrates is a product decision about when an agent and
// a person collide, not a detail of where the list is stored. This is the proof
// that nothing was added or dropped on the way.
//
// Changing this set is allowed — it is not sacred — but it is a separate,
// deliberate act. Editing this list to make a failing test pass is the mistake
// it exists to catch.
var gatedBeforeTheRefactor = []string{
	"mouse_move", "mouse_click", "mouse_down", "mouse_up", "mouse_drag",
	"mouse_scroll", "type_text", "key_combo",
	// The four gamepad_* tools stood here until 2026-08-20, when the virtual
	// gamepad retired from the product (owner's decision) — removed from this
	// list because the tools no longer exist, which is the one edit this
	// list's warning does not forbid: retirement is not reclassification.
	"ui_click", "ui_set_text", "ui_focus", "fill_form", "terminal_run",
	// Types a credential into a field: keystrokes on the shared screen, so it
	// passes the same gate every other injection does.
	"type_secret",
	"start_restream", "stop_restream",
}

// gatedByTheSharedScreenDecision is the separate, deliberate act the comment
// above asks for, written down rather than folded into the frozen list.
//
// The gate used to mean "puts events into X". That drew the line in the right
// place for a keyboard and in the wrong place for everything else: with
// `you_have_control: false` it was possible to close somebody's window, move
// another, maximise it, switch their desktop and change the volume — five out
// of five, tried against a running desktop. None of those injects an event, and
// all of them are unmistakably driving a desktop somebody else is looking at.
// browser_click and browser_type are the sharpest case: they ARE keyboard and
// pointer input, routed through the DevTools protocol instead of XTEST, and the
// old rule let them through on a technicality.
//
// So the line is now "changes the shared screen", which is what visVisible
// already meant — the classification existed and nothing consulted it.
//
// Three visVisible tools are deliberately NOT here, and the reason is the same
// for all three: they are how an agent NEGOTIATES for the desktop rather than
// uses it. Gating request_control is a deadlock. Gating ask_human means the
// agent cannot ask permission without already holding what it is asking for.
// release_control is the way out, and a way out that can be locked is not one.
//
// The cost is real and was accepted: a flow that used to open a window without
// asking now spends one call on request_control, which is granted immediately
// when nobody is driving.
var gatedByTheSharedScreenDecision = []string{
	"launch_app", "open_app_and_wait", "terminal_open", "kill_process",
	"activate_window", "move_window", "resize_window", "close_window",
	"minimize_window", "maximize_window", "restore_window", "fullscreen_window",
	"window_set_state", "set_window_desktop", "switch_desktop",
	"set_volume", "set_resolution",
	"browser_open", "browser_goto", "browser_eval", "browser_click", "browser_type",

	// run_command and job_start joined later, and they are the case that
	// finished the argument rather than another instance of it.
	//
	// run_command was the counter-example the rule was written around: it
	// changed the machine without changing the screen, so "changes the shared
	// screen" left it out and it stayed ungated. That exemption stopped existing
	// when every command started running in a terminal window people can see.
	// The tool did not become more dangerous — it became observable, and a thing
	// that appears on somebody else's screen is a thing you ask for first.
	//
	// Which is the whole shape of it: the gate was never really about XTEST or
	// about danger. It is about whether an action lands in a space that is
	// shared. Making the desktop the only place work happens is what made the
	// answer uniform.
	"run_command", "job_start",

	// install_packages and remove_packages joined for exactly the reason the
	// paragraph above gives, and they are worth writing down separately because
	// they show how long the exemption survived after the argument was settled.
	//
	// run_command was moved onto the shared screen and gated; apt was not. So
	// the last hidden way to change a machine was not a shell tool at all — it
	// was the tool whose entire job is putting new software on somebody's
	// desktop. A user asked for gimp, got exit_code 0 in eight and a half
	// seconds, and saw an idle wallpaper the whole time.
	//
	// They now run in a job window like everything else, which settles the gate
	// by the stated rule rather than by a judgement about danger: they land in
	// a space that is shared, so they are asked for first.
	"install_packages", "remove_packages",

	// remote_open and remote_close join for the same stated reason, and they are
	// the sharpest example of it: a remote desktop is not a private side channel
	// like ssh_*, it is somebody else's machine drawn onto the screen the whole
	// room is watching. Opening it, and taking it away again, is driving the
	// shared desktop — so both are asked for first. remote_list and the
	// remote_profile_* tools are NOT here: reading the open sessions and managing
	// saved connection files changes nothing on the screen.
	"remote_open", "remote_close",
}

func TestControlGateParity(t *testing.T) {
	want := map[string]bool{}
	for _, name := range gatedBeforeTheRefactor {
		want[name] = true
	}
	for _, name := range gatedByTheSharedScreenDecision {
		want[name] = true
	}

	got := map[string]bool{}
	for _, tool := range catalogue(t) {
		if tool.RequiresControl {
			got[tool.Name] = true
		}
	}

	for name := range want {
		if !got[name] {
			t.Errorf("%s was gated before the refactor and is not now", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("%s is gated now and was not before the refactor", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("gated %d tools, want %d", len(got), len(want))
	}
}

// TestServerGateReadsTheCatalogue closes the loop: handleToolCall asks
// s.injectsInput, so the index has to agree with the field it was built from.
func TestServerGateReadsTheCatalogue(t *testing.T) {
	s := testServer(t)
	for _, tool := range s.tools {
		if s.injectsInput(tool.Name) != tool.RequiresControl {
			t.Errorf("%s: gate says %v, toolDef says %v",
				tool.Name, s.injectsInput(tool.Name), tool.RequiresControl)
		}
	}
	if s.injectsInput("no_such_tool") {
		t.Error("the gate claimed a tool that does not exist needs control")
	}
}

// TestRequiresControlIsPublished is the point of the whole change: a client has
// to be able to learn this from tools/list instead of carrying its own copy.
func TestRequiresControlIsPublished(t *testing.T) {
	for _, tool := range catalogue(t) {
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		var wire struct {
			Annotations struct {
				RequiresControl bool `json:"sentineldesk/requiresControl"`
			} `json:"annotations"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if wire.Annotations.RequiresControl != tool.RequiresControl {
			t.Errorf("%s: published %v, want %v",
				tool.Name, wire.Annotations.RequiresControl, tool.RequiresControl)
		}
	}
}

// TestRiskDoesNotImplyControl records why the annotation is needed at all: a
// client cannot derive the room gate from the risk level, in either direction.
func TestRiskDoesNotImplyControl(t *testing.T) {
	byName := map[string]toolDef{}
	for _, tool := range catalogue(t) {
		byName[tool.Name] = tool
	}
	for _, c := range []struct {
		gated, notGated string
		risk            riskLevel
	}{
		// set_clipboard rather than set_volume, which used to stand here and is
		// now gated: changing what everyone HEARS turned out to be the same kind
		// of act as changing what everyone sees. The pair still makes the point,
		// and makes it better — both of these write to the desktop, and the one
		// that does it where nobody can see is the one that goes ungated.
		{"ui_click", "set_clipboard", riskWrite},
		{"start_restream", "write_file", riskDanger},
	} {
		a, b := byName[c.gated], byName[c.notGated]
		if a.Risk != c.risk || b.Risk != c.risk {
			t.Fatalf("%s and %s no longer share risk %s", c.gated, c.notGated, c.risk)
		}
		if !a.RequiresControl {
			t.Errorf("%s should require control", c.gated)
		}
		if b.RequiresControl {
			t.Errorf("%s should not require control", c.notGated)
		}
	}
}

func TestToolSearchFindsTheObviousThings(t *testing.T) {
	tools := catalogue(t)
	cases := []struct {
		query string
		want  string
	}{
		{"give someone remote access over ssh", "ssh_"},
		{"read the text on the screen", "read_screen_text"},
		{"take a screenshot", "screenshot"},
		{"open a tunnel", "ssh_tunnel"},
		{"click a button by name", "ui_"},
		{"install a package", "install_packages"},
	}
	for _, c := range cases {
		hits := searchTools(tools, c.query, 10)
		if len(hits) == 0 {
			t.Errorf("%q matched nothing", c.query)
			continue
		}
		found := false
		for _, h := range hits {
			if len(h.Name) >= len(c.want) && h.Name[:len(c.want)] == c.want {
				found = true
				break
			}
		}
		if !found {
			names := make([]string, len(hits))
			for i, h := range hits {
				names[i] = h.Name
			}
			t.Errorf("%q did not surface %s* in the top %d: %v", c.query, c.want, len(hits), names)
		}
	}
}

func TestToolSearchRespectsTheLimit(t *testing.T) {
	hits := searchTools(catalogue(t), "window", 3)
	if len(hits) > 3 {
		t.Errorf("limit 3 returned %d", len(hits))
	}
	// The schema has to come back with the hit: the point of searching is to be
	// able to call what you found without a second round trip.
	for _, h := range hits {
		if len(h.InputSchema) == 0 {
			t.Errorf("%s came back without its schema", h.Name)
		}
	}
}

// --- over the wire -------------------------------------------------------------
//
// The tests above check the catalogue as data. These check the path a real AI
// host takes to reach it — serve() reading JSON-RPC off a socket, the policy
// filter on tools/list, the discovery filter on top of it, and a tool_search
// call going through dispatch. None of it touches X, so it runs anywhere.

// session drives one Server over an in-memory connection.
type session struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	id   int
}

func newSession(t *testing.T, s *Server) *session {
	t.Helper()
	client, server := net.Pipe()
	go s.serve(server)
	t.Cleanup(func() { client.Close() })
	return &session{t: t, conn: client, r: bufio.NewReader(client)}
}

// send writes a request and returns its id without waiting for the answer, so a
// test can send something else — a cancellation — while it is still running.
func (c *session) send(method string, params any) int {
	c.t.Helper()
	c.id++
	req := map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method}
	if params != nil {
		req["params"] = params
	}
	c.write(req, method)
	return c.id
}

// notify writes a request with no id, which is what a notification is.
func (c *session) notify(method string, params any) {
	c.t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	c.write(req, method)
}

func (c *session) write(req map[string]any, method string) {
	c.t.Helper()
	line, err := json.Marshal(req)
	if err != nil {
		c.t.Fatalf("%v", err)
	}
	_ = c.conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		c.t.Fatalf("write %s: %v", method, err)
	}
}

// readFull returns the response id alongside the result, so a test can prove a
// reply belongs to the request it thinks it does.
func (c *session) readFull() (int, map[string]any) {
	c.t.Helper()
	_ = c.conn.SetDeadline(time.Now().Add(20 * time.Second))
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var resp struct {
		ID     int            `json:"id"`
		Result map[string]any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		c.t.Fatalf("%s", resp.Error.Message)
	}
	return resp.ID, resp.Result
}

func (c *session) read() map[string]any {
	c.t.Helper()
	_, res := c.readFull()
	return res
}

func (c *session) call(method string, params any) map[string]any {
	c.t.Helper()
	c.send(method, params)
	return c.read()
}

func (c *session) listedNames() []string {
	c.t.Helper()
	raw, _ := json.Marshal(c.call("tools/list", nil)["tools"])
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		c.t.Fatalf("%v", err)
	}
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}

// testServer builds a real Server with none of the desktop attached. Nothing in
// construction or in the tools exercised here reaches X.
func testServer(t *testing.T) *Server {
	t.Helper()
	return NewServer(config.Config{Display: ":99"}, nil, nil, nil)
}

func TestToolsListAdvertisesTheWholeCatalogue(t *testing.T) {
	s := testServer(t)
	got := newSession(t, s).listedNames()
	if len(got) != len(s.tools) {
		t.Fatalf("tools/list returned %d, catalogue has %d", len(got), len(s.tools))
	}
}

// TestDiscoveryHidesButDoesNotForbid is the invariant the whole discovery mode
// rests on. If a hidden tool ever stopped being callable, discovery would have
// silently become a permission system — one nobody wrote, audited or logged.
func TestDiscoveryHidesButDoesNotForbid(t *testing.T) {
	s := testServer(t)
	s.discovery = true
	c := newSession(t, s)

	listed := c.listedNames()
	if len(listed) >= len(s.tools) {
		t.Fatalf("discovery listed %d of %d tools", len(listed), len(s.tools))
	}
	for _, name := range listed {
		if !coreTools[name] {
			t.Errorf("discovery advertised %s, which is not in the core set", name)
		}
	}

	// get_screen_info is deliberately not in the core set. It must still run.
	if coreTools["get_screen_info"] {
		t.Fatal("this test needs a tool outside the core set")
	}
	res := c.call("tools/call", map[string]any{
		"name": "tool_search", "arguments": map[string]any{"query": "screen resolution"},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("tool_search failed: %v", res["content"])
	}
}

func TestToolSearchOverTheWireReturnsSchemas(t *testing.T) {
	c := newSession(t, testServer(t))
	res := c.call("tools/call", map[string]any{
		"name":      "tool_search",
		"arguments": map[string]any{"category": "ssh", "limit": 5},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("tool_search reported an error: %v", res["content"])
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatal("tool_search returned no content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var payload struct {
		Matched int `json:"matched"`
		Tools   []struct {
			Name        string          `json:"name"`
			Category    string          `json:"category"`
			Risk        string          `json:"risk"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool_search did not return JSON: %v\n%s", err, text)
	}
	if payload.Matched == 0 {
		t.Fatal("category ssh matched nothing")
	}
	for _, tool := range payload.Tools {
		if tool.Category != "ssh" {
			t.Errorf("category ssh returned %s (%s)", tool.Name, tool.Category)
		}
		if tool.Risk == "" || tool.Risk == "unclassified" {
			t.Errorf("%s came back with risk %q", tool.Name, tool.Risk)
		}
		if len(tool.InputSchema) == 0 {
			t.Errorf("%s came back without its schema", tool.Name)
		}
	}
}

// TestReadonlyConnectionSearchesOnlyWhatItMayCall covers the reason tool_search
// takes the connection's policy: surfacing a tool that will then be refused is
// a worse answer than surfacing nothing.
func TestReadonlyConnectionSearchesOnlyWhatItMayCall(t *testing.T) {
	c := newSession(t, testServer(t))
	c.call("sentineldesk/policy", map[string]any{"level": "readonly"})

	res := c.call("tools/call", map[string]any{
		"name":      "tool_search",
		"arguments": map[string]any{"query": "run a command in a shell"},
	})
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)

	var payload struct {
		Tools []struct {
			Name string `json:"name"`
			Risk string `json:"risk"`
		} `json:"tools"`
	}
	// No match at all is a valid answer here; an unparseable one is not.
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return
	}
	for _, tool := range payload.Tools {
		if tool.Risk != "read" {
			t.Errorf("a readonly connection was offered %s (risk %s)", tool.Name, tool.Risk)
		}
	}
}

// --- denial kinds ----------------------------------------------------------------
//
// The sentence a caller gets back is written for a model to read and gets
// reworded whenever a better one is found. These pin the machine-readable half,
// which is the part a runtime branches on: policy is final, room means ask a
// person and retry, tool_error may be worth retrying. Getting them confused
// turns "wait your turn" into "give up".

// roomWithoutControls is a Rooms that never grants control, so the gate refuses.
// Only the three methods mayInject touches do anything.
type roomWithoutControls struct{ Rooms }

func (roomWithoutControls) JoinAgent(string) string      { return AgentID }
func (roomWithoutControls) LeaveAgent()                  {}
func (roomWithoutControls) IsController(string) bool     { return false }
func (roomWithoutControls) Controller() (string, string) { return "someone", "Viewer 1" }

// denialOf calls a tool and returns the kind reported alongside the content.
// An empty string means the call succeeded.
func (c *session) denialOf(name string, args map[string]any) string {
	c.t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	res := c.call("tools/call", params)

	isErr, _ := res["isError"].(bool)
	meta, hasMeta := res["_meta"].(map[string]any)
	if !isErr {
		if hasMeta {
			c.t.Errorf("%s succeeded but carried _meta %v", name, meta)
		}
		return ""
	}
	if !hasMeta {
		c.t.Fatalf("%s failed with no _meta to say why", name)
	}
	kind, _ := meta["sentineldesk/denial"].(string)
	if kind == "" {
		c.t.Fatalf("%s: _meta has no denial kind: %v", name, meta)
	}
	return kind
}

func TestDenialKindUnknownTool(t *testing.T) {
	c := newSession(t, testServer(t))
	if got := c.denialOf("no_such_tool", nil); got != string(denialUnknown) {
		t.Errorf("kind %q, want %q", got, denialUnknown)
	}
}

// TestDenialKindUnknownToolAtEveryLevel is why the catalogue is checked before
// policy. The same nonexistent name used to come back as a policy refusal under
// safe and an unknown tool under full — two answers to one question.
func TestDenialKindUnknownToolAtEveryLevel(t *testing.T) {
	for _, level := range []string{"full", "safe", "readonly"} {
		t.Run(level, func(t *testing.T) {
			c := newSession(t, testServer(t))
			c.call("sentineldesk/policy", map[string]any{"level": level})
			if got := c.denialOf("no_such_tool", nil); got != string(denialUnknown) {
				t.Errorf("kind %q, want %q", got, denialUnknown)
			}
		})
	}
}

// TestAConnectionCannotWidenItself is the invariant the whole restriction
// mechanism exists for, checked where it is actually used rather than on the
// method in isolation.
//
// Restrict itself was always correct. serve called it on the DAEMON's policy
// every time, so each request started afresh at the ceiling and a connection
// that had dropped itself to readonly could ask for full and be given it. The
// unit test for Restrict passed throughout; it took a live check against a real
// desktop to notice, because the bug was in the caller.
func TestAConnectionCannotWidenItself(t *testing.T) {
	c := newSession(t, testServer(t))

	applied := c.call("sentineldesk/policy", map[string]any{"level": "readonly"})
	if applied["level"] != "readonly" {
		t.Fatalf("restricting to readonly gave %v", applied["level"])
	}

	for _, level := range []string{"full", "safe"} {
		applied = c.call("sentineldesk/policy", map[string]any{"level": level})
		if applied["level"] != "readonly" {
			t.Errorf("asking for %s from readonly gave %v", level, applied["level"])
		}
	}
	// And the ceiling really is enforced, not just reported.
	if got := c.denialOf("run_command", map[string]any{"command": "true"}); got != string(denialPolicy) {
		t.Errorf("run_command after asking for full: kind %q, want %q", got, denialPolicy)
	}
}

// TestRestrictionsAccumulate: denials add up across calls rather than replacing
// each other, which is the same monotonicity from the other direction.
func TestRestrictionsAccumulate(t *testing.T) {
	c := newSession(t, testServer(t))
	c.call("sentineldesk/policy", map[string]any{"deny": "ui_*"})
	c.call("sentineldesk/policy", map[string]any{"deny": "browser_*"})

	if got := c.denialOf("ui_tree", nil); got != string(denialPolicy) {
		t.Errorf("the first denial was forgotten: ui_tree kind %q", got)
	}
	if got := c.denialOf("browser_tabs", nil); got != string(denialPolicy) {
		t.Errorf("the second denial did not take: browser_tabs kind %q", got)
	}
}

// TestDenialKindPolicy also proves the separation holds the other way: a tool
// that exists but is hidden by the level reports policy, not unknown_tool.
func TestDenialKindPolicy(t *testing.T) {
	c := newSession(t, testServer(t))
	c.call("sentineldesk/policy", map[string]any{"level": "readonly"})
	if got := c.denialOf("run_command", map[string]any{"command": "true"}); got != string(denialPolicy) {
		t.Errorf("kind %q, want %q", got, denialPolicy)
	}
}

func TestDenialKindPolicyFromDenyList(t *testing.T) {
	c := newSession(t, testServer(t))
	c.call("sentineldesk/policy", map[string]any{"deny": "ui_*"})
	if got := c.denialOf("ui_tree", nil); got != string(denialPolicy) {
		t.Errorf("kind %q, want %q", got, denialPolicy)
	}
}

// TestDenialKindRoom is the one the agent loop most needs to tell apart: it
// means ask a person and try again, not give up.
func TestDenialKindRoom(t *testing.T) {
	s := testServer(t)
	s.SetRoom(roomWithoutControls{}, "AI agent")
	c := newSession(t, s)

	got := c.denialOf("mouse_move", map[string]any{"x": 10, "y": 10})
	if got != string(denialRoom) {
		t.Errorf("kind %q, want %q", got, denialRoom)
	}

	// A tool that does not need the controls is unaffected by the same room.
	if got := c.denialOf("tool_search", map[string]any{"query": "screen"}); got != "" {
		t.Errorf("tool_search was refused with kind %q", got)
	}
}

// TestDenialKindOrder pins the precedence. A gated tool that policy already
// refuses must report policy: the room question never arises, because the call
// was not going to happen either way.
func TestDenialKindOrder(t *testing.T) {
	s := testServer(t)
	s.SetRoom(roomWithoutControls{}, "AI agent")
	c := newSession(t, s)
	c.call("sentineldesk/policy", map[string]any{"level": "readonly"})

	// mouse_move requires control AND is refused by readonly.
	if got := c.denialOf("mouse_move", map[string]any{"x": 10, "y": 10}); got != string(denialPolicy) {
		t.Errorf("kind %q, want %q — policy outranks the room gate", got, denialPolicy)
	}
}

// TestDenialKindIsLogged keeps the audit trail machine-readable too: the reason
// a call was refused should not have to be recovered from prose there either.
func TestDenialKindIsLogged(t *testing.T) {
	s := testServer(t)
	c := newSession(t, s)
	c.denialOf("no_such_tool", nil)

	entries := s.actions.Tail(1, "")
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	if entries[0].Kind != string(denialUnknown) {
		t.Errorf("logged kind %q, want %q", entries[0].Kind, denialUnknown)
	}
	if entries[0].Denied == "" {
		t.Error("logged a kind with no human reason beside it")
	}
	if entries[0].OK {
		t.Error("a refused call was logged as OK")
	}
}

// TestNoRoomDoesNotKillTheCatalogue covers a regression found while adding the
// denial kinds: callRoom claimed every tool name when SetRoom had not been
// called, so a Server without a room answered "this build has no room attached"
// to the entire catalogue. The daemon always calls SetRoom, so it never showed
// there — it showed the moment anything else embedded the server.
func TestNoRoomDoesNotKillTheCatalogue(t *testing.T) {
	s := testServer(t)
	if s.room != nil {
		t.Fatal("this test needs a Server with no room")
	}
	c := newSession(t, s)

	// `wait` only sleeps, so it needs no display and must simply succeed. It
	// also sits in the main switch, AFTER callRoom in the dispatch chain — the
	// tools handled before callRoom never reached the bug and would pass this
	// test either way.
	res := c.call("tools/call", map[string]any{
		"name": "wait", "arguments": map[string]any{"ms": 1},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Errorf("wait failed with no room attached: %v", res["content"])
	}

	// The room tools themselves still report the missing room, as before.
	if got := c.denialOf("room_state", nil); got != string(denialToolError) {
		t.Errorf("room_state kind %q, want %q", got, denialToolError)
	}
}

func TestSuccessCarriesNoDenial(t *testing.T) {
	c := newSession(t, testServer(t))
	if got := c.denialOf("tool_search", map[string]any{"query": "take a screenshot"}); got != "" {
		t.Errorf("a successful call reported kind %q", got)
	}
}

// --- cancellation ------------------------------------------------------------

func TestInflightBookkeeping(t *testing.T) {
	f := newInflight()
	_, c1 := context.WithCancel(context.Background())
	ctx2, c2 := context.WithCancel(context.Background())

	var answers int
	note := func(map[string]any) { answers++ }
	f.add("1", c1, note)
	f.add("2", c2, note)

	if !f.cancel("1", "stop") {
		t.Error("cancel reported nothing to cancel")
	}
	// Cancelling answers as well as stopping: the client is waiting and the
	// tool may not be listening.
	if answers != 1 {
		t.Errorf("cancel produced %d answers, want 1", answers)
	}
	// A second cancel for the same id is the normal race with a call that has
	// just finished, and must not be treated as an error or answered twice.
	if f.cancel("1", "stop") {
		t.Error("cancel reported a second stop for the same id")
	}
	if f.cancel("nope", "stop") {
		t.Error("cancel reported stopping a request that never existed")
	}
	if answers != 1 {
		t.Errorf("stray cancels produced %d answers, want 1", answers)
	}

	f.cancelAll("connection closed")
	select {
	case <-ctx2.Done():
	default:
		t.Error("cancelAll left a call running")
	}
	if answers != 2 {
		t.Errorf("cancelAll produced %d answers in total, want 2", answers)
	}
	if len(f.calls) != 0 {
		t.Errorf("cancelAll left %d entries behind", len(f.calls))
	}
}

// TestDoneDoesNotAnswer: a call that finishes normally is answered by its own
// handler. If done() answered as well, every successful call would produce two
// responses for one id.
func TestDoneDoesNotAnswer(t *testing.T) {
	f := newInflight()
	_, cancel := context.WithCancel(context.Background())
	answered := false
	f.add("1", cancel, func(map[string]any) { answered = true })
	f.done("1")
	if answered {
		t.Error("done answered a call the handler was about to answer itself")
	}
	if len(f.calls) != 0 {
		t.Error("done left the entry behind")
	}
}

// TestRequestKeyKeepsTypesApart: JSON-RPC allows a string or a number id and
// they are different requests, so cancelling id "2" must not stop id 2.
func TestRequestKeyKeepsTypesApart(t *testing.T) {
	if requestKey(json.RawMessage(`2`)) == requestKey(json.RawMessage(`"2"`)) {
		t.Error("id 2 and id \"2\" share a key")
	}
	if requestKey(json.RawMessage(" 7 ")) != requestKey(json.RawMessage("7")) {
		t.Error("whitespace changed the key")
	}
}

// TestCancelStopsARunningCommand is the one that matters: before this, closing
// a client or cancelling a run left the work running, because dispatch took no
// context and every tool that needed a deadline built one from Background.
//
// The command touches a file and then sleeps. Waiting for the file means the
// process really started and the call is really registered, so the cancellation
// cannot arrive too early — the alternative is a sleep and a flaky test.
// requireSupervisedShell skips a test that needs a command to actually run.
//
// tmux is a hard dependency of run_command now, not an implementation choice it
// happens to make: the command runs in a window on the shared screen because
// that is the promise, and a machine with no tmux cannot keep it. So these tests
// stopped being runnable everywhere, which is a real narrowing of what a green
// `go test ./...` means on a bare host and is recorded here rather than papered
// over. In the container — the only place the daemon actually runs — tmux is
// installed and they run.
func requireSupervisedShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux: commands run in a terminal window now, and there is none to open")
	}
}

func TestCancelStopsARunningCommand(t *testing.T) {
	requireSupervisedShell(t)
	started := filepath.Join(t.TempDir(), "started")
	c := newSession(t, testServer(t))

	id := c.send("tools/call", map[string]any{
		"name": "run_command",
		"arguments": map[string]any{
			"command":    "touch " + started + "; sleep 30",
			"timeout_ms": 30000,
		},
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	begin := time.Now()
	c.notify("notifications/cancelled", map[string]any{
		"requestId": id, "reason": "test",
	})

	res := c.read()
	elapsed := time.Since(begin)

	// The command was going to sleep for thirty seconds. Coming back at all is
	// the result; coming back quickly is what says the process was killed
	// rather than waited out. The couple of seconds it does take are
	// cmd.WaitDelay, not the sleep.
	if elapsed > 10*time.Second {
		t.Errorf("the call took %v after cancelling", elapsed)
	}

	// And it has to SAY it was cancelled. The first version of this passed the
	// timing check and still reported success: a killed process is just a
	// process with a non-zero exit status, so run_command answered
	// {"exit_code": -1} with no error, which is true about the process and a
	// lie about the request.
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("a cancelled command reported success: %v", res["content"])
	}
	meta, _ := res["_meta"].(map[string]any)
	if kind, _ := meta["sentineldesk/denial"].(string); kind != string(denialCancelled) {
		t.Errorf("kind %q, want %q", kind, denialCancelled)
	}
}

// TestCancelAnswersWithoutWaitingForTheTool is the point of the second half of
// this work. handleToolCall blocks on dispatch, so a tool that is not listening
// to its context used to hold the response back until it finished — the client
// asked to stop and then waited out the full duration to be told it had, unable
// to tell "still stopping" from "ignored you".
//
// A thirty-second wait, cancelled immediately, has to come back immediately.
func TestCancelAnswersWithoutWaitingForTheTool(t *testing.T) {
	c := newSession(t, testServer(t))
	id := c.send("tools/call", map[string]any{
		"name": "wait", "arguments": map[string]any{"ms": 30000},
	})

	begin := time.Now()
	c.notify("notifications/cancelled", map[string]any{
		"requestId": id, "reason": "user pressed stop",
	})
	gotID, res := c.readFull()
	elapsed := time.Since(begin)

	if gotID != id {
		t.Errorf("answered request %d, want %d", gotID, id)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the cancellation took %v to be acknowledged", elapsed)
	}
	meta, _ := res["_meta"].(map[string]any)
	if kind, _ := meta["sentineldesk/denial"].(string); kind != string(denialCancelled) {
		t.Errorf("kind %q, want %q", kind, denialCancelled)
	}
	// The client's reason comes back, because the model reading the transcript
	// was not the one that pressed stop.
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if text, _ := first["text"].(string); !strings.Contains(text, "user pressed stop") {
		t.Errorf("the reason did not survive: %q", text)
	}

	// And exactly one response for that id. The handler goroutine wakes up
	// afterwards and tries to reply too; if that got through, this next call
	// would read the stale answer instead of its own.
	pingID := c.send("ping", nil)
	if gotID, _ := c.readFull(); gotID != pingID {
		t.Errorf("a second response arrived for the cancelled call (id %d)", gotID)
	}
}

// TestCancelUnknownRequestIsHarmless: a cancellation that names a request which
// already finished is a normal race, not a reason to break the connection.
func TestCancelUnknownRequestIsHarmless(t *testing.T) {
	c := newSession(t, testServer(t))
	c.notify("notifications/cancelled", map[string]any{"requestId": 999, "reason": "nothing"})
	c.notify("notifications/cancelled", nil)

	// The connection still works.
	if got := c.denialOf("no_such_tool", nil); got != string(denialUnknown) {
		t.Errorf("kind %q after a stray cancellation, want %q", got, denialUnknown)
	}
}

// --- connection identity -------------------------------------------------------

// initialize announces a client and returns the connection id the server gave
// it. The id has to come back to the client, or nothing that supervises it can
// name the connection it wants stopped.
func (c *session) initialize(name string) uint64 {
	c.t.Helper()
	res := c.call("initialize", map[string]any{
		"clientInfo": map[string]any{"name": name, "version": "1.0"},
	})
	meta, ok := res["_meta"].(map[string]any)
	if !ok {
		c.t.Fatal("initialize returned no _meta, so the client cannot learn its id")
	}
	id, ok := meta["sentineldesk/connectionId"].(float64)
	if !ok {
		c.t.Fatalf("no connection id in %v", meta)
	}
	return uint64(id)
}

func TestConnectionsAreNumberedAndNamed(t *testing.T) {
	s := testServer(t)
	a := newSession(t, s)
	b := newSession(t, s)

	idA := a.initialize("agent-runtime")
	idB := b.initialize("claude-code")
	if idA == idB {
		t.Fatalf("both connections got id %d", idA)
	}

	// The name and the number both reach the audit trail. Without them every
	// entry reads "the agent did this", which stops being a useful sentence the
	// moment a runtime fans out across several connections.
	a.call("tools/call", map[string]any{"name": "wait", "arguments": map[string]any{"ms": 1}})
	entries := s.actions.Tail(1, "")
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	if entries[0].Conn != idA {
		t.Errorf("logged connection %d, want %d", entries[0].Conn, idA)
	}
	if entries[0].Client != "agent-runtime 1.0" {
		t.Errorf("logged client %q", entries[0].Client)
	}
}

// TestHaltStopsOneConnectionOnly is the whole point of the identity. An
// emergency stop for the agent runtime must not stop an operator's own MCP
// session, and it must not stop the desktop.
func TestHaltStopsOneConnectionOnly(t *testing.T) {
	s := testServer(t)
	agent := newSession(t, s)
	operator := newSession(t, s)

	agentID := agent.initialize("agent-runtime")
	operator.initialize("claude-code")

	s.HaltConnection(agentID, "emergency stop")

	if got := agent.denialOf("wait", map[string]any{"ms": 1}); got != string(denialEmergency) {
		t.Errorf("halted connection got kind %q, want %q", got, denialEmergency)
	}
	if got := operator.denialOf("wait", map[string]any{"ms": 1}); got != "" {
		t.Errorf("the other connection was refused with kind %q", got)
	}

	s.ResumeConnection(agentID)
	if got := agent.denialOf("wait", map[string]any{"ms": 1}); got != "" {
		t.Errorf("resume did not lift the halt: kind %q", got)
	}
}

// TestHaltOutranksEverything: a halted connection is not being told about the
// catalogue, it is being told to stop. Answering "unknown tool" first would let
// a client that is supposed to be doing nothing map what exists.
func TestHaltOutranksEverything(t *testing.T) {
	s := testServer(t)
	c := newSession(t, s)
	id := c.initialize("agent-runtime")
	s.HaltConnection(id, "emergency stop")

	for _, name := range []string{"no_such_tool", "run_command", "wait"} {
		if got := c.denialOf(name, nil); got != string(denialEmergency) {
			t.Errorf("%s: kind %q, want %q", name, got, denialEmergency)
		}
	}
}

func TestHaltIsLoggedWithItsConnection(t *testing.T) {
	s := testServer(t)
	c := newSession(t, s)
	id := c.initialize("agent-runtime")
	s.HaltConnection(id, "emergency stop: operator")
	c.denialOf("wait", map[string]any{"ms": 1})

	entries := s.actions.Tail(1, "")
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	if entries[0].Kind != string(denialEmergency) {
		t.Errorf("logged kind %q, want %q", entries[0].Kind, denialEmergency)
	}
	if entries[0].Conn != id {
		t.Errorf("logged connection %d, want %d", entries[0].Conn, id)
	}
	if !strings.Contains(entries[0].Denied, "operator") {
		t.Errorf("the reason did not reach the log: %q", entries[0].Denied)
	}
}

// TestUnhaltedConnectionsNeedNoInitialize: a client that never sends
// clientInfo still gets an id and still works. The name is nice to have; the
// number is what the halt needs.
func TestConnectionWorksWithoutClientInfo(t *testing.T) {
	c := newSession(t, testServer(t))
	if got := c.denialOf("wait", map[string]any{"ms": 1}); got != "" {
		t.Errorf("a connection that never introduced itself was refused: %q", got)
	}
}

// --- progress --------------------------------------------------------------------

// readMessage returns the whole outbound message, so a test can look at
// notifications as well as answers.
func (c *session) readMessage() map[string]any {
	c.t.Helper()
	_ = c.conn.SetDeadline(time.Now().Add(20 * time.Second))
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(raw, &msg); err != nil {
		c.t.Fatalf("decode: %v", err)
	}
	return msg
}

// callCollecting runs a tool and returns its result plus every notification
// that arrived first.
func (c *session) callCollecting(params map[string]any) (map[string]any, []map[string]any) {
	c.t.Helper()
	c.send("tools/call", params)
	var notes []map[string]any
	for {
		msg := c.readMessage()
		if _, isResponse := msg["result"]; isResponse {
			res, _ := msg["result"].(map[string]any)
			return res, notes
		}
		if _, isErr := msg["error"]; isErr {
			c.t.Fatalf("error response: %v", msg["error"])
		}
		notes = append(notes, msg)
	}
}

func TestProgressReachesTheClientWhileACommandRuns(t *testing.T) {
	requireSupervisedShell(t)
	restore := progressInterval
	progressInterval = 50 * time.Millisecond
	t.Cleanup(func() { progressInterval = restore })

	c := newSession(t, testServer(t))
	res, notes := c.callCollecting(map[string]any{
		"name": "run_command",
		"arguments": map[string]any{
			"command":    "echo first; sleep 0.4; echo second; sleep 0.2",
			"timeout_ms": 10000,
		},
		"_meta": map[string]any{"progressToken": "tok-1"},
	})

	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("the command failed: %v", res["content"])
	}
	if len(notes) == 0 {
		t.Fatal("a command that ran for most of a second sent no progress at all")
	}

	sawToken, sawOutput := false, false
	for _, n := range notes {
		if n["method"] != "notifications/progress" {
			t.Errorf("unexpected notification %v", n["method"])
			continue
		}
		params, _ := n["params"].(map[string]any)
		if params["progressToken"] == "tok-1" {
			sawToken = true
		}
		if _, ok := params["progress"]; !ok {
			t.Error("a progress notification with no progress in it")
		}
		// The command's own output is the only honest progress it has, so it
		// should be what the message carries.
		if msg, _ := params["message"].(string); strings.Contains(msg, "first") ||
			strings.Contains(msg, "second") {
			sawOutput = true
		}
	}
	if !sawToken {
		t.Error("the client's own token did not come back")
	}
	if !sawOutput {
		t.Errorf("no notification carried the command's output: %v", notes)
	}
}

// TestNoProgressWithoutAToken: a client that did not ask must not be given a
// stream of messages it has to discard.
func TestNoProgressWithoutAToken(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell available")
	}
	restore := progressInterval
	progressInterval = 50 * time.Millisecond
	t.Cleanup(func() { progressInterval = restore })

	c := newSession(t, testServer(t))
	_, notes := c.callCollecting(map[string]any{
		"name": "run_command",
		"arguments": map[string]any{
			"command": "echo hello; sleep 0.4", "timeout_ms": 10000,
		},
	})
	if len(notes) != 0 {
		t.Errorf("sent %d notifications to a client that asked for none: %v", len(notes), notes)
	}
}

func TestProgressTokenParsing(t *testing.T) {
	for _, tc := range []struct {
		name, params, want string
	}{
		{"string", `{"_meta":{"progressToken":"abc"}}`, `"abc"`},
		{"number", `{"_meta":{"progressToken":7}}`, `7`},
		{"absent", `{"name":"wait"}`, ""},
		{"null", `{"_meta":{"progressToken":null}}`, ""},
		{"no meta", `{}`, ""},
		{"malformed", `not json`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := progressToken(json.RawMessage(tc.params))
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", string(got), tc.want)
			}
		})
	}
}

func TestTailWriterKeepsTheLastLine(t *testing.T) {
	w := &tailWriter{}
	w.Write([]byte("one\ntwo\n"))
	if got := w.line(); got != "two" {
		t.Errorf("got %q, want %q", got, "two")
	}
	// A partial line is not reported until it is complete.
	w.Write([]byte("thr"))
	if got := w.line(); got != "two" {
		t.Errorf("a partial line was reported: %q", got)
	}
	w.Write([]byte("ee\n"))
	if got := w.line(); got != "three" {
		t.Errorf("got %q, want %q", got, "three")
	}
	// Blank lines do not overwrite the last real one.
	w.Write([]byte("\n   \n"))
	if got := w.line(); got != "three" {
		t.Errorf("a blank line overwrote the last one: %q", got)
	}
	// And a command that never emits a newline cannot grow the buffer forever.
	w.Write(bytes.Repeat([]byte("x"), 100_000))
	if len(w.buf) > 8192 {
		t.Errorf("buffer grew to %d bytes", len(w.buf))
	}
}

func TestEveryToolHasACategory(t *testing.T) {
	for _, tool := range catalogue(t) {
		if categoryOf(tool.Name) == "" {
			t.Errorf("%s has no category", tool.Name)
		}
	}
}

// --- argument validation ------------------------------------------------------

func TestEveryToolDeclaresItsArguments(t *testing.T) {
	// The index is built from the published schema, so a tool whose schema and
	// dispatcher disagree would start refusing arguments it actually reads.
	// This does not catch that — nothing static can — but it does catch a tool
	// whose schema failed to parse at all, which would silently accept
	// everything again.
	idx := buildArgIndex(catalogue(t))
	for _, tool := range catalogue(t) {
		if _, ok := idx[tool.Name]; !ok {
			t.Errorf("%s has no entry in the argument index", tool.Name)
		}
	}
}

func TestUnknownArgumentsAreNamed(t *testing.T) {
	idx := buildArgIndex(catalogue(t))

	// The case that motivated this: ui_tree takes depth, and max_depth was
	// accepted and ignored three calls running while the caller believed the
	// depth was changing.
	bad := idx.UnknownArgs("ui_tree", map[string]any{"max_depth": 1})
	if len(bad) != 1 || bad[0] != "max_depth" {
		t.Fatalf("unknownArgs = %v, want [max_depth]", bad)
	}
	if got := idx.UnknownArgs("ui_tree", map[string]any{"depth": 1}); len(got) != 0 {
		t.Fatalf("a declared argument was rejected: %v", got)
	}
}

func TestMetaIsNotAToolArgument(t *testing.T) {
	// _meta is the protocol's extension slot, not the tool's, and rejecting it
	// would break progress reporting for every tool at once.
	idx := buildArgIndex(catalogue(t))
	if got := idx.UnknownArgs("wait", map[string]any{"ms": 5, "_meta": map[string]any{}}); len(got) != 0 {
		t.Fatalf("_meta was treated as a tool argument: %v", got)
	}
}

func TestToolsWithNoArgumentsRefuseAll(t *testing.T) {
	// An empty schema means no arguments, not any argument. Defaulting the
	// other way would leave exactly the tools with the simplest contracts
	// accepting anything.
	idx := buildArgIndex(catalogue(t))
	got := idx.UnknownArgs("get_screen_info", map[string]any{"width": 100})
	if len(got) != 1 || got[0] != "width" {
		t.Fatalf("unknownArgs = %v, want [width]", got)
	}
}

func TestDeclaredListsWhatTheToolTakes(t *testing.T) {
	// The refusal quotes this, and a caller who is told only "bad argument"
	// has to go back to tools/list to find out which one they meant.
	idx := buildArgIndex(catalogue(t))
	names := idx.Declared("wait")
	if len(names) != 1 || names[0] != "ms" {
		t.Fatalf("declared(wait) = %v, want [ms]", names)
	}
}

// --- visibility -----------------------------------------------------------------
//
// The third axis, added for stage 2: will a person sharing the desktop see this
// happen? These tests exist for the same reason the Risk ones do — the field is
// only worth having if it cannot silently drift away from what the tools do.

// TestEveryToolHasAVisibility is the catalogue-wide completeness check.
// validateCatalogue enforces it at startup; this fails sooner and says more.
func TestEveryToolHasAVisibility(t *testing.T) {
	tools := (&Server{}).buildTools()
	var missing []string
	counts := map[string]int{}
	for _, tool := range tools {
		v := tool.EffectiveVisibility()
		if v == visUnset {
			missing = append(missing, tool.Name)
			continue
		}
		counts[v.String()]++
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d tool(s) that change something with no Visibility: %s",
			len(missing), strings.Join(missing, ", "))
	}
	t.Logf("hidden %d · visible %d · injects %d",
		counts["hidden"], counts["visible"], counts["injects"])
}

// TestInjectingToolsAreExactlyTheGatedOnes ties the new field to the old one.
//
// A tool that says it drives the desktop must hold the controls first, or it is
// typing into somebody else's session. The reverse is deliberately not required:
// start_restream and stop_restream are gated because they publish the desktop
// outward, which is a different reason from driving it.
func TestInjectingToolsAreExactlyTheGatedOnes(t *testing.T) {
	for _, tool := range (&Server{}).buildTools() {
		if tool.Visibility == visInjects && !tool.RequiresControl {
			t.Errorf("%s injects input and is not gated by the room", tool.Name)
		}
	}
}

// TestReadOnlyToolsAreHidden. A tool that observes and changes nothing cannot be
// seen changing something, so the two fields would be contradicting each other.
func TestReadOnlyToolsAreHidden(t *testing.T) {
	for _, tool := range (&Server{}).buildTools() {
		if tool.Risk != riskRead {
			continue
		}
		if got := tool.EffectiveVisibility(); got != visHidden {
			t.Errorf("%s is read-only and declares visibility %q", tool.Name, got)
		}
	}
}

// TestNoInvisibleWayToRunACommand is the rule that replaced the substitution
// pair, and it is worth explaining why the earlier test is gone rather than
// quietly editing it — deleting an assertion is normally the mistake this file
// exists to catch.
//
// It asserted that run_command was visHidden and terminal_run visInjects: two
// tools doing one job, told apart so the runtime could substitute the visible
// one when a role called for evidence. That worked, and it left the default in
// the wrong place. Substitution is opt-in, so the ordinary case remained an
// agent doing real work on somebody's machine with nothing on screen, and the
// role that fixed it had to be chosen in advance by whoever started the run —
// which is exactly the person who cannot yet know whether they will want to
// watch.
//
// So the pair was collapsed instead: run_command now runs its command in a
// terminal window like everything else, and there is no invisible half left to
// substitute for. What this test defends is that nobody adds one back. A hidden
// tool that runs arbitrary shell is not a feature with a trade-off, it is a hole
// in the only property this desktop promises — that a person watching it can
// see what is being done to it, and stop it.
func TestNoInvisibleWayToRunACommand(t *testing.T) {
	byName := map[string]toolDef{}
	for _, tool := range (&Server{}).buildTools() {
		byName[tool.Name] = tool
	}

	// The tools that take a shell command and execute it. Named rather than
	// detected, because a heuristic over descriptions would drift the moment
	// somebody rephrased one.
	for _, name := range []string{"run_command", "job_start", "launch_app", "terminal_run"} {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("%s is gone from the catalogue; if it was renamed, rename it here too", name)
			continue
		}
		if got := tool.EffectiveVisibility(); got == visHidden {
			t.Errorf("%s runs a shell command and is classified %q — there is no "+
				"invisible way to run a command on this desktop, by design", name, got)
		}
		if !tool.RequiresControl {
			t.Errorf("%s runs a shell command in a window on the shared screen and is "+
				"not gated; it must hold the controls first", name)
		}
	}

	if got := byName["terminal_run"].EffectiveVisibility(); got != visInjects {
		t.Errorf("terminal_run is %q, want injects — it types into a terminal people watch", got)
	}
}

// TestInstallingSoftwareIsWatched is the same invariant one step out.
//
// TestNoInvisibleWayToRunACommand covers the tools that take a shell command,
// and covering only those is what let this defect live: install_packages does
// not take a shell command, it takes a list of package names, so it was outside
// the net while doing something nobody would call less consequential than
// `run_command`. Somebody asked for gimp, apt ran for eight and a half seconds
// as root, and the person sharing the desktop watched an idle wallpaper.
//
// "Runs a shell command" was never the property worth defending. "Changes this
// machine in a way somebody would want to see and be able to stop" is.
func TestInstallingSoftwareIsWatched(t *testing.T) {
	byName := map[string]toolDef{}
	for _, tool := range (&Server{}).buildTools() {
		byName[tool.Name] = tool
	}

	for _, name := range []string{"install_packages", "remove_packages"} {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("%s is gone from the catalogue; if it was renamed, rename it here too", name)
			continue
		}
		if got := tool.EffectiveVisibility(); got == visHidden {
			t.Errorf("%s puts software on somebody's desktop and is classified %q — "+
				"it runs as a job in a terminal window people can see, by design", name, got)
		}
		if !tool.RequiresControl {
			t.Errorf("%s opens a window on the shared screen and is not gated; "+
				"it must hold the controls first", name)
		}
	}

	// search_packages is the deliberate exception, and it is only sound while
	// it stays a read.
	//
	// It used to run `apt-get update` as root, over the network, whenever the
	// index happened to be empty — unasked, off-screen, and while classified
	// riskRead, which meant a connection restricted to `readonly` could trigger
	// it. The classification is the load-bearing part: riskRead is what excuses
	// this tool from the window, so if it ever needs to change the machine
	// again, that capability belongs on install_packages or on a job, not here
	// behind a risk level that promises the opposite.
	search, ok := byName["search_packages"]
	if !ok {
		t.Fatal("search_packages is gone from the catalogue")
	}
	if search.Risk != riskRead {
		t.Errorf("search_packages is %q — a package search that is not read-only "+
			"has to run where it can be seen, like install_packages does", search.Risk)
	}
	if search.RequiresControl {
		t.Error("search_packages is gated; reading the on-disk index needs nobody's permission")
	}
}

// TestVisibilityIsPublished. A client cannot work this out for itself, so a
// field the server keeps to itself is a field the runtime has to duplicate — and
// duplicating it is how the risk maps drifted in the first place.
func TestVisibilityIsPublished(t *testing.T) {
	c := newSession(t, testServer(t))
	raw, _ := json.Marshal(c.call("tools/list", nil)["tools"])
	var tools []struct {
		Name        string `json:"name"`
		Annotations struct {
			Visibility string `json:"sentineldesk/visibility"`
		} `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("%v", err)
	}
	valid := map[string]bool{"hidden": true, "visible": true, "injects": true}
	for _, tool := range tools {
		if !valid[tool.Annotations.Visibility] {
			t.Errorf("%s published visibility %q", tool.Name, tool.Annotations.Visibility)
		}
	}
}

// TestValidateCatalogueRejectsAnUnclassifiedTool proves the startup guard, not
// just the test above it. The guard is the thing that makes a missing
// declaration a build failure rather than a default nobody chose — the failure
// mode that cost forty-six tools their risk level.
func TestValidateCatalogueRejectsAnUnclassifiedTool(t *testing.T) {
	// Against the real catalogue plus one extra, because validateCatalogue also
	// checks that no keyword vocabulary is stranded — a whole-catalogue
	// question that a two-element slice cannot answer. Testing the real entry
	// point is worth carrying the real catalogue.
	base := (&Server{}).buildTools()
	with := func(extra toolDef) []toolDef {
		return append(append([]toolDef{}, base...), extra)
	}

	if err := validateCatalogue(with(
		toolDef{Name: "a", Risk: riskWrite, Visibility: visHidden})); err != nil {
		t.Fatalf("a correctly declared tool was rejected: %v", err)
	}
	for _, bad := range []struct {
		why  string
		tool toolDef
		want string
	}{
		{"no visibility on a writing tool",
			toolDef{Name: "b", Risk: riskWrite}, "Visibility"},
		{"read-only claiming to be visible",
			toolDef{Name: "c", Risk: riskRead, Visibility: visVisible}, "cannot be visible"},
		{"injects without the room gate",
			toolDef{Name: "d", Risk: riskWrite, Visibility: visInjects}, "somebody else's session"},
	} {
		err := validateCatalogue(with(bad.tool))
		if err == nil {
			t.Errorf("%s was accepted", bad.why)
			continue
		}
		if !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: error does not mention %q: %v", bad.why, bad.want, err)
		}
	}

	// And a read-only tool may say hidden out loud. Redundant, not wrong —
	// refusing it would make the rule feel like a trap.
	if err := validateCatalogue(with(
		toolDef{Name: "e", Risk: riskRead, Visibility: visHidden})); err != nil {
		t.Errorf("a read-only tool declaring visHidden was rejected: %v", err)
	}
}

// --- provenance and the trail ---------------------------------------------------

// TestTaskIdAndGoalReachTheLog. Per-call provenance is not a trail: without
// these, a job a person would describe in one sentence appears as a scatter of
// rows across as many connections as the client happened to open.
func TestTaskIdAndGoalReachTheLog(t *testing.T) {
	s := testServer(t)
	c := newSession(t, s)

	res := c.call("tools/call", map[string]any{
		"name":      "wait",
		"arguments": map[string]any{"ms": 1},
		"_meta": map[string]any{
			"sentineldesk/taskId": "task-42",
			"sentineldesk/goal":   "prove the trail groups",
		},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("wait failed: %v", res["content"])
	}

	entries := s.actions.Tail(10, "wait")
	if len(entries) == 0 {
		t.Fatal("the call is not in the log at all")
	}
	last := entries[len(entries)-1]
	if last.Task != "task-42" {
		t.Errorf("task is %q, want task-42", last.Task)
	}
	if last.Goal != "prove the trail groups" {
		t.Errorf("goal is %q", last.Goal)
	}
}

// TestProvenanceIsOptional. Every external host sends none of this, and they all
// have to keep working — it is a namespaced extension, not a requirement.
func TestProvenanceIsOptional(t *testing.T) {
	s := testServer(t)
	c := newSession(t, s)
	res := c.call("tools/call", map[string]any{
		"name": "wait", "arguments": map[string]any{"ms": 1}})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("a call with no _meta failed: %v", res["content"])
	}
	entries := s.actions.Tail(10, "wait")
	if last := entries[len(entries)-1]; last.Task != "" || last.Goal != "" {
		t.Errorf("provenance appeared from nowhere: task %q goal %q", last.Task, last.Goal)
	}
}

// TestAGoalCannotBeUsedAsStorage. It is written into a line of every audit
// record, so an unbounded one turns the trail into somebody's scratch space.
func TestAGoalCannotBeUsedAsStorage(t *testing.T) {
	s := testServer(t)
	c := newSession(t, s)
	c.call("tools/call", map[string]any{
		"name": "wait", "arguments": map[string]any{"ms": 1},
		"_meta": map[string]any{"sentineldesk/goal": strings.Repeat("x", 5000)},
	})
	entries := s.actions.Tail(10, "wait")
	if got := len(entries[len(entries)-1].Goal); got > maxGoalLen+4 {
		t.Errorf("a %d-character goal was stored whole", got)
	}
}

// TestTheResultIsRecordedBesideTheArguments. Arguments answer "how did you do
// it"; for an audit the more interesting half is usually "what did it say".
func TestTheResultIsRecordedBesideTheArguments(t *testing.T) {
	requireSupervisedShell(t)
	s := testServer(t)
	c := newSession(t, s)
	c.call("tools/call", map[string]any{
		"name": "run_command",
		"arguments": map[string]any{
			"command": "echo audit-me-please", "timeout_ms": 10000},
	})
	entries := s.actions.Tail(10, "run_command")
	if len(entries) == 0 {
		t.Fatal("the command is not in the log")
	}
	if got := entries[len(entries)-1].Result; !strings.Contains(got, "audit-me-please") {
		t.Errorf("the result is not in the trail: %q", got)
	}
}

// TestAnImageIsNotedRatherThanStored. A screenshot is forty kilobytes of base64
// whose first two hundred characters say nothing, so a prefix of it would cost
// as much as four real results and be worth none of them.
func TestAnImageIsNotedRatherThanStored(t *testing.T) {
	got := summarizeContent([]map[string]any{
		{"type": "image", "mimeType": "image/png", "data": strings.Repeat("A", 40000)},
	})
	if strings.Contains(got, "AAAA") {
		t.Errorf("base64 leaked into the trail: %q", got)
	}
	if !strings.Contains(got, "image/png") {
		t.Errorf("the trail does not record that an image came back: %q", got)
	}
}

// A description that states a default must carry that default in the schema.
//
// # Why this test exists
//
// Because the number is written twice — once in prose for a model, once as a
// fact for everything else — and two copies of a number drift. This is the
// check that makes the duplication safe rather than a second place to be wrong.
//
// It also found a real one on the way in: ui_find's description said `default
// 20` while a11y.py has always applied 200, so the catalogue documented a limit
// the tool did not have. Nothing else in the project could have noticed that,
// because nothing else read the sentence.
func TestADocumentedDefaultIsAlsoAFactInTheSchema(t *testing.T) {
	checked := 0
	for _, tool := range (&Server{}).buildTools() {
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
				Type        string `json:"type"`
				Default     any    `json:"default"`
			} `json:"properties"`
		}
		if len(tool.InputSchema) == 0 {
			continue
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s: its schema is not readable: %v", tool.Name, err)
		}
		for name, prop := range schema.Properties {
			m := documentedDefault.FindStringSubmatch(prop.Description)
			if m == nil {
				continue
			}
			checked++
			said, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if prop.Default == nil {
				t.Errorf("%s.%s says %q and carries no default in the schema — "+
					"use pIntDef so a program can read it too", tool.Name, name, m[0])
				continue
			}
			got, ok := prop.Default.(float64)
			if !ok {
				t.Errorf("%s.%s has a default of type %T, want a number",
					tool.Name, name, prop.Default)
				continue
			}
			if int(got) != said {
				t.Errorf("%s.%s: the description says %d and the schema says %d — "+
					"one of them is lying to somebody", tool.Name, name, said, int(got))
			}
		}
	}
	// A floor, so that deleting every pIntDef and passing this test is not
	// possible. There were thirty-three when it was written.
	if checked < 30 {
		t.Fatalf("only %d documented defaults were checked; there were 33 when this "+
			"was written, so either the catalogue shrank or the pattern stopped matching",
			checked)
	}
}

// documentedDefault matches a default stated in a description.
//
// Deliberately narrow: `default 15000`, with an optional `=` or `:`. It is not
// trying to parse English — it is trying to catch the one phrasing this
// catalogue uses, so that a NEW phrasing is invisible to it rather than
// half-understood. A miss here costs a schema default nobody added; a false
// match would cost a failing build over a sentence about something else.
var documentedDefault = regexp.MustCompile(`(?i)default[ :=]+(\d+)`)
