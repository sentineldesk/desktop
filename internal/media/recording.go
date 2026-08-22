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

package media

import (
	"fmt"
	"github.com/sentineldesk/desktop/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Recorder writes the screen (and audio) to a file using a separate gst-launch
// process, running alongside the WebRTC stream — ximagesrc happily serves more
// than one reader.
//
// A separate process, while the live pipeline is in-process through go-gst, and
// the asymmetry is the point rather than an inconsistency left over from
// somewhere. This pipeline is assembled from what the caller asked for: a codec,
// a container, a bitrate, a path — and start_recording is an MCP tool, so those
// come from an agent. A combination this host cannot satisfy, or a disk that
// fills halfway through, ends the child and nothing else. In-process the same
// fault would be in the address space serving every viewer, and a recording
// nobody is watching would have taken down the stream they are.
//
// The -e flag makes gst send EOS on SIGINT so the container is finalised
// properly (the mp4 moov atom, the webm index): without it the file is left
// corrupt.
type Recorder struct {
	display     string
	audioDevice string
	Dir         string

	mu        sync.Mutex
	cmd       *exec.Cmd
	done      chan struct{} // closed once cmd.Wait has returned
	path      string
	container string
	startedAt time.Time
}

func NewRecorder(display, audioDevice, dir string) *Recorder {
	if dir == "" {
		dir = "/home/sentineldesk/Recordings"
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Recorder{display: display, audioDevice: audioDevice, Dir: dir}
}

type RecordOpts struct {
	Container string // mp4 | webm | mkv
	Codec     string // h264 | vp8 | vp9
	FPS       int
	Kbps      int
	Audio     bool
	Path      string // optional; generated inside dir when empty
}

// Start begins recording. It fails if one is already in progress.
func (r *Recorder) Start(o RecordOpts) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return "", fmt.Errorf("a recording is already in progress: %s", r.path)
	}

	if o.Container == "" {
		o.Container = "mp4"
	}
	if o.FPS <= 0 {
		o.FPS = 30
	}
	if o.Kbps <= 0 {
		o.Kbps = 4000
	}
	path := o.Path
	if path == "" {
		name := "rec-" + time.Now().Format("20060102-150405") + "." + o.Container
		path = filepath.Join(r.Dir, name)
	}

	args, err := r.buildArgs(o, path)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("gst-launch-1.0", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+r.display)
	// Its own process group, so the signal reaches gst and nothing else.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("gst-launch: %w", err)
	}
	r.cmd = cmd
	r.path = path
	r.container = o.Container
	r.startedAt = time.Now()

	// Reap it when it exits, and publish that fact through a channel rather than
	// letting Stop read cmd.ProcessState. Wait writes ProcessState from this
	// goroutine, so a Stop that polled the field would be racing it — and losing
	// that race means Stop never sees the exit, kills a process that had already
	// finished, and reports a file that gst was still writing the index into.
	// Closing a channel is the one signal both sides can see safely.
	done := make(chan struct{})
	r.done = done
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return path, nil
}

func (r *Recorder) buildArgs(o RecordOpts, path string) ([]string, error) {
	// Pin the encoder's thread count instead of leaving it at the default.
	//
	// Both x264enc and vp8enc treat threads=0 as "work it out from the host",
	// and what they work out is sized for finishing one frame as fast as
	// possible. A recording does not need that: it needs to keep pace with 30fps
	// and nothing more, and every thread beyond that spends real CPU on
	// coordination while making prediction worse. Measured on a 20-core host
	// against a scrolling terminal, the default cost 281% of a core and pinning
	// it to 2 cost 148% — the same 449 of 450 frames out the other end.
	//
	// 2 rather than 1 because the floor is not the target. One thread is cheaper
	// again on an idle desktop (88%) but drops 34 frames in 450 as soon as the
	// screen actually moves, and a recorder that thins out precisely when
	// something is happening is worse than one that costs more.
	//
	// RECORD_THREADS exists because the right number depends on the host, and
	// the default being wrong here is exactly what this comment is about.
	threads := max(config.Int("RECORD_THREADS", 2), 1)

	var mux, venc, aenc string
	switch o.Container {
	case "webm":
		mux = "webmmux"
		venc = fmt.Sprintf("vp8enc deadline=1 cpu-used=4 threads=%d target-bitrate=%d keyframe-max-dist=%d", threads, o.Kbps*1000, o.FPS*2)
		aenc = "opusenc"
	case "mkv":
		mux = "matroskamux"
		venc = fmt.Sprintf("x264enc tune=zerolatency speed-preset=veryfast threads=%d bitrate=%d key-int-max=%d ! h264parse", threads, o.Kbps, o.FPS*2)
		aenc = "avenc_aac bitrate=128000"
	default: // mp4
		mux = "mp4mux"
		venc = fmt.Sprintf("x264enc tune=zerolatency speed-preset=veryfast threads=%d bitrate=%d key-int-max=%d ! h264parse", threads, o.Kbps, o.FPS*2)
		aenc = "avenc_aac bitrate=128000"
	}

	// Pin the chroma to 4:2:0, which is not what negotiation picks on its own.
	//
	// ximagesrc hands out BGRx and x264enc accepts Y444 as happily as I420, so
	// videoconvert takes the conversion that is cheapest for videoconvert: the
	// one that discards nothing. The recording then comes out as High 4:4:4
	// Predictive — twice the chroma to encode, and a profile that almost no
	// hardware decoder will touch, so the file plays back in software or not at
	// all on the phones and TVs most likely to open it. Nobody asked for that;
	// it is what happens when neither end of the link states a preference.
	//
	// Measured against a scrolling terminal it also cost 130% of a core where
	// I420 costs 98%. Better compatibility and a third less CPU, from naming the
	// format the file was always supposed to be in.
	desc := fmt.Sprintf(
		"-e %s name=mux ! filesink location=%s "+
			"ximagesrc display-name=%s show-pointer=true use-damage=0 "+
			"! video/x-raw,framerate=%d/1 ! videoconvert "+
			"! video/x-raw,format=I420 ! queue ! %s ! mux.",
		mux, path, r.display, o.FPS, venc)
	if o.Audio {
		desc += fmt.Sprintf(
			" pulsesrc device=%s ! audioconvert ! audioresample ! queue ! %s ! mux.",
			r.audioDevice, aenc)
	}
	return SplitArgs(desc), nil
}

// Stop ends the recording cleanly: SIGINT -> EOS -> the file is finalised.
func (r *Recorder) Stop() (string, int64, error) {
	r.mu.Lock()
	cmd := r.cmd
	done := r.done
	path := r.path
	r.mu.Unlock()
	if cmd == nil {
		return "", 0, fmt.Errorf("no recording in progress")
	}

	// SIGINT to gst-launch: with -e it emits EOS and writes the container index.
	_ = cmd.Process.Signal(syscall.SIGINT)

	// Wait for it to finish, but not forever. Writing the index of a long
	// recording takes a moment, and cutting that short is what produces the
	// unplayable file this whole dance exists to avoid.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill() // did not stop: kill it, and the file may be truncated
		<-done                 // Wait still has to run, or the process stays a zombie
	}

	r.mu.Lock()
	r.cmd = nil
	r.done = nil
	r.path = ""
	r.mu.Unlock()

	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	return path, size, nil
}

// Status reports whether a recording is running, and how far along it is.
func (r *Recorder) Status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return map[string]any{"recording": false}
	}
	var size int64
	if fi, err := os.Stat(r.path); err == nil {
		size = fi.Size()
	}
	return map[string]any{
		"recording":  true,
		"path":       r.path,
		"container":  r.container,
		"seconds":    int(time.Since(r.startedAt).Seconds()),
		"size_bytes": size,
	}
}

// SplitArgs splits a pipeline description into arguments on whitespace (paths and
// element names never contain spaces in the pipelines we build).
func SplitArgs(s string) []string {
	var out []string
	for _, tok := range splitFields(s) {
		out = append(out, tok)
	}
	return out
}

func splitFields(s string) []string {
	var fields []string
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if cur != "" {
				fields = append(fields, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		fields = append(fields, cur)
	}
	return fields
}
