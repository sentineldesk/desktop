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
	"reflect"
	"testing"
)

// The quality controller's rules, held against the decision core directly.
// The core is deliberately pure — samples in, rung out, time counted in ticks
// — so these run with no clock, no pipeline and no X, like everything else
// this package can promise from a laptop.

// Shorthands for the three kinds of tick a pipeline produces.
func pressured() qualitySample     { return qualitySample{queued: pressureLevel} }
func calmAt(fps int) qualitySample { return qualitySample{queued: 0, fps: fps} }
func quiet() qualitySample         { return qualitySample{queued: 0, fps: 0} }

func TestQualityLadderRespectsTheCeiling(t *testing.T) {
	cases := []struct {
		ceiling int
		want    []int
	}{
		{30, []int{30, 24, 20, 15}},
		{60, []int{60, 24, 20, 15}},
		{24, []int{24, 20, 15}},
		{20, []int{20, 15}},
		{15, []int{15}},
		// A deployment already below the floor gets exactly what it asked
		// for and no rungs at all: there is nowhere sane to step to.
		{10, []int{10}},
	}
	for _, c := range cases {
		if got := qualityLadder(c.ceiling); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ladder(%d) = %v, want %v", c.ceiling, got, c.want)
		}
	}
}

func TestSustainedPressureStepsDownAndOnlySustained(t *testing.T) {
	a := newAutoState(30)

	// Three pressured ticks, then a calm one: no step. The calm tick resets
	// the count — a blip is not saturation.
	for i := 0; i < downAfter-1; i++ {
		if _, changed := a.step(pressured()); changed {
			t.Fatalf("stepped down after only %d pressured ticks", i+1)
		}
	}
	if _, changed := a.step(quiet()); changed {
		t.Fatal("a single calm tick must not change the rung")
	}

	// Now the real thing: downAfter consecutive pressured ticks.
	var fps int
	var changed bool
	for i := 0; i < downAfter; i++ {
		fps, changed = a.step(pressured())
	}
	if !changed || fps != 24 {
		t.Fatalf("after %d pressured ticks: fps=%d changed=%v, want 24, true", downAfter, fps, changed)
	}
}

// The lesson the first live test taught, frozen: a saturated box starves the
// CAPTURE too, so the encoder's queue sits empty while the viewer gets 11fps.
// Underdelivery with an empty queue must still read as pressure.
func TestStarvedCaptureIsPressureEvenWithAnEmptyQueue(t *testing.T) {
	a := newAutoState(30)
	var fps int
	var changed bool
	for i := 0; i < downAfter; i++ {
		fps, changed = a.step(qualitySample{queued: 0, fps: 11})
	}
	if !changed || fps != 24 {
		t.Fatalf("11fps delivered against a 30fps rung: fps=%d changed=%v, want 24, true", fps, changed)
	}
}

// And the twin guard: a QUIET screen delivers ~0fps because damage capture
// has nothing to say, and that is headroom, never failure.
func TestAQuietScreenIsHeadroomNotFailure(t *testing.T) {
	a := newAutoState(30)
	for i := 0; i < downAfter; i++ {
		a.step(pressured())
	}
	// Down at 24; a long quiet spell must climb back, not sink further.
	var fps int
	var changed bool
	for i := 0; i < upAfter; i++ {
		fps, changed = a.step(quiet())
	}
	if !changed || fps != 30 {
		t.Fatalf("a quiet screen should climb back: fps=%d changed=%v, want 30, true", fps, changed)
	}
}

func TestClimbingBackIsSlowByDesign(t *testing.T) {
	a := newAutoState(30)
	for i := 0; i < downAfter; i++ {
		a.step(pressured())
	}

	// upAfter-1 calm ticks at full delivery: still at 24. The asymmetry IS
	// the rule: it keeps the stream from flapping 30↔24 every few seconds.
	for i := 0; i < upAfter-1; i++ {
		if fps, changed := a.step(calmAt(24)); changed {
			t.Fatalf("climbed after only %d calm ticks (fps=%d)", i+1, fps)
		}
	}
	fps, changed := a.step(calmAt(24))
	if !changed || fps != 30 {
		t.Fatalf("after %d calm ticks: fps=%d changed=%v, want 30, true", upAfter, fps, changed)
	}
}

func TestTheAmbiguousBandResetsBothStreaks(t *testing.T) {
	a := newAutoState(30)
	for i := 0; i < downAfter; i++ {
		a.step(pressured())
	}
	// Nearly a full climb's worth of calm, one middling sample, then what
	// would have finished the climb: the middling sample must have reset it.
	for i := 0; i < upAfter-1; i++ {
		a.step(quiet())
	}
	// 26fps against a 24 rung with one buffer queued: neither streak.
	if _, changed := a.step(qualitySample{queued: 1, fps: 26}); changed {
		t.Fatal("one buffer in flight is neither pressure nor headroom")
	}
	if fps, changed := a.step(quiet()); changed {
		t.Fatalf("the calm streak survived a middling sample (fps=%d)", fps)
	}
}

func TestTheFloorAndTheCeilingHold(t *testing.T) {
	a := newAutoState(30)
	// Pressure forever: lands on the floor and stays there.
	for i := 0; i < downAfter*10; i++ {
		a.step(pressured())
	}
	if fps, _ := a.step(pressured()); fps != autoFloorFPS {
		t.Fatalf("sustained pressure should rest on the floor, got %d", fps)
	}
	// Calm forever: back to the ceiling and no further.
	for i := 0; i < upAfter*10; i++ {
		a.step(quiet())
	}
	if fps, _ := a.step(quiet()); fps != 30 {
		t.Fatalf("sustained calm should rest on the ceiling, got %d", fps)
	}
}

// The stream-status mirror's shape, held still: keys the card script parses,
// the driver's row leading, and client numbers clamped into displayability.
func TestStreamStatusFormatAndClamp(t *testing.T) {
	s := statusSnapshot{viewers: 2, kbps: 3500, mode: "auto", capFPS: 30,
		rows: []string{
			"viewer fede 28 3400 12 140 0 1",
			"viewer ana 24 2100 80 300 5 0",
		}}
	got := formatStatus(s, 27, 3100)
	want := "viewers=2\nkbps=3500\nsent=3100\nfps=27\nquality=auto\ncap=30\n" +
		"viewer fede 28 3400 12 140 0 1\nviewer ana 24 2100 80 300 5 0\n"
	if got != want {
		t.Errorf("formatStatus:\n got %q\nwant %q", got, want)
	}
	if clampStat(-5, 100) != 0 || clampStat(1e6, 240) != 240 || clampStat(30, 240) != 30 {
		t.Error("clampStat does not keep client numbers displayable")
	}
}
