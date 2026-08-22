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

// A leak is not a bug you notice.
//
// Everything else in this repository fails loudly enough to be found: a command
// does not run, a window does not open, an exit code is wrong. This fails by
// working perfectly while a password travels to somebody else's API, and the
// first evidence arrives from outside. So these tests assert the negative — that
// a value is NOT in a place — which is the only kind of assertion that catches
// it, and they assert it at every exit rather than at the one that was in mind
// when the code was written.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vaultWith(t *testing.T, pairs map[string]string) *vault {
	t.Helper()
	v := &vault{values: map[string]string{}, asked: map[string]string{}}
	for k, val := range pairs {
		v.values[k] = val
	}
	return v
}

// TestTheValueNeverEntersTheCommandText is the one that decides whether any of
// this is worth having.
//
// Interpolating the secret into the command would pass every other test in this
// file — the agent still would not have seen it — while printing it in a tmux
// pane on a screen other people are watching, into the job's cmd file, and into
// the shell history. That is not a smaller leak than telling the model; it is a
// bigger one with better manners.
func TestTheValueNeverEntersTheCommandText(t *testing.T) {
	s := &Server{vault: vaultWith(t, map[string]string{"db_root": "hunter2-correct-horse"})}

	command := `mysql -u root -p{{secret:db_root}} -e 'show databases'`
	rewritten, env, missing := s.resolveSecrets(command)

	if len(missing) != 0 {
		t.Fatalf("a registered secret came back missing: %v", missing)
	}
	if strings.Contains(rewritten, "hunter2-correct-horse") {
		t.Fatalf("THE PASSWORD IS IN THE COMMAND TEXT, which is shown on the shared "+
			"screen and written to disk:\n  %s", rewritten)
	}
	if !strings.Contains(rewritten, `"$SD_SECRET_DB_ROOT"`) {
		t.Errorf("the reference was not turned into a shell variable: %s", rewritten)
	}
	// Quoted, or a password containing a space or a glob becomes several
	// arguments and the failure looks like bad credentials rather than bad
	// quoting.
	if !strings.Contains(rewritten, `-p"$SD_SECRET_DB_ROOT"`) {
		t.Errorf("the variable is unquoted: %s", rewritten)
	}
	if len(env) != 1 || env[0] != "SD_SECRET_DB_ROOT=hunter2-correct-horse" {
		t.Errorf("the environment does not carry the value: %v", env)
	}
}

// TestRedactionReplacesWithTheReferenceNotAsterisks.
//
// `password=*****` tells an agent something was hidden and leaves it stuck.
// `password={{secret:db_root}}` tells it the same thing and hands it the token
// to use next. Redaction that keeps the capability is redaction people leave on.
func TestRedactionReplacesWithTheReferenceNotAsterisks(t *testing.T) {
	v := vaultWith(t, map[string]string{"db_root": "hunter2-correct-horse"})

	got := v.Redact("db.password=hunter2-correct-horse\ndb.host=localhost")
	if strings.Contains(got, "hunter2-correct-horse") {
		t.Fatalf("the secret survived redaction:\n%s", got)
	}
	if !strings.Contains(got, "{{secret:db_root}}") {
		t.Errorf("redacted to something the agent cannot act on:\n%s", got)
	}
	if !strings.Contains(got, "db.host=localhost") {
		t.Errorf("redaction ate content that was not secret:\n%s", got)
	}
}

// TestRedactionCoversEveryTextBlock. A tool that returns several blocks —
// stdout in one, stderr in another — must not have one of them cleaned.
func TestRedactionCoversEveryTextBlock(t *testing.T) {
	v := vaultWith(t, map[string]string{"api": "sk-live-9f2b7c4e1a"})
	content := []map[string]any{
		{"type": "text", "text": "connecting"},
		{"type": "text", "text": "curl -H 'Authorization: sk-live-9f2b7c4e1a'"},
		{"type": "image", "data": "sk-live-9f2b7c4e1a"},
	}
	v.redactContent(content)

	if strings.Contains(content[1]["text"].(string), "sk-live-9f2b7c4e1a") {
		t.Error("a later text block was not redacted; only the first is not enough")
	}
	// Stated rather than silently true: an image is bytes and cannot be scanned
	// this way. The mitigation is that a secret used by reference never reaches
	// the screen, so there is nothing in the picture to find.
	if content[2]["data"] != "sk-live-9f2b7c4e1a" {
		t.Error("image data was rewritten; redaction must not corrupt binary fields")
	}
}

// TestLongestFirst. A vault holding both a token and a prefix of it must not
// replace the prefix and leave the rest of the token sitting beside a reference.
func TestLongestFirst(t *testing.T) {
	v := vaultWith(t, map[string]string{
		"short": "abc123456",
		"long":  "abc123456789xyz",
	})
	got := v.Redact("token=abc123456789xyz")
	if strings.Contains(got, "789xyz") {
		t.Errorf("the longer secret was cut in half, leaving part of it in the "+
			"output: %s", got)
	}
}

// TestAShortSecretIsNotRedacted, and why that is the right call.
//
// A six-character minimum means a very short password is not protected, which
// sounds like a hole and is a trade. Below it, the value appears inside ordinary
// words and paths, every output turns to confetti, and somebody switches the
// feature off — which protects nothing at all. The honest position is that a
// password that short is not one.
func TestAShortSecretIsNotRedacted(t *testing.T) {
	v := vaultWith(t, map[string]string{"pin": "1234"})
	if got := v.Redact("the file is at /var/1234/log"); !strings.Contains(got, "1234") {
		t.Errorf("a four-character value was redacted out of an unrelated path: %s", got)
	}
}

// TestAWorldReadableVaultIsRefused. Loading it would record a protection that
// is not there: every user and every process in the container already has the
// contents.
func TestAWorldReadableVaultIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets")
	if err := os.WriteFile(path, []byte("db_root=hunter2-correct-horse\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	v := &vault{values: map[string]string{}, asked: map[string]string{}}
	v.loadFile(path)
	if len(v.values) != 0 {
		t.Error("a 0644 secrets file was loaded; anything another user can read " +
			"is not a secret, and loading it claims a guarantee that does not exist")
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	v.loadFile(path)
	if got, ok := v.lookup("db_root"); !ok || got != "hunter2-correct-horse" {
		t.Errorf("a 0600 file did not load: %q %v", got, ok)
	}
}

// TestNamesAreOfferedAndValuesAreNot. The names are genuinely useful — an agent
// that knows db_root exists can write the command — and are the only part that
// is safe to hand over.
func TestNamesAreOfferedAndValuesAreNot(t *testing.T) {
	s := &Server{vault: vaultWith(t, map[string]string{
		"db_root": "hunter2-correct-horse",
		"api":     "sk-live-9f2b7c4e1a",
	})}
	content, isErr, handled := s.dispatchSecrets("secret_list", nil)
	if !handled || isErr {
		t.Fatalf("secret_list did not answer: handled=%v isErr=%v", handled, isErr)
	}
	rendered := ""
	for _, block := range content {
		if text, ok := block["text"].(string); ok {
			rendered += text
		}
	}
	for _, value := range []string{"hunter2-correct-horse", "sk-live-9f2b7c4e1a"} {
		if strings.Contains(rendered, value) {
			t.Fatalf("secret_list returned a VALUE:\n%s", rendered)
		}
	}
	for _, name := range []string{"db_root", "api"} {
		if !strings.Contains(rendered, name) {
			t.Errorf("%q is missing from the names: %s", name, rendered)
		}
	}
}

// TestAnUnknownReferenceIsAQuestionNotAFailure. A command naming a secret
// nobody registered is not broken — it is waiting for a person to type
// something, which is the strongest version of this: the agent never holds the
// value even in redacted form.
func TestAnUnknownReferenceIsAQuestionNotAFailure(t *testing.T) {
	s := &Server{vault: vaultWith(t, nil)}
	_, _, missing := s.resolveSecrets("ssh -i key user@host {{secret:bastion_pass}}")
	if len(missing) != 1 || missing[0] != "bastion_pass" {
		t.Fatalf("missing came back as %v, want [bastion_pass]", missing)
	}

	// And once somebody has typed it, the same command resolves with no trace of
	// the value in the text.
	s.vault.remember("bastion_pass", "typed-at-the-desktop")
	rewritten, env, missing := s.resolveSecrets("ssh -i key user@host {{secret:bastion_pass}}")
	if len(missing) != 0 {
		t.Fatalf("still missing after being told: %v", missing)
	}
	if strings.Contains(rewritten, "typed-at-the-desktop") {
		t.Errorf("what the person typed went into the command text: %s", rewritten)
	}
	if len(env) != 1 || !strings.Contains(env[0], "typed-at-the-desktop") {
		t.Errorf("the typed value did not reach the environment: %v", env)
	}
}

// TestWhatAPersonTypedIsNotWrittenToDisk. It was given for a task, not
// deposited for keeping, and storing it would convert one into the other
// without anybody agreeing to it.
func TestWhatAPersonTypedIsNotWrittenToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets")
	if err := os.WriteFile(path, []byte("db_root=hunter2-correct-horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := &vault{values: map[string]string{}, asked: map[string]string{}}
	v.loadFile(path)
	v.remember("typed_one", "only-in-memory")

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "only-in-memory") {
		t.Error("a value somebody typed at a prompt was appended to the vault file")
	}
	// It is still usable for as long as the desktop is up, so a five-step task
	// does not ask five times.
	if got, ok := v.lookup("typed_one"); !ok || got != "only-in-memory" {
		t.Errorf("it was not remembered for this session: %q %v", got, ok)
	}
}

// TestReferencesWithNoSecretsAreLeftAlone. The overwhelming majority of
// commands have no reference in them and must be handed through untouched.
func TestReferencesWithNoSecretsAreLeftAlone(t *testing.T) {
	s := &Server{vault: vaultWith(t, map[string]string{"db_root": "hunter2-correct-horse"})}
	const command = `df -h / && echo "{{ not a reference }}"`
	rewritten, env, missing := s.resolveSecrets(command)
	if rewritten != command {
		t.Errorf("a command with no reference was rewritten:\n  %s\n  %s", command, rewritten)
	}
	if len(env) != 0 || len(missing) != 0 {
		t.Errorf("env=%v missing=%v, want both empty", env, missing)
	}
}
