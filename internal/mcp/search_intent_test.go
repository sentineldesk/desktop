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

// The other half of the search measurement: questions asked from the PROBLEM.
//
// search_test.go writes one query per tool, phrased as a goal rather than as a
// tool name, and that constraint is real work — "open the calculator
// application" for launch_app is not the same as "launch an app". But every
// query in it is still written FROM a tool, one per tool, by somebody holding
// the catalogue. What it therefore measures is: given that this tool is the
// answer, is it found? It cannot measure the case where the asker has no idea
// which tool the answer is, because there is no such query in it.
//
// That case is not hypothetical. "how much free disk space is left" returned
// ZERO tools, measured against the running catalogue, and so did "what is the
// CPU temperature". Both have the same answer — run a command — and the search
// had no way to reach it, because the vocabulary described what run_command IS
// rather than what it is FOR. A hundred percent recall@10 sat beside a question
// a person asks on their first day and got nothing.
//
// A caveat worth writing down rather than discovering later: the first two
// queries below ARE the bug report, and the vocabulary was widened in the same
// change. For those two this test locks in a fix rather than proving a design,
// and it should be read that way. The rest were written afterwards, from
// scratch, and were not tuned for.

import (
	"sort"
	"strings"
	"testing"
)

// intentCorpus maps a question somebody would actually ask to the tool that
// answers it. Unlike searchCorpus this is deliberately NOT one per tool: it is
// a list of hard cases, and it grows when a real question comes up empty.
var intentCorpus = []struct {
	query string
	want  string
}{
	// The two that were measured at zero.
	{"how much free disk space is left", "run_command"},
	{"what is the CPU temperature", "run_command"},

	// Written afterwards, from the question rather than from the catalogue.
	{"is this machine running out of memory", "run_command"},
	{"everything feels slow, what is eating the cpu", "list_processes"},
	{"the application stopped responding", "kill_process"},
	{"what is going on right now on the desktop", "desktop_state"},
	{"am I allowed to move the mouse", "room_state"},
	{"I want to drive the desktop", "request_control"},
	{"something failed and I need to read what it said", "terminal_read"},
	{"is there an error box on the screen", "check_errors"},
	{"I need to see the desktop as a picture", "screenshot"},
	{"put this text where I can paste it", "set_clipboard"},
	{"wait until the thing I started has settled", "wait_for_idle"},
	{"ask the people watching which one they meant", "ask_human"},

	// Asked in Spanish. Not a translation exercise: the searcher has no idea
	// which language it is being asked in, and a query that matches NOTHING
	// falls back to offering all 121 schemas — roughly five times the input
	// tokens of a run that matched. "cerrá esa ventana" returned zero against
	// the English vocabulary while "close that window" returned fifteen, so a
	// person asking in their own language paid five times over for nothing they
	// did wrong.
	//
	// These are here so that stops being true silently. A language file that
	// rots takes the price back with it and nothing else would notice.
	{"cerrá esa ventana", "close_window"},
	{"abrí una terminal", "terminal_open"},
	{"sacá una captura de pantalla", "screenshot"},
	{"cuánto espacio libre queda en el disco", "run_command"},
	{"quién más está conectado", "room_state"},
	{"qué está usando el procesador", "list_processes"},
	{"subí el volumen", "set_volume"},
	{"grabá un video de la pantalla", "start_recording"},
}

// TestIntentRecall keeps every one of those questions reachable.
//
// The bar is recall@10 for all of them, because ten is what tool_search returns
// and a tool that is not in it does not exist as far as the agent is concerned.
// The rank distribution is reported rather than asserted: pushing these to rank
// one means tuning the searcher against fourteen sentences written in this file,
// which measures how well it was tuned and nothing else.
func TestIntentRecall(t *testing.T) {
	tools := (&Server{}).buildTools()

	var unreachable []string
	top3 := 0
	ranks := map[string]int{}

	for _, c := range intentCorpus {
		rank := rankOf(searchTools(tools, c.query, 10), c.want)
		ranks[c.query] = rank
		switch {
		case rank == 0:
			unreachable = append(unreachable, c.query+" → "+c.want)
		case rank <= 3:
			top3++
		}
	}

	queries := make([]string, 0, len(intentCorpus))
	for _, c := range intentCorpus {
		queries = append(queries, c.query)
	}
	sort.Strings(queries)
	for _, q := range queries {
		if r := ranks[q]; r == 0 {
			t.Logf("  NOT FOUND  %s", q)
		} else {
			t.Logf("  rank %-2d    %s", r, q)
		}
	}
	t.Logf("intent recall@10 %d/%d, of which top-3: %d",
		len(intentCorpus)-len(unreachable), len(intentCorpus), top3)

	if len(unreachable) > 0 {
		t.Errorf("%d question(s) reach no tool at all — an agent asked this way "+
			"would go and do the job the hard way, or report that it cannot:\n  %s",
			len(unreachable), strings.Join(unreachable, "\n  "))
	}
}

// TestIntentCorpusNamesRealTools stops this file rotting into a list of
// assertions about tools that were renamed three refactors ago, which is the
// failure mode of every hand-written corpus that nothing checks.
func TestIntentCorpusNamesRealTools(t *testing.T) {
	catalogue := map[string]bool{}
	for _, tool := range (&Server{}).buildTools() {
		catalogue[tool.Name] = true
	}
	for _, c := range intentCorpus {
		if !catalogue[c.want] {
			t.Errorf("intentCorpus expects %q for %q, and no such tool exists",
				c.want, c.query)
		}
	}
}
