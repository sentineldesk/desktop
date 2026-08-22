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

// Package config holds every setting the server reads from the environment.
//
// Configuration is deliberately environment-only: the container is the unit of
// deployment, and a second configuration file would just be another thing that
// can disagree with docker-compose.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultClientStun is the fallback public STUN, named so the session can
// tell "the operator chose this" from "nobody touched it": with the embedded
// STUN on, an untouched default is simply not sent to browsers — they use
// the desktop's own responder — while an explicit choice is always honoured.
const DefaultClientStun = "stun:stun.l.google.com:19302"

// Config is the fully resolved configuration for one server process.
type Config struct {
	HTTPPort     int
	HTTPAddr     string
	Display      string
	FPS          int
	Encoder      string // auto | nvenc | vaapi | h264 | vp8
	MinVideoKbps int
	VideoKbps    int
	AudioBitrate int
	AudioDevice  string
	RemoteCursor bool
	StunServer   string
	MinPort      uint16
	MaxPort      uint16
	NAT1To1IP    string
	// PanelURL, when set, replaces the embedded web client: this desktop is a
	// ROOM in a fleet, its people arrive through the front desk's panel, and
	// the port answering with a second, older UI is a door nobody should
	// find. `/` redirects there; the WebSocket, the media and the ticketed
	// download/upload endpoints keep working, because they are the plumbing
	// the panel itself uses.
	PanelURL string

	// The embedded STUN responder (internal/stream/stun.go): on by default
	// so a VPS install answers "what is my address?" itself — no Google
	// server in the path, nothing extra to install. STUN_EMBEDDED=0 turns
	// it off; CLIENT_STUN still points browsers anywhere else.
	StunEmbedded bool
	StunPort     int

	ClientStun    string
	ClientTurnURL []string
	TurnUser      string
	TurnPass      string

	AuthUser   string
	AuthPass   string
	AuthSecret string
	AuthTTL    time.Duration

	TLSCert       string
	TLSKey        string
	TLSSelfSigned bool
	TLSDir        string
	TLSHosts      string

	// FilesRoot bounds what the browser's file manager can reach. The home
	// directory is the sensible default: it is where people keep the things
	// they want to download. FILES_ROOT=/ opens up the whole container.
	FilesRoot string

	// MaxViewers caps how many people can share one desktop at a time.
	MaxViewers int

	// PublicURL is the address a PERSON types to reach this desktop, which is
	// not something the daemon can work out for itself.
	//
	// It exists because of one requirement: every job the agent starts hands
	// back a link where its output can be read, and that link has to survive
	// being pasted into a chat window. Inside the container the daemon knows
	// only that it is bound to :8080 — not the hostname it is reached by, not
	// whether Caddy terminates TLS in front of it, not the path prefix a
	// reverse proxy might mount it under. Guessing produces a link that works
	// on the operator's laptop and nowhere else, which is worse than no link,
	// because a broken link is tried once and then distrusted.
	//
	// Unset falls back to http://localhost:HTTP_PORT, which is right for the
	// documented `make up` case and wrong the moment anybody else is watching.
	PublicURL string

	// ActionLog is where the MCP audit trail is appended, one JSON object per
	// line.
	//
	// On by default, which is a change: the trail used to exist only in memory
	// unless somebody set an environment variable that appeared in no compose
	// file, no config struct and no table in the README. It answered "how did
	// you install that" perfectly and lost the answer on the next restart —
	// the wrong way round for something whose entire purpose is to still be
	// there afterwards.
	//
	// Deliberately outside FILES_ROOT. An agent with run_command can reach it
	// anyway, so this is not tamper-proofing; it is keeping the audit trail
	// out of the file manager people browse and tidy up.
	//
	// ACTION_LOG= (empty) turns persistence off and keeps the in-memory ring.
	ActionLog string

	// ActionLogMaxMB rotates the trail once it passes this size, keeping one
	// previous file. Without it a long-running desktop writes an unbounded
	// file, which is how a durable audit trail becomes a full disk.
	ActionLogMaxMB int

	// WorkroomID and RuntimeID are who this desktop IS in a fleet.
	//
	// One instance of this daemon is one desktop and one room — that is a
	// design decision, not a limitation — so a host running several rooms
	// runs several instances, and every log line, event and audit entry has
	// to say which one it came from. These two exist so that the answer is
	// stamped at the source rather than reconstructed from port numbers by
	// whoever is reading. WorkroomID names the ROOM (stable across restarts,
	// the unit an administrator reasons about); RuntimeID names this
	// CONTAINER (fresh per instance, the unit the orchestrator reasons
	// about). Both empty means what it always meant: a standalone desktop
	// that is nobody's fleet member, and nothing changes for it.
	WorkroomID string
	RuntimeID  string
}

// Load reads the environment and fills in defaults.
func Load() Config {
	cfg := Config{
		HTTPPort: Int("HTTP_PORT", 8080),
		// The interface to listen on. Empty means every one, which is what a
		// container wants and what this always did. Set it to 127.0.0.1 when
		// something else terminates TLS in front — otherwise the backend goes
		// on answering in the clear on its own port, and the proxy is a
		// suggestion rather than a gate.
		HTTPAddr:  Str("HTTP_ADDR", ""),
		Display:   Str("DISPLAY", ":0"),
		FPS:       Int("FPS", 30),
		Encoder:   strings.ToLower(Str("ENCODER", "auto")),
		VideoKbps: Int("VIDEO_BITRATE_KBPS", 4000),
		// The floor the shared encoder never goes under, however bad one
		// participant's estimate gets.
		MinVideoKbps: Int("MIN_VIDEO_BITRATE_KBPS", 1200),
		AudioBitrate: Int("AUDIO_BITRATE", 96000),
		AudioDevice:  Str("AUDIO_DEVICE", "sentineldesk.monitor"),
		RemoteCursor: Bool("REMOTE_CURSOR", false),
		StunServer:   Str("STUN_SERVER", DefaultClientStun),
		MinPort:      uint16(Int("WEBRTC_MIN_PORT", 0)),
		MaxPort:      uint16(Int("WEBRTC_MAX_PORT", 0)),
		NAT1To1IP:    Str("NAT1TO1_IP", ""),
		PanelURL:     Str("PANEL_URL", ""),
		StunEmbedded: Bool("STUN_EMBEDDED", true),
		StunPort:     Int("STUN_PORT", 3478),
		ClientStun:   Str("CLIENT_STUN", DefaultClientStun),
		TurnUser:     Str("TURN_USER", ""),
		TurnPass:     Str("TURN_PASS", ""),
		AuthUser:     Str("AUTH_USER", ""),
		AuthPass:     Str("AUTH_PASS", ""),
		AuthSecret:   Str("AUTH_SECRET", ""),
		AuthTTL:      time.Duration(Int("AUTH_TTL_HOURS", 12)) * time.Hour,
		TLSCert:      Str("TLS_CERT", ""),
		TLSKey:       Str("TLS_KEY", ""),
		// HTTPS by default (2026-08-20, at the owner's decision): a desktop
		// reached over a network deserves a lock without being asked, and the
		// browser APIs the client leans on — the microphone, the rich
		// clipboard — only exist in secure contexts. TLS_SELFSIGNED=0 is the
		// explicit opt-out for the two hops where plain HTTP is legitimate:
		// the front desk's internal proxy to its rooms (the orchestrator pins
		// it), and a development loop behind someone else's TLS.
		TLSSelfSigned: Bool("TLS_SELFSIGNED", true),
		TLSDir:        Str("TLS_DIR", "/home/sentineldesk/.tls"),
		TLSHosts:      Str("TLS_HOSTS", ""),
		FilesRoot:     Str("FILES_ROOT", "/home/sentineldesk"),
		MaxViewers:    Int("MAX_VIEWERS", 4),
		PublicURL:     strings.TrimRight(Str("PUBLIC_URL", ""), "/"),
		// /var/log/sentineldesk is created and chowned to the desktop user by
		// the entrypoint. A path the daemon cannot write degrades to the
		// in-memory ring with a line on stderr, rather than refusing to start.
		ActionLog:      Str("ACTION_LOG", "/var/log/sentineldesk/actions.jsonl"),
		ActionLogMaxMB: Int("ACTION_LOG_MAX_MB", 64),
		WorkroomID:     Str("WORKROOM_ID", ""),
		RuntimeID:      Str("RUNTIME_ID", ""),
	}
	if urls := Str("CLIENT_TURN_URLS", ""); urls != "" {
		for _, u := range strings.Split(urls, ",") {
			if u = strings.TrimSpace(u); u != "" {
				cfg.ClientTurnURL = append(cfg.ClientTurnURL, u)
			}
		}
	}
	if cfg.MaxViewers < 1 {
		cfg.MaxViewers = 1
	}
	return cfg
}

// Str returns an environment variable, or def when it is unset or empty.
func Str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Int returns an environment variable parsed as an integer, or def.
func Int(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Bool reads the usual spellings of yes and no. Unrecognised values fall back
// to def rather than silently meaning false.
func Bool(key string, def bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return def
}
