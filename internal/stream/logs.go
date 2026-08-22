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

package stream

// A link where the logs can be seen.
//
// # What was already true, and what was missing
//
// Everything this serves was already being written. Every job the agent starts
// keeps its stdout and stderr apart under /tmp/sentineldesk/jobs/<id>/; every
// interactive shell on the desktop appends the command, the user, the pane and
// the exit code to /tmp/sentineldesk/shell.log through shell-report.sh; every
// tool call the agent makes lands in the action trail. Three durable records of
// the same afternoon, and no way to look at any of them that did not involve
// getting a shell inside the container.
//
// That gap had a shape: the person supervising an agent is exactly the person
// least likely to have a shell in the container. They are watching a desktop in
// a browser. So the record existed for the audit and not for the supervision,
// which is the wrong way round — an audit is read once, afterwards, by somebody
// who already knows something went wrong.
//
// # The symmetry is the point
//
// This serves BOTH sides from one page: what the agent ran and what the people
// here ran, in the same place, with the same fields. That is not a convenience.
// The rule this desktop is built on is that neither party gets to act unseen —
// the agent's commands run in a tmux window on the shared screen, and a person's
// commands are reported by their own shell. A viewer that showed only the
// agent's half would quietly restate the older assumption, that the machine is
// the thing under supervision and the humans are the supervisors. Here both are
// on the record, and both records open at the same URL.
//
// # Why this is behind the session token
//
// It would be easy to argue that logs are not secrets and can be served openly:
// it is a text file, the desktop is behind a login anyway, and anybody who can
// see the screen can already see the terminal.
//
// The argument is wrong on every clause, and it is worth writing down which is
// which, because this is the kind of endpoint that gets opened up later by
// somebody who only reads the route name.
//
//   - Job output is not a log, it is a transcript of a shell on a shared
//     desktop. `env`, `kubectl get secret -o yaml`, a curl that echoed an
//     Authorization header on failure, a database dump that scrolled past. The
//     daemon takes the position everywhere else that the CONTENTS of this
//     desktop are the sensitive thing — no HTTP endpoint returns secrets, ICE
//     credentials travel over the authenticated socket — and a job's stdout is
//     desktop contents that somebody wrote down.
//   - shell.log is the one file on this desktop that records typed text
//     verbatim. internal/stream/witness.go deliberately counts keystrokes
//     instead of capturing them, precisely because this material is read by an
//     agent that forwards what it reads to a model API. Publishing the file that
//     does keep the text would undo that decision from the other end.
//   - "Behind a login anyway" is the claim being made, not a premise: an
//     unauthenticated /logs IS the hole. And it is a worse hole than an
//     unauthenticated screen, because a screen shows the present moment to
//     whoever is looking, while a log is greppable, complete and still there
//     tomorrow.
//
// So the data endpoints require the same session token the file manager
// requires, issued by the same WebSocket login, and they are the only thing
// under /logs that does. The PAGE at /logs is served openly on exactly the same
// grounds as `/`: it is an empty frame containing no data, which can do nothing
// at all until it has authenticated. With AUTH_USER and AUTH_PASS unset — the
// documented development mode — the token check passes for everyone, the same
// way it does for the file manager, because at that point there is no login for
// this to be weaker than.
//
// # Why there is no ticket in the URL
//
// Downloads use one-use 60-second tickets because a browser NAVIGATION cannot
// carry a header, so the token would otherwise end up in the query string, in
// history and in referers. Nothing here is a navigation: the page fetches its
// data with XHR and puts the token in a header, so a ticket would have bought
// nothing and put a credential in a URL that an agent is about to paste into a
// chat window. If a "download this job's output" button is ever added it should
// build a Blob from a fetch that did the same, not link to a ticketed URL.
//
// That last part is the constraint that shaped this whole design. The URL a tool
// result carries is handed to a model, which repeats it to a person, who may
// paste it anywhere. It therefore has to be worth nothing on its own — a job id
// and a hostname, and not one byte more.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Where the three records live. internal/mcp writes the first two and reads all
// of them; the paths are repeated here rather than imported because internal/mcp
// already imports this package (the room, the delivery), and the dependency
// cannot run both ways.
//
// They are fields on the server rather than constants so the tests can point
// them at a scratch directory. A test that reads the real /tmp reads whatever
// the last run left there, which is how a suite starts depending on its own
// history.
const (
	defaultJobsRoot = "/tmp/sentineldesk/jobs"
	defaultShellLog = "/tmp/sentineldesk/shell.log"
)

// jobIDPattern is the whole of the traversal defence's first half.
//
// A job id is generated by this daemon and is always j<digits>. Nothing else is
// ever a valid id, so the handler does not sanitise the input — it refuses
// anything that is not already exactly the shape it produces. Sanitising is
// where traversal bugs live: every round of stripping "../" is a round somebody
// can encode around, and the list of encodings is not knowable in advance.
//
// The second half is in resolveJobFile, which resolves symlinks BEFORE comparing
// against the root, because a valid id whose `out` file is a link to
// /etc/shadow would otherwise pass this check and read the link.
var jobIDPattern = regexp.MustCompile(`^j[0-9]{1,9}$`)

// maxLogTail bounds a single answer. Job output can be a kernel build, and a
// browser handed 400 MB of text does not display it, it stops responding —
// which reads to the person as "the log viewer is broken" rather than "that is
// a lot of output". The default is smaller still; this is the ceiling a caller
// can ask for.
const maxLogTail = 20000

// LogServer serves the desktop's own records: what the agent ran and what the
// people here ran.
type LogServer struct {
	auth *Auth

	jobsRoot string
	shellLog string
	inputLog string

	// actionLog is the MCP action trail (config.ActionLog). Empty means the
	// operator turned persistence off, which is a legal choice — the page then
	// says the trail is not being kept, rather than showing an empty list that
	// reads as "the agent did nothing".
	actionLog string
}

// NewLogServer builds the viewer. actionLog is config.ActionLog, and may be
// empty.
func NewLogServer(auth *Auth, actionLog string) *LogServer {
	return &LogServer{
		auth:      auth,
		jobsRoot:  defaultJobsRoot,
		shellLog:  defaultShellLog,
		inputLog:  witnessPath,
		actionLog: actionLog,
	}
}

func (l *LogServer) Register(mux *http.ServeMux) {
	// The page. Open, and empty until it authenticates — the same standing as
	// `/`. Registered twice because Go's mux treats "/logs" and "/logs/" as
	// different patterns and a person typing the URL will produce either.
	mux.HandleFunc("/logs", l.handlePage)
	mux.HandleFunc("/logs/", l.handlePage)

	// The data. Longer patterns win in Go's mux, so these take precedence over
	// the "/logs/" above.
	mux.HandleFunc("/logs/api/index", l.handleIndex)
	mux.HandleFunc("/logs/api/job", l.handleJob)
	mux.HandleFunc("/logs/api/people", l.handlePeople)
	mux.HandleFunc("/logs/api/agent", l.handleAgent)
}

// authorized is the same check the file manager makes, deliberately: one
// session token, issued by the WebSocket login, accepted by every HTTP endpoint
// that returns anything about this desktop. With authentication disabled there
// is nothing to check against.
func (l *LogServer) authorized(r *http.Request) bool {
	if !l.auth.Enabled() {
		return true
	}
	return l.auth.ValidToken(r.Header.Get("X-SentinelDesk-Token"))
}

// --- job files ---------------------------------------------------------------

// resolveJobFile turns an id and a file name into a real path inside the jobs
// root, or refuses.
//
// The order matters and is the same order files.go uses: join, resolve
// symlinks, THEN compare against the root. Comparing first and resolving later
// is the classic hole — the string looks confined and the descriptor is not.
//
// The root is resolved too. On a developer's machine /tmp is a symlink to
// /private/tmp and a test's temporary directory lives under /var, which is a
// symlink to /private/var; comparing a resolved path against an unresolved root
// rejects everything, which would look like a working defence right up to the
// day somebody "fixed" it by dropping the resolution.
func (l *LogServer) resolveJobFile(id, name string) (string, error) {
	if !jobIDPattern.MatchString(id) {
		return "", fmt.Errorf("not a job id: %q", id)
	}
	root, err := filepath.EvalSymlinks(l.jobsRoot)
	if err != nil {
		// No jobs directory at all means no job has ever run here.
		return "", fmt.Errorf("no job called %q", id)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, id, name))
	if err != nil {
		return "", fmt.Errorf("job %s has no %s", id, name)
	}
	if !withinRoot(real, root) {
		// A file inside a job directory that points outside it. The id was
		// valid, so this is not a caller mistake — it is somebody with a shell
		// on the desktop trying to read a file through the viewer.
		return "", fmt.Errorf("job %s: %s leaves the job directory", id, name)
	}
	return real, nil
}

// readJobFile reads one of a job's files, keeping at most tail lines.
func (l *LogServer) readJobFile(id, name string, tail int) (string, bool) {
	path, err := l.resolveJobFile(id, name)
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	text := strings.TrimRight(string(raw), "\n")
	if tail > 0 {
		if lines := strings.Split(text, "\n"); len(lines) > tail {
			text = strings.Join(lines[len(lines)-tail:], "\n")
		}
	}
	return text, true
}

// logJob is one job as the viewer shows it. It is rebuilt from the directory
// rather than from any in-process state, for the same reason internal/mcp does:
// the directory is the record, and it outlives the connection that made it.
type logJob struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Aborted  string `json:"aborted_by,omitempty"`
	Started  string `json:"started,omitempty"`
	Ended    string `json:"ended,omitempty"`
}

// jobMeta reads the small marker files a job leaves beside its output.
//
// The status logic is a deliberate copy of readJob in internal/mcp/tools_jobs.go
// and has to keep agreeing with it, including the part that looks arbitrary:
// aborted beats the exit code. A command killed by SIGTERM exits 143, which is
// indistinguishable from a command that chose to exit 143, and the difference
// between "this failed" and "a person stopped this" is the entire reason the
// panic button exists. A viewer that showed the exit code instead would make
// the button look like it had done nothing.
func (l *LogServer) jobMeta(id string) (logJob, bool) {
	read := func(name string) string {
		text, ok := l.readJobFile(id, name, 0)
		if !ok {
			return ""
		}
		return strings.TrimSpace(text)
	}
	// `cmd` is written for every job before the window opens, so its absence
	// means this is not a job directory.
	if _, err := l.resolveJobFile(id, "cmd"); err != nil {
		return logJob{}, false
	}
	job := logJob{
		ID:      id,
		Command: read("cmd"),
		Started: read("started"),
		Ended:   read("ended"),
		Aborted: read("aborted"),
	}
	rc := read("rc")
	if rc != "" {
		if code, err := strconv.Atoi(rc); err == nil {
			job.ExitCode = &code
		}
	}
	switch {
	case job.Aborted != "":
		job.Status = "aborted"
	case rc == "":
		job.Status = "running"
	case job.ExitCode != nil && *job.ExitCode == 0:
		job.Status = "done"
	default:
		job.Status = "failed"
	}
	return job, true
}

// listJobs returns every job on disk, newest first.
func (l *LogServer) listJobs() []logJob {
	root, err := filepath.EvalSymlinks(l.jobsRoot)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []logJob
	for _, e := range entries {
		if !e.IsDir() || !jobIDPattern.MatchString(e.Name()) {
			continue
		}
		if job, ok := l.jobMeta(e.Name()); ok {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(out[i].ID, "j"))
		b, _ := strconv.Atoi(strings.TrimPrefix(out[j].ID, "j"))
		return a > b
	})
	return out
}

// --- the two halves of the record --------------------------------------------

// logLine is one entry from either side, in one shape so the page can render
// them with one function and a person can compare them without translating.
type logLine struct {
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Source string `json:"source"` // terminal | person | agent
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
	Exit   *int   `json:"exit_code,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
}

// readTabLog parses the two tab-separated files people's activity is written to.
//
// The parser is told which shape it is reading rather than counting fields: a
// shell command containing a tab would otherwise be misread as an input event,
// and the misreading would be silent.
func readTabLog(path, source string) []logLine {
	f, err := os.Open(path)
	if err != nil {
		return nil // absent means nobody has done that yet, not a failure
	}
	defer f.Close()

	var out []logLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if source == "terminal" {
			// time, user, pane, exit code, command
			parts := strings.SplitN(line, "\t", 5)
			if len(parts) != 5 {
				continue
			}
			e := logLine{Time: parts[0], Actor: parts[1], Source: "terminal",
				What: "ran", Detail: parts[4]}
			if code, err := strconv.Atoi(strings.TrimSpace(parts[3])); err == nil {
				ok := code == 0
				e.Exit, e.OK = &code, &ok
			}
			out = append(out, e)
			continue
		}
		// time, actor, what, detail
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		out = append(out, logLine{Time: parts[0], Actor: parts[1],
			Source: "person", What: parts[2], Detail: parts[3]})
	}
	return out
}

// readActionTrail parses the agent's side: the JSONL trail of every tool call.
//
// Decoded into the fields this page shows rather than into internal/mcp's
// struct, which this package cannot see. An entry that will not parse is
// skipped: the trail is appended to by a live process, so the last line can be
// half-written at the moment somebody loads the page, and refusing the whole
// file because of it would make the viewer fail most reliably when it is being
// watched most closely.
func readActionTrail(path string) []logLine {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []logLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var e struct {
			Time   string `json:"time"`
			Tool   string `json:"tool"`
			Args   string `json:"args"`
			OK     bool   `json:"ok"`
			Denied string `json:"denied"`
			Client string `json:"client"`
			Result string `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Tool == "" {
			continue
		}
		actor := e.Client
		if actor == "" {
			actor = "agent"
		}
		detail := e.Args
		if e.Denied != "" {
			detail = strings.TrimSpace(detail + " — refused: " + e.Denied)
		} else if e.Result != "" {
			detail = strings.TrimSpace(detail + " → " + e.Result)
		}
		ok := e.OK
		out = append(out, logLine{Time: e.Time, Actor: actor, Source: "agent",
			What: e.Tool, Detail: detail, OK: &ok})
	}
	return out
}

func tailLines(lines []logLine, n int) []logLine {
	if n > 0 && len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// tailParam reads the caller's line budget, defaulting and clamping.
func tailParam(r *http.Request, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("tail"))
	if err != nil || n <= 0 {
		return def
	}
	if n > maxLogTail {
		return maxLogTail
	}
	return n
}

// --- handlers ----------------------------------------------------------------

func (l *LogServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !l.authorized(r) {
		writeErr(w, http.StatusUnauthorized, "not authorized")
		return
	}
	jobs := l.listJobs()
	if jobs == nil {
		jobs = []logJob{}
	}
	writeJSON(w, map[string]any{
		"jobs": jobs,
		// Stated rather than inferred from an empty list, because "nothing has
		// happened" and "this is not being recorded" are opposite answers and
		// an empty array reads as whichever one the reader already believed.
		"people_recorded": beingRecorded(l.shellLog) || beingRecorded(l.inputLog),
		"agent_recorded":  l.actionLog != "" && beingRecorded(l.actionLog),
		"generated":       time.Now().UTC().Format(time.RFC3339),
	})
}

func (l *LogServer) handleJob(w http.ResponseWriter, r *http.Request) {
	if !l.authorized(r) {
		writeErr(w, http.StatusUnauthorized, "not authorized")
		return
	}
	id := r.URL.Query().Get("id")
	if !jobIDPattern.MatchString(id) {
		// 400 rather than 404: the caller sent something that is not an id at
		// all, and telling them "not found" would invite them to try variations
		// of it. There is nothing to search for here.
		writeErr(w, http.StatusBadRequest, "not a job id: %q", id)
		return
	}
	job, ok := l.jobMeta(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no job called %q on this desktop", id)
		return
	}
	tail := tailParam(r, 2000)
	stdout, _ := l.readJobFile(id, "out", tail)
	stderr, _ := l.readJobFile(id, "err", tail)
	writeJSON(w, map[string]any{
		"job": job, "stdout": stdout, "stderr": stderr, "tail": tail,
	})
}

func (l *LogServer) handlePeople(w http.ResponseWriter, r *http.Request) {
	if !l.authorized(r) {
		writeErr(w, http.StatusUnauthorized, "not authorized")
		return
	}
	lines := append(readTabLog(l.shellLog, "terminal"), readTabLog(l.inputLog, "person")...)
	// Both writers stamp RFC3339 in UTC, so string order is time order. Stable,
	// so two entries in the same second keep the order they were gathered in
	// instead of shuffling every time somebody reloads.
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time < lines[j].Time })
	writeJSON(w, map[string]any{
		"entries":  tailLines(lines, tailParam(r, 500)),
		"recorded": beingRecorded(l.shellLog) || beingRecorded(l.inputLog),
		"note": "keystrokes are counted, never captured — this desktop is where " +
			"people type passwords. Commands typed at a shell are recorded in full.",
	})
}

func (l *LogServer) handleAgent(w http.ResponseWriter, r *http.Request) {
	if !l.authorized(r) {
		writeErr(w, http.StatusUnauthorized, "not authorized")
		return
	}
	if l.actionLog == "" {
		writeJSON(w, map[string]any{"entries": []logLine{}, "recorded": false,
			"note": "ACTION_LOG is empty, so the agent's tool calls are kept in memory " +
				"only and cannot be read here."})
		return
	}
	lines := readActionTrail(l.actionLog)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time < lines[j].Time })
	writeJSON(w, map[string]any{
		"entries":  tailLines(lines, tailParam(r, 500)),
		"recorded": beingRecorded(l.actionLog),
	})
}

// beingRecorded reports whether one of the record files exists as a file.
//
// Separate from tls.go's fileExists, which answers a different question: there,
// a directory named like a certificate is somebody's problem to discover, and
// an empty path is not reachable. Here an empty path is the ordinary "the
// operator turned this off" case, and it must answer no rather than stat("").
func beingRecorded(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func (l *LogServer) handlePage(w http.ResponseWriter, r *http.Request) {
	// Nothing under /logs/ exists except the page and the API above it. A
	// deep path is a mistake or a probe, and either way it is not this page.
	if r.URL.Path != "/logs" && r.URL.Path != "/logs/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// No caching. The page is small and the alternative is somebody reading
	// yesterday's viewer against today's desktop after an upgrade.
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(logViewerHTML))
}
