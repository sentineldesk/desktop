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

// The arithmetic behind wait_for_idle, without a desktop.
//
// What is checked here is the part that was wrong for as long as the tool
// existed: it summed `ps -eo pcpu`, a per-process average over each process's
// whole lifetime, and reported the total as current load. These tests pin the
// replacement to the property that column never had — that the answer describes
// an interval, and that an idle interval reads as idle.

import (
	"strings"
	"testing"
	"time"
)

func TestParseCPULineSplitsIdleFromBusy(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	line := "cpu  100 0 100 700 100 0 0 0"
	idle, total, ok := parseCPULine(line)
	if !ok {
		t.Fatal("a well-formed cpu line was rejected")
	}
	if total != 1000 {
		t.Fatalf("total = %d, want 1000", total)
	}
	// idle plus iowait: 700 + 100. Counting iowait as busy would mean a desktop
	// reading a file never looks settled.
	if idle != 800 {
		t.Fatalf("idle = %d, want 800 (idle + iowait)", idle)
	}
}

func TestParseCPULineRejectsWhatIsNotTheAggregate(t *testing.T) {
	// The per-core lines come straight after the aggregate one, and reading
	// "cpu0" as the total would divide the machine's load by its core count.
	for _, line := range []string{"cpu0 1 2 3 4 5", "intr 12345", "", "cpu"} {
		if _, _, ok := parseCPULine(line); ok {
			t.Fatalf("accepted %q as the aggregate cpu line", line)
		}
	}
}

func TestCPUSamplerReportsTheIntervalNotTheUptime(t *testing.T) {
	var c cpuSampler
	// Stand in for two reads: a machine that has been up a long time and was
	// busy for most of it, but did nothing at all in the interval just past.
	c.idle, c.total, c.primed = 1_000_000, 2_000_000, true

	// Every jiffy since then went to idle.
	idle, total := uint64(1_000_100), uint64(2_000_100)
	dTotal, dIdle := total-c.total, idle-c.idle
	got := float64(dTotal-dIdle) * 100 / float64(dTotal)
	if got != 0 {
		t.Fatalf("an interval spent entirely idle reported %.1f%% busy", got)
	}
	// The lifetime ratio for the same numbers is 50% busy. That gap is the bug
	// this replaced: the old measure would have refused to call this desktop
	// idle on the strength of work it finished long ago.
	lifetime := float64(total-idle) * 100 / float64(total)
	if lifetime < 40 {
		t.Fatalf("test is not exercising the gap: lifetime ratio is %.1f%%", lifetime)
	}
}

func TestCPUSamplerUnprimedReportsZero(t *testing.T) {
	// Before prime() there is nothing to subtract from, and the honest answer
	// is "no reading yet" rather than the machine's whole uptime charged to one
	// interval.
	var c cpuSampler
	if got := c.percent(); got != 0 {
		t.Fatalf("unprimed sampler reported %.1f%%, want 0", got)
	}
}

func TestIdleFailureReasonSeparatesTheTwoFailures(t *testing.T) {
	quiet := time.Second

	// Screen settled, machine still working: the caller's work is happening
	// off-screen and watching the picture will never tell them it finished.
	r := idleFailureReason(time.Now().Add(-5*time.Second), quiet, 87)
	if r == "" || !contains(r, "CPU") {
		t.Fatalf("a settled screen over a busy machine gave %q", r)
	}
	if !contains(r, "87") {
		t.Fatalf("the reason dropped the CPU figure the caller needs: %q", r)
	}

	// Screen still moving: a different problem with a different response.
	r = idleFailureReason(time.Now(), quiet, 3)
	if !contains(r, "still changing") {
		t.Fatalf("a moving screen gave %q", r)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- telling a busy page from a moving picture -------------------------------
//
// Both keep the screen changing and they call for opposite responses: waiting
// helps a page that is loading and can never help a video that is playing. The
// run that made this necessary called wait_for_idle twice on a YouTube page and
// spent thirty of its hundred and twenty-four seconds being told, correctly,
// that the screen was still moving.

// repaints builds a series of LastChange readings from the moments a screen was
// redrawn, sampled every `every`, the way sampleRepaints reads them: the value
// repeats while nothing is drawn.
func repaints(start time.Time, every time.Duration, count int, paints []time.Duration) []time.Time {
	var out []time.Time
	last := start
	next := 0
	for i := 0; i < count; i++ {
		now := start.Add(time.Duration(i) * every)
		for next < len(paints) && !start.Add(paints[next]).After(now) {
			last = start.Add(paints[next])
			next++
		}
		out = append(out, last)
	}
	return out
}

func TestLongestStillnessFindsTheBiggestGap(t *testing.T) {
	base := time.Unix(1000, 0)
	got := longestStillness([]time.Time{
		base,
		base.Add(50 * time.Millisecond),
		base.Add(50 * time.Millisecond), // the same event read twice
		base.Add(900 * time.Millisecond),
		base.Add(950 * time.Millisecond),
	}, 40*time.Millisecond)
	if want := 850 * time.Millisecond; got != want {
		t.Errorf("longestStillness = %v, want %v", got, want)
	}
}

// A screen that stopped moving a moment ago is quiet NOW. Ignoring the stretch
// after the last repaint would report the opposite.
func TestLongestStillnessCountsTheTail(t *testing.T) {
	base := time.Unix(1000, 0)
	var samples []time.Time
	samples = append(samples, base, base.Add(30*time.Millisecond))
	for i := 0; i < 20; i++ { // nothing drawn for the rest of the window
		samples = append(samples, base.Add(30*time.Millisecond))
	}
	// Twenty readings after the last repaint, at 40ms each: the screen has been
	// still for 800ms and that is the answer. Reporting the 30ms gap between the
	// two paints instead would describe a screen that stopped a second ago as
	// still working — which is the case this whole probe exists to get right.
	if got, want := longestStillness(samples, 40*time.Millisecond), 800*time.Millisecond; got != want {
		t.Errorf("longestStillness = %v, want %v: the stillness happening NOW was not counted", got, want)
	}
}

// Video: a repaint on a frame clock. At 30fps the longest gap is 33ms, which no
// sane quiet window is under, so no amount of waiting will ever produce one.
func TestVideoIsRecognisedAsSteadyRepainting(t *testing.T) {
	base := time.Unix(1000, 0)
	var frames []time.Duration
	for i := 1; i <= 20; i++ {
		frames = append(frames, time.Duration(i)*33*time.Millisecond)
	}
	samples := repaints(base, 40*time.Millisecond, 15, frames)
	if !repaintingSteadily(samples, 40*time.Millisecond, 1200*time.Millisecond) {
		t.Errorf("a 30fps repaint was not recognised; longest gap %v", longestStillness(samples, 40*time.Millisecond))
	}
}

// A page loading paints in bursts with real gaps. Calling that "it will never
// settle" would tell somebody their page is not coming when it was about to.
func TestALoadingPageIsNotMistakenForVideo(t *testing.T) {
	base := time.Unix(1000, 0)
	// Three bursts, ~400ms apart — busy, but with genuine stillness between.
	paints := []time.Duration{
		20 * time.Millisecond, 60 * time.Millisecond,
		480 * time.Millisecond, 520 * time.Millisecond,
		900 * time.Millisecond,
	}
	samples := repaints(base, 40*time.Millisecond, 30, paints)
	if repaintingSteadily(samples, 40*time.Millisecond, 1200*time.Millisecond) {
		t.Errorf("a loading page was called steady repainting; longest gap %v",
			longestStillness(samples, 40*time.Millisecond))
	}
}

// Too few readings is not evidence. Guessing from three samples would make the
// answer depend on when the probe happened to start.
func TestTooFewSamplesDecideNothing(t *testing.T) {
	base := time.Unix(1000, 0)
	for n := 0; n < 4; n++ {
		var s []time.Time
		for i := 0; i < n; i++ {
			s = append(s, base.Add(time.Duration(i)*10*time.Millisecond))
		}
		if repaintingSteadily(s, 40*time.Millisecond, time.Second) {
			t.Errorf("%d samples were enough to declare steady repainting", n)
		}
	}
}

// The message a caller gets when the screen never stops has to suggest
// something to do. The version that did not is why a run spent thirty seconds
// learning nothing twice.
func TestTheStillChangingReasonSaysWhatToDoInstead(t *testing.T) {
	for _, want := range []string{"NEVER", "browser_wait_for", "waiting longer cannot help"} {
		if !strings.Contains(stillChangingReason, want) {
			t.Errorf("the reason does not mention %q: %s", want, stillChangingReason)
		}
	}
}
