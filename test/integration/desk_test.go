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

// Package integration drives a running desktop through MCP and checks what
// actually happened to it.
//
// These are not the tests `make test` runs, and the build tag is why. The rest
// of this project's tests hold to a rule worth keeping: they run anywhere, with
// no X display, no GStreamer and no container, so a green run means the logic is
// sound rather than that somebody's laptop happened to be set up. Nothing here
// can honour that — every test below needs a desktop with windows on it — so
// they are kept out of the default build entirely rather than making `make test`
// mean two different things depending on the machine.
//
// What they add is the half the unit tests cannot reach. buildTools() assembling
// literals correctly says nothing about whether type_text puts characters into
// an application, and the five silent successes this project has found all
// returned cleanly from code whose unit tests passed.
//
// The rule that gives them their value is the same one the sweep follows:
// whatever proves the tool worked must arrive through a different door than the
// tool. A test asserting that read_file returns what write_file wrote proves
// only that the two agree, which is exactly what they would do if they shared a
// bug. So the checks go into the container with docker exec and read the
// filesystem, the X server and the process table directly.
//
// Run them with:
//
//	make up && make test-integration
package integration

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	container = "sentineldesk"
	sockPath  = "/run/user/1000/sentineldesk-mcp.sock"
)

// Desk is one connection to the desktop's MCP server.
//
// It speaks to the same stdio bridge an AI host spawns, rather than dialling the
// socket directly, because that bridge is part of what is being tested: if it
// mangles a frame, every tool is wrong and no test that skipped it would notice.
type Desk struct {
	cmd  *exec.Cmd
	in   *bufio.Writer
	out  *bufio.Reader
	mu   sync.Mutex
	next int
}

// The desktop connection is dialled LAZILY, on the first test that asks for
// it, because this package holds two kinds of test that live on different
// machines: the desktop suites need the dev harness (`make up`), and the
// FrontDesk suite needs a control plane and no desktop at all. A TestMain
// that dialled up front made `make frontdesk-test` die on a deployment
// target that — correctly — never started the dev container.
var (
	shared     *Desk
	sharedOnce sync.Once
	sharedErr  error
)

// devDesk hands back the dev desktop's connection, dialling once on first use.
// A test that needs the desktop on a machine without one fails HERE, with
// the sentence that names the fix, instead of taking the whole package down.
func devDesk(t *testing.T) *Desk {
	t.Helper()
	sharedOnce.Do(func() { shared, sharedErr = connect() })
	if sharedErr != nil {
		t.Fatalf("cannot reach the desktop: %v\nIs it running? make up", sharedErr)
	}
	return shared
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared != nil {
		shared.close()
	}
	os.Exit(code)
}

func connect() (*Desk, error) {
	cmd := exec.Command("docker", "exec", "-i", "-u", "sentineldesk", container,
		"/usr/local/bin/sentineldesk", "-mcp-stdio", "-mcp-sock", sockPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d := &Desk{cmd: cmd, in: bufio.NewWriter(stdin), out: bufio.NewReader(stdout)}

	if _, err := d.rpc("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "go-integration", "version": "1"},
		"capabilities":    map[string]any{},
	}); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return d, nil
}

func (d *Desk) close() {
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
}

// rpc sends one request and returns its result, skipping notifications.
func (d *Desk) rpc(method string, params map[string]any) (map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := d.next

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := d.in.Write(append(raw, '\n')); err != nil {
		return nil, err
	}
	if err := d.in.Flush(); err != nil {
		return nil, err
	}

	for {
		line, err := d.out.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // not a frame we understand; the bridge logs nothing else
		}
		got, ok := msg["id"].(float64)
		if !ok || int(got) != id {
			continue // a progress notification, or an answer to something else
		}
		if e, ok := msg["error"].(map[string]any); ok {
			return nil, fmt.Errorf("rpc: %v", e["message"])
		}
		res, _ := msg["result"].(map[string]any)
		return res, nil
	}
}

// Call runs a tool and fails the test if the tool reports an error.
//
// The returned string is every text block joined, which is what the tools
// actually produce: some answer in prose and some in JSON, and a test that
// wanted to tell them apart would be asserting the shape of a reply rather than
// what happened to the desktop.
func (d *Desk) Call(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	out, isErr := d.call(t, tool, args)
	if isErr {
		t.Fatalf("%s failed: %s", tool, out)
	}
	return out
}

// CallErr runs a tool expecting it to refuse, and returns what it said.
func (d *Desk) CallErr(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	out, isErr := d.call(t, tool, args)
	if !isErr {
		t.Fatalf("%s was expected to fail and returned: %s", tool, out)
	}
	return out
}

func (d *Desk) call(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := d.rpc("tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	isErr, _ := res["isError"].(bool)

	var sb strings.Builder
	if content, ok := res["content"].([]any); ok {
		for _, c := range content {
			block, _ := c.(map[string]any)
			if txt, ok := block["text"].(string); ok {
				sb.WriteString(txt)
			}
		}
	}
	return sb.String(), isErr
}

// CallImage runs a tool that answers with a picture and returns the decoded
// bytes.
//
// Some capture tools write a file and some hand the image back inline; a test
// that only knew about paths would have to skip the inline ones, which are the
// ones where the bytes are the whole answer.
func (d *Desk) CallImage(t *testing.T, tool string, args map[string]any) []byte {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := d.rpc("tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("%s failed", tool)
	}
	content, _ := res["content"].([]any)
	for _, c := range content {
		block, _ := c.(map[string]any)
		if block["type"] != "image" {
			continue
		}
		data, _ := block["data"].(string)
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			t.Fatalf("%s returned an image that is not base64: %v", tool, err)
		}
		return raw
	}
	t.Fatalf("%s returned no image", tool)
	return nil
}

// pngSize reads the dimensions out of a PNG's IHDR, which is the only way to
// establish that a capture is of the region asked for rather than merely valid.
func pngSize(t *testing.T, raw []byte) (int, int) {
	t.Helper()
	if len(raw) < 24 || string(raw[1:4]) != "PNG" {
		t.Fatalf("not a PNG (%d bytes)", len(raw))
	}
	w := int(binary.BigEndian.Uint32(raw[16:20]))
	h := int(binary.BigEndian.Uint32(raw[20:24]))
	return w, h
}

// Tools lists the catalogue as the connection sees it.
func (d *Desk) Tools(t *testing.T) []map[string]any {
	t.Helper()
	res, err := d.rpc("tools/list", nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	raw, _ := res["tools"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// --- the other door ----------------------------------------------------------

// Sh runs a command inside the container, outside MCP entirely.
//
// This is what makes a test mean something. Confirming a tool with another tool
// establishes that the two agree; reading the container establishes what is
// there.
func Sh(t *testing.T, format string, a ...any) string {
	t.Helper()
	cmd := exec.Command("docker", "exec", container, "sh", "-c", fmt.Sprintf(format, a...))
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// ShBash runs a command through bash rather than sh, for the few checks that
// need a bash-only feature such as /dev/tcp.
func ShBash(t *testing.T, format string, a ...any) string {
	t.Helper()
	out, _ := exec.Command("docker", "exec", container, "bash", "-c",
		fmt.Sprintf(format, a...)).Output()
	return strings.TrimSpace(string(out))
}

// shIn is Sh against any container, which the ssh tests need in order to read
// the far side of the connection they opened.
func shIn(t *testing.T, name, cmd string) string {
	t.Helper()
	out, _ := exec.Command("docker", "exec", name, "sh", "-c", cmd).Output()
	return strings.TrimSpace(string(out))
}

// dockerIP is a container's address on whatever network it joined, or "" when
// there is no such container.
func dockerIP(name string) string {
	out, err := exec.Command("docker", "inspect", name, "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ShUser is Sh as the desktop's own user, for the things root cannot touch —
// PulseAudio refuses root outright, and a check that ran as root would report
// the volume as unreadable rather than as wrong.
func ShUser(t *testing.T, format string, a ...any) string {
	t.Helper()
	inner := "XDG_RUNTIME_DIR=/run/user/1000 " + fmt.Sprintf(format, a...)
	cmd := exec.Command("docker", "exec", container, "su", "-s", "/bin/sh",
		"sentineldesk", "-c", inner)
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// X runs a command against the display.
func X(t *testing.T, format string, a ...any) string {
	t.Helper()
	return Sh(t, "DISPLAY=:0 "+format, a...)
}

// --- shared fixtures ---------------------------------------------------------

// control takes the room's controls, which every input tool needs and none of
// them will do implicitly. Released by the cleanup so one test holding them
// cannot make the next one fail for a reason that has nothing to do with it.
func control(t *testing.T) {
	t.Helper()
	out := shared.Call(t, "request_control", map[string]any{"timeout_ms": 8000})
	if !strings.Contains(out, "true") {
		t.Fatalf("could not take the controls: %s", out)
	}
	t.Cleanup(func() { shared.Call(t, "release_control", nil) })
}

// openWindow starts an xterm with a unique title and waits for it, returning the
// window id. Removed when the test ends, so a failure does not leave the desktop
// dressed for it.
func openWindow(t *testing.T, title string) string {
	t.Helper()
	shared.Call(t, "launch_app", map[string]any{
		"command": fmt.Sprintf("xterm -T %s -e sleep 600", title)})
	out := shared.Call(t, "wait_for_window", map[string]any{
		"match": title, "timeout_ms": 15000})
	id := jsonField(t, out, "id")
	if id == "" {
		t.Fatalf("no window id in %s", out)
	}
	t.Cleanup(func() {
		X(t, "wmctrl -i -c %s 2>/dev/null || true", id)
	})
	return id
}

// jsonField pulls one string field out of a reply, for the tools that answer in
// JSON. Deliberately forgiving: a test asserting the exact shape of a reply
// would fail on a harmless extra field.
func jsonField(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	}
	return ""
}

// eventually retries until the condition holds or the deadline passes.
//
// Not every effect is instant: a window manager restacks on its own schedule and
// an application takes a moment to draw. The alternative is a fixed sleep long
// enough for the worst case, which is slower and still occasionally wrong.
func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}
