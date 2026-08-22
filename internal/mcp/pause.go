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

// Pause: hold on, I want to look.
//
// # Not a gentler abort
//
// The panic button beside this one is for "what you are doing is wrong". It
// kills the running work, takes the controls, and interrupts the agent hard
// enough that it has to re-orient — which is right when the plan was wrong, and
// enormously expensive when it was not. Watching an agent do something correct
// and wanting to READ the output before it moves on is a completely different
// intention, and abort answers it by destroying the thing you wanted to read.
//
// So pause is a different axis, not a weaker setting on the same one:
//
//	                 abort                    pause
//	running work     killed                   suspended, resumes where it was
//	new actions      blocked by taking the    blocked by refusing anything
//	                 controls                 that changes something
//	the agent's plan discarded                kept
//	reversible       no                       yes
//
// # What a paused agent may still do
//
// Read. Everything. That is deliberate and it is the same reasoning that kept
// abort from using HaltConnection: the reason somebody paused is usually that
// they want the agent to LOOK at something, and an agent that cannot call
// screenshot or activity while paused is not paused, it is deaf.
//
// So the gate is on risk, not on the connection. riskRead passes; anything that
// writes or is dangerous is refused, with a message that says who paused it and
// that waiting is the correct response — because the natural reading of a
// refusal is "try something else", and something else is exactly what a paused
// agent must not do.
//
// # A pause nobody lifts is a hang
//
// Somebody pauses, closes the tab, goes to lunch. Without a limit the agent
// waits forever and the desktop looks broken rather than paused. It lifts itself
// after pauseExpiry, and the expiry is generous rather than short: the cost of
// resuming too early is the agent doing the thing you were trying to look at.
package mcp

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// pauseExpiry is how long a pause survives with nobody lifting it.
//
// Fifteen minutes: long enough to read output, take a call, or walk over to
// somebody's desk; short enough that a forgotten pause does not look like a
// dead desktop for the rest of the afternoon. It is a backstop rather than a
// deadline — the ordinary end of a pause is somebody pressing resume.
const pauseExpiry = 15 * time.Minute

type pauseState struct {
	mu sync.RWMutex

	by    string
	since time.Time

	// suspended holds the ids of jobs this pause stopped, so resuming wakes
	// exactly those. Not "every job that is stopped": a job somebody suspended
	// by hand from a shell is theirs, and continuing it because a pause ended
	// would be this program overruling a person on their own machine.
	suspended []string
}

// paused reports whether a pause is in force, and by whom.
func (s *Server) paused() (string, bool) {
	s.pause.mu.RLock()
	by, since := s.pause.by, s.pause.since
	s.pause.mu.RUnlock()

	if by == "" {
		return "", false
	}
	if time.Since(since) > pauseExpiry {
		// Expired. Lifted here, on the read, rather than by a timer: the only
		// thing that cares is the next tool call, and a goroutine per pause
		// would be machinery for a question nobody is asking in between.
		s.ResumeAll("the pause expired")
		return "", false
	}
	return by, true
}

// PauseAll suspends the running work and stops the agent changing anything.
//
// Called from the room, like the panic button. Returns how many jobs were
// actually suspended, which is what the log line and the banner need — "paused"
// with nothing running and "paused mid-download" are different situations for
// whoever pressed it.
func (s *Server) PauseAll(who string) int {
	if who == "" {
		who = "a person watching"
	}

	var stopped []string
	for _, rec := range listJobs() {
		if rec.Status != jobRunning || rec.PID <= 0 {
			continue
		}
		// SIGSTOP, not SIGTERM. The process keeps its memory, its open files and
		// its position in whatever it was doing, and SIGCONT puts it back
		// exactly there — which is the entire difference from abort.
		//
		// The honest caveat, worth stating rather than discovering: it keeps its
		// network connections too, and the machine at the OTHER end does not
		// know it has been paused. A download suspended for ten minutes can fail
		// on resume because the server dropped it. Pause preserves this side's
		// state; it cannot preserve anybody else's.
		if _, err := s.output("kill", "-STOP", strconv.Itoa(rec.PID)); err == nil {
			stopped = append(stopped, rec.ID)
		}
	}

	s.pause.mu.Lock()
	s.pause.by = who
	s.pause.since = time.Now()
	s.pause.suspended = stopped
	s.pause.mu.Unlock()

	return len(stopped)
}

// ResumeAll lifts the pause and wakes what it suspended.
func (s *Server) ResumeAll(who string) int {
	s.pause.mu.Lock()
	suspended := s.pause.suspended
	wasPaused := s.pause.by != ""
	s.pause.by = ""
	s.pause.suspended = nil
	s.pause.mu.Unlock()

	if !wasPaused {
		return 0
	}
	woken := 0
	for _, id := range suspended {
		rec, err := readJob(id)
		if err != nil || rec.PID <= 0 {
			continue
		}
		if _, err := s.output("kill", "-CONT", strconv.Itoa(rec.PID)); err == nil {
			woken++
		}
	}
	return woken
}

// pauseRefusal is what the agent reads when it tries to change something.
//
// Written to make waiting the obvious next move, because the natural reading of
// any refusal is "try a different way" — and a paused agent finding a different
// way is precisely the failure this is meant to prevent. It also says what it
// MAY still do, so the pause does not read as a dead end.
func pauseRefusal(by string, tool string) string {
	return fmt.Sprintf(
		"PAUSED by %s. %s changes something, so it is being held.\n\n"+
			"This is not a fault and not a policy you can work around: somebody "+
			"pressed pause because they want to look at something before you go "+
			"on. Do not look for another way to do this.\n\n"+
			"You can still READ everything — screenshot, activity, job_output, the "+
			"window list — and that is usually why the pause happened. Whatever "+
			"you were running is suspended and will carry on from where it was, "+
			"so there is nothing to restart.\n\n"+
			"Wait. If you have been waiting a while and nobody has said anything, "+
			"ask with ask_human.", by, tool)
}
