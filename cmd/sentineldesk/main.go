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

// Command sentineldesk runs a Linux desktop and streams it to the browser over
// WebRTC.
//
// One binary serves everything:
//   - HTTP : the embedded browser client and the file-transfer endpoints
//   - WS   : /ws, which is the only door — authentication happens there, and
//     nothing else is delivered until it succeeds
//   - MCP  : a local Unix socket that lets an AI agent drive the desktop
//
// Video (ximagesrc) and audio (pulsesrc) are captured and encoded by GStreamer
// inside this process (go-gst); each RTP packet goes straight from appsink to a
// Pion track. The encoder is chosen automatically (NVENC → VA-API → VP8) and
// steered at runtime: keyframes on PLI, bitrate from congestion estimation.
//
// This file is wiring only. The behaviour lives under internal/.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/sentineldesk/desktop/internal/desktop"
	"github.com/sentineldesk/desktop/internal/mcp"
	"github.com/sentineldesk/desktop/internal/media"
	"github.com/sentineldesk/desktop/internal/stream"
	"github.com/sentineldesk/desktop/internal/webui"
	"github.com/sentineldesk/desktop/pkg/config"
	"github.com/sentineldesk/desktop/pkg/ratelimit"
	"github.com/sentineldesk/desktop/pkg/version"
)

// Accept the upgrade from the page we serve, from an origin somebody named, or
// when there is no Origin at all (non-browser clients — the WebSocket login
// validates them anyway).
//
// ALLOWED_ORIGINS is what lets a desktop be watched from somewhere other than
// itself. This runtime was written to serve its own client from its own host,
// so same-origin was the whole rule and it cost nothing — until a front desk
// started rooms and served ONE panel for all of them from a different port.
// Every desktop then refused the only client that had a ticket for it, with a
// message about origins that named nothing about the arrangement.
//
// The check is not what protects this door and never was: the first frame must
// authenticate, and the credential is a token in that frame rather than a
// cookie the browser attaches by itself — so a hostile page opening a socket
// gets a socket it cannot use. Keeping the check narrow is defence in depth,
// which is why this is a list rather than "allow anything".
var allowedOrigins = parseOrigins(config.Str("ALLOWED_ORIGINS", ""))

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		if strings.EqualFold(u.Host, r.Host) {
			return true
		}
		for _, allowed := range allowedOrigins {
			if strings.EqualFold(allowed, u.Host) || strings.EqualFold(allowed, origin) {
				return true
			}
		}
		return false
	},
}

// parseOrigins reads `a.example,https://b.example:9090` into a list of hosts.
//
// Both spellings are accepted because both are what people write, and a
// configuration that works only with the scheme somebody happened to include
// is a configuration that fails at 3am for a reason nobody can see.
func parseOrigins(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if u, err := url.Parse(part); err == nil && u.Host != "" {
			out = append(out, u.Host)
			continue
		}
		out = append(out, part)
	}
	return out
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("sentineldesk: ")

	// -mcp-stdio runs a thin stdio<->socket bridge for an AI host instead of the
	// daemon. It never touches the desktop.
	mcpStdio := flag.Bool("mcp-stdio", false, "run as MCP stdio bridge (connects to -mcp-sock)")
	mcpSock := flag.String("mcp-sock", config.Str("MCP_SOCK", ""), "MCP unix socket path")
	// These restrict ONLY this connection. The daemon sets the ceiling through
	// MCP_POLICY; a bridge can drop below it but never rise above, which is what
	// makes it safe to hand an agent a read-only endpoint.
	mcpPolicy := flag.String("mcp-policy", "", "restrict this bridge: full | safe | readonly")
	mcpDeny := flag.String("mcp-deny", "", "comma-separated tools to deny (suffix * for prefix match)")
	mcpAllow := flag.String("mcp-allow", "", "comma-separated allow-list for this bridge")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("sentineldesk", version.String())
		return
	}

	if *mcpStdio {
		if err := mcp.RunBridge(*mcpSock, *mcpPolicy, *mcpDeny, *mcpAllow); err != nil {
			log.Fatalf("mcp-stdio: %v", err)
		}
		return
	}

	cfg := config.Load()
	// Said once at the top, because in a fleet the FIRST question about any
	// log is whose it is. A standalone desktop prints nothing here — absence
	// of identity is itself the answer.
	if cfg.WorkroomID != "" || cfg.RuntimeID != "" {
		log.Printf("identity: workroom=%s runtime=%s", cfg.WorkroomID, cfg.RuntimeID)
	}
	gst.Init(nil)

	injector, err := desktop.NewInputInjector(cfg.Display)
	if err != nil {
		log.Fatalf("cannot connect to display %s: %v", cfg.Display, err)
	}

	strategy := media.SelectEncoder(cfg.Encoder)
	log.Printf("video strategy: %s (%s, hardware=%v)", strategy.Name, strategy.MimeType, strategy.Hardware)

	// The embedded STUN responder, on by default: a VPS install answers
	// "what is my address?" itself and no third-party STUN sits in the
	// path. A busy port degrades to a log line and the browsers' configured
	// fallback, like every optional capability here.
	if cfg.StunEmbedded {
		if stun, err := stream.StartStun(cfg.StunPort); err != nil {
			log.Printf("embedded STUN disabled: %v", err)
		} else {
			log.Printf("embedded STUN answering on %s", stun.Addr())
		}
	}

	// One room: the screen is encoded ONCE and fanned out to everyone watching,
	// rather than a pair of pipelines per client.
	room := stream.NewRoom(cfg, strategy)
	// The real cursor's position, broadcast so viewers can see where
	// whoever is driving actually is — the live capture deliberately
	// leaves the pointer out of the video (see Room.TrackPointer).
	room.TrackPointer(injector.Pointer)

	// The room reads its verb gates off the same catalogue the MCP server
	// serves (§4.6) — one menu, two wires. Unconditional, not inside the MCP
	// block below: the menu is a set of literals and exists whether or not an
	// agent ever connects, and a room without it gates conservatively, which
	// would quietly demand the controls for verbs that do not need them.
	room.SetCapabilities(mcp.Catalogue())

	// One recorder, shared by the agent and by the toolbar button: a recording
	// is a single resource and two of them writing at once would collide.
	recorder := media.NewRecorder(cfg.Display, cfg.AudioDevice, "")

	// The return path: whatever the browser's microphone captures enters the
	// desktop as an ordinary capture device.
	//
	// Built now rather than on the first share. A page enumerates its audio
	// devices when it loads, so a microphone that appears later is missing from
	// the list of the very page that wanted it.
	upstream := media.NewUpstream(cfg)
	if err := upstream.EnsureMic(); err != nil {
		log.Printf("virtual microphone unavailable: %v", err)
	}

	// With a local cursor the client needs the real pointer shape (resize
	// arrows, text beam, hand…); XFixes reports every change.
	var cursors *desktop.CursorTracker
	if !cfg.RemoteCursor {
		if cursors, err = desktop.NewCursorTracker(cfg.Display); err != nil {
			log.Printf("no cursor-shape tracking (XFixes): %v", err)
			cursors = nil
		}
	}

	// Two-way clipboard (xclip). Optional: missing xclip degrades to no
	// clipboard sync, never to a broken desktop.
	clip := desktop.NewClipboard(cfg.Display)

	auth := stream.NewAuth(cfg.AuthUser, cfg.AuthPass, cfg.AuthSecret, cfg.AuthTTL)
	webRoot := webui.FS()

	gate := ratelimit.NewIPGate()

	mux := http.NewServeMux()
	if cfg.PanelURL != "" {
		// A fleet room's people arrive through the panel; the embedded client
		// staying reachable here would be a second, older UI on a port
		// somebody will eventually try. Everything the PANEL needs from this
		// port — /ws, the media, the ticketed downloads — is mounted above
		// and keeps working; only the pages go.
		panel := cfg.PanelURL
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, panel, http.StatusTemporaryRedirect)
		}))
	} else {
		mux.Handle("/", webui.Handler())
	}

	// There is no /docs/ route. The guide used to be embedded here and served
	// behind the same login as the desktop; it is published on the project site
	// now, so the rail links out to it instead. That makes documentation the one
	// thing in this binary that needs a route to the internet — deliberately, to
	// keep a single copy that cannot drift from what a reader is looking at.
	// Nothing about running, streaming or driving the desktop depends on it.

	// The file tree's confinement (FILES_ROOT). No routes: files move over
	// each session's DataChannel since 2026-08-20, and the web door that used
	// to serve the manager closed behind them — this object now exists for
	// its resolve/rel discipline and for the delivery of finished captures.
	files := stream.NewFileServer(cfg.FilesRoot)

	// The log viewer: one page showing what the agent ran AND what the people
	// here ran. The symmetry is the reason it exists — both parties on this
	// desktop leave a record, and until now neither could be read without a
	// shell inside the container, which is precisely what the person doing the
	// supervising does not have.
	//
	// Behind the same session token as the file manager. Job output is a
	// transcript of a shell on a shared desktop and shell.log is the one file
	// here that keeps typed text verbatim; serving either of them openly would
	// be a wider hole than the file manager it sits beside. See logs.go.
	stream.NewLogServer(auth, cfg.ActionLog).Register(mux)

	// Handing finished screenshots and recordings to the browsers. It is what
	// makes destination:download work for both the agent and the person.
	delivery := stream.NewDelivery(files, room)

	// MCP server: an AI agent drives the desktop over a local Unix socket.
	if *mcpSock != "" {
		server := mcp.NewServer(cfg, injector, clip, recorder)
		server.SetDelivery(delivery)
		// The agent joins the same room as the people: it gets a name in the
		// participant list and has to take turns with them. Without this it
		// would still work, but invisibly — a pointer moving on its own with
		// nobody able to tell whether a colleague or the model was driving.
		server.SetRoom(room, config.Str("AGENT_NAME", "AI agent"))

		// The panic button, joined up. This is the one signal that travels from
		// the people's plane to the agent's, and it is wired here rather than
		// inside either of them because neither should have to know the other
		// exists: the room raises an event, the MCP server knows how to stop
		// work, and this line is the whole of the coupling.
		room.OnAbort(func(who string) {
			if n := server.AbortAll(who); n > 0 {
				log.Printf("mcp: %s pressed abort, stopped %d running job(s)", who, n)
			} else {
				log.Printf("mcp: %s pressed abort; nothing was running", who)
			}
		})

		// The pause, which is the panic button's reversible sibling. Same one
		// line of coupling, and the same direction of travel: the room raises
		// the event, the MCP server knows how to hold and to let go.
		room.OnPause(func(who string, on bool) {
			if on {
				log.Printf("mcp: %s paused the agent, %d job(s) suspended",
					who, server.PauseAll(who))
			} else {
				log.Printf("mcp: %s resumed the agent, %d job(s) woken",
					who, server.ResumeAll(who))
			}
		})

		if err := server.Listen(*mcpSock); err != nil {
			log.Printf("mcp: cannot open socket %s: %v", *mcpSock, err)
		}
	}

	// The only informational endpoint (always 200, no secrets): it says whether
	// a login is required. Credentials, ICE configuration and everything else
	// travel over the authenticated WebSocket.
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"required": auth.Enabled()})
	})

	// Some browsers request /favicon.ico unconditionally: always serve it so a
	// 404 never shows up in the console.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(webRoot, "favicon.svg")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Rate-limit by origin BEFORE the upgrade: a banned or too-eager origin
		// does not even get to spend a WebSocket handshake.
		ip := ratelimit.ClientIP(r)
		key := ratelimit.GateKey(ip)
		if gate.Banned(key) {
			http.Error(w, "locked", http.StatusTooManyRequests)
			return
		}
		if ok, retryAfter := gate.Take(key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds()+1)))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("websocket upgrade: %v", err)
			return
		}
		sess := stream.NewSession(cfg, strategy, room, upstream, recorder, delivery, injector, cursors,
			clip, auth, gate, key, ws, ip)
		sess.Run() // blocks until the client disconnects
	})

	addr := cfg.HTTPAddr + ":" + strconv.Itoa(cfg.HTTPPort)
	certFile, keyFile, err := stream.EnsureTLS(cfg)
	if err != nil {
		log.Fatalf("TLS configuration: %v", err)
	}
	scheme := "http"
	if certFile != "" {
		scheme = "https"
	}
	log.Printf("desktop on %s://0.0.0.0%s (display %s, %d fps, auth=%v)",
		scheme, addr, cfg.Display, cfg.FPS, cfg.AuthUser != "")
	if certFile != "" {
		log.Fatal(http.ListenAndServeTLS(addr, certFile, keyFile, mux))
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}
