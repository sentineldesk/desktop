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

// Waiting on purpose.
//
// # The gap this fills
//
// An agent that starts a screen recording and wants three minutes of it had no
// way to say so. What it did instead was reach for something with a timeout —
// wait_for_idle, browser_wait_for — and let that expire, which is waiting by
// accident: the tool reports failure, the model has to know that the failure was
// the point, and the row on screen says a thing timed out rather than a thing
// was awaited. The user asked for this directly, and the case they gave is the
// one that shows why it matters: start a recording, wait, stop the recording.
//
// # Why it is a JOB and not a blocking call
//
// Because a call that holds the MCP socket open for three minutes is
// indistinguishable from a hang, and this project has already shipped one thing
// that looked like a hang and was not. The reasoning is the top-level CLAUDE.md's
// own, about run_command: a wait bounds how long the CALL waits, not how long
// the work is allowed to take, and a wait that expires hands back an id rather
// than killing anything.
//
// So sleeping is a job like any other. It runs in a tmux window on the shared
// screen, it is listed by job_list, it is waited on with job_wait, it is stopped
// by job_abort — and, the part that matters most, Room.Abort takes it down with
// everything else, because AbortAll walks the same job records. A sleep that
// survived the panic button would be a hole in the panic button.
//
// # Why it counts down out loud
//
// The pane prints the seconds remaining, once a second. A person watching the
// desktop sees `sleeping — 172s left` rather than a window that appears to have
// stopped, which is the whole difference between an agent that is waiting and an
// agent that has died. It is also what makes the wait witnessed rather than
// merely confined: the screen says what is happening and for how long.

import (
	"context"
	"fmt"
	"time"
)

// maxSleep is the longest wait this will start.
//
// Two hours, and a request for more is REFUSED rather than quietly shortened. A
// clamp would mean an agent asking for six hours, being given two, and reporting
// that it had waited six — which is a lie the model would then act on. Refusing
// names the ceiling, so the next call can be a correct one.
const maxSleep = 2 * time.Hour

// sleepCallWait is how long the sleep CALL itself waits before handing back a
// job id.
//
// Short on purpose. A sleep long enough to be worth asking for is longer than
// any call should stay open, so the common case is: the call returns an id
// almost at once, the model calls job_wait, and the desktop is answering
// throughout. Nothing is blocked and nothing looks stuck.
const sleepCallWait = 5 * time.Second

// sleepDuration reads the requested wait out of the arguments.
//
// seconds and minutes are separate parameters rather than one number and a unit,
// because a unit is a second thing to get wrong: `{"duration": 3, "unit":
// "minutes"}` fails silently when the unit is dropped, and `{"minutes": 3}`
// cannot. Both may be given and they add, so "a minute and a half" is
// expressible without decimals.
func sleepDuration(args map[string]any) (time.Duration, error) {
	secs := argInt(args, "seconds")
	mins := argInt(args, "minutes")
	if secs < 0 || mins < 0 {
		return 0, fmt.Errorf("a negative wait is not a wait")
	}
	total := time.Duration(secs)*time.Second + time.Duration(mins)*time.Minute
	if total <= 0 {
		return 0, fmt.Errorf("say how long: `seconds`, `minutes`, or both")
	}
	if total > maxSleep {
		// Named, not clamped. See maxSleep.
		return 0, fmt.Errorf(
			"%s is longer than this will wait in one call — the limit is %s. "+
				"Start a job that does the waiting itself, or sleep more than once",
			total, maxSleep)
	}
	return total, nil
}

// sleepScript is what runs in the pane: a countdown, then nothing.
//
// Written with `sleep 1` in a loop rather than one long `sleep`, for one reason
// that is not cosmetic: a single `sleep 180` shows a pane that has not printed
// anything for three minutes, which reads as dead. The loop prints the remaining
// seconds every second, so the shared screen shows a wait in progress and
// anybody can see how much is left.
//
// It is also why aborting works cleanly: the process is asleep for at most one
// second at a time, so a signal lands almost immediately rather than at the end
// of a three-minute syscall.
func sleepScript(d time.Duration) string {
	total := int(d.Round(time.Second) / time.Second)
	if total < 1 {
		total = 1
	}
	return fmt.Sprintf(
		`left=%d; `+
			`echo "sleeping for %ds — the agent is waiting on purpose"; `+
			`while [ "$left" -gt 0 ]; do `+
			`printf '\rsleeping — %%4ds left ' "$left"; `+
			`sleep 1; `+
			`left=$((left-1)); `+
			`done; `+
			`printf '\rslept %ds%%-20s\n' ''; `+
			`echo "awake"`,
		total, total, total)
}

func (s *Server) buildSleepTools() []toolDef {
	return []toolDef{
		{
			Name:       "sleep",
			Visibility: visVisible,
			Risk:       riskWrite,
			// It injects nothing into X — it opens a window and waits — so it
			// does not need the controls. That is deliberate and worth stating:
			// waiting is the one thing an agent should always be allowed to do,
			// including while somebody else is driving. Requiring control to
			// wait would mean an agent that has politely given the desktop back
			// cannot pause before asking for it again.
			RequiresControl: false,
			Description: "Pause. Wait, deliberately, for a set time — then carry on. Runs as a " +
				"job in a terminal window on the shared screen, counting down out loud so " +
				"everyone can see the agent is waiting and for how long. Use it when the " +
				"point IS the delay: start a screen recording, sleep 3 minutes, stop the " +
				"recording. A deliberate pause, sleep or delay of minutes or seconds is " +
				"exactly this tool. Do NOT use a tool's timeout to wait — that reports a failure " +
				"for something that went exactly to plan. This returns as soon as the " +
				"wait is short, and otherwise hands back a job id for job_wait; either " +
				"way the sleep keeps running and anybody watching can stop it.",
			InputSchema: schema(map[string]any{
				"seconds": pInt("how long to wait, in seconds"),
				"minutes": pInt("how long to wait, in minutes (adds to seconds)"),
				"wait_ms": pIntDef(
					"how long THIS CALL waits before handing back a job id (default 5000)",
					5000),
			}),
		},
	}
}

func (s *Server) dispatchSleep(ctx context.Context, name string,
	args map[string]any) ([]map[string]any, bool, bool) {

	if name != "sleep" {
		return nil, false, false
	}

	d, err := sleepDuration(args)
	if err != nil {
		return textContent("%v", err), true, true
	}

	rec, err := s.startJob(ctx, sleepScript(d), false)
	if err != nil {
		return textContent("could not start the wait: %v", err), true, true
	}

	wait := time.Duration(argInt(args, "wait_ms")) * time.Millisecond
	if wait <= 0 {
		wait = sleepCallWait
	}
	// Never longer than the sleep itself: waiting six seconds for a two-second
	// sleep would hold the call open past the moment it could have answered.
	if wait > d+time.Second {
		wait = d + time.Second
	}

	final, finished := s.waitForJob(ctx, rec.ID, wait)
	if finished {
		// The whole wait happened inside the call, which is the common case for
		// anything under a few seconds.
		if final.Status == jobAborted {
			return jsonContent(map[string]any{
				"slept": false, "job": rec.ID, "aborted_by": final.AbortedBy,
				"reason": "the wait was stopped before it finished",
			}), false, true
		}
		return jsonContent(map[string]any{
			"slept": true, "job": rec.ID, "seconds": int(d / time.Second),
		}), false, true
	}

	// Still going. The id, and what to do with it — a model told only that
	// something is "still running" tends to start it again.
	return jsonContent(map[string]any{
		"slept":   false,
		"job":     rec.ID,
		"seconds": int(d / time.Second),
		"note": fmt.Sprintf(
			"the wait is running on the shared screen; call job_wait with id %s "+
				"and a timeout_ms of at least %d to be woken when it ends. "+
				"It is NOT cancelled by not waiting for it.",
			rec.ID, d.Milliseconds()),
	}), false, true
}
