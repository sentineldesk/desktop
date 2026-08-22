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

// Terminal tools (shell_*), SSH (ssh_*) and low-level windows (window_*).

import (
	"context"
	"fmt"
	"github.com/sentineldesk/desktop/internal/shell"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func (s *Server) buildSysTools() []toolDef {
	return []toolDef{
		// ---------- terminal persistente ----------
		{
			Name:        "shell_open",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Open a PERSISTENT shell session on a real terminal (PTY). Unlike run_command, the session keeps its working directory, environment and history between calls, and can talk to interactive programs (vim, top, installers asking yes/no). Pass user:\"root\" for a root terminal — no password needed. Returns a session id.",
			InputSchema: schema(map[string]any{
				"shell": pStr("shell to run (default /bin/bash)"),
				"cwd":   pStr("initial working directory"),
				"user":  pStr("run the shell as this user, e.g. \"root\" (default: the desktop user)"),
				"cols":  pIntDef("terminal width (default 120)", 120),
				"rows":  pIntDef("terminal height (default 34)", 34),
			}),
		},
		{
			Name:        "shell_exec",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Run a command in a shell session and return its output. State persists: after `cd /etc` the next command runs there. Any Linux command works, pipes and redirection included.",
			InputSchema: schema(map[string]any{
				"id":         pStr("session id from shell_open"),
				"command":    pStr("command line"),
				"timeout_ms": pIntDef("max wait (default 20000)", 20000),
				"quiet_ms":   pIntDef("idle time that means 'finished' (default 400)", 400),
			}, "id", "command"),
		},
		{
			Name:        "shell_input",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Send raw keystrokes to a shell session WITHOUT a trailing newline — for answering a prompt, typing a password, or sending control characters (use \\u0003 for Ctrl+C, \\u001b for Escape). Read the result with shell_read.",
			InputSchema: schema(map[string]any{
				"id": pStr("session id"), "text": pStr("text or control characters to send"),
				"enter": pBool("append Enter (default false)"),
			}, "id", "text"),
		},
		{
			Name:        "shell_read",
			Risk:        riskRead,
			Description: "Read (and clear) the output a shell session has produced since the last read. Use it after shell_input, or to follow a long-running command.",
			InputSchema: schema(map[string]any{"id": pStr("session id")}, "id"),
		},
		{
			Name:        "shell_list",
			Risk:        riskRead,
			Description: "List the open shell sessions with their state and pending output.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "shell_close",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Close a shell session and terminate its process.",
			InputSchema: schema(map[string]any{"id": pStr("session id")}, "id"),
		},

		// ---------- SSH ----------
		{
			Name:        "ssh_connect",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Open an SSH connection to a remote host, authenticating with a password or a private key (with optional passphrase). The connection stays open for the other ssh_* tools. Returns a session id.",
			InputSchema: schema(map[string]any{
				"host":     pStr("hostname or IP"),
				"user":     pStr("username"),
				"port":     pIntDef("port (default 22)", 22),
				"password": pStr("password (or use key_path)"),
				"key_path": pStr("path to the private key, e.g. /home/sentineldesk/.ssh/id_ed25519"),
				"key_pass": pStr("passphrase of the key, if it has one"),
			}, "host", "user"),
		},
		{
			Name:        "ssh_exec",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Run a command on the remote host and return stdout, stderr and exit code.",
			InputSchema: schema(map[string]any{
				"id": pStr("ssh session id"), "command": pStr("remote command"),
				"timeout_sec": pIntDef("timeout in seconds (default 60)", 60),
			}, "id", "command"),
		},
		{
			Name:        "ssh_upload",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Copy a local file to the remote host over SFTP.",
			InputSchema: schema(map[string]any{
				"id": pStr("ssh session id"), "local": pStr("local path"), "remote": pStr("remote path"),
			}, "id", "local", "remote"),
		},
		{
			Name:        "ssh_download",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Copy a file from the remote host to the desktop over SFTP.",
			InputSchema: schema(map[string]any{
				"id": pStr("ssh session id"), "remote": pStr("remote path"), "local": pStr("local path"),
			}, "id", "remote", "local"),
		},
		{
			Name:        "ssh_list_remote",
			Risk:        riskRead,
			Description: "List a directory on the remote host over SFTP.",
			InputSchema: schema(map[string]any{
				"id": pStr("ssh session id"), "path": pStr("remote directory"),
			}, "id", "path"),
		},
		{
			Name:        "ssh_tunnel_local",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Local port forward (ssh -L): open a port HERE whose traffic comes out on the remote side. Use it to reach a service that only the remote host can see, e.g. its database on 127.0.0.1:5432.",
			InputSchema: schema(map[string]any{
				"id":          pStr("ssh session id"),
				"local_addr":  pStr("local listen address, e.g. 127.0.0.1:15432"),
				"remote_addr": pStr("destination as seen from the server, e.g. 127.0.0.1:5432"),
			}, "id", "local_addr", "remote_addr"),
		},
		{
			Name:        "ssh_tunnel_remote",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "REVERSE port forward (ssh -R): the server opens a port and everything arriving there is delivered to an address reachable from here. This is how you publish this desktop through a public jump host when it sits behind NAT. The server usually needs GatewayPorts enabled to listen on 0.0.0.0.",
			InputSchema: schema(map[string]any{
				"id":          pStr("ssh session id"),
				"remote_addr": pStr("address the SERVER will listen on, e.g. 0.0.0.0:8080"),
				"local_addr":  pStr("where to deliver it from here, e.g. 127.0.0.1:8080"),
			}, "id", "remote_addr", "local_addr"),
		},
		{
			Name:        "ssh_tunnels",
			Risk:        riskRead,
			Description: "List the tunnels open on an SSH session, with how many connections each has served.",
			InputSchema: schema(map[string]any{"id": pStr("ssh session id")}, "id"),
		},
		{
			Name:        "ssh_tunnel_close",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Close one tunnel by its id.",
			InputSchema: schema(map[string]any{
				"id": pStr("ssh session id"), "tunnel_id": pStr("tunnel id from ssh_tunnels"),
			}, "id", "tunnel_id"),
		},
		{
			Name:        "ssh_list",
			Risk:        riskRead,
			Description: "List the open SSH sessions.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:        "ssh_disconnect",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Close an SSH session and all of its tunnels.",
			InputSchema: schema(map[string]any{"id": pStr("ssh session id")}, "id"),
		},
		{
			Name:        "ssh_keygen",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Generate an SSH key pair on the desktop (ed25519 by default) and return the public key, ready to paste into a server's authorized_keys.",
			InputSchema: schema(map[string]any{
				"path":    pStr("output path (default /home/sentineldesk/.ssh/id_ed25519)"),
				"type":    pStr("ed25519 | rsa (default ed25519)"),
				"comment": pStr("key comment"),
			}),
		},
		{
			Name:        "ssh_copy_id",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Install a public key into the remote user's authorized_keys over an existing session, so future connections can use the key instead of a password.",
			InputSchema: schema(map[string]any{
				"id":       pStr("ssh session id"),
				"key_path": pStr("path to the PUBLIC key (default /home/sentineldesk/.ssh/id_ed25519.pub)"),
			}, "id"),
		},

		// ---------- low-level windows (EWMH / X11) ----------
		{
			Name:        "window_properties",
			Risk:        riskRead,
			Description: "Read every EWMH/X11 property of a window: type, states (_NET_WM_STATE), pid, class, allowed actions, struts and geometry. This is the low level under list_windows — use it when you need to know exactly how the window manager sees a window.",
			InputSchema: schema(map[string]any{"id": pStr("window id, e.g. 0x01200003")}, "id"),
		},
		{
			Name:            "window_set_state",
			Visibility:      visVisible,
			Risk:            riskWrite,
			RequiresControl: true,
			Description:     "Change a window state via EWMH: above, below, sticky, shaded, fullscreen, maximized_vert, maximized_horz, skip_taskbar, skip_pager, hidden, modal, demands_attention.",
			InputSchema: schema(map[string]any{
				"id":     pStr("window id"),
				"state":  pStr("state name, e.g. 'above' or 'sticky'"),
				"action": pStr("add | remove | toggle (default toggle)"),
			}, "id", "state"),
		},
		{
			Name:        "window_hierarchy",
			Risk:        riskRead,
			Description: "Dump the raw X11 window tree (parents and children, geometry, mapped state) — deeper than the window-manager view; useful to debug embedded or override-redirect windows.",
			InputSchema: schema(map[string]any{"id": pStr("optional window id (default: the root window)")}),
		},
	}
}

func (s *Server) dispatchSys(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	// ---------- terminal ----------
	case "shell_open":
		sess, err := s.shells.Open(argStr(args, "shell"), argStr(args, "cwd"),
			argStr(args, "user"), uint16(argInt(args, "cols")), uint16(argInt(args, "rows")))
		if err != nil {
			return textContent("shell_open failed: %v", err), true, true
		}
		// Swallow the greeting and first prompt so the first read is clean.
		sess.Run("", 1500, 300)
		return jsonContent(map[string]any{
			"id": sess.ID, "shell": argStr(args, "shell"), "user": sess.User,
		}), false, true

	case "shell_exec":
		sess, err := s.shells.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		out, done := sess.Run(argStr(args, "command"), argInt(args, "timeout_ms"), argInt(args, "quiet_ms"))
		return jsonContent(map[string]any{"output": out, "completed": done}), false, true

	case "shell_input":
		sess, err := s.shells.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		text := argStr(args, "text")
		if b, _ := args["enter"].(bool); b {
			text += "\n"
		}
		if err := sess.Write(text); err != nil {
			return textContent("shell_input failed: %v", err), true, true
		}
		return textContent("sent %d bytes", len(text)), false, true

	case "shell_read":
		sess, err := s.shells.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		return textContent("%s", shell.StripANSI(sess.Drain())), false, true

	case "shell_list":
		return jsonContent(s.shells.List()), false, true

	case "shell_close":
		if err := s.shells.Close(argStr(args, "id")); err != nil {
			return textContent("%v", err), true, true
		}
		return textContent("session closed"), false, true

	// ---------- SSH ----------
	case "ssh_connect":
		sess, err := s.sshm.Connect(shell.DialOpts{
			Host: argStr(args, "host"), Port: argInt(args, "port"),
			User: argStr(args, "user"), Password: argStr(args, "password"),
			KeyPath: argStr(args, "key_path"), KeyPass: argStr(args, "key_pass"),
		})
		if err != nil {
			return textContent("ssh_connect failed: %v", err), true, true
		}
		return jsonContent(map[string]any{"id": sess.ID, "host": sess.Host, "user": sess.User}), false, true

	case "ssh_exec":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		stdout, stderr, code, err := sess.Exec(argStr(args, "command"), argInt(args, "timeout_sec"))
		res := map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": code}
		if err != nil {
			res["error"] = err.Error()
		}
		return jsonContent(res), false, true

	case "ssh_upload":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		n, err := sess.Upload(argStr(args, "local"), argStr(args, "remote"))
		if err != nil {
			return textContent("ssh_upload failed: %v", err), true, true
		}
		return textContent("uploaded %d bytes to %s", n, argStr(args, "remote")), false, true

	case "ssh_download":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		n, err := sess.Download(argStr(args, "remote"), argStr(args, "local"))
		if err != nil {
			return textContent("ssh_download failed: %v", err), true, true
		}
		return textContent("downloaded %d bytes to %s", n, argStr(args, "local")), false, true

	case "ssh_list_remote":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		items, err := sess.ListRemote(argStr(args, "path"))
		if err != nil {
			return textContent("ssh_list_remote failed: %v", err), true, true
		}
		return jsonContent(items), false, true

	case "ssh_tunnel_local":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		t, err := sess.TunnelLocal(argStr(args, "local_addr"), argStr(args, "remote_addr"))
		if err != nil {
			return textContent("ssh_tunnel_local failed: %v", err), true, true
		}
		return jsonContent(map[string]any{"tunnel_id": t.ID, "spec": t.Spec}), false, true

	case "ssh_tunnel_remote":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		t, err := sess.TunnelRemote(argStr(args, "remote_addr"), argStr(args, "local_addr"))
		if err != nil {
			return textContent("ssh_tunnel_remote failed: %v", err), true, true
		}
		return jsonContent(map[string]any{"tunnel_id": t.ID, "spec": t.Spec}), false, true

	case "ssh_tunnels":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		return jsonContent(sess.Tunnels()), false, true

	case "ssh_tunnel_close":
		sess, err := s.sshm.Get(argStr(args, "id"))
		if err != nil {
			return textContent("%v", err), true, true
		}
		if err := sess.CloseTunnel(argStr(args, "tunnel_id")); err != nil {
			return textContent("%v", err), true, true
		}
		return textContent("tunnel closed"), false, true

	case "ssh_list":
		return jsonContent(s.sshm.List()), false, true

	case "ssh_disconnect":
		if err := s.sshm.Close(argStr(args, "id")); err != nil {
			return textContent("%v", err), true, true
		}
		return textContent("SSH session closed"), false, true

	case "ssh_keygen":
		c, e := s.toolSSHKeygen(args)
		return c, e, true

	case "ssh_copy_id":
		c, e := s.toolSSHCopyID(args)
		return c, e, true

	// ---------- low-level windows ----------
	case "window_properties":
		out, err := s.output("xprop", "-id", argStr(args, "id"))
		if err != nil {
			return textContent("window_properties failed: %v", err), true, true
		}
		return jsonContent(parseXProps(out)), false, true

	case "window_set_state":
		action := argStr(args, "action")
		if action == "" {
			action = "toggle"
		}
		state := argStr(args, "state")
		if err := s.run("wmctrl", "-i", "-r", argStr(args, "id"), "-b", action+","+state); err != nil {
			return textContent("window_set_state failed: %v", err), true, true
		}
		return textContent("%s %s en %s", action, state, argStr(args, "id")), false, true

	case "window_hierarchy":
		xargs := []string{"-root", "-tree"}
		if id := argStr(args, "id"); id != "" {
			xargs = []string{"-id", id, "-tree"}
		}
		out, err := s.output("xwininfo", xargs...)
		if err != nil {
			return textContent("window_hierarchy failed: %v", err), true, true
		}
		return textContent("%s", out), false, true
	}
	return nil, false, false
}

func (s *Server) toolSSHKeygen(args map[string]any) ([]map[string]any, bool) {
	path := argStr(args, "path")
	if path == "" {
		path = "/home/sentineldesk/.ssh/id_ed25519"
	}
	ktype := argStr(args, "type")
	if ktype == "" {
		ktype = "ed25519"
	}
	comment := argStr(args, "comment")
	if comment == "" {
		comment = "sentineldesk"
	}
	os.MkdirAll("/home/sentineldesk/.ssh", 0o700)
	if _, err := os.Stat(path); err == nil {
		pub, _ := os.ReadFile(path + ".pub")
		return jsonContent(map[string]any{
			"path": path, "public_key": strings.TrimSpace(string(pub)),
			"note": "the key already existed and was not overwritten",
		}), false
	}
	cmd := exec.Command("ssh-keygen", "-t", ktype, "-N", "", "-C", comment, "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return textContent("ssh_keygen failed: %v (%s)", err, string(out)), true
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return textContent("key created but its public half could not be read: %v", err), true
	}
	return jsonContent(map[string]any{
		"path": path, "public_key": strings.TrimSpace(string(pub)),
	}), false
}

func (s *Server) toolSSHCopyID(args map[string]any) ([]map[string]any, bool) {
	sess, err := s.sshm.Get(argStr(args, "id"))
	if err != nil {
		return textContent("%v", err), true
	}
	keyPath := argStr(args, "key_path")
	if keyPath == "" {
		keyPath = "/home/sentineldesk/.ssh/id_ed25519.pub"
	}
	pub, err := os.ReadFile(keyPath)
	if err != nil {
		return textContent("could not read the public key %s: %v", keyPath, err), true
	}
	key := strings.TrimSpace(string(pub))
	cmd := fmt.Sprintf(
		"mkdir -p ~/.ssh && chmod 700 ~/.ssh && "+
			"grep -qxF %s ~/.ssh/authorized_keys 2>/dev/null || echo %s >> ~/.ssh/authorized_keys; "+
			"chmod 600 ~/.ssh/authorized_keys && echo installed",
		shellQuote(key), shellQuote(key))
	stdout, stderr, code, err := sess.Exec(cmd, 30)
	if err != nil || code != 0 {
		return textContent("ssh_copy_id failed (code %d): %s %v", code, stderr, err), true
	}
	return textContent("key installed on %s@%s: %s", sess.User, sess.Host, strings.TrimSpace(stdout)), false
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// parseXProps turns xprop's output into a readable map.
func parseXProps(out string) map[string]any {
	props := map[string]any{}
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, " = ")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+3:])
		if p := strings.Index(key, "("); p > 0 {
			key = strings.TrimSpace(key[:p])
		}
		// Lists such as _NET_WM_STATE come back as an array
		if strings.Contains(val, ", ") {
			parts := strings.Split(val, ", ")
			for i := range parts {
				parts[i] = strings.Trim(strings.TrimSpace(parts[i]), `"`)
			}
			props[key] = parts
			continue
		}
		if n, err := strconv.Atoi(val); err == nil {
			props[key] = n
			continue
		}
		props[key] = strings.Trim(val, `"`)
	}
	return props
}
