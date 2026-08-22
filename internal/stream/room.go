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

package stream

// A room: one capture, shared by everyone watching.
//
// Each client used to raise its own pair of GStreamer pipelines, so two people
// watching meant encoding the same screen twice — double the CPU for the same
// result. Here the pipelines belong to the room: they start when the first
// person arrives, stop when the last one leaves, and every RTP packet is fanned
// out to all the tracks.
//
// That raises the question of who is in charge. The model is a shared console:
// exactly ONE participant holds control at a time and the rest watch. Control is
// asked for and handed over; if the holder disappears it passes to the next.
// Everyone's pointer is broadcast so it is visible what each person is doing —
// which is what turns this into working together rather than taking blind
// turns.

import (
	"encoding/json"
	"fmt"
	"github.com/sentineldesk/desktop/internal/desktop"
	"github.com/sentineldesk/desktop/internal/media"
	"github.com/sentineldesk/desktop/pkg/capability"
	"github.com/sentineldesk/desktop/pkg/config"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

// ControlFree is the controller id when nobody is driving. Named rather than a
// bare empty string: "free" is a state the room can be in for as long as it
// likes — everybody watching, nobody at the controls — and code that compares
// against a literal "" reads like it is handling a missing value instead.
const ControlFree = ""

// roomMember is one participant together with their outbound tracks.
type roomMember struct {
	id      string
	name    string
	session *Session // nil for the agent: it has no WebRTC connection
	agent   bool
	// The member's ink: pointer windows, the viewer overlay and the roster
	// all read it, dealt per member at join (see Join) so the colour follows
	// the person. The agent's is always the reserved violet.
	colour     uint32
	colourSlot int
	video      *webrtc.TrackLocalStaticRTP
	audio      *webrtc.TrackLocalStaticRTP
	joinedAt   time.Time

	// Last known pointer position, so the others can draw it.
	ptrX, ptrY  int
	lastPtrSent time.Time
}

// Room shares one capture among several participants.
type Room struct {
	cfg      config.Config
	strategy media.EncoderStrategy

	mu         sync.RWMutex
	members    map[string]*roomMember
	order      []string // arrival order: decides who inherits control
	controller string
	seq        int

	videoPipe *media.MediaPipeline
	audioPipe *media.MediaPipeline

	// The outbound broadcasts, as gst-launch children with their own capture
	// — pointer included, bitrate their own. See internal/media/restream.go.
	restreams *media.ChildRestreams

	// Other people's pointers, drawn onto the X display so that recordings,
	// screenshots and everyone else's stream show them too. Optional: without
	// the SHAPE extension this stays nil and only the browser overlays remain.
	pointers *desktop.PeerPointers

	// A control request from the agent, waiting for somebody to answer it.
	pending   *controlRequest
	requestNo int

	// A question from the agent, waiting for somebody to answer it. One at a
	// time, like the control request: a queue of dialogs is not something to
	// hand a person who came here to watch a desktop.
	asking     *question
	questionNo int

	// Observers of who is here and who is driving. See WatchPresence: the MCP
	// plane subscribes so that an agent can be told the controls moved instead
	// of discovering it when its next injection is refused.
	presenceSubs map[int]func()
	presenceSeq  int

	// Listeners on the panic button. Separate from presence because they carry
	// who pressed it, and because presence fires constantly — every join, every
	// control change — while this fires when somebody has decided that what is
	// happening must stop.
	abortSubs map[int]func(string)
	abortSeq  int

	// The pause, which is the reversible half of the same idea. Held here so
	// presence can carry it: a second person seeing the agent stopped with no
	// explanation reaches for abort, which is exactly what the pause was
	// avoiding.
	pauseSubs map[int]func(string, bool)
	pauseSeq  int
	pausedBy  string

	// What the people here did, so the agent can read it instead of guessing
	// from a screenshot. See witness.go.
	witness *Witness

	// The room's menu — the shared verb catalogue (§4.6), handed in at wiring
	// time by whoever also gives it to the MCP server, so that both planes
	// read the SAME answer to "does this verb need the controls". Before this
	// the DataChannel carried its own opinions in its switch arms, and they
	// had drifted: screenshots and recording were controller-only for a
	// person and ungated for the agent, two prices for the same sandwich.
	// Optional, like every capability here — see verbNeedsControl for what
	// its absence means.
	verbs *capability.Catalogue

	// The agreed bitrate: the minimum of what each network can carry. Encoding
	// for the best link would break the worst one; the other way round only
	// costs some quality.
	bitrates map[string]int
	lastRate int

	// The stream's quality position — auto / media / high — and the framerate
	// cap it currently means. See quality.go for the rules; the zero values
	// read as "auto at the ceiling", which QualityState normalises.
	// qualityFrames counts whole encoded frames leaving the pipeline (the RTP
	// marker bit, ticked off in writeVideo) — Auto's measure of what the path
	// actually delivers, as opposed to what it was asked for.
	qualityMode   string
	qualityFPS    int
	qualityBy     string
	qualityCancel chan struct{}
	qualityFrames atomic.Int64

	// The stream-status mirror on disk (streamstatus.go): each viewer's
	// self-reported reception, the writer's own frame counter — separate
	// from Auto's, two consumers must not swap one counter — and the loop's
	// cancel.
	viewStats    map[string]viewerStats
	statFrames   atomic.Int64
	statBytes    atomic.Int64
	statusCancel chan struct{}
}

func NewRoom(cfg config.Config, strategy media.EncoderStrategy) *Room {
	pointers, err := desktop.NewPeerPointers(cfg.Display)
	if err != nil {
		log.Printf("room: peer pointers unavailable, browser overlays only: %v", err)
		pointers = nil
	}
	return &Room{
		pointers: pointers,
		// One writer for the whole desktop: the history is a single file and
		// several sessions appending through several descriptors would interleave
		// halves of lines exactly when somebody is reading it.
		witness: NewWitness(),
		cfg:     cfg, strategy: strategy,
		members:  map[string]*roomMember{},
		bitrates: map[string]int{},
		lastRate: cfg.VideoKbps,
		// Auto from birth, explicitly: applyQualityLocked compares this
		// against the mode constants, and an empty string that "reads as
		// auto" somewhere else is exactly how the controller silently never
		// started in the first live test.
		qualityMode: QualityAuto,
		qualityFPS:  cfg.FPS,
	}
}

// SetCapabilities hands the room the shared verb catalogue. Called once at
// wiring time, before any session exists, which is why it takes no lock.
func (r *Room) SetCapabilities(c *capability.Catalogue) { r.verbs = c }

// TrackPointer starts broadcasting the REAL X cursor's position to every
// session, so a viewer can see where whoever is driving — person or agent —
// actually is.
//
// This closes a deliberate gap. The live pipeline captures with
// show-pointer=false so the controller's own browser can draw its cursor
// locally at zero latency (recordings and screenshots keep the real pointer
// — their pipelines say show-pointer=true). The cost was that everyone ELSE
// watched a desktop where things clicked themselves: the controller's
// cursor existed nowhere in their stream. The panel now draws an overlay
// for non-controllers from these positions.
//
// Polling QueryPointer rather than observing input events, deliberately:
// the humans' moves arrive through session 'mm' events but the agent's
// arrive through MCP tools, and an application can warp the pointer with
// nobody moving anything — one 30Hz X roundtrip covers every source
// without coupling this package to any of them. Idle costs nothing on the
// wire: unchanged positions are not sent.
func (r *Room) TrackPointer(position func() (int, int, error)) {
	go func() {
		ticker := time.NewTicker(33 * time.Millisecond)
		defer ticker.Stop()
		lastX, lastY := -1, -1
		for range ticker.C {
			r.mu.RLock()
			var targets []*Session
			for _, m := range r.members {
				if m.session != nil {
					targets = append(targets, m.session)
				}
			}
			r.mu.RUnlock()
			if len(targets) == 0 {
				continue
			}
			x, y, err := position()
			if err != nil || (x == lastX && y == lastY) {
				continue
			}
			lastX, lastY = x, y
			payload, err := json.Marshal(map[string]any{"t": "pointer", "x": x, "y": y})
			if err != nil {
				continue
			}
			for _, s := range targets {
				s.sendOnChannel(string(payload))
			}
		}
	}()
}

// verbNeedsControl is the control-lease gate for the human wire, answered from
// the same catalogue the MCP plane consults — one gate, whoever is asking.
//
// A room built without a catalogue — a test, a bridge that never wired one —
// answers YES for every verb. Conservative in the only direction that is safe
// to default to: requiring a turn that was not needed inconveniences somebody,
// granting one that was needed publishes or records a shared desktop on nobody's
// authority. And it cannot happen silently in production, because main.go sets
// the catalogue unconditionally.
func (r *Room) verbNeedsControl(name string) bool {
	if r.verbs == nil {
		return true
	}
	return r.verbs.RequiresControl(name)
}

// Join adds a session together with its tracks. It returns the assigned id and
// whether that participant ends up holding control.
func (r *Room) Join(s *Session, video, audio *webrtc.TrackLocalStaticRTP) (string, bool, error) {
	r.mu.Lock()

	// Evict members whose connection is already dead before counting. Without
	// this, a few tabs closed without a clean goodbye hold their slots for the
	// full keepalive window — up to 90 seconds — and the room reports itself
	// full to someone standing right there trying to get in.
	for id, m := range r.members {
		// The agent has no WebRTC connection to be alive or dead; it leaves
		// when the MCP plane says so.
		if m.session == nil {
			continue
		}
		if !m.session.connectionAlive() {
			log.Printf("room: %s had a dead connection, freeing its slot", id)
			delete(r.members, id)
			delete(r.bitrates, id)
			for i, v := range r.order {
				if v == id {
					r.order = append(r.order[:i], r.order[i+1:]...)
					break
				}
			}
			if r.controller == id {
				r.controller = ControlFree
			}
			if r.pointers != nil {
				r.pointers.Remove(id)
			}
		}
	}
	// Nothing is promoted into the empty seat: free is a state the room is
	// allowed to sit in, for as long as nobody wants the controls.

	if len(r.members) >= r.cfg.MaxViewers {
		r.mu.Unlock()
		return "", false, fmt.Errorf("the session is full (%d of %d viewers)",
			len(r.members), r.cfg.MaxViewers)
	}

	r.seq++
	id := fmt.Sprintf("u%d", r.seq)

	// The visible number is the lowest one free, not the join counter. With a
	// monotonic counter two people in the room end up as "Viewer 1" and
	// "Viewer 20" after a few reconnects, which reads like a leak even though
	// nothing leaked.
	slot := 1
	for {
		taken := false
		for _, m := range r.members {
			if m.name == fmt.Sprintf("Viewer %d", slot) {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
		slot++
	}
	// The name the viewer asked for wins over the numbered fallback: the
	// workroom panel sends the member's real name, so the roster, the
	// witness lines and the agent's room_state say "Carlos took the
	// controls" instead of "Viewer 2". Standalone clients that send nothing
	// keep the numbers.
	name := fmt.Sprintf("Viewer %d", slot)
	if s != nil && s.displayName != "" {
		name = s.displayName
	}
	// The pointer colour follows the same lowest-free rule as the visible
	// number, but on its own ledger: a renamed member keeps their colour,
	// and a colour freed by a leaver is dealt to the next arrival. First in
	// gets yellow, then cyan, magenta, key — the owner's CMYK order.
	cslot := 0
	for {
		taken := false
		for _, m := range r.members {
			if !m.agent && m.colourSlot == cslot {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
		cslot++
	}
	m := &roomMember{
		id: id, name: name, session: s,
		colour: desktop.PointerColour(cslot), colourSlot: cslot,
		video: video, audio: audio, joinedAt: time.Now(),
	}
	r.members[id] = m
	r.order = append(r.order, id)

	// First in holds control; later arrivals watch until they ask for it.
	//
	// But control must never sit with a dead session. On reload the browser
	// opens the new connection BEFORE the old one's close is detected — the
	// keepalive can take 90 seconds — so without this you end up watching your
	// own ghost: the clicks do nothing and there is no way to tell why.
	//
	// A dead connection does not get to keep the controls — but they go FREE
	// rather than to whoever happened to walk in next. Arriving is not the same
	// as asking, and somebody opening the desktop to watch a colleague work
	// should not find themselves holding it.
	if r.controller != ControlFree && !r.memberAlive(r.controller) {
		log.Printf("room: %s held control on a dead connection; the controls are free",
			r.controller)
		r.controller = ControlFree
	}
	// Whether capture has to start is about the PIPELINE, not the head count.
	// The agent is a member with no video track, so counting members would
	// leave the first real viewer looking at a black screen: with the agent
	// already in the room it is no longer "the first" and capture never began.
	first := r.videoPipe == nil
	isController := r.controller == id
	r.mu.Unlock()

	if first {
		if err := r.startPipelines(); err != nil {
			r.Leave(id)
			return "", false, err
		}
	} else {
		// A newcomer cannot wait for the next keyframe in the GOP: they would
		// stare at garbage or black for seconds.
		r.ForceKeyFrame()
	}
	r.broadcastPresence()
	return id, isController, nil
}

// agentID is fixed: there is one agent plane, and giving it a stable identity
// means humans always see the same name in the participant list instead of a
// new one appearing after every reconnection of the AI host.
const agentID = "agent"

// JoinAgent puts the MCP plane in the room as an ordinary participant.
//
// Before this the agent was invisible: it moved the pointer and typed, and the
// people watching saw a cursor move on its own with no way to tell whether a
// colleague or the model was driving. Being a member gives it a name in the
// list, a marker on screen, and a turn in the control rotation.
//
// It takes no video or audio track — it has no WebRTC connection at all. What it
// shares with a human member is identity and the right to hold control.
func (r *Room) JoinAgent(name string) string {
	r.mu.Lock()
	if _, ok := r.members[agentID]; ok {
		r.mu.Unlock()
		return agentID
	}
	if name == "" {
		name = "AI agent"
	}
	r.members[agentID] = &roomMember{
		id: agentID, name: name, agent: true, colour: desktop.AgentColour,
		joinedAt: time.Now(),
	}
	r.order = append(r.order, agentID)
	// Never made controller on arrival, not even alone in the room. The agent
	// asks for control every time it needs it — request_control grants it at
	// once when nothing is driving, so the cost is one call, and in exchange
	// there is no state in which the agent holds the desktop without having
	// said so.
	r.mu.Unlock()
	log.Printf("room: %s joined (agent)", name)
	r.broadcastPresence()
	return agentID
}

// LeaveAgent takes the agent out of the room.
func (r *Room) LeaveAgent() {
	r.mu.Lock()
	if _, ok := r.members[agentID]; !ok {
		r.mu.Unlock()
		return
	}
	delete(r.members, agentID)
	for i, v := range r.order {
		if v == agentID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	if r.controller == agentID {
		r.controller = ControlFree
	}
	r.mu.Unlock()
	if r.pointers != nil {
		r.pointers.Remove(agentID)
	}
	log.Printf("room: agent left")
	r.broadcastPresence()
}

// HumansPresent reports whether anybody is connected through a browser.
//
// This is what decides whether control arbitration applies to the agent at all:
// with nobody watching there is no one to take turns with, and requiring the
// agent to ask permission from an empty room would break every headless run.
func (r *Room) HumansPresent() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.members {
		if !m.agent && m.session != nil && m.session.connectionAlive() {
			return true
		}
	}
	return false
}

// hasViewers reports whether anybody is actually receiving video. Callers must
// hold the lock.
func (r *Room) hasViewers() bool {
	for _, m := range r.members {
		if m.video != nil {
			return true
		}
	}
	return false
}

// controlRequest is one outstanding "may I drive?" from the agent.
type controlRequest struct {
	id     int
	who    string
	answer chan bool
}

// question is the agent asking a person something and waiting for the answer.
//
// The same shape as controlRequest, one level more general: that one asks a
// fixed question with two answers, this one carries its own text and its own
// options. They are kept separate rather than merged because a control request
// is not a question the agent composed — it is a protocol step the client draws
// as a permission prompt, and giving it free text would let an agent write
// whatever it liked into the dialog that grants it the desktop.
type question struct {
	// secret marks an answer that must not be echoed on a shared screen.
	secret bool

	id      int
	text    string
	options []string
	answer  chan string
}

// AskForControl asks the people in the room to hand control to the agent.
//
// Taking it silently is what a human does, and between humans that is right:
// everybody arrived with the same credential and can see each other's names. An
// agent is different — it can act faster than anyone can react, and the person
// watching did not necessarily invite it. So it asks, and waits.
//
// A timeout is a refusal, not an approval. Nobody answering means nobody is
// looking, and that is the worst moment to start moving somebody's mouse.
func (r *Room) AskForControl(timeout time.Duration) (bool, string) {
	r.mu.Lock()
	if _, ok := r.members[agentID]; !ok {
		r.mu.Unlock()
		return false, "the agent is not in the room"
	}
	if r.controller == agentID {
		r.mu.Unlock()
		return true, "you already had control"
	}
	// Nobody is driving: taking it interrupts no one, so there is nothing to
	// ask permission for.
	if r.controller == "" {
		r.mu.Unlock()
		r.TakeControl(agentID)
		return true, "nobody was driving"
	}
	if r.pending != nil {
		r.mu.Unlock()
		return false, "a request is already waiting for an answer"
	}
	r.requestNo++
	req := &controlRequest{id: r.requestNo, who: r.members[agentID].name,
		answer: make(chan bool, 1)}
	r.pending = req
	targets := r.snapshotMembers()
	r.mu.Unlock()

	msg, err := json.Marshal(map[string]any{
		"t": "control_request", "id": req.id, "who": req.who,
		"seconds": int(timeout.Seconds()),
	})
	if err == nil {
		for _, m := range targets {
			if m.session != nil {
				m.session.sendOnChannel(string(msg))
			}
		}
	}

	var granted bool
	var reason string
	select {
	case granted = <-req.answer:
		if granted {
			reason = "a person granted it"
		} else {
			reason = "a person refused"
		}
	case <-time.After(timeout):
		granted, reason = false, "nobody answered in time"
	}

	r.mu.Lock()
	if r.pending == req {
		r.pending = nil
	}
	r.mu.Unlock()

	// Tell the browsers the prompt is over, whichever way it went, so a stale
	// dialog does not sit on somebody's screen.
	if done, err := json.Marshal(map[string]any{
		"t": "control_request_done", "id": req.id, "granted": granted,
	}); err == nil {
		for _, m := range targets {
			if m.session != nil {
				m.session.sendOnChannel(string(done))
			}
		}
	}

	if granted {
		r.TakeControl(agentID)
	}
	return granted, reason
}

// AnswerControlRequest records a person's decision.
func (r *Room) AnswerControlRequest(id int, granted bool) {
	r.mu.Lock()
	req := r.pending
	r.mu.Unlock()
	if req == nil || (id != 0 && req.id != id) {
		return
	}
	select {
	case req.answer <- granted:
	default: // already answered by somebody else; first reply wins
	}
}

// memberAlive reports whether a member can still act. The agent always can:
// it has no connection to lose. Callers must hold the lock.
func (r *Room) memberAlive(id string) bool {
	m, ok := r.members[id]
	if !ok {
		return false
	}
	if m.agent {
		return true
	}
	return m.session != nil && m.session.connectionAlive()
}

// Controller returns the id and name of whoever is driving.
func (r *Room) Controller() (string, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.members[r.controller]; ok {
		return m.id, m.name
	}
	return "", ""
}

// Leave removes a participant, and shuts the capture down if they were last.
func (r *Room) Leave(id string) {
	r.mu.Lock()
	if _, ok := r.members[id]; !ok {
		r.mu.Unlock()
		return
	}
	delete(r.members, id)
	delete(r.bitrates, id)
	delete(r.viewStats, id)
	if r.pointers != nil {
		r.pointers.Remove(id)
	}
	for i, v := range r.order {
		if v == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	// The controller left, so the controls are free. They used to pass to the
	// longest-present member, which handed the desktop to somebody who never
	// asked for it; FREE is a state anyone can claim when they actually want it.
	if r.controller == id {
		r.controller = ControlFree
	}
	// Likewise on the way out: the agent alone in the room is nobody to encode
	// for, so capture stops when the last member holding a video track leaves.
	empty := !r.hasViewers()
	r.mu.Unlock()

	if empty {
		if r.pointers != nil {
			r.pointers.Clear()
		}
		r.stopPipelines()
		return
	}
	r.broadcastPresence()
}

// --- pipelines ---------------------------------------------------------------

func (r *Room) startPipelines() error {
	var err error
	r.videoPipe, err = media.NewMediaPipeline("video", r.videoDesc(), r.writeVideo)
	if err != nil {
		return err
	}
	r.videoPipe.Strategy = r.strategy
	// The restreams are CHILDREN of their own, not branches of this pipeline:
	// they carry the pointer the live capture deliberately leaves out, and
	// they encode at their own fixed rate instead of following the room's
	// congestion minimum. See internal/media/restream.go for the full story.
	r.restreams = media.NewChildRestreams(
		r.cfg.Display, r.cfg.AudioDevice, r.cfg.VideoKbps, r.cfg.FPS)
	// A destination that fails on its own — a rejected key, a receiver that went
	// away — has to reach the toolbar, or the badge says "live" to nobody.
	r.restreams.OnError = func(id string, err error) {
		r.broadcastRestreams(fmt.Sprintf("%s: %v", id, err))
	}
	if err := r.videoPipe.Start(); err != nil {
		return fmt.Errorf("video pipeline: %w", err)
	}
	// The quality position survives the capture: a room that chose Media
	// before everyone left is still Media when the next viewer arrives, and
	// Auto gets its controller back against the new pipeline. This function
	// runs outside r.mu — Join releases the lock before starting pipelines —
	// so the quality state takes it here.
	r.mu.Lock()
	r.applyQualityLocked()
	// The status mirror runs for exactly as long as the capture does.
	if r.statusCancel != nil {
		close(r.statusCancel)
	}
	r.statusCancel = make(chan struct{})
	go r.streamStatusLoop(r.statusCancel)
	r.mu.Unlock()

	r.audioPipe, err = media.NewMediaPipeline("audio", r.audioDesc(), r.writeAudio)
	if err != nil {
		log.Printf("room: audio unavailable: %v", err) // the video carries on
		r.audioPipe = nil
	} else if err := r.audioPipe.Start(); err != nil {
		log.Printf("room: audio unavailable: %v", err)
		r.audioPipe = nil
	}
	log.Printf("room: capture started (encoder %s)", r.strategy.Name)
	return nil
}

func (r *Room) stopPipelines() {
	// The quality controller first: it holds a pointer to the pipeline about
	// to be torn down, and the cancel channel is the promise that it stops
	// touching it.
	r.mu.Lock()
	if r.qualityCancel != nil {
		close(r.qualityCancel)
		r.qualityCancel = nil
	}
	if r.statusCancel != nil {
		close(r.statusCancel)
		r.statusCancel = nil
	}
	r.mu.Unlock()
	// The broadcast children next, each getting its EOS: the room emptying
	// ends its streams too, on purpose — an unattended broadcast of an empty
	// desktop is a camera someone forgot running.
	if r.restreams != nil {
		r.restreams.StopAll()
		r.restreams = nil
	}
	if r.videoPipe != nil {
		r.videoPipe.Stop()
		r.videoPipe = nil
	}
	if r.audioPipe != nil {
		r.audioPipe.Stop()
		r.audioPipe = nil
	}
	log.Printf("room: empty, capture stopped")
}

// writeVideo fans each RTP packet out to every track.
//
// A write error is deliberately not propagated: if one client is falling over,
// everyone else must keep watching. The broken session cleans itself up through
// its own PeerConnection state change.
func (r *Room) writeVideo(pkt []byte) {
	// The marker bit closes a frame: this is where Auto learns what the
	// pipeline actually delivered, at the cost of one bit test on a packet
	// already in hand.
	if len(pkt) > 1 && pkt[1]&0x80 != 0 {
		r.qualityFrames.Add(1)
		r.statFrames.Add(1)
	}
	r.statBytes.Add(int64(len(pkt)))
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.members {
		if m.video != nil {
			m.video.Write(pkt)
		}
	}
}

func (r *Room) writeAudio(pkt []byte) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.members {
		if m.audio != nil {
			m.audio.Write(pkt)
		}
	}
}

func (r *Room) ForceKeyFrame() {
	r.mu.RLock()
	p := r.videoPipe
	r.mu.RUnlock()
	if p != nil {
		p.ForceKeyFrame()
	}
}

// ReportBitrate records one participant's network estimate and applies the
// minimum across everyone to the shared encoder.
func (r *Room) ReportBitrate(id string, kbps int) {
	r.mu.Lock()
	r.bitrates[id] = kbps
	min := 0
	for _, v := range r.bitrates {
		if min == 0 || v < min {
			min = v
		}
	}
	// A floor. The estimator reads a client that cannot DECODE fast enough as a
	// congested network and keeps lowering the target; below this the picture is
	// unusable and dropping further helps nobody — it only guarantees that the
	// client which was struggling now has nothing worth decoding either.
	if floor := r.cfg.MinVideoKbps; floor > 0 && min < floor {
		min = floor
	}
	pipe := r.videoPipe
	last := r.lastRate
	// Hysteresis: a change under 10% is not worth disturbing the encoder for.
	if min == 0 || pipe == nil || abs(min-last)*10 < last {
		r.mu.Unlock()
		return
	}
	r.lastRate = min
	r.mu.Unlock()

	pipe.SetBitrateKbps(min)
	log.Printf("room: bitrate %d kbps (minimum across %d participants)", min, len(r.bitrates))
}

// --- external destinations ----------------------------------------------------

// StartRestream sends this desktop somewhere else as well.
//
// It reuses the encode the room is already producing, so going live costs a mux
// and a socket rather than a second capture. Everyone in the room is told, on
// purpose: a session being broadcast to the internet is not something to find
// out about afterwards.
func (r *Room) StartRestream(t media.RestreamTarget) error {
	r.mu.RLock()
	streams := r.restreams
	r.mu.RUnlock()
	if streams == nil {
		return fmt.Errorf("nothing is being captured yet; someone has to be watching first")
	}
	if err := streams.Start(t); err != nil {
		return err
	}
	r.broadcastRestreams("")
	return nil
}

func (r *Room) StopRestream(id string) error {
	r.mu.RLock()
	streams := r.restreams
	r.mu.RUnlock()
	if streams == nil {
		return fmt.Errorf("nothing is being captured")
	}
	if err := streams.Stop(id); err != nil {
		return err
	}
	r.broadcastRestreams("")
	return nil
}

// Restreams reports the destinations currently running.
func (r *Room) Restreams() []media.RestreamInfo {
	r.mu.RLock()
	streams := r.restreams
	r.mu.RUnlock()
	if streams == nil {
		return []media.RestreamInfo{}
	}
	return streams.List()
}

// CanRestream reports whether an external destination can be fed at all.
// Always, now: the restream child brings its own H.264 encoder, so even a
// session whose live encode is VP8 can broadcast.
func (r *Room) CanRestream() bool {
	return true
}

func (r *Room) broadcastRestreams(problem string) {
	list := r.Restreams()
	r.mu.RLock()
	targets := r.snapshotMembers()
	r.mu.RUnlock()

	msg := map[string]any{"t": "restreams", "list": list}
	if problem != "" {
		msg["error"] = problem
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for _, m := range targets {
		if m.session == nil {
			continue // the agent asks for this through its own tool
		}
		m.session.sendOnChannel(string(payload))
	}
}

// --- control ------------------------------------------------------------------

func (r *Room) IsController(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.controller == id
}

// TakeControl hands control to whoever asks. This is deliberately cooperative:
// there is no hierarchy, because everyone got in with the same credential.
// Whoever was watching sees the change in their toolbar immediately.
func (r *Room) TakeControl(id string) bool {
	r.mu.Lock()
	if _, ok := r.members[id]; !ok {
		r.mu.Unlock()
		return false
	}
	if r.controller == id {
		r.mu.Unlock()
		return true
	}
	previous := r.controller
	r.controller = id
	r.mu.Unlock()
	// The new controller's marker goes away (their pointer is now the real X
	// pointer); the previous one gets a marker as soon as they move.
	if r.pointers != nil {
		r.pointers.Remove(id)
		_ = previous
	}
	r.broadcastPresence()
	return true
}

// Abort is the panic button: stop what the agent is doing, and take the wheel.
//
// The two halves are not interchangeable and neither is optional. Killing the
// running work stops what is happening now; taking the controls stops what
// happens next. An abort that only killed the jobs would leave an agent free to
// start the same thing again a second later, which is not a stop — it is a
// pause the agent did not agree to.
//
// Notice what it does NOT do: it does not disconnect the agent, and it does not
// forbid it from reading. That is deliberate, and it is the difference between
// supervising something and merely switching it off. The person pressing this
// usually has something to say — that was the wrong directory, that is not what
// I meant, look at what I just did instead — and an agent that has been cut off
// cannot be told any of it. So it keeps its eyes and loses its hands.
func (r *Room) Abort(by string) {
	r.mu.Lock()
	who := by
	if m, ok := r.members[by]; ok && m.name != "" {
		who = m.name
	}
	// Only claim the controls if a member pressed it. An abort raised from
	// somewhere that is not in the room should still stop the work.
	if _, ok := r.members[by]; ok {
		r.controller = by
	}
	subs := make([]func(string), 0, len(r.abortSubs))
	for _, fn := range r.abortSubs {
		subs = append(subs, fn)
	}
	r.mu.Unlock()

	if r.pointers != nil {
		r.pointers.Remove(by)
	}
	r.broadcastPresence()

	// Outside the lock: a subscriber reaches into the MCP server, which signals
	// processes and waits on tmux. None of that belongs under the mutex that
	// every video frame's bitrate report also wants.
	for _, fn := range subs {
		fn(who)
	}
}

// Notice puts an informational banner on everybody's screen.
//
// Fire and forget, and that is the difference from AskHuman beside it. A
// question blocks the agent until somebody answers, because the answer changes
// what happens next. A notice changes nothing: whatever it is about has already
// happened, and holding the agent hostage to an acknowledgement would punish the
// person for reading it.
//
// So there is no reply channel, no timeout to wait out, and no way for this to
// fail in a way the caller has to handle. Nobody in the room means nobody is
// told, which is the correct outcome — a warning exists to reach a person, and
// with no person there is nothing to reach.
func (r *Room) Notice(kind string, items []string) {
	msg, err := json.Marshal(map[string]any{
		"t": "notice", "kind": kind, "items": items,
	})
	if err != nil {
		return
	}
	r.tellEveryone(string(msg))
}

// tellEveryone sends one already-encoded message to every member that can
// receive one, and is the only place that decides what "can receive" means.
//
// It exists because the two callers below got that decision wrong in opposite
// directions. Notice was calling snapshotMembers WITHOUT the lock it documents
// as required — a race against every join and every leave, the kind that
// surfaces as a corrupted map in production rather than as a failure in a test.
// And the nil check is the one delivery_test.go records as having taken the
// whole daemon down: the agent is a member with no Session, it is present
// whenever the agent is talking, and dereferencing it panics.
//
// Written once so a third broadcaster cannot rediscover either of them.
func (r *Room) tellEveryone(msg string) {
	for _, s := range r.receivers() {
		s.sendOnChannel(msg)
	}
}

// receivers is every member that has somewhere for a message to go.
//
// Split out from tellEveryone so the selection can be asserted on its own: the
// send itself disappears into a DataChannel a test cannot watch, while WHO was
// selected is the part that has been wrong before and is the part worth
// pinning.
func (r *Room) receivers() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Session, 0, len(r.members))
	for _, m := range r.members {
		if m.session == nil {
			continue // the agent, which has no WebRTC connection at all
		}
		out = append(out, m.session)
	}
	return out
}

// ChatTurn is one line of the conversation between the agent and a person.
//
// Deliberately not the wire shape: AgentChat builds that below, the same way
// the internal `question` struct is a different thing from the "question"
// message it produces. Keeping them apart means a field can be added here for
// the daemon's own use without silently changing what browsers parse.
type ChatTurn struct {
	// ID ties a question to its answer. Minted by whoever started the exchange
	// — which is not always this process: when the agent runtime answers from
	// its own console the daemon never sees the question being put, only the
	// record of it, so the room cannot be the one counting.
	ID string

	// Role is "agent", "human" or "system". The third one is not decoration:
	// a question that timed out has no human turn, and a chat panel that showed
	// the question alone would leave the reader believing an answer is still
	// coming. Something has to say that the exchange ended without one.
	Role string

	// Via records where the turn happened — "console" for the terminal the
	// person typed the goal into, "room" for the browser dialog. A transcript
	// that cannot say where somebody was answering from cannot explain why a
	// dialog nobody saw timed out.
	Via string

	Text string

	// Options are the buttons a question offered, if any. Empty for free text,
	// and absent from the wire in that case rather than sent as null.
	Options []string
}

// AgentChat delivers one turn of the agent's conversation to the room as a
// RECORD, not as a prompt.
//
// This is Notice's shape and Notice's reasoning — fire and forget, no reply
// channel, no timeout, nothing for the caller to handle — applied to a
// different problem. What forced it into existence: ask_human fanned its
// question out over the DataChannel as a "question" message, every browser drew
// a modal dialog, and in the case that actually happens the person was not in a
// browser at all. They had typed the goal into the agent's terminal and were
// watching that terminal. The dialog opened on a tab nobody was looking at and
// timed out two minutes later, and the agent was told nobody answered — which
// was true, and useless.
//
// So the two halves are separated. Whoever has the person's attention asks them
// and waits; the room is told what was said, and told it without being asked to
// do anything about it. Sending continues, displaying stops.
//
// The message type is "agent_chat", deliberately NOT "question". An older
// client that has never heard of this ignores an unknown `t` and draws nothing,
// which is exactly the required behaviour; had this reused "question" the same
// client would have popped the dialog this exists to stop, and a record would
// have become a prompt again — with no one to close it, because nothing is
// waiting for an answer.
//
// Nobody in the room means nobody is told, and that is correct rather than a
// failure: this is a transcript for whoever is watching, and with nobody
// watching there is nothing to transcribe to. The agent's conversation is
// unaffected either way, which is the whole point of it not blocking.
func (r *Room) AgentChat(turn ChatTurn) {
	msg, err := turn.encode()
	if err != nil {
		return
	}
	r.tellEveryone(string(msg))
}

// AgentChatType is the `t` a browser matches on. Named rather than left as a
// literal in one function because it is a contract with the web client, and the
// value is the whole design: NOT "question", so a client that predates this
// draws nothing instead of popping a dialog nothing is waiting on.
const AgentChatType = "agent_chat"

// encode builds the wire form of a turn. Separated from the send so the shape
// can be asserted against the real code rather than against a copy of it in a
// test — a duplicated payload builder would keep passing after the two drifted
// apart, which is the failure mode this whole file is about.
func (t ChatTurn) encode() ([]byte, error) {
	payload := map[string]any{
		"t": AgentChatType, "id": t.ID, "role": t.Role,
		"text": t.Text,
		// When the daemon saw it, in milliseconds. A panel that renders these in
		// arrival order gets it right today and wrong the moment two planes both
		// emit; the timestamp is what survives that.
		"at": time.Now().UnixMilli(),
	}
	if t.Via != "" {
		payload["via"] = t.Via
	}
	// Omitted rather than sent as null: a client checking `msg.options.length`
	// should not have to know the difference between "no buttons" and "the
	// field is there but empty".
	if len(t.Options) > 0 {
		payload["options"] = t.Options
	}
	return json.Marshal(payload)
}

// Pause holds the agent without destroying its work; Resume lets it go.
//
// One function for both, because they are one decision with two values and
// splitting them invites the state to disagree with itself — a Resume that
// forgot to clear the flag, a Pause that fired listeners the other did not.
//
// Unlike Abort it does NOT take the controls. Pausing is not a claim on the
// desktop, it is a request to hold still; somebody who also wants to drive can
// take control, which is a separate act with its own button and its own meaning
// in the log.
func (r *Room) Pause(by string, on bool) {
	r.mu.Lock()
	who := by
	if m, ok := r.members[by]; ok && m.name != "" {
		who = m.name
	}
	if on {
		r.pausedBy = by
	} else {
		r.pausedBy = ""
	}
	subs := make([]func(string, bool), 0, len(r.pauseSubs))
	for _, fn := range r.pauseSubs {
		subs = append(subs, fn)
	}
	r.mu.Unlock()

	r.broadcastPresence()
	for _, fn := range subs {
		fn(who, on)
	}
}

// Paused reports whether the room is holding, and who asked.
func (r *Room) Paused() (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.pausedBy == "" {
		return "", false
	}
	if m, ok := r.members[r.pausedBy]; ok && m.name != "" {
		return m.name, true
	}
	return r.pausedBy, true
}

// OnPause registers a listener. Modelled on OnAbort, and separate from it
// because the two say opposite things about whether the work survives.
func (r *Room) OnPause(fn func(who string, on bool)) func() {
	if fn == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.pauseSubs == nil {
		r.pauseSubs = map[int]func(string, bool){}
	}
	id := r.pauseSeq
	r.pauseSeq++
	r.pauseSubs[id] = fn
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.pauseSubs, id)
			r.mu.Unlock()
		})
	}
}

// NameOf is the display name of a member — "Viewer 2", or the agent's name —
// falling back to the raw id for somebody who has already left.
//
// The history is read by people and by a model, and neither of them knows what
// u7 is. A record that cannot say who did something is half a record.
// Rename changes how one member is shown, everywhere a name is read from the
// room: the roster (via presence), the witness log's future lines, and the
// pointer tag drawn into X on the next move. The session validated the name;
// the room only records it.
func (r *Room) Rename(id, name string) {
	r.mu.Lock()
	m, ok := r.members[id]
	if !ok || name == "" {
		r.mu.Unlock()
		return
	}
	m.name = name
	r.mu.Unlock()
	r.broadcastPresence()
}

func (r *Room) NameOf(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.members[id]; ok && m.name != "" {
		return m.name
	}
	return id
}

// OnAbort registers a listener for the panic button, and returns a function
// that unregisters it. Modelled on WatchPresence, for the same reason: the MCP
// plane has no other way to be TOLD anything.
func (r *Room) OnAbort(fn func(who string)) func() {
	if fn == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.abortSubs == nil {
		r.abortSubs = map[int]func(string){}
	}
	id := r.abortSeq
	r.abortSeq++
	r.abortSubs[id] = fn
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.abortSubs, id)
			r.mu.Unlock()
		})
	}
}

// ReleaseControl passes it to the next member, or to nobody if alone.
func (r *Room) ReleaseControl(id string) {
	r.mu.Lock()
	if r.controller != id {
		r.mu.Unlock()
		return
	}
	// Released means FREE, not handed on. Passing it to the next member made
	// "I am done with this" indistinguishable from "you are up now", and put
	// the desktop in the hands of somebody who might only have been watching.
	// Whoever wants it next asks for it — including the agent, which is the
	// whole point: it releases when it finishes a task and asks again for the
	// next one, instead of holding the controls between errands.
	r.controller = ControlFree
	r.mu.Unlock()
	r.broadcastPresence()
}

// AskHuman puts a question to the room and waits for somebody to answer it.
//
// This is the mechanism behind the runtime's `ask` role: a person granted the
// agent control while they were working, so whether a step happens visibly or
// in the background is a question about THEIR attention, and the agent is not
// in a position to guess. It is also the general form of something the room
// already did in one special case — AskForControl — and the only reason that
// one is not built on this is that its text is not the agent's to write.
//
// A timeout is not an answer. Nobody responding means nobody is looking, and
// the caller is told that rather than being handed a default that reads like a
// decision somebody made.
func (r *Room) AskHuman(text string, options []string, timeout time.Duration) (string, error) {
	return r.ask(text, options, timeout, false)
}

// AskSecret is AskHuman for an answer nobody else should read.
//
// A separate entry point rather than a flag on the public one, because the two
// have different callers and only one of them is reachable by the agent. The
// agent asks questions; the DAEMON asks for a secret, on the agent's behalf and
// without the answer ever going back to it. A model that could set this flag
// itself would be able to make any question look like a password prompt, which
// is a small social-engineering surface nobody needs.
func (r *Room) AskSecret(text string, timeout time.Duration) (string, error) {
	return r.ask(text, nil, timeout, true)
}

func (r *Room) ask(text string, options []string, timeout time.Duration, secret bool) (string, error) {
	r.mu.Lock()
	if _, ok := r.members[agentID]; !ok {
		r.mu.Unlock()
		return "", fmt.Errorf("the agent is not in the room")
	}
	human := false
	for _, m := range r.members {
		if !m.agent && m.session != nil && m.session.connectionAlive() {
			human = true
			break
		}
	}
	if !human {
		r.mu.Unlock()
		return "", fmt.Errorf("nobody is here to ask")
	}
	if r.asking != nil {
		r.mu.Unlock()
		return "", fmt.Errorf("a question is already waiting for an answer")
	}
	r.questionNo++
	q := &question{id: r.questionNo, text: text, options: options,
		secret: secret, answer: make(chan string, 1)}
	r.asking = q
	targets := r.snapshotMembers()
	r.mu.Unlock()

	// Whether the people at the DESKTOP are the ones who have to answer this.
	//
	// True for everything that gets here, and that is a statement about the
	// architecture rather than a placeholder. A question the agent's own console
	// can answer never reaches this function at all: the runtime holds the
	// person's attention, answers there, and tells the room what was said
	// through AgentChat. So a "question" message on this channel is by
	// construction one that nobody else is going to answer — an unattended run,
	// or the daemon's own secret prompt.
	//
	// It is sent explicitly anyway, because the client cannot derive it. Without
	// the flag the browser has to choose between drawing every question (the
	// defect: a modal on a tab nobody was looking at, timing out after two
	// minutes while the person waited in front of a terminal) and drawing none
	// (the worse defect: an unattended run with nowhere to answer, silent). And
	// when a console-aware route does land in the daemon, this is the one line
	// that has to become conditional — better a field that reads as constant
	// today than a client guessing from `secret` and the phase of the moon.
	desktopAnswers := true

	msg, err := json.Marshal(map[string]any{
		"t": "question", "id": q.id, "text": q.text,
		"desktop": desktopAnswers,
		"options": q.options, "seconds": int(timeout.Seconds()),
		// Whether the answer is a secret, so the browser can mask the field.
		//
		// Carried on the question rather than guessed at by the client from the
		// wording: "type the password for db_root" is recognisable to a person
		// and not reliably to a regexp, and a field that fails to mask once is
		// worse than one that never claimed to.
		"secret": q.secret,
	})
	if err == nil {
		for _, m := range targets {
			if m.session != nil {
				m.session.sendOnChannel(string(msg))
			}
		}
	}

	var answer string
	var failure error
	select {
	case answer = <-q.answer:
	case <-time.After(timeout):
		failure = fmt.Errorf("nobody answered in %s", timeout)
	}

	r.mu.Lock()
	if r.asking == q {
		r.asking = nil
	}
	r.mu.Unlock()

	// Take the prompt off everyone's screen whichever way it ended, so a stale
	// dialog does not outlive the question.
	if done, err := json.Marshal(map[string]any{
		"t": "question_done", "id": q.id, "answer": answer,
	}); err == nil {
		for _, m := range targets {
			if m.session != nil {
				m.session.sendOnChannel(string(done))
			}
		}
	}
	return answer, failure
}

// AnswerQuestion delivers a person's answer. Anyone in the room may answer, for
// the same reason anyone may answer a control request: they all got in with the
// same credential, and requiring one particular person leaves the agent stuck
// the moment that person steps away.
func (r *Room) AnswerQuestion(id int, answer string) {
	r.mu.Lock()
	q := r.asking
	r.mu.Unlock()
	if q == nil || q.id != id {
		return // a stale dialog, or an answer to a question already over
	}
	select {
	case q.answer <- answer:
	default: // already answered; first one wins
	}
}

// pointerRate throttles the broadcast of other people's pointers. The client
// sends up to 120 positions a second; relaying all of them to every participant
// floods the
// DataChannel for what is, in the end, an ornament. At 25/s the movement
// already reads as fluid.
const pointerRate = 40 * time.Millisecond

// UpdatePointer records where a participant's pointer is and broadcasts it to
// the others. This is what makes it visible what someone else is pointing at.
func (r *Room) UpdatePointer(id string, x, y int) {
	r.mu.Lock()
	m, ok := r.members[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	m.ptrX, m.ptrY = x, y
	if time.Since(m.lastPtrSent) < pointerRate {
		r.mu.Unlock()
		return
	}
	m.lastPtrSent = time.Now()
	name := m.name
	colour := m.colour
	isController := r.controller == id
	isAgent := m.agent
	// With a single participant there is nobody to tell.
	if len(r.members) < 2 {
		r.mu.Unlock()
		return
	}
	targets := r.snapshotMembers()
	r.mu.Unlock()

	// Draw it on the desktop itself. For people the rule is "only when NOT
	// driving": the controller's pointer already IS the X pointer, so a marker
	// on top would be a duplicate.
	//
	// The agent is the deliberate exception — it gets a marker even while
	// driving. A duplicate is a small price; a pointer moving on its own with
	// nothing to say who is behind it reads as a fault, and that is exactly the
	// moment somebody needs to know a model is at the controls.
	if p := r.pointers; p != nil {
		switch {
		case isAgent:
			p.SetColoured(id, name, x, y, desktop.AgentColour)
		case isController:
			p.Remove(id)
		default:
			p.SetColoured(id, name, x, y, colour)
		}
	}

	msg, err := json.Marshal(map[string]any{
		"t": "peer_cursor", "id": id, "name": name, "x": x, "y": y,
		"color": fmt.Sprintf("#%06x", colour),
	})
	if err != nil {
		return
	}
	for _, t := range targets {
		if t.id == id || t.session == nil {
			continue
		}
		t.session.sendOnChannel(string(msg))
	}
}

// --- presence -----------------------------------------------------------------

type MemberInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Controller bool   `json:"controller"`
	Agent      bool   `json:"agent"`
	// Color is the member's ink as CSS hex, the same colour their pointer
	// wears on the desktop — so every surface that shows who is who agrees.
	Color   string `json:"color"`
	Seconds int    `json:"seconds"`
}

func (r *Room) Members() []MemberInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]MemberInfo, 0, len(r.members))
	for _, id := range r.order {
		m := r.members[id]
		if m == nil {
			continue
		}
		out = append(out, MemberInfo{
			ID: m.id, Name: m.name, Controller: r.controller == m.id,
			Agent:   m.agent,
			Color:   fmt.Sprintf("#%06x", m.colour),
			Seconds: int(time.Since(m.joinedAt).Seconds()),
		})
	}
	return out
}

// snapshotMembers copies the member list under the caller's existing lock.
func (r *Room) snapshotMembers() []*roomMember {
	out := make([]*roomMember, 0, len(r.members))
	for _, m := range r.members {
		out = append(out, m)
	}
	return out
}

// WatchPresence registers a callback for every change in who is here and who is
// driving, and returns the function that unregisters it.
//
// The callback is given nothing. That is deliberate and it is the same
// reasoning as desktop.Watcher's: an event that carries state has to be
// delivered in order and cannot be dropped, while an event that means "look
// again" can be coalesced, delivered late, or missed entirely without the
// observer reaching a different conclusion — it re-reads Members() and
// Controller() and sees the truth, not a stale copy of it.
//
// This exists for the MCP plane, which until now could not be *told* anything.
// An agent could ask room_state who was driving, but if a person took the
// controls in the middle of a task the agent found out by having its next
// injection refused — an error where there should have been a notice. Every
// caller of broadcastPresence is a moment a human client already gets told;
// this puts the other plane on the same footing.
//
// The callback runs on the goroutine that made the change, so it must not
// block and must not call back into the room. The one subscriber today hands
// off to a channel immediately.
func (r *Room) WatchPresence(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.presenceSubs == nil {
		r.presenceSubs = map[int]func(){}
	}
	id := r.presenceSeq
	r.presenceSeq++
	r.presenceSubs[id] = fn
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.presenceSubs, id)
			r.mu.Unlock()
		})
	}
}

// broadcastPresence tells everyone who is present and who holds control.
func (r *Room) broadcastPresence() {
	members := r.Members()
	r.mu.RLock()
	targets := r.snapshotMembers()
	watchers := make([]func(), 0, len(r.presenceSubs))
	for _, fn := range r.presenceSubs {
		watchers = append(watchers, fn)
	}
	r.mu.RUnlock()

	// Outside the lock: a watcher that touched the room would deadlock, and one
	// that is merely slow would hold every other participant's presence update
	// behind it.
	for _, fn := range watchers {
		fn()
	}

	for _, m := range targets {
		if m.session == nil {
			continue // the agent hears about the room through room_state
		}
		// paused travels with presence rather than as its own message, because
		// it is a property of the room in the same way the controller is — and
		// a second person who sees the agent stopped without being told why
		// reaches for abort, which is the destructive thing the pause exists to
		// avoid.
		pausedBy, isPaused := r.Paused()
		payload, err := json.Marshal(map[string]any{
			"t": "presence", "you": m.id, "members": members,
			"paused": isPaused, "pausedBy": pausedBy,
		})
		if err != nil {
			continue
		}
		m.session.sendOnChannel(string(payload))
	}
}

// RelayVoice passes one voice-mesh envelope from a member to another, unread.
//
// The browsers negotiate their own peer-to-peer audio through these: the
// server checks only that the recipient is a member of this room and stamps
// the sender's identity, so a browser cannot claim to be somebody else. The
// payload stays sealed — what the room talks about over voice is not this
// server's business, cannot reach a recording, and never appears in a
// restream. A recipient without a live session (the agent) is silently
// skipped: there is no browser there to speak with.
func (r *Room) RelayVoice(from, to string, data json.RawMessage) {
	r.mu.RLock()
	m, ok := r.members[to]
	r.mu.RUnlock()
	if !ok || m.session == nil {
		return
	}
	m.session.send(wsMsg{Type: "voice", From: from, Data: data})
}

// --- pipeline descriptions -------------------------------------------------

func (r *Room) videoDesc() string {
	showPointer := "false"
	if r.cfg.RemoteCursor {
		showPointer = "true"
	}
	// Damage tracking on. With it off, ximagesrc re-reads the whole 1080p
	// framebuffer every frame even when not a pixel changed — which on a desktop
	// that is mostly still is the single largest waste in the pipeline. This is
	// the real version of "only send what changed": the codec already sends
	// differences, but the CAPTURE was doing full reads regardless.
	//
	// USE_DAMAGE=0 turns it off for a driver where it misbehaves.
	damage := config.Int("USE_DAMAGE", 1)
	// videorate in drop-only mode is the quality control's muscle, and the
	// named CAPSFILTER after it is the trigger. videorate's own max-rate
	// looked like the obvious knob and turned out to be a placebo: it is
	// only read during caps negotiation, so writing it on a running element
	// succeeds and drops nothing (SetFPSCap tells the story). Rewriting the
	// capsfilter's framerate forces a live renegotiation instead, and the
	// videorate obeys the new rate. drop-only matters throughout, because a
	// normal videorate would fill the quiet gaps damage-based capture
	// deliberately leaves with duplicate frames, undoing that saving to
	// enforce a constant rate nobody asked for. The queue is named so the
	// quality controller can read its depth: a queue that stays full is the
	// one honest sign the encoder is behind.
	return fmt.Sprintf(
		"ximagesrc display-name=%s use-damage=%d show-pointer=%s "+
			"! video/x-raw,framerate=%d/1 "+
			"! videorate drop-only=true "+
			"! capsfilter name=ratecap caps=video/x-raw,framerate=%d/1 "+
			"! queue name=vq max-size-buffers=2 leaky=downstream "+
			"! %s",
		r.cfg.Display, damage, showPointer, r.cfg.FPS, r.cfg.FPS,
		r.strategy.Fragment(r.cfg.VideoKbps, r.cfg.FPS))
}

func (r *Room) audioDesc() string {
	return fmt.Sprintf(
		"pulsesrc device=%s ! audio/x-raw,rate=48000,channels=2 "+
			"! audioconvert ! audioresample "+
			"! opusenc bitrate=%d inband-fec=true "+
			"! rtpopuspay pt=97 "+
			"! application/x-rtp,media=audio,encoding-name=OPUS,payload=97",
		r.cfg.AudioDevice, r.cfg.AudioBitrate)
}
