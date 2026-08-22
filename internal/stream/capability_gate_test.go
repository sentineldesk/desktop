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

package stream

import (
	"testing"

	"github.com/sentineldesk/desktop/pkg/capability"
)

// The human wire's control gate reads off the shared catalogue (§4.6). These
// tests hold the Room's half of that sentence: that it really answers from the
// card it was handed. The other half — that the names this wire asks about are
// on the real menu, with the answers the wires were unified around — is
// capability_parity_test.go, external because internal/mcp imports this
// package and a test inside it cannot import back.

func TestVerbGateComesFromTheCatalogue(t *testing.T) {
	r := &Room{}
	r.SetCapabilities(capability.NewCatalogue([]capability.Def{
		{Name: "publish", Risk: capability.RiskDanger,
			Visibility: capability.VisVisible, RequiresControl: true},
		{Name: "observe", Risk: capability.RiskRead},
	}))

	if !r.verbNeedsControl("publish") {
		t.Error("a gated verb came back free")
	}
	if r.verbNeedsControl("observe") {
		t.Error("an ungated verb came back gated")
	}
}

// TestNoCatalogueGatesEverything: a room nobody handed a menu answers YES for
// every verb. Conservative in the only safe direction — requiring a turn that
// was not needed inconveniences somebody; granting one that was needed
// publishes a shared desktop on nobody's authority.
func TestNoCatalogueGatesEverything(t *testing.T) {
	r := &Room{}
	for _, verb := range []string{"start_restream", "screenshot", "made_up"} {
		if !r.verbNeedsControl(verb) {
			t.Errorf("with no catalogue, %q is not gated", verb)
		}
	}
}
