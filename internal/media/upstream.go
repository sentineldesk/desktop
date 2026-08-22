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

// Audio on the RETURN path: from the browser into the desktop.
//
// Until this existed audio only travelled outward. With the return path the
// desktop becomes usable for a video call, or for any application that expects a
// microphone: whatever the browser captures shows up inside as another device.
//
//	microphone → incoming Opus track → GStreamer → virtual PulseAudio source
//
// Two PulseAudio objects make that work, and both matter:
//
//   - a null sink, which the GStreamer pipeline writes into;
//   - a source REMAPPED from that sink's monitor.
//
// The remap is not decoration. A monitor is how PulseAudio exposes "what a sink
// is playing", and applications treat it as such: browsers list monitors apart
// from real inputs, or hide them from the microphone picker altogether. Remapped,
// the same audio presents as an ordinary capture device, which is what a page
// asking for a microphone will accept.
//
// Both are created at startup rather than when somebody starts sharing. A page
// enumerates its devices when it loads; a microphone that only materialises later
// is not there when it counts.

import (
	"fmt"
	"github.com/sentineldesk/desktop/pkg/config"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/pion/webrtc/v4"
)

// upstreamSinkName is the null sink whose .monitor acts as the virtual
// microphone. Desktop applications see it as just another capture source.
const (
	// The sink the pipeline writes into, and the source applications record from.
	upstreamSinkName   = "sentineldesk_mic"
	upstreamSourceName = "sentineldesk_mic_in"
)

// Upstream receives the tracks the browser sends and pours them into the desktop.
type Upstream struct {
	cfg config.Config

	mu    sync.Mutex
	audio *MediaPipeline
	owner string // memberID of whoever is publishing
	ready bool   // the virtual microphone exists
}

func NewUpstream(cfg config.Config) *Upstream {
	return &Upstream{cfg: cfg}
}

// EnsureMic builds the virtual microphone: the sink the pipeline writes into,
// and the remapped source applications record from.
//
// It is idempotent, and it makes the source the system default. Without that
// last step a page calling getUserMedia with no explicit device gets whatever
// PulseAudio considers default — here, the monitor of the desktop's own output,
// which is the desktop's loudspeakers, not the person talking. The symptom is a
// microphone test that shows a signal only when the desktop happens to be making
// noise, which is a confusing way to discover the wrong device was picked.
func (u *Upstream) EnsureMic() error {
	if out, err := exec.Command("pactl", "list", "short", "sinks").Output(); err == nil &&
		strings.Contains(string(out), upstreamSinkName) {
		u.ready = true
		return nil
	}
	if err := exec.Command("pactl", "load-module", "module-null-sink",
		"sink_name="+upstreamSinkName,
		"sink_properties=device.description=SentinelDeskMic").Run(); err != nil {
		return fmt.Errorf("creating the microphone sink: %w", err)
	}

	// The rate and channel count have to be spelled out: without them the module
	// refuses to initialise rather than inferring them from its master.
	if err := exec.Command("pactl", "load-module", "module-remap-source",
		"master="+upstreamSinkName+".monitor",
		"source_name="+upstreamSourceName,
		"channels=2", "rate=48000", "format=s16le",
		"source_properties=device.description=SentinelDeskMicrophone").Run(); err != nil {
		return fmt.Errorf("creating the microphone source: %w", err)
	}

	// A failure here is not fatal: the device still exists and can be chosen by
	// hand, it simply is not the one picked automatically.
	if err := exec.Command("pactl", "set-default-source", upstreamSourceName).Run(); err != nil {
		log.Printf("upstream: could not make %s the default source: %v", upstreamSourceName, err)
	}
	u.ready = true
	log.Printf("upstream: virtual microphone ready (%s)", upstreamSourceName)
	return nil
}

// Available reports what this installation can actually receive.
func (u *Upstream) Available() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	res := map[string]any{
		"microphone": true,
		"mic_source": upstreamSourceName,
		"mic_ready":  u.ready,
	}
	if u.owner != "" {
		res["publisher"] = u.owner
	}
	return res
}

// StartAudio starts the pipeline that pours the microphone track into the sink.
//
// The incoming track is Opus over RTP: depayload, decode, hand to PulseAudio.
// The appsrc is fed the raw RTP packets the session reads off the track.
func (u *Upstream) StartAudio(owner string, feed func(func([]byte))) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.audio != nil {
		return fmt.Errorf("a microphone is already publishing (%s)", u.owner)
	}
	if err := u.EnsureMic(); err != nil {
		return err
	}
	desc := fmt.Sprintf(
		"appsrc name=src is-live=true format=time do-timestamp=true "+
			"caps=application/x-rtp,media=audio,encoding-name=OPUS,clock-rate=48000,payload=111 "+
			"! rtpjitterbuffer latency=60 ! rtpopusdepay ! opusdec plc=true "+
			"! audioconvert ! audioresample ! pulsesink device=%s sync=false",
		upstreamSinkName)

	pipe, err := NewAppSrcPipeline("upstream-audio", desc)
	if err != nil {
		return err
	}
	if err := pipe.Start(); err != nil {
		return fmt.Errorf("microphone pipeline: %w", err)
	}
	u.audio = pipe
	u.owner = owner
	feed(pipe.Push)
	log.Printf("upstream: microphone from %s → %s", owner, upstreamSourceName)
	return nil
}

// Stop releases whatever that participant was publishing.
func (u *Upstream) Stop(owner string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.owner != owner {
		return
	}
	if u.audio != nil {
		u.audio.Stop()
		u.audio = nil
	}
	u.owner = ""
	log.Printf("upstream: %s stopped publishing", owner)
}

// CodecOf returns the negotiated codec name for an incoming track.
func CodecOf(track *webrtc.TrackRemote) string {
	return track.Codec().MimeType
}
