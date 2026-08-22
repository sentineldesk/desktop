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

import (
	"encoding/json"
	"fmt"
	"github.com/sentineldesk/desktop/internal/desktop"
	"github.com/sentineldesk/desktop/internal/media"
	"github.com/sentineldesk/desktop/pkg/config"
	"github.com/sentineldesk/desktop/pkg/ratelimit"
	"github.com/sentineldesk/desktop/pkg/version"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// wsMsg is the signalling envelope that travels over the WebSocket.
type wsMsg struct {
	Type          string  `json:"type"`
	SDP           string  `json:"sdp,omitempty"`
	Candidate     string  `json:"candidate,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`

	// The voice mesh's envelopes (§fase 3): To names the recipient on the way
	// in, From names the sender on the way out, and Data is the sealed payload
	// this server relays WITHOUT reading — the whole point of the design is
	// that the audio, and even its negotiation, never becomes the server's
	// business. A blind postman with a member directory.
	To   string          `json:"to,omitempty"`
	From string          `json:"from,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`

	// Authentication: the mandatory first frame, of type "auth".
	User   string `json:"user,omitempty"`
	Pass   string `json:"pass,omitempty"`
	Token  string `json:"token,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Name is how this viewer wants to be SHOWN — in the roster, the audit
	// lines, the agent's room_state. Display only, never identity: the
	// workroom panel passes the member's name here so the record says
	// "Carlos took the controls" instead of "Viewer 2", and a client that
	// sends nothing keeps the numbered fallback.
	Name string `json:"name,omitempty"`

	// Post-auth configuration (type "config"). Nothing sensitive over HTTP.
	IceServers []iceServer `json:"iceServers,omitempty"`
	// StunPort, when non-zero, says "this desktop answers STUN itself, on
	// this UDP port". The PORT travels instead of a URL because the client
	// knows the right host better than the server does: it builds
	// stun:<the-hostname-it-already-connected-to>:<port>, which is correct
	// through every NAT, tunnel and rebinding the server cannot see.
	StunPort     int         `json:"stunPort,omitempty"`
	Encoder      string      `json:"encoder,omitempty"`
	RemoteCursor *bool       `json:"remoteCursor,omitempty"`
	Version      string      `json:"version,omitempty"`
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// WebSocket limits. An auth frame is around 100 bytes, so nothing larger than
// 4 KB is accepted before authentication, and a short deadline stops sockets
// from hanging around without ever identifying themselves.
const (
	wsAuthReadLimit = 4 << 10
	wsReadLimit     = 512 << 10
	wsAuthDeadline  = 10 * time.Second
	wsPingEvery     = 30 * time.Second
	wsPongWait      = 90 * time.Second
)

// inputEvent is one event received over the DataChannel: keyboard, mouse,
// clipboard.
type inputEvent struct {
	T      string `json:"t"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	B      int    `json:"b"`
	D      int    `json:"d"`
	Dy     int    `json:"dy"`
	Dx     int    `json:"dx"`
	K      string `json:"k"`
	Clip   string `json:"clip"`   // clipboard text (browser -> desktop)
	Action string `json:"action"` // capture: shot | rec_start | rec_stop
	Format string `json:"format"` // capture: mp4 | webm | mkv
	ReqID  int    `json:"req"`    // control_answer / question_answer: which one
	Grant  bool   `json:"grant"`  // control_answer: allowed or refused
	Answer string `json:"answer"` // question_answer: what the person chose
	Mode   string `json:"mode"`   // quality: auto | media | high
	Name   string `json:"name"`   // rename: what this viewer wants to be called

	// viewstats: this client's self-measured reception, for the desktop's
	// stream card (streamstatus.go). Telemetry, clamped on arrival.
	Fps    int `json:"fps"`
	Kbps   int `json:"kbps"`
	Rtt    int `json:"rtt"`
	Behind int `json:"behind"`
	LossPM int `json:"losspm"`

	RS *restreamCmd `json:"rs"` // restream: where to send the desktop
}

// restreamCmd asks for an external destination to be attached or detached.
type restreamCmd struct {
	Action   string `json:"action"`   // start | stop | list
	ID       string `json:"id"`       // which destination to stop
	Platform string `json:"platform"` // youtube | twitch | facebook | custom
	URL      string `json:"url"`
	Audio    bool   `json:"audio"`

	// Keyframes is the answer to "can a viewer show up mid-stream?", asked of
	// the person who knows: whoever typed the address. It only has a say for a
	// custom destination — the platforms decide for themselves below.
	Keyframes bool `json:"kf"`
}

// Event types that are not input but room control:
//   take_control    — ask for control; cooperative between humans, so granted
//   release_control — pass it to the next participant
//   control_answer  — allow or refuse the agent's request

// Session is one client's WebRTC connection: its own PeerConnection and
// DataChannel, but the capture is shared with everyone else through the room.
type Session struct {
	cfg      config.Config
	strategy media.EncoderStrategy
	room     *Room
	memberID string
	// displayName is what the auth frame asked to be called; empty falls
	// back to the room's numbered "Viewer N".
	displayName string
	// privileged is true when the ticket this session authenticated with was
	// minted for an administrator or a moderator — stamped by the front desk,
	// never inferred from anything typed. It buys exactly one thing:
	// restreaming without holding the controls. See ParseToken.
	privileged bool
	recorder   *media.Recorder // shared with the MCP: one recording at a time
	delivery   *Delivery
	upstream   *media.Upstream // the browser's microphone into the desktop
	injector   *desktop.InputInjector
	cursors    *desktop.CursorTracker // may be nil (no XFixes, or a remote cursor)
	clip       *desktop.Clipboard
	auth       *Auth
	gate       *ratelimit.IPGate
	GateKey    string
	ws         *websocket.Conn
	peer       string

	wsMu       sync.Mutex
	pc         *webrtc.PeerConnection
	estimatorC chan cc.BandwidthEstimator
	cursorCh   chan desktop.CursorState
	lastClip   string
	closeOnce  sync.Once
	done       chan struct{}

	chanMu  sync.Mutex
	channel *webrtc.DataChannel // the input channel; it also carries presence

	// In-flight chunked transfers on the files channel. See transfers.go.
	transfersMu sync.Mutex
	transfers   map[string]*upload
	downloads   map[string]chan struct{}
	// Server-produced files offered to this session — finished recordings,
	// screenshots — pulled by id over the same channel. See deliveredFile.
	deliveries map[string]deliveredFile
	deliverSeq int
}

// connectionAlive reports whether this session is still usable.
//
// `connecting` counts as alive: that is a legitimate session still negotiating,
// and taking control from it would be stealing from someone about to arrive.
// Only `failed`, `closed` and `disconnected` are unambiguously dead.
func (s *Session) connectionAlive() bool {
	if s == nil || s.pc == nil {
		return true // not started yet: it cannot be declared dead
	}
	switch s.pc.ConnectionState() {
	case webrtc.PeerConnectionStateFailed,
		webrtc.PeerConnectionStateClosed,
		webrtc.PeerConnectionStateDisconnected:
		return false
	}
	return true
}

// sendOnChannel sends a message over the DataChannel if it is already open. The
// room uses it to broadcast presence and other people's pointers.
func (s *Session) sendOnChannel(text string) {
	s.chanMu.Lock()
	ch := s.channel
	s.chanMu.Unlock()
	if ch != nil && ch.ReadyState() == webrtc.DataChannelStateOpen {
		ch.SendText(text)
	}
}

func NewSession(cfg config.Config, strategy media.EncoderStrategy, room *Room, up *media.Upstream, rec *media.Recorder, deliver *Delivery, injector *desktop.InputInjector, cursors *desktop.CursorTracker, clip *desktop.Clipboard, auth *Auth, gate *ratelimit.IPGate, GateKey string, ws *websocket.Conn, peer string) *Session {
	return &Session{
		cfg:      cfg,
		strategy: strategy,
		room:     room,
		recorder: rec,
		delivery: deliver,
		upstream: up,
		injector: injector,
		cursors:  cursors,
		clip:     clip,
		auth:     auth,
		gate:     gate,
		GateKey:  GateKey,
		ws:       ws,
		peer:     peer,
		done:     make(chan struct{}),
	}
}

func (s *Session) logf(format string, args ...any) {
	log.Printf("[%s] "+format, append([]any{s.peer}, args...)...)
}

func (s *Session) send(msg wsMsg) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if err := s.ws.WriteJSON(msg); err != nil {
		s.logf("error sending over the websocket: %v", err)
	}
}

// Run serves the connection: authentication first — without it there is no
// WebRTC handshake at all — then signalling until the client goes away.
func (s *Session) Run() {
	defer s.Close()
	s.logf("client connected")

	if !s.authenticate() {
		return
	}

	// Keepalive: periodic ping, with the read deadline renewed by each pong.
	// Without it a NAT that drops the connection leaves the session hanging
	// forever, holding a slot and possibly the control token.
	s.ws.SetReadDeadline(time.Now().Add(wsPongWait))
	s.ws.SetPongHandler(func(string) error {
		return s.ws.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	go func() {
		ticker := time.NewTicker(wsPingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.wsMu.Lock()
				s.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				s.wsMu.Unlock()
			}
		}
	}()

	s.sendConfig()

	if err := s.start(); err != nil {
		s.logf("could not start the session: %v", err)
		// Say why. Retrying blindly against a full room just produces a
		// reconnect loop with no explanation on screen.
		s.send(wsMsg{Type: "fatal", Reason: err.Error()})
		return
	}

	for {
		var msg wsMsg
		if err := s.ws.ReadJSON(&msg); err != nil {
			s.logf("client disconnected: %v", err)
			return
		}
		switch msg.Type {
		case "answer":
			desc := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: msg.SDP}
			if err := s.pc.SetRemoteDescription(desc); err != nil {
				s.logf("could not apply the answer: %v", err)
			}
		case "renegotiate":
			// The browser wants to start publishing its microphone.
			//
			// replaceTrack alone is not enough. When the session was negotiated
			// the browser had nothing to send, so it answered that media line
			// `inactive` and it stayed switched off. We have to offer again so
			// the browser can answer `sendonly` — this time with the track
			// actually attached.
			if err := s.renegotiate(); err != nil {
				s.logf("renegotiation failed: %v", err)
			}
		case "ice":
			if msg.Candidate == "" {
				continue
			}
			init := webrtc.ICECandidateInit{
				Candidate:     msg.Candidate,
				SDPMLineIndex: msg.SDPMLineIndex,
			}
			if err := s.pc.AddICECandidate(init); err != nil {
				s.logf("ICE candidate refused: %v", err)
			}
		case "voice":
			// Peer-to-peer voice between the people watching: the browsers
			// exchange their own offers and candidates through here, and the
			// audio then flows directly between them — it never touches this
			// server, PulseAudio, a recording or a restream. The relay only
			// checks that the recipient is a member of this room, stamps who
			// it came from, and passes the envelope on unopened.
			s.room.RelayVoice(s.memberID, msg.To, msg.Data)
		}
	}
}

// sanitizeNickname reduces a requested display name to characters every
// surface it lands on carries without escaping: ASCII letters, digits, space,
// dash and underscore. Deliberately narrower than the auth frame's name — the
// workroom panel vouches for the names IT sends, while this one is typed by
// whoever is at the keyboard and painted into an X window verbatim. Anything
// else is dropped rather than replaced, and the result is trimmed and capped.
func sanitizeNickname(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z',
			r >= '0' && r <= '9', r == ' ', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	name := strings.TrimSpace(b.String())
	if len(name) > 48 {
		name = strings.TrimSpace(name[:48])
	}
	return name
}

// authenticate requires the FIRST frame to be a valid "auth", under a short
// deadline and a small read limit. Anything else — a WebRTC offer, an unknown
// type, silence — ends the connection. There is no second chance on the same
// socket, which is what gives the per-origin gate (IPGate) its meaning.
func (s *Session) authenticate() bool {
	s.ws.SetReadLimit(wsAuthReadLimit)
	s.ws.SetReadDeadline(time.Now().Add(wsAuthDeadline))

	deny := func(reason string) bool {
		no := false
		s.send(wsMsg{Type: "auth", OK: &no, Reason: reason})
		s.wsMu.Lock()
		s.ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "auth failed"),
			time.Now().Add(time.Second))
		s.wsMu.Unlock()
		return false
	}

	// A banned origin usually never reaches this point — it is cut off at the
	// upgrade — but a ban can land while the connection is already accepted.
	if s.gate.Banned(s.GateKey) {
		return deny("locked")
	}

	var msg wsMsg
	if err := s.ws.ReadJSON(&msg); err != nil {
		return false // silencio o basura: ni respuesta merece
	}
	if msg.Type != "auth" {
		// Anyone opening with something else is not one of our browsers. They
		// get exactly the same answer as a wrong password: nothing in the
		// response tells them which part failed.
		s.gate.Fail(s.GateKey)
		return deny("invalid credentials")
	}
	if s.auth.Enabled() {
		role, tokenOK := s.auth.ParseToken(msg.Token)
		ok := tokenOK ||
			(msg.User != "" && s.auth.ValidCredentials(msg.User, msg.Pass))
		if !ok {
			s.gate.Fail(s.GateKey)
			s.logf("access denied")
			return deny("invalid credentials")
		}
		// Privilege comes from the TICKET, never from anything typed: the
		// front desk stamps the role of an administrator or moderator into
		// the tokens it signs, and this is the only place the runtime learns
		// it. What it buys is narrow on purpose — restreaming without the
		// controls (see handleRestream) — not a skeleton key.
		s.privileged = tokenOK && (role == "admin" || role == "moderator")
	}
	s.gate.Pass(s.GateKey)
	// The display name, sanitized to a single short line: it lands in the
	// roster, the witness log and every client's screen, and a name with a
	// newline in it would be a log injection wearing an alias.
	if name := strings.TrimSpace(msg.Name); name != "" {
		name = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return r
		}, name)
		if len(name) > 48 {
			name = name[:48]
		}
		s.displayName = name
	}
	s.logf("access granted")

	s.ws.SetReadLimit(wsReadLimit)
	s.ws.SetReadDeadline(time.Time{})
	yes := true
	s.send(wsMsg{Type: "auth", OK: &yes, Token: s.auth.NewToken()})
	return true
}

// sendConfig hands the authenticated client what /config.json used to expose
// over HTTP. The ICE servers carry TURN credentials, which is exactly why they
// must not be reachable before authentication.
func (s *Session) sendConfig() {
	// With the embedded STUN on, an UNTOUCHED default CLIENT_STUN is simply
	// not sent: browsers use the desktop's own responder and no Google
	// server sits in the path. An explicitly configured CLIENT_STUN rides
	// along regardless — more candidates never hurt, and the operator said
	// so.
	servers := []iceServer{}
	if !s.cfg.StunEmbedded || s.cfg.ClientStun != config.DefaultClientStun {
		servers = append(servers, iceServer{URLs: []string{s.cfg.ClientStun}})
	}
	if len(s.cfg.ClientTurnURL) > 0 {
		servers = append(servers, iceServer{
			URLs:       s.cfg.ClientTurnURL,
			Username:   s.cfg.TurnUser,
			Credential: s.cfg.TurnPass,
		})
	}
	remote := s.cfg.RemoteCursor
	stunPort := 0
	if s.cfg.StunEmbedded {
		stunPort = s.cfg.StunPort
	}
	s.send(wsMsg{
		Type:         "config",
		IceServers:   servers,
		StunPort:     stunPort,
		Encoder:      s.strategy.Name,
		RemoteCursor: &remote,
		// So the rail can say which build this is. After auth on purpose: a
		// version string is a gift to whoever is fingerprinting servers.
		Version: version.Short(),
	})
}

func (s *Session) start() error {
	// Its own API per session: the congestion interceptor (GCC) hands back one
	// estimator per PeerConnection, so this keeps the association unambiguous.
	api, estimatorC, err := newPeerAPI(s.cfg)
	if err != nil {
		return err
	}
	s.estimatorC = estimatorC

	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{s.cfg.StunServer}}},
	})
	if err != nil {
		return fmt.Errorf("NewPeerConnection: %w", err)
	}
	s.pc = pc

	// --- pistas de salida -------------------------------------------------
	videoCaps := webrtc.RTPCodecCapability{MimeType: s.strategy.MimeType, ClockRate: 90000}
	if s.strategy.MimeType == webrtc.MimeTypeH264 {
		videoCaps.SDPFmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"
	}
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(videoCaps, "video", "desktop")
	if err != nil {
		return err
	}
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "desktop",
	)
	if err != nil {
		return err
	}

	videoSender, err := pc.AddTrack(videoTrack)
	if err != nil {
		return err
	}
	audioSender, err := pc.AddTrack(audioTrack)
	if err != nil {
		return err
	}

	// Video RTCP: answer PLI/FIR with an immediate keyframe.
	go s.watchRTCP(videoSender)
	// Audio RTCP: just drain it so the interceptors can do their work.
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := audioSender.Read(buf); err != nil {
				return
			}
		}
	}()

	// --- INCOMING track: the browser's microphone ---------------------------
	// Declared recvonly so the offer reserves a slot for it; the browser turns
	// it on when the person presses the microphone button.
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		s.logf("could not offer audio reception: %v", err)
	}
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		s.handleRemoteTrack(track)
	})

	// --- join the room ------------------------------------------------------
	// The tracks belong to this session; the capture feeding them is shared.
	memberID, isController, err := s.room.Join(s, videoTrack, audioTrack)
	if err != nil {
		return err
	}
	s.memberID = memberID
	s.logf("room: %s (control=%v)", memberID, isController)

	// --- DataChannel de entrada ------------------------------------------
	channel, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		return err
	}
	// The SECOND channel: files, chunked, so a gigabyte never queues behind
	// a mouse move. See transfers.go.
	s.openFilesChannel(pc)
	s.chanMu.Lock()
	s.channel = channel
	s.chanMu.Unlock()
	channel.OnMessage(func(msg webrtc.DataChannelMessage) {
		var ev inputEvent
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			return
		}
		// A malformed or unlucky input event must never take the DAEMON down:
		// this desktop is shared, and one viewer's bad frame ending every
		// other viewer's session is the worst trade there is. The recover is
		// LOUD — a swallowed panic is the silent-failure class this project
		// ranks below a crash — and it already earned its keep: a wrong
		// argument order in the virtual keyboard's remap path panicked here
		// on the very first keystroke it was fed.
		defer func() {
			if r := recover(); r != nil {
				s.logf("input event %q PANICKED (recovered): %v", ev.T, r)
			}
		}()
		s.handleInput(ev)
	})
	// The real pointer shape (resize arrows, text beam, hand…) travels to the
	// client over the same channel; the browser applies it as a CSS cursor.
	channel.OnOpen(func() {
		// Presence as soon as it opens: who is in the room and who has control.
		s.room.broadcastPresence()
		// And whether a recording is already running. Without this, a reload
		// leaves the button saying "Record" while the server is recording, and
		// the next click fails with a confusing error.
		s.sendCaptureState()
		// The quality position, for the same reason: the toolbar's dial must
		// open showing what the room already chose, not its own default.
		mode, fps, by := s.room.QualityState()
		s.sendQuality(mode, fps, by, "")
		// Clipboard synchronisation, desktop -> browser.
		go s.watchClipboard(channel)
		// The real pointer shape (resize arrows, text beam, hand…).
		if s.cursors != nil {
			current, updates := s.cursors.Subscribe()
			s.cursorCh = updates
			s.sendCursor(channel, current)
			go func() {
				for {
					select {
					case <-s.done:
						return
					case state := <-updates:
						s.sendCursor(channel, state)
					}
				}
			}()
		}
	})

	// --- signalling ---------------------------------------------------------
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		j := c.ToJSON()
		s.send(wsMsg{Type: "ice", Candidate: j.Candidate, SDPMLineIndex: j.SDPMLineIndex})
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		s.logf("WebRTC state: %s", st)
		switch st {
		case webrtc.PeerConnectionStateConnected:
			// A keyframe the moment it connects: a full picture without waiting
			// for the GOP to come round.
			s.room.ForceKeyFrame()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.Close()
		}
	})

	go s.adaptBitrate()

	// --- oferta -----------------------------------------------------------
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	s.send(wsMsg{Type: "offer", SDP: pc.LocalDescription().SDP})
	s.logf("SDP offer sent (encoder %s)", s.strategy.Name)
	return nil
}

func (s *Session) sendCursor(channel *webrtc.DataChannel, state desktop.CursorState) {
	if state.DataURL == "" {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"t": "cursor", "d": state.DataURL, "x": state.HotX, "y": state.HotY,
	})
	if err == nil {
		channel.SendText(string(payload))
	}
}

// watchRTCP handles incoming RTCP on the video sender. A PLI or FIR means the
// browser lost its reference picture, so force a keyframe immediately rather
// than letting it stare at a smeared image until the next GOP.
func (s *Session) watchRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		n, _, err := sender.Read(buf)
		if err != nil {
			return
		}
		packets, err := rtcp.Unmarshal(buf[:n])
		if err != nil {
			continue
		}
		for _, pkt := range packets {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				// The keyframe reaches everyone, because there is only one
				// encoder. That is the price of sharing the capture, and it is
				// cheap: whoever did not ask for it gets one extra full frame,
				// not an interruption.
				s.room.ForceKeyFrame()
			}
		}
	}
}

// adaptBitrate consumes the bandwidth estimate (GCC over TWCC) and steers the
// encoder's bitrate at runtime.
func (s *Session) adaptBitrate() {
	var estimator cc.BandwidthEstimator
	select {
	case estimator = <-s.estimatorC:
	case <-s.done:
		return
	}

	const floorKbps = 300
	maxKbps := s.cfg.VideoKbps
	current := maxKbps
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
		target := estimator.GetTargetBitrate() / 1000 // kbps
		if target > maxKbps {
			target = maxKbps
		}
		if target < floorKbps {
			target = floorKbps
		}
		if target == current {
			continue
		}
		current = target
		// The encoder is shared, so this does not set the bitrate directly. It
		// reports what THIS network can carry and the room applies the minimum
		// across everyone.
		s.room.ReportBitrate(s.memberID, target)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (s *Session) handleInput(ev inputEvent) {
	// Room control: one person drives, the rest watch. These are handled before
	// the control check, because asking for control is precisely what someone
	// who does not have it needs to be able to do.
	switch ev.T {
	case "control_answer":
		// A person answered the agent's request. Anyone in the room may answer:
		// they are all equally entitled, and requiring the current controller
		// would leave the agent stuck whenever that person stepped away.
		s.room.AnswerControlRequest(ev.ReqID, ev.Grant)
		return

	case "question_answer":
		// A person answered the agent's question. Like a control answer, anyone
		// present may reply: they all got in with the same credential, and
		// requiring one particular person leaves the agent waiting out its
		// timeout the moment that person steps away from the keyboard.
		s.room.AnswerQuestion(ev.ReqID, ev.Answer)
		return

	case "pause", "resume":
		// Reversible, so anybody may press it and anybody may lift it. A pause
		// only its author could undo would strand the room the first time
		// somebody paused and closed the tab — and the expiry in the MCP server
		// is a backstop, not the normal way out.
		on := ev.T == "pause"
		s.room.witness.Note(s.room.NameOf(s.memberID),
			map[bool]string{true: "paused the agent", false: "resumed the agent"}[on],
			"")
		s.room.Pause(s.memberID, on)
		s.logf("%s the agent", map[bool]string{true: "paused", false: "resumed"}[on])
		return

	case "abort":
		// The panic button. Handled here, above the control check, and that
		// placement is the whole design.
		//
		// Anyone in the room may press it, including somebody who is not driving
		// — especially somebody who is not driving, because the person watching
		// an agent work is by definition not the one holding the controls. A
		// stop that only the controller can issue is not a stop, it is a
		// privilege, and the moment that matters is the moment a bystander sees
		// something going wrong.
		//
		// It does two things and they are both necessary. Taking the controls is
		// what actually prevents the next action; killing the running jobs is
		// what stops the current one. Either alone leaves half a running agent.
		s.room.witness.Note(s.room.NameOf(s.memberID), "pressed abort",
			"stopped the agent and took the controls")
		s.room.Abort(s.memberID)
		s.logf("pressed abort")
		return

	case "take_control":
		if s.room.TakeControl(s.memberID) {
			s.room.witness.Note(s.room.NameOf(s.memberID), "took control", "")
			s.logf("took control")
		}
		return
	case "release_control":
		s.room.ReleaseControl(s.memberID)
		s.room.witness.Note(s.room.NameOf(s.memberID), "released control", "")
		s.logf("released control")
		return
	case "viewstats":
		// A viewer reporting its own reception for the desktop's stream card.
		// Pure telemetry about oneself: no gate, no log line per report — two
		// a second per viewer would drown the log in heartbeat.
		s.room.ReportViewStats(s.memberID, ev.Fps, ev.Kbps, ev.Rtt, ev.Behind, ev.LossPM)
		return
	case "capture":
		// Capture and recording go through the SERVER rather than the browser,
		// so the file comes out as MP4 — which opens anywhere without
		// installing a player — and at the quality of the original framebuffer,
		// without the stream's compression.
		s.handleCapture(ev)
		return
	case "restream":
		s.handleRestream(ev)
		return
	case "quality":
		// The stream's cost dial — auto / media / high, see quality.go. It
		// changes what EVERYONE receives, so it follows the restream rule:
		// the controller's turn, or a privileged ticket. The refusal answers
		// only the session that asked, named, because the silent alternative
		// is a button that does nothing.
		if !s.room.IsController(s.memberID) && !s.privileged {
			mode, fps, by := s.room.QualityState()
			s.sendQuality(mode, fps, by, "needControl")
			return
		}
		if err := s.room.SetQuality(s.room.NameOf(s.memberID), ev.Mode); err != nil {
			mode, fps, by := s.room.QualityState()
			s.sendQuality(mode, fps, by, err.Error())
			return
		}
		s.logf("set quality %s", ev.Mode)
		return
	case "rename":
		// A person naming THEMSELVES — no control needed, identity is not an
		// act on the shared desktop. The charset is deliberately narrow
		// (letters, digits, space, dash, underscore): the name lands in the
		// roster, the witness log, and a pointer tag drawn straight into X,
		// and every one of those is happier without surprises. Empty after
		// sanitizing means "ignore", not "erase": the numbered fallback only
		// applies at join.
		name := sanitizeNickname(ev.Name)
		if name == "" {
			return
		}
		old := s.room.NameOf(s.memberID)
		if name == old {
			return
		}
		s.displayName = name
		s.room.Rename(s.memberID, name)
		s.room.witness.Note(old, "is now called", name)
		s.logf("renamed to %q", name)
		return
	}

	if !s.room.IsController(s.memberID) {
		// A viewer injects nothing, but their pointer is still broadcast: that
		// is what lets the others see what they are looking at or pointing to.
		if ev.T == "mm" {
			s.room.UpdatePointer(s.memberID, ev.X, ev.Y)
		}
		return
	}

	// Everything below this line is a person acting on the shared desktop, and
	// this is the only place all of it passes through — which is why the
	// recording hangs off here rather than off five call sites that would each
	// have to remember.
	//
	// Movement is deliberately NOT recorded. It arrives at whatever rate the
	// browser can send, and a log holding every intermediate coordinate is
	// larger, slower to read and LESS informative than one holding "clicked at
	// 415,301" — the interesting fact is where the act landed, and the trail of
	// pixels leading to it buries it. Presses, wheels and keys are the events;
	// movement is the context that comes with them.
	witness := s.room.witness
	who := s.room.NameOf(s.memberID)

	switch ev.T {
	case "mm":
		s.injector.Move(ev.X, ev.Y)
		s.room.UpdatePointer(s.memberID, ev.X, ev.Y)
	case "mb":
		s.injector.Button(ev.B, ev.D == 1)
		// On press only. A click is one act; recording the release as well
		// doubles the log to say the same thing twice.
		if ev.D == 1 {
			witness.Pointer(who, "clicked", ev.X, ev.Y)
		}
	case "mw":
		s.injector.Wheel(ev.Dy, ev.Dx)
		witness.Pointer(who, "scrolled", ev.X, ev.Y)
	case "kb":
		s.injector.Key(ev.K, ev.D == 1)
		if ev.D == 1 {
			witness.Key(who)
		}
	case "menu":
		// The on-screen keyboard's ⊞ key, tapped alone. On a real Linux
		// desktop the Super key opens the applications menu; Openbox cannot
		// bind a bare modifier press, so the daemon opens it directly —
		// xfce4-panel ships the popup command for exactly this. Used as a
		// modifier (⊞ latched with another key) the client sends a normal
		// Super chord instead and this case never fires.
		if err := exec.Command("xfce4-popup-whiskermenu").Run(); err != nil {
			s.logf("menu popup: %v", err)
		}
		witness.Key(who)
	case "kbt":
		// A run of text from the virtual keyboard. It arrives on the desktop
		// as a PASTE — the text goes to the clipboard and Shift+Insert lands
		// it — because that is the one route that survives every keyboard
		// layout. Two attempts at "really typing" it failed measurably:
		// bare keycodes turn '>' into '.', and remapping a spare keycode
		// races every client's cached keymap (xdotool loses the second
		// group's ñ the same way). The trade is honest and stated: kbt
		// overwrites the desktop's clipboard, exactly like the paste it is.
		// Witnessed one key per rune, so "typed N keys" stays honest.
		if s.clip != nil {
			if err := s.clip.SetBoth(ev.K); err == nil {
				s.injector.Key("Shift", true)
				s.injector.Key("Insert", true)
				s.injector.Key("Insert", false)
				s.injector.Key("Shift", false)
				// The paste is asynchronous on the receiving side — the
				// window asks the selection's owner and inserts on the
				// answer. Hold the serialized queue briefly so a key sent
				// right behind this (the virtual keyboard's Enter) cannot
				// arrive before the text does; it happened, measurably.
				time.Sleep(150 * time.Millisecond)
			}
		}
		for range ev.K {
			witness.Key(who)
		}
	case "reset":
		s.injector.ReleaseAll()
	case "clip":
		// Clipboard, browser -> desktop. Remember the value so the watcher does
		// not immediately send it back to us as if it were new.
		if s.clip != nil {
			// Recorded by size, not by content: a clipboard is where a password
			// manager puts things.
			witness.Note(who, "pasted",
				fmt.Sprintf("%d characters onto the desktop clipboard", len(ev.Clip)))
			s.lastClip = ev.Clip
			if err := s.clip.Set(ev.Clip); err != nil {
				s.logf("clipboard: %v", err)
			}
		}
	}
}

// handleCapture takes a screenshot or drives the recording, and hands the file
// to this browser. Only the controller may: a recording is a single shared
// resource, and two people starting one would fight over the same file.
// platformKeyframes says how often a destination has to send a keyframe.
//
// The platforms are not asked, because the answer is a property of what they
// do: they serve an audience that arrives whenever it likes, and a viewer sees
// nothing at all until the next keyframe. Two seconds is what all three of them
// require.
//
// A destination you point at yourself — VLC on the next desk, OBS on your own
// machine — has no such audience, so it keeps the sparse keyframes the desktop
// normally runs with and gets noticeably sharper text for it.
func platformKeyframes(platform string, wanted bool) int {
	switch platform {
	case "youtube", "twitch", "facebook":
		return 2
	}
	if wanted {
		return 2
	}
	return 0
}

// The human wire speaks in terse event names; the catalogue speaks in verbs.
// These two maps are the translation, package-level rather than inline so the
// parity test can range over exactly what the handlers consult: a verb named
// here that the catalogue does not know would silently gate as the fallback
// answers, which is the class of quiet drift §4.6 exists to end.
var (
	captureVerb = map[string]string{
		"shot": "screenshot", "rec_start": "start_recording", "rec_stop": "stop_recording",
	}
	restreamVerb = map[string]string{
		"start": "start_restream", "stop": "stop_restream",
	}
)

// handleRestream starts or stops sending this desktop somewhere else.
//
// Publishing the session to the internet is held to the same rule as driving
// it: whoever has control decides. A viewer who could start a broadcast would
// be doing something to everyone else's session without holding the turn.
// Whether the rule applies is no longer this switch's opinion, though — it is
// read off the shared catalogue (§4.6), the same entry the agent's identical
// call is gated by, so the two wires cannot come to disagree about it again.
func (s *Session) handleRestream(ev inputEvent) {
	cmd := ev.RS
	if cmd == nil {
		return
	}
	if cmd.Action == "list" {
		s.sendRestreams("")
		return
	}
	// The controls gate, with one named exception: a PRIVILEGED session — an
	// administrator or moderator, stamped into the ticket by the front desk —
	// broadcasts without holding the seat. Restreaming was originally held to
	// the driving rule because publishing everyone's session must be
	// somebody's authority to exercise; the refinement is recognising that an
	// administrator carries that authority by role, and making them wrestle
	// the controls away from a working guest just to start a broadcast
	// conflated two different powers. Guests still need the turn, and the
	// agent always does — its plane never mints privileged tickets.
	if s.room.verbNeedsControl(restreamVerb[cmd.Action]) &&
		!s.room.IsController(s.memberID) && !s.privileged {
		s.sendRestreams("needControl")
		return
	}

	switch cmd.Action {
	case "start":
		id := cmd.ID
		if id == "" {
			id = cmd.Platform
		}
		if id == "" {
			id = "custom"
		}
		err := s.room.StartRestream(media.RestreamTarget{
			ID:          id,
			Platform:    cmd.Platform,
			URL:         cmd.URL,
			Audio:       cmd.Audio,
			KeyframeSec: platformKeyframes(cmd.Platform, cmd.Keyframes),
		})
		if err != nil {
			s.logf("restream refused: %v", err)
			s.sendRestreams(err.Error())
			return
		}
		// By destination, never by URL: an RTMP address carries the stream key,
		// and this log is read by an agent that sends what it reads to a model
		// API — the same reasoning that records the clipboard by size.
		s.room.witness.Note(s.room.NameOf(s.memberID), "started streaming the desktop",
			"to "+id)
		s.logf("streaming to %s", cmd.Platform)

	case "stop":
		if err := s.room.StopRestream(cmd.ID); err != nil {
			s.sendRestreams(err.Error())
			return
		}
		s.room.witness.Note(s.room.NameOf(s.memberID), "stopped streaming the desktop", "")
	}
}

// sendRestreams answers just the client that asked, which is what a rejected
// start needs: the error belongs to the person who typed the address, not to
// everyone in the room.
func (s *Session) sendRestreams(problem string) {
	msg := map[string]any{
		"t":    "restreams",
		"list": s.room.Restreams(),
		"able": s.room.CanRestream(),
	}
	if problem != "" {
		msg["error"] = problem
	}
	if payload, err := json.Marshal(msg); err == nil {
		s.sendOnChannel(string(payload))
	}
}

func (s *Session) handleCapture(ev inputEvent) {
	if s.recorder == nil || s.delivery == nil {
		s.sendOnChannel(`{"t":"capture_error","error":"capture unavailable"}`)
		return
	}
	// The gate comes from the shared catalogue, and for these verbs it answers
	// NO — capturing is not driving, and the agent has always been allowed to
	// screenshot or record without holding the turn. Requiring the controls
	// here was this wire's own stricter opinion, which is exactly the drift
	// §4.6 exists to end: one verb, one answer, whoever asks. What actually
	// protects the recorder from two starts is the recorder itself, which
	// refuses a second one by name.
	if s.room.verbNeedsControl(captureVerb[ev.Action]) && !s.room.IsController(s.memberID) {
		s.sendOnChannel(`{"t":"capture_error","error":"needControl"}`)
		return
	}

	switch ev.Action {
	case "shot":
		name := "screenshot-" + time.Now().Format("20060102-150405") + ".png"
		path := filepath.Join(s.recorder.Dir, name)
		if err := os.MkdirAll(s.recorder.Dir, 0o755); err != nil {
			s.captureError(err)
			return
		}
		if err := desktop.GrabToFile(s.cfg.Display, path, 0, 0, 0, 0); err != nil {
			s.captureError(err)
			return
		}
		s.room.witness.Note(s.room.NameOf(s.memberID), "took a screenshot", name)
		s.delivery.Deliver(path, name)

	case "rec_start":
		container := ev.Format
		if container == "" {
			container = "mp4" // opens everywhere without installing a player
		}
		if _, err := s.recorder.Start(media.RecordOpts{Container: container, Audio: true}); err != nil {
			s.captureError(err)
			return
		}
		s.room.witness.Note(s.room.NameOf(s.memberID), "started recording the desktop", container)
		s.sendCaptureState()

	case "rec_stop":
		path, _, err := s.recorder.Stop()
		if err != nil {
			s.captureError(err)
			return
		}
		s.room.witness.Note(s.room.NameOf(s.memberID), "stopped recording the desktop",
			filepath.Base(path))
		s.sendCaptureState()
		s.delivery.Deliver(path, filepath.Base(path))
	}
}

// sendCaptureState tells this client whether a recording is currently running.
func (s *Session) sendCaptureState() {
	if s.recorder == nil {
		return
	}
	active, _ := s.recorder.Status()["recording"].(bool)
	payload, _ := json.Marshal(map[string]any{"t": "capture_state", "recording": active})
	s.sendOnChannel(string(payload))
}

func (s *Session) captureError(err error) {
	payload, _ := json.Marshal(map[string]string{"t": "capture_error", "error": err.Error()})
	s.sendOnChannel(string(payload))
	s.logf("capture: %v", err)
}

// sendQuality tells this client the stream's quality position — the mode, the
// framerate cap it currently means, who set it, and (only on a rejected
// change) why this session's attempt was refused. The refusal rides the same
// message as the state on purpose: whatever the answer, the dial ends up
// showing the truth.
func (s *Session) sendQuality(mode string, fps int, by, refused string) {
	payload, _ := json.Marshal(map[string]any{
		"t": "quality", "mode": mode, "fps": fps, "by": by, "refused": refused,
	})
	s.sendOnChannel(string(payload))
}

// renegotiate emits a fresh offer over the already established connection.
//
// The client asks for it when switching the microphone on. It is
// another offer on the same PeerConnection: the video does not stop and ICE is
// not rebuilt, only
// the directions of the media lines change.
func (s *Session) renegotiate() error {
	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := s.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	s.send(wsMsg{Type: "offer", SDP: s.pc.LocalDescription().SDP})
	s.logf("renegotiating to receive the microphone")
	return nil
}

// handleRemoteTrack pours a track the browser sends into the desktop, as a
// PulseAudio source.
//
// Only the participant holding control may publish. Otherwise two open
// microphones would pile into the same sink and nobody could tell where the
// noise was coming from.
func (s *Session) handleRemoteTrack(track *webrtc.TrackRemote) {
	if s.upstream == nil {
		return
	}
	if !s.room.IsController(s.memberID) {
		s.logf("incoming track ignored: only the controller publishes")
		return
	}

	// The reader runs in its own goroutine and dies with the track.
	feed := func(push func([]byte)) {
		go func() {
			buf := make([]byte, 1600)
			for {
				n, _, err := track.Read(buf)
				if err != nil {
					s.upstream.Stop(s.memberID)
					return
				}
				pkt := make([]byte, n)
				copy(pkt, buf[:n])
				push(pkt)
			}
		}()
	}

	// Video on the return path is not accepted: it needs v4l2loopback, a host
	// kernel module that cannot be loaded from inside a container.
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		s.logf("incoming %s track ignored: only audio travels upstream", track.Kind())
		return
	}
	if err := s.upstream.StartAudio(s.memberID, feed); err != nil {
		s.logf("could not publish %s: %v", track.Kind(), err)
		s.sendOnChannel(fmt.Sprintf(`{"t":"upstream_error","kind":%q,"error":%q}`,
			track.Kind().String(), err.Error()))
	}
}

// watchClipboard watches the desktop's CLIPBOARD selection and forwards changes
// to the browser, so something copied on the remote side can be pasted locally.
// Deduplicating against lastClip prevents echoing back what the browser
// acaba de enviar.
func (s *Session) watchClipboard(channel *webrtc.DataChannel) {
	if s.clip == nil {
		return
	}
	if text, ok := s.clip.Get(); ok && text != "" {
		s.lastClip = text // do not echo the initial contents back
	}
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
		text, ok := s.clip.Get()
		if !ok || text == "" || text == s.lastClip {
			continue
		}
		s.lastClip = text
		payload, err := json.Marshal(map[string]string{"t": "clip", "d": text})
		if err == nil {
			channel.SendText(string(payload))
		}
	}
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		// Half-arrived uploads go with the session — a temp file with no
		// sender is a corpse, not a download in progress.
		s.closeUploads()
		if s.cursors != nil && s.cursorCh != nil {
			s.cursors.Unsubscribe(s.cursorCh)
		}
		// Only release the keys if this session was the one driving. Otherwise
		// it would be yanking the keyboard out from under whoever is working.
		if s.memberID != "" && s.room.IsController(s.memberID) {
			s.injector.ReleaseAll()
		}
		if s.memberID != "" {
			if s.upstream != nil {
				s.upstream.Stop(s.memberID)
			}
			s.room.Leave(s.memberID)
		}
		if s.pc != nil {
			s.pc.Close()
		}
		s.ws.Close()
		s.logf("session closed")
	})
}
