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

// Telling the agent something it did not ask about.
//
// Everything else in this server answers a question. A tool is called, it looks,
// it replies — and between two calls the agent is blind. That is tolerable for
// most of what happens on a desktop, because the agent can look again, but there
// is one thing it cannot recover by looking: it does not know *when* to look.
//
// The case that forced this is control. An agent holding the controls can have
// them taken by a person at any moment — that is the whole point of a shared
// desktop, and the room is deliberately cooperative about it. Until now the
// agent found out by having its next injection refused. A denial where there
// should have been a notice: it had already decided what to type, and the first
// news that the desktop was no longer its to type into arrived as an error in
// the middle of a plan built on the opposite assumption.
//
// The other two are cheaper versions of the same thing. A window appearing is
// how a dialog interrupts, and focus moving is how a person redirects; both are
// already watched for the wait_* tools, and the watcher fans out to any number
// of subscribers, so publishing them costs one more subscription.
//
// Three decisions worth keeping:
//
// Subscription is a tool call, not a capability. The protocol has no negotiation
// for a server pushing something the client did not name, and inventing one
// would be a private extension that hosts have to be taught. A tool is the
// mechanism this server already has for "the agent asks for something": it is
// discoverable in the catalogue, it goes through policy like everything else,
// and a host that never calls it receives nothing. That last part matters —
// a general-purpose host would treat an unsolicited notification as noise, and
// silence is the correct default for a client that did not ask.
//
// Events are namespaced under notifications/sentineldesk/event rather than
// dressed up as a standard method. They are not in the specification and
// pretending otherwise would collide the day the specification defines one.
//
// An event carries the new state, not just the fact of a change. This is the
// opposite of what desktop.Watcher and Room.WatchPresence do internally, and
// the reason is the distance: those hand a "look again" to code that can look
// immediately and cheaply. Here the reader is a model on the other side of a
// socket, and "something about the room changed" would buy it nothing but an
// obligatory round trip. The state is small and already computed.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sentineldesk/desktop/internal/desktop"
)

// eventMethod is the JSON-RPC method every published event arrives as.
const eventMethod = "notifications/sentineldesk/event"

// eventTopic is one thing an agent can ask to be told about.
type eventTopic string

const (
	// topicControl fires when the room's controller changes. The one an agent
	// cannot live without: it is the difference between being told the desktop
	// was taken and finding out when the next click is refused.
	topicControl eventTopic = "control"

	// topicRoom fires when somebody joins or leaves.
	topicRoom eventTopic = "room"

	// topicWindows fires when a window appears or disappears — which is how a
	// dialog interrupts whatever was being done.
	topicWindows eventTopic = "windows"

	// topicFocus fires when the active window changes.
	topicFocus eventTopic = "focus"

	// topicDesktop fires when the current virtual desktop changes.
	topicDesktop eventTopic = "desktop"

	// topicRecording fires when a recording starts, stops, or DIES. The last
	// one is the reason the topic exists: a gst pipeline can be killed, crash,
	// or hit a full disk, and until this event the only way to learn that was
	// to poll get_recording_status — which for one memorable afternoon did not
	// know either. The event carries kind, path, and for a death the reason
	// gst printed.
	topicRecording eventTopic = "recording"
)

// allTopics is both the default subscription and the validation list. Ordered,
// because it is quoted back in error messages and in the tool's reply, and an
// answer that reorders itself between calls reads as though something moved.
var allTopics = []eventTopic{topicControl, topicRoom, topicWindows, topicFocus, topicDesktop, topicRecording}

func parseTopic(s string) (eventTopic, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, t := range allTopics {
		if string(t) == s {
			return t, true
		}
	}
	return "", false
}

func topicNames() []string {
	out := make([]string, 0, len(allTopics))
	for _, t := range allTopics {
		out = append(out, string(t))
	}
	return out
}

// --- the hub -------------------------------------------------------------------

// eventHub is one connection's subscription. It owns the goroutine that watches
// the sources and the write function that reaches this client and no other.
//
// Per connection rather than per server on purpose. Two agents on the same
// socket may want different topics, and one of them unsubscribing must not go
// quiet for the other. It also means the subscription dies with the connection
// with no bookkeeping: closing the socket ends the goroutine, and a dead client
// cannot leak a watcher into the desktop.
type eventHub struct {
	write func(rpcResponse)

	// The sources, held rather than reached through the Server so that a hub
	// built for a test can be given none and still work.
	room    Rooms
	watcher func() *desktop.Watcher
	active  func() (desktop.WindowInfo, bool)
	// recwatch subscribes to the recorder — media.Recorder.Watch — behind a
	// closure for the same testability as the two above. Nil means no recorder.
	recwatch func(func(kind string, detail map[string]any)) func()

	mu      sync.Mutex
	topics  map[eventTopic]bool
	stop    func() // tears down the current sources; nil when not subscribed
	closed  bool
	lastCtl string // the controller as of the last event, to describe transitions

	// rebuildMu serialises source teardown-and-rebuild. Two concurrent
	// rebuilds — a subscribe racing a wait_for_event, or two waits — would
	// interleave "tear down the old" with "store the new" and leak a running
	// source that keeps publishing forever, doubling every event after it.
	// The state lock cannot serve: sources are built with it released, because
	// building one reads the room.
	rebuildMu sync.Mutex

	// waiters are one-shot: the first event matching topic+where is delivered
	// on ch and the waiter is removed. They are subscriptions in every way the
	// sources care about — effectiveLocked folds them in — but invisible to
	// the notification stream, so a wait does not spray events at a client
	// that never subscribed.
	waitSeq int
	waiters map[int]*eventWaiter
}

// eventWaiter is one pending wait_for_event call.
type eventWaiter struct {
	topic eventTopic
	// where filters on the event's detail: every key must be present and its
	// value — stringified — must contain the given substring, case-insensitive.
	// Empty means any event on the topic matches.
	where map[string]string
	ch    chan map[string]any // buffered 1; the hub sends at most once
}

// matches reports whether one event satisfies this waiter's filter.
func (w *eventWaiter) matches(topic eventTopic, detail map[string]any) bool {
	if topic != w.topic {
		return false
	}
	for k, want := range w.where {
		v, ok := detail[k]
		if !ok {
			return false
		}
		if !strings.Contains(strings.ToLower(fmt.Sprint(v)), strings.ToLower(want)) {
			return false
		}
	}
	return true
}

func newEventHub(write func(rpcResponse), room Rooms, watcher func() *desktop.Watcher,
	active func() (desktop.WindowInfo, bool),
	recwatch func(func(kind string, detail map[string]any)) func()) *eventHub {
	return &eventHub{write: write, room: room, watcher: watcher, active: active,
		recwatch: recwatch, topics: map[eventTopic]bool{}}
}

// publish sends one event to the notification stream (if this connection asked
// for the topic) and to every pending waiter it matches (one-shot each). A
// waiter hears an event the stream does not carry: waiting is its own ask.
func (h *eventHub) publish(topic eventTopic, detail map[string]any) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	stream := h.topics[topic]
	var hit []*eventWaiter
	for id, w := range h.waiters {
		if w.matches(topic, detail) {
			hit = append(hit, w)
			delete(h.waiters, id)
		}
	}
	write := h.write
	h.mu.Unlock()

	for _, w := range hit {
		// Buffered 1 and sent at most once, so this cannot block; the select
		// is belt over braces for a waiter that somehow already heard.
		select {
		case w.ch <- detail:
		default:
		}
	}
	if !stream {
		return
	}
	params := map[string]any{"topic": string(topic)}
	for k, v := range detail {
		params[k] = v
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return
	}
	write(rpcResponse{Method: eventMethod, Params: raw})
}

// subscribe replaces the subscription with the given set of topics. Replacing
// rather than adding is the simpler contract to reason about from the far side:
// the agent states what it wants to hear now, and does not have to remember what
// it asked for earlier in the session.
func (h *eventHub) subscribe(topics []eventTopic) []string {
	want := map[eventTopic]bool{}
	for _, t := range topics {
		want[t] = true
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.topics = want
	h.mu.Unlock()

	h.rebuild()

	h.mu.Lock()
	out := make([]string, 0, len(h.topics))
	for _, t := range allTopics {
		if h.topics[t] {
			out = append(out, string(t))
		}
	}
	h.mu.Unlock()
	return out
}

// effectiveLocked is every topic a source has to run for: the explicit
// subscription plus one per pending waiter. Caller holds h.mu.
func (h *eventHub) effectiveLocked() map[eventTopic]bool {
	eff := map[eventTopic]bool{}
	for t, on := range h.topics {
		if on {
			eff[t] = true
		}
	}
	for _, w := range h.waiters {
		eff[w.topic] = true
	}
	return eff
}

// rebuild tears the sources down and starts the set the effective topics need.
// Serialised by rebuildMu — see the field — and safe to call from subscribe,
// from a waiter arriving, and from a waiter giving up.
func (h *eventHub) rebuild() {
	h.rebuildMu.Lock()
	defer h.rebuildMu.Unlock()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	want := h.effectiveLocked()
	old := h.stop
	h.stop = nil
	h.mu.Unlock()

	wantRoom := want[topicControl] || want[topicRoom]
	wantX := want[topicWindows] || want[topicFocus] || want[topicDesktop]
	wantRec := want[topicRecording]

	// Read the room before taking this hub's lock, never while holding it. The
	// presence callback runs on whichever goroutine changed the room, and a hub
	// that reached into the room under its own lock would be one careless
	// callback away from a cycle.
	current := ""
	if h.room != nil && wantRoom {
		current, _ = h.room.Controller()
	}

	if old != nil {
		old()
	}

	h.mu.Lock()
	// Seeded with who is driving *now*, so the first event reports a real
	// change rather than announcing the state as though it had just happened.
	h.lastCtl = current
	h.mu.Unlock()

	var stops []func()
	if wantRoom && h.room != nil {
		stops = append(stops, h.watchRoom())
	}
	if wantX && h.watcher != nil {
		if w := h.watcher(); w != nil {
			stops = append(stops, h.watchX(w))
		}
	}
	if wantRec && h.recwatch != nil {
		stops = append(stops, h.recwatch(func(kind string, detail map[string]any) {
			d := map[string]any{"kind": kind}
			for k, v := range detail {
				d[k] = v
			}
			h.publish(topicRecording, d)
		}))
	}
	stopAll := func() {
		for _, s := range stops {
			s()
		}
	}

	h.mu.Lock()
	// Closed while the sources were being built: they belong to nobody now, so
	// they are torn down rather than stored.
	if h.closed {
		h.mu.Unlock()
		stopAll()
		return
	}
	h.stop = stopAll
	h.mu.Unlock()
}

// addWaiter registers a one-shot wait and makes sure its topic's source runs.
func (h *eventHub) addWaiter(topic eventTopic, where map[string]string) (int, chan map[string]any, bool) {
	w := &eventWaiter{topic: topic, where: where, ch: make(chan map[string]any, 1)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, nil, false
	}
	if h.waiters == nil {
		h.waiters = map[int]*eventWaiter{}
	}
	h.waitSeq++
	id := h.waitSeq
	h.waiters[id] = w
	h.mu.Unlock()
	h.rebuild()
	return id, w.ch, true
}

// removeWaiter forgets a wait that gave up, and lets the sources shrink back.
func (h *eventHub) removeWaiter(id int) {
	h.mu.Lock()
	_, had := h.waiters[id]
	delete(h.waiters, id)
	h.mu.Unlock()
	if had {
		h.rebuild()
	}
}

// unsubscribe stops everything. Idempotent: a client that says it twice is
// making sure, not making a mistake.
func (h *eventHub) unsubscribe() {
	h.mu.Lock()
	stop := h.stop
	h.stop = nil
	h.topics = map[eventTopic]bool{}
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// close ends the hub for good. Called when the connection goes.
func (h *eventHub) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	stop := h.stop
	h.stop = nil
	h.topics = map[eventTopic]bool{}
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (h *eventHub) subscribed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.topics))
	for _, t := range allTopics {
		if h.topics[t] {
			out = append(out, string(t))
		}
	}
	return out
}

// --- sources ---------------------------------------------------------------------

// watchRoom turns presence changes into control and room events.
//
// The room hands over a bare "something changed", so the diffing happens here.
// That is the right place for it: the room has no opinion about which of its
// changes an agent cares about, and this is the only subscriber that needs to
// distinguish "control moved to somebody else" from "control moved to you".
func (h *eventHub) watchRoom() func() {
	// One-slot buffer with a non-blocking send. The callback runs on the
	// goroutine that changed the room — a click handler, a departing session —
	// and must never wait on this one. Coalescing is safe because each event
	// re-reads the room rather than replaying a queue: two changes arriving
	// while the goroutine is busy produce one event describing the end state,
	// which is the state that is true.
	ping := make(chan struct{}, 1)
	done := make(chan struct{})

	cancel := h.room.WatchPresence(func() {
		select {
		case ping <- struct{}{}:
		default:
		}
	})

	go func() {
		for {
			select {
			case <-done:
				return
			case <-ping:
				id, name := h.room.Controller()

				h.mu.Lock()
				previous := h.lastCtl
				changed := previous != id
				h.lastCtl = id
				h.mu.Unlock()

				if changed {
					detail := map[string]any{
						"controller":      id,
						"controller_name": name,
						"previous":        previous,
						"you_have_it":     id == AgentID,
					}
					// The transition the agent has to act on, named rather than
					// left to be inferred from two id comparisons. An agent that
					// reads only this field still behaves correctly.
					//
					// The order is load-bearing, and getting it wrong was a real
					// bug: "released" has to be tested BEFORE "taken from you".
					// An agent calling release_control at the end of a task
					// leaves previous=agent and controller="", which satisfies
					// "previous was the agent and now it is not" — so a run that
					// finished cleanly reported that somebody had snatched the
					// desktop out of its hands, and the runtime marked a
					// successful task as interrupted.
					//
					// Nobody driving is never a theft. It is what the room sits
					// in after anyone lets go, including the agent itself and
					// including a controller whose connection died.
					switch {
					case id == "":
						detail["change"] = "released"
					case id == AgentID:
						detail["change"] = "granted_to_you"
					case previous == AgentID:
						detail["change"] = "taken_from_you"
					default:
						detail["change"] = "moved"
					}
					h.publish(topicControl, detail)
				}

				members := h.room.Members()
				people := make([]map[string]any, 0, len(members))
				for _, m := range members {
					people = append(people, map[string]any{
						"id": m.ID, "name": m.Name, "controller": m.Controller,
					})
				}
				h.publish(topicRoom, map[string]any{
					"members": people,
					"count":   len(people),
					"humans":  h.room.HumansPresent(),
				})
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			close(done)
		})
	}
}

// watchX turns root-window property changes into window, focus and desktop
// events. The watcher is the same one the wait_* tools use, and it fans out, so
// this is a subscription rather than a second connection to the display.
func (h *eventHub) watchX(w *desktop.Watcher) func() {
	ch, cancel := w.Subscribe()
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-done:
				return
			case kind, ok := <-ch:
				if !ok {
					return
				}
				switch kind {
				case desktop.WatchWindows:
					h.publish(topicWindows, map[string]any{
						// No list. The event says the set changed, and
						// list_windows says what it changed to — reading every
						// window's title on every property change would put an
						// X round trip per event behind a channel that exists to
						// avoid exactly that. The one thing the agent needs from
						// here is that now is the moment to look.
						"change": "window_list",
					})
				case desktop.WatchActive:
					// This one DOES carry its subject, unlike the window list
					// above, and the difference is not inconsistency: the
					// property that fired names exactly one window, so
					// describing it is a single query rather than one per
					// window on the desktop.
					//
					// Worth the query because of what the topic is for. An
					// agent subscribed to focus is tracking where it is, and an
					// event that says only "focus moved" makes it call
					// get_active_window to find out where to — so the round trip
					// happens either way, just later, and with a gap in between
					// during which what it believes is wrong.
					detail := map[string]any{"change": "active_window"}
					if h.active != nil {
						if info, ok := h.active(); ok {
							detail["id"] = info.ID
							detail["class"] = info.Class
							detail["title"] = info.Title
						} else {
							// Nothing focused is a state, not a failure: it is
							// what a desktop reports between closing the last
							// window and the next one mapping.
							detail["id"] = nil
						}
					}
					h.publish(topicFocus, detail)
				case desktop.WatchDesktop:
					h.publish(topicDesktop, map[string]any{"change": "current_desktop"})
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			close(done)
		})
	}
}

// --- the context handle -------------------------------------------------------

// The hub reaches the tools the same way the progress reporter does, and for the
// same reason: the context is already threaded through every dispatcher, so a
// per-connection handle arrives without changing a hundred and twenty
// signatures. A Server built without one — every test that does not exercise
// this file — simply finds nothing there.
type eventsKeyType struct{}

var eventsKey eventsKeyType

func withEvents(ctx context.Context, h *eventHub) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, eventsKey, h)
}

func eventsOf(ctx context.Context) *eventHub {
	h, _ := ctx.Value(eventsKey).(*eventHub)
	return h
}

// --- the tools -----------------------------------------------------------------

func (s *Server) buildEventTools() []toolDef {
	return []toolDef{
		{
			Name: "subscribe_events",
			Description: "Ask to be told when something changes, instead of " +
				"polling for it. Events arrive as JSON-RPC notifications with " +
				"the method " + eventMethod + " and a `topic` field. Topics: " +
				"`control` (who is driving — this is how you learn a person " +
				"took the controls away from you mid-task, rather than " +
				"discovering it when your next click is refused), `room` (who " +
				"joined or left), `windows` (a window appeared or closed, which " +
				"is how a dialog interrupts you), `focus` (the active window " +
				"changed), `desktop` (the virtual desktop changed). Pass " +
				"`topics` to choose, or omit it for all of them. Calling this " +
				"again replaces the previous subscription. Nothing is sent " +
				"until you call this, so a host that ignores unknown " +
				"notifications is unaffected.",
			Risk: riskRead,
			InputSchema: schema(map[string]any{
				"topics": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "enum": topicNames()},
					"description": "which topics to receive; omit for all",
				},
			}),
		},
		{
			Name:        "unsubscribe_events",
			Description: "Stop receiving event notifications on this connection.",
			Risk:        riskRead,
			InputSchema: schema(map[string]any{}),
		},
		{
			Name: "wait_for_event",
			Risk: riskRead,
			Description: "Block until ONE event matching a topic (and an optional " +
				"filter) fires, and return that event's detail. This is the " +
				"desktop-wide sibling of browser_wait_until: one call, however " +
				"long the wait, no polling and no turns spent looking — the " +
				"matching is done here, deterministically, and you spend a step " +
				"only on the event itself.\n\n" +
				"Topics are the same as subscribe_events: control, room, windows, " +
				"focus, desktop, recording. `where` narrows the match: every key " +
				"must exist in the event's detail and its value must contain your " +
				"substring, case-insensitively. Examples: topic `recording` with " +
				"where {\"kind\":\"died\"} returns the moment a recording stops " +
				"being written, with the reason; topic `control` with " +
				"{\"change\":\"granted_to_you\"} waits for the controls; topic " +
				"`focus` with {\"title\":\"Save\"} waits for a Save dialog to take " +
				"focus.\n\n" +
				"Returns {ok, waited_ms, event}. ok false means the timeout came " +
				"first — which for a watchdog wait is often the GOOD outcome: " +
				"'ok: false' on a five-minute wait for `recording`/`died` means " +
				"five minutes with no death. This does not touch the " +
				"subscribe_events subscription: waiting is its own ask, and the " +
				"notification stream neither starts nor changes.",
			InputSchema: schema(map[string]any{
				"topic": map[string]any{
					"type": "string", "enum": topicNames(),
					"description": "which topic to wait on",
				},
				"where": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
					"description":          "detail field -> required substring (case-insensitive); omit to match any event on the topic",
				},
				"timeout_ms": pIntDef("give up after this long (default 60000, max 600000)", 60000),
			}, "topic"),
		},
	}
}

func (s *Server) dispatchEvents(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	case "subscribe_events":
		hub := eventsOf(ctx)
		if hub == nil {
			// Not an error the agent can do anything about, and not a lie
			// either: this is what a bridge process with no event plumbing
			// looks like. Saying so beats reporting a subscription that will
			// never deliver.
			return textContent("this connection cannot deliver events"), true, true
		}
		topics := allTopics
		if raw, ok := args["topics"]; ok && raw != nil {
			list, ok := raw.([]any)
			if !ok {
				return textContent("`topics` must be an array of topic names"), true, true
			}
			var chosen []eventTopic
			var bad []string
			for _, item := range list {
				str, _ := item.(string)
				t, ok := parseTopic(str)
				if !ok {
					bad = append(bad, str)
					continue
				}
				chosen = append(chosen, t)
			}
			if len(bad) > 0 {
				sort.Strings(bad)
				return textContent("unknown topic(s) %s. Available: %s",
					strings.Join(bad, ", "), strings.Join(topicNames(), ", ")), true, true
			}
			if len(chosen) == 0 {
				return textContent("`topics` was empty: name at least one of %s, "+
					"or omit it for all", strings.Join(topicNames(), ", ")), true, true
			}
			topics = chosen
		}
		active := hub.subscribe(topics)

		// What was asked for and what can actually be delivered are two
		// different things, and the difference is worth stating plainly. A
		// server with no room cannot report control changes; one with no
		// display cannot report windows. Reporting the subscription as
		// complete would leave the agent waiting on an event that is never
		// coming, which is the failure this whole file exists to remove.
		var unavailable []string
		if s.room == nil {
			for _, t := range []eventTopic{topicControl, topicRoom} {
				if hasTopic(active, t) {
					unavailable = append(unavailable, string(t))
				}
			}
		}
		if w, _ := s.watch(); w == nil {
			for _, t := range []eventTopic{topicWindows, topicFocus, topicDesktop} {
				if hasTopic(active, t) {
					unavailable = append(unavailable, string(t))
				}
			}
		}
		if s.recorder == nil && hasTopic(active, topicRecording) {
			unavailable = append(unavailable, string(topicRecording))
		}
		result := map[string]any{
			"subscribed": active,
			"method":     eventMethod,
		}
		if len(unavailable) > 0 {
			result["unavailable"] = unavailable
			result["note"] = "these topics have no source on this desktop and will not fire"
		}
		return jsonContent(result), false, true

	case "unsubscribe_events":
		hub := eventsOf(ctx)
		if hub == nil {
			return jsonContent(map[string]any{"subscribed": []string{}}), false, true
		}
		hub.unsubscribe()
		return jsonContent(map[string]any{"subscribed": hub.subscribed()}), false, true

	case "wait_for_event":
		hub := eventsOf(ctx)
		if hub == nil {
			return textContent("this connection cannot deliver events"), true, true
		}
		topic, ok := parseTopic(argStr(args, "topic"))
		if !ok {
			return textContent("unknown topic %q. Available: %s",
				argStr(args, "topic"), strings.Join(topicNames(), ", ")), true, true
		}
		if err := s.topicAvailable(topic); err != nil {
			// Refused rather than waited on: a topic with no source here will
			// never fire, and a timeout would report "no event" about an event
			// that was never possible.
			return textContent("wait_for_event: %v", err), true, true
		}
		where := map[string]string{}
		if raw, ok := args["where"].(map[string]any); ok {
			for k, v := range raw {
				sv, ok := v.(string)
				if !ok {
					return textContent("`where` values must be strings; %q is not", k), true, true
				}
				where[k] = sv
			}
		}
		timeout := argInt(args, "timeout_ms")
		if timeout <= 0 {
			timeout = 60000
		}
		// The same ceiling as browser_wait_until, for the same reason: this
		// holds a step open, and a wait that will never end should still get
		// its answer today.
		if timeout > 600000 {
			timeout = 600000
		}
		id, ch, ok := hub.addWaiter(topic, where)
		if !ok {
			return textContent("this connection is closing"), true, true
		}
		started := time.Now()
		timer := time.NewTimer(time.Duration(timeout) * time.Millisecond)
		defer timer.Stop()
		select {
		case detail := <-ch:
			return jsonContent(map[string]any{
				"ok": true, "waited_ms": time.Since(started).Milliseconds(),
				"event": detail,
			}), false, true
		case <-timer.C:
			hub.removeWaiter(id)
			return jsonContent(map[string]any{
				"ok": false, "waited_ms": time.Since(started).Milliseconds(),
				"note": "no matching event before the timeout",
			}), false, true
		case <-ctx.Done():
			hub.removeWaiter(id)
			return textContent("cancelled while waiting"), true, true
		}
	}
	return nil, false, false
}

// topicAvailable says whether this desktop can ever fire the topic.
func (s *Server) topicAvailable(t eventTopic) error {
	switch t {
	case topicControl, topicRoom:
		if s.room == nil {
			return fmt.Errorf("topic %s has no source on this desktop (no room)", t)
		}
	case topicWindows, topicFocus, topicDesktop:
		if w, _ := s.watch(); w == nil {
			return fmt.Errorf("topic %s has no source on this desktop (no display watcher)", t)
		}
	case topicRecording:
		if s.recorder == nil {
			return fmt.Errorf("topic %s has no source on this desktop (no recorder)", t)
		}
	}
	return nil
}

func hasTopic(list []string, t eventTopic) bool {
	for _, s := range list {
		if s == string(t) {
			return true
		}
	}
	return false
}
