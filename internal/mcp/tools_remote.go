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

// Graphical remote sessions: RDP, VNC and SPICE, opened onto the SHARED screen.
//
// This is the deliberate counterpart to ssh_*. SSH is headless — a connection
// the agent reads and types into, invisible to the room. A remote desktop is
// the opposite: it lands a window everyone watching the SentinelDesk screen can
// see, which is exactly why it belongs here and why remote_open and
// remote_close require control. Opening someone's Windows box in front of the
// room is driving the shared desktop, not a private side channel.
//
// Two backends, and the split is a decision, not laziness (§ the owner asked
// for both):
//
//   - Remmina is the primary. One code path for every protocol, and the
//     password never touches argv or the witnessed job pane: it is encrypted
//     with `remmina --encrypt-password` (which reads the plaintext from STDIN)
//     and written into a 0600 .remmina profile. Saved profiles live in
//     Remmina's own store, so a profile the agent saves also appears in the
//     Remmina a person opens by hand.
//   - The direct CLIs are the fallback for a stripped image with no Remmina:
//     xfreerdp3 with /from-stdin for RDP (password fed over stdin, never in
//     `ps`), and xtigervncviewer for VNC with a passwd file this package writes
//     itself — the standard fixed-key VNC obfuscation, so the fallback stays
//     headless without pulling vncpasswd into the image. SPICE has no CLI
//     fallback; it needs Remmina.
//
// Remmina is single-instance: `remmina -c` hands the request to the running
// process and the one we launched exits, so its pid tells us nothing. A Remmina
// session is therefore tracked by the WINDOW that appears and closed with
// wmctrl. A CLI client is a normal process — tracked and killed by pid.

import (
	"context"
	"crypto/des"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// remoteSession is one open remote desktop, kept so it can be listed and closed.
type remoteSession struct {
	ID        string    `json:"id"`
	Protocol  string    `json:"protocol"`
	Host      string    `json:"host"`
	User      string    `json:"user,omitempty"`
	Backend   string    `json:"backend"` // "remmina" | "cli"
	Profile   string    `json:"profile,omitempty"`
	PID       int       `json:"pid,omitempty"`    // set for the CLI backend
	Window    string    `json:"window,omitempty"` // set once a window is found
	StartedAt time.Time `json:"started_at"`
}

// remoteProtocols is the set the tools accept, mapped to Remmina's own names.
var remoteProtocols = map[string]string{
	"rdp":   "RDP",
	"vnc":   "VNC",
	"spice": "SPICE",
}

// remoteDefaultPort fills in the port when the caller gives only a host.
var remoteDefaultPort = map[string]int{"rdp": 3389, "vnc": 5900, "spice": 5900}

func (s *Server) buildRemoteTools() []toolDef {
	return []toolDef{
		{
			Name:            "remote_open",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Open a graphical REMOTE DESKTOP (RDP, VNC or SPICE) in a window on the shared screen — connect to a Windows box, another Linux machine or a VM and see it inside SentinelDesk. For a shell rather than a screen use ssh_connect instead. Give either a saved profile name, or the connection details inline (host, user, password). The password is passed securely — never on the command line. Returns a session id.",
			InputSchema: schema(map[string]any{
				"protocol":   pStr("rdp | vnc | spice (not needed when 'profile' is given)"),
				"host":       pStr("hostname or IP of the remote machine"),
				"port":       pInt("port (optional; the protocol's standard port if omitted -- 3389 rdp, 5900 vnc/spice)"),
				"username":   pStr("username on the remote machine"),
				"password":   pStr("password (kept out of the process list and the job log)"),
				"domain":     pStr("Windows domain, for rdp (optional)"),
				"profile":    pStr("connect using a saved profile by name (from remote_profile_save) instead of inline details"),
				"fullscreen": pBool("take the remote over the whole screen (default false; on a shared desktop it covers everything, so windowed is usually right)"),
				"backend":    pStr("auto | remmina | cli (default auto: Remmina if present, else the direct client)"),
			}),
		},
		{
			Name:            "remote_close",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Close an open remote-desktop session by its id (from remote_open or remote_list). Ends the connection and removes its window from the shared screen.",
			InputSchema:     schema(map[string]any{"id": pStr("session id")}, "id"),
		},
		{
			Name:        "remote_list",
			Risk:        riskRead,
			Description: "List the remote-desktop sessions currently open on the shared screen: id, protocol, host, backend and window.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "remote_profile_save",
			Visibility:  visHidden,
			Risk:        riskWrite,
			Description: "Save (or overwrite) a reusable remote-desktop connection profile — protocol, host, credentials and options under a name. The password is stored encrypted. Profiles live in Remmina's own store, so a person's Remmina shows them too. Connect later with remote_open using the profile name.",
			InputSchema: schema(map[string]any{
				"name":       pStr("a short name for the profile, e.g. \"office-pc\""),
				"protocol":   pStr("rdp | vnc | spice"),
				"host":       pStr("hostname or IP"),
				"port":       pInt("port (optional; the protocol's standard port if omitted -- 3389 rdp, 5900 vnc/spice)"),
				"username":   pStr("username on the remote machine"),
				"password":   pStr("password (stored encrypted, never in clear text)"),
				"domain":     pStr("Windows domain, for rdp (optional)"),
				"fullscreen": pBool("open fullscreen when connecting (default false)"),
			}, "name", "protocol", "host"),
		},
		{
			Name:        "remote_profile_list",
			Risk:        riskRead,
			Description: "List the saved remote-desktop connection profiles: name, protocol, host and user. Passwords are never returned.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "remote_profile_delete",
			Visibility:  visHidden,
			Risk:        riskWrite,
			Description: "Delete a saved remote-desktop connection profile by name.",
			InputSchema: schema(map[string]any{"name": pStr("profile name")}, "name"),
		},
	}
}

func (s *Server) dispatchRemote(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	case "remote_open":
		content, isErr := s.remoteOpen(ctx, args)
		return content, isErr, true
	case "remote_close":
		content, isErr := s.remoteClose(argStr(args, "id"))
		return content, isErr, true
	case "remote_list":
		s.remoteMu.Lock()
		out := make([]*remoteSession, 0, len(s.remoteSessions))
		for _, sess := range s.remoteSessions {
			out = append(out, sess)
		}
		s.remoteMu.Unlock()
		return jsonContent(map[string]any{"sessions": out}), false, true
	case "remote_profile_save":
		content, isErr := s.remoteProfileSave(args)
		return content, isErr, true
	case "remote_profile_list":
		content, isErr := s.remoteProfileList()
		return content, isErr, true
	case "remote_profile_delete":
		content, isErr := s.remoteProfileDelete(argStr(args, "name"))
		return content, isErr, true
	}
	return nil, false, false
}

// --- opening -----------------------------------------------------------------

func (s *Server) remoteOpen(ctx context.Context, args map[string]any) ([]map[string]any, bool) {
	profile := strings.TrimSpace(argStr(args, "profile"))
	fullscreen, _ := args["fullscreen"].(bool)
	backend := strings.ToLower(strings.TrimSpace(argStr(args, "backend")))
	if backend == "" {
		backend = "auto"
	}

	// A saved profile carries an encrypted password only Remmina can read, so
	// connecting to one is a Remmina-only path by construction.
	if profile != "" {
		path := filepath.Join(remminaStoreDir(), remoteProfileFile(profile))
		if _, err := os.Stat(path); err != nil {
			return textContent("no saved profile named %q", profile), true
		}
		_, proto, host, user := readProfileSummary(path)
		return s.launchRemmina(ctx, path, remoteSession{
			Protocol: proto, Host: host, User: user, Profile: profile,
		})
	}

	proto := strings.ToLower(strings.TrimSpace(argStr(args, "protocol")))
	rproto, ok := remoteProtocols[proto]
	if !ok {
		return textContent("protocol must be one of rdp, vnc, spice (got %q)", proto), true
	}
	host := strings.TrimSpace(argStr(args, "host"))
	if host == "" {
		return textContent("a host is required"), true
	}
	port := argInt(args, "port")
	user := argStr(args, "username")
	password := argStr(args, "password")
	domain := argStr(args, "domain")

	useRemmina := backend == "remmina" || (backend == "auto" && haveBinary("remmina"))
	if backend == "cli" && proto == "spice" {
		return textContent("SPICE has no direct CLI client in this image; use backend \"remmina\" (the default)"), true
	}
	if !useRemmina && !cliBackendAvailable(proto) {
		return textContent("no client for %s: Remmina is not installed and neither is its CLI fallback", proto), true
	}

	if useRemmina {
		// An ephemeral profile in a temp dir, so an inline connection does not
		// clutter the saved list. Encrypted password, 0600.
		enc, err := encryptRemminaPassword(password)
		if err != nil {
			return textContent("could not prepare the connection: %v", err), true
		}
		dir := filepath.Join(os.TempDir(), "sentineldesk-remote")
		_ = os.MkdirAll(dir, 0o700)
		label := fmt.Sprintf("%s %s", strings.ToUpper(proto), host)
		path := filepath.Join(dir, fmt.Sprintf("sd-%s-%d.remmina", proto, time.Now().UnixNano()))
		if err := writeRemminaProfile(path, remminaProfile{
			Name: label, Protocol: rproto, Server: remoteServer(proto, host, port),
			User: user, EncPass: enc, Domain: domain, Fullscreen: fullscreen,
		}); err != nil {
			return textContent("could not write the connection profile: %v", err), true
		}
		return s.launchRemmina(ctx, path, remoteSession{
			Protocol: proto, Host: host, User: user,
		})
	}
	return s.launchCLI(ctx, proto, host, port, user, password, domain, fullscreen)
}

// launchRemmina starts `remmina -c <profile>` and, because Remmina is
// single-instance, finds the session by the window that appears rather than by
// the pid (which exits as soon as it hands the request to the running Remmina).
func (s *Server) launchRemmina(ctx context.Context, profilePath string, sess remoteSession) ([]map[string]any, bool) {
	before := s.remminaWindowSet()

	cmd := exec.Command("setsid", "remmina", "-c", profilePath)
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display)
	if err := cmd.Start(); err != nil {
		return textContent("could not start Remmina: %v", err), true
	}
	go func() { _ = cmd.Wait() }() // reap; the real process is the running Remmina

	// Wait for a NEW Remmina window. Its absence is the failure signal: a
	// connection that is refused never opens one.
	win := s.waitForNewRemminaWindow(ctx, before, 12*time.Second)
	if win == "" {
		return textContent("Remmina did not open a session window within 12s — the connection was most likely refused (wrong host, port, or credentials). Nothing is left open."), true
	}
	s.bringToTheRoom(ctx, []string{win}, 6*time.Second)

	sess.Backend = "remmina"
	sess.Window = win
	sess.ID = s.registerRemote(&sess)
	return jsonContent(map[string]any{
		"id": sess.ID, "protocol": sess.Protocol, "host": sess.Host,
		"backend": "remmina", "window": win,
		"note": "the remote desktop is open on the shared screen; close it with remote_close",
	}), false
}

// launchCLI starts a direct client (xfreerdp3 / xtigervncviewer). These are
// ordinary processes: the pid is real, so the session is tracked and closed by
// it, and an immediate exit is a connection that failed.
func (s *Server) launchCLI(ctx context.Context, proto, host string, port int, user, password, domain string, fullscreen bool) ([]map[string]any, bool) {
	var cmd *exec.Cmd
	var stdin string
	var cleanup func()

	switch proto {
	case "rdp":
		v := remoteServer("rdp", host, port)
		a := []string{"xfreerdp3", "/v:" + v, "/cert:ignore", "/dynamic-resolution", "+clipboard"}
		if user != "" {
			a = append(a, "/u:"+user)
		}
		if fullscreen {
			a = append(a, "/f")
		}
		a = append(a, "/from-stdin")
		cmd = exec.Command("setsid", a...)
		// /from-stdin asks for the credentials still missing, in order: username
		// (only if /u was not given), then domain, then password. Feed exactly
		// those lines so nothing lands in argv.
		var lines []string
		if user == "" {
			lines = append(lines, "")
		}
		lines = append(lines, domain, password)
		stdin = strings.Join(lines, "\n") + "\n"
	case "vnc":
		a := []string{"xtigervncviewer"}
		if fullscreen {
			a = append(a, "-FullScreen")
		}
		if password != "" {
			// TigerVNC reads a password only from an obfuscated file. We write
			// one ourselves (fixed-key VNC scheme) so the fallback is headless
			// without vncpasswd; 0600 and deleted once the viewer has read it.
			pf, err := writeVNCPasswdFile(password)
			if err == nil {
				a = append(a, "-passwd", pf)
				cleanup = func() {
					time.Sleep(8 * time.Second)
					_ = os.Remove(pf)
				}
			}
		}
		a = append(a, remoteServer("vnc", host, port))
		cmd = exec.Command("setsid", a...)
	default:
		return textContent("no CLI client for %s", proto), true
	}

	cmd.Env = append(os.Environ(), "DISPLAY="+s.display)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Start(); err != nil {
		return textContent("could not start the %s client: %v", proto, err), true
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	if cleanup != nil {
		go cleanup()
	}

	// setsid keeps the pid (it does not fork when it is not a group leader), so
	// this is the client itself. If it is gone after a moment, the connection
	// failed rather than opened.
	if !sleepCtx(ctx, 2500*time.Millisecond) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return textContent("cancelled"), true
	}
	if syscall.Kill(pid, 0) != nil {
		return textContent("the %s client exited immediately — the connection was refused (check the host, port and credentials)", proto), true
	}

	win := ""
	if ids := s.windowsOf(pid); len(ids) > 0 {
		win = ids[0]
		s.bringToTheRoom(ctx, ids, 6*time.Second)
	}
	sess := &remoteSession{
		Protocol: proto, Host: host, User: user, Backend: "cli", PID: pid, Window: win,
	}
	sess.ID = s.registerRemote(sess)
	return jsonContent(map[string]any{
		"id": sess.ID, "protocol": proto, "host": host, "backend": "cli",
		"pid": pid, "window": win,
		"note": "the remote desktop is open on the shared screen; close it with remote_close",
	}), false
}

func (s *Server) remoteClose(id string) ([]map[string]any, bool) {
	s.remoteMu.Lock()
	sess, ok := s.remoteSessions[id]
	if ok {
		delete(s.remoteSessions, id)
	}
	s.remoteMu.Unlock()
	if !ok {
		return textContent("no remote session with id %q", id), true
	}
	switch sess.Backend {
	case "cli":
		if sess.PID > 0 {
			_ = syscall.Kill(sess.PID, syscall.SIGTERM)
		}
		if sess.Window != "" {
			_ = s.run("wmctrl", "-i", "-c", sess.Window)
		}
	default: // remmina: the pid is gone; close the window
		if sess.Window != "" {
			_ = s.run("wmctrl", "-i", "-c", sess.Window)
		}
	}
	return textContent("closed remote session %s (%s %s)", id, sess.Protocol, sess.Host), false
}

func (s *Server) registerRemote(sess *remoteSession) string {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	s.remoteSeq++
	sess.ID = fmt.Sprintf("remote-%d", s.remoteSeq)
	sess.StartedAt = time.Now()
	s.remoteSessions[sess.ID] = sess
	return sess.ID
}

// --- saved profiles ----------------------------------------------------------

func (s *Server) remoteProfileSave(args map[string]any) ([]map[string]any, bool) {
	name := strings.TrimSpace(argStr(args, "name"))
	if !validProfileName(name) {
		return textContent("a profile name may hold only letters, digits, space, dash and underscore"), true
	}
	proto := strings.ToLower(strings.TrimSpace(argStr(args, "protocol")))
	rproto, ok := remoteProtocols[proto]
	if !ok {
		return textContent("protocol must be one of rdp, vnc, spice"), true
	}
	host := strings.TrimSpace(argStr(args, "host"))
	if host == "" {
		return textContent("a host is required"), true
	}
	enc, err := encryptRemminaPassword(argStr(args, "password"))
	if err != nil {
		return textContent("could not encrypt the password: %v", err), true
	}
	fullscreen, _ := args["fullscreen"].(bool)
	dir := remminaStoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return textContent("could not create the profile store: %v", err), true
	}
	path := filepath.Join(dir, remoteProfileFile(name))
	if err := writeRemminaProfile(path, remminaProfile{
		Name: name, Protocol: rproto, Server: remoteServer(proto, host, argInt(args, "port")),
		User: argStr(args, "username"), EncPass: enc, Domain: argStr(args, "domain"),
		Fullscreen: fullscreen,
	}); err != nil {
		return textContent("could not write the profile: %v", err), true
	}
	return textContent("saved profile %q (%s %s)", name, strings.ToUpper(proto), host), false
}

func (s *Server) remoteProfileList() ([]map[string]any, bool) {
	dir := remminaStoreDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return jsonContent(map[string]any{"profiles": []any{}}), false
	}
	type prof struct {
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
		Host     string `json:"host"`
		User     string `json:"user,omitempty"`
	}
	var out []prof
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".remmina") {
			continue
		}
		name, proto, host, user := readProfileSummary(filepath.Join(dir, e.Name()))
		if name == "" {
			// A profile that predates this tool, or one a person made in Remmina
			// by hand: fall back to the file's own name so it still lists.
			name = strings.TrimSuffix(e.Name(), ".remmina")
		}
		out = append(out, prof{Name: name, Protocol: proto, Host: host, User: user})
	}
	return jsonContent(map[string]any{"profiles": out}), false
}

func (s *Server) remoteProfileDelete(name string) ([]map[string]any, bool) {
	if !validProfileName(name) {
		return textContent("invalid profile name"), true
	}
	path := filepath.Join(remminaStoreDir(), remoteProfileFile(name))
	if _, err := os.Stat(path); err != nil {
		return textContent("no saved profile named %q", name), true
	}
	if err := os.Remove(path); err != nil {
		return textContent("could not delete the profile: %v", err), true
	}
	return textContent("deleted profile %q", name), false
}

// --- helpers -----------------------------------------------------------------

func remminaStoreDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/home/sentineldesk"
	}
	return filepath.Join(home, ".local", "share", "remmina")
}

// remoteProfileFile maps a profile name to a filename Remmina reads, keeping the
// name legible in its store rather than hashing it.
func remoteProfileFile(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '-', r == '_':
			return '_'
		default:
			return -1
		}
	}, name)
	return "sentineldesk-" + safe + ".remmina"
}

var profileNameOK = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,64}$`)

func validProfileName(name string) bool { return profileNameOK.MatchString(name) }

func remoteServer(proto, host string, port int) string {
	if port <= 0 {
		port = remoteDefaultPort[proto]
	}
	if port <= 0 || strings.Contains(host, ":") {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}

func haveBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func cliBackendAvailable(proto string) bool {
	switch proto {
	case "rdp":
		return haveBinary("xfreerdp3") || haveBinary("xfreerdp")
	case "vnc":
		return haveBinary("xtigervncviewer") || haveBinary("vncviewer")
	}
	return false
}

type remminaProfile struct {
	Name       string
	Protocol   string // RDP / VNC / SPICE
	Server     string
	User       string
	EncPass    string
	Domain     string
	Fullscreen bool
}

func writeRemminaProfile(path string, p remminaProfile) error {
	var b strings.Builder
	b.WriteString("[remmina]\n")
	b.WriteString("name=" + p.Name + "\n")
	b.WriteString("protocol=" + p.Protocol + "\n")
	b.WriteString("server=" + p.Server + "\n")
	if p.User != "" {
		b.WriteString("username=" + p.User + "\n")
	}
	if p.EncPass != "" {
		b.WriteString("password=" + p.EncPass + "\n")
	}
	if p.Domain != "" {
		b.WriteString("domain=" + p.Domain + "\n")
	}
	if p.Protocol == "RDP" {
		b.WriteString("colordepth=32\n")
		b.WriteString("ignore-tls-errors=1\n")
	}
	if p.Protocol == "VNC" {
		b.WriteString("colordepth=32\n")
		b.WriteString("quality=9\n")
	}
	// viewmode 4 is Remmina's fullscreen; 1 is a normal window. Windowed by
	// default: a fullscreen remote on a shared screen hides SentinelDesk's own
	// panel from everyone else in the room.
	if p.Fullscreen {
		b.WriteString("viewmode=4\n")
	} else {
		b.WriteString("viewmode=1\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// encryptRemminaPassword turns a plaintext password into the token Remmina
// stores. The plaintext goes over STDIN, never argv; the output carries the
// encrypted form inside sample URIs, which is what the regexp lifts back out.
// An empty password stays empty (Remmina prints "(null)" for one).
func encryptRemminaPassword(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	cmd := exec.Command("remmina", "--encrypt-password")
	cmd.Stdin = strings.NewReader(plain + "\n")
	// The OUTPUT is the source of truth, not the exit code. Remmina prints the
	// encrypted token to stdout and then, in the daemon's environment
	// (GTK_MODULES=atk-bridge), its GTK/AT-SPI teardown exits non-zero even
	// though the encryption succeeded — the token is already on stdout. So the
	// exit status is deliberately ignored and the token is parsed either way;
	// the failure is a token that is absent, not a process that returned 1.
	out, _ := cmd.CombinedOutput()
	m := regexp.MustCompile(`://username:([^@]+)@server`).FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("could not read the encrypted password from Remmina (output: %q)",
			strings.TrimSpace(string(out)))
	}
	return string(m[1]), nil
}

// readProfileSummary pulls the non-secret fields out of a .remmina file. The
// name is the one stored inside (what the person chose), not the sanitised
// filename it lives under, so the list shows back exactly what remote_open and
// remote_profile_delete expect.
func readProfileSummary(path string) (name, proto, host, user string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "name":
			name = v
		case "protocol":
			proto = strings.ToLower(v)
		case "server":
			host = v
		case "username":
			user = v
		}
	}
	return name, proto, host, user
}

// remminaWindowSet is the set of Remmina window ids right now.
func (s *Server) remminaWindowSet() map[string]bool {
	set := map[string]bool{}
	for _, w := range s.listWindows() {
		if strings.Contains(strings.ToLower(w.Class), "remmina") {
			set[w.ID] = true
		}
	}
	return set
}

// waitForNewRemminaWindow polls until a Remmina window appears that was not in
// `before`, or the timeout elapses. The main window (the connection manager)
// carries no title while a session tab does; both are acceptable — either means
// Remmina came up in response to our request.
func (s *Server) waitForNewRemminaWindow(ctx context.Context, before map[string]bool, within time.Duration) string {
	deadline := time.Now().Add(within)
	for {
		for _, w := range s.listWindows() {
			if !strings.Contains(strings.ToLower(w.Class), "remmina") {
				continue
			}
			if !before[w.ID] {
				return w.ID
			}
		}
		if time.Now().After(deadline) || !sleepCtx(ctx, 400*time.Millisecond) {
			return ""
		}
	}
}

// writeVNCPasswdFile writes the obfuscated password file TigerVNC's -passwd
// expects. The scheme is the classic one: DES-ECB the (8-byte, zero-padded)
// password with a FIXED key whose every byte is bit-reversed. This is
// obfuscation, not security — the key is public — which is exactly why the file
// is 0600 and short-lived, and why the primary backend (Remmina, keyring) is
// preferred.
func writeVNCPasswdFile(password string) (string, error) {
	fixed := []byte{23, 82, 107, 6, 35, 78, 88, 7}
	key := make([]byte, 8)
	for i := range fixed {
		key[i] = reverseBits(fixed[i])
	}
	block, err := des.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := make([]byte, 8)
	copy(plain, []byte(password)) // truncated to 8, zero-padded — the VNC limit
	enc := make([]byte, 8)
	block.Encrypt(enc, plain)

	dir := filepath.Join(os.TempDir(), "sentineldesk-remote")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "vncpass-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := f.Write(enc); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func reverseBits(b byte) byte {
	var r byte
	for i := 0; i < 8; i++ {
		r = (r << 1) | (b & 1)
		b >>= 1
	}
	return r
}
