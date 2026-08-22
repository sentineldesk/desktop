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

// SSH client: persistent connections, remote execution, file transfer and
// tunnels (local, reverse and SOCKS).
//
// This uses Go's native SSH library rather than shelling out to the `ssh`
// binary, because that way the connections and tunnels live inside this process:
// they can be listed, closed and reported on, instead of becoming stray child
// processes nobody can account for.

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sshTunnel struct {
	ID      string
	Kind    string // local | remote | dynamic
	Spec    string // human-readable description
	closers []io.Closer
	stop    chan struct{}
	conns   int
	// lastErr is why the most recent connection through this tunnel failed.
	//
	// A local forward cannot know at creation whether the server will allow it:
	// the listener opens here, and the refusal only arrives when something
	// connects and the channel request is denied. Without recording it, the
	// tool reports a tunnel id, the port listens, and every connection dies
	// without a word — which is what an sshd with AllowTcpForwarding no
	// produces, and it is the default on some distributions.
	lastErr error
	mu      sync.Mutex
}

// SSHSession is one open connection together with its tunnels.
type SSHSession struct {
	ID      string
	Host    string
	User    string
	client  *ssh.Client
	opened  time.Time
	mu      sync.Mutex
	tunnels map[string]*sshTunnel
	tunSeq  int
}

type SSHManager struct {
	mu       sync.Mutex
	sessions map[string]*SSHSession
	seq      int
}

func NewSSHManager() *SSHManager {
	return &SSHManager{sessions: map[string]*SSHSession{}}
}

type DialOpts struct {
	Host       string
	Port       int
	User       string
	Password   string
	KeyPath    string
	KeyPass    string
	TimeoutSec int
}

// Connect opens an SSH session using either a key or a password.
func (m *SSHManager) Connect(o DialOpts) (*SSHSession, error) {
	if o.Port == 0 {
		o.Port = 22
	}
	if o.TimeoutSec == 0 {
		o.TimeoutSec = 15
	}
	var auths []ssh.AuthMethod

	if o.KeyPath != "" {
		data, err := os.ReadFile(o.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("could not read the key %s: %w", o.KeyPath, err)
		}
		var signer ssh.Signer
		if o.KeyPass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(o.KeyPass))
		} else {
			signer, err = ssh.ParsePrivateKey(data)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid key (is it passphrase-protected?): %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if o.Password != "" {
		auths = append(auths, ssh.Password(o.Password),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range qs {
					ans[i] = o.Password
				}
				return ans, nil
			}))
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("either password or key_path is required")
	}

	cfg := &ssh.ClientConfig{
		User: o.User,
		Auth: auths,
		// Any host key is accepted. The desktop is ephemeral and there is no
		// known_hosts to carry between runs, so pinning would reject every
		// connection on the first attempt. This is a deliberate choice for an
		// automation environment, not an oversight — do not copy it into a
		// context where the remote host is untrusted.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Duration(o.TimeoutSec) * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", o.Host, o.Port)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("ssh%d", m.seq)
	s := &SSHSession{ID: id, Host: addr, User: o.User, client: client,
		opened: time.Now(), tunnels: map[string]*sshTunnel{}}
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

func (m *SSHManager) Get(id string) (*SSHSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("no such session %q", id)
	}
	return s, nil
}

func (m *SSHManager) List() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, s := range m.sessions {
		s.mu.Lock()
		out = append(out, map[string]any{
			"id": s.ID, "host": s.Host, "user": s.User,
			"seconds": int(time.Since(s.opened).Seconds()),
			"tunnels": len(s.tunnels),
		})
		s.mu.Unlock()
	}
	return out
}

func (m *SSHManager) Close(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such session %q", id)
	}
	s.mu.Lock()
	for _, t := range s.tunnels {
		closeTunnel(t)
	}
	s.mu.Unlock()
	return s.client.Close()
}

// Exec runs a remote command and returns its output and exit code.
func (s *SSHSession) Exec(command string, timeoutSec int) (string, string, int, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer sess.Close()

	var stdout, stderr strings.Builder
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(command) }()

	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	select {
	case err = <-done:
	case <-time.After(time.Duration(timeoutSec) * time.Second):
		sess.Signal(ssh.SIGKILL)
		return stdout.String(), stderr.String(), -1, fmt.Errorf("timeout de %ds", timeoutSec)
	}

	code := 0
	if err != nil {
		if ee, ok := err.(*ssh.ExitError); ok {
			code = ee.ExitStatus()
			err = nil
		} else {
			code = -1
		}
	}
	return stdout.String(), stderr.String(), code, err
}

// --- transferencia de archivos (SFTP) -------------------------------------

func (s *SSHSession) Upload(local, remote string) (int64, error) {
	cli, err := sftp.NewClient(s.client)
	if err != nil {
		return 0, err
	}
	defer cli.Close()
	src, err := os.Open(local)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	dst, err := cli.Create(remote)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	return io.Copy(dst, src)
}

func (s *SSHSession) Download(remote, local string) (int64, error) {
	cli, err := sftp.NewClient(s.client)
	if err != nil {
		return 0, err
	}
	defer cli.Close()
	src, err := cli.Open(remote)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	dst, err := os.Create(local)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	return io.Copy(dst, src)
}

func (s *SSHSession) ListRemote(path string) ([]map[string]any, error) {
	cli, err := sftp.NewClient(s.client)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	entries, err := cli.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, e := range entries {
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		out = append(out, map[string]any{
			"name": e.Name(), "type": kind, "size": e.Size(),
			"modified": e.ModTime().Format(time.RFC3339),
		})
	}
	return out, nil
}

// --- tunnels ---------------------------------------------------------------

func (s *SSHSession) newTunnelID(kind string) string {
	s.tunSeq++
	return fmt.Sprintf("%s-%s%d", s.ID, kind[:1], s.tunSeq)
}

// TunnelLocal forwards a local port to an address as seen from the server, the
// equivalent of ssh -L. Anything arriving at localAddr comes out on the remote
// side — the way to reach a service only the remote host can see.
func (s *SSHSession) TunnelLocal(localAddr string, remoteAddr string) (*sshTunnel, error) {
	// Ask the server once, before opening anything, whether it will carry this
	// at all — and refuse only for the answer that can never change.
	//
	// A server with AllowTcpForwarding no (the default on some distributions,
	// Alpine among them) denies the channel outright, and that verdict holds
	// for the whole session: a tunnel created against it can only ever accept
	// connections and drop them. Failing here turns a tunnel id handed back for
	// something that will never work into an error naming the reason.
	//
	// A refused connection is a different matter and is NOT fatal: the service
	// on the far side may simply not be up yet, and opening the forward before
	// starting the thing it points at is ordinary. That case creates the tunnel
	// and lets the caller find out through ssh_tunnels.
	if probe, err := s.client.Dial("tcp", remoteAddr); err != nil {
		if strings.Contains(err.Error(), "administratively prohibited") ||
			strings.Contains(err.Error(), "forwarding") {
			return nil, fmt.Errorf(
				"the server will not forward to %s: %w — check AllowTcpForwarding in its sshd_config",
				remoteAddr, err)
		}
	} else {
		probe.Close()
	}

	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	t := &sshTunnel{ID: s.newTunnelID("local"), Kind: "local",
		Spec:    localAddr + " → " + remoteAddr + " (via " + s.Host + ")",
		closers: []io.Closer{ln}, stop: make(chan struct{})}
	s.tunnels[t.ID] = t
	s.mu.Unlock()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.mu.Lock()
			t.conns++
			t.mu.Unlock()
			go func() {
				defer conn.Close()
				remote, err := s.client.Dial("tcp", remoteAddr)
				if err != nil {
					// Keep it. Closing the connection in silence is what made a
					// forbidden forward look like a working tunnel.
					t.mu.Lock()
					t.lastErr = err
					t.mu.Unlock()
					return
				}
				defer remote.Close()
				pipeBoth(conn, remote)
			}()
		}
	}()
	return t, nil
}

// TunnelRemote is the REVERSE tunnel (ssh -R): the server opens a port and
// everything arriving there is delivered to an address reachable from here.
//
// This is how a desktop behind NAT gets published through a jump host with a
// public address. The server usually needs GatewayPorts enabled to listen on
// anything other than its own loopback.
func (s *SSHSession) TunnelRemote(remoteAddr string, localAddr string) (*sshTunnel, error) {
	ln, err := s.client.Listen("tcp", remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("the server refused to open %s (GatewayPorts?): %w", remoteAddr, err)
	}
	s.mu.Lock()
	t := &sshTunnel{ID: s.newTunnelID("remote"), Kind: "remote",
		Spec:    s.Host + ":" + remoteAddr + " → " + localAddr + " (reverse)",
		closers: []io.Closer{ln}, stop: make(chan struct{})}
	s.tunnels[t.ID] = t
	s.mu.Unlock()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.mu.Lock()
			t.conns++
			t.mu.Unlock()
			go func() {
				defer conn.Close()
				local, err := net.Dial("tcp", localAddr)
				if err != nil {
					return
				}
				defer local.Close()
				pipeBoth(conn, local)
			}()
		}
	}()
	return t, nil
}

func (s *SSHSession) Tunnels() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, t := range s.tunnels {
		t.mu.Lock()
		entry := map[string]any{
			"id": t.ID, "kind": t.Kind, "spec": t.Spec, "connections": t.conns,
		}
		if t.lastErr != nil {
			entry["last_error"] = t.lastErr.Error()
			entry["working"] = false
		}
		out = append(out, entry)
		t.mu.Unlock()
	}
	return out
}

func (s *SSHSession) CloseTunnel(id string) error {
	s.mu.Lock()
	t, ok := s.tunnels[id]
	if ok {
		delete(s.tunnels, id)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such tunnel %q", id)
	}
	closeTunnel(t)
	return nil
}

func closeTunnel(t *sshTunnel) {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	for _, c := range t.closers {
		c.Close()
	}
}

func pipeBoth(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}
