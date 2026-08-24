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

// The name tag's SIZE, which is the part that was never recomputed.
//
// No X server here: measure is arithmetic over font metrics, and with none
// loaded it falls back to a fixed advance — which is exactly the path a test
// can walk. What cannot be checked without a display is the resize itself, and
// that is one ConfigureWindow away from these numbers.

import "testing"

// TestTheTagIsSizedForTheNameItActuallyHas.
//
// # The failure
//
// A pointer is created by the first movement that arrives, and a name can
// arrive after it. Every dimension — the window, the tag, and the SHAPE mask
// that decides which pixels exist at all — was computed once, in create, from
// whatever name was known then. Renaming stored the new string and redrew into
// the old bounds, so a peer whose name landed second kept a tag too narrow for
// it and the mask clipped the rest away.
//
// What was left on screen was a coloured arrow with a blank bar stuck to it.
func TestTheTagIsSizedForTheNameItActuallyHas(t *testing.T) {
	p := &PeerPointers{}

	emptyW, _, emptyTotal, _, _ := p.measure("")
	nameW, _, nameTotal, _, _ := p.measure("Federico Pereira")

	if nameW <= emptyW {
		t.Errorf("a tag for %q is %d wide and an empty one is %d — "+
			"the name is not being measured at all", "Federico Pereira", nameW, emptyW)
	}
	if nameTotal <= emptyTotal {
		t.Error("the window did not grow with its tag, so the mask clips the name away")
	}

	// A longer name needs a wider tag than a shorter one. This is the property
	// the bug broke: not that measuring works, but that the ANSWER travels.
	short, _, _, _, _ := p.measure("Ana")
	long, _, _, _, _ := p.measure("Ana Maria Rodriguez")
	if long <= short {
		t.Errorf("a 19-character name measured %d and a 3-character one %d", long, short)
	}

	// Height does not depend on the name — a tag that grew taller for a longer
	// name would be a different bug, and the baseline has to stay put.
	_, h1, _, _, b1 := p.measure("Ana")
	_, h2, _, _, b2 := p.measure("Ana Maria Rodriguez")
	if h1 != h2 || b1 != b2 {
		t.Errorf("height/baseline moved with the name: %d/%d vs %d/%d", h1, b1, h2, b2)
	}
}

// TestTheAgentsColourIsNeverDealtToAPerson.
//
// The violet means "this is not a person". A rotation that could hand it out
// would make that promise breakable by the fifth person to join.
func TestTheAgentsColourIsNeverDealtToAPerson(t *testing.T) {
	for slot := 0; slot < 64; slot++ {
		if PointerColour(slot) == AgentColour {
			t.Fatalf("slot %d is dealt the agent's violet", slot)
		}
	}
}

// TestANameIsReadableOnEveryInkItCanSitOn.
//
// A black pointer with black text was no name at all, which is why tagInk
// exists. It has to hold for every colour the palette can produce, not just the
// one that was on screen when somebody noticed.
func TestANameIsReadableOnEveryInkItCanSitOn(t *testing.T) {
	lum := func(c uint32) uint32 {
		r, g, b := (c>>16)&0xff, (c>>8)&0xff, c&0xff
		return (r*299 + g*587 + b*114) / 1000
	}
	for slot := 0; slot < len(pointerColours); slot++ {
		ground := PointerColour(slot)
		ink := tagInk(ground)
		if d := int(lum(ground)) - int(lum(ink)); d < 60 && d > -60 {
			t.Errorf("colour %#06x gets ink %#06x — %d apart, which is not a readable name",
				ground, ink, d)
		}
	}
	if d := int(lum(AgentColour)) - int(lum(tagInk(AgentColour))); d < 60 && d > -60 {
		t.Errorf("the agent's own tag is unreadable: %d apart", d)
	}
}
