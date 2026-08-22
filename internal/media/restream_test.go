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

// childDesc is the part of the restream that can rot quietly: a gst-launch
// description is a string until the child parses it, and a wrong element or a
// smuggled argument fails at broadcast time on somebody's real stream. These
// run anywhere — the builder is pure; only the child needs a display.

import (
	"strings"
	"testing"
)

func TestEachSchemeBuildsItsOwnChild(t *testing.T) {
	cases := []struct {
		url  string
		want []string
	}{
		{"rtmp://a.rtmp.youtube.com/live2/KEY", []string{"flvmux", "rtmpsink location=rtmp://a.rtmp.youtube.com/live2/KEY"}},
		{"srt://192.168.1.20:5000", []string{"mpegtsmux", "srtsink uri=srt://192.168.1.20:5000"}},
		{"udp://172.17.0.5:5000", []string{"mpegtsmux", "udpsink host=172.17.0.5 port=5000"}},
	}
	for _, c := range cases {
		desc, err := childDesc(RestreamTarget{ID: "x", URL: c.url}, ":0", "", 3000, 30)
		if err != nil {
			t.Fatalf("%s refused: %v", c.url, err)
		}
		for _, want := range append(c.want, "show-pointer=true", "x264enc") {
			if !strings.Contains(desc, want) {
				t.Errorf("%s: description lacks %q:\n%s", c.url, want, desc)
			}
		}
	}
}

func TestTheBadDestinationsAreRefusedByName(t *testing.T) {
	for _, url := range []string{
		"utp://172.17.0.5:5000",         // the typo that cost a debugging session
		"udp://172.17.0.5",              // no port
		"udp://172.17.0.5:notaport",     // not a port
		"rtmp://host/app/key with size", // a space would smuggle arguments into argv
		"file:///tmp/x.ts",              // not a streaming destination
	} {
		if _, err := childDesc(RestreamTarget{ID: "x", URL: url}, ":0", "", 3000, 30); err == nil {
			t.Errorf("%s was accepted", url)
		}
	}
}

func TestKeyframesAreNeverZeroInAChild(t *testing.T) {
	// A child has no PLI to answer, so "no keyframes" must become a sparse
	// interval — a player that connects has to eventually see a picture.
	desc, err := childDesc(RestreamTarget{ID: "x", URL: "udp://10.0.0.1:5000"}, ":0", "", 3000, 30)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(desc, "key-int-max=0") {
		t.Fatal("a destination that asked for nothing got key-int-max=0")
	}
	if !strings.Contains(desc, "key-int-max=300") { // 30 fps × 10 s
		t.Errorf("expected the sparse default of 10s, got:\n%s", desc)
	}
}

func TestAudioOnlyRidesWhenAskedAndPossible(t *testing.T) {
	with, _ := childDesc(RestreamTarget{ID: "x", URL: "udp://10.0.0.1:5000", Audio: true}, ":0", "mysink.monitor", 3000, 30)
	if !strings.Contains(with, "pulsesrc device=mysink.monitor") {
		t.Error("audio was asked for and not included")
	}
	without, _ := childDesc(RestreamTarget{ID: "x", URL: "udp://10.0.0.1:5000", Audio: false}, ":0", "mysink.monitor", 3000, 30)
	if strings.Contains(without, "pulsesrc") {
		t.Error("audio rode along uninvited")
	}
}
