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

// Redaction through the REAL path, not the function on its own.
//
// secrets_test.go proves Redact replaces what it is given. That is necessary and
// it is not the question anybody actually has, which is: does a secret survive a
// tool call? Those are different, and the gap between them is a line of wiring
// nobody would notice was missing — every unit test would stay green while every
// result went out untouched.
//
// This project has already paid for that distinction once, in a tool called
// fill_form that returned ok and had never filled anything. So these go over the
// wire: a real Server, a real session, a real tools/call, and the assertion is
// made against the bytes that came back.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// callAndRender makes a tool call and returns everything the client received as
// text, which is the only surface that matters — anywhere the value can appear
// is somewhere it can leave.
func callAndRender(t *testing.T, c *session, name string, args map[string]any) string {
	t.Helper()
	result := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestASecretDoesNotSurviveARealToolCall(t *testing.T) {
	const secret = "Tr0ub4dor-and-3-xkcd-staples"

	dir := t.TempDir()
	path := filepath.Join(dir, "app.conf")
	body := "host=db.internal\ndb_password=" + secret + "\nport=5432\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	s := testServer(t)
	s.vault.values["db_root"] = secret
	c := newSession(t, s)

	rendered := callAndRender(t, c, "read_file", map[string]any{"path": path})

	if strings.Contains(rendered, secret) {
		t.Fatalf("THE SECRET CAME BACK OVER THE WIRE. Redact works in isolation "+
			"and is not reaching the result:\n%s", rendered)
	}
	if !strings.Contains(rendered, "{{secret:db_root}}") {
		t.Errorf("the reference is not in the result, so redaction did not run "+
			"at all:\n%s", rendered)
	}
	// The rest of the file has to survive, or the agent cannot do the work it
	// was reading the file for.
	if !strings.Contains(rendered, "db.internal") {
		t.Errorf("redaction ate content that was not secret:\n%s", rendered)
	}
}

// TestTheActionLogIsRedactedToo. The trail is written to disk, merged into the
// activity timeline and read by people who were not in the room. A secret
// surviving there would have escaped the model only to be filed somewhere more
// permanent.
func TestTheActionLogIsRedactedToo(t *testing.T) {
	const secret = "Tr0ub4dor-and-3-xkcd-staples"

	dir := t.TempDir()
	path := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(path, []byte("db_password="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := testServer(t)
	s.vault.values["db_root"] = secret
	c := newSession(t, s)
	callAndRender(t, c, "read_file", map[string]any{"path": path})

	for _, entry := range s.actions.Tail(0, "") {
		if strings.Contains(entry.Args, secret) || strings.Contains(entry.Result, secret) {
			t.Fatalf("the secret is in the action log: args=%q result=%q",
				entry.Args, entry.Result)
		}
	}
}

// TestAnUnregisteredCredentialIsReportedRatherThanHidden.
//
// The two halves have to be told apart, and this is where they meet. A value in
// the vault is silently replaced, because the agent can still work with the
// reference. A value nobody registered is NOT redacted — doing so would be
// pretending it had not already left — and instead somebody is told to rotate it.
func TestAnUnregisteredCredentialIsReportedRatherThanHidden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds")
	if err := os.WriteFile(path, []byte("AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := testServer(t)
	c := newSession(t, s)
	callAndRender(t, c, "read_file", map[string]any{"path": path})

	var reported bool
	for _, entry := range s.actions.Tail(0, "") {
		if entry.Tool == "credential_exposed" {
			reported = true
			if strings.Contains(entry.Args, "AKIAIOSFODNN7EXAMPLE") {
				t.Errorf("the incident record quotes the credential: %q", entry.Args)
			}
			if !strings.Contains(entry.Args, "AWS access key") {
				t.Errorf("the record does not say WHAT to rotate: %q", entry.Args)
			}
		}
	}
	if !reported {
		t.Error("an unregistered AWS key went out and nothing was recorded — the " +
			"whole value of the detector is that somebody can rotate one credential " +
			"instead of all of them")
	}
}

// TestARedactedSecretIsNotAlsoReported. The two mechanisms must not disagree:
// a value the vault handled did NOT escape, and warning about it would send
// somebody to rotate a credential that is still safe — which trains them to
// ignore the ones that are not.
func TestARedactedSecretIsNotAlsoReported(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"

	dir := t.TempDir()
	path := filepath.Join(dir, "creds")
	if err := os.WriteFile(path, []byte("AWS_ACCESS_KEY_ID="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := testServer(t)
	s.vault.values["aws_prod"] = secret
	c := newSession(t, s)
	callAndRender(t, c, "read_file", map[string]any{"path": path})

	for _, entry := range s.actions.Tail(0, "") {
		if entry.Tool == "credential_exposed" {
			t.Errorf("a secret the vault redacted was ALSO reported as exposed. "+
				"It never reached the model, and telling somebody to rotate it "+
				"spends the attention the real warnings need: %q", entry.Args)
		}
	}
}

// TestOurOwnHelpTextDoesNotLookLikeALeak.
//
// secret_list explains itself with the literal `{{secret:<name>}}`, and that
// string is the word `secret`, a colon, and enough characters after it to be a
// password assignment. So the tool whose entire job is to tell an agent how to
// use a secret safely reported a credential as having leaked.
//
// Nothing had leaked. Somebody would have been sent to rotate a key over a
// sentence in a tool description — which is the false positive that ruins a
// warning, because the next one is read with less attention.
//
// It survived every unit test because they all fed the detector realistic file
// contents, and none of them fed it this program's own output. Found by running
// it in the container.
func TestOurOwnHelpTextDoesNotLookLikeALeak(t *testing.T) {
	s := testServer(t)
	s.vault.values["db_root"] = "Tr0ub4dor-and-3-xkcd-staples"
	c := newSession(t, s)

	callAndRender(t, c, "secret_list", nil)

	for _, entry := range s.actions.Tail(0, "") {
		if entry.Tool == "credential_exposed" {
			t.Fatalf("secret_list reported a credential leak from its own "+
				"description: %q", entry.Args)
		}
	}
}
