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

// What a job READS as, which is the half of this that decides everything else.
//
// The runner is a shell script and needs tmux and a real process to prove
// anything about; those tests skip on a bare host. The reader does not. It takes
// a directory of files and answers "what happened here", and every consumer —
// job_status, job_wait, run_command's return value, the panic button's decision
// about what is still running — is downstream of that one answer. A reader that
// calls a killed job "finished" makes the abort button lie in the log, and no
// amount of correctness in the script above it would show up.
//
// These are also the tests that hold the ordering decision in place: aborted
// beats the exit code. It is one branch of a switch and it looks arbitrary until
// the day somebody reorders it for tidiness, at which point stopping a command
// becomes indistinguishable from a command that happened to exit 143.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jobsIn points the package at a scratch directory for the duration of a test.
func jobsIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	previous := jobsRoot
	jobsRoot = dir
	t.Cleanup(func() { jobsRoot = previous })
	return dir
}

// writeJob lays out a job directory by hand, exactly as job-run.sh would.
func writeJob(t *testing.T, id string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(jobsRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAJobWithNoExitCodeIsStillRunning(t *testing.T) {
	jobsIn(t)
	writeJob(t, "j1", map[string]string{
		"cmd": "sleep 300", "out": "starting\n", "pid": "4242",
	})

	rec, err := readJob("j1")
	if err != nil {
		t.Fatalf("readJob: %v", err)
	}
	if rec.Status != jobRunning {
		t.Errorf("status %q, want running — the exit code file is the ONLY thing that "+
			"says a job ended, and this one has none", rec.Status)
	}
	if rec.ExitCode != nil {
		t.Errorf("exit code %v on a job that has not finished", *rec.ExitCode)
	}
	if rec.PID != 4242 {
		t.Errorf("pid %d, want 4242 — without it an abort has nothing to signal", rec.PID)
	}
}

func TestTheExitCodeDecidesDoneOrFailed(t *testing.T) {
	jobsIn(t)
	writeJob(t, "j1", map[string]string{"cmd": "true", "rc": "0\n"})
	writeJob(t, "j2", map[string]string{"cmd": "false", "rc": "1\n"})

	for _, c := range []struct {
		id   string
		want jobStatus
		code int
	}{{"j1", jobDone, 0}, {"j2", jobFailed, 1}} {
		rec, err := readJob(c.id)
		if err != nil {
			t.Fatalf("readJob %s: %v", c.id, err)
		}
		if rec.Status != c.want {
			t.Errorf("%s is %q, want %q", c.id, rec.Status, c.want)
		}
		if rec.ExitCode == nil || *rec.ExitCode != c.code {
			t.Errorf("%s exit code %v, want %d", c.id, rec.ExitCode, c.code)
		}
	}
}

// TestAbortedBeatsTheExitCode is the ordering, as an assertion.
//
// 143 is what a command exits with when it is terminated, and it is also a
// number a command may choose to exit with. The marker file is the only thing
// that can tell those apart, and it is written before the signal so it cannot
// lose the race. If this ever reads "failed", the audit trail can no longer
// answer the question people will actually ask about a stopped agent: did it
// break, or did somebody stop it.
func TestAbortedBeatsTheExitCode(t *testing.T) {
	jobsIn(t)
	writeJob(t, "j1", map[string]string{
		"cmd": "curl -O big.iso", "rc": "143\n", "aborted": "Ana: panic button\n",
	})
	// The nastier case: a command that was aborted and still managed to exit 0.
	writeJob(t, "j2", map[string]string{
		"cmd": "sleep 1", "rc": "0\n", "aborted": "Ana: panic button\n",
	})

	for _, id := range []string{"j1", "j2"} {
		rec, err := readJob(id)
		if err != nil {
			t.Fatalf("readJob %s: %v", id, err)
		}
		if rec.Status != jobAborted {
			t.Errorf("%s is %q, want aborted — a person stopped this, and the exit "+
				"code cannot carry that fact", id, rec.Status)
		}
		if !strings.Contains(rec.AbortedBy, "Ana") {
			t.Errorf("%s lost who stopped it: %q", id, rec.AbortedBy)
		}
	}
}

// TestTheTwoStreamsStayApart. `2>&1` is the easy implementation and it destroys
// the one thing a person reading a failure needs.
func TestTheTwoStreamsStayApart(t *testing.T) {
	jobsIn(t)
	writeJob(t, "j1", map[string]string{
		"cmd": "tar xf broken.tar.gz",
		"out": "x etc/\nx etc/hosts\n",
		"err": "tar: unexpected EOF\n",
		"rc":  "2\n",
	})

	stdout, err := jobOutput("j1", "out", 0)
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	stderr, err := jobOutput("j1", "err", 0)
	if err != nil {
		t.Fatalf("stderr: %v", err)
	}
	if strings.Contains(stdout, "unexpected EOF") {
		t.Error("the error leaked into stdout; the streams are merged somewhere")
	}
	if !strings.Contains(stderr, "unexpected EOF") {
		t.Errorf("stderr does not hold the error: %q", stderr)
	}

	// tail_lines exists because a build log is megabytes and the failure is at
	// the end of it.
	if got, _ := jobOutput("j1", "out", 1); got != "x etc/hosts" {
		t.Errorf("tail_lines 1 gave %q, want the last line only", got)
	}
}

// TestJobIdsAreNotReused. The id is how a person refers to a job out loud and
// how the action log refers to it afterwards. Two jobs answering to j3 makes
// every sentence about j3 ambiguous, including in the record somebody reads
// after an incident.
func TestJobIdsAreNotReused(t *testing.T) {
	jobsIn(t)
	s := &Server{}

	if got := s.nextJobID(); got != "j1" {
		t.Errorf("first id %q, want j1", got)
	}
	writeJob(t, "j1", map[string]string{"cmd": "true", "rc": "0\n"})
	writeJob(t, "j2", map[string]string{"cmd": "true", "rc": "0\n"})
	if got := s.nextJobID(); got != "j3" {
		t.Errorf("next id %q, want j3", got)
	}

	// The finished ones are still there, so the ids they used are still taken.
	// A counter kept in memory would hand out j1 again after a restart.
	writeJob(t, "j9", map[string]string{"cmd": "true", "rc": "0\n"})
	if got := s.nextJobID(); got != "j10" {
		t.Errorf("after a gap the next id is %q, want j10", got)
	}
}

func TestListJobsIsNewestFirst(t *testing.T) {
	jobsIn(t)
	for _, id := range []string{"j1", "j2", "j10"} {
		writeJob(t, id, map[string]string{"cmd": "echo " + id, "rc": "0\n"})
	}
	got := listJobs()
	if len(got) != 3 {
		t.Fatalf("got %d jobs, want 3", len(got))
	}
	// Numerically, not lexically: j10 sorts before j2 as a string, and the most
	// recent job is the one somebody is asking about.
	if got[0].ID != "j10" || got[1].ID != "j2" || got[2].ID != "j1" {
		t.Errorf("order %s, %s, %s — want j10, j2, j1",
			got[0].ID, got[1].ID, got[2].ID)
	}
}

// TestTheAbortNoteIsDeliveredOnceAndTellsTheAgentToStop.
//
// One-shot because it is an interruption, not a state. If it stuck, every
// subsequent call would be refused and the agent could not do the one thing the
// note asks of it — go and read what happened.
func TestTheAbortNoteIsDeliveredOnceAndTellsTheAgentToStop(t *testing.T) {
	s := &Server{}
	if got := s.takeAbortNote(); got != "" {
		t.Errorf("a fresh server has a pending abort: %q", got)
	}

	s.jobs.abortNote = buildAbortNote("Ana", []string{"j4 (rm -rf /srv/data)"})
	first := s.takeAbortNote()
	if first == "" {
		t.Fatal("the note was not delivered at all")
	}
	if second := s.takeAbortNote(); second != "" {
		t.Errorf("the note was delivered twice; it must clear after one: %q", second)
	}

	// The wording carries the work, so the wording is what is asserted. A bare
	// "aborted" reads to a model as a transient failure, and the natural
	// response to a transient failure is to try again — which is the single
	// worst thing to do after a human has stopped you.
	for _, want := range []string{"Ana", "j4", "Do NOT retry", "wait to be told"} {
		if !strings.Contains(first, want) {
			t.Errorf("the abort note does not mention %q:\n%s", want, first)
		}
	}
}

// TestShQuoteSurvivesAQuote. Only as_root sends the command through a second
// shell, and a naive wrapper there turns an apostrophe in a filename into a
// syntax error at best.
func TestShQuoteSurvivesAQuote(t *testing.T) {
	for _, in := range []string{
		"echo hello",
		`echo "it's fine"`,
		`rm -f '/tmp/a b/c'`,
	} {
		quoted := shQuote(in)
		if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
			t.Errorf("%q was not wrapped: %s", in, quoted)
		}
		// Every embedded quote must be closed and reopened, never left bare.
		inner := quoted[1 : len(quoted)-1]
		if strings.Contains(strings.ReplaceAll(inner, `'\''`, ""), "'") {
			t.Errorf("%q leaves a bare quote inside: %s", in, quoted)
		}
	}
}

func TestAMissingJobIsAnErrorRatherThanAnEmptyRecord(t *testing.T) {
	jobsIn(t)
	if _, err := readJob("j99"); err == nil {
		t.Error("readJob invented a job that does not exist — an empty record here " +
			"reads as a job that ran and printed nothing")
	}
	if _, err := jobOutput("j99", "out", 0); err == nil {
		t.Error("jobOutput returned no error for a job with no directory")
	}
}
