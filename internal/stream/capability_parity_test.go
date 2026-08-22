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

package stream_test

import (
	"testing"

	"github.com/sentineldesk/desktop/internal/mcp"
	"github.com/sentineldesk/desktop/internal/stream"
)

// TestTheHumanWireSpeaksTheMenusNames is the parity check between the
// DataChannel adapter and the real catalogue, in the same spirit as the
// Go↔TypeScript wire test: the names session.go consults must exist on the
// menu, and the menu's answers must be the ones this wire's behaviour was
// unified around — publishing needs the turn, capturing does not. A rename on
// either side, or someone quietly re-gating recording for one wire only,
// fails here with the verb's name in the message.
func TestTheHumanWireSpeaksTheMenusNames(t *testing.T) {
	menu := mcp.Catalogue()

	gated := map[string]bool{
		// Restreaming publishes what is on everyone's screen to somewhere
		// outside the room; both wires hold it to the controller's turn.
		"start_restream": true,
		"stop_restream":  true,
		// Capturing is not driving: the agent has always been free to
		// screenshot and record without holding the turn, and after §4.6 the
		// person is too. The recorder itself refuses a second start.
		"screenshot":      false,
		"start_recording": false,
		"stop_recording":  false,
	}

	check := func(table map[string]string, name string) {
		for wire, verb := range table {
			want, expected := gated[verb]
			if !expected {
				t.Errorf("%s[%q]=%q is not a verb this test expects — update the table", name, wire, verb)
				continue
			}
			if !menu.Known(verb) {
				t.Errorf("the catalogue does not know %q, which the human wire consults for %q", verb, wire)
				continue
			}
			if got := menu.RequiresControl(verb); got != want {
				t.Errorf("%q: catalogue says RequiresControl=%v, the wires were unified around %v", verb, got, want)
			}
		}
	}
	check(stream.CaptureVerbForTest, "captureVerb")
	check(stream.RestreamVerbForTest, "restreamVerb")
}
