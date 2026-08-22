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

// Sending the desktop somewhere else while people are watching it.
//
// A destination is its own gst-launch CHILD: a second capture of the same
// display, a second encoder, muxed and pushed to the URL. This used to be a
// tee off the live encode — one capture, one encode, a branch per destination
// — and that design was undone by its own cleverness, three ways at once:
//
//   - The pointer. The live capture deliberately excludes the cursor (the
//     driver's browser draws its own at zero latency; viewers get a named
//     overlay), but a broadcast without a cursor is a desktop where things
//     click themselves. Compositing the cursor into the shared encode while
//     streaming put a second, delayed cursor on every participant's screen —
//     seen in production within a day and reported as a fault, because it is
//     one.
//
//   - The bitrate. The shared encode follows congestion control at the
//     MINIMUM across the room's viewers, so one participant on bad wifi
//     dragged the public broadcast down with them. A child encodes at its own
//     fixed rate; the room's adaptation stays the room's.
//
//   - The doctrine. Recording, screenshots and the roomless fallback are
//     deliberately gst-launch children — built from parameters somebody
//     chose, the ones that can fail on a bad combination, kept where a fault
//     cannot take the room down. The in-process restream was the one
//     exception, defended by the saved encode; the two points above are what
//     that encode actually cost.
//
// The price is a second x264 at the streaming resolution while a broadcast
// runs — the same price the recorder has always paid, for the same reason,
// and only while somebody is actually live. What it buys beyond the fixes: a
// VP8 session can restream now (the child brings its own H.264), and a
// destination that dies takes a child process with it, never a branch of the
// pipeline everyone is watching.

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// RestreamTarget is one destination as the caller describes it.
type RestreamTarget struct {
	ID       string // caller's handle, used to stop this one destination
	Platform string // youtube | twitch | facebook | custom — labelling only
	URL      string
	Audio    bool

	// KeyframeSec is how often this destination needs a keyframe.
	//
	// It is a property of the destination because it answers a question about
	// the audience: can a viewer arrive at an arbitrary moment? The platforms
	// serve people who click play whenever they like and mandate one every two
	// seconds. 0 means the destination did not ask — but a child pipeline has
	// no PLI to answer, so "nothing" becomes a sparse interval rather than
	// none at all: a player that connects must eventually see a picture.
	KeyframeSec int
}

// RestreamInfo is a running destination, for reporting.
type RestreamInfo struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
	Audio    bool   `json:"audio"`
	Seconds  int    `json:"seconds"`
}

// ChildRestreams runs and tracks the destination children.
type ChildRestreams struct {
	// The capture's coordinates, set once by whoever builds the room.
	Display     string
	AudioDevice string
	Kbps        int // the child's own fixed rate; the room's GCC has no say
	FPS         int

	// OnError reports a destination that died on its own — a rejected key, a
	// host that went away — so the people in the room find out instead of
	// watching a "live" badge that means nothing.
	OnError func(id string, err error)

	mu      sync.Mutex
	running map[string]*childStream
}

type childStream struct {
	target  RestreamTarget
	cmd     *exec.Cmd
	stderr  *bytes.Buffer
	started time.Time
	done    chan struct{}
	stopped bool
}

func NewChildRestreams(display, audioDevice string, kbps, fps int) *ChildRestreams {
	return &ChildRestreams{
		Display: display, AudioDevice: audioDevice, Kbps: kbps, FPS: fps,
		running: map[string]*childStream{},
	}
}

// Start launches one destination's child. It returns once the process is
// running; whether the far end accepts the stream is known a moment later, so
// a refusal arrives through OnError rather than here.
func (c *ChildRestreams) Start(t RestreamTarget) error {
	if t.ID == "" {
		return fmt.Errorf("a destination needs an id")
	}
	desc, err := childDesc(t, c.Display, c.AudioDevice, c.Kbps, c.FPS)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.running[t.ID]; exists {
		return fmt.Errorf("already streaming to %s", t.ID)
	}

	cmd := exec.Command("gst-launch-1.0", append([]string{"-e"}, SplitArgs(desc)...)...)
	cmd.Env = append(os.Environ(), "DISPLAY="+c.Display)
	// Its own process group, so stopping kills the whole gst-launch tree and
	// a wedged child never has to be reasoned with individually.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The last words matter: an RTMP handshake refused for a bad key says so
	// on stderr, and that sentence is the difference between "failed" and a
	// person fixing their key.
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start the stream: %w", err)
	}

	cs := &childStream{
		target: t, cmd: cmd, stderr: stderr,
		started: time.Now(), done: make(chan struct{}),
	}
	c.running[t.ID] = cs

	go func() {
		err := cmd.Wait()
		close(cs.done)
		c.mu.Lock()
		stopped := cs.stopped
		delete(c.running, t.ID)
		c.mu.Unlock()
		if stopped {
			return // a person ended it; nothing to report
		}
		// The child died on its own: the destination refused or vanished.
		reason := lastLine(stderr.String())
		if reason == "" && err != nil {
			reason = err.Error()
		}
		if reason == "" {
			reason = "the stream ended unexpectedly"
		}
		log.Printf("restream: %s failed: %s", t.ID, reason)
		if c.OnError != nil {
			c.OnError(t.ID, fmt.Errorf("%s", reason))
		}
	}()

	log.Printf("restream: %s → %s (audio=%v, %d kbps, own capture)",
		t.ID, redact(t.URL), t.Audio, c.Kbps)
	return nil
}

// Stop ends one destination and leaves everything else running.
func (c *ChildRestreams) Stop(id string) error {
	c.mu.Lock()
	cs, ok := c.running[id]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("not streaming to %s", id)
	}
	cs.stopped = true
	c.mu.Unlock()

	// SIGINT to the group: with -e, gst-launch emits EOS and the mux closes
	// the stream properly. A child that will not die gets killed — a truncated
	// broadcast tail is nobody's tragedy, unlike a zombie holding the display.
	_ = syscall.Kill(-cs.cmd.Process.Pid, syscall.SIGINT)
	select {
	case <-cs.done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-cs.cmd.Process.Pid, syscall.SIGKILL)
		<-cs.done
	}
	log.Printf("restream: %s stopped", id)
	return nil
}

// StopAll is what the room calls when it shuts its pipelines down.
func (c *ChildRestreams) StopAll() {
	c.mu.Lock()
	ids := make([]string, 0, len(c.running))
	for id := range c.running {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		_ = c.Stop(id)
	}
}

// List reports what is running. URLs leave redacted: the last path segment of
// an RTMP address IS the credential, and this list goes over the wire to every
// browser in the room.
func (c *ChildRestreams) List() []RestreamInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]RestreamInfo, 0, len(c.running))
	for _, cs := range c.running {
		out = append(out, RestreamInfo{
			ID:       cs.target.ID,
			Platform: cs.target.Platform,
			URL:      redact(cs.target.URL),
			Audio:    cs.target.Audio,
			Seconds:  int(time.Since(cs.started).Seconds()),
		})
	}
	return out
}

// childDesc builds the gst-launch description for one destination.
//
// The three schemes cover the two audiences separately. RTMP is what the
// platforms accept and nothing else; SRT and plain UDP are for a receiver you
// run yourself — VLC and OBS take either. show-pointer=true is the point of
// the whole child: an outbound broadcast carries the real cursor, which the
// live capture deliberately leaves out.
func childDesc(t RestreamTarget, display, audioDevice string, kbps, fps int) (string, error) {
	raw := strings.TrimSpace(t.URL)
	if strings.ContainsAny(raw, " \t\n") {
		return "", fmt.Errorf("a destination URL cannot contain spaces")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("that destination is not a URL: %w", err)
	}

	var mux, sink string
	switch strings.ToLower(u.Scheme) {
	case "rtmp", "rtmps":
		mux = "flvmux name=mux streamable=true"
		sink = fmt.Sprintf("rtmpsink location=%s sync=false async=false", raw)
	case "srt":
		mux = "mpegtsmux name=mux"
		sink = fmt.Sprintf("srtsink uri=%s sync=false async=false wait-for-connection=false", raw)
	case "udp":
		host, portStr, err := net.SplitHostPort(u.Host)
		if err != nil {
			return "", fmt.Errorf("udp needs host:port, e.g. udp://192.168.1.20:5000: %w", err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			return "", fmt.Errorf("%q is not a port", portStr)
		}
		mux = "mpegtsmux name=mux"
		sink = fmt.Sprintf("udpsink host=%s port=%d sync=false async=false", host, port)
	default:
		return "", fmt.Errorf("unsupported destination %q: use rtmp://, rtmps://, srt:// or udp://", u.Scheme)
	}

	// A child has no PLI to answer, so keyframes are periodic or nothing ever
	// starts: a destination that asked for nothing still gets a sparse one,
	// because a player that connects must eventually see a picture.
	kfSec := t.KeyframeSec
	if kfSec <= 0 {
		kfSec = 10
	}

	desc := fmt.Sprintf(
		"%s ! %s "+
			"ximagesrc display-name=%s show-pointer=true use-damage=0 "+
			"! video/x-raw,framerate=%d/1 ! videoconvert "+
			"! queue max-size-buffers=4 leaky=downstream "+
			"! x264enc tune=zerolatency speed-preset=veryfast bitrate=%d key-int-max=%d "+
			"! h264parse ! queue ! mux.",
		mux, sink, display, fps, kbps, fps*kfSec)
	if t.Audio && audioDevice != "" {
		desc += fmt.Sprintf(
			" pulsesrc device=%s ! audio/x-raw,rate=48000,channels=2 "+
				"! audioconvert ! audioresample ! queue "+
				"! avenc_aac bitrate=128000 ! aacparse ! queue ! mux.",
			audioDevice)
	}
	return desc, nil
}

// lastLine pulls the final non-empty line out of a child's stderr — where
// gst-launch puts the sentence that explains the death.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 300 {
				l = l[:300]
			}
			return l
		}
	}
	return ""
}

// redact hides the stream key.
//
// The last path segment of an RTMP URL IS the credential: anyone holding it can
// broadcast to that channel. It goes through logs and over the wire to every
// browser in the room, so it does not travel whole.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return raw
	}
	i := strings.LastIndex(u.Path, "/")
	if i < 0 || i == len(u.Path)-1 {
		return raw
	}
	key := u.Path[i+1:]
	if len(key) <= 4 {
		u.Path = u.Path[:i+1] + "•••"
	} else {
		u.Path = u.Path[:i+1] + key[:2] + "•••" + key[len(key)-2:]
	}
	return u.String()
}
