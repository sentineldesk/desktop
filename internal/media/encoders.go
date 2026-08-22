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
	"log"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/tinyzimmer/go-gst/gst"
)

// EncoderStrategy describes how to encode video: the pipeline fragment, the RTP
// codec it produces, and how to steer its bitrate at runtime.
type EncoderStrategy struct {
	Name        string // nvenc | vaapi | x264 | vp8
	Hardware    bool
	MimeType    string // webrtc.MimeTypeH264 | webrtc.MimeTypeVP8
	BitrateProp string // the encoder element's bitrate property
	BitrateBPS  bool   // true when that property is in bps, otherwise kbps
}

// h264Tee is where an external destination gets its copy of the picture.
//
// It sits straight after the encoder and before h264parse, so each branch parses
// for itself: WebRTC wants byte-stream, FLV wants AVC, and asking one parser to
// produce both would deadlock the negotiation. Parsing is nearly free — it reads
// headers, it does not decode — so the duplication costs nothing next to what a
// second encoder would.
//
// allow-not-linked keeps the pipeline running with no destination attached,
// which is the normal case.
const h264Tee = "! tee name=vtee allow-not-linked=true " +
	"! queue max-size-buffers=4 leaky=downstream "

// Fragment returns the encoder-to-payloader stretch of the pipeline. The
// encoder element is always named "venc" so it can be looked up and adjusted
// while the pipeline is running.
func (e EncoderStrategy) Fragment(kbps, fps int) string {
	switch e.Name {
	case "nvenc":
		// Upload and colour conversion on the GPU (CUDA), then NVENC in CBR at
		// very low latency.
		return fmt.Sprintf(
			"cudaupload ! cudaconvert ! "+
				"nvh264enc name=venc bitrate=%d gop-size=%d rc-mode=cbr zerolatency=true "+
				h264Tee+
				"! h264parse ! rtph264pay pt=96 config-interval=-1 aggregate-mode=zero-latency "+
				"! application/x-rtp,media=video,encoding-name=H264,payload=96",
			kbps, fps*10)
	case "vaapi":
		// Colour conversion on the GPU (VA-API) plus the driver's H.264 encoder.
		return fmt.Sprintf(
			"vapostproc ! "+
				"vah264enc name=venc rate-control=cbr bitrate=%d key-int-max=%d target-usage=7 "+
				h264Tee+
				"! h264parse ! rtph264pay pt=96 config-interval=-1 aggregate-mode=zero-latency "+
				"! application/x-rtp,media=video,encoding-name=H264,payload=96",
			kbps, fps*10)
	case "x264":
		// sliced-threads: intra-frame parallelism. x264's default threading
		// pipelines whole FRAMES across the cores, which buys throughput by
		// adding a frame of delay — the wrong trade for a desktop someone is
		// driving by hand. Slicing each frame instead keeps the latency
		// bounded and costs a little compression. tune=zerolatency implies
		// it, but it is stated explicitly so this choice survives anybody's
		// future retuning of that knob; the roadmap once carried "verify
		// which mode we run" as an open question, answered here (2026-08-20).
		return fmt.Sprintf(
			"videoconvert ! "+
				"x264enc name=venc tune=zerolatency speed-preset=veryfast bitrate=%d "+
				"threads=8 sliced-threads=true "+
				"key-int-max=%d bframes=0 "+
				"! video/x-h264,profile=constrained-baseline "+
				h264Tee+
				"! h264parse ! rtph264pay pt=96 config-interval=-1 aggregate-mode=zero-latency "+
				"! application/x-rtp,media=video,encoding-name=H264,payload=96",
			kbps, fps*10)
	default: // vp8
		// Tuned for desktop content. Keyframes are spaced far apart because
		// the client asks for one via PLI whenever it actually needs it — a
		// keyframe every second was pushing quantisation to the point where
		// text turned to mush. Quantiser bounds keep text crisp, and generous
		// buffers absorb bursts without the encoder paying for them in
		// quality.
		// Scale down before encoding when there is no hardware encoder.
		//
		// 1080p30 VP8 in software does not fit: measured on a 20-core host the
		// encoder sat at 153% CPU and still only delivered 17 fps. The frames it
		// could not produce turned into stalls on the client, the congestion
		// estimator read those stalls as a slow network, and the bitrate spiralled
		// to 300 kbps — at which point the picture is worthless anyway.
		//
		// Encoding at 720p costs about 2.25x less and the browser scales it back
		// up. Text is a little softer; the alternative was a slideshow minutes
		// behind reality.
		//
		// SOFTWARE_SCALE_HEIGHT=0 turns this off for a host that can take it.
		scale := ""
		if h := config.Int("SOFTWARE_SCALE_HEIGHT", 0); h > 0 {
			scale = fmt.Sprintf("videoscale method=nearest-neighbour ! "+
				"video/x-raw,height=%d,pixel-aspect-ratio=1/1 ! ", h)
		}
		return fmt.Sprintf(
			"%svideoconvert ! "+
				"vp8enc name=venc deadline=1 cpu-used=8 end-usage=cbr target-bitrate=%d "+
				"keyframe-max-dist=%d error-resilient=default lag-in-frames=0 threads=8 "+
				"buffer-initial-size=500 buffer-optimal-size=600 buffer-size=1000 "+
				"min-quantizer=2 max-quantizer=40 "+
				"! rtpvp8pay pt=96 picture-id-mode=15-bit "+
				"! application/x-rtp,media=video,encoding-name=VP8,payload=96",
			scale, kbps*1000, fps*10)
	}
}

func strategyByName(name string) EncoderStrategy {
	switch name {
	case "nvenc":
		return EncoderStrategy{Name: "nvenc", Hardware: true, MimeType: webrtc.MimeTypeH264, BitrateProp: "bitrate"}
	case "vaapi":
		return EncoderStrategy{Name: "vaapi", Hardware: true, MimeType: webrtc.MimeTypeH264, BitrateProp: "bitrate"}
	case "x264", "h264":
		return EncoderStrategy{Name: "x264", MimeType: webrtc.MimeTypeH264, BitrateProp: "bitrate"}
	default:
		return EncoderStrategy{Name: "vp8", MimeType: webrtc.MimeTypeVP8, BitrateProp: "target-bitrate", BitrateBPS: true}
	}
}

// probeStrategy runs a real throwaway pipeline (videotestsrc → encoder →
// fakesink). If the hardware or the driver is missing it fails here, at start-up
// and in one place, instead of halfway through someone's session.
func probeStrategy(s EncoderStrategy) error {
	desc := fmt.Sprintf(
		"videotestsrc num-buffers=5 ! video/x-raw,width=320,height=240,framerate=30/1 ! %s ! fakesink sync=false",
		s.Fragment(1000, 30))
	pipeline, err := gst.NewPipelineFromString(desc)
	if err != nil {
		return err
	}
	defer pipeline.SetState(gst.StateNull)
	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		return err
	}
	bus := pipeline.GetPipelineBus()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPopFiltered(200*time.Millisecond,
			gst.MessageEOS|gst.MessageError)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			return nil
		case gst.MessageError:
			return msg.ParseError()
		}
	}
	return fmt.Errorf("timed out probing %s", s.Name)
}

// SelectEncoder resolves ENCODER=auto|nvenc|vaapi|h264|x264|vp8.
// On auto it tries hardware first (NVENC → VA-API), then x264, then software VP8.
func SelectEncoder(pref string) EncoderStrategy {
	pref = strings.ToLower(strings.TrimSpace(pref))
	if pref != "" && pref != "auto" {
		s := strategyByName(pref)
		if err := probeStrategy(s); err != nil {
			log.Printf("WARNING: the requested encoder %q does not work (%v); carrying on", pref, err)
		}
		return s
	}
	// x264 before VP8. Measured on this desktop content, libx264 at veryfast
	// carries 1080p for roughly a third of what vp8enc needs, and H.264 is
	// what every browser decodes in hardware — which matters as much on the
	// receiving side, where a client that cannot keep up is read as a congested
	// network and drags the whole room's bitrate down.
	for _, name := range []string{"nvenc", "vaapi", "x264"} {
		s := strategyByName(name)
		if err := probeStrategy(s); err != nil {
			log.Printf("encoder %s unavailable: %v", name, err)
			continue
		}
		// Say which KIND it is, not just its name. This line read "hardware
		// encoder detected: x264" — and x264 is software, so the one message
		// somebody reads when they are asking exactly this question was
		// answering it wrong. On a machine where the GPU never appeared, the
		// log said it had.
		kind := "software"
		if s.Hardware {
			kind = "hardware"
		}
		log.Printf("using the %s encoder %s", kind, name)
		return s
	}
	log.Printf("no hardware encoder: falling back to software VP8")
	return strategyByName("vp8")
}
