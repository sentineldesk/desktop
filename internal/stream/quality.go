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
	"log"
	"time"

	"github.com/sentineldesk/desktop/internal/media"
)

// The stream's quality control: Auto, Media, Alta — chosen from the panel by
// whoever holds the controls, YouTube's three-word register instead of a list
// of framerates nobody should have to understand.
//
// This is a control of the ROOM, not of one viewer, and that follows from the
// architecture: the screen is encoded once and fanned out, so there is no
// per-viewer quality to sell. What the control actually adjusts is the cost
// of that one encode — a live cap on how many frames per second reach the
// encoder, dropped before they cost anything.
//
// Auto is the interesting position, and its signal earned its shape the
// empirical way. The first draft watched only the leaky queue that feeds the
// encoder — and a live test on a saturated 4-core VM delivered 11fps to the
// viewer while that queue sat empty the whole time, because starvation does
// not stop at the encoder: ximagesrc and videoconvert starve too, frames
// arrive at the queue already slow, and the pressure this was built to catch
// never shows up where it was being looked for. So the signal is now the
// whole path's own output: the frames per second actually LEAVING the
// encoder (counted off the RTP marker bits the fan-out already handles),
// with the queue kept as the fast tell for the encoder-bound case. A quiet
// screen — damage capture producing nothing — counts as headroom, because
// encoding nothing is the cheapest a stream gets. On hardware with a GPU the
// delivered rate simply matches the cap and Auto sits invisibly at the
// ceiling, which is why there is nothing to deactivate anywhere: the rule
// quiesces by itself where it has no work.
//
// The rules, stated once here and enforced in autoState.step:
//   - down fast: sustained pressure for ~2s drops one rung (ceiling → 24 →
//     20 → 15). Saturation hurts immediately — latency compounds while the
//     encoder falls behind — so the reaction is prompt.
//   - up slow: ~12s of a consistently empty queue climbs one rung back. The
//     asymmetry is what prevents the 30↔24 flapping that looks worse than a
//     steady 24.
//   - floor 15: below that, dragging a window feels broken, and the problem
//     stopped being one framerates can solve.
//
// Every change — manual or automatic — is a visible outcome: witnessed for
// the agent, broadcast to every session for the toolbar, logged for the
// operator. A stream that quietly changed its character is the bug class
// this repository ranks above a crash.

const (
	QualityAuto  = "auto"
	QualityMedia = "media"
	QualityHigh  = "high"
)

const (
	// Media in one number: two thirds of the usual ceiling, ~33% less encode
	// work, still fluid enough for work. Clamped to the ceiling for
	// deployments that already run below it.
	qualityMediaFPS = 20

	// Auto's cadence and thresholds. The tick is deliberately coarse — this
	// regulates a trend, not a frame.
	qualityTick   = 500 * time.Millisecond
	pressureLevel = 2  // queued buffers that mean "the encoder is behind"
	downAfter     = 4  // ticks of pressure before stepping down (~2s)
	upAfter       = 24 // ticks of calm before stepping back up (~12s)
	autoFloorFPS  = 15
)

// qualityLadder is Auto's descending set of framerates, ceiling first. The
// fixed rungs below the ceiling are shared with Media on purpose: fewer
// distinct behaviours to reason about.
func qualityLadder(ceiling int) []int {
	rungs := []int{ceiling, 24, qualityMediaFPS, autoFloorFPS}
	out := make([]int, 0, len(rungs))
	for _, r := range rungs {
		if r > ceiling {
			continue
		}
		if len(out) > 0 && out[len(out)-1] <= r {
			continue // keep strictly descending; a 20fps ceiling skips 24
		}
		out = append(out, r)
	}
	return out
}

// idleFPS: below this, the screen is quiet rather than underdelivering —
// damage-based capture producing (nearly) nothing is the cheapest state a
// stream has, and it must never read as the encoder failing.
const idleFPS = 2

// qualitySample is one tick's observation of the pipeline: how many buffers
// wait in the encoder's queue, and how many frames per second actually left
// it since the last tick.
type qualitySample struct {
	queued int
	fps    int
}

// autoState is the decision core, deliberately pure: it consumes one sample
// per tick and answers "what should the cap be now". Everything timing-
// related is counted in ticks so the rules are testable without a clock.
type autoState struct {
	ladder []int
	idx    int // current rung
	press  int // consecutive pressured ticks
	calm   int // consecutive calm ticks
}

func newAutoState(ceiling int) *autoState {
	return &autoState{ladder: qualityLadder(ceiling)}
}

// step consumes one sample and reports the rung to run at, and whether this
// sample changed it.
func (a *autoState) step(s qualitySample) (fps int, changed bool) {
	rung := a.ladder[a.idx]
	switch {
	case s.queued >= pressureLevel || (s.fps >= idleFPS && s.fps*4 < rung*3):
		// Frames waiting behind the encoder, or frames flowing at less than
		// three quarters of what was asked for: the path is not keeping up.
		a.press++
		a.calm = 0
	case s.queued == 0 && (s.fps < idleFPS || s.fps*10 >= rung*9):
		// Nothing queued and either a quiet screen or delivery at (nearly)
		// the full cap: headroom either way.
		a.calm++
		a.press = 0
	default:
		// The band between — delivering, but ambiguously. Neither streak
		// survives it: hysteresis is the whole point.
		a.press = 0
		a.calm = 0
	}

	switch {
	case a.press >= downAfter && a.idx < len(a.ladder)-1:
		a.idx++
		a.press = 0
		return a.ladder[a.idx], true
	case a.calm >= upAfter && a.idx > 0:
		a.idx--
		a.calm = 0
		return a.ladder[a.idx], true
	}
	return rung, false
}

// SetQuality is the wire's entry: a member chose a position. The gate — who
// may choose — belongs to the session that received the frame; here the mode
// is applied, witnessed and broadcast. An unknown mode is a named refusal,
// not a silent default.
func (r *Room) SetQuality(memberName, mode string) error {
	switch mode {
	case QualityAuto, QualityMedia, QualityHigh:
	default:
		return fmt.Errorf("quality %q is not a position this room has — auto, media or high", mode)
	}

	r.mu.Lock()
	if r.qualityMode == mode {
		r.mu.Unlock()
		return nil // pressing the position you are in is not an event
	}
	r.qualityMode = mode
	r.qualityBy = memberName
	r.applyQualityLocked()
	fps := r.qualityFPS
	r.mu.Unlock()

	r.witness.Note(memberName, "set the stream quality to "+mode,
		fmt.Sprintf("%d fps cap", fps))
	log.Printf("room: quality %s (%d fps) by %s", mode, fps, memberName)
	r.broadcastQuality()
	return nil
}

// applyQualityLocked makes the current mode true against the running video
// pipeline. Callers hold r.mu. Safe with no pipeline: the mode is state, and
// startPipelines applies it when the capture comes up.
func (r *Room) applyQualityLocked() {
	// Whatever was regulating before stands down first.
	if r.qualityCancel != nil {
		close(r.qualityCancel)
		r.qualityCancel = nil
	}

	ceiling := r.cfg.FPS
	switch r.qualityMode {
	case QualityMedia:
		r.qualityFPS = min(qualityMediaFPS, ceiling)
	case QualityHigh:
		r.qualityFPS = ceiling
	default: // auto starts at the ceiling and earns its way down
		r.qualityFPS = ceiling
	}

	if r.videoPipe == nil {
		return
	}
	r.videoPipe.SetFPSCap(r.qualityFPS)
	if r.qualityMode == QualityAuto {
		cancel := make(chan struct{})
		r.qualityCancel = cancel
		go r.autoQualityLoop(r.videoPipe, newAutoState(ceiling), cancel)
	}
}

// autoQualityLoop runs while the room is in Auto with a live pipeline. It
// holds no lock while sleeping and re-checks nothing it does not own: the
// pipe pointer it was handed stays valid until the cancel channel closes,
// which stopPipelines does before tearing the pipeline down.
func (r *Room) autoQualityLoop(pipe *media.MediaPipeline, state *autoState, cancel <-chan struct{}) {
	tick := time.NewTicker(qualityTick)
	defer tick.Stop()
	prev := state.ladder[0]
	ticksPerSecond := int(time.Second / qualityTick)
	for {
		select {
		case <-cancel:
			return
		case <-tick.C:
		}
		// The delivered rate: whole frames that left the encoder since the
		// last tick, counted by writeVideo off the RTP marker bit.
		frames := int(r.qualityFrames.Swap(0))
		fps, changed := state.step(qualitySample{
			queued: pipe.QueuePressure(),
			fps:    frames * ticksPerSecond,
		})
		if !changed {
			continue
		}
		pipe.SetFPSCap(fps)
		r.mu.Lock()
		r.qualityFPS = fps
		r.mu.Unlock()
		// An automatic step is as much an outcome as a pressed button, and
		// the wording says which it was: nobody should read "the stream
		// changed" and go looking for who touched it.
		why := "the encoder has headroom again"
		if fps < prev {
			why = "the encoder was at its limit"
		}
		prev = fps
		r.witness.Note("the stream", "stepped to a new framerate on its own",
			fmt.Sprintf("%d fps — %s", fps, why))
		log.Printf("room: quality auto -> %d fps", fps)
		r.broadcastQuality()
	}
}

// QualityState answers the wire: the mode, the cap in force, who set it.
func (r *Room) QualityState() (mode string, fps int, by string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	mode = r.qualityMode
	if mode == "" {
		mode = QualityAuto
	}
	fps = r.qualityFPS
	if fps == 0 {
		fps = r.cfg.FPS
	}
	return mode, fps, r.qualityBy
}

// broadcastQuality tells every session what the stream now costs, so each
// toolbar shows the same truth.
func (r *Room) broadcastQuality() {
	mode, fps, by := r.QualityState()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.members {
		if m.session == nil {
			continue
		}
		m.session.sendQuality(mode, fps, by, "")
	}
}
