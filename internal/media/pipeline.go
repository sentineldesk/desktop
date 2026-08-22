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
	"log"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

// MediaPipeline is a GStreamer pipeline whose RTP leaves through an appsink into
// a callback, which pushes it onto a Pion track. Unlike gst-launch, this keeps
// the encoder reachable at runtime: bitrate changes, keyframes on demand in
// answer to a PLI, and destinations attached and detached while it runs.
type MediaPipeline struct {
	pipeline *gst.Pipeline
	encoder  *gst.Element // nil on audio
	ratecap  *gst.Element // the live FPS cap; video capture only
	vqueue   *gst.Element // the leaky queue feeding the encoder; video capture only
	src      *app.Source  // inbound pipelines only (the microphone)
	Strategy EncoderStrategy
}

// NewMediaPipeline builds `desc ! appsink name=rtpsink` and delivers every
// RTP packet to the callback. The callback gets its own copy, because Pion's
// interceptors hold on to packets for NACK retransmission and the GStreamer
// buffer is unmapped as soon as we return.
func NewMediaPipeline(kind, desc string, onPacket func([]byte)) (*MediaPipeline, error) {
	full := desc + " ! appsink name=rtpsink emit-signals=false sync=false max-buffers=64 drop=true"
	pipeline, err := gst.NewPipelineFromString(full)
	if err != nil {
		return nil, fmt.Errorf("pipeline %s: %w", kind, err)
	}

	elem, err := pipeline.GetElementByName("rtpsink")
	if err != nil {
		return nil, fmt.Errorf("appsink %s: %w", kind, err)
	}
	sink := app.SinkFromElement(elem)
	sink.SetCallbacks(&app.SinkCallbacks{
		NewSampleFunc: func(s *app.Sink) gst.FlowReturn {
			sample := s.PullSample()
			if sample == nil {
				return gst.FlowEOS
			}
			buffer := sample.GetBuffer()
			if buffer == nil {
				return gst.FlowOK
			}
			mapInfo := buffer.Map(gst.MapRead)
			if mapInfo == nil {
				return gst.FlowOK
			}
			packet := make([]byte, len(mapInfo.Bytes()))
			copy(packet, mapInfo.Bytes())
			buffer.Unmap()
			onPacket(packet)
			return gst.FlowOK
		},
	})

	mp := &MediaPipeline{pipeline: pipeline}
	if enc, err := pipeline.GetElementByName("venc"); err == nil {
		mp.encoder = enc
	}
	// Both optional, like venc: an audio pipeline has neither, and a video
	// description that dropped them would simply lose the live quality knob
	// rather than fail to build.
	if rc, err := pipeline.GetElementByName("ratecap"); err == nil {
		mp.ratecap = rc
	}
	if q, err := pipeline.GetElementByName("vq"); err == nil {
		mp.vqueue = q
	}
	return mp, nil
}

// NewAppSrcPipeline is the other direction: `appsrc ! …` fed from Go with the
// RTP packets arriving from the browser. The client's microphone and camera use
// it — they come in over WebRTC and go out to PulseAudio or v4l2.
func NewAppSrcPipeline(kind, desc string) (*MediaPipeline, error) {
	pipeline, err := gst.NewPipelineFromString(desc)
	if err != nil {
		return nil, fmt.Errorf("pipeline %s: %w", kind, err)
	}
	elem, err := pipeline.GetElementByName("src")
	if err != nil {
		return nil, fmt.Errorf("appsrc %s: %w", kind, err)
	}
	return &MediaPipeline{
		pipeline: pipeline,
		src:      app.SrcFromElement(elem),
	}, nil
}

// Push injects an RTP packet into the appsrc. On a capture pipeline there is no
// appsrc and this does nothing, so it is safe to call unconditionally.
func (mp *MediaPipeline) Push(pkt []byte) {
	if mp.src == nil {
		return
	}
	buf := gst.NewBufferFromBytes(pkt)
	if buf == nil {
		return
	}
	mp.src.PushBuffer(buf)
}

func (mp *MediaPipeline) Start() error {
	return mp.pipeline.SetState(gst.StatePlaying)
}

func (mp *MediaPipeline) Stop() {
	mp.pipeline.SetState(gst.StateNull)
}

// ForceKeyFrame asks the encoder for an immediate keyframe, which is how a PLI
// from the client gets answered without waiting for the next GOP.
func (mp *MediaPipeline) ForceKeyFrame() {
	if mp.encoder == nil {
		return
	}
	structure := gst.NewStructure("GstForceKeyUnit")
	structure.SetValue("all-headers", true)
	mp.encoder.SendEvent(gst.NewCustomEvent(gst.EventTypeCustomUpstream, structure))
}

// SetFPSCap adjusts the live framerate ceiling: the videorate element drops
// frames beyond it before they cost an encode.
//
// The cap is a named CAPSFILTER after the videorate, not videorate's own
// max-rate property — and that distinction was bought with a live placebo.
// max-rate is only read while caps are negotiated, so writing it on a
// running element succeeds, logs nothing, and drops nothing; the dial spent
// a while "working" because the saturated encoder happened to deliver fewer
// frames on its own, which is exactly the observation that had appeared to
// verify it. Rewriting a capsfilter's caps is different: it forces a live
// renegotiation, videorate re-reads the target rate, and the drop actually
// happens. The stream is not interrupted — only the framerate field of the
// caps changes downstream.
func (mp *MediaPipeline) SetFPSCap(fps int) {
	if mp.ratecap == nil || fps <= 0 {
		return
	}
	caps := gst.NewCapsFromString(fmt.Sprintf("video/x-raw,framerate=%d/1", fps))
	if caps == nil {
		log.Printf("could not build caps for the %d fps cap", fps)
		return
	}
	if err := mp.ratecap.SetProperty("caps", caps); err != nil {
		log.Printf("could not cap the framerate at %d: %v", fps, err)
	}
}

// QueuePressure reports how many buffers sit in the queue feeding the video
// encoder — the quality controller's whole signal. Zero means the encoder
// keeps up; at the queue's small capacity, frames are waiting and the encoder
// is behind, whoever is actually eating the CPU.
func (mp *MediaPipeline) QueuePressure() int {
	if mp.vqueue == nil {
		return 0
	}
	v, err := mp.vqueue.GetProperty("current-level-buffers")
	if err != nil {
		return 0
	}
	if n, ok := v.(uint); ok {
		return int(n)
	}
	return 0
}

// SetBitrateKbps adjusts the encoder's bitrate at runtime, driven by congestion
// control. Each encoder spells the property in its own unit, hence the strategy
// carrying both the name and whether it wants bits or kilobits.
func (mp *MediaPipeline) SetBitrateKbps(kbps int) {
	if mp.encoder == nil || kbps <= 0 {
		return
	}
	var err error
	if mp.Strategy.BitrateBPS {
		err = mp.encoder.SetProperty(mp.Strategy.BitrateProp, kbps*1000)
	} else {
		err = mp.encoder.SetProperty(mp.Strategy.BitrateProp, uint(kbps))
	}
	if err != nil {
		log.Printf("could not set the bitrate to %d kbps: %v", kbps, err)
	}
}
