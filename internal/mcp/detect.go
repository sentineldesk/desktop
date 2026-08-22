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

// Telling somebody their credential has left the building.
//
// # This does not prevent anything, and pretending otherwise would be the bug
//
// The vault next door is a real guarantee, and a narrow one: a value somebody
// registered, used through a reference, never enters the model's context. It is
// arithmetic rather than judgement.
//
// Everything else is not covered and cannot be. A person pastes a password into
// a terminal the agent is reading. Somebody runs `cat .env` in a shared session.
// A config file holds a token nobody thought to register. In every one of those
// the secret reached the model before any code here ran, and the honest response
// is not to dress up a filter as protection.
//
// So this detector has one job: say WHAT probably went out and WHEN, precisely
// enough that somebody can rotate that one credential instead of rotating
// everything on the machine because they cannot tell. It turns a silent failure
// into an incident with a known scope, which is the whole of the value.
//
// That is also why it does not block. A tool result withheld after the fact
// protects nothing — the copy has already left — and would break the task for
// no benefit, which is how a warning gets switched off.
//
// # Shapes, not guesses
//
// Only things with syntax of their own: a key with a fixed prefix and length, a
// PEM header, the three dot-separated segments of a JWT, a URL carrying
// credentials. No entropy heuristics and no "looks like a password", because
// both produce false positives on ordinary output — and a notice that cries wolf
// is worse than none. People learn to dismiss it, and then the real one is
// dismissed too.
//
// An LLM as a second pass was considered and is not here. Asking a model whether
// some text contains a secret means sending it the text, which is the thing
// being avoided; it would only make sense against a model running locally, and
// then the shapes below already catch what it would.
//
// # The policy set grows without a rebuild
//
// The shapes below are the ones worth shipping. They are not the ones a given
// installation needs: an internal token format, a customer id, the prefix a
// company puts on its own keys. CREDENTIAL_POLICY points at a file of extra
// rules, one `name = regexp` per line, merged with these.
//
// Same reasoning that took the search vocabulary out of Go and into a file. A
// rule somebody cannot add without recompiling is a rule they do not add, and
// the person who knows what their secrets look like is not the person building
// this binary. A rule that does not compile is reported and skipped rather than
// taken down with the daemon — one bad regexp must not cost somebody a
// desktop.
package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sentineldesk/desktop/pkg/config"
)

// finding is one credential that appears to have been exposed.
//
// It carries a KIND and never a value. The notice goes onto the shared screen
// and into a log that other people read, so a warning quoting the secret would
// be a second leak dressed as a safety feature — and this one would land in
// screenshots and recordings.
type finding struct {
	// ID is the stable key the web client translates, empty for a rule from an
	// installation's own policy file. Kind is the English name, which is what
	// the log and the daemon's stderr use — everything this project writes for a
	// machine or for its own record stays English; only the browser translates.
	ID    string `json:"id,omitempty"`
	Kind  string `json:"kind"`
	Where string `json:"where"` // the tool it came out of
	// Fingerprint is a truncated hash, so two sightings of the same credential
	// can be recognised as one without the credential being recoverable.
	Fingerprint string `json:"fingerprint"`
}

// secretShape is one rule. id is a stable key the web client translates; a rule
// loaded from an installation's own policy file has none, and its name is shown
// as the operator wrote it — in whatever language they wrote it in, which is the
// only sensible thing to do with a string this binary has never seen.
type secretShape struct {
	id   string
	kind string
	re   *regexp.Regexp
}

// shapes are the patterns worth acting on. Each one names a specific product,
// because "rotate the thing that matches pattern 4" is not actionable and
// "rotate your AWS access key" is.
var shapes = []secretShape{
	{"aws_key", "AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github_token", "GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"slack_token", "Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"anthropic_key", "Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`)},
	{"openai_key", "OpenAI API key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9]{32,}\b`)},
	{"google_key", "Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"private_key", "private key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`)},
	{"jwt", "JSON Web Token", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},

	// A URL carrying credentials — postgres://user:password@host. Common in
	// connection strings, .env files and docker-compose, which is exactly the
	// material somebody cats in a shared session.
	{"url_password", "password in a connection URL", regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s:/@]{4,}@`)},

	// An assignment. Deliberately the narrowest of the set: it requires the
	// value to be quoted or unbroken and at least eight characters, because
	// `password=` followed by a placeholder or an empty string is the most
	// common thing in any config file on earth and warning about it is how this
	// becomes noise.
}

// loadPolicyFile merges an installation's own rules into the set.
func loadPolicyFile(path string) []secretShape {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "mcp: credential policy %s: %v\n", path, err)
		}
		return nil
	}
	var out []secretShape
	for n, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, pattern, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name, pattern = strings.TrimSpace(name), strings.TrimSpace(pattern)
		if name == "" || pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp: credential policy %s line %d (%s): %v\n",
				path, n+1, name, err)
			continue
		}
		out = append(out, secretShape{id: "", kind: name, re: re})
	}
	if len(out) > 0 {
		fmt.Fprintf(os.Stderr, "mcp: %d extra credential rule(s) from %s\n", len(out), path)
	}
	return out
}

// credentialWatcher notices credentials on their way out and remembers what
// it has already reported.
type credentialWatcher struct {
	mu sync.Mutex

	// The built-in shapes plus whatever this installation added. Held here
	// rather than read from the package variable so a test can build a watcher
	// with exactly the rules it means to exercise.
	shapes []secretShape
	// seen fingerprints, so one .env file read three times is one warning. A
	// notice that repeats trains people to dismiss it without reading, and a
	// dismissed warning is the same as no warning.
	seen map[string]time.Time
}

func newCredentialWatcher() *credentialWatcher {
	w := &credentialWatcher{seen: map[string]time.Time{}}
	w.shapes = append(w.shapes, shapes...)
	// The vocabulary's rules, built from credentials/*.md. They come after the
	// fixed shapes so a value with a distinctive syntax is named as what it
	// actually is — "an AWS access key" rather than "a password in a
	// configuration value", which is the difference between knowing what to
	// rotate and knowing that something must be.
	w.shapes = append(w.shapes, shapesFromLexicon()...)
	w.shapes = append(w.shapes,
		loadPolicyFile(config.Str("CREDENTIAL_POLICY", "/etc/sentineldesk/credential-policy"))...)
	return w
}

func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// inspect reports credentials in text that have not been reported already.
//
// The scan is capped. A tool can return megabytes — a whole log file, a
// directory listing — and running ten regexes over all of it on every call would
// put a measurable cost on the path every result travels. The cap is stated as a
// limitation rather than hidden: a credential past the first 256 KB of one
// result is missed.
func (w *credentialWatcher) inspect(text, where string) []finding {
	const scanLimit = 256 << 10
	if len(text) > scanLimit {
		text = text[:scanLimit]
	}

	var out []finding
	now := time.Now()

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, shape := range w.shapes {
		for _, match := range shape.re.FindAllStringSubmatch(text, 20) {
			value := match[0]
			// A capture group means the pattern found the value inside a larger
			// expression — the assignment shape — and the group is the part that
			// matters for both the placeholder check and the fingerprint.
			if len(match) > 1 && match[1] != "" {
				value = match[1]
			}
			if isPlaceholder(value) {
				continue
			}
			print := fingerprint(value)
			// Reported within the last hour is not reported again. Long enough
			// that a task touching the same file repeatedly stays quiet, short
			// enough that a credential still leaking tomorrow says so again.
			if last, ok := w.seen[print]; ok && now.Sub(last) < time.Hour {
				continue
			}
			w.seen[print] = now
			out = append(out, finding{ID: shape.id, Kind: shape.kind, Where: where, Fingerprint: print})
		}
	}
	return out
}

// noticeKinds returns what to warn about, as keys rather than as a sentence.
//
// It deliberately does NOT build the message. The web client speaks three
// languages and this binary speaks one, so prose composed here would arrive in
// English on a desktop somebody is using in Spanish — and a warning nobody can
// read is worse than none, because it looks like the system is working.
//
// The project's rule already said this: every user-facing string lives in
// assets/lang/*.json, and that is the only translated text anywhere. What
// crosses this boundary is data.
//
// A rule from an installation's own policy file has no key, so its name travels
// verbatim and the client shows it as written. The operator chose that string;
// nobody here is in a position to translate it.
func noticeKinds(found []finding) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range found {
		key := f.ID
		if key == "" {
			key = f.Kind
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// warnAboutCredentials tells the people watching what has already gone out.
//
// It runs at the same choke point as redaction and after it, so the two do not
// disagree: the vault's values are gone by now, and anything the shapes still
// find is genuinely unregistered — which is what makes the warning specific
// enough to act on rather than a general reminder to be careful.
func (s *Server) warnAboutCredentials(content []map[string]any, tool string) {
	if s.creds == nil {
		return
	}
	var found []finding
	for _, block := range content {
		text, ok := block["text"].(string)
		if !ok {
			continue
		}
		// Strip our OWN references before looking, or the redaction triggers the
		// detector it runs beside.
		//
		// `{{secret:db_root}}` contains the word `secret` followed by a colon
		// and then eight-plus characters, which is precisely the shape of a
		// password assignment — so every value the vault successfully protected
		// came back out as a credential that had supposedly escaped. Both halves
		// were correct in isolation and wrong together, and only a test that ran
		// a real tool call through both of them showed it.
		//
		// The consequence would have been worse than noise: somebody told to
		// rotate a key that never left is somebody learning that these warnings
		// are wrong, which spends the attention the real ones need.
		found = append(found, s.creds.inspect(secretRefLoose.ReplaceAllString(text, ""), tool)...)
	}
	if len(found) == 0 {
		return
	}

	// Into the trail first, and to the screen second. The banner is for whoever
	// happens to be looking; the record is what somebody reads afterwards to
	// work out the scope, and it has to exist even if the room was empty.
	for _, f := range found {
		s.actions.Add(actionEntry{
			Time: time.Now().UTC().Format(time.RFC3339),
			Tool: "credential_exposed", OK: false,
			Args: fmt.Sprintf("%s seen in the output of %s (fingerprint %s)",
				f.Kind, f.Where, f.Fingerprint),
			Kind:     "credential",
			Workroom: s.cfg.WorkroomID, Runtime: s.cfg.RuntimeID,
		})
		log.Printf("mcp: %s was visible to the agent via %s (fingerprint %s) — rotate it",
			f.Kind, f.Where, f.Fingerprint)
	}
	if s.room != nil {
		s.room.Notice("credential", noticeKinds(found))
	}
}
