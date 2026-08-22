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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The stream's status, written to disk for the desktop to wear.
//
// The room knows the sending half of the story — encoder bitrate, frames
// actually delivered, the quality position, who is watching — and each
// viewer's client measures the receiving half (the numbers its Statistics
// panel already shows) and reports them back over the DataChannel every
// couple of seconds. This file is where the two halves meet, and putting
// them ON DISK rather than only on a wire is the golden rule applied to
// telemetry: the conky card reads it, and so can the agent — "how well are
// the people watching me receiving this?" becomes a file read.
//
//	/tmp/sentineldesk/stream.status   key=value plus one line per viewer
//	/tmp/sentineldesk/stream.fps      one number, food for conky's execgraph
//
// Writes are atomic (tmp + rename): a torn read would draw a corrupted card
// on everyone's screen at the exact cadence of the writer.

const (
	streamStatusDir  = "/tmp/sentineldesk"
	streamStatusFile = "stream.status"
	streamFPSFile    = "stream.fps"
	streamRateFile   = "stream.rate"
	streamHistFile   = "stream.hist"
	histSamples      = 60
	statusEvery      = 1 * time.Second
	// A report older than this is a viewer who stopped reporting — a closed
	// tab whose session has not timed out yet. Stale rows mislead more than
	// absent ones.
	viewStatsTTL = 10 * time.Second
)

// viewerStats is one client's self-reported reception, clamped on arrival.
type viewerStats struct {
	fps    int
	kbps   int
	rtt    int // ms
	behind int // ms behind live (jitter buffer depth)
	lossPM int // packet loss, per mille
	at     time.Time
}

// clampStat keeps a client-supplied number displayable: this is telemetry
// from the other side of the wire, shown on a shared screen and read by an
// agent — data, never trusted arithmetic.
func clampStat(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// ReportViewStats records what one viewer says it is receiving.
func (r *Room) ReportViewStats(memberID string, fps, kbps, rtt, behind, lossPM int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[memberID]; !ok {
		return
	}
	if r.viewStats == nil {
		r.viewStats = map[string]viewerStats{}
	}
	r.viewStats[memberID] = viewerStats{
		fps:    clampStat(fps, 240),
		kbps:   clampStat(kbps, 1_000_000),
		rtt:    clampStat(rtt, 60_000),
		behind: clampStat(behind, 600_000),
		lossPM: clampStat(lossPM, 1000),
		at:     time.Now(),
	}
}

// statusSnapshot collects everything one write needs under a single lock.
type statusSnapshot struct {
	viewers int
	kbps    int
	mode    string
	capFPS  int
	rows    []string // "viewer <name> fps kbps rtt behind lossPM driving", name sanitised
}

func (r *Room) snapshotStatus() statusSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := statusSnapshot{kbps: r.lastRate, mode: r.qualityMode, capFPS: r.qualityFPS}
	if s.mode == "" {
		s.mode = QualityAuto
	}
	if s.capFPS == 0 {
		s.capFPS = r.cfg.FPS
	}
	now := time.Now()
	var driver []string
	for id, m := range r.members {
		if m.video == nil {
			continue // the agent watches through other eyes
		}
		s.viewers++
		vs, ok := r.viewStats[id]
		if !ok || now.Sub(vs.at) > viewStatsTTL {
			continue
		}
		name := strings.Map(func(c rune) rune {
			// One line per viewer, fields split on spaces: the name a person
			// typed at the door must not be able to forge columns or rows.
			if c == '\n' || c == '\r' || c == ' ' || c == '=' {
				return '_'
			}
			return c
		}, m.name)
		if name == "" {
			name = id
		}
		driving := 0
		if id == r.controller {
			driving = 1
		}
		row := fmt.Sprintf("viewer %s %d %d %d %d %d %d",
			name, vs.fps, vs.kbps, vs.rtt, vs.behind, vs.lossPM, driving)
		// Whoever holds the controls leads the list, at the owner's ask: with
		// several people watching, the reception that matters first is the
		// driver's — a laggy driver is everyone's problem.
		if driving == 1 {
			driver = append(driver, row)
		} else {
			s.rows = append(s.rows, row)
		}
	}
	sort.Strings(s.rows)
	s.rows = append(driver, s.rows...)
	return s
}

// formatStatus renders the snapshot plus the delivered framerate as the file's
// content. Split out pure so the shape is testable without a clock or a room.
func formatStatus(s statusSnapshot, deliveredFPS, sentKbps int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "viewers=%d\n", s.viewers)
	fmt.Fprintf(&b, "kbps=%d\n", s.kbps)
	fmt.Fprintf(&b, "sent=%d\n", sentKbps)
	fmt.Fprintf(&b, "fps=%d\n", deliveredFPS)
	fmt.Fprintf(&b, "quality=%s\n", s.mode)
	fmt.Fprintf(&b, "cap=%d\n", s.capFPS)
	for _, row := range s.rows {
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return b.String()
}

// writeStatusFile lands content atomically at dir/name.
func writeStatusFile(dir, name, content string) {
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(dir, name))
}

// streamStatusLoop runs while the capture does, mirroring the stream onto
// disk every couple of seconds. It owns statFrames — the second frame
// counter beside Auto's, because two consumers swapping one counter would
// each see half the truth.
func (r *Room) streamStatusLoop(cancel <-chan struct{}) {
	if err := os.MkdirAll(streamStatusDir, 0o755); err != nil {
		return
	}
	tick := time.NewTicker(statusEvery)
	defer tick.Stop()
	// The sparkline's memory: the last minute of sent bitrate, oldest first,
	// one value per line. The desktop's Lua redraws the whole polyline from
	// it every tick — the same shape the browser's own spark draws from its
	// rolling window.
	hist := make([]int, 0, histSamples)
	for {
		select {
		case <-cancel:
			// The capture ended: say so rather than letting the last numbers
			// sit there looking current. An empty room's card reads "offline"
			// because that is what is true.
			writeStatusFile(streamStatusDir, streamStatusFile, "viewers=0\noffline=1\n")
			writeStatusFile(streamStatusDir, streamFPSFile, "0\n")
			return
		case <-tick.C:
		}
		secs := int(statusEvery / time.Second)
		if secs < 1 {
			secs = 1
		}
		fps := int(r.statFrames.Swap(0)) / secs
		// What actually left the encoder, not what GCC asked for: on a quiet
		// screen the damage capture sends nearly nothing, and the wave on the
		// card should say so — that dip IS the traffic being honest.
		sentKbps := int(r.statBytes.Swap(0)) * 8 / 1000 / secs
		hist = append(hist, sentKbps)
		if len(hist) > histSamples {
			hist = hist[1:]
		}
		var hb strings.Builder
		for _, v := range hist {
			fmt.Fprintf(&hb, "%d\n", v)
		}
		writeStatusFile(streamStatusDir, streamStatusFile, formatStatus(r.snapshotStatus(), fps, sentKbps))
		writeStatusFile(streamStatusDir, streamFPSFile, fmt.Sprintf("%d\n", fps))
		writeStatusFile(streamStatusDir, streamRateFile, fmt.Sprintf("%d\n", sentKbps))
		writeStatusFile(streamStatusDir, streamHistFile, hb.String())
	}
}
