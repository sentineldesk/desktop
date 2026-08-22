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

// What the log viewer must not do, which is most of what these tests are about.
//
// The tests that matter here are the refusals. A viewer that renders a job
// nicely is worth nothing if it also renders /etc/shadow when asked for one, and
// the difference between the two is a handful of characters in a path — the kind
// of thing that survives a review because everyone reads the happy path first.
// So the refusals come first: an id that is not an id, an id that walks out of
// the jobs root, a file inside a job directory that is a symlink to somewhere
// else, an id nobody has used, and a request with no session token.
//
// The last one is the one worth stating out loud. The endpoints serve command
// output from a shared desktop and the verbatim commands people typed at its
// shells; if the token check is ever loosened for convenience, this is the test
// that has to be deleted first, which is the point of writing it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logsUnder builds a LogServer over a scratch directory, with authentication
// ON — the configuration the deployed desktop runs in, and the only one in
// which the token checks mean anything.
func logsUnder(t *testing.T) (*LogServer, string, string) {
	t.Helper()
	dir := t.TempDir()
	jobs := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	auth := &Auth{user: "ana", pass: "secret", secret: []byte("test-secret"),
		ttl: time.Hour, enabled: true}
	l := &LogServer{
		auth:      auth,
		jobsRoot:  jobs,
		shellLog:  filepath.Join(dir, "shell.log"),
		inputLog:  filepath.Join(dir, "input.log"),
		actionLog: filepath.Join(dir, "actions.jsonl"),
	}
	return l, dir, auth.NewToken()
}

// writeJobDir lays out a job directory the way job-run.sh does.
func writeJobDir(t *testing.T, root, id string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// get issues a request through the registered mux, with the token unless it is
// empty.
func logGet(t *testing.T, l *LogServer, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	l.Register(mux)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("X-SentinelDesk-Token", token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func logJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return body
}

func TestJobOutputNeedsTheSessionToken(t *testing.T) {
	l, _, token := logsUnder(t)
	writeJobDir(t, l.jobsRoot, "j1", map[string]string{
		"cmd": "printenv", "rc": "0", "out": "AWS_SECRET_ACCESS_KEY=hunter2\n",
	})

	// Every data endpoint, because a hole in one of them is a hole.
	for _, target := range []string{
		"/logs/api/job?id=j1", "/logs/api/index", "/logs/api/people", "/logs/api/agent",
	} {
		rec := logGet(t, l, target, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a token, want 401 — this endpoint serves "+
				"command output and typed commands from a shared desktop", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("%s leaked job output to an unauthenticated caller", target)
		}
	}

	// And with the token, the same request works — otherwise the test above
	// would pass just as well against a handler that refuses everybody.
	rec := logGet(t, l, "/logs/api/job?id=j1", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated request answered %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("authenticated request did not return the job's output: %s", rec.Body.String())
	}
}

func TestAGarbageTokenIsNotASessionToken(t *testing.T) {
	l, _, _ := logsUnder(t)
	writeJobDir(t, l.jobsRoot, "j1", map[string]string{"cmd": "ls", "rc": "0", "out": "x\n"})

	for _, bad := range []string{"x", "....", "eyJ.eyJ", "true"} {
		if rec := logGet(t, l, "/logs/api/job?id=j1", bad); rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q answered %d, want 401", bad, rec.Code)
		}
	}
}

func TestThePageItselfIsOpenAndCarriesNoData(t *testing.T) {
	l, _, _ := logsUnder(t)
	writeJobDir(t, l.jobsRoot, "j1", map[string]string{
		"cmd": "printenv", "rc": "0", "out": "AWS_SECRET_ACCESS_KEY=hunter2\n",
	})

	rec := logGet(t, l, "/logs?job=j1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("the page answered %d without a token, want 200 — it is an empty "+
			"frame, on the same footing as /", rec.Code)
	}
	// The whole justification for serving it openly is that it contains nothing.
	// A future version that server-renders the job would quietly turn an open
	// page into an open log.
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Error("the openly-served page contains job output; it must fetch its data " +
			"with the session token instead of being rendered with it")
	}
}

func TestPathTraversalIsRefusedBeforeAnythingIsRead(t *testing.T) {
	l, dir, token := logsUnder(t)
	secret := filepath.Join(dir, "outside-the-root")
	if err := os.WriteFile(secret, []byte("root:x:0:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJobDir(t, l.jobsRoot, "j1", map[string]string{"cmd": "ls", "rc": "0", "out": "x\n"})

	// These are the ids AFTER the query string has been decoded — the form the
	// handler actually sees. Every one is refused by the id pattern rather than
	// by cleaning the string, which is the whole point: a handler that strips
	// "../" and then joins is defeated by whichever encoding its author did not
	// think of, and that list cannot be enumerated in advance.
	traversals := []string{
		"../outside-the-root",
		"../../outside-the-root",
		"j1/../../outside-the-root",
		"/etc/passwd",
		"..",
		".",
		"",
		"j1\x00",
		"j1 ",
		"J1",
		"j1/out",
		"j1/",
		"jj1",
		"j" + strings.Repeat("1", 40),
	}
	for _, id := range traversals {
		rec := logGet(t, l, "/logs/api/job?id="+url.QueryEscape(id), token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q answered %d, want 400 — a job id is j<digits> and nothing else",
				id, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "root:x") {
			t.Fatalf("id %q read a file outside the jobs root", id)
		}
	}

	// And the same thing arriving percent-encoded, written into the URL by hand
	// so nothing in the test normalises it first. It decodes to "../../" before
	// the handler sees it, which is exactly the case a defence built on string
	// inspection of the raw query would miss.
	for _, raw := range []string{
		"/logs/api/job?id=..%2F..%2Foutside-the-root",
		"/logs/api/job?id=%2e%2e%2f%2e%2e%2foutside-the-root",
		"/logs/api/job?id=j1%2f..%2f..%2foutside-the-root",
	} {
		rec := logGet(t, l, raw, token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", raw, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "root:x") {
			t.Fatalf("%s read a file outside the jobs root", raw)
		}
	}
}

func TestASymlinkOutOfAJobDirectoryIsNotFollowed(t *testing.T) {
	l, dir, token := logsUnder(t)
	secret := filepath.Join(dir, "outside-the-root")
	if err := os.WriteFile(secret, []byte("root:x:0:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A valid id, a valid file name, and `out` is a link to somewhere else.
	// Anybody with a shell on the desktop can arrange this, and the id check
	// above passes it — this is what makes resolving symlinks BEFORE comparing
	// against the root the load-bearing half of the defence.
	writeJobDir(t, l.jobsRoot, "j2", map[string]string{"cmd": "cat", "rc": "0"})
	if err := os.Symlink(secret, filepath.Join(l.jobsRoot, "j2", "out")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	rec := logGet(t, l, "/logs/api/job?id=j2", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200 — the job exists, only its out file is bogus", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "root:x") {
		t.Fatal("followed a symlink out of the jobs root; resolve BEFORE comparing")
	}
	if body := logJSON(t, rec); body["stdout"] != "" {
		t.Errorf("stdout = %q, want empty — the link was refused, not read", body["stdout"])
	}
}

func TestAJobThatDoesNotExistSaysSo(t *testing.T) {
	l, _, token := logsUnder(t)
	writeJobDir(t, l.jobsRoot, "j1", map[string]string{"cmd": "ls", "rc": "0", "out": "x\n"})

	rec := logGet(t, l, "/logs/api/job?id=j99", token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("answered %d for an unused id, want 404", rec.Code)
	}
	// 404 and not 200-with-empty-output. "This job printed nothing" and "there
	// is no such job" send a reader in opposite directions, and an empty pre
	// block reads as the first.
	if body := logJSON(t, rec); !strings.Contains(body["error"].(string), "j99") {
		t.Errorf("error %q does not name the id that was not found", body["error"])
	}
}

func TestAJobDirectoryWithNoCommandIsNotAJob(t *testing.T) {
	l, _, token := logsUnder(t)
	// A directory that matches the id pattern but has none of a job's markers —
	// a half-created job, or something a person made by hand. Reporting it as a
	// job with an empty command would put a phantom row in the audit.
	if err := os.MkdirAll(filepath.Join(l.jobsRoot, "j7"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := logGet(t, l, "/logs/api/job?id=j7", token); rec.Code != http.StatusNotFound {
		t.Errorf("answered %d for a directory with no cmd file, want 404", rec.Code)
	}
	body := logJSON(t, logGet(t, l, "/logs/api/index", token))
	if jobs := body["jobs"].([]any); len(jobs) != 0 {
		t.Errorf("the index listed %d job(s) from a directory that has none", len(jobs))
	}
}

func TestAbortedBeatsTheExitCode(t *testing.T) {
	l, _, token := logsUnder(t)
	// 143 is what SIGTERM leaves behind and is indistinguishable from a command
	// that chose to exit 143. The marker file is the only thing that can say a
	// person stopped this, so it has to win — otherwise the panic button looks,
	// in the one record anybody reads afterwards, as though it did nothing.
	writeJobDir(t, l.jobsRoot, "j3", map[string]string{
		"cmd": "sleep 900", "rc": "143", "aborted": "ana: panic button", "out": "",
	})
	body := logJSON(t, logGet(t, l, "/logs/api/job?id=j3", token))
	job := body["job"].(map[string]any)
	if job["status"] != "aborted" {
		t.Errorf("status %v, want aborted", job["status"])
	}
	if job["aborted_by"] != "ana: panic button" {
		t.Errorf("aborted_by %v, want the note the abort left", job["aborted_by"])
	}
}

func TestStatusesComeFromTheDirectoryAndNothingElse(t *testing.T) {
	l, _, token := logsUnder(t)
	writeJobDir(t, l.jobsRoot, "j1", map[string]string{"cmd": "true", "rc": "0"})
	writeJobDir(t, l.jobsRoot, "j2", map[string]string{"cmd": "false", "rc": "1"})
	writeJobDir(t, l.jobsRoot, "j3", map[string]string{"cmd": "sleep 60", "pid": "42"})

	want := map[string]string{"j1": "done", "j2": "failed", "j3": "running"}
	body := logJSON(t, logGet(t, l, "/logs/api/index", token))
	jobs := body["jobs"].([]any)
	if len(jobs) != 3 {
		t.Fatalf("listed %d jobs, want 3", len(jobs))
	}
	// Newest first: the id is how a person refers to a job, and a list that
	// buries the one that just started at the bottom is a list nobody scrolls.
	if first := jobs[0].(map[string]any); first["id"] != "j3" {
		t.Errorf("first listed job is %v, want j3 — newest first", first["id"])
	}
	for _, entry := range jobs {
		j := entry.(map[string]any)
		if got := j["status"]; got != want[j["id"].(string)] {
			t.Errorf("job %v: status %v, want %v", j["id"], got, want[j["id"].(string)])
		}
	}
}

func TestBothSidesOfTheDesktopAreReadable(t *testing.T) {
	l, _, token := logsUnder(t)

	// The people's side, exactly as shell-report.sh writes it:
	// time, user, pane, exit code, command.
	shell := "2026-08-09T10:00:00Z\tana\t%1\t0\tsystemctl restart nginx\n" +
		"2026-08-09T10:00:05Z\tana\t%1\t1\tls /nope\n"
	if err := os.WriteFile(l.shellLog, []byte(shell), 0o644); err != nil {
		t.Fatal(err)
	}
	// And the witness, which shares the file's shape but not its columns.
	input := "2026-08-09T10:00:02Z\tana\tclicked\tat 400,300\n"
	if err := os.WriteFile(l.inputLog, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	// The agent's side: the JSONL action trail, with a half-written last line
	// of the sort a live appender leaves behind.
	trail := `{"time":"2026-08-09T10:00:01Z","tool":"job_start","args":"apt update","ok":true}` + "\n" +
		`{"time":"2026-08-09T10:00:03Z","tool":"type_text","ok":false,"denied":"policy"}` + "\n" +
		`{"time":"2026-08-09T10:00:0`
	if err := os.WriteFile(l.actionLog, []byte(trail), 0o644); err != nil {
		t.Fatal(err)
	}

	people := logJSON(t, logGet(t, l, "/logs/api/people", token))
	entries := people["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("people: %d entries, want 3 (two commands and one click)", len(entries))
	}
	// Merged into one timeline, oldest first, so a person reads what happened
	// rather than two files they have to interleave in their head.
	if got := entries[1].(map[string]any)["what"]; got != "clicked" {
		t.Errorf("entries are not in time order: second entry is %v", got)
	}
	first := entries[0].(map[string]any)
	if first["detail"] != "systemctl restart nginx" || first["source"] != "terminal" {
		t.Errorf("first entry = %v, want the shell command verbatim", first)
	}
	if failed := entries[2].(map[string]any); failed["ok"] != false {
		t.Errorf("a command that exited 1 is reported as ok=%v", failed["ok"])
	}

	agent := logJSON(t, logGet(t, l, "/logs/api/agent", token))
	acts := agent["entries"].([]any)
	if len(acts) != 2 {
		t.Fatalf("agent: %d entries, want 2 — the truncated third line must be "+
			"skipped, not fail the whole read", len(acts))
	}
	if refused := acts[1].(map[string]any); !strings.Contains(refused["detail"].(string), "refused: policy") {
		t.Errorf("a refused call reads as %v; a denial is the most interesting row "+
			"in an audit and must survive into the viewer", refused["detail"])
	}

	index := logJSON(t, logGet(t, l, "/logs/api/index", token))
	if index["people_recorded"] != true || index["agent_recorded"] != true {
		t.Errorf("index says people=%v agent=%v; both records exist",
			index["people_recorded"], index["agent_recorded"])
	}
}

func TestAnAbsentRecordIsReportedAsAbsentRatherThanEmpty(t *testing.T) {
	l, _, token := logsUnder(t)

	people := logJSON(t, logGet(t, l, "/logs/api/people", token))
	if people["recorded"] != false {
		t.Error("with no shell.log the people's side must say it is not recorded: " +
			"'nobody typed anything' and 'nothing is being written down' are " +
			"opposite answers and an empty list reads as whichever one the reader " +
			"already believed")
	}

	// ACTION_LOG deliberately emptied — a legal choice that keeps the trail in
	// memory only. The endpoint has to say that, not return silence.
	l.actionLog = ""
	agent := logJSON(t, logGet(t, l, "/logs/api/agent", token))
	if agent["recorded"] != false || !strings.Contains(agent["note"].(string), "ACTION_LOG") {
		t.Errorf("with ACTION_LOG unset the answer was %v, and it must name the "+
			"setting that turned the trail off", agent)
	}
}

func TestTheTailIsBoundedSoOneJobCannotHangTheBrowser(t *testing.T) {
	l, _, token := logsUnder(t)
	var big strings.Builder
	for i := 0; i < 5000; i++ {
		big.WriteString("line\n")
	}
	writeJobDir(t, l.jobsRoot, "j1", map[string]string{
		"cmd": "yes", "rc": "0", "out": big.String(),
	})

	body := logJSON(t, logGet(t, l, "/logs/api/job?id=j1", token))
	if lines := strings.Count(body["stdout"].(string), "\n") + 1; lines != 2000 {
		t.Errorf("stdout came back as %d lines, want the 2000-line default tail", lines)
	}
	// And a caller cannot ask for more than the ceiling.
	body = logJSON(t, logGet(t, l, "/logs/api/job?id=j1&tail=999999", token))
	if tail := body["tail"].(float64); int(tail) != maxLogTail {
		t.Errorf("tail=999999 was honoured as %v, want it clamped to %d", tail, maxLogTail)
	}
}

func TestADeepPathUnderLogsIsNotThePage(t *testing.T) {
	l, _, _ := logsUnder(t)
	// /logs/ catches everything beneath it in Go's mux. Serving the page for
	// /logs/api/whatever would answer a mistyped API call with 200 and HTML,
	// which a fetch reports as a JSON parse error and a reader debugs for an
	// hour.
	for _, target := range []string{"/logs/api", "/logs/anything", "/logs/api/jobs"} {
		if rec := logGet(t, l, target, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", target, rec.Code)
		}
	}
}

func TestWithAuthenticationOffTheDataIsServedTheSameWayTheFilesAre(t *testing.T) {
	// Development mode: AUTH_USER and AUTH_PASS unset. There is no login for
	// this to be weaker than, and refusing here would leave `make up` with a
	// viewer nobody can open. It is the same trade NewFileServer makes.
	dir := t.TempDir()
	jobs := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	l := &LogServer{auth: &Auth{}, jobsRoot: jobs,
		shellLog: filepath.Join(dir, "shell.log"), inputLog: filepath.Join(dir, "input.log")}
	writeJobDir(t, jobs, "j1", map[string]string{"cmd": "ls", "rc": "0", "out": "hello\n"})

	rec := logGet(t, l, "/logs/api/job?id=j1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d with authentication disabled, want 200", rec.Code)
	}
	if body := logJSON(t, rec); body["stdout"] != "hello" {
		t.Errorf("stdout = %v, want hello", body["stdout"])
	}
}
