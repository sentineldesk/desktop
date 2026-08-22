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

package shell

// Sesiones de terminal persistentes.
//
// run_command runs each command in isolation: every call starts from scratch,
// so `cd /tmp` is forgotten and anything that reads from the keyboard hangs.
//
// This opens a REAL shell behind a pseudo-terminal instead. It keeps the working
// directory, the environment and the history, and it can hold a conversation
// with interactive programs — sudo, vim, top, an installer asking yes/no.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// ShellSession is a live shell behind a PTY.
type ShellSession struct {
	ID      string
	User    string // the shell's effective user ("" = the daemon's own)
	cmd     *exec.Cmd
	pty     *os.File
	mu      sync.Mutex
	buf     bytes.Buffer // output collected but not read yet
	started time.Time
	closed  bool
}

// ShellManager owns the open sessions.
type ShellManager struct {
	mu       sync.Mutex
	sessions map[string]*ShellSession
	seq      int
}

func NewShellManager() *ShellManager {
	return &ShellManager{sessions: map[string]*ShellSession{}}
}

// Open starts a new shell. The PTY is given a sensible size so that full-screen
// programs (top, htop, vim) lay themselves out reasonably.
func (m *ShellManager) Open(shell, cwd, user string, cols, rows uint16) (*ShellSession, error) {
	if shell == "" {
		shell = "/bin/bash"
	}
	if cols == 0 {
		cols = 120
	}
	if rows == 0 {
		rows = 34
	}
	var cmd *exec.Cmd
	if user != "" && user != os.Getenv("USER") {
		// A terminal as another user, in practice root. This goes through
		// sudo -u rather than `su`, which would demand a password typed into
		// the PTY before anything useful could happen.
		if !SudoAvailable() {
			return nil, fmt.Errorf("no passwordless sudo: cannot open a shell as %q", user)
		}
		cmd = exec.Command("sudo", "-n", "-E", "-u", user, shell, "-i")
	} else {
		cmd = exec.Command(shell, "-i")
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"PS1=$ ", // short, stable prompt: the output is easier to parse
	)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("sh%d", m.seq)
	s := &ShellSession{ID: id, User: user, cmd: cmd, pty: f, started: time.Now()}
	m.sessions[id] = s
	m.mu.Unlock()

	// Reap it whenever it ends, however it ends.
	//
	// Without this every session ever opened leaves a zombie: Close kills the
	// process and nothing waits on it, so the entry stays in the process table
	// with the daemon as its parent until the daemon itself exits. Measured on
	// a desktop that had been used for a while, seven bash processes were
	// listed and every one of them was in state Zs. A shell the user ends by
	// typing exit leaks the same way, and that path never reaches Close at all,
	// which is why the reaper starts here rather than there.
	go func() {
		_ = cmd.Wait()
	}()

	// Drain the PTY continuously. If nobody reads it the kernel buffer fills
	// and the shell blocks mid-write, which looks exactly like a hang.
	go func() {
		chunk := make([]byte, 8192)
		for {
			n, err := f.Read(chunk)
			if n > 0 {
				s.mu.Lock()
				// Memory ceiling: keep the tail, which is the part anyone reads.
				if s.buf.Len() > 512*1024 {
					tail := s.buf.Bytes()[s.buf.Len()-256*1024:]
					s.buf.Reset()
					s.buf.Write(tail)
				}
				s.buf.Write(chunk[:n])
				s.mu.Unlock()
			}
			if err != nil {
				s.mu.Lock()
				s.closed = true
				s.mu.Unlock()
				return
			}
		}
	}()
	return s, nil
}

func (m *ShellManager) Get(id string) (*ShellSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("no such session %q", id)
	}
	return s, nil
}

func (m *ShellManager) List() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, s := range m.sessions {
		s.mu.Lock()
		out = append(out, map[string]any{
			"id": s.ID, "alive": !s.closed, "user": s.User,
			"seconds": int(time.Since(s.started).Seconds()),
			"pending": s.buf.Len(),
		})
		s.mu.Unlock()
	}
	return out
}

func (m *ShellManager) Close(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such session %q", id)
	}
	s.pty.Close()
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	return nil
}

// Write sends raw text to the shell, without appending a newline.
func (s *ShellSession) Write(text string) error {
	_, err := s.pty.WriteString(text)
	return err
}

// Drain returns everything buffered so far and clears it.
func (s *ShellSession) Drain() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.buf.String()
	s.buf.Reset()
	return out
}

// Run executes a command and waits for the shell to go quiet again.
//
// A PTY offers no clean "the command finished" signal, so this uses the usual
// heuristic: read until no more output arrives for quietMs, or until the overall
// timeout expires. The boolean reports which of the two ended the wait.
func (s *ShellSession) Run(command string, timeoutMs, quietMs int) (string, bool) {
	if timeoutMs <= 0 {
		timeoutMs = 20000
	}
	if quietMs <= 0 {
		quietMs = 400
	}
	s.Drain() // drop what came before, so the output belongs to this command
	s.Write(command + "\n")

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	var acc strings.Builder
	lastData := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(80 * time.Millisecond)
		chunk := s.Drain()
		if chunk != "" {
			acc.WriteString(chunk)
			lastData = time.Now()
			continue
		}
		if acc.Len() > 0 && time.Since(lastData) > time.Duration(quietMs)*time.Millisecond {
			return cleanOutput(acc.String(), command), true
		}
	}
	return cleanOutput(acc.String(), command), false
}

// Terminal escape sequences: colours, cursor movement, window titles and
// bracketed paste. A human never sees them; in plain text they are pure noise
// that makes the output unreadable.
var ansiRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]|\x1b\[[0-9;?]*[ -/]*[@-~]|\r`)

// StripANSI leaves the output looking the way it would on screen.
func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// cleanOutput removes escapes, the echo of the command we just typed, and the
// trailing prompt — none of which are output.
func cleanOutput(out, command string) string {
	out = StripANSI(out)
	lines := strings.Split(out, "\n")
	var keep []string
	for i, ln := range lines {
		if i == 0 && command != "" && strings.Contains(ln, command) {
			continue // the echo of the command we just wrote
		}
		keep = append(keep, ln)
	}
	res := strings.Join(keep, "\n")
	// The prompt left at the end ("$ ", "user@host:~$ ") is not output.
	trimmed := strings.TrimRight(res, " \n\t")
	if idx := strings.LastIndex(trimmed, "\n"); idx >= 0 {
		last := trimmed[idx+1:]
		if strings.HasSuffix(last, "$") || strings.HasSuffix(last, "#") {
			trimmed = trimmed[:idx]
		}
	} else if strings.HasSuffix(trimmed, "$") || strings.HasSuffix(trimmed, "#") {
		trimmed = ""
	}
	return strings.TrimRight(trimmed, " \n\t")
}
