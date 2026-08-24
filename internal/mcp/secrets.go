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

// Secrets the agent can USE without ever seeing.
//
// # The shape of the problem
//
// An agent that administers a machine needs passwords. It needs to log into a
// database, unlock a key, authenticate to a registry. And everything it reads
// goes into a request to a model API run by somebody else — so a password that
// reaches the agent has left the building, and no amount of care afterwards
// brings it back.
//
// The usual answers are both bad. Giving it the password trades the secret for
// the capability. Withholding it means the agent cannot do the work and a person
// has to finish every task by hand, which is the same as not having an agent.
//
// # Reference, resolve, redact
//
// The agent writes a REFERENCE and never a value:
//
//	run_command  mysql -u root -p{{secret:db_root}} -e 'show databases'
//
// The daemon rewrites that into a shell variable and puts the value in the
// process's environment:
//
//	mysql -u root -p"$SD_SECRET_DB_ROOT" -e 'show databases'
//
// This is the part that looks like a detail and is the whole design. String
// interpolation — swapping the reference for the value in the command text —
// would have worked, and would have taken the password out of the model's
// context and put it in four worse places: the terminal pane on the SHARED
// SCREEN where everyone watching can read it, the job's `cmd` file, the shell
// history, and the desktop's own activity log. A secret moved out of one leak
// and into four is not progress.
//
// The reverse direction is the same rule read backwards. Anything travelling
// OUT to the agent passes through Redact, which replaces a known value with the
// reference it came from. So `cat /etc/app.conf` returns
// `password={{secret:db_root}}` rather than the password, and the agent can go
// on reasoning about the file without ever holding what is in it.
//
// # Two ways a value gets here, and only one of them is a security property
//
// REGISTERED: somebody put it in the vault, deliberately. Redaction is then
// exact string matching, which is completely reliable for what is in it.
//
// DETECTED by shape: an AWS key, a PEM block, a JWT, an assignment that says
// `password=`. Worth having, and it is a net rather than a wall — the false
// negative is a secret that goes out, and you find out afterwards. It is written
// here as a second layer precisely so nobody mistakes it for the first.
//
// A third way is better than both and needs no vault at all: ASK. A reference
// with nothing behind it becomes a question on the desktop, the person types the
// value, and it goes straight into the environment of the command that needed
// it. It is never stored, never logged, and never returned to the agent — which
// asked for a password to be USED and got exactly that.
package mcp

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sentineldesk/desktop/pkg/config"
)

// secretRef is how the agent names something it must not see: {{secret:name}}.
//
// Doubled braces because the alternatives collide with things people really
// type. $NAME is a shell variable and would be ambiguous inside a command that
// legitimately uses one; <name> appears in every usage string ever written.
var secretRef = regexp.MustCompile(`\{\{secret:([a-z0-9][a-z0-9_]*)\}\}`)

// secretRefLoose matches the reference syntax in ANY form, including the way it
// is written in documentation: {{secret:<name>}}, {{secret:name}}, whatever a
// description happens to show.
//
// It exists because the credential detector fired on this package's own help
// text. `secret_list` explains itself with the literal `{{secret:<name>}}`, and
// that string contains the word `secret` followed by a colon and then enough
// characters to look like a password assignment — so calling the tool that
// tells an agent how to USE secrets reported a credential leak.
//
// Found by running it in the container rather than by any unit test, because
// every unit test fed the detector realistic file contents and none of them fed
// it our own output. It is exactly the false positive that ruins a warning:
// nothing had leaked, and somebody would have been sent to rotate a key over a
// sentence in a tool description.
var secretRefLoose = regexp.MustCompile(`\{\{\s*secret\s*:[^}]*\}\}`)

// secretEnvPrefix namespaces the variables the command sees, so a reference
// cannot be used to smuggle a value into PATH or LD_PRELOAD.
const secretEnvPrefix = "SD_SECRET_"

// vault holds what the desktop has been told, and nothing it has guessed.
type vault struct {
	mu sync.RWMutex

	// values is name -> secret. Never marshalled, never logged, never returned
	// by a tool. The only ways out are into a process's environment and into
	// Redact's replacement table, and both of those consume it rather than
	// publishing it.
	values map[string]string

	// asked remembers what a person typed for the life of the desktop, so they
	// are not prompted once per command in a five-step task.
	//
	// In memory only, deliberately: a value somebody typed into a prompt was
	// given for a task, not deposited for keeping, and writing it to disk would
	// convert one into the other without asking. It dies with the process.
	asked map[string]string
}

func newVault() *vault {
	v := &vault{values: map[string]string{}, asked: map[string]string{}}
	v.loadFile(config.Str("SECRETS_FILE", "/var/lib/sentineldesk/secrets"))
	return v
}

// loadFile reads name=value lines, refusing a file anybody else can read.
//
// The permission check is a refusal rather than a warning. A secrets file at
// 0644 is not a smaller version of a secrets file at 0600 — every other user
// and every process in the container already has the contents, so loading it
// would be recording a protection that is not there.
func (v *vault) loadFile(path string) {
	if path == "" {
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "mcp: secrets file %s: %v\n", path, err)
		}
		return
	}
	if st.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr,
			"mcp: refusing to read %s, mode is %04o and must be 0600 — "+
				"anything readable by another user is not a secret\n",
			path, st.Mode().Perm())
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: secrets file %s: %v\n", path, err)
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if name == "" || value == "" {
			continue
		}
		v.values[name] = value
	}
	if len(v.values) > 0 {
		// The count, never the names' values. Somebody has to be able to tell a
		// vault that loaded from one that silently did not.
		fmt.Fprintf(os.Stderr, "mcp: %d secret(s) loaded from %s\n", len(v.values), path)
	}
}

// lookup returns a value from either store.
func (v *vault) lookup(name string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if val, ok := v.values[name]; ok {
		return val, true
	}
	val, ok := v.asked[name]
	return val, ok
}

// remember keeps what a person typed, for this desktop's lifetime only.
func (v *vault) remember(name, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.asked[name] = value
}

// names lists what exists, which is the only thing about a secret that is safe
// to tell an agent — and is genuinely useful, because an agent that knows
// `db_root` exists can write a command that uses it.
func (v *vault) names() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.values)+len(v.asked))
	seen := map[string]bool{}
	for _, m := range []map[string]string{v.values, v.asked} {
		for name := range m {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// all returns every value paired with its name, for redaction. Longest first,
// so a secret that contains another one is replaced whole rather than leaving
// half of itself behind next to a reference.
func (v *vault) all() [][2]string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	var out [][2]string
	for _, m := range []map[string]string{v.values, v.asked} {
		for name, value := range m {
			out = append(out, [2]string{name, value})
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i][1]) > len(out[j][1]) })
	return out
}

// refsIn returns the secret names a command asks for, in order, without repeats.
func refsIn(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range secretRef.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

func envNameFor(name string) string {
	return secretEnvPrefix + strings.ToUpper(name)
}

// resolveSecrets rewrites a command's references into shell variables and
// returns the environment that gives them values.
//
// Missing names come back separately rather than as an error, because the
// caller can do something about them: ask the people watching. A command that
// referred to a secret nobody has is not a broken command, it is a command
// waiting for somebody to type something.
func (s *Server) resolveSecrets(command string) (rewritten string, env []string, missing []string) {
	names := refsIn(command)
	if len(names) == 0 {
		return command, nil, nil
	}
	for _, name := range names {
		value, ok := s.vault.lookup(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		env = append(env, envNameFor(name)+"="+value)
	}
	// Quoted, because a password containing a space or a glob character would
	// otherwise be split into several arguments or expanded against the
	// filesystem — and the failure would be a confusing authentication error
	// rather than anything that points at quoting.
	rewritten = secretRef.ReplaceAllStringFunc(command, func(m string) string {
		return `"$` + envNameFor(secretRef.FindStringSubmatch(m)[1]) + `"`
	})
	return rewritten, env, missing
}

// askForSecrets puts the missing ones to the people watching.
//
// The answer goes into the vault's in-memory half and NOT back to the caller.
// That asymmetry is the point of the whole file: the agent asked for a password
// to be used, and it is being used. It was never asked to be told one.
func (s *Server) askForSecrets(missing []string, why string) error {
	if s.room == nil {
		return fmt.Errorf("this command needs %s, which is not in the vault, and "+
			"there is nobody here to ask", strings.Join(missing, ", "))
	}
	for _, name := range missing {
		question := fmt.Sprintf(
			"The agent needs the secret %q to run: %s\n"+
				"Type it and it goes straight into that command. It is not stored on "+
				"disk, not written to any log, and not shown to the agent.", name, why)
		answer, err := s.room.AskSecret(question, 120*time.Second)
		if err != nil {
			return fmt.Errorf("nobody supplied %q: %v", name, err)
		}
		if strings.TrimSpace(answer) == "" {
			return fmt.Errorf("nobody supplied %q", name)
		}
		s.vault.remember(name, answer)
	}
	return nil
}

// Redact replaces known secret values with the reference they came from.
//
// Applied on the way OUT, at one choke point, for the same reason the policy
// gate lives in exactly one place: a redaction that each tool has to remember
// is a redaction that the next tool forgets. There is no way to add a tool that
// leaks a registered secret, because no tool decides this.
//
// The replacement is a reference rather than asterisks, and that is worth a
// sentence. `password=*****` tells the agent that something was hidden and
// leaves it unable to act; `password={{secret:db_root}}` tells it the same thing
// AND hands it the exact token to write into the next command. Redaction that
// preserves capability is redaction people leave switched on.
func (v *vault) Redact(text string) string {
	if text == "" {
		return text
	}
	for _, pair := range v.all() {
		// Short values are not replaced. A secret of three characters appears
		// inside ordinary words and paths, and redacting those turns readable
		// output into confetti — which gets the feature switched off, which
		// protects nothing. A password too short for this is too short.
		if len(pair[1]) < 6 {
			continue
		}
		text = strings.ReplaceAll(text, pair[1], "{{secret:"+pair[0]+"}}")
	}
	return text
}

// redactContent walks an MCP content block and redacts every text field.
//
// Images are NOT covered and cannot be, which is stated here rather than
// discovered later: a screenshot of a terminal showing a password is a picture
// of a password, and nothing short of running OCR over every frame would catch
// it. The mitigation is elsewhere — a secret used through a reference never
// appears on screen in the first place, because it travels by environment.
func (v *vault) redactContent(content []map[string]any) []map[string]any {
	for _, block := range content {
		if text, ok := block["text"].(string); ok {
			block["text"] = v.Redact(text)
		}
	}
	return content
}

func (s *Server) buildSecretTools() []toolDef {
	return []toolDef{
		{
			Name: "secret_list",
			Risk: riskRead,
			Description: "The names of the secrets this desktop holds — never their " +
				"values, which you cannot read by any means. Use a name by writing " +
				"{{secret:name}} inside a command: it is replaced by a shell variable " +
				"and the value arrives in the process's environment, so it never " +
				"appears on the shared screen, in the shell history or in anything " +
				"returned to you. A name that is not listed still works: the people " +
				"here are asked to type it, and what they type is not shown to you " +
				"either. Never ask a person to tell you a password in conversation — " +
				"reference it and let it be used.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "type_secret",
			Risk:            riskWrite,
			Visibility:      visInjects,
			RequiresControl: true,
			Description: "Type a secret into a field on screen by ref, without ever seeing it. " +
				"For a login form, where {{secret:name}} cannot help because the value has to " +
				"become keystrokes rather than an environment variable. The value is written " +
				"straight into the element and is never returned to you, never logged and never " +
				"put on the shared screen as text. A name the vault does not hold is not a " +
				"failure: the people here are asked to type it. Find the ref with ui_find first, " +
				"and never fall back to type_text with the value written out.",
			InputSchema: schema(map[string]any{
				"name": pStr("the secret's name, as secret_list reports it"),
				"ref":  pStr("element ref of the field, from ui_find"),
			}, "name", "ref"),
		},
	}
}

// typeSecret writes a secret into an element without the value passing through
// anything the agent can read.
//
// The ref is REQUIRED and that is the load-bearing half. Typing "the secret"
// with no target sends it wherever focus happens to be, and focus is not a
// property this tool controls — a dialog that stole it between ui_find and here
// would receive somebody's password. So the value goes to a named element or it
// does not go.
func (s *Server) typeSecret(args map[string]any) ([]map[string]any, bool) {
	name := strings.TrimSpace(argStr(args, "name"))
	ref := strings.TrimSpace(argStr(args, "ref"))
	if name == "" {
		return textContent("type_secret needs the `name` of a secret"), true
	}
	if ref == "" {
		return textContent("type_secret needs a `ref` — find the field with ui_find first. " +
			"Without one the value would go to whatever holds focus, which is how a " +
			"password ends up in the wrong box."), true
	}

	value, ok := s.vault.lookup(name)
	if !ok {
		// A name with nothing behind it is a question, exactly as it is for a
		// command reference. See askForSecrets.
		if err := s.askForSecrets([]string{name}, "typing it into a field on screen"); err != nil {
			return textContent("%s", err.Error()), true
		}
		if value, ok = s.vault.lookup(name); !ok {
			return textContent("nobody supplied %q", name), true
		}
	}

	if err := s.typeSecretInto(ref, value); err != nil {
		// The error is the tool's, not the value's: a11y failures name the ref
		// and never echo what was being written.
		return textContent("could not type into %s: %v", ref, err), true
	}
	// What comes back names the secret and the field and nothing else. The
	// length is not reported either: it is a fact about the value, and a
	// character count narrows a guess more than nothing does.
	return textContent(
		"typed the secret %q into %s. Its value was not shown to you and is not in any log.",
		name, ref), false
}

// typeSecretInto is the keystrokes, behind a seam so a test can watch what would
// have been typed without an accessibility bridge or a display.
func (s *Server) typeSecretInto(ref, value string) error {
	if s.typeInto != nil {
		return s.typeInto(ref, value)
	}
	_, isErr := s.a11y("settext", "--ref", ref, "--text", value)
	if isErr {
		return fmt.Errorf("the accessibility bridge refused the write")
	}
	return nil
}

func (s *Server) dispatchSecrets(name string, args map[string]any) ([]map[string]any, bool, bool) {
	if name == "type_secret" {
		c, isErr := s.typeSecret(args)
		return c, isErr, true
	}
	if name != "secret_list" {
		return nil, false, false
	}
	names := s.vault.names()
	if len(names) == 0 {
		return textContent("no secrets are registered. You can still write " +
			"{{secret:some_name}} in a command — the people here will be asked to " +
			"type it, and it will be used without being shown to you."), false, true
	}
	return jsonContent(map[string]any{
		"names": names,
		"how":   "write {{secret:<name>}} inside a command; the value never reaches you",
	}), false, true
}
