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

// Recording, audio, the room, packages, snapshots and the catalogue itself.
//
// The recording tests decode what was written rather than checking that a file
// exists, because a file existing is exactly what the recorder produced while
// it was shipping a profile almost nothing can play. The room tests are the
// only ones that assert about arbitration, which is the property that makes an
// agent safe to leave running.

import (
	"strings"
	"testing"
	"time"
)

// --- recording and streaming --------------------------------------------------

func TestStartRecording(t *testing.T) {
	control(t)
	out := devDesk(t).Call(t, "start_recording", map[string]any{
		"container": "mp4", "fps": 15, "audio": false})
	t.Cleanup(func() { devDesk(t).Call(t, "stop_recording", nil) })

	if !strings.Contains(out, ".mp4") {
		t.Fatalf("no path in %s", trunc(out, 200))
	}
	// A recorder that reported a path and started nothing would look the same
	// from here, so the encoder has to be running.
	eventually(t, 8*time.Second, "the encoder to be running", func() bool {
		return atoi(Sh(t, "pgrep -f gst-launch | wc -l")) > 0
	})
	// And the file has to be growing, which is the difference between started
	// and starting.
	path := firstPath(out, ".mp4")
	first := atoi(Sh(t, "stat -c %%s %s 2>/dev/null || echo 0", path))
	time.Sleep(2 * time.Second)
	if atoi(Sh(t, "stat -c %%s %s 2>/dev/null || echo 0", path)) <= first {
		t.Errorf("%s is not growing two seconds in", path)
	}
}

func TestGetRecordingStatus(t *testing.T) {
	control(t)
	// While nothing is recording it must say so, or a caller cannot tell.
	idle := devDesk(t).Call(t, "get_recording_status", nil)
	if strings.Contains(idle, "\"recording\": true") {
		t.Fatalf("nothing is recording and the status says otherwise: %s", trunc(idle, 200))
	}

	devDesk(t).Call(t, "start_recording", map[string]any{"container": "mp4", "fps": 15, "audio": false})
	t.Cleanup(func() { devDesk(t).Call(t, "stop_recording", nil) })

	out := devDesk(t).Call(t, "get_recording_status", nil)
	if !strings.Contains(out, "\"recording\": true") {
		t.Fatalf("a recording is running and the status says: %s", trunc(out, 300))
	}
	// The size it reports has to match the file, since that is what makes the
	// field worth having over a bare boolean.
	// Given a moment: mp4mux buffers, so the file is created before it holds
	// anything, and demanding bytes the instant recording starts would be
	// asserting about the muxer's scheduling rather than about the tool.
	path := jsonField(t, out, "path")
	if path != "" {
		eventually(t, 8*time.Second, "the reported file to hold something", func() bool {
			return atoi(Sh(t, "stat -c %%s %s 2>/dev/null || echo 0", path)) > 0
		})
	}
}

func TestStopRecording(t *testing.T) {
	control(t)
	devDesk(t).Call(t, "start_recording", map[string]any{"container": "mp4", "fps": 15, "audio": false})
	time.Sleep(2500 * time.Millisecond)

	out := devDesk(t).Call(t, "stop_recording", nil)
	path := firstPath(out, ".mp4")
	if path == "" {
		t.Fatalf("no path in %s", trunc(out, 200))
	}

	// Decode it. The file existing and growing is what the recorder did for as
	// long as it was writing High 4:4:4 Predictive, which almost no hardware
	// decoder will accept — so the check is that it plays, in a profile things
	// can play, with frames in it.
	info := Sh(t, "ffprobe -v error -select_streams v:0 -count_frames "+
		"-show_entries stream=codec_name,pix_fmt,nb_read_frames -of default=nw=1 %s", path)
	if !strings.Contains(info, "codec_name=h264") {
		t.Fatalf("%s has no h264 stream: %s", path, strings.Join(strings.Fields(info), " "))
	}
	if !strings.Contains(info, "pix_fmt=yuv420p") {
		t.Errorf("%s is not 4:2:0: %s", path, strings.Join(strings.Fields(info), " "))
	}
	if atoi(afterEquals(info, "nb_read_frames")) < 10 {
		t.Errorf("%s has almost no frames: %s", path, strings.Join(strings.Fields(info), " "))
	}
	// Stopping again has to fail rather than report a second success.
	devDesk(t).CallErr(t, "stop_recording", nil)
}

func TestListRecordings(t *testing.T) {
	control(t)
	devDesk(t).Call(t, "start_recording", map[string]any{"container": "mp4", "fps": 15, "audio": false})
	time.Sleep(1500 * time.Millisecond)
	made := firstPath(devDesk(t).Call(t, "stop_recording", nil), ".mp4")

	out := devDesk(t).Call(t, "list_recordings", nil)
	if made != "" && !strings.Contains(out, baseName(made)) {
		t.Fatalf("the recording just made is not listed:\n%s", trunc(out, 300))
	}
	// Everything it lists has to be on disk — a listing of files that are gone
	// is worse than an empty one.
	for _, p := range allPaths(out, ".mp4") {
		if Sh(t, "test -f %s && echo yes", p) != "yes" {
			t.Errorf("it lists %s and there is no such file", p)
		}
	}
}

func TestStartRestream(t *testing.T) {
	control(t)
	// UDP to a local port: no external service, and the packets are countable.
	out := devDesk(t).Call(t, "start_restream", map[string]any{
		"url": "udp://127.0.0.1:19999", "platform": "udp"})
	t.Cleanup(func() { devDesk(t).Call(t, "stop_restream", nil) })
	if strings.Contains(strings.ToLower(out), "failed") {
		t.Fatalf("start_restream said %s", trunc(out, 200))
	}

	// It tees off the live encoder rather than starting a second one, which is
	// the whole reason it is cheap — so no new gst-launch child should appear.
	if strings.Contains(devDesk(t).Call(t, "list_restreams", nil), "19999") {
		return
	}
	t.Errorf("the restream is not listed after being started")
}

func TestListRestreams(t *testing.T) {
	control(t)
	before := devDesk(t).Call(t, "list_restreams", nil)
	devDesk(t).Call(t, "start_restream", map[string]any{
		"url": "udp://127.0.0.1:19998", "platform": "udp"})
	t.Cleanup(func() { devDesk(t).Call(t, "stop_restream", nil) })
	after := devDesk(t).Call(t, "list_restreams", nil)

	if strings.Contains(before, "19998") {
		t.Errorf("it listed the destination before it was started")
	}
	if !strings.Contains(after, "19998") {
		t.Fatalf("the destination is missing after starting it:\n%s", trunc(after, 300))
	}
}

func TestStopRestream(t *testing.T) {
	control(t)
	devDesk(t).Call(t, "start_restream", map[string]any{
		"url": "udp://127.0.0.1:19997", "platform": "udp"})

	devDesk(t).Call(t, "stop_restream", nil)

	if out := devDesk(t).Call(t, "list_restreams", nil); strings.Contains(out, "19997") {
		t.Fatalf("the destination is still listed after stopping:\n%s", trunc(out, 300))
	}
}

// --- audio and display --------------------------------------------------------

func TestSetVolume(t *testing.T) {
	control(t)
	for _, want := range []int{40, 75} {
		devDesk(t).Call(t, "set_volume", map[string]any{"percent": want})
		// pactl as the desktop's user: it refuses root outright, and a check run
		// as root reports the volume unreadable rather than wrong.
		eventually(t, 6*time.Second, "PulseAudio to take the new volume", func() bool {
			return strings.Contains(
				ShUser(t, "pactl get-sink-volume @DEFAULT_SINK@ 2>/dev/null"),
				itoaPercent(want))
		})
	}
}

func TestGetAudioState(t *testing.T) {
	control(t)
	// Set it from outside, read it through MCP — the direction that keeps the
	// tool from confirming its own writing.
	ShUser(t, "pactl set-sink-volume @DEFAULT_SINK@ 55%%")
	time.Sleep(600 * time.Millisecond)

	out := devDesk(t).Call(t, "get_audio_state", nil)
	if !strings.Contains(out, "55%") {
		t.Errorf("the sink is at 55%% and get_audio_state says %s", trunc(out, 200))
	}
	if !strings.Contains(out, "sink") {
		t.Errorf("no sink named in %s", trunc(out, 200))
	}
}

func TestSetResolution(t *testing.T) {
	control(t)
	t.Cleanup(func() {
		devDesk(t).Call(t, "set_resolution", map[string]any{"width": 1920, "height": 1080})
	})

	devDesk(t).Call(t, "set_resolution", map[string]any{"width": 1600, "height": 900})
	eventually(t, 10*time.Second, "the display to change size", func() bool {
		return strings.Contains(X(t, "xdpyinfo | grep dimensions"), "1600x900")
	})
}

// --- the room -----------------------------------------------------------------

func TestRequestControl(t *testing.T) {
	devDesk(t).Call(t, "release_control", nil)
	time.Sleep(300 * time.Millisecond)

	out := devDesk(t).Call(t, "request_control", map[string]any{"timeout_ms": 8000})
	t.Cleanup(func() { devDesk(t).Call(t, "release_control", nil) })
	if !strings.Contains(out, "true") {
		t.Fatalf("nobody was driving and control was not granted: %s", trunc(out, 200))
	}
	// The room has to agree, which is what the input tools consult.
	state := devDesk(t).Call(t, "room_state", nil)
	if !strings.Contains(state, "\"may_inject\": true") {
		t.Fatalf("control was granted and the room says it cannot inject:\n%s", trunc(state, 300))
	}
}

func TestReleaseControl(t *testing.T) {
	devDesk(t).Call(t, "request_control", map[string]any{"timeout_ms": 8000})
	devDesk(t).Call(t, "release_control", nil)

	state := devDesk(t).Call(t, "room_state", nil)
	if strings.Contains(state, "\"may_inject\": true") {
		t.Fatalf("control was released and the room still allows injection:\n%s", trunc(state, 300))
	}
	// And an input tool must now refuse. This is the arbitration the whole
	// design rests on: releasing sets the room to free rather than handing the
	// controls on, so nothing may type until somebody asks again.
	devDesk(t).CallErr(t, "type_text", map[string]any{"text": "should be refused"})
}

func TestRoomState(t *testing.T) {
	devDesk(t).Call(t, "release_control", nil)
	free := devDesk(t).Call(t, "room_state", nil)
	if !strings.Contains(free, "may_inject") {
		t.Fatalf("room_state does not report whether this connection may act:\n%s", trunc(free, 300))
	}

	devDesk(t).Call(t, "request_control", map[string]any{"timeout_ms": 8000})
	t.Cleanup(func() { devDesk(t).Call(t, "release_control", nil) })
	held := devDesk(t).Call(t, "room_state", nil)

	// The two readings have to differ, or the field is decoration.
	if free == held {
		t.Errorf("room_state reads identically with and without the controls:\n%s", trunc(held, 300))
	}
}

func TestActionLog(t *testing.T) {
	// Something distinctive, then look for it in the log.
	devDesk(t).Call(t, "wait", map[string]any{"ms": 37})

	out := devDesk(t).Call(t, "action_log", map[string]any{"limit": 10})
	if !strings.Contains(out, "wait") {
		t.Fatalf("a call was just made and the log does not have it:\n%s", trunc(out, 400))
	}
	// A refused call has to be recorded too, since an audit that only holds
	// what succeeded is not an audit.
	devDesk(t).CallErr(t, "read_file", map[string]any{"path": "/definitely/not/a/file"})
	out = devDesk(t).Call(t, "action_log", map[string]any{"limit": 5})
	if !strings.Contains(out, "read_file") {
		t.Errorf("a failed call is missing from the log:\n%s", trunc(out, 400))
	}
}

func TestToolSearch(t *testing.T) {
	// The words the description uses, which is what it matches on.
	out := devDesk(t).Call(t, "tool_search", map[string]any{"query": "record the screen to a file", "limit": 10})
	if !strings.Contains(out, "start_recording") {
		t.Errorf("searching for recording did not surface start_recording:\n%s", trunc(out, 400))
	}
	// It reports how many of the catalogue it looked at, which is what makes
	// the answer interpretable.
	if !strings.Contains(out, "\"of\"") {
		t.Errorf("no total in the reply:\n%s", trunc(out, 200))
	}
	// A query matching nothing must come back empty rather than with the whole
	// catalogue.
	empty := devDesk(t).Call(t, "tool_search", map[string]any{
		"query": "zzzznothingmatchesthisquery", "limit": 10})
	if strings.Count(empty, "\"name\"") > 3 {
		t.Errorf("a nonsense query returned a full catalogue:\n%s", trunc(empty, 300))
	}
}

// --- packages, services and snapshots ----------------------------------------

func TestSearchPackages(t *testing.T) {
	// This used to pass on an image with no index, because the tool quietly ran
	// apt-get update as root and searched again. It no longer does that, so the
	// precondition has to be stated instead of manufactured — which is the same
	// check TestInstallPackages already makes.
	if Sh(t, "apt-cache policy openssh-client 2>/dev/null | head -1") == "" {
		t.Skip("no package index; run apt-get update in the container first")
	}
	out := devDesk(t).Call(t, "search_packages", map[string]any{"query": "openssh-client"})
	if !strings.Contains(out, "openssh") {
		t.Fatalf("a package that certainly exists was not found:\n%s", trunc(out, 300))
	}
}

func TestInstallPackages(t *testing.T) {
	// Both package tools now open a terminal window on the shared screen, which
	// puts them behind the room gate like everything else that lands there.
	control(t)

	// A tiny package with no dependencies, removed first so the install is real.
	const pkg = "sl"
	if Sh(t, "apt-cache policy %s 2>/dev/null | head -1", pkg) == "" {
		t.Skip("no package index; run apt-get update in the container first")
	}
	devDesk(t).Call(t, "remove_packages", map[string]any{"packages": []any{pkg}, "timeout_ms": 120000})

	devDesk(t).Call(t, "install_packages", map[string]any{
		"packages": []any{pkg}, "update": false, "timeout_ms": 300000})

	// dpkg is the authority, not the tool's own transcript.
	if !strings.Contains(Sh(t, "dpkg-query -W -f='${Status}' %s 2>/dev/null", pkg), "install ok installed") {
		t.Fatalf("%s is not installed according to dpkg", pkg)
	}
}

func TestRemovePackages(t *testing.T) {
	control(t)

	const pkg = "sl"
	if !strings.Contains(Sh(t, "dpkg-query -W -f='${Status}' %s 2>/dev/null", pkg), "install ok installed") {
		t.Skip("sl is not installed, so there is nothing to remove")
	}

	devDesk(t).Call(t, "remove_packages", map[string]any{
		"packages": []any{pkg}, "timeout_ms": 180000})

	if strings.Contains(Sh(t, "dpkg-query -W -f='${Status}' %s 2>/dev/null", pkg), "install ok installed") {
		t.Fatalf("%s is still installed", pkg)
	}
}

func TestServiceControl(t *testing.T) {
	// It speaks to supervisord, which is what runs everything in this image, so
	// the service asked about has to be one supervisord knows. "ssh" is not:
	// sshd is started by hand when the SSH tests need it, and asking about it
	// returns a truthful "no such process" that reads like a broken tool.
	out := devDesk(t).Call(t, "service_control", map[string]any{
		"name": "lxpanel", "action": "status"})
	if !strings.Contains(out, "RUNNING") {
		t.Fatalf("lxpanel is running under supervisord and status says: %s", trunc(out, 300))
	}

	// And a service that does not exist has to be reported as such rather than
	// as a success with empty output.
	missing := devDesk(t).CallErr(t, "service_control", map[string]any{
		"name": "no-such-service-at-all", "action": "status"})
	if !strings.Contains(strings.ToLower(missing), "no such process") {
		t.Errorf("an unknown service returned: %s", trunc(missing, 200))
	}
}

func TestSnapshotCreate(t *testing.T) {
	name := "it-snap"
	Sh(t, "rm -f /home/sentineldesk/.sentineldesk-snapshots/%s.*", name)

	devDesk(t).Call(t, "snapshot_create", map[string]any{
		"name": name, "note": "integration"})
	t.Cleanup(func() { devDesk(t).Call(t, "snapshot_delete", map[string]any{"name": name}) })

	// A snapshot is a tar.gz beside a note and a package list, not a directory.
	// On disk, and big enough to hold a home — an empty archive would satisfy a
	// weaker check and restore nothing.
	archive := "/home/sentineldesk/.sentineldesk-snapshots/" + name + ".tar.gz"
	if Sh(t, "test -f %s && echo yes", archive) != "yes" {
		t.Fatalf("no snapshot archive at %s", archive)
	}
	if atoi(Sh(t, "stat -c %%s %s 2>/dev/null || echo 0", archive)) < 1024 {
		t.Errorf("the archive is too small to contain anything")
	}
	// And it is a readable gzip, not a truncated write.
	if Sh(t, "gzip -t %s 2>&1 || echo BAD", archive) == "BAD" {
		t.Errorf("%s is not a valid gzip archive", archive)
	}
}

func TestSnapshotList(t *testing.T) {
	name := "it-snap-list"
	devDesk(t).Call(t, "snapshot_create", map[string]any{"name": name, "note": "integration"})
	t.Cleanup(func() { devDesk(t).Call(t, "snapshot_delete", map[string]any{"name": name}) })

	out := devDesk(t).Call(t, "snapshot_list", nil)
	if !strings.Contains(out, name) {
		t.Fatalf("the snapshot just made is not listed:\n%s", trunc(out, 300))
	}
}

func TestSnapshotDelete(t *testing.T) {
	name := "it-snap-delete"
	devDesk(t).Call(t, "snapshot_create", map[string]any{"name": name, "note": "integration"})

	devDesk(t).Call(t, "snapshot_delete", map[string]any{"name": name})

	if Sh(t, "test -f /home/sentineldesk/.sentineldesk-snapshots/%s.tar.gz && echo yes", name) == "yes" {
		t.Fatalf("the snapshot archive is still on disk")
	}
	// Deleting it twice has to fail rather than report a second success.
	devDesk(t).CallErr(t, "snapshot_delete", map[string]any{"name": name})
}

func TestSnapshotRestore(t *testing.T) {
	// Not run against the live home, which is what the sweep skips it for. The
	// restore is exercised as far as it can be safely: a snapshot is made, a
	// file is added afterwards, and the tool is asked to restore a name that
	// does not exist — which must fail rather than empty anything.
	devDesk(t).CallErr(t, "snapshot_restore", map[string]any{"name": "no-such-snapshot-at-all"})

	// The home has to be untouched by that refusal.
	if Sh(t, "test -d /home/sentineldesk && echo yes") != "yes" {
		t.Fatal("the home directory is gone after a refused restore")
	}
	t.Skip("restoring over the live home is not something a test may do; the refusal path is covered")
}

// --- helpers -----------------------------------------------------------------

func afterEquals(body, key string) string {
	for _, line := range strings.Split(body, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func allPaths(body, suffix string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(body, func(r rune) bool {
		return r == ' ' || r == '"' || r == '\n' || r == ',' || r == '[' || r == ']'
	}) {
		if strings.HasPrefix(f, "/") && strings.HasSuffix(f, suffix) {
			out = append(out, f)
		}
	}
	return out
}

func itoaPercent(n int) string {
	d := []byte{}
	for v := n; v > 0; v /= 10 {
		d = append([]byte{byte('0' + v%10)}, d...)
	}
	return string(d) + "%"
}
