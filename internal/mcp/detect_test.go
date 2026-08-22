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

// The two ways a warning stops working, tested from both ends.
//
// It misses the credential, which is the obvious failure and the less dangerous
// one — nothing is worse than before. Or it fires on ordinary output, which
// looks harmless and is not: people learn within a day that the banner means
// nothing, and then the real one is dismissed at a glance. A detector's false
// positive rate IS its effectiveness, because attention is the resource it
// spends.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testWatcher builds the same rule set the daemon does, minus the operator's
// own policy file — which a test must not read, because it would then pass or
// fail depending on the machine it runs on.
func testWatcher(t *testing.T) *credentialWatcher {
	t.Helper()
	w := &credentialWatcher{seen: map[string]time.Time{}}
	w.shapes = append(w.shapes, shapes...)
	w.shapes = append(w.shapes, shapesFromLexicon()...)
	return w
}

func TestTheShapesCatchRealCredentials(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"aws", `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE`, "aws_key"},
		{"github", `token: ghp_16C7e42F292c6912E7710c838347Ae178B4a`, "github_token"},
		{"anthropic", `ANTHROPIC_API_KEY=sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv`, "anthropic_key"},
		{"pem", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNza", "private_key"},
		{"jwt", `Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N`, "jwt"},
		{"url", `postgres://appuser:s3cr3tpassword@db.internal:5432/app`, "url_password"},
		{"config", `db_password = "Tr0ub4dor&3xkcd"`, "config_password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := testWatcher(t).inspect(c.text, "read_file")
			if len(got) == 0 {
				t.Fatalf("nothing detected in %q", c.text)
			}
			for _, f := range got {
				if f.ID == c.want {
					return
				}
			}
			t.Errorf("detected %v, want one of them to be %s", got, c.want)
		})
	}
}

// TestOrdinaryOutputIsQuiet is the half that decides whether anybody keeps
// reading the banner.
func TestOrdinaryOutputIsQuiet(t *testing.T) {
	quiet := []string{
		`total 48\ndrwxr-xr-x 2 sentineldesk sentineldesk 4096 Aug  8 12:00 .`,
		`Filesystem  Size  Used Avail Use% Mounted on\n/dev/sda1    50G   12G   36G  25% /`,
		`# password = changeme`,
		`PASSWORD=xxxxxxxx`,
		`api_key: ${API_KEY}`,
		`export DB_PASSWORD="<your-password-here>"`,
		`password: placeholder`,
		`token = TODO`,
		`Connecting to https://example.com/api/v1/users`,
		`git clone https://github.com/sentineldesk/desktop.git`,
		`error: password authentication failed for user "app"`,
	}
	for _, text := range quiet {
		if got := testWatcher(t).inspect(text, "run_command"); len(got) > 0 {
			t.Errorf("false alarm on ordinary output:\n  %s\n  detected %v", text, got)
		}
	}
}

// TestTheSameCredentialWarnsOnce. A file read three times in one task is one
// problem, and three identical banners is how people learn to click them away.
func TestTheSameCredentialWarnsOnce(t *testing.T) {
	w := testWatcher(t)
	const text = `AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE`

	if got := w.inspect(text, "read_file"); len(got) != 1 {
		t.Fatalf("first sighting reported %d findings, want 1", len(got))
	}
	if got := w.inspect(text, "read_file"); len(got) != 0 {
		t.Errorf("the same credential warned again: %v", got)
	}
	// A DIFFERENT credential still warns — deduplication must be per value, not
	// a global "already warned once about AWS".
	if got := w.inspect(`AWS_ACCESS_KEY_ID=AKIAJ7QK4LMNOPQRSTUV`, "read_file"); len(got) != 1 {
		t.Errorf("a second, different key was suppressed: %v", got)
	}
}

// TestTheWarningNeverCarriesTheValue. The notice lands on a shared screen and
// in recordings; quoting the secret would make the safety feature the leak.
func TestTheWarningNeverCarriesTheValue(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	found := testWatcher(t).inspect(`AWS_ACCESS_KEY_ID=`+secret, "read_file")
	if len(found) == 0 {
		t.Fatal("nothing detected")
	}
	for _, f := range found {
		if strings.Contains(f.Kind+f.ID+f.Fingerprint+f.Where, secret) {
			t.Fatalf("the finding carries the credential: %+v", f)
		}
	}
	for _, key := range noticeKinds(found) {
		if strings.Contains(key, secret) {
			t.Fatalf("the notice carries the credential: %q", key)
		}
	}
}

// TestTheNoticeIsKeysNotProse. Composing the sentence in Go would deliver
// English to somebody using the desktop in Spanish, and a warning that cannot
// be read looks exactly like a system that is working.
func TestTheNoticeIsKeysNotProse(t *testing.T) {
	found := []finding{
		{ID: "aws_key", Kind: "AWS access key"},
		{ID: "private_key", Kind: "private key"},
		{ID: "aws_key", Kind: "AWS access key"},
	}
	got := noticeKinds(found)
	if len(got) != 2 || got[0] != "aws_key" || got[1] != "private_key" {
		t.Fatalf("got %v, want [aws_key private_key] — deduplicated, in order", got)
	}
	for _, key := range got {
		if strings.ContainsAny(key, " .") {
			t.Errorf("%q is a sentence, not a key; the client cannot translate it", key)
		}
	}
}

// TestACustomRuleTravelsByName. An installation's own rule has no translation
// key, so its name is shown as the operator wrote it — which is the only
// sensible thing to do with a string this binary has never seen.
func TestACustomRuleTravelsByName(t *testing.T) {
	found := []finding{{ID: "", Kind: "internal customer id"}}
	got := noticeKinds(found)
	if len(got) != 1 || got[0] != "internal customer id" {
		t.Errorf("got %v, want the raw name to travel", got)
	}
}

func TestAPolicyFileAddsRulesAndSurvivesABadOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential-policy")
	body := "# our own formats\n" +
		"internal service token = \\bSVC-[0-9A-F]{12}\\b\n" +
		"broken rule = [unclosed\n" +
		"\n" +
		"legacy key = \\bLEG_[a-z0-9]{10,}\\b\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := loadPolicyFile(path)
	if len(got) != 2 {
		t.Fatalf("loaded %d rules, want 2 — a regexp that does not compile must be "+
			"skipped, not fatal: one bad line cannot cost somebody a desktop", len(got))
	}
	w := &credentialWatcher{seen: map[string]time.Time{}, shapes: got}
	if found := w.inspect("auth: SVC-A1B2C3D4E5F6", "read_file"); len(found) != 1 {
		t.Errorf("the custom rule did not fire: %v", found)
	}
	// And it carries the operator's own name rather than a key.
	found := w.inspect("key LEG_abcdef1234567", "read_file")
	if len(found) != 1 || found[0].Kind != "legacy key" || found[0].ID != "" {
		t.Errorf("custom rule identity is wrong: %+v", found)
	}
}

// TestEveryShapeCompilesAndIsNamed. A rule with no id cannot be translated and
// a rule with no name cannot be reported, and both fail silently at the moment
// somebody needs them.
func TestEveryShapeCompilesAndIsNamed(t *testing.T) {
	ids := map[string]bool{}
	all := append(append([]secretShape{}, shapes...), shapesFromLexicon()...)
	for _, shape := range all {
		if shape.id == "" {
			t.Errorf("the built-in rule %q has no id, so the browser cannot "+
				"translate it", shape.kind)
		}
		if shape.kind == "" {
			t.Errorf("rule %q has no English name for the log", shape.id)
		}
		if ids[shape.id] {
			t.Errorf("two rules share the id %q", shape.id)
		}
		ids[shape.id] = true
		if _, err := regexp.Compile(shape.re.String()); err != nil {
			t.Errorf("%s: %v", shape.id, err)
		}
	}
}

// --- the vocabulary ---------------------------------------------------------

// TestTheVocabularyLoadsInEveryLanguage. A dictionary that silently loads zero
// terms leaves a detector that compiles, runs, warns about nothing and looks
// exactly like one that found nothing to warn about.
func TestTheVocabularyLoadsInEveryLanguage(t *testing.T) {
	lx := credentialWords()
	if len(lx.names) < 50 {
		t.Errorf("only %d names loaded; the files hold far more, so something is "+
			"not being parsed", len(lx.names))
	}
	if len(lx.phrases) < 20 {
		t.Errorf("only %d phrases loaded", len(lx.phrases))
	}
	if len(lx.placeholders) < 40 {
		t.Errorf("only %d placeholders loaded", len(lx.placeholders))
	}
	// One term from each language, so a file that stops being merged is caught
	// rather than silently narrowing the detector to English.
	for _, want := range []string{"password", "contraseña", "senha"} {
		found := false
		for _, name := range lx.names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is missing: a language file is not being merged", want)
		}
	}
}

// TestCredentialsAreCaughtInEveryLanguage is the whole reason the vocabulary is
// a directory of files rather than a Go literal.
func TestCredentialsAreCaughtInEveryLanguage(t *testing.T) {
	for _, text := range []string{
		`db_password = "Tr0ub4dor&3xkcd"`,
		`contraseña_db = "Tr0ub4dor&3xkcd"`,
		`SENHA_DO_BANCO="Tr0ub4dor&3xkcd"`,
		`clave de acceso: Tr0ub4dor3xkcd`,
		`la clave es Tr0ub4dor3xkcd`,
		`the password is Tr0ub4dor3xkcd`,
		`a senha é Tr0ub4dor3xkcd`,
	} {
		if got := testWatcher(t).inspect(text, "terminal_read"); len(got) == 0 {
			t.Errorf("not detected: %s", text)
		}
	}
}

// TestPlaceholdersAreQuietInEveryLanguage is the half that was broken before
// the vocabulary existed: every placeholder was English, so a Spanish sample
// config raised a credential warning about nothing.
func TestPlaceholdersAreQuietInEveryLanguage(t *testing.T) {
	for _, text := range []string{
		`db_password = changeme`,
		`clave = cambiame`,
		`contraseña = tu_clave`,
		`senha = suasenha`,
		`clave_de_acceso = ejemplo`,
		`senha_do_banco = exemplo`,
		`password = <your-password>`,
		`clave = ${CLAVE}`,
		`senha = xxxxxxxx`,
	} {
		if got := testWatcher(t).inspect(text, "read_file"); len(got) > 0 {
			t.Errorf("false alarm on a placeholder: %s -> %v", text, got)
		}
	}
}

// TestProseDoesNotFire. The dictionary is the LEFT side of a structure, never a
// match on its own — otherwise every sentence containing "contraseña" is an
// incident and the warning becomes noise within a day.
func TestProseDoesNotFire(t *testing.T) {
	for _, text := range []string{
		`cambié la contraseña ayer y anduvo bien`,
		`I forgot my password again`,
		`esqueci a senha do servidor`,
		`the password policy requires twelve characters`,
		`la clave del éxito es la constancia`,
		`Error: authentication failed, check your credentials`,
	} {
		if got := testWatcher(t).inspect(text, "terminal_read"); len(got) > 0 {
			t.Errorf("prose reported as a credential: %q -> %v", text, got)
		}
	}
}
