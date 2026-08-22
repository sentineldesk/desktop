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

// The link has to be on EVERY reading tool, not on the one that starts the work.
//
// That is the failure this file exists to catch, and it is a quiet one: a link
// that appears only on job_start is mentioned once, at the moment there is
// nothing to read yet, and never again at the moment somebody asks "where can I
// see that?". The result reads fine either way — which is exactly the shape of
// bug this repository keeps finding, an action that returns ok and produces
// nothing anybody can use.
//
// So these tests read the tool results the way the model does: parse the JSON,
// look for the field, check it is a URL that points at the route the daemon
// actually serves.

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/sentineldesk/desktop/pkg/config"
)

// resultOf runs a job dispatcher and decodes the JSON it returned.
func resultOf(t *testing.T, s *Server, tool string, args map[string]any) map[string]any {
	t.Helper()
	content, isErr, handled := s.dispatchJobs(t.Context(), tool, args)
	if !handled {
		t.Fatalf("%s was not handled by dispatchJobs", tool)
	}
	if isErr {
		t.Fatalf("%s returned an error: %v", tool, content)
	}
	if len(content) == 0 {
		t.Fatalf("%s returned nothing", tool)
	}
	text, _ := content[0]["text"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s did not return JSON, so it has nowhere to put the link: %s", tool, text)
	}
	return out
}

func TestEveryJobResultCarriesTheLink(t *testing.T) {
	jobsIn(t)
	writeJob(t, "j4", map[string]string{
		"cmd": "apt-get install nginx", "rc": "0", "out": "done\n", "err": "",
	})
	s := &Server{cfg: config.Config{PublicURL: "https://desk.example.test"}}

	// Every tool a person could be holding when they ask where to look. Not
	// job_start (it needs a tmux window) and not job_abort (it needs a running
	// process) — those two are covered by withLogLinks being the one place any
	// of them builds the field.
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"job_status", map[string]any{"id": "j4"}},
		{"job_output", map[string]any{"id": "j4"}},
		{"job_output", map[string]any{"id": "j4", "stream": "both"}},
		{"job_wait", map[string]any{"id": "j4", "timeout_ms": 100}},
		{"job_list", map[string]any{}},
	}
	for _, c := range cases {
		out := resultOf(t, s, c.tool, c.args)
		if out["all_logs_url"] != "https://desk.example.test/logs" {
			t.Errorf("%s: all_logs_url = %v, want the viewer's page — the people here "+
				"need one link that shows both sides", c.tool, out["all_logs_url"])
		}
		if c.tool == "job_list" {
			continue // a list has one link per job rather than one for itself
		}
		link, _ := out["logs_url"].(string)
		if link != "https://desk.example.test/logs?job=j4" {
			t.Errorf("%s: logs_url = %q, want a link to this job's output", c.tool, link)
		}
	}
}

func TestTheLinkIsWorthNothingOnItsOwn(t *testing.T) {
	jobsIn(t)
	writeJob(t, "j1", map[string]string{"cmd": "id", "rc": "0", "out": "uid=1000\n"})
	// A session secret in the config, because the risk being tested is a future
	// version deciding the link would be friendlier if it just worked.
	s := &Server{cfg: config.Config{
		PublicURL:  "https://desk.example.test",
		AuthUser:   "ana",
		AuthPass:   "correct-horse-battery",
		AuthSecret: "a-session-signing-secret",
	}}

	out := resultOf(t, s, "job_status", map[string]any{"id": "j1"})
	link := out["logs_url"].(string)
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("logs_url is not a URL: %q", link)
	}
	// This string is handed to a model, repeated to a person, and pasted
	// wherever people paste things. Anything in it that grants access is a
	// credential in a chat log.
	if q := parsed.Query(); len(q) != 1 || q.Get("job") != "j1" {
		t.Errorf("logs_url carries %v; a job id is the only thing it may say", q)
	}
	for _, secret := range []string{"correct-horse-battery", "a-session-signing-secret",
		"ticket", "token"} {
		if strings.Contains(strings.ToLower(link), strings.ToLower(secret)) {
			t.Errorf("logs_url contains %q: %s", secret, link)
		}
	}
}

func TestWithoutPublicURLTheLinkIsObviouslyLocalRatherThanWrong(t *testing.T) {
	// The fallback has to be a link somebody can correct at a glance. Guessing a
	// hostname would produce one that looks right and resolves nowhere, which
	// gets tried once and then distrusted — and a link nobody trusts is the same
	// as no link, except that it also cost the reader a minute.
	s := &Server{cfg: config.Config{HTTPPort: 8080}}
	if got := s.allLogsURL(); got != "http://localhost:8080/logs" {
		t.Errorf("fallback = %q, want http://localhost:8080/logs", got)
	}

	// Bound to every interface says nothing about where to point a browser.
	s = &Server{cfg: config.Config{HTTPPort: 9000, HTTPAddr: "0.0.0.0"}}
	if got := s.allLogsURL(); got != "http://localhost:9000/logs" {
		t.Errorf("0.0.0.0 became %q; it is not a destination", got)
	}

	// Serving TLS means the link has to say https, or it fails on the first
	// click with a protocol error nobody reads as a configuration problem.
	s = &Server{cfg: config.Config{HTTPPort: 8443, TLSSelfSigned: true}}
	if got := s.allLogsURL(); !strings.HasPrefix(got, "https://") {
		t.Errorf("with TLS on the link is %q, want https", got)
	}

	// And PUBLIC_URL wins over all of it, trailing slash and all.
	s = &Server{cfg: config.Config{HTTPPort: 8080, PublicURL: "https://desk.example.test/"}}
	if got := s.jobLogURL("j3"); got != "https://desk.example.test/logs?job=j3" {
		t.Errorf("PUBLIC_URL produced %q", got)
	}
}
