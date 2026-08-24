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

// The MCP (Model Context Protocol) server, which lets an AI drive the desktop.
//
// The daemon opens a local Unix socket (0600) and the -mcp-stdio sub-command is
// a thin stdio<->socket bridge that the AI host spawns. Killing the host
// therefore never takes the desktop down with it.
//
// The control logic reuses what already exists — desktop.InputInjector,
// desktop.Clipboard — plus helpers for windows, execution and
// capture.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/sentineldesk/desktop/internal/desktop"
	"github.com/sentineldesk/desktop/internal/media"
	"github.com/sentineldesk/desktop/internal/shell"
	"github.com/sentineldesk/desktop/pkg/config"
	"github.com/sentineldesk/desktop/pkg/version"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mcpProtocolVersion = "2024-11-05"
	mcpServerName      = "sentineldesk"
)

// --- JSON-RPC 2.0 ---------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is anything the server sends: an answer to a request, or a
// notification of its own.
//
// One struct for both because they go out of the same door, under the same
// write lock — a progress notification interleaved halfway through a response
// would be two broken messages rather than one of each. A notification carries
// Method and Params and no ID; an answer carries ID and Result or Error.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`

	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- cancellation ------------------------------------------------------------

// inflight tracks the tool calls running on one connection, so that a
// notifications/cancelled can reach the right one and closing the connection can
// stop all of them.
//
// Per connection rather than per server: JSON-RPC ids are only unique within a
// connection, and one client cancelling another's work by guessing an id would
// be a strange thing to make possible.
type inflight struct {
	mu    sync.Mutex
	calls map[string]*call
}

// call is one request in progress: how to stop it, and how to answer it.
//
// The answer matters as much as the cancel. handleToolCall blocks on dispatch,
// so if the tool is not listening to its context the response cannot be written
// until the tool finishes — which for a thirty-second sleep means the client
// asked to stop and then waited thirty seconds to be told it had. It could not
// tell "still stopping" from "ignored you", which is the worst of the two
// things to be unsure about.
//
// So cancelling answers straight away, and whatever the tool eventually returns
// is dropped. The wait always stops even when the work does not.
type call struct {
	cancel context.CancelFunc
	answer func(map[string]any)
}

func newInflight() *inflight { return &inflight{calls: map[string]*call{}} }

func (f *inflight) add(id string, cancel context.CancelFunc, answer func(map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[id] = &call{cancel: cancel, answer: answer}
}

// done releases a finished call without answering it — the handler is about to
// do that itself. It still cancels, which is the usual defer cancel(): the
// context is over either way and its resources should go.
func (f *inflight) done(id string) {
	f.mu.Lock()
	c, ok := f.calls[id]
	delete(f.calls, id)
	f.mu.Unlock()
	if ok {
		c.cancel()
	}
}

// cancel stops one call, answers it, and reports whether there was one to stop.
// A cancel for a request that already finished is not an error: the race is
// normal and the client's intent was satisfied either way.
func (f *inflight) cancel(id string, reason string) bool {
	f.mu.Lock()
	c, ok := f.calls[id]
	delete(f.calls, id)
	f.mu.Unlock()
	if !ok {
		return false
	}
	c.cancel()
	c.answer(toolCallResult(textContent("%s", reason), denialCancelled))
	return true
}

func (f *inflight) cancelAll(reason string) {
	f.mu.Lock()
	pending := make([]*call, 0, len(f.calls))
	for id, c := range f.calls {
		pending = append(pending, c)
		delete(f.calls, id)
	}
	f.mu.Unlock()
	for _, c := range pending {
		c.cancel()
		c.answer(toolCallResult(textContent("%s", reason), denialCancelled))
	}
}

// pendingCall is one tools/call prepared for handling: its context, the single
// reply it is allowed to produce, and the cleanup when it finishes normally.
//
// It is built in the connection's own goroutine, BEFORE the handler is spawned,
// and that ordering is the whole reason it exists. Registering inside the
// handler left a window: a client may send tools/call and
// notifications/cancelled back to back, and the handler goroutine need not have
// run yet when the cancellation arrives. The cancellation then found nothing to
// cancel and was silently dropped, and the client waited out a call it had
// already asked to stop. Reading the connection is sequential, so doing it here
// makes the order on the wire the order in the map.
type pendingCall struct {
	ctx   context.Context
	reply func(map[string]any)
	done  func()
}

func newPendingCall(req rpcRequest, write func(rpcResponse), flight *inflight) *pendingCall {
	ctx, cancel := context.WithCancel(context.Background())

	// Only when the client asked for it. The token is echoed exactly as it
	// arrived, because it is the client's handle and not ours to normalise.
	if token := progressToken(req.Params); token != nil {
		var step atomic.Uint64
		ctx = withProgress(ctx, func(message string, done float64) {
			params := map[string]any{
				"progressToken": token,
				// The specification wants progress to increase. Elapsed
				// seconds would stall on a command that produces nothing for a
				// while, so the count of reports is used instead: it is
				// monotonic by construction and the message carries the detail.
				"progress": step.Add(1),
			}
			if done > 0 {
				params["progress"] = done
			}
			if message != "" {
				params["message"] = message
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return
			}
			write(rpcResponse{Method: "notifications/progress", Params: raw})
		})
	}

	// Exactly one response per request, whoever gets there first. A
	// cancellation answers from the connection's goroutine while the handler
	// is still inside dispatch, so the result that eventually arrives has to be
	// dropped rather than sent as a second reply to the same id.
	var answered atomic.Bool
	reply := func(result map[string]any) {
		if answered.Swap(true) {
			return
		}
		write(rpcResponse{ID: req.ID, Result: result})
	}

	pc := &pendingCall{ctx: ctx, reply: reply, done: cancel}
	// A call with no id is notification-shaped: nothing can name it to cancel
	// it, and registering it would collide with the next one under the empty
	// key. It still ends when the connection does, through its context.
	if req.ID != nil {
		key := requestKey(req.ID)
		flight.add(key, cancel, reply)
		pc.done = func() { flight.done(key) }
	}
	return pc
}

// --- progress --------------------------------------------------------------------

// A long tool used to say nothing at all until it finished. install_packages
// can run for minutes, snapshot_create for longer, and everything watching —
// a person, a timeline in a console — had the same view as during a hang.
//
// The protocol's answer is notifications/progress, and the client opts in by
// putting a progressToken in the call's _meta. Without one, nothing is sent:
// a client that did not ask should not be given a stream of messages it has to
// discard.
//
// The reporter rides on the context rather than the signatures. The context is
// already threaded through every dispatcher and every tool, so this reaches all
// of them without touching one of the hundred and twenty, and a tool that has
// nothing to report simply never asks for it.
type progressFunc func(message string, done float64)

type progressKeyType struct{}

var progressKey progressKeyType

func withProgress(ctx context.Context, fn progressFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey, fn)
}

// progressOf never returns nil, so callers can report unconditionally. When the
// client asked for nothing, reporting costs one function call that returns.
func progressOf(ctx context.Context) progressFunc {
	if fn, ok := ctx.Value(progressKey).(progressFunc); ok && fn != nil {
		return fn
	}
	return func(string, float64) {}
}

// progressToken pulls the client's token out of the call's _meta. The protocol
// allows a string or a number, so it is kept as raw JSON and echoed back
// exactly as it arrived — the client has to recognise its own token.
func progressToken(params json.RawMessage) json.RawMessage {
	var p struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	if len(p.Meta.ProgressToken) == 0 || string(p.Meta.ProgressToken) == "null" {
		return nil
	}
	return p.Meta.ProgressToken
}

// callProvenance is what the client says about WHY it is calling, pulled out of
// the call's _meta.
//
// Namespaced, because none of it is in the specification, and optional, because
// an external host will send none of it and must keep working. It exists for
// the runtime: the server can see what was called and by which connection, and
// it cannot see that seven calls across five connections were one job somebody
// would describe in a sentence. Only the caller knows that, so the caller is
// asked and the server keeps what it is told.
//
// Deliberately not trusted for anything but the log. A task id is a label on an
// audit row, never an identity, never a capability — if it ever decides
// something, a client can name another client's task and inherit it.
type callProvenance struct {
	Task string
	Goal string
}

// maxGoalLen bounds what a client can write into every log line. A goal is a
// sentence; anything longer is either a mistake or an attempt to use the audit
// trail as storage.
const maxGoalLen = 300

func provenanceOf(params json.RawMessage) callProvenance {
	var p struct {
		Meta struct {
			Task string `json:"sentineldesk/taskId"`
			Goal string `json:"sentineldesk/goal"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return callProvenance{}
	}
	out := callProvenance{
		Task: strings.TrimSpace(p.Meta.Task),
		Goal: strings.TrimSpace(p.Meta.Goal),
	}
	if len(out.Task) > 64 {
		out.Task = out.Task[:64]
	}
	if len(out.Goal) > maxGoalLen {
		out.Goal = out.Goal[:maxGoalLen] + "…"
	}
	return out
}

// --- connections ---------------------------------------------------------------

// connection is what the server knows about one client on the socket.
//
// Until now it knew nothing: `serve` held a policy and that was all, so every
// call in the action log read "the agent did this" with no way to say which
// client, and there was no handle by which one client could be stopped without
// stopping the others.
//
// Both matter more than they look, because of how the room works. Every MCP
// connection shares the room identity `agent` — that is deliberate, and it is
// what lets a runtime fan sub-agents out across connections and have them act
// under one claim on the desktop. The cost is that "the agent" stops being a
// useful name in an audit the moment there is more than one. This is the name
// that distinguishes them.
//
// It is NOT a second room identity, and must not become one. Several agents
// acting as one participant is the property; this only makes the log and the
// emergency stop able to tell them apart.
type connection struct {
	id uint64

	// Where unsolicited notifications go, when this client has asked for any.
	// It lives on the connection because that is exactly its lifetime: the
	// subscription cannot outlive the socket, and closing the socket needs no
	// bookkeeping to end it. See events.go.
	events *eventHub

	mu     sync.Mutex
	client string // name and version from initialize, when it sent any
}

func (c *connection) setClient(name, version string) {
	if name = strings.TrimSpace(name); name == "" {
		return
	}
	if version = strings.TrimSpace(version); version != "" {
		name += " " + version
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = name
}

func (c *connection) clientName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

// HaltConnection refuses further tool calls from one connection, leaving every
// other client alone.
//
// This is the instrument an emergency stop needs. Stopping the agent runtime
// must not stop an operator's own MCP session or the desktop itself, so it
// cannot be a flag on the server or a closed socket — it has to name somebody.
// The id comes back to the client in its own initialize response, so whatever
// supervises it can quote the id it was given.
//
// Deliberately not a kill: calls already running are left to finish under their
// own cancellation, and nothing here reaches into the desktop. It refuses what
// has not started yet.
func (s *Server) HaltConnection(id uint64, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "halted by the server"
	}
	s.haltMu.Lock()
	defer s.haltMu.Unlock()
	if s.halted == nil {
		s.halted = map[uint64]string{}
	}
	s.halted[id] = reason
}

// ResumeConnection lifts a halt. Explicit, because an emergency stop that
// expires on its own is not one.
func (s *Server) ResumeConnection(id uint64) {
	s.haltMu.Lock()
	defer s.haltMu.Unlock()
	delete(s.halted, id)
}

func (s *Server) haltedReason(id uint64) (string, bool) {
	s.haltMu.RLock()
	defer s.haltMu.RUnlock()
	reason, ok := s.halted[id]
	return reason, ok
}

// requestKey turns a JSON-RPC id into a map key.
//
// The id is raw JSON because the protocol allows a string or a number and the
// two are distinct — id 2 and id "2" are different requests. Trimming is enough
// to normalise: both the request and the cancellation that refers to it come
// from the same client, encoded the same way.
func requestKey(raw json.RawMessage) string {
	return strings.TrimSpace(string(raw))
}

// --- Server ----------------------------------------------------------------

// Deliverer hands a file on the desktop's disk to the connected browsers, and
// reports how many were told. It is an interface so that this package does not
// depend on the streaming session just to send one message.
type Deliverer interface {
	Deliver(absPath, name string) int
}

// Server answers MCP requests and turns them into actions on the desktop.
type Server struct {
	cfg      config.Config
	display  string
	injector *desktop.InputInjector
	clip     *desktop.Clipboard
	recorder *media.Recorder
	shells   *shell.ShellManager
	sshm     *shell.SSHManager
	policy   *Policy
	actions  *ActionLog

	// How to hand a finished file to the browsers. Optional: with nobody
	// watching, destination:download degrades to leaving it on disk and saying
	// so, rather than failing.
	delivery Deliverer

	// The room, so the agent can be a participant rather than an invisible
	// hand: a name in the list, a marker on screen, and a turn at the controls.
	// Optional — a bridge process has none.
	room      Rooms
	agentName string

	// Where the recording in progress should end up, remembered from
	// start_recording because stop_recording is what has the file.
	recDestination string
	tools          []toolDef
	control        controlIndex
	known          nameIndex
	argNames       argIndex

	pkgOnce  sync.Once
	pkgIndex map[string]commandOrigin

	// discovery trims tools/list to the core set, read once at startup because
	// the environment does not change under a running process and re-reading it
	// per request would only invite it to disagree with itself.
	discovery bool

	// Connections, numbered so the action log and the emergency stop can name
	// one without naming the rest. See connection.
	connSeq atomic.Uint64
	haltMu  sync.RWMutex
	halted  map[uint64]string // connection id -> why it was halted

	// Background work the agent started, and the interruption a person can
	// raise against it. See tools_jobs.go.
	jobs jobs

	// Values the agent may use and must never see. See secrets.go.
	vault *vault

	// typeInto writes a secret into an element, behind a seam so a test can
	// watch what would have been typed without an accessibility bridge or a
	// display. nil is the real thing. See typeSecretInto.
	typeInto func(ref, text string) error

	// Credentials spotted leaving, so somebody can be told to rotate them.
	// See detect.go — it warns and never blocks.
	creds *credentialWatcher

	// Held, reversibly, while somebody reads something. See pause.go.
	pause pauseState

	// How many connections are open right now, as opposed to connSeq, which
	// only ever counts up. The agent leaves the room when this reaches zero —
	// see serve().
	connLive atomic.Int64

	// The native window and desktop reader, opened the first time something
	// asks rather than at startup: a Server built without a display — every
	// test in this package — then never touches X at all.
	ewmhOnce sync.Once
	ewmh     *desktop.EWMH
	ewmhErr  error

	watchOnce sync.Once
	watcher   *desktop.Watcher
	watchErr  error

	damageOnce sync.Once
	damageW    *desktop.DamageWatcher
	damageErr  error

	uiMu   sync.Mutex
	uiLast map[string]uiNode // last snapshot of the tree, for ui_diff

	restreamMu  sync.Mutex
	restream    *exec.Cmd
	restreamURL string

	// Graphical remote sessions (RDP/VNC/SPICE) opened onto the shared screen.
	// Unlike ssh_*, which is headless, these land a window everyone in the room
	// sees — so they are tracked here to be listed and closed, the same way the
	// shell and ssh managers track theirs.
	remoteMu       sync.Mutex
	remoteSessions map[string]*remoteSession
	remoteSeq      int
}

func NewServer(cfg config.Config, injector *desktop.InputInjector, clip *desktop.Clipboard, rec *media.Recorder) *Server {
	s := &Server{
		cfg:       cfg,
		display:   cfg.Display,
		injector:  injector,
		clip:      clip,
		recorder:  rec,
		shells:    shell.NewShellManager(),
		sshm:      shell.NewSSHManager(),
		policy:    NewPolicy(),
		actions:   NewActionLog(cfg.ActionLog, cfg.ActionLogMaxMB),
		discovery: discoveryEnabled(),

		remoteSessions: map[string]*remoteSession{},
	}
	// The shell's half of the desktop's history lives in a directory the shells
	// create for themselves; made here too, so the first reader does not depend
	// on somebody having opened a terminal first.
	ensureActivityDir()
	s.vault = newVault()
	s.creds = newCredentialWatcher()

	s.tools = s.buildTools()
	// A tool defined without a risk level is a permission decision nobody made.
	// Refusing to start is loud, and the failure it replaces was not: the tool
	// simply behaved as though someone had classified it, in whichever
	// direction the missing entry happened to imply.
	if err := validateCatalogue(s.tools); err != nil {
		log.Fatalf("mcp: %v", err)
	}
	s.policy.risk = buildRiskIndex(s.tools)
	s.control = buildControlIndex(s.tools)
	s.known = buildNameIndex(s.tools)
	s.argNames = buildArgIndex(s.tools)
	return s
}

// windows returns the EWMH reader, opening it on first use.
func (s *Server) windows() (*desktop.EWMH, error) {
	s.ewmhOnce.Do(func() {
		s.ewmh, s.ewmhErr = desktop.NewEWMH(s.display)
	})
	return s.ewmh, s.ewmhErr
}

// watch returns the root-window event watcher, opening it on first use.
//
// Opened lazily and separately from the EWMH reader because it is worth having
// only if something waits: a session that never calls a wait_* tool never pays
// for the connection or the goroutine. A failure here is not fatal — the
// callers fall back to polling, the way every other optional capability in this
// project degrades rather than refusing.
func (s *Server) watch() (*desktop.Watcher, error) {
	s.watchOnce.Do(func() {
		s.watcher, s.watchErr = desktop.NewWatcher(s.display)
		if s.watchErr != nil {
			log.Printf("mcp: root-window events unavailable, waits will poll: %v", s.watchErr)
		}
	})
	return s.watcher, s.watchErr
}

// damage returns the screen-change watcher, opening it on first use.
//
// A display without the DAMAGE extension returns an error here and wait_for_idle
// falls back to capturing and hashing the screen, which is what it always did.
// Same shape as peer pointers without XShape, or cursor tracking without
// XShape: the capability is optional and its absence costs performance, never
// the feature.
// Its failure is logged rather than swallowed. Degrading quietly is right for
// the caller and wrong for whoever has to explain the machine: DAMAGE failing
// to start once cost nothing visible anywhere, no error and no changed answer,
// only wait_for_idle going on capturing and PNG-encoding the whole screen five
// times a second while appearing to work. An optional capability should say
// when it is not there.
func (s *Server) damage() (*desktop.DamageWatcher, error) {
	s.damageOnce.Do(func() {
		s.damageW, s.damageErr = desktop.NewDamageWatcher(s.display)
		if s.damageErr != nil {
			log.Printf("mcp: screen-change events unavailable, wait_for_idle will capture instead: %v", s.damageErr)
		}
	})
	return s.damageW, s.damageErr
}

// SetDelivery wires up browser delivery. Without it, destination:download has
// nowhere to send the file and falls back to leaving it on the desktop.
func (s *Server) SetDelivery(d Deliverer) { s.delivery = d }

// SetRoom puts the agent in the shared session. Without it the agent still
// works, but invisibly and without arbitration — which is only right when
// nothing else can be watching.
func (s *Server) SetRoom(r Rooms, name string) {
	s.room, s.agentName = r, name
}

// deliver hands a file to the browsers, returning how many were told.
func (s *Server) deliver(path, name string) int {
	if s.delivery == nil {
		return 0
	}
	return s.delivery.Deliver(path, name)
}

// Listen opens the Unix socket and serves connections, one MCP session each.
func (s *Server) Listen(sockPath string) error {
	_ = os.Remove(sockPath) // a stale socket from an earlier run
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		log.Printf("mcp: could not set 0600 on the socket: %v", err)
	}
	log.Printf("mcp: listening on %s (%d tools)", sockPath, len(s.tools))
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return nil
}

// serve processes one connection's JSON-RPC messages, one per line.
//
// Each connection starts from the daemon's policy and may RESTRICT itself
// further (the "sentineldesk/policy" method) but never widen. That asymmetry is
// what makes it safe to hand an agent a read-only endpoint without changing
// anyone else's.
func (s *Server) serve(conn net.Conn) {
	defer conn.Close()

	// Everything this connection started stops when it goes away. Before this
	// the goroutines simply outlived the socket, so a host that died mid-call
	// left the work running with nobody to give the answer to.
	flight := newInflight()
	defer flight.cancelAll("cancelled: the connection closed")

	client := &connection{id: s.connSeq.Add(1)}

	// Leaving the room is the other half of joining it, and for a long time
	// there was no other half: Room.LeaveAgent existed, released control
	// correctly, and was called from nowhere. So an agent that took the controls
	// and then went away — the host quitting, `docker exec` killed, a crash mid
	// task — left the desktop held by a process that no longer existed, and the
	// person sharing it had no way to take it back except by asking somebody to
	// restart the daemon.
	//
	// Counted rather than done per connection, because "the agent" is one
	// identity for every MCP connection there is. A host that opens a second
	// connection alongside its first, or a `-mcp-stdio` bridge run beside a
	// long-lived one, must not evict the agent that is still driving when only
	// one of them hangs up. The room empties when the last of them does.
	if s.room != nil {
		s.connLive.Add(1)
		defer func() {
			if s.connLive.Add(-1) == 0 {
				s.room.LeaveAgent()
			}
		}()
	}

	connPolicy := s.policy
	var policyMu sync.RWMutex
	var writeMu sync.Mutex
	enc := json.NewEncoder(conn)
	write := func(resp rpcResponse) {
		writeMu.Lock()
		defer writeMu.Unlock()
		resp.JSONRPC = "2.0"
		_ = enc.Encode(resp)
	}

	// Built for every connection, subscribed to nothing until the client asks.
	// An idle hub costs one struct and watches no source, so there is no reason
	// to construct it lazily and a good reason not to: the deferred close is
	// the only thing that guarantees a departing client's watchers are torn
	// down, and it has to be registered before the first message is read.
	client.events = newEventHub(write, s.room, func() *desktop.Watcher {
		w, _ := s.watch()
		return w
	}, func() (desktop.WindowInfo, bool) {
		e, err := s.windows()
		if err != nil {
			return desktop.WindowInfo{}, false
		}
		info, ok, err := e.ActiveWindow()
		return info, ok && err == nil
	})
	defer client.events.close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		// This connection's restriction is handled inline rather than in a
		// goroutine, so that it is in force before any tool call that follows.
		if req.Method == "sentineldesk/policy" {
			var p struct {
				Level string `json:"level"`
				Deny  string `json:"deny"`
				Allow string `json:"allow"`
			}
			_ = json.Unmarshal(req.Params, &p)
			policyMu.Lock()
			// From where this connection stands, not from the daemon's
			// ceiling. Restricting from s.policy each time started every
			// request afresh at the top, so a connection that had dropped
			// itself to readonly could ask for full and get it — the one
			// thing this method promises cannot happen. Chaining from the
			// current policy makes it monotonic: Restrict already refuses a
			// level above its own, accumulates denials and intersects
			// allow-lists.
			connPolicy = connPolicy.Restrict(p.Level, p.Deny, p.Allow)
			applied := connPolicy.Describe()
			policyMu.Unlock()
			if req.ID != nil {
				write(rpcResponse{ID: req.ID, Result: applied})
			}
			continue
		}

		// Cancellation is handled inline for the same reason the policy change
		// is: queueing it behind a goroutine would let the call it refers to
		// run on for as long as the scheduler felt like it.
		if req.Method == "notifications/cancelled" {
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
				Reason    string          `json:"reason"`
			}
			_ = json.Unmarshal(req.Params, &p)
			flight.cancel(requestKey(p.RequestID), cancelReason(p.Reason))
			continue
		}

		policyMu.RLock()
		active := connPolicy
		policyMu.RUnlock()

		// Prepared here rather than in the handler so that a cancellation sent
		// straight after the call always finds it registered — see
		// newPendingCall.
		var pending *pendingCall
		if req.Method == "tools/call" {
			pending = newPendingCall(req, write, flight)
		}

		// One goroutine per request: a slow or wedged tool must not freeze the
		// rest of the connection. The client pairs responses by id anyway.
		go s.handle(req, write, active, pending, client)
	}
}

func (s *Server) handle(req rpcRequest, write func(rpcResponse), policy *Policy, pending *pendingCall, client *connection) {
	switch req.Method {
	case "initialize":
		var p struct {
			ClientInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		client.setClient(p.ClientInfo.Name, p.ClientInfo.Version)
		write(rpcResponse{ID: req.ID, Result: map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": mcpServerName, "version": buildVersion()},
			// The connection is told its own number so that whatever supervises
			// it can quote the id back when it needs this one stopped and no
			// others. Namespaced: the specification does not define it.
			//
			// The catalogue size rides along so a host can pin what it wrote
			// against — "I require system_updates" becomes a check against a
			// number here plus docs/mcp-changelog.md, instead of a missing-tool
			// error three calls in. Deliberately the FULL catalogue, not this
			// connection's policy-filtered view: the question it answers is
			// "which catalogue does this server speak", and that answer must
			// not change with MCP_POLICY.
			"_meta": map[string]any{
				"sentineldesk/connectionId": client.id,
				"sentineldesk/catalogue":    map[string]any{"tools": len(s.tools)},
			},
		}})
	case "notifications/initialized", "initialized":
		// a notification: no response is expected
	case "ping":
		write(rpcResponse{ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		// Advertise only what this connection may use. Offering a tool that
		// will be refused is an invitation to walk into a wall.
		//
		// With MCP_DISCOVERY on, narrow it further to the core set and let
		// tool_search surface the rest on demand. Note the difference between
		// the two filters: policy decides what may be CALLED and applies again
		// in handleToolCall, while discovery only decides what is MENTIONED
		// here. A tool left out by discovery still runs if the model names it,
		// which is what makes searching for one worth doing.
		write(rpcResponse{ID: req.ID, Result: map[string]any{"tools": s.listedTools(policy)}})
	case "tools/call":
		s.handleToolCall(req, write, policy, pending, client)
	default:
		if req.ID != nil {
			write(rpcResponse{ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
		}
	}
}

func (s *Server) handleToolCall(req rpcRequest, write func(rpcResponse), policy *Policy, pending *pendingCall, client *connection) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		write(rpcResponse{ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
		return
	}
	// The single point every tool call passes through, which is where policy is
	// applied and the action log written — without cluttering each tool.
	args := map[string]any{}
	if len(params.Arguments) > 0 {
		_ = json.Unmarshal(params.Arguments, &args)
	}
	prov := provenanceOf(req.Params)
	entry := actionEntry{
		Time: nowStamp(), Tool: params.Name, Args: summarizeArgs(args),
		VideoAt: videoOffset(s.recorder),
		Conn:    client.id, Client: client.clientName(),
		Task: prov.Task, Goal: prov.Goal,
		Workroom: s.cfg.WorkroomID, Runtime: s.cfg.RuntimeID,
	}

	// One cancellable context per call, registered under this request's id so a
	// notifications/cancelled can find it and so closing the connection takes
	// it with everything else.
	//
	// It is created here rather than inside dispatch because this is the only
	// place that knows the request id. Registering is skipped for a call with
	// no id — a notification-shaped tools/call, which has nothing to cancel by
	// and would collide with the next one under the empty key.
	ctx, reply := pending.ctx, pending.reply
	// The connection's event hub rides on the context the same way the progress
	// reporter does, so subscribe_events can reach it without a signature
	// change in every dispatcher between here and there.
	ctx = withEvents(ctx, client.events)
	defer pending.done()

	// refuse writes the failure once, in both forms: the sentence for whoever
	// reads it and the kind for whatever branches on it, and the same pair into
	// the action log so an audit can be read by machine too.
	refuse := func(kind denialKind, content []map[string]any, reason string) {
		entry.OK = false
		entry.Denied = reason
		entry.Kind = string(kind)
		s.actions.Add(entry)
		reply(toolCallResult(content, kind))
	}

	// Before anything else, including whether the tool exists: a halted
	// connection is not being told about the catalogue, it is being told to
	// stop. Answering "unknown tool" first would leak the shape of the
	// catalogue to a client that is supposed to be doing nothing at all.
	if reason, halted := s.haltedReason(client.id); halted {
		refuse(denialEmergency, textContent("%s", reason), reason)
		return
	}

	// A pending abort interrupts whatever comes next, once.
	//
	// Deliberately NOT a halt, although the halt was sitting right there and
	// looks like the obvious fit for a panic button. A halted connection can do
	// nothing at all — including read. The whole point of the button is that a
	// person saw the agent going wrong, stopped it, and now wants it to LOOK at
	// what happened, including at whatever they did themselves while it was
	// stopped. Refusing every call would leave it blind and waiting, which is
	// not supervision, it is a hang.
	//
	// So the interruption lands once and then gets out of the way. What contains
	// the agent afterwards is the same thing that contains it the rest of the
	// time: it no longer holds the controls, so it can read everything and
	// change nothing until somebody hands them back.
	if note := s.takeAbortNote(); note != "" {
		refuse(denialEmergency, textContent("%s", note), "aborted by a person")
		return
	}

	// A pause holds anything that CHANGES something, and lets reads through.
	//
	// The asymmetry is the design, not a compromise. Somebody who pauses an
	// agent usually does it to show it something, or to read what it just did
	// before it moves on — and an agent that cannot call screenshot or activity
	// while paused cannot participate in the thing the pause was for. Gating on
	// risk rather than on the connection is what keeps its eyes open while its
	// hands are still.
	//
	// Checked after the abort note so that an abort during a pause is delivered
	// as an abort: it is the stronger statement, and the person who pressed it
	// has just decided the work was wrong rather than merely early.
	if by, held := s.paused(); held && s.policy.risk[params.Name] != riskRead {
		refuse(denialEmergency, textContent("%s", pauseRefusal(by, params.Name)),
			"paused by a person")
		return
	}

	// Before policy, because a name that does not exist was not refused — it
	// was never there. Asking policy first meant the same nonexistent tool came
	// back as "not in the tool catalogue" under safe and "unknown tool" under
	// full, which is two answers to one question. Tools that DO exist and are
	// hidden by policy still report policy: this only separates missing from
	// forbidden.
	if !s.known[params.Name] {
		refuse(denialUnknown, textContent("unknown tool: %s", params.Name), "no such tool")
		return
	}

	if ok, reason := policy.Allowed(params.Name, args); !ok {
		refuse(denialPolicy, textContent("denied by the server policy: %s", reason), reason)
		return
	}

	// After policy on purpose: a caller who may not call this tool should not
	// learn its argument names by guessing at them.
	//
	// The refusal names what it did not recognise and lists what it would have
	// taken, because the mistake is almost always a near miss — depth for
	// max_depth, text for selector — and a caller told only "bad arguments"
	// has to go back to tools/list to find out which one.
	if bad := s.argNames.UnknownArgs(params.Name, args); len(bad) > 0 {
		known := s.argNames.Declared(params.Name)
		msg := fmt.Sprintf("%s does not take %s", params.Name, strings.Join(bad, ", "))
		if len(known) > 0 {
			msg += fmt.Sprintf(" — it takes %s", strings.Join(known, ", "))
		} else {
			msg += " — it takes no arguments"
		}
		refuse(denialBadArgs, textContent("%s", msg), "unknown argument")
		return
	}

	// Turn-taking, enforced in ONE place so a new input tool cannot forget it.
	// Policy above is the hard ceiling; this is the cooperative layer below it.
	if s.injectsInput(params.Name) {
		if err := s.mayInject(); err != nil {
			refuse(denialRoom, textContent("%v", err), "room arbitration")
			return
		}
	}

	// MCP_POLICY=approve: a dangerous call waits here for a person in the room
	// to allow it. LAST of the gates on purpose — a call that policy would
	// refuse, that names a tool that does not exist, or that cannot hold the
	// controls it needs should fail on its own terms before it costs a human
	// any attention at all. The prompt's text is composed by the server, never
	// by the agent, for the same reason request_control's is: a prompt that
	// unlocks something must not be able to say whatever the agent would like
	// it to say.
	if policy.RequiresApproval(params.Name, args) {
		ok, why := s.approveDanger(ctx, params.Name, entry.Args)
		if !ok {
			refuse(denialApproval, textContent("%s", why), why)
			return
		}
		// On the record: this call ran because somebody said yes, and an audit
		// of a dangerous action's trail should show the permission next to the
		// act instead of leaving a reader to infer it from the policy level.
		entry.Approved = true
	}

	start := time.Now()
	content, isErr := s.dispatch(ctx, params.Name, params.Arguments, policy)
	entry.Millis = time.Since(start).Milliseconds()

	// A cancelled call is not a result, whatever the tool managed to return.
	// Checked here rather than in each tool because the tools cannot tell the
	// difference: to run_command a killed process is just a process with a
	// non-zero exit status, so it answered exit_code -1 and reported success —
	// true about the process, and a lie about the request.
	//
	// The client has usually been told already, from the other goroutine. What
	// this adds is the log entry, and the answer in the one case reply has not
	// covered: a call with no id, which nothing can cancel by name but which
	// still ends when the connection does.
	if err := ctx.Err(); err != nil {
		refuse(denialCancelled, textContent("cancelled: %v", err), "cancelled")
		return
	}

	// Redaction, at the single point everything leaves through.
	//
	// Here rather than in each tool for the same reason the policy gate is here:
	// a rule that every tool has to remember is a rule the next tool forgets,
	// and the cost of forgetting this one is a password in somebody else's API
	// logs. There is no way to add a leaking tool, because no tool decides this.
	//
	// The action log is redacted too, and that is not belt-and-braces. The trail
	// is written to disk, read by the activity timeline and shown to people who
	// were not in the room — a secret surviving there would have escaped the
	// model only to be filed somewhere more permanent.
	content = s.vault.redactContent(content)
	entry.Args = s.vault.Redact(entry.Args)

	entry.OK = !isErr
	entry.Result = s.vault.Redact(summarizeContent(content))

	// AFTER redaction, so a value the vault already handled is not reported as
	// having escaped — it did not. What is left is what nobody registered, which
	// is exactly the set somebody needs to be told about.
	//
	// The result has already gone out by the time anyone reads the banner, and
	// that is not a flaw in the ordering. The copy left when the tool ran;
	// withholding it here would break the task and protect nothing. The warning
	// exists so that a person can rotate ONE credential in a known window,
	// instead of rotating everything because they cannot tell what was seen.
	s.warnAboutCredentials(content, params.Name)
	kind := denialKind("")
	if isErr {
		kind = denialToolError
		entry.Kind = string(kind)
	}
	s.actions.Add(entry)

	reply(toolCallResult(content, kind))
}

// injectsInput reports whether a tool has to hold the room's controls first.
//
// The set is the tools that put events into X — which is where an agent and a
// person actually collide — plus start_restream and stop_restream, which are
// not input but are held to the same rule for a stronger reason: they publish
// what is on everyone's screen to somewhere outside the room, and starting or
// stopping that while a person is working is not the agent's call to make
// alone.
//
// Deliberately narrow. Installing a package or reading a file while somebody
// works is not a conflict; two hands on the same mouse is. Widening this to
// every state-changing tool would make the agent useless for background work,
// and doing so is a product decision rather than a tidy-up.
//
// This used to be a switch statement here. It is now the RequiresControl field
// on each toolDef, indexed at startup, so that the classification sits beside
// the tool and can be published in tools/list — a client cannot ask for control
// at the right moment if the server keeps the list to itself.
func (s *Server) injectsInput(name string) bool { return s.control[name] }

// buildVersion is what this server reports to an AI host in initialize.
//
// It was the literal "1.0.0", which is not a stale value that drifted — it never
// moved at all, and never would have. Every host that logged the server version
// recorded the same number across every release, so the field that exists to
// answer "which build was I talking to" answered nothing. Worse, it claimed 1.0
// while the project was pre-release, which is the one thing a version string is
// there to prevent somebody assuming.
func buildVersion() string { return version.Version }

// --- Result helpers -------------------------------------------------

// textContent builds a text MCP content block.
func textContent(format string, args ...any) []map[string]any {
	return []map[string]any{{"type": "text", "text": fmt.Sprintf(format, args...)}}
}

// imageContent builds an image MCP content block (base64 PNG).
func imageContent(b64, mime string) []map[string]any {
	return []map[string]any{{"type": "image", "data": b64, "mimeType": mime}}
}

// jsonContent serialises v and returns it as text, for structured answers.
func jsonContent(v any) []map[string]any {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return textContent("could not serialise the result: %v", err)
	}
	return textContent("%s", string(b))
}

// --- Bridge stdio <-> socket (sub-comando -mcp-stdio) ---------------------

// RunBridge wires stdin/stdout to the daemon's socket. The AI host
// spawns "sentineldesk -mcp-stdio"; this process is only a pipe.
func RunBridge(sockPath, level, deny, allow string) error {
	if sockPath == "" {
		return fmt.Errorf("-mcp-sock is required with -mcp-stdio")
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("could not connect to the MCP socket %s: %w", sockPath, err)
	}
	defer conn.Close()

	// THIS connection's restriction, sent before anything from the client is
	// allowed through. The server only applies it if it is stricter than its
	// own, so a bridge can never gain permissions — only give them up.
	if level != "" || deny != "" || allow != "" {
		req := map[string]any{
			"jsonrpc": "2.0", "method": "sentineldesk/policy",
			"params": map[string]string{"level": level, "deny": deny, "allow": allow},
		}
		b, _ := json.Marshal(req)
		if _, err := conn.Write(append(b, '\n')); err != nil {
			return fmt.Errorf("could not set the connection policy: %w", err)
		}
	}

	done := make(chan struct{}, 2)
	go func() { io.Copy(conn, os.Stdin); done <- struct{}{} }()
	go func() { io.Copy(os.Stdout, conn); done <- struct{}{} }()
	<-done
	return nil
}

// cancelReason turns the client's optional reason into the sentence the call
// comes back with. The reason is echoed because the client knows why it
// stopped and the model reading the transcript does not.
func cancelReason(reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" {
		return "cancelled by the client: " + reason
	}
	return "cancelled by the client"
}
