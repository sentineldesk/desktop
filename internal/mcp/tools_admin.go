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

// Administration tools: privileges, packages and services.
//
// A real desktop is not just moving the mouse around. It is being able to
// install what is missing, restart what has wedged, and edit what lives in /etc.
// None of that is possible without root, and an agent without it ends up staring
// at a screen it cannot fix.
//
// The container is the sandbox: the security boundary is the WebSocket login,
// not the inside of the desktop. See docs/mcp.md, Security.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sentineldesk/desktop/pkg/version"
)

// --- privilege escalation ----------------------------------------------------

// sudoAvailable reports whether escalation without a password is possible. It is
// resolved once, because the answer cannot change while the container lives.
var sudoAvailable = func() bool {
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	// -n makes sudo fail rather than prompt on a terminal that is not there.
	return exec.Command("sudo", "-n", "true").Run() == nil
}()

// elevate builds the final command. With asRoot=false it is an ordinary
// `sh -c`; with asRoot=true it is wrapped in `sudo -n -E`, where -E preserves
// DISPLAY and friends so a graphical app launched as root can still find the
// screen.
func elevate(ctx context.Context, command string, asRoot bool) (*exec.Cmd, error) {
	if !asRoot {
		return exec.CommandContext(ctx, "sh", "-c", command), nil
	}
	if !sudoAvailable {
		return nil, fmt.Errorf("this image has no passwordless sudo: rebuild it " +
			"(make image) to pick up the privilege layer")
	}
	return exec.CommandContext(ctx, "sudo", "-n", "-E", "sh", "-c", command), nil
}

// runElevated executes and returns stdout, stderr and the exit code.
func (s *Server) runElevated(ctx context.Context, command string, asRoot bool, timeoutMs int) (map[string]any, error) {
	if timeoutMs <= 0 {
		timeoutMs = 15000
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	cmd, err := elevate(ctx, command, asRoot)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(os.Environ(), "DISPLAY="+s.display, "DEBIAN_FRONTEND=noninteractive")
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr strings.Builder
	// The tail goes alongside the buffers, not instead of them: the caller
	// still gets the whole output, and whoever is watching gets the last line
	// while it is still running.
	tail := &tailWriter{}
	cmd.Stdout = io.MultiWriter(&stdout, tail)
	cmd.Stderr = io.MultiWriter(&stderr, tail)
	stop := reportWhileRunning(ctx, "running", tail)
	runErr := cmd.Run()
	stop()
	exitCode := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	res := map[string]any{
		"exit_code": exitCode,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"as_root":   asRoot,
	}
	if ctx.Err() == context.DeadlineExceeded {
		res["timed_out"] = true
	}
	return res, nil
}

// --- privileged file access --------------------------------------------------
//
// The daemon runs unprivileged, so /etc/shadow or /root are out of reach for
// os.ReadFile. These helpers do the same work by delegating to utilities under
// sudo. The path is always passed as an argument, never interpolated into a
// `sh -c`, so a name containing spaces or quotes breaks nothing.

func requireSudo() error {
	if !sudoAvailable {
		return fmt.Errorf("this image has no passwordless sudo: rebuild it (make image)")
	}
	return nil
}

func rootRead(path string) ([]byte, error) {
	if err := requireSudo(); err != nil {
		return nil, err
	}
	cmd := exec.Command("sudo", "-n", "cat", "--", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(firstNonEmpty(stderr.String(), err.Error())))
	}
	return out, nil
}

// rootWrite hands the content to `tee` over stdin, so the text never travels on
// a command line — not ours, and not anyone's `ps` output — and needs no
// escaping.
func rootWrite(path, content string, appendMode bool, mode string) (int, error) {
	if err := requireSudo(); err != nil {
		return 0, err
	}
	args := []string{"-n", "tee"}
	if appendMode {
		args = append(args, "-a")
	}
	args = append(args, "--", path)
	cmd := exec.Command("sudo", args...)
	cmd.Stdin = strings.NewReader(content)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = nil // drop tee's echo
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("%s", strings.TrimSpace(firstNonEmpty(stderr.String(), err.Error())))
	}
	if mode != "" {
		if strings.ContainsAny(mode, "'\"$`;&|<> \n") {
			return 0, fmt.Errorf("invalid mode: %q", mode)
		}
		if err := exec.Command("sudo", "-n", "chmod", mode, "--", path).Run(); err != nil {
			return len(content), fmt.Errorf("written, but chmod %s failed: %v", mode, err)
		}
	}
	return len(content), nil
}

// rootList uses `find -printf`: tabulated and stable output, unlike `ls`.
func rootList(path string) ([]map[string]any, error) {
	if err := requireSudo(); err != nil {
		return nil, err
	}
	out, err := exec.Command("sudo", "-n", "find", path,
		"-maxdepth", "1", "-mindepth", "1", "-printf", `%y\t%s\t%T@\t%f\n`).Output()
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.SplitN(ln, "\t", 4)
		if len(f) < 4 {
			continue
		}
		kind := "file"
		switch f[0] {
		case "d":
			kind = "dir"
		case "l":
			kind = "link"
		}
		size := 0
		fmt.Sscanf(f[1], "%d", &size)
		var epoch float64
		fmt.Sscanf(f[2], "%f", &epoch)
		items = append(items, map[string]any{
			"name": f[3], "type": kind, "size": size,
			"modified": time.Unix(int64(epoch), 0).Format(time.RFC3339),
		})
	}
	return items, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- catalogue ---------------------------------------------------------------

func (s *Server) buildRootTools() []toolDef {
	return []toolDef{
		{
			Name:        "sudo_status",
			Risk:        riskRead,
			Description: "Report what privilege escalation is available inside the desktop: passwordless sudo, su with a root password, pkexec/polkit, and the current user's groups. Call it first when an action needs root and you want to know how to get there.",
			InputSchema: schema(map[string]any{}),
		},
		{
			Name:            "install_packages",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Install Debian packages with apt (as root), in a terminal window on the shared screen where the people here watch apt work and can stop it. Use it to add whatever the task needs — an editor, a compiler, a game, a driver. Returns the apt log and, once it has finished, what dpkg says is installed for each package. If it has not finished within timeout_ms this hands back a job_id and the install KEEPS RUNNING — call job_wait, do not install again.",
			InputSchema: schema(map[string]any{
				"packages":   pStrArray("package names, e.g. [\"gimp\",\"inkscape\"]"),
				"update":     pBool("run apt-get update first (default true)"),
				"timeout_ms": pIntDef("how long to wait before handing back a job id (default 300000 — installs are slow)", 300000),
			}, "packages"),
		},
		{
			Name:            "remove_packages",
			Visibility:      visVisible,
			Risk:            riskDanger,
			RequiresControl: true,
			Description:     "Uninstall Debian packages with apt (as root), in a terminal window on the shared screen. purge=true also deletes their configuration. Reports what dpkg says about each package afterwards, so a removal that apt reported and did not perform cannot pass for one that worked.",
			InputSchema: schema(map[string]any{
				"packages": pStrArray("package names"),
				"purge":    pBool("also remove configuration files (default false)"),
				// The integration suite has always sent this and it has always
				// been dropped on the floor: the schema never declared it, so a
				// caller asking for a longer wait silently got the fixed one.
				"timeout_ms": pIntDef(
					"how long to wait before handing back a job id (default 180000)", 180000),
			}, "packages"),
		},
		{
			Name: "search_packages",
			Risk: riskRead,
			Description: "Search the Debian archive by name/description and report, for each hit, whether it is already installed and which version is available. Use before install_packages to pick the right package name. " +
				"This reads the index already on disk and never refreshes it: refreshing means a root command talking to the network, which is not something a search does on your behalf. If there is no index it says so and fails rather than answering 'nothing found'.",
			InputSchema: schema(map[string]any{
				"query": pStr("search terms"),
				"limit": pIntDef("max results (default 15)", 15),
			}, "query"),
		},
		{
			Name: "system_updates",
			Risk: riskRead,
			Description: "Report whether this desktop is behind: the packages an upgrade would " +
				"change, with security updates counted separately, plus this build's own version. " +
				"Reads the apt index already on disk and never refreshes it — the answer carries " +
				"the index's age so you can judge how current it is. Use it to answer \"does this " +
				"desktop need patching\" without running apt yourself; if the index is stale, " +
				"refreshing is a visible root command (install_packages refreshes by default, or " +
				"`sudo apt-get update` through run_command).",
			InputSchema: schema(map[string]any{
				"limit": pIntDef("max packages to list in detail (default 30); the counts always cover everything", 30),
			}),
		},
		{
			Name:        "service_control",
			Visibility:  visHidden,
			Risk:        riskDanger,
			Description: "Manage the desktop's own services through supervisor: X server, PulseAudio, the window manager, the accessibility bus, the WebRTC server. action: status (default), start, stop, restart. Omit `name` to act on everything. Restarting sentineldesk-server drops the live WebRTC session.",
			InputSchema: schema(map[string]any{
				"name":   pStr("service: xvfb, pulseaudio, dbus-session, at-spi, openbox, sentineldesk-server… (omit for all)"),
				"action": pStr("status | start | stop | restart (default status)"),
			}),
		},
	}
}

// pStrArray describes a string-list parameter.
func pStrArray(desc string) map[string]any {
	return map[string]any{
		"type": "array", "description": desc,
		"items": map[string]any{"type": "string"},
	}
}

// argStrList accepts a list, a single string, or a string separated by spaces or
// commas. Models send all three shapes, and rejecting two of them would just be
// a trap.
func argStrList(m map[string]any, k string) []string {
	var out []string
	switch v := m[k].(type) {
	case []any:
		for _, it := range v {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' }) {
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// --- dispatch ----------------------------------------------------------------

func (s *Server) dispatchRoot(ctx context.Context, name string, args map[string]any) ([]map[string]any, bool, bool) {
	switch name {
	case "sudo_status":
		return s.toolSudoStatus()
	case "install_packages":
		return s.toolInstallPackages(ctx, args)
	case "remove_packages":
		return s.toolRemovePackages(ctx, args)
	case "search_packages":
		return s.toolSearchPackages(ctx, args)
	case "system_updates":
		return s.toolSystemUpdates(ctx, args)
	case "service_control":
		return s.toolServiceControl(ctx, args)
	}
	return nil, false, false
}

func (s *Server) toolSudoStatus() ([]map[string]any, bool, bool) {
	out := map[string]any{"sudo_nopasswd": sudoAvailable}

	who, _ := exec.Command("id", "-un").Output()
	out["user"] = strings.TrimSpace(string(who))
	groups, _ := exec.Command("id", "-Gn").Output()
	out["groups"] = strings.Fields(string(groups))

	// Is the root account unlocked? The second field of /etc/shadow is "!" or
	// "*" when there is no usable password, and then `su` is of no help.
	suOK := false
	if data, err := os.ReadFile("/etc/shadow"); err == nil {
		for _, ln := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(ln, "root:") {
				f := strings.SplitN(ln, ":", 3)
				suOK = len(f) > 1 && f[1] != "" && f[1] != "!" && f[1] != "*" && !strings.HasPrefix(f[1], "!")
			}
		}
	} else if sudoAvailable {
		// /etc/shadow is not readable unprivileged, so ask through sudo.
		b, _ := exec.Command("sudo", "-n", "sh", "-c",
			`passwd -S root 2>/dev/null | awk '{print $2}'`).Output()
		suOK = strings.TrimSpace(string(b)) == "P"
	}
	out["su_root"] = suOK

	if _, err := exec.LookPath("pkexec"); err == nil {
		out["pkexec"] = true
	} else {
		out["pkexec"] = false
	}

	// Confirm it for real rather than inferring it.
	if sudoAvailable {
		b, _ := exec.Command("sudo", "-n", "id", "-un").Output()
		out["sudo_resolves_to"] = strings.TrimSpace(string(b))
	}
	out["hint"] = "as_root:true on run_command / launch_app / write_file / read_file, " +
		"or shell_open with user:\"root\" for a persistent root terminal"
	return jsonContent(out), false, true
}

// --- packages, on the screen the packages land on ----------------------------

// aptEnv is prefixed onto every apt command these tools build.
//
// It used to arrive through cmd.Env in runElevated, which worked only because
// the daemon was the parent. A job's parent is the tmux server, started long
// before the call and holding whatever environment the desktop booted with —
// so moving apt into a window would have quietly dropped this and handed the
// pane a package manager that stops to ask a question nobody is sitting in
// front of. An install that hangs forever on a purple configuration dialog is
// the exact silent failure this whole change is against, so the setting travels
// in the command text where it cannot be lost.
const aptEnv = "DEBIAN_FRONTEND=noninteractive "

// validPackageNames rejects anything that could close the quoting and smuggle a
// second command into the shell the job runs.
func validPackageNames(pkgs []string) error {
	for _, p := range pkgs {
		if strings.ContainsAny(p, "'\"$`;&|<>()\n\\ ") {
			return fmt.Errorf("invalid package name: %q", p)
		}
	}
	return nil
}

// runOnScreen runs a root command as a job in a terminal window on the shared
// desktop and waits for it, returning the same shape run_command returns.
//
// This is toolRunCommand's body with a different caller, and the duplication is
// deliberate: run_command hands back finished MCP content, and these tools have
// to add `installed` or `removed` to the result before it is serialised. Sharing
// the code would have meant either a second return path through run_command or
// unpacking content it had already packed.
//
// Two properties come with it, and they are the point rather than a side
// effect. The work is ON SCREEN: apt scrolls past in a pane that everyone in
// the room can read and anybody can stop, which is what a person means when
// they say they are watching an agent install something. And the timeout bounds
// the WAIT, not the WORK: an install that outlasts it comes back as a job id
// still running, instead of a partially-unpacked dpkg state and a report that
// says timed_out as though the machine had merely been slow.
func (s *Server) runOnScreen(ctx context.Context, command string, timeoutMs int) (map[string]any, bool, error) {
	if timeoutMs <= 0 {
		timeoutMs = 300000
	}
	rec, err := s.startJob(ctx, command, true)
	if err != nil {
		return nil, false, err
	}

	// The progress heartbeat, kept across the move. apt cannot say what fraction
	// of the way through it is, but "Setting up python3…" every couple of
	// seconds tells a caller a great deal more than silence — and silence for
	// the length of an install is indistinguishable from a hang. It is polled
	// off the file the job is writing rather than off a pipe, because there is
	// no pipe any more.
	tail := &tailWriter{}
	feeding := make(chan struct{})
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-feeding:
				return
			case <-ticker.C:
				if line, err := jobOutput(rec.ID, "out", 1); err == nil && line != "" {
					_, _ = tail.Write([]byte(line + "\n"))
				}
			}
		}
	}()
	stopReporting := reportWhileRunning(ctx, "running", tail)

	final, finished := s.waitForJob(ctx, rec.ID, time.Duration(timeoutMs)*time.Millisecond)
	stopReporting()
	close(feeding)

	// A cancelled CALL really does stop the work, unlike the timeout beside it.
	// The caller withdrew the request, and leaving apt unpacking an answer
	// nobody will read is how a container ends up full of the results of
	// abandoned questions.
	if ctx.Err() != nil && !finished {
		if aborted, aerr := s.abortJob(rec.ID, "the caller", "the tool call was cancelled"); aerr == nil {
			final = aborted
		}
	}

	stdout, _ := jobOutput(rec.ID, "out", 0)
	stderr, _ := jobOutput(rec.ID, "err", 0)

	res := map[string]any{
		"job_id":  rec.ID,
		"stdout":  stdout,
		"stderr":  stderr,
		"as_root": true,
		// Present only when there IS one. An absent exit code means the job is
		// still going, which is not a failure and must not be reported as exit
		// code zero either — both readings are wrong in a way that would be
		// acted on.
		"still_running": !finished,
	}
	if final.ExitCode != nil {
		res["exit_code"] = *final.ExitCode
	}
	if final.AbortedBy != "" {
		res["aborted_by"] = final.AbortedBy
	}
	// Everything that runs here is a job, so everything that runs here carries
	// the link to its output. This is the path install_packages and
	// remove_packages take, and they are the calls MOST likely to outlive the
	// wait — an apt install is the case where a person is left watching a
	// terminal and the agent has nothing to hand them.
	return s.withLogLinks(res, rec.ID), finished, nil
}

// dpkgState asks dpkg what it actually believes about each package.
//
// `${Version}` alone was not an answer: dpkg keeps the version of a package
// that has been removed but not purged, so a removal that failed and a removal
// that worked both came back with a version string, and install_packages
// reported one for a package apt had never unpacked. The status field is the
// authority — "installed", "config-files", "not-installed" — so it is what gets
// reported, with the version beside it when there is one.
func dpkgState(pkgs []string) map[string]string {
	out := map[string]string{}
	for _, p := range pkgs {
		b, _ := exec.Command("dpkg-query", "-W",
			"-f=${db:Status-Status}\t${Version}", p).Output()
		status, version, _ := strings.Cut(strings.TrimSpace(string(b)), "\t")
		switch {
		case status == "":
			// dpkg has never heard of it. From a search that is simply "not
			// installed"; from an install it is the failure, and the exit code
			// beside it says which of the two this is.
			out[p] = "not installed"
		case status == "installed" && version != "":
			out[p] = "installed " + version
		default:
			out[p] = status
		}
	}
	return out
}

func (s *Server) toolInstallPackages(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	pkgs := argStrList(args, "packages")
	if len(pkgs) == 0 {
		return textContent("install_packages: the `packages` list is missing"), true, true
	}
	if err := validPackageNames(pkgs); err != nil {
		return textContent("%v", err), true, true
	}
	timeout := argInt(args, "timeout_ms")
	if timeout <= 0 {
		timeout = 300000
	}
	update := true
	if v, ok := args["update"].(bool); ok {
		update = v
	}
	cmd := ""
	if update {
		// Not `-qq >/dev/null 2>&1` any more. The refresh was hidden twice over
		// — off the screen entirely, and then discarded even from the log — so
		// an install that failed because the archive was unreachable looked
		// exactly like one that failed because the package did not exist.
		// It is still separated by `;` rather than `&&`: a refresh that cannot
		// reach the network must not block an install the local index can
		// already satisfy. What changed is that the failure is now legible.
		cmd = aptEnv + "apt-get update; "
	}
	cmd += aptEnv + "apt-get install -y --no-install-recommends " + strings.Join(pkgs, " ")

	res, finished, err := s.runOnScreen(ctx, cmd, timeout)
	if err != nil {
		return textContent("install_packages: %v", err), true, true
	}
	res["log"] = tailLines(fmt.Sprint(res["stdout"]), 25)
	delete(res, "stdout")

	failed := false
	if finished {
		// What matters is not apt's log but what dpkg holds afterwards.
		res["installed"] = dpkgState(pkgs)
		if code, ok := res["exit_code"].(int); ok && code != 0 {
			failed = true
		}
	} else {
		// Asking dpkg mid-install would report a package that is half unpacked
		// as though the answer were final, so it is not asked at all. Saying
		// which packages are pending is honest; guessing their state is not.
		res["installed_pending"] = pkgs
		res["note"] = "still installing as job " + fmt.Sprint(res["job_id"]) +
			" on the shared screen — this is the output so far, not the end of it. " +
			"Call job_wait with this job_id and then install_packages again with the " +
			"same list to read back what dpkg holds. Do NOT start the install over: " +
			"it was not cancelled."
	}
	return jsonContent(res), failed, true
}

func (s *Server) toolRemovePackages(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	pkgs := argStrList(args, "packages")
	if len(pkgs) == 0 {
		return textContent("remove_packages: the `packages` list is missing"), true, true
	}
	if err := validPackageNames(pkgs); err != nil {
		return textContent("%v", err), true, true
	}
	timeout := argInt(args, "timeout_ms")
	if timeout <= 0 {
		timeout = 180000
	}
	verb := "remove"
	if v, ok := args["purge"].(bool); ok && v {
		verb = "purge"
	}
	res, finished, err := s.runOnScreen(ctx,
		aptEnv+"apt-get "+verb+" -y "+strings.Join(pkgs, " "), timeout)
	if err != nil {
		return textContent("remove_packages: %v", err), true, true
	}
	res["log"] = tailLines(fmt.Sprint(res["stdout"]), 20)
	delete(res, "stdout")

	failed := false
	if finished {
		// This tool used to return apt's transcript and nothing else, so a
		// removal apt declined — a package another one depends on, a name that
		// matched nothing — read the same as one that worked. dpkg is asked
		// instead of believed.
		res["removed"] = dpkgState(pkgs)
		if code, ok := res["exit_code"].(int); ok && code != 0 {
			failed = true
		}
	} else {
		res["removal_pending"] = pkgs
		res["note"] = "still removing as job " + fmt.Sprint(res["job_id"]) +
			" on the shared screen. Call job_wait with this job_id; the removal was " +
			"not cancelled."
	}
	return jsonContent(res), failed, true
}

func (s *Server) toolSearchPackages(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	q := strings.TrimSpace(argStr(args, "query"))
	if q == "" {
		return textContent("search_packages: `query` is missing"), true, true
	}
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 15
	}
	// Seconds, not four minutes. The old ceiling was sized for the apt-get
	// update this used to run behind the caller's back; what is left is one
	// read of a file that is already on disk.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// apt-cache search touches no network, needs no root and changes nothing,
	// which is what lets this tool be riskRead and go unwatched.
	//
	// It used to be none of those things. Images delete /var/lib/apt/lists to
	// stay small, and rather than say so, a search that came back empty ran
	// `apt-get update` as root over the network and then said so afterwards, in
	// a `note` field, in the reply. Three separate problems in one branch:
	//
	//   - it was UNASKED. Somebody wanted to know whether a package exists and
	//     got a privileged network operation that rewrote the machine's package
	//     index. Nothing in the tool's name, description or risk level said that
	//     could happen.
	//   - it was UNWATCHED. It went through runElevated, off-screen, so the
	//     people sharing the desktop saw nothing at all.
	//   - it made the tool a LIAR about itself. A tool that can run apt-get
	//     update is not read-only, and this one was classified riskRead — so
	//     under `-mcp-policy readonly`, the level whose whole promise is that
	//     the agent changes nothing, it was allowed.
	//
	// The refresh does not move to the visible path here; it stops happening.
	// Nothing about answering "is there a package for editing video" requires
	// changing the machine, and a capability that needs asking for already
	// exists on a tool that says what it does — install_packages refreshes
	// first by default, and `sudo apt-get update` through run_command or
	// job_start runs in a window everybody can see. What is left is to be
	// honest when there is nothing to search.
	b, err := exec.CommandContext(ctx, "apt-cache", "search", "--names-only", q).Output()
	if err != nil {
		return textContent("search_packages failed: %v", err), true, true
	}
	var results []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" || len(results) >= limit {
			continue
		}
		name, desc, _ := strings.Cut(ln, " - ")
		results = append(results, map[string]any{
			"package":     name,
			"description": desc,
			"installed":   dpkgState([]string{name})[name],
		})
	}
	// An empty result over an empty index is an ERROR, not an answer.
	//
	// `count: 0` is the shape of "that package does not exist", and a model
	// reading it will tell somebody gimp is not available on Debian. A wrong
	// answer that looks like an answer is worse than a refusal: the refusal
	// gets acted on and the wrong answer gets believed.
	if len(results) == 0 && aptListsEmpty() {
		return textContent("search_packages: there is no apt index on this desktop " +
			"(/var/lib/apt/lists is empty — the image ships that way to stay small), " +
			"so there was nothing to search and this is NOT evidence that the package " +
			"does not exist. Refreshing the index is a root command that talks to the " +
			"network, so this tool will not do it on your behalf: run " +
			"`sudo apt-get update` through run_command or job_start, where the people " +
			"here can see it happen, or call install_packages directly — it refreshes " +
			"first unless you pass update:false."), true, true
	}
	return jsonContent(map[string]any{
		"query": q, "count": len(results), "results": results,
	}), false, true
}

// toolSystemUpdates answers "is this desktop behind" as one structured reply.
//
// The question was always answerable — `apt-get -s dist-upgrade` through
// run_command — but the answer arrived as a screenful of apt prose that every
// caller re-parsed with its own regex, and the same question is one the PANEL
// wants to ask too. A first-class shape serves both: the agent branches on
// `security > 0`, a dashboard draws a badge from the same JSON.
//
// Read-only for the same reasons search_packages fought to be: the simulation
// takes no lock, needs no root and touches no network, so the tool can run
// under `readonly` without lying about itself. The cost of never refreshing is
// carried honestly in the reply — the index's age is a field, not a footnote —
// because "0 updates against an index from March" and "0 updates as of this
// morning" are different answers that would otherwise look identical.
func (s *Server) toolSystemUpdates(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 30
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// The image's own identity rides along whether or not apt has anything to
	// say: "which build is this desktop" is half of "is it behind", and it is
	// the half that stays answerable on an image whose index was deleted.
	image := map[string]any{
		"version": version.Version, "commit": version.GitHash, "built": version.BuildDate,
	}

	// Same honesty rule as search_packages: an empty index yields an error, not
	// "0 pending". The zero would be read as "fully patched", which the desktop
	// has done nothing to earn.
	if aptListsEmpty() {
		return textContent("system_updates: there is no apt index on this desktop "+
			"(/var/lib/apt/lists is empty — the image ships that way to stay small), "+
			"so pending updates are UNKNOWN, not zero. This build is %s. "+
			"Refreshing the index is a root command that talks to the network, so this "+
			"tool will not do it on your behalf: run `sudo apt-get update` through "+
			"run_command or job_start, where the people here can see it happen.",
			version.String()), true, true
	}

	// The simulation, not `apt list --upgradable`: apt(8) warns that its CLI
	// has no stable interface, while the Inst/Conf/Remv lines of -s have been
	// script-fodder for decades. LC_ALL=C pins them further.
	cmd := exec.CommandContext(ctx, "apt-get", "-s", "-q", "dist-upgrade")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return textContent("system_updates: the apt simulation failed: %v", err), true, true
	}

	pending, securityCount := parseAptSimulation(string(out))

	// Security first, then the rest, so a truncated list never hides the part
	// that made somebody ask.
	listed := make([]map[string]any, 0, min(limit, len(pending)))
	for _, wantSecurity := range []bool{true, false} {
		for _, u := range pending {
			if u.security != wantSecurity || len(listed) >= limit {
				continue
			}
			listed = append(listed, map[string]any{
				"package": u.name, "from": u.from, "to": u.to, "security": u.security,
			})
		}
	}

	reply := map[string]any{
		"pending":  len(pending),
		"security": securityCount,
		"packages": listed,
		"image":    image,
	}
	if len(pending) > len(listed) {
		reply["truncated"] = fmt.Sprintf("%d more not listed — raise `limit` to see them", len(pending)-len(listed))
	}
	if updated, ok := aptIndexUpdated(); ok {
		reply["index_updated"] = updated.Format(time.RFC3339)
		reply["index_age_hours"] = int(time.Since(updated).Hours())
		reply["note"] = "counts are as of index_updated; this tool never refreshes the index"
	}
	return jsonContent(reply), false, true
}

// aptUpgrade is one pending package change, as read off an `Inst` line.
type aptUpgrade struct {
	name, from, to string
	security       bool
}

// parseAptSimulation reads the Inst lines out of `apt-get -s dist-upgrade`
// output and returns the pending upgrades plus how many come from a security
// suite. A pure function so the parsing — the part of system_updates that can
// rot when apt's wording shifts — is testable on a machine with no apt at all.
func parseAptSimulation(out string) ([]aptUpgrade, int) {
	var pending []aptUpgrade
	security := 0
	for _, ln := range strings.Split(out, "\n") {
		if !strings.HasPrefix(ln, "Inst ") {
			continue
		}
		// Inst libssl3 [3.0.11-1] (3.0.13-1 Debian-Security:12/stable-security [amd64])
		// The [old version] is absent for a package the upgrade newly pulls in.
		rest := strings.TrimPrefix(ln, "Inst ")
		name, rest, _ := strings.Cut(rest, " ")
		u := aptUpgrade{name: name}
		if strings.HasPrefix(rest, "[") {
			if end := strings.Index(rest, "]"); end > 0 {
				u.from = rest[1:end]
				rest = strings.TrimSpace(rest[end+1:])
			}
		}
		if open := strings.Index(rest, "("); open >= 0 {
			inner := rest[open+1:]
			if end := strings.Index(inner, ")"); end > 0 {
				inner = inner[:end]
			}
			u.to, _, _ = strings.Cut(inner, " ")
			u.security = strings.Contains(strings.ToLower(inner), "security")
		}
		if u.security {
			security++
		}
		pending = append(pending, u)
	}
	return pending, security
}

// aptIndexUpdated reports when the on-disk package index was last refreshed:
// the newest Packages file under /var/lib/apt/lists. False when there is none,
// which callers have already turned into an error by then.
func aptIndexUpdated() (time.Time, bool) {
	entries, err := os.ReadDir("/var/lib/apt/lists")
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), "_Packages") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest, !newest.IsZero()
}

// aptListsEmpty reports whether the image was left without a package index, the
// usual consequence of the `rm -rf /var/lib/apt/lists/*` that shrinks images.
func aptListsEmpty() bool {
	entries, err := os.ReadDir("/var/lib/apt/lists")
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), "_Packages") {
			return false
		}
	}
	return true
}

func (s *Server) toolServiceControl(ctx context.Context, args map[string]any) ([]map[string]any, bool, bool) {
	action := strings.ToLower(strings.TrimSpace(argStr(args, "action")))
	if action == "" {
		action = "status"
	}
	switch action {
	case "status", "start", "stop", "restart":
	default:
		return textContent("invalid action %q: use status, start, stop or restart", action), true, true
	}
	name := strings.TrimSpace(argStr(args, "name"))
	if name == "" {
		name = "all"
	}
	if strings.ContainsAny(name, "'\"$`;&|<>()\n\\ ") {
		return textContent("invalid service name: %q", name), true, true
	}
	// The config lives at a different path depending on how this was installed:
	// the image puts it at supervisord.conf, install.sh writes it as
	// sentineldesk.conf so it sits beside whatever else the host supervises.
	// Hardcoding the container's path meant this tool — one of the 114 — failed
	// on every native install, pointing at a file that was not there.
	//
	// First readable wins, and the container's path is checked first because
	// that is the common case. supervisord runs as root with a 0700 socket, so
	// this needs sudo either way.
	conf := "/etc/supervisor/supervisord.conf"
	if _, err := os.Stat(conf); err != nil {
		if _, err := os.Stat("/etc/supervisor/sentineldesk.conf"); err == nil {
			conf = "/etc/supervisor/sentineldesk.conf"
		}
	}
	res, err := s.runElevated(ctx,
		fmt.Sprintf("supervisorctl -c %s %s %s", conf, action, name),
		true, 60000)
	if err != nil {
		return textContent("service_control: %v", err), true, true
	}
	res["action"] = action
	res["service"] = name
	// supervisorctl writes some errors to stdout and still exits 0 — for
	// instance "Error: .ini file does not include supervisorctl section" — so
	// the exit code alone is not enough to tell success from failure.
	out := fmt.Sprint(res["stdout"]) + fmt.Sprint(res["stderr"])
	// `status` exits non-zero when any program is not running, and desktop-init
	// exits on purpose. That is not a failure of the tool.
	failed := strings.HasPrefix(strings.TrimSpace(out), "Error:") ||
		strings.Contains(out, "ERROR (no such process)") ||
		(action != "status" && res["exit_code"] != 0)
	if failed {
		res["error"] = strings.TrimSpace(out)
	}
	return jsonContent(res), failed, true
}

// tailLines keeps the last n lines: with apt, the informative part is the end.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return "…\n" + strings.Join(lines[len(lines)-n:], "\n")
}
