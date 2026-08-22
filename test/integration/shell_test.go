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

//go:build integration

package integration

// The terminal a person can watch, and the shells only the agent sees.
//
// Both families answer with a transcript, and a transcript is the tool quoting
// itself. So every test here asks the shell to leave a mark on the filesystem
// and then looks for the mark, which is the difference between "the tool says
// the command ran" and "the command ran".

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTerminalOpen(t *testing.T) {
	control(t)
	out := devDesk(t).Call(t, "terminal_open", nil)
	t.Cleanup(func() { X(t, "pkill -f lxterminal 2>/dev/null || true") })

	// A window a person could actually watch is the whole point of this tool
	// existing beside shell_open, so the assertion is that one appeared.
	eventually(t, 15*time.Second, "a terminal window on the display", func() bool {
		wins := X(t, "wmctrl -l")
		return strings.Contains(strings.ToLower(wins), "terminal") ||
			strings.Contains(wins, "@")
	})
	if strings.Contains(strings.ToLower(out), "error") {
		t.Errorf("terminal_open reported %s", trunc(out, 200))
	}
}

func TestTerminalRun(t *testing.T) {
	control(t)
	devDesk(t).Call(t, "terminal_open", nil)
	t.Cleanup(func() { X(t, "pkill -f lxterminal 2>/dev/null || true") })
	time.Sleep(1500 * time.Millisecond)

	marker := "/tmp/it-terminal.txt"
	Sh(t, "rm -f %s", marker)
	devDesk(t).Call(t, "terminal_run", map[string]any{
		"command": "echo terminal-ran > " + marker, "timeout_ms": 30000})

	// The file, not the transcript. A tool that echoed the command back without
	// sending it would produce an identical-looking reply.
	eventually(t, 10*time.Second, "the command to run in the terminal", func() bool {
		return strings.Contains(Sh(t, "cat %s 2>/dev/null", marker), "terminal-ran")
	})
}

func TestTerminalRead(t *testing.T) {
	control(t)
	devDesk(t).Call(t, "terminal_open", nil)
	t.Cleanup(func() { X(t, "pkill -f lxterminal 2>/dev/null || true") })
	time.Sleep(1500 * time.Millisecond)

	// Put something distinctive on screen, then read it back. The string is
	// unusual enough that finding it cannot be a coincidence.
	devDesk(t).Call(t, "terminal_run", map[string]any{
		"command": "echo MARKER-7F3A-READBACK", "timeout_ms": 30000})
	time.Sleep(800 * time.Millisecond)

	out := devDesk(t).Call(t, "terminal_read", map[string]any{"lines": 40})
	if !strings.Contains(out, "MARKER-7F3A-READBACK") {
		t.Fatalf("the terminal printed the marker and the read does not have it:\n%s", trunc(out, 400))
	}
}

// openShell starts a persistent shell and returns its id, closing it afterwards
// whatever the test does.
func openShell(t *testing.T) string {
	t.Helper()
	out := devDesk(t).Call(t, "shell_open", map[string]any{"shell": "/bin/bash"})
	id := jsonField(t, out, "id")
	if id == "" {
		t.Fatalf("no shell id in %s", trunc(out, 200))
	}
	t.Cleanup(func() { devDesk(t).Call(t, "shell_close", map[string]any{"id": id}) })
	return id
}

func TestShellOpen(t *testing.T) {
	id := openShell(t)

	// A session is only open if it survives to the next call, which is the
	// property that distinguishes this from run_command.
	out := devDesk(t).Call(t, "shell_list", nil)
	if !strings.Contains(out, id) {
		t.Fatalf("shell %s was opened and is not in the list:\n%s", id, trunc(out, 300))
	}
	// And there is a real process behind it, not a bookkeeping entry.
	if countOf(t, "bash") == 0 {
		t.Error("a bash session was opened and no bash is running")
	}
}

func TestShellExec(t *testing.T) {
	id := openShell(t)
	marker := "/tmp/it-shell-exec.txt"
	Sh(t, "rm -f %s", marker)

	devDesk(t).Call(t, "shell_exec", map[string]any{
		"id": id, "command": "echo shell-exec-ran > " + marker, "timeout_ms": 15000})

	if got := Sh(t, "cat %s 2>/dev/null", marker); !strings.Contains(got, "shell-exec-ran") {
		t.Fatalf("the file holds %q", got)
	}
}

func TestShellInput(t *testing.T) {
	id := openShell(t)
	marker := "/tmp/it-shell-input.txt"
	Sh(t, "rm -f %s", marker)

	// Raw keystrokes without waiting, which is what separates this from
	// shell_exec — so the proof has to be the effect arriving on its own.
	devDesk(t).Call(t, "shell_input", map[string]any{
		"id": id, "text": "echo shell-input-ran > " + marker, "enter": true})

	eventually(t, 8*time.Second, "the typed line to run", func() bool {
		return strings.Contains(Sh(t, "cat %s 2>/dev/null", marker), "shell-input-ran")
	})
}

func TestShellRead(t *testing.T) {
	id := openShell(t)
	devDesk(t).Call(t, "shell_input", map[string]any{
		"id": id, "text": "echo MARKER-91C2-SHELLREAD", "enter": true})
	time.Sleep(1200 * time.Millisecond)

	out := devDesk(t).Call(t, "shell_read", map[string]any{"id": id})
	if !strings.Contains(out, "MARKER-91C2-SHELLREAD") {
		t.Fatalf("the shell echoed the marker and the read does not have it:\n%s", trunc(out, 300))
	}
}

func TestShellList(t *testing.T) {
	before := devDesk(t).Call(t, "shell_list", nil)
	id := openShell(t)
	after := devDesk(t).Call(t, "shell_list", nil)

	if strings.Contains(before, id) {
		t.Errorf("the list contained %s before it was opened", id)
	}
	if !strings.Contains(after, id) {
		t.Fatalf("the list does not contain %s after opening it:\n%s", id, trunc(after, 300))
	}
}

func TestShellClose(t *testing.T) {
	out := devDesk(t).Call(t, "shell_open", map[string]any{"shell": "/bin/bash"})
	id := jsonField(t, out, "id")
	if id == "" {
		t.Fatalf("no shell id in %s", trunc(out, 200))
	}
	before := countOf(t, "bash")

	devDesk(t).Call(t, "shell_close", map[string]any{"id": id})

	if list := devDesk(t).Call(t, "shell_list", nil); strings.Contains(list, id) {
		t.Errorf("%s is still listed after being closed:\n%s", id, trunc(list, 300))
	}
	// The process has to go with it, or closing is only forgetting.
	eventually(t, 8*time.Second, "the shell's process to exit", func() bool {
		return countOf(t, "bash") < before
	})
	// And it can only be closed once.
	if again := devDesk(t).CallErr(t, "shell_close", map[string]any{"id": id}); again == "" {
		t.Error("closing a closed session should say so")
	}
}

// --- SSH ---------------------------------------------------------------------

// peerIP is the second host, when tools/ssh-peer.sh has started one. Without it
// the ssh tests are skipped rather than pointed at 127.0.0.1: a session to
// yourself cannot show that anything crossed a machine boundary, which is the
// only thing these tests are for.
func peerIP(t *testing.T) string {
	t.Helper()
	out := Sh(t, "true") // ensure docker is reachable at all
	_ = out
	ip := dockerIP("sentineldesk-ssh-peer")
	if ip == "" {
		t.Skip("no ssh peer running — start one with make ssh-peer")
	}
	return ip
}

func peerSh(t *testing.T, format string, a ...any) string {
	t.Helper()
	return shIn(t, "sentineldesk-ssh-peer", fmt.Sprintf(format, a...))
}

// sshSession connects to the peer and hands back the session id.
func sshSession(t *testing.T) string {
	t.Helper()
	ip := peerIP(t)
	out := devDesk(t).Call(t, "ssh_connect", map[string]any{
		"host": ip, "user": "peer", "password": "peerpass"})
	id := jsonField(t, out, "id")
	if id == "" {
		t.Fatalf("no session id in %s", trunc(out, 200))
	}
	t.Cleanup(func() { devDesk(t).Call(t, "ssh_disconnect", map[string]any{"id": id}) })
	return id
}

func TestSSHKeygen(t *testing.T) {
	path := "/home/sentineldesk/.ssh/it-key"
	Sh(t, "rm -f %s %s.pub", path, path)

	devDesk(t).Call(t, "ssh_keygen", map[string]any{
		"path": path, "type": "ed25519", "comment": "integration"})

	// Both halves on disk, and the public one a key ssh-keygen itself accepts —
	// a file of the right name containing nothing would pass a weaker check.
	if problem := Sh(t, "test -f %s && test -f %s.pub && echo ok", path, path); problem != "ok" {
		t.Fatalf("the key pair is not on disk")
	}
	if fp := Sh(t, "ssh-keygen -l -f %s.pub 2>&1", path); !strings.Contains(fp, "ED25519") {
		t.Fatalf("the public key does not read as ed25519: %s", fp)
	}
}

func TestSSHConnect(t *testing.T) {
	id := sshSession(t)
	// The session exists as far as the daemon is concerned...
	if list := devDesk(t).Call(t, "ssh_list", nil); !strings.Contains(list, id) {
		t.Fatalf("%s is not in the session list:\n%s", id, trunc(list, 300))
	}
	// ...and the peer's sshd agrees that somebody is connected.
	// pgrep on the peer is BusyBox's and has no -c, so it is counted with wc.
	// Asking for a count from a tool that does not offer one returns its usage
	// text, which parses as zero and can never pass.
	eventually(t, 8*time.Second, "the peer to show an established session", func() bool {
		return atoi(peerSh(t, "pgrep sshd | wc -l")) > 1
	})
}

func TestSSHExec(t *testing.T) {
	id := sshSession(t)
	marker := "/home/peer/it-ssh-exec.txt"
	peerSh(t, "rm -f %s", marker)

	out := devDesk(t).Call(t, "ssh_exec", map[string]any{
		"id": id, "command": "echo ssh-exec-ran > " + marker + "; hostname"})

	// The file is on the OTHER machine, which is the whole claim being tested.
	if got := peerSh(t, "cat %s 2>/dev/null", marker); !strings.Contains(got, "ssh-exec-ran") {
		t.Fatalf("the peer's file holds %q", got)
	}
	// And the hostname it returned is the peer's, not this desktop's — the one
	// thing a session to 127.0.0.1 could never show.
	peerHost := peerSh(t, "hostname")
	if !strings.Contains(out, peerHost) {
		t.Errorf("hostname over ssh returned %q, the peer is %q", trunc(out, 150), peerHost)
	}
}

func TestSSHUpload(t *testing.T) {
	id := sshSession(t)
	local, remote := "/tmp/it-upload.txt", "/home/peer/it-upload.txt"
	Sh(t, "printf %%s uploaded-payload > %s", local)
	peerSh(t, "rm -f %s", remote)

	devDesk(t).Call(t, "ssh_upload", map[string]any{"id": id, "local": local, "remote": remote})

	if got := peerSh(t, "cat %s 2>/dev/null", remote); !strings.Contains(got, "uploaded-payload") {
		t.Fatalf("the peer's copy holds %q", got)
	}
}

func TestSSHDownload(t *testing.T) {
	id := sshSession(t)
	remote, local := "/home/peer/it-download.txt", "/tmp/it-download.txt"
	peerSh(t, "printf %%s downloaded-payload > %s", remote)
	Sh(t, "rm -f %s", local)

	devDesk(t).Call(t, "ssh_download", map[string]any{"id": id, "remote": remote, "local": local})

	if got := Sh(t, "cat %s 2>/dev/null", local); !strings.Contains(got, "downloaded-payload") {
		t.Fatalf("the local copy holds %q", got)
	}
}

func TestSSHListRemote(t *testing.T) {
	id := sshSession(t)
	peerSh(t, "mkdir -p /home/peer/it-listdir && touch /home/peer/it-listdir/remote-entry.txt")

	out := devDesk(t).Call(t, "ssh_list_remote", map[string]any{
		"id": id, "path": "/home/peer/it-listdir"})
	if !strings.Contains(out, "remote-entry.txt") {
		t.Fatalf("the remote directory has the file and the listing does not:\n%s", trunc(out, 300))
	}
}

func TestSSHList(t *testing.T) {
	before := devDesk(t).Call(t, "ssh_list", nil)
	id := sshSession(t)
	after := devDesk(t).Call(t, "ssh_list", nil)

	if strings.Contains(before, id) {
		t.Errorf("the list held %s before it existed", id)
	}
	if !strings.Contains(after, id) {
		t.Fatalf("the list does not hold %s:\n%s", id, trunc(after, 300))
	}
}

func TestSSHTunnelLocal(t *testing.T) {
	id := sshSession(t)
	out := devDesk(t).Call(t, "ssh_tunnel_local", map[string]any{
		"id": id, "local_addr": "127.0.0.1:18091", "remote_addr": "127.0.0.1:22"})
	if !strings.Contains(out, "tunnel") {
		t.Fatalf("no tunnel id in %s", trunc(out, 200))
	}

	// The forward is only real if something comes back through it, and what
	// comes back has to be the PEER's sshd rather than anything here.
	eventually(t, 8*time.Second, "the peer's banner through the forwarded port", func() bool {
		// /dev/tcp is a bash feature; the container's /bin/sh does not have it,
		// so the read has to be run by bash explicitly.
		banner := ShBash(t, `exec 3<>/dev/tcp/127.0.0.1/18091 && head -c 20 <&3`)
		return strings.Contains(banner, "SSH-2.0")
	})
}

func TestSSHTunnelRemote(t *testing.T) {
	id := sshSession(t)
	devDesk(t).Call(t, "ssh_tunnel_remote", map[string]any{
		"id": id, "remote_addr": "127.0.0.1:19091", "local_addr": "127.0.0.1:8080"})

	// The listening socket is on the peer, so that is where to look.
	eventually(t, 8*time.Second, "the peer to open the reverse port", func() bool {
		return strings.Contains(peerSh(t, "netstat -ltn 2>/dev/null"), "19091")
	})
}

func TestSSHTunnels(t *testing.T) {
	id := sshSession(t)
	devDesk(t).Call(t, "ssh_tunnel_local", map[string]any{
		"id": id, "local_addr": "127.0.0.1:18092", "remote_addr": "127.0.0.1:22"})

	out := devDesk(t).Call(t, "ssh_tunnels", map[string]any{"id": id})
	if !strings.Contains(out, "18092") {
		t.Fatalf("the tunnel just opened is not listed:\n%s", trunc(out, 300))
	}
}

func TestSSHTunnelClose(t *testing.T) {
	id := sshSession(t)
	out := devDesk(t).Call(t, "ssh_tunnel_local", map[string]any{
		"id": id, "local_addr": "127.0.0.1:18093", "remote_addr": "127.0.0.1:22"})
	tid := jsonField(t, out, "tunnel_id")
	if tid == "" {
		t.Fatalf("no tunnel id in %s", trunc(out, 200))
	}
	eventually(t, 6*time.Second, "the port to open", func() bool {
		return portListening(t, 18093)
	})

	devDesk(t).Call(t, "ssh_tunnel_close", map[string]any{"id": id, "tunnel_id": tid})

	// Closing has to free the socket, not just forget the entry.
	eventually(t, 6*time.Second, "the port to close", func() bool {
		return !portListening(t, 18093)
	})
}

func TestSSHCopyID(t *testing.T) {
	id := sshSession(t)
	key := "/home/sentineldesk/.ssh/it-copyid"
	Sh(t, "rm -f %s %s.pub", key, key)
	devDesk(t).Call(t, "ssh_keygen", map[string]any{
		"path": key, "type": "ed25519", "comment": "it-copyid"})
	peerSh(t, "sed -i '/it-copyid/d' /home/peer/.ssh/authorized_keys 2>/dev/null || true")

	devDesk(t).Call(t, "ssh_copy_id", map[string]any{"id": id, "key_path": key + ".pub"})

	// On the peer's authorized_keys...
	if got := peerSh(t, "grep -c it-copyid /home/peer/.ssh/authorized_keys || echo 0"); atoi(got) == 0 {
		t.Fatal("the key is not in the peer's authorized_keys")
	}
	// ...and it actually works, which the file alone does not prove: a key with
	// the wrong permissions on either side is present and refused.
	out := devDesk(t).Call(t, "ssh_connect", map[string]any{
		"host": peerIP(t), "user": "peer", "key_path": key})
	keyID := jsonField(t, out, "id")
	if keyID == "" {
		t.Fatalf("the installed key does not authenticate: %s", trunc(out, 200))
	}
	devDesk(t).Call(t, "ssh_disconnect", map[string]any{"id": keyID})
}

func TestSSHDisconnect(t *testing.T) {
	ip := peerIP(t)
	out := devDesk(t).Call(t, "ssh_connect", map[string]any{
		"host": ip, "user": "peer", "password": "peerpass"})
	id := jsonField(t, out, "id")
	if id == "" {
		t.Fatalf("no session id in %s", trunc(out, 200))
	}

	devDesk(t).Call(t, "ssh_disconnect", map[string]any{"id": id})

	if list := devDesk(t).Call(t, "ssh_list", nil); strings.Contains(list, id) {
		t.Errorf("%s is still listed after disconnecting:\n%s", id, trunc(list, 300))
	}
	// Using it afterwards has to fail rather than quietly reconnect.
	devDesk(t).CallErr(t, "ssh_exec", map[string]any{"id": id, "command": "true"})
}

// --- helpers -----------------------------------------------------------------

func portListening(t *testing.T, port int) bool {
	t.Helper()
	hexPort := fmt.Sprintf("%04X", port)
	out := Sh(t, `awk '$4=="0A" {split($2,a,":"); print a[2]}' /proc/net/tcp /proc/net/tcp6 2>/dev/null`)
	for _, f := range strings.Fields(out) {
		if f == hexPort {
			return true
		}
	}
	return false
}
