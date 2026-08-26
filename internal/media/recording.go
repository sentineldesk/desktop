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
	"bytes"
	"fmt"
	"github.com/sentineldesk/desktop/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	display string

	// Overlays is what steps out of the frame for a clean take, when anything
	// is wired to it. An interface rather than the concrete type because the
	// pointers belong to the room and the recorder belongs to the tool server —
	// two things that do not otherwise know about each other, and should not
	// start to over this.
	Overlays interface {
		Hide()
		Show()
	}
	audioDevice string
	Dir         string

	mu        sync.Mutex
	cmd       *exec.Cmd
	done      chan struct{} // closed once cmd.Wait has returned
	cleaned   bool          // this recording hid the overlays, so this one restores them
	stopping  bool          // Stop asked for this exit; the reaper must not call it a death
	path      string
	container string
	startedAt time.Time

	// watchers hear about recordings starting, stopping and dying, so an event
	// subscriber does not have to poll Status for a fact this struct learns
	// first. Keyed by a token so cancellation is O(1) and double-cancel is a
	// no-op — the same contract Room.WatchPresence gives its callers.
	watchSeq int
	watchers map[int]func(kind string, detail map[string]any)
}

// Watch registers fn to be called on every recording event — kind "started",
// "stopped" or "died", each with the file's path and, for a death, the reason
// gst gave. fn runs on the recorder's own goroutines and must return quickly.
// The returned cancel is idempotent.
func (r *Recorder) Watch(fn func(kind string, detail map[string]any)) func() {
	r.mu.Lock()
	if r.watchers == nil {
		r.watchers = map[int]func(string, map[string]any){}
	}
	r.watchSeq++
	id := r.watchSeq
	r.watchers[id] = fn
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.watchers, id)
		r.mu.Unlock()
	}
}

// tell fans one event out to every watcher. Callers must NOT hold r.mu: a
// watcher is somebody else's code, and somebody else's code under our lock is
// a deadlock waiting for its second caller.
func (r *Recorder) tell(kind string, detail map[string]any) {
	r.mu.Lock()
	fns := make([]func(string, map[string]any), 0, len(r.watchers))
	for _, fn := range r.watchers {
		fns = append(fns, fn)
	}
	r.mu.Unlock()
	for _, fn := range fns {
		fn(kind, detail)
	}
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

	// Clean keeps the take clear of the things that are on the screen for the
	// benefit of whoever is watching live rather than for the recording: the
	// mouse cursor, and the name tags that say who is driving.
	//
	// The two are not the same cost. The cursor is drawn by THIS pipeline, so
	// dropping it changes the file and nothing else. The name tags are real
	// windows on the display, so hiding them hides them from everybody until
	// the recording stops — see desktop.PeerPointers.Hide.
	Clean bool

	// XID records ONE window instead of the whole screen. Zero is the screen.
	//
	// This is the answer to "something might pop up in the middle of my
	// recording", and it is a better answer than hiding things one at a time:
	// what is not inside that window cannot be in the frame, including windows
	// nobody predicted.
	XID uint32
}

// evenSize is the caps that keep the encoder alive on a window of any size.
//
// I420 stores one chroma sample per 2x2 block of luma, so a frame with an odd
// width or an odd height has no valid representation in it and x264 refuses to
// initialise at all. Not a warning and not a degraded picture — the element
// posts "Can not initialize x264 encoder", the pipeline goes to NULL about two
// milliseconds after PLAYING, and what is left on disk is a file of zero bytes.
//
// The whole screen is almost always even (1920x1080), so this was invisible
// until `window:` arrived and started pointing the source at real windows.
// Chromium on this desktop is 1271 pixels wide. Three zero-byte recordings in
// one afternoon, every one of them reported to the caller as a recording in
// progress, with a path.
//
// The range carries a STEP of 2, which is what makes this general: videoscale
// negotiates the nearest size that satisfies it — 1271 becomes 1270 — for any
// window, without this file ever having to know how big anything is. Pinning an
// exact size would have needed the geometry up front and would then have been
// wrong the moment somebody resized the window mid-take, which ximagesrc
// follows and renegotiates.
//
// Measured on the window that produced the zero-byte files: 1271x1026 in,
// 1270x1026 out, 135 frames in five seconds, no errors, playable.
const evenSize = "width=(int)[2,16384,2],height=(int)[2,16384,2]"

// Start begins recording. It fails if one is already in progress.
// pipelineFor is the gst-launch description, built apart from running it.
//
// Separate so a test can read it. What goes wrong here is invisible from the
// outside: a recording made with the wrong source still records, still writes a
// playable file, and is only wrong when somebody watches it — which is minutes
// later and usually somebody else.
func pipelineFor(display, audioDevice, mux, path, venc, aenc string, o RecordOpts) string {
	// show-pointer was hard-coded true, which is right for watching somebody
	// work and wrong for recording a video: the cursor sat over the picture for
	// the whole take with nothing to be done about it.
	source := fmt.Sprintf("ximagesrc display-name=%s show-pointer=%t use-damage=0",
		display, !o.Clean)
	if o.XID != 0 {
		source += fmt.Sprintf(" xid=%d", o.XID)
	}
	desc := fmt.Sprintf(
		"-e %s name=mux ! filesink location=%s "+
			"%s "+
			"! video/x-raw,framerate=%d/1 ! videoconvert ! videoscale "+
			"! video/x-raw,format=I420,%s ! queue ! %s ! mux.",
		mux, path, source, o.FPS, evenSize, venc)
	if o.Audio {
		desc += fmt.Sprintf(
			" pulsesrc device=%s ! audioconvert ! audioresample ! queue ! %s ! mux.",
			audioDevice, aenc)
	}
	return desc
}

// boundedBuffer keeps the last of what a child wrote to stderr, not all of it.
//
// gst is capable of producing megabytes of warnings on a pipeline that is
// otherwise fine, and this buffer lives for as long as the recording. The cap
// is generous enough to hold the error block gst prints when it gives up —
// message, debug line and element path — which is the only part anybody reads.
type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

const stderrCap = 8192

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if b.buf.Len() > stderrCap {
		keep := b.buf.Bytes()[b.buf.Len()-stderrCap:]
		trimmed := append([]byte(nil), keep...)
		b.buf.Reset()
		b.buf.Write(trimmed)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lastGstError picks the sentence worth showing out of gst's output.
//
// gst narrates: "Setting pipeline to PAUSED", "Pipeline is live", "New clock",
// and then, somewhere in the middle, the one line that says what is wrong.
// Handing the whole thing back puts nine lines of progress in front of the
// answer, and the caller is an agent that will quote it to a person.
//
// The ERROR line is taken and the rest dropped. If there is no ERROR line —
// gst killed by something outside, or a build with different wording — the
// last non-empty line is a better guess than the first.
func lastGstError(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "ERROR") {
			return l
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return strings.TrimSpace(out)
}

func (r *Recorder) Start(o RecordOpts) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		// In progress, or merely never reaped? A recorder that died on its own
		// — killed, crashed, disk full — has nobody to call Stop for it, and
		// the leftover r.cmd blocked every future recording behind a process
		// that no longer existed. Found live: Status had already learned to
		// say `recording: false` about that state while Start went on refusing
		// with "already in progress", which is two answers to one question.
		select {
		case <-r.done:
			r.reapLocked()
		default:
			return "", fmt.Errorf("a recording is already in progress: %s", r.path)
		}
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
	// Kept, because a pipeline that refuses to start says why HERE and nowhere
	// else. Without it the reason was thrown away and the caller was handed a
	// path instead.
	var stderr boundedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("gst-launch: %w", err)
	}

	// Reap it when it exits, and publish that fact through a channel rather than
	// letting Stop read cmd.ProcessState. Wait writes ProcessState from this
	// goroutine, so a Stop that polled the field would be racing it — and losing
	// that race means Stop never sees the exit, kills a process that had already
	// finished, and reports a file that gst was still writing the index into.
	// Closing a channel is the one signal both sides can see safely.
	//
	// Started BEFORE the health check below, because that check waits on this
	// channel: there is one Wait for this process and both readers need it.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
		// A death nobody asked for is the event a subscriber most needs: the
		// recording everyone believes is running has stopped being one. Stop
		// sets `stopping` before it signals, so a deliberate end stays quiet
		// here and speaks through Stop's own "stopped" event instead. The
		// window where Start is still health-checking is also quiet — Start
		// reports that failure as its OWN error, and an event on top of an
		// error would say the same thing twice.
		r.mu.Lock()
		deliberate := r.stopping
		startedOK := r.cmd == cmd // Start finished and adopted this process
		p := r.path
		r.mu.Unlock()
		if !deliberate && startedOK {
			reason := lastGstError(strings.TrimSpace(stderr.String()))
			if reason == "" {
				// SIGKILL, an OOM kill, a display yanked out — deaths that
				// leave no stderr. An empty reason reads like a bug in the
				// event; this reads like what happened.
				reason = "the process exited without an error message — killed, or the display went away"
			}
			r.tell("died", map[string]any{"path": p, "reason": reason})
		}
	}()

	// Did it actually start?
	//
	// cmd.Start() reports that the FORK succeeded. It says nothing about the
	// pipeline, and a gst pipeline that cannot be built dies about two
	// milliseconds later — an encoder that will not initialise, a source that
	// cannot open the display, a path that cannot be written. Every one of those
	// used to return this function's happy path: a filename, no error, and a
	// Recorder that believed it was recording.
	//
	// That is not a theoretical failure. It shipped, and what it produced was an
	// agent telling somebody "recording to /home/sentineldesk/Recordings/
	// rec-….mp4" three times over one afternoon while nothing was being
	// recorded at all. The agent was not wrong to believe it; this function told
	// it so. A tool that reports success for work it did not do is the failure
	// this project ranks above a crash, and this is the same waiting-briefly fix
	// the app launcher already carries, arriving late.
	//
	// 700ms is far past the couple of milliseconds a broken pipeline takes to
	// give up, and short enough that nobody notices it in a call that is about
	// to run for minutes. A pipeline still alive after it has negotiated its
	// caps and is writing frames.
	select {
	case <-done:
		why := strings.TrimSpace(stderr.String())
		if why == "" {
			why = "the pipeline exited immediately and said nothing"
		}
		// The file gst opened and never wrote to. Leaving it behind is how the
		// recordings directory fills with zero-byte mp4s that look like takes.
		if fi, err := os.Stat(path); err == nil && fi.Size() == 0 {
			_ = os.Remove(path)
		}
		return "", fmt.Errorf("the recording did not start: %s", lastGstError(why))
	case <-time.After(700 * time.Millisecond):
	}

	r.cmd = cmd
	r.done = done
	r.path = path
	r.container = o.Container
	r.startedAt = time.Now()
	r.stopping = false
	// From a goroutine, NEVER inline or deferred: Start still holds r.mu — the
	// unlock deferred at the top has not run, and defers run in reverse order,
	// so a deferred tell fires BEFORE it — and tell takes the same lock to
	// snapshot the watchers. As a defer this deadlocked the whole recorder on
	// the first real start after the event shipped: every recording tool on
	// the desktop hung behind a mutex nobody was ever going to release. tell's
	// own comment said callers must not hold r.mu; a defer was not the
	// exemption it looked like.
	go r.tell("started", map[string]any{"path": path})

	// Hidden AFTER the process is up, so a pipeline that fails to start does
	// not leave the room without its pointers. Remembered rather than inferred
	// from the options later, because Stop must put back exactly what Start
	// took away — and a recording started clean while something else had
	// already hidden them is not this one's to restore.
	r.cleaned = false
	if o.Clean && r.Overlays != nil {
		r.Overlays.Hide()
		r.cleaned = true
	}

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
	desc := pipelineFor(r.display, r.audioDevice, mux, path, venc, aenc, o)
	return SplitArgs(desc), nil
}

// Stop ends the recording cleanly: SIGINT -> EOS -> the file is finalised.
// reapLocked clears the state of a recorder whose process is already gone,
// and puts back what the recording took: pointers hidden for a clean take
// would otherwise stay hidden forever, because the Show lived only in Stop
// and Stop is exactly the call a self-inflicted death never gets.
//
// Caller holds r.mu — which is why the Show runs from a goroutine: Overlays
// is somebody else's code, and somebody else's code under our lock is the
// same shape that deadlocked the "started" event. Asynchronous restore is
// fine; pointers reappearing a scheduler-tick late is invisible.
func (r *Recorder) reapLocked() {
	r.cmd = nil
	r.done = nil
	r.path = ""
	restore := r.cleaned
	r.cleaned = false
	if restore && r.Overlays != nil {
		go r.Overlays.Show()
	}
}

func (r *Recorder) Stop() (string, int64, error) {
	r.mu.Lock()
	cmd := r.cmd
	done := r.done
	path := r.path
	r.mu.Unlock()
	if cmd == nil {
		return "", 0, fmt.Errorf("no recording in progress")
	}
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()

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
	restore := r.cleaned
	r.cleaned = false
	r.mu.Unlock()

	// The screen goes back to telling people who is driving. Done here rather
	// than left to whoever asked, because a recording can end in ways nobody
	// asked for — a stop from the toolbar, a run cut off — and pointers that
	// stayed hidden after one of those would be a permanent change made by a
	// temporary request.
	if restore && r.Overlays != nil {
		r.Overlays.Show()
	}

	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	r.tell("stopped", map[string]any{"path": path, "size_bytes": size})
	return path, size, nil
}

// Status reports whether a recording is running, and how far along it is.
func (r *Recorder) Status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return map[string]any{"recording": false}
	}
	// Alive, not merely started. `r.cmd != nil` only says a recording was once
	// begun, and it stayed true for a gst that had already died — so the one
	// call an agent has for CHECKING a recording answered `recording: true`
	// about a file nothing was writing to. A tool that cannot be checked with
	// is worse than no tool: it converts a careful caller into a confident
	// wrong one.
	//
	// r.done is closed by the reaper the moment Wait returns, so this is the
	// same fact Stop uses, read without racing it.
	select {
	case <-r.done:
		path := r.path
		// Reaped here as well as in Start, because this may be the only call
		// that ever comes: it restores the pointers a clean take hid, and it
		// leaves the recorder ready instead of claiming "in progress" forever.
		r.reapLocked()
		return map[string]any{
			"recording": false,
			"path":      path,
			"stopped":   "the recorder exited on its own — the file is not being written",
		}
	default:
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

// Fire delivers one event to every watcher, exactly as the recorder's own
// paths do. It exists so the layer above — the event hub, its tools — can test
// its wiring against a real Recorder without needing a gst process to die on
// cue. Production code has no business calling it: the recorder already knows
// when its own events happen.
func (r *Recorder) Fire(kind string, detail map[string]any) { r.tell(kind, detail) }
