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

package desktop

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
)

// GrabToFile captures the screen — or a region when w and h are positive — to a
// PNG file.
//
// It reuses GStreamer rather than adding a screenshot library: the pipeline
// ximagesrc num-buffers=1 -> pngenc -> filesink already does exactly this, and
// the plugins are in the image because the streaming path needs them anyway.
func GrabToFile(display, path string, x, y, w, h int) error {
	args := []string{"-q", "ximagesrc", "num-buffers=1",
		"display-name=" + display, "show-pointer=true"}
	if w > 0 && h > 0 {
		// ximagesrc treats endx/endy as inclusive, hence the -1.
		args = append(args,
			fmt.Sprintf("startx=%d", x), fmt.Sprintf("starty=%d", y),
			fmt.Sprintf("endx=%d", x+w-1), fmt.Sprintf("endy=%d", y+h-1))
	}
	args = append(args, "!", "videoconvert", "!", "pngenc", "!", "filesink", "location="+path)

	cmd := exec.Command("gst-launch-1.0", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gst grab: %v (%s)", err, string(out))
	}
	return nil
}

// GrabForOCR captures at twice the size, scaled with lanczos.
//
// Tesseract is markedly more accurate on small UI text when the glyphs are
// bigger; upscaling before OCR took recognition confidence from unusable to
// around 90% on menus and labels. Callers must divide the resulting coordinates
// by two to get back to screen space.
func GrabForOCR(display, path string, x, y, w, h, screenW, screenH int) error {
	outW, outH := screenW*2, screenH*2
	args := []string{"-q", "ximagesrc", "num-buffers=1",
		"display-name=" + display, "show-pointer=false"}
	if w > 0 && h > 0 {
		args = append(args,
			fmt.Sprintf("startx=%d", x), fmt.Sprintf("starty=%d", y),
			fmt.Sprintf("endx=%d", x+w-1), fmt.Sprintf("endy=%d", y+h-1))
		outW, outH = w*2, h*2
	}
	args = append(args,
		"!", "videoconvert",
		"!", "videoscale", "method=lanczos",
		"!", fmt.Sprintf("video/x-raw,width=%d,height=%d", outW, outH),
		"!", "videoconvert",
		"!", "pngenc", "!", "filesink", "location="+path)

	cmd := exec.Command("gst-launch-1.0", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gst grab ocr: %v (%s)", err, string(out))
	}
	return nil
}

// GrabPNG captures to a temporary file and returns the PNG as base64.
func GrabPNG(display string, x, y, w, h int) (string, error) {
	tmp, err := os.CreateTemp("", "sentineldesk-shot-*.png")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	if err := GrabToFile(display, path, x, y, w, h); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty capture")
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// GrabScreenshotPNG captures the whole screen as base64 PNG.
func GrabScreenshotPNG(display string) (string, error) {
	return GrabPNG(display, 0, 0, 0, 0)
}

// GrabRegionPNG captures a rectangle of the screen as base64 PNG.
func GrabRegionPNG(display string, x, y, w, h int) (string, error) {
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("width and height must be greater than zero")
	}
	return GrabPNG(display, x, y, w, h)
}
