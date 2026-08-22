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

// Jobs: everything the agent runs, run where somebody can stop it.
//
// # Why this replaced a function call
//
// run_command used to be `exec.Command` with a fifteen-second timeout and a
// string buffer. Three properties of that are wrong for a desktop that is being
// shared with people, and they compound:
//
//   - INVISIBLE. Nothing appeared on screen. A person watching the desktop while
//     an agent worked saw an idle wallpaper. They could not judge the work
//     because they could not see it, and a supervisor who cannot observe cannot
//     supervise — the role collapses into trust.
//   - UNSTOPPABLE. There was no handle on it. Wanting it to stop meant killing
//     the agent, or the container, and finding out afterwards what had already
//     happened.
//   - AMNESIAC. The timeout killed the command and threw the partial output
//     away. `curl -O kernel.tar.gz` was not a command that took a while, it was
//     a command that always failed, and the failure looked like a timeout rather
//     than like a design that could not express "this takes four minutes".
//
// A job fixes all three with the same move: the command runs in a tmux window on
// the shared screen, its streams go to files, and it has an id. Visible because
// it is on screen. Stoppable because the id reaches a pid. Patient because
// nothing kills it when a reader stops waiting — job_wait timing out returns
// what there is so far and leaves the work running, which is the difference
// between a slow answer and a lost one.
//
// # The rule this enforces
//
// The agent has exactly one way to affect anything: this MCP interface. It runs
// on the operator's machine, the desktop runs in a container, and between them
// is a socket that speaks a fixed catalogue. A skill can tell the model to run
// something on the host; the model has nothing to run it with. That containment
// is the product, and this file is where the last hole in it was closed —
// commands were already confined to the container, but confined is not the same
// as witnessed, and a cage nobody is looking into is just a smaller room.
//
// # Where the state lives
//
// On disk, under /tmp/sentineldesk/jobs/<id>/, not in a map in this process.
// Deliberately: a person can `cat` it, the daemon can restart without orphaning
// work it can no longer describe, and the record of what an agent did outlives
// the connection that asked for it. The map would have been faster and would
// have made every one of those false.
package mcp

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// jobsRoot is under /tmp on purpose. These are transcripts of work, not the
// work itself: a job that produced something durable wrote it where it was told
// to, and the record of HOW is worth keeping until the desktop stops, not beyond
// it. Somewhere permanent would grow without limit and would eventually hold a
// year of somebody's shell history nobody chose to keep.
//
// A var rather than a const only so the tests can point it somewhere else. The
// alternative was tests that write into the real directory and read back
// whatever a previous run left behind, which is how a test suite starts
// depending on the order it runs in.
var jobsRoot = "/tmp/sentineldesk/jobs"

const (
	jobRunner = "/usr/local/bin/job-run.sh"

	// jobWindowPrefix names the tmux windows. A hyphen, not a colon: tmux reads
	// a colon as the session:window separator, so `job:j3` could not be targeted
	// by name at all.
	jobWindowPrefix = "job-"

	// keepDeadWindows is how many finished jobs stay on screen. The pane is kept
	// after the command exits (remain-on-exit) so a person who looked away can
	// still read what happened, which is worth more than a tidy window list —
	// but only up to a point, and past it the terminal becomes unusable for the
	// human whose desktop it is.
	keepDeadWindows = 5
)

// jobStatus is what a reader needs to know before reading anything else.
type jobStatus string

const (
	jobRunning jobStatus = "running"
	jobDone    jobStatus = "done"
	jobFailed  jobStatus = "failed"
	jobAborted jobStatus = "aborted"
)

// jobRecord is one job, reconstructed from its directory.
type jobRecord struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Status    jobStatus `json:"status"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Started   string    `json:"started,omitempty"`
	Ended     string    `json:"ended,omitempty"`
	AbortedBy string    `json:"aborted_by,omitempty"`
	Window    string    `json:"window,omitempty"`
	PID       int       `json:"pid,omitempty"`
	Dir       string    `json:"dir"`
}

// jobs is the small amount of state that genuinely cannot live on disk: the
// lock that keeps two concurrent starts from claiming the same id, and the note
// left behind by an abort for the agent to trip over.
type jobs struct {
	mu sync.Mutex

	// abortNote is a one-shot message delivered to the agent on its next call,
	// whatever that call is.
	//
	// One-shot and out-of-band because of what an abort MEANS. A person pressed
	// a panic button: they have decided that what is happening is wrong. The
	// agent is mid-plan, and its plan is now built on a premise that stopped
	// being true — so the useful thing is not to refuse the current call on its
	// own merits, it is to interrupt. Anything else and the model carries on
	// from step four of a plan whose step three was killed by a human.
	//
	// It clears after one delivery. A permanent flag would be a second control
	// mechanism sitting beside room arbitration and disagreeing with it, and
	// there is already exactly one answer to "may the agent act": who holds the
	// controls. The abort takes those away; this note only explains why.
	abortNote string
}

func jobDir(id string) string { return filepath.Join(jobsRoot, id) }

// shQuote wraps a string so a shell sees it as one word.
//
// Needed only for as_root, where the command has to survive being handed to
// sudo. Everywhere else the command reaches bash as a single argv element and
// never passes through a second shell, which is why job-run.sh takes it as an
// argument instead of interpolating it.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// nextJobID allocates the next free id by looking at what is already there.
//
// Reading the directory rather than keeping a counter means a restarted daemon
// does not reuse j3 for a second job while the first one's transcript is still
// sitting there — the id is how a person refers to a job in conversation, and
// two different jobs answering to the same name is the kind of confusion that
// makes an audit trail worthless.
func (s *Server) nextJobID() string {
	entries, _ := os.ReadDir(jobsRoot)
	highest := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "j") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "j")); err == nil && n > highest {
			highest = n
		}
	}
	return "j" + strconv.Itoa(highest+1)
}

// readJob rebuilds a record from its directory.
func readJob(id string) (jobRecord, error) {
	dir := jobDir(id)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return jobRecord{}, fmt.Errorf("no job called %q", id)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	rec := jobRecord{
		ID: id, Dir: dir,
		Command:   read("cmd"),
		Started:   read("started"),
		Ended:     read("ended"),
		AbortedBy: read("aborted"),
		Window:    read("window"),
	}
	rec.PID, _ = strconv.Atoi(read("pid"))

	rcText := read("rc")
	switch {
	case rec.AbortedBy != "":
		// Aborted wins over the exit code, and that ordering is the point. A
		// command killed by SIGTERM exits 143, which is indistinguishable from a
		// command that chose to exit 143 — and the difference between "this
		// failed" and "a person stopped this" is the whole reason the button
		// exists. The marker file is written before the signal, so the record
		// cannot lose the distinction even if the kill races the exit.
		rec.Status = jobAborted
		if rcText != "" {
			if code, err := strconv.Atoi(rcText); err == nil {
				rec.ExitCode = &code
			}
		}
	case rcText == "":
		rec.Status = jobRunning
	default:
		code, err := strconv.Atoi(rcText)
		if err != nil {
			return rec, fmt.Errorf("job %s left an unreadable exit code %q", id, rcText)
		}
		rec.ExitCode = &code
		if code == 0 {
			rec.Status = jobDone
		} else {
			rec.Status = jobFailed
		}
	}
	return rec, nil
}

// jobOutput reads one of the two streams, optionally just the end of it.
func jobOutput(id, stream string, tailLines int) (string, error) {
	name := "out"
	if stream == "err" {
		name = "err"
	}
	b, err := os.ReadFile(filepath.Join(jobDir(id), name))
	if err != nil {
		return "", fmt.Errorf("job %s has no %s stream on disk: %v", id, name, err)
	}
	text := strings.TrimRight(string(b), "\n")
	if tailLines > 0 {
		text = lastLines(text, tailLines)
	}
	return text, nil
}

// listJobs returns every job still on disk, newest first.
func listJobs() []jobRecord {
	entries, _ := os.ReadDir(jobsRoot)
	var out []jobRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if rec, err := readJob(e.Name()); err == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(out[i].ID, "j"))
		b, _ := strconv.Atoi(strings.TrimPrefix(out[j].ID, "j"))
		return a > b
	})
	return out
}

// startJob puts a command on the shared screen and returns its id.
//
// FOLLOW-UP, named rather than silently walked past. ensureVisibleSession now
// guarantees an X window that is mapped on the desktop the room is watching, and
// that closes every way an X window can be where nobody is looking. It does not close
// the multiplexer's own version of the same question: tmux draws ONE of its
// windows at a time, and a job created with -d is not the one being drawn. The
// job is on the shared screen in the sense that matters for stopping it — the
// window list names it, a person is one keystroke from it, and job_output has the
// text — but a person watching the terminal sees the shell, not the job. Fixing
// that properly means either a split pane (which takes the person's space
// permanently) or switching windows (which takes their screen, the very thing -d
// exists to stop), so it is a design decision and not a patch.
func (s *Server) startJob(ctx context.Context, command string, asRoot bool) (jobRecord, error) {
	if strings.TrimSpace(command) == "" {
		return jobRecord{}, fmt.Errorf("no command")
	}
	if err := os.MkdirAll(jobsRoot, 0o755); err != nil {
		return jobRecord{}, fmt.Errorf("could not prepare the job directory: %v", err)
	}
	if err := s.ensureVisibleSession(ctx); err != nil {
		return jobRecord{}, err
	}

	s.jobs.mu.Lock()
	id := s.nextJobID()
	// Claim the id before releasing the lock. nextJobID reads the directory, so
	// two starts racing between the read and the window opening would both see
	// the same highest and both return j7.
	if err := os.MkdirAll(jobDir(id), 0o755); err != nil {
		s.jobs.mu.Unlock()
		return jobRecord{}, fmt.Errorf("could not prepare job %s: %v", id, err)
	}
	s.jobs.mu.Unlock()

	// Secrets become shell variables here, and their values reach the process
	// through a file only it can read — never through the command text.
	//
	// The obvious alternatives both leak. Interpolating the value into the
	// command puts it in the tmux pane on the SHARED SCREEN, in the cmd file
	// below and in the desktop's history. Passing it with `tmux new-window -e
	// NAME=value` puts it in the argv of the tmux process, where `ps aux` shows
	// it to every user in the container. A 0600 file that the runner sources and
	// deletes is the only one of the three where the value is readable by
	// exactly the process that needs it.
	launch, env, missing := s.resolveSecrets(command)
	if len(missing) > 0 {
		if err := s.askForSecrets(missing, trimCommand(command)); err != nil {
			return jobRecord{}, err
		}
		// Resolve again now that somebody has typed them.
		launch, env, missing = s.resolveSecrets(command)
		if len(missing) > 0 {
			return jobRecord{}, fmt.Errorf("still missing %s", strings.Join(missing, ", "))
		}
	}
	if len(env) > 0 {
		envPath := filepath.Join(jobDir(id), "env")
		body := ""
		for _, kv := range env {
			name, value, _ := strings.Cut(kv, "=")
			body += "export " + name + "=" + shQuote(value) + "\n"
		}
		if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
			return jobRecord{}, fmt.Errorf("could not stage the secrets for job %s: %v", id, err)
		}
	}
	if asRoot {
		launch = "sudo -n bash -c " + shQuote(launch)
	}

	// -P -F prints the new window's id, which is the handle abort needs when the
	// pid alone is not enough — a pipeline leaves bash alive holding children,
	// and killing the window SIGHUPs the lot.
	// The window is named job-<id>, with a hyphen. A colon reads as a
	// session:window separator to tmux, so `job:j3` made every name-based
	// command ambiguous — including the ones a PERSON types when they want to
	// look at what the agent is doing, which is the audience this whole window
	// exists for.
	//
	// -d, so the job does not become the current window. Without it tmux
	// switches to whatever was created last, and two things followed. The agent
	// lost its shell: terminal_run resolved "the active pane" and got the JOB's
	// pane, whose stdin belongs to job-run.sh and is not being read — send-keys
	// returned success and the command was never run by anything. And the
	// PERSON lost their screen: an agent starting three jobs yanked them away
	// from whatever they were typing, three times. The job is still on the
	// shared screen, still one keystroke away in the window list, and still
	// announced in the return value; what it no longer does is grab.
	//
	// pickTerminalWindow refuses to type into a job window regardless, so this
	// is the second of two locks on the same door. That is intentional: -d fixes
	// where the eye goes, the name check fixes where the keys go, and a future
	// change to either one should not be able to quietly reopen the other.
	win, err := s.tmux("new-window", "-d", "-t", tmuxSession, "-n", jobWindowPrefix+id,
		"-P", "-F", "#{window_id}", "--", jobRunner, id, launch)
	if err != nil {
		return jobRecord{}, fmt.Errorf("could not open a window for job %s: %v", id, err)
	}
	win = strings.TrimSpace(win)
	_ = os.WriteFile(filepath.Join(jobDir(id), "window"), []byte(win), 0o644)

	// remain-on-exit is set by job-run.sh from inside the pane, not here. Two
	// calls leave a gap, and everything that finishes inside that gap — which is
	// most commands — closed its window before the option landed.

	s.reapDeadJobWindows()

	// The record keeps the REFERENCE form, not what was typed and not what ran.
	// It is the version that is safe on a shared screen, safe in the history and
	// still exactly readable — {{secret:db_root}} says what happened.
	return jobRecord{ID: id, Command: command, Status: jobRunning,
		Window: win, Dir: jobDir(id)}, nil
}

// ensureVisibleSession guarantees there is a tmux session AND that a terminal
// window is genuinely showing it.
//
// Both halves matter and only one of them is obvious. A session with no client
// attached still runs commands perfectly — and that is precisely the failure
// this file exists to prevent, so "the session is alive" is not the condition to
// check. It is the condition that made terminal_run silently invisible once
// before, when the session outlived the window that had been showing it.
//
// The second half is not "a client is attached" either, which is what it used to
// mean and is a weaker claim than it sounds: a client is a pty, and a pty can
// belong to a minimised window, a window on another desktop, a window somebody
// dragged off the screen, or a `tmux attach` typed into `docker exec` from a
// shell on the far side of the container with no window anywhere. Each of those
// satisfied the old test and each of them is a job running where nobody can see
// it. sessionOnScreen asks X instead.
//
// If X cannot answer at all, this refuses. That is the deliberate choice and it
// only goes one way: a job that does not start is a missing feature, and a job
// that runs unwatched while the daemon reports it as visible is the silent
// failure this whole file exists to prevent. CLAUDE.md's degradation rule — an
// optional capability that is missing disables a feature rather than failing —
// is honoured by DISABLING job_start, not by assuming the answer it could not
// get. In practice the two cases coincide anyway: no X means no lxterminal, no
// desktop and nothing for anybody to watch.
//
// The repair is no longer only "open another window and hope it lands somewhere
// useful". attachWindow pins the window it opens to the desktop the room is
// watching, un-shades it and raises it, and then refuses if X does not confirm
// it landed there — so a job is not started merely because a terminal exists
// somewhere, but because one was PUT where everybody is looking. A window
// manager that will not move it is a job that does not start, with an error that
// names the desktop it is stuck on.
//
// What this still cannot promise is which tmux window the client is displaying.
// The job's pane is created with -d, so it deliberately does not take the screen
// from whoever is typing, and it is one keystroke away in the window list rather
// than in front of them. That is a tmux question and not an X one; see the
// follow-up noted above startJob.
func (s *Server) ensureVisibleSession(ctx context.Context) error {
	if !s.sessionAlive() {
		if _, err := s.tmux("new-session", "-d", "-s", tmuxSession); err != nil {
			return fmt.Errorf("could not start the terminal session: %v", err)
		}
	}
	if state, why := s.sessionOnScreen(); state != screenShowing {
		if err := s.attachWindow(ctx); err != nil {
			return fmt.Errorf("nothing on this desktop is showing the terminal (%s) "+
				"and one could not be opened, so the job was NOT started: %v", why, err)
		}
	}
	return nil
}

// reapDeadJobWindows closes the oldest finished job panes.
func (s *Server) reapDeadJobWindows() {
	wins, err := s.sessionWindows()
	if err != nil {
		return
	}
	var dead []string
	for _, w := range wins {
		if strings.HasPrefix(w.Name, jobWindowPrefix) && w.Dead {
			dead = append(dead, w.ID)
		}
	}
	// list-windows is ordered by index, so the front of the slice is the oldest.
	for i := 0; i+keepDeadWindows < len(dead); i++ {
		_, _ = s.tmux("kill-window", "-t", dead[i])
	}
}

// waitForJob blocks until the job ends or the wait times out.
//
// A timeout here does NOT stop the job, and that is the single most important
// property of this function. The caller asked how long to WATCH, not how long to
// ALLOW — those were the same question while a command was a blocking call with
// a deadline, and conflating them meant a four-minute download could not be
// expressed at all. Now a reader that gives up gets a partial answer and the
// work carries on.
func (s *Server) waitForJob(ctx context.Context, id string, timeout time.Duration) (jobRecord, bool) {
	deadline := time.Now().Add(timeout)
	for {
		rec, err := readJob(id)
		if err != nil {
			return jobRecord{ID: id}, false
		}
		if rec.Status != jobRunning {
			return rec, true
		}
		if !time.Now().Before(deadline) {
			return rec, false
		}
		if !sleepCtx(ctx, 100*time.Millisecond) {
			return rec, false
		}
	}
}

// abortJob stops one job and records who stopped it.
func (s *Server) abortJob(id, who, reason string) (jobRecord, error) {
	rec, err := readJob(id)
	if err != nil {
		return jobRecord{}, err
	}
	if rec.Status != jobRunning {
		return rec, fmt.Errorf("job %s already finished (%s)", id, rec.Status)
	}
	if who == "" {
		who = "someone"
	}
	note := who
	if reason != "" {
		note += ": " + reason
	}
	// The marker goes down BEFORE the signal. If the process happens to exit on
	// its own in the same millisecond, the record still says a person meant to
	// stop it — which is the fact somebody will want six months later, and the
	// exit code cannot carry it.
	_ = os.WriteFile(filepath.Join(jobDir(id), "aborted"), []byte(note), 0o644)

	if rec.PID > 0 {
		_, _ = s.output("kill", "-TERM", strconv.Itoa(rec.PID))
	}
	// Escalate in the background: a command deserves the chance to clean up
	// after itself, and a supervisor pressing a panic button deserves not to
	// wait for one that will not.
	go func(rec jobRecord) {
		time.Sleep(3 * time.Second)
		if cur, err := readJob(rec.ID); err == nil && cur.ExitCode == nil {
			if rec.Window != "" {
				_, _ = s.tmux("kill-window", "-t", rec.Window)
			}
			if rec.PID > 0 {
				_, _ = s.output("kill", "-KILL", strconv.Itoa(rec.PID))
			}
		}
	}(rec)

	rec.Status = jobAborted
	rec.AbortedBy = note
	return rec, nil
}

// AbortAll stops every running job and leaves a note for the agent.
//
// This is the panic button, reached from the browser through the room. It is
// exported because it is called from the other control plane — the one humans
// are on — and that direction of travel is new: everything else in this package
// is the agent asking the desktop for something.
func (s *Server) AbortAll(who string) int {
	if who == "" {
		who = "a person watching"
	}
	stopped := 0
	var names []string
	for _, rec := range listJobs() {
		if rec.Status != jobRunning {
			continue
		}
		if _, err := s.abortJob(rec.ID, who, "panic button"); err == nil {
			stopped++
			names = append(names, rec.ID+" ("+trimCommand(rec.Command)+")")
		}
	}

	s.jobs.mu.Lock()
	s.jobs.abortNote = buildAbortNote(who, names)
	s.jobs.mu.Unlock()
	return stopped
}

// takeAbortNote returns the pending abort message and clears it.
func (s *Server) takeAbortNote() string {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	note := s.jobs.abortNote
	s.jobs.abortNote = ""
	return note
}

// buildAbortNote is what the agent reads at the moment it is interrupted.
//
// Written as instructions rather than as a status line because of what the model
// will otherwise do with it. A bare "aborted" reads as a failure, and a failure
// is something to retry — so the most likely next action after a person stops
// the agent is the agent doing it again. It has to be told, in the same breath,
// that this was a decision by a human, that retrying is the wrong response, and
// that the person may have changed things in the meantime that it cannot see
// without looking.
func buildAbortNote(who string, stopped []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "INTERRUPTED. %s pressed abort on this desktop.\n\n", who)
	if len(stopped) > 0 {
		fmt.Fprintf(&b, "Stopped mid-run: %s\n\n", strings.Join(stopped, ", "))
	}
	b.WriteString(
		"This was a decision, not a fault. Do NOT retry what you were doing and do " +
			"NOT start something else: whatever you were in the middle of, a person " +
			"watching judged it wrong enough to stop it, and they were in a better " +
			"position to judge it than you are.\n\n" +
			"The controls have been taken from you. They may also have done things " +
			"themselves while you were stopped, so anything you believed about the " +
			"state of this desktop is now a guess. Before acting again:\n" +
			"  1. call `activity` — it is one timeline of what you did AND what the " +
			"people here typed, which is the only way to find out what changed while " +
			"you were stopped. Then job_output on the jobs above for the detail,\n" +
			"  2. say what you understand and what you got wrong,\n" +
			"  3. wait to be told to continue. Ask with ask_human if nobody says.\n\n" +
			"Do not request control back until somebody has answered.")
	return b.String()
}

// logsBase is the origin a PERSON would type to reach this desktop.
//
// PUBLIC_URL when it is set, and it should be set anywhere the desktop is
// reached by a name — a reverse proxy, a tunnel, a hostname on a LAN. The daemon
// cannot work this out for itself: it knows it is bound to a port, and nothing
// about the hostname, the scheme somebody else is terminating, or the path a
// proxy mounted it under.
//
// The fallback is localhost with the port the daemon is listening on, which is
// exactly right for `make up` on the machine that ran it and exactly wrong for
// anybody else. That is the better failure of the two available: a link to
// localhost is obviously local and gets corrected, whereas guessing a hostname
// from a Host header the agent never sees would produce a link that looks right
// and resolves nowhere.
func (s *Server) logsBase() string {
	if u := strings.TrimRight(s.cfg.PublicURL, "/"); u != "" {
		return u
	}
	scheme := "http"
	if s.cfg.TLSCert != "" || s.cfg.TLSSelfSigned {
		scheme = "https"
	}
	host := s.cfg.HTTPAddr
	// An empty bind address means every interface, and 0.0.0.0 says the same
	// thing out loud. Neither is a destination anybody can open.
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "localhost"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, s.cfg.HTTPPort)
}

// jobLogURL is the link a tool result carries: where THIS job's output can be
// read, by a person, in a browser.
//
// It carries a job id and nothing else. That is deliberate and it is the whole
// reason the viewer authenticates the way it does — this string is handed to a
// model, which repeats it to a person, who may paste it into a chat, a ticket or
// a screenshot. A one-use ticket or a session token in the query would have made
// the link work without a login and made every copy of it a credential.
func (s *Server) jobLogURL(id string) string {
	return s.logsBase() + "/logs?job=" + url.QueryEscape(id)
}

// allLogsURL is the same page with nothing selected: every job, the agent's tool
// calls, and the commands the people here typed.
//
// Both halves are reachable from one link on purpose. The desktop's rule is that
// neither party acts unseen — the agent's commands run in a window on the shared
// screen, and a person's commands are reported by their own shell — and a viewer
// that only showed the agent's side would quietly restate the assumption that
// the machine is what needs watching.
func (s *Server) allLogsURL() string {
	return s.logsBase() + "/logs"
}

// withLogLinks adds the two links to a job tool's result.
//
// Every job-shaped result goes through here rather than each dispatcher
// building its own map entries, because the requirement is that the link is
// ALWAYS there. A link that appears on job_start and not on job_wait is a link
// the model mentions once, at the moment there is nothing to read yet, and never
// again at the moment somebody wants it.
func (s *Server) withLogLinks(out map[string]any, id string) map[string]any {
	if id != "" {
		out["logs_url"] = s.jobLogURL(id)
	}
	out["all_logs_url"] = s.allLogsURL()
	out["logs_note"] = "give logs_url to the people you are working with — it is " +
		"where they read this job's output in a browser, without a shell in the " +
		"container. all_logs_url is the same page showing everything: your jobs, " +
		"your tool calls, and the commands they typed themselves."
	return out
}

func trimCommand(cmd string) string {
	cmd = strings.TrimSpace(strings.ReplaceAll(cmd, "\n", " "))
	if len(cmd) > 60 {
		return cmd[:57] + "..."
	}
	return cmd
}

func (s *Server) buildJobTools() []toolDef {
	return []toolDef{
		{
			Name:            "job_start",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description: "Start a shell command in the background, in a terminal window " +
				"on the shared screen, and return its job id immediately. Use this for " +
				"anything that takes more than a few seconds — downloads, builds, " +
				"installs, long transfers — and then job_wait or job_status. The command " +
				"is NOT killed when you stop waiting for it. Its stdout and stderr are " +
				"kept apart on disk and shown live to everyone watching, who can stop it " +
				"at any moment. The result carries logs_url — a link to this job's " +
				"output in a browser. Give that link to the people you are working " +
				"with whenever you tell them you started something.",
			InputSchema: schema(map[string]any{
				"command": pStr("shell command"),
				"as_root": pBool("run through passwordless sudo (default false)"),
			}, "command"),
		},
		{
			Name: "job_status",
			Risk: riskRead,
			Description: "How a job is doing: running, done, failed or aborted, with its " +
				"exit code once it has one and who stopped it if somebody did.",
			InputSchema: schema(map[string]any{"id": pStr("job id, e.g. j3")}, "id"),
		},
		{
			Name: "job_output",
			Risk: riskRead,
			Description: "Read a job's output. stream: out (default), err, or both — they " +
				"are kept separate, so read err when you are working out why something " +
				"failed. Works while the job is still running.",
			InputSchema: schema(map[string]any{
				"id":         pStr("job id"),
				"stream":     pStr("out | err | both (default out)"),
				"tail_lines": pInt("only the last N lines (default all)"),
			}, "id"),
		},
		{
			Name: "job_wait",
			Risk: riskRead,
			Description: "Wait for a job to finish and return its status and output. If it " +
				"has not finished by timeout_ms this returns what there is so far and " +
				"says so — THE JOB KEEPS RUNNING. Call it again rather than restarting " +
				"the work.",
			InputSchema: schema(map[string]any{
				"id":         pStr("job id"),
				"timeout_ms": pIntDef("how long to watch for (default 60000)", 60000),
			}, "id"),
		},
		{
			Name:       "job_abort",
			Visibility: visVisible,
			Risk:       riskWrite,
			Description: "Stop a running job. It is signalled first and killed if it does " +
				"not go. Use it on your own work when you realise it was wrong — do not " +
				"leave something running that you no longer want the answer to.",
			InputSchema: schema(map[string]any{
				"id":     pStr("job id"),
				"reason": pStr("why, in a few words"),
			}, "id"),
		},
		{
			Name: "job_list",
			Risk: riskRead,
			Description: "Every job on this desktop, newest first, with status, command " +
				"and a logs_url for each. Includes jobs somebody else started and jobs " +
				"that finished earlier. all_logs_url in the result is the page a person " +
				"opens to read all of it, including the commands they typed themselves.",
			InputSchema: schema(map[string]any{}),
		},
	}
}

func (s *Server) dispatchJobs(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	case "job_start":
		asRoot, _ := args["as_root"].(bool)
		rec, err := s.startJob(ctx, argStr(args, "command"), asRoot)
		if err != nil {
			return textContent("%v", err), true, true
		}
		return jsonContent(s.withLogLinks(map[string]any{
			"job_id": rec.ID, "status": rec.Status, "window": rec.Window,
			"note": "running on the shared screen. job_wait to watch it, job_status " +
				"to check without waiting. It keeps running either way.",
		}, rec.ID)), false, true

	case "job_status":
		rec, err := readJob(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		// The record is a struct everywhere else it is used, so it is widened
		// into a map here rather than growing two link fields that would then
		// be written into every job directory's worth of code that builds one.
		return jsonContent(s.withLogLinks(map[string]any{
			"id": rec.ID, "command": rec.Command, "status": rec.Status,
			"exit_code": rec.ExitCode, "started": rec.Started, "ended": rec.Ended,
			"aborted_by": rec.AbortedBy, "window": rec.Window, "pid": rec.PID,
			"dir": rec.Dir,
		}, rec.ID)), false, true

	case "job_output":
		id := argStr(args, "id")
		tail := argInt(args, "tail_lines")
		stream := argStr(args, "stream")
		if stream == "both" {
			stdout, err := jobOutput(id, "out", tail)
			if err != nil {
				return textContent("%v", err), true, true
			}
			stderr, _ := jobOutput(id, "err", tail)
			return jsonContent(s.withLogLinks(map[string]any{
				"stdout": stdout, "stderr": stderr}, id)), false, true
		}
		text, err := jobOutput(id, stream, tail)
		if err != nil {
			return textContent("%v", err), true, true
		}
		// This used to be bare text. It is JSON now for one reason: bare text
		// has nowhere to put the link, and a result that carries the link
		// everywhere except the tool whose entire job is reading output is the
		// one place it was most needed.
		if stream == "" {
			stream = "out"
		}
		return jsonContent(s.withLogLinks(map[string]any{
			"stream": stream, "text": text}, id)), false, true

	case "job_wait":
		id := argStr(args, "id")
		timeout := argInt(args, "timeout_ms")
		if timeout <= 0 {
			timeout = 60000
		}
		rec, finished := s.waitForJob(ctx, id, time.Duration(timeout)*time.Millisecond)
		stdout, _ := jobOutput(id, "out", 0)
		stderr, _ := jobOutput(id, "err", 0)
		out := map[string]any{
			"job_id": id, "status": rec.Status, "finished": finished,
			"stdout": stdout, "stderr": stderr,
		}
		if rec.ExitCode != nil {
			out["exit_code"] = *rec.ExitCode
		}
		if rec.AbortedBy != "" {
			out["aborted_by"] = rec.AbortedBy
		}
		if !finished {
			out["note"] = "still running — this is everything so far, not the end. " +
				"Call job_wait again; do not start the work over."
		}
		return jsonContent(s.withLogLinks(out, id)), false, true

	case "job_abort":
		rec, err := s.abortJob(argStr(args, "id"), s.agentName, argStr(args, "reason"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		return jsonContent(s.withLogLinks(map[string]any{
			"job_id": rec.ID, "status": rec.Status}, rec.ID)), false, true

	case "job_list":
		// Per-job links as well as the page, so the model can hand somebody the
		// one job they asked about instead of a list to search through.
		records := listJobs()
		listed := make([]map[string]any, 0, len(records))
		for _, rec := range records {
			listed = append(listed, map[string]any{
				"id": rec.ID, "command": rec.Command, "status": rec.Status,
				"exit_code": rec.ExitCode, "aborted_by": rec.AbortedBy,
				"logs_url": s.jobLogURL(rec.ID),
			})
		}
		return jsonContent(s.withLogLinks(map[string]any{"jobs": listed}, "")), false, true
	}
	return nil, false, false
}
