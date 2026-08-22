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

// The history's reader, which is the part everything else trusts.
//
// The three writers are elsewhere — a bash function, a Go recorder, the action
// log — and they agree with this file by convention rather than by type, which
// is exactly the arrangement that rots quietly. A shell line misparsed as an
// input event does not crash anything; it produces a timeline that reads
// plausibly and says the wrong thing, and somebody investigating an incident
// believes it.
//
// Retention gets the same attention for the opposite reason: it DELETES. A
// cutoff that is off by a sign throws away the recent half and keeps the
// ancient one, and nothing would notice until the day the log was needed.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logsIn points both files at a scratch directory.
func logsIn(t *testing.T) (shell, input string) {
	t.Helper()
	dir := t.TempDir()
	shell, input = filepath.Join(dir, "shell.log"), filepath.Join(dir, "input.log")
	return shell, input
}

func stamp(ago time.Duration) string {
	return time.Now().UTC().Add(-ago).Format(time.RFC3339)
}

func TestAShellLineKeepsItsCommandAndExitCode(t *testing.T) {
	shell, _ := logsIn(t)
	body := stamp(time.Minute) + "\tfederico\t%3\t0\tdocker compose up -d\n" +
		stamp(30*time.Second) + "\tfederico\t%3\t127\tdokcer ps\n"
	if err := os.WriteFile(shell, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readLog(shell, "terminal")
	if err != nil {
		t.Fatalf("readLog: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Detail != "docker compose up -d" {
		t.Errorf("command came back as %q", got[0].Detail)
	}
	if got[0].Actor != "federico" {
		t.Errorf("actor is %q — a record that cannot say who did something is "+
			"half a record", got[0].Actor)
	}
	// The failure is the interesting one. An agent asked "what went wrong" needs
	// to see the 127, and a reader that dropped it would leave the typo looking
	// like a command that worked.
	if got[1].Exit == nil || *got[1].Exit != 127 {
		t.Errorf("exit code came back as %v, want 127", got[1].Exit)
	}
	if got[1].OK == nil || *got[1].OK {
		t.Error("a command that exited 127 is reported as having succeeded")
	}
}

// TestACommandWithATabInItIsNotMisreadAsAnInputEvent is why readLog is told
// which file it is reading instead of counting fields.
func TestACommandWithATabInItIsNotMisreadAsAnInputEvent(t *testing.T) {
	shell, _ := logsIn(t)
	// Four fields before the command, and the command itself contains one.
	cmd := "awk -F'\\t' '{print $2}' file"
	body := stamp(time.Minute) + "\tfederico\t%1\t0\t" + cmd + "\n"
	if err := os.WriteFile(shell, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readLog(shell, "terminal")
	if err != nil {
		t.Fatalf("readLog: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Detail != cmd {
		t.Errorf("the command was truncated at its tab: %q", got[0].Detail)
	}
}

func TestAPersonEventCarriesWhatAndWhere(t *testing.T) {
	_, input := logsIn(t)
	body := stamp(time.Minute) + "\tViewer 1\tclicked\tat 415,301\n" +
		stamp(50*time.Second) + "\tViewer 1\ttyped\t14 keys over 3s\n" +
		stamp(40*time.Second) + "\tViewer 1\tpressed abort\tstopped the agent\n"
	if err := os.WriteFile(input, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readLog(input, "person")
	if err != nil {
		t.Fatalf("readLog: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if got[0].What != "clicked" || got[0].Detail != "at 415,301" {
		t.Errorf("click read as %q / %q", got[0].What, got[0].Detail)
	}
	if got[2].What != "pressed abort" {
		t.Errorf("the panic button read as %q — it is the one entry somebody "+
			"will search this file for by name", got[2].What)
	}
	for _, e := range got {
		if e.Source != "person" {
			t.Errorf("%q came back with source %q", e.What, e.Source)
		}
	}
}

// TestRetentionKeepsTheRECENTHalf. The direction of the cutoff, asserted,
// because getting it backwards deletes exactly what somebody needed and leaves
// a file that still looks full.
func TestRetentionKeepsTheRecentHalf(t *testing.T) {
	shell, _ := logsIn(t)
	t.Setenv("ACTIVITY_RETENTION_HOURS", "1")

	body := stamp(48*time.Hour) + "\tfederico\t%1\t0\tancient\n" +
		stamp(3*time.Hour) + "\tfederico\t%1\t0\told\n" +
		stamp(10*time.Minute) + "\tfederico\t%1\t0\trecent\n" +
		stamp(time.Minute) + "\tfederico\t%1\t0\tjust now\n"
	if err := os.WriteFile(shell, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	enforceRetention(shell)

	got, err := readLog(shell, "terminal")
	if err != nil {
		t.Fatalf("readLog: %v", err)
	}
	var kept []string
	for _, e := range got {
		kept = append(kept, e.Detail)
	}
	if len(kept) != 2 || kept[0] != "recent" || kept[1] != "just now" {
		t.Fatalf("kept %v, want [recent, just now] — anything else means the "+
			"cutoff points the wrong way", kept)
	}
}

func TestRetentionOfZeroKeepsEverything(t *testing.T) {
	shell, _ := logsIn(t)
	t.Setenv("ACTIVITY_RETENTION_HOURS", "0")

	body := stamp(400*time.Hour) + "\tfederico\t%1\t0\tvery old\n" +
		stamp(time.Minute) + "\tfederico\t%1\t0\tnew\n"
	if err := os.WriteFile(shell, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	enforceRetention(shell)

	got, _ := readLog(shell, "terminal")
	if len(got) != 2 {
		t.Errorf("kept %d entries with retention disabled, want 2 — 0 means keep, "+
			"not discard", len(got))
	}
}

// TestSizeIsABackstopWhenAgeIsNot. A day busy enough to fill a disk inside the
// retention window is the case age alone cannot answer.
func TestSizeIsABackstopWhenAgeIsNot(t *testing.T) {
	shell, _ := logsIn(t)
	t.Setenv("ACTIVITY_RETENTION_HOURS", "0")

	var b strings.Builder
	line := strings.Repeat("x", 500)
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "%s\tfederico\t%%1\t0\t%s\n", stamp(time.Minute), line)
	}
	if err := os.WriteFile(shell, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(shell); st.Size() <= activityMaxBytes {
		t.Fatalf("the fixture is only %d bytes; it has to exceed the cap to test it",
			st.Size())
	}

	enforceRetention(shell)

	st, err := os.Stat(shell)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > activityMaxBytes {
		t.Errorf("still %d bytes after trimming, cap is %d", st.Size(), activityMaxBytes)
	}
	if st.Size() == 0 {
		t.Error("the file was emptied; forgetting the oldest half is the point, " +
			"and a history that vanishes when it gets long is worse than none")
	}
}

// TestAMissingHistoryIsEmptyRatherThanBroken. Nobody has typed yet is an
// ordinary state, and reporting it as a failure would send a caller looking for
// a fault that is not there.
func TestAMissingHistoryIsEmptyRatherThanBroken(t *testing.T) {
	shell, _ := logsIn(t)
	got, err := readLog(shell, "terminal")
	if err != nil {
		t.Errorf("a history nobody has written to reported an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries from a file that does not exist", len(got))
	}
}
