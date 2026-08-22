// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

// What browser_wait_for waits FOR.
//
// The defect these pin down had no error anywhere and returned in six
// milliseconds. A run waiting for a YouTube skip button was told the selector
// "appeared", which was true — the element is in the page for the whole
// advertisement — and useless, because it is not pressable until the countdown
// ends. The wait cost nothing and bought nothing, and the run went on to click
// a control that was not ready.
//
// The predicate is JavaScript that only a browser can run, so what is checked
// here is the DECISION: which predicate a state asks for, and above all that
// asking for nothing still means visible.
package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The behavioural fix, and the one a refactor could revert without any test
// noticing: the default is visibility, not presence.
func TestWaitingForNothingInParticularMeansVisible(t *testing.T) {
	def, ok := waitPredicate("")
	if !ok {
		t.Fatal("the empty state was refused")
	}
	vis, _ := waitPredicate("visible")
	if def != vis {
		t.Error("the default is not `visible`. Presence is almost never the question, " +
			"and defaulting to it is how a wait returns in six milliseconds on a button " +
			"nobody can press yet.")
	}
	if strings.Contains(def, "!!el") && !strings.Contains(def, "getClientRects") {
		t.Error("the default became a presence check")
	}
}

// Each of these is a way a page keeps a control mounted and unusable. Dropping
// any one of them reopens the same hole for a different kind of widget.
func TestTheVisiblePredicateChecksEveryWayToHideSomething(t *testing.T) {
	got, _ := waitPredicate("visible")
	for what, why := range map[string]string{
		"getClientRects": "not rendered at all",
		"visibility":     "visibility:hidden, which keeps layout and shows nothing",
		"opacity":        "opacity:0, which is how fades and pre-open modals sit",
		"width > 0":      "a zero-size box, which is how a collapsed control measures",
	} {
		if !strings.Contains(got, what) {
			t.Errorf("the visible predicate does not check %s (%s)", what, why)
		}
	}
}

// offsetParent is the obvious check and the wrong one: it is null for
// position:fixed, which is what a skip button, a cookie banner and most modals
// are. Using it would call every one of them invisible.
func TestTheVisiblePredicateDoesNotUseOffsetParent(t *testing.T) {
	got, _ := waitPredicate("visible")
	if strings.Contains(got, "offsetParent") {
		t.Error("offsetParent is null for position:fixed — the skip button this was " +
			"written for is exactly that, and so is every modal")
	}
}

// `present` has to stay available and stay honest: it is the old behaviour, and
// there are real cases for it. It just is not the default any more.
func TestPresentIsStillPresence(t *testing.T) {
	got, ok := waitPredicate("present")
	if !ok {
		t.Fatal("present was refused")
	}
	if strings.Contains(got, "getComputedStyle") {
		t.Error("`present` grew a visibility check, which makes the two states the same")
	}
	if !strings.Contains(got, "el") {
		t.Errorf("present is %q", got)
	}
}

// "It went away" is satisfied two different ways and a page picks whichever it
// likes: removing the node, or hiding it where it stands. Waiting for only one
// of them hangs on the other — which for an advertisement overlay is the whole
// wait, every time.
func TestGoneAcceptsBothRemovedAndHidden(t *testing.T) {
	got, ok := waitPredicate("gone")
	if !ok {
		t.Fatal("gone was refused")
	}
	if !strings.Contains(got, "!el") {
		t.Error("`gone` does not accept the node being removed")
	}
	if !strings.Contains(got, "getClientRects") {
		t.Error("`gone` does not accept the node being hidden in place, so an overlay " +
			"that is merely hidden would never satisfy it")
	}
}

// An unknown state must be refused rather than quietly treated as one of the
// real ones. Silently falling back to `present` would be the original bug
// arriving through a typo.
func TestAnUnknownStateIsRefused(t *testing.T) {
	for _, s := range []string{"attached", "enabled", "clickable", "VISIBLE!", "shown"} {
		if _, ok := waitPredicate(s); ok {
			t.Errorf("state %q was accepted; a typo must not silently become a weaker wait", s)
		}
	}
}

// Spelling and spacing are a person's business, not a failure.
func TestStateIsCaseAndSpaceInsensitive(t *testing.T) {
	want, _ := waitPredicate("visible")
	for _, s := range []string{"VISIBLE", " visible ", "Visible"} {
		got, ok := waitPredicate(s)
		if !ok || got != want {
			t.Errorf("state %q was not read as visible", s)
		}
	}
}

// The tool's own description has to say what it waits for, because the schema is
// the only thing the model reads before choosing. The old one said "appears",
// which is what led a run to trust six milliseconds.
func TestTheToolSaysItWaitsForVisibility(t *testing.T) {
	var found bool
	for _, td := range (&Server{}).buildTools() {
		if td.Name != "browser_wait_for" {
			continue
		}
		found = true
		if !strings.Contains(td.Description, "VISIBLE") {
			t.Errorf("the description does not say it waits for visibility: %s", td.Description)
		}
		for _, s := range []string{"present", "gone"} {
			if !strings.Contains(td.Description, s) {
				t.Errorf("the description never mentions state=%s, so nothing will use it", s)
			}
		}
		var sch struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(td.InputSchema, &sch); err != nil {
			t.Fatalf("the schema does not parse: %v", err)
		}
		if _, ok := sch.Properties["state"]; !ok {
			t.Error("the schema has no `state`, so the states are unreachable however " +
				"well the description describes them")
		}
	}
	if !found {
		t.Fatal("browser_wait_for is not in the catalogue")
	}
}

// --- being able to check whatever you are allowed to do ----------------------

// The hole this closes was in the policy, not in a tool.
//
// Reading a page meant browser_eval, which is riskDanger for good reason: it
// runs arbitrary JavaScript and can navigate, fetch, submit or rewrite the
// document. But that left MCP_POLICY=safe granting exactly the wrong pair — an
// agent could CLICK and could not CHECK. Acting without being able to verify is
// worse than not acting at all, and it is the reverse of what a safety level is
// for.
func TestUnderEveryPolicyYouCanCheckAtLeastAsMuchAsYouCanDo(t *testing.T) {
	byName := map[string]toolDef{}
	for _, td := range (&Server{}).buildTools() {
		byName[td.Name] = td
	}

	for _, level := range []string{"readonly", "safe", "full"} {
		p := &Policy{level: level}
		can := func(name string) bool {
			if _, ok := byName[name]; !ok {
				t.Fatalf("%s is not in the catalogue", name)
			}
			allowed, _ := p.Allowed(name, nil)
			return allowed
		}
		// If a level lets you press things in a page, it has to let you look at
		// them too.
		if can("browser_click") && !can("browser_element") {
			t.Errorf("MCP_POLICY=%s allows browser_click and refuses browser_element: "+
				"an agent that can act and cannot verify is the worst arrangement of the two", level)
		}
		if can("browser_click") && !can("browser_wait_for") {
			t.Errorf("MCP_POLICY=%s allows clicking and refuses waiting for what to click", level)
		}
	}
}

// The narrow reader has to stay narrow. The moment it can be talked into doing
// something, riskRead stops being true and readonly stops meaning anything.
func TestTheElementReaderIsReadOnlyInFactAndNotOnlyInLabel(t *testing.T) {
	js := elementReportJS(".x")
	for _, forbidden := range []string{
		"location", "fetch(", "XMLHttpRequest", ".click()", ".submit(",
		"innerHTML =", "eval(", "Function(", "open(",
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("the element report contains %q, so calling it riskRead is a claim "+
				"the code does not support", forbidden)
		}
	}
	// And the selector is the ONLY thing that varies, so there is no room for a
	// caller to append an expression of their own.
	a, b := elementReportJS(".one"), elementReportJS(".two")
	if len(a) != len(b) {
		t.Error("the generated expression changes shape with the selector")
	}
}

// The fields the YouTube skill needs in order to tell 'still playing, my click
// did nothing' from 'a new item started'. Losing any of them puts the run that
// clicked four times back on the table.
func TestTheElementReaderCarriesWhatDecidesAClickWorked(t *testing.T) {
	js := elementReportJS("video")
	for field, why := range map[string]string{
		"currentTime": "the clock that says whether the same item is still playing",
		"duration":    "different for the advertisement and for the video after it",
		"paused":      "the difference between stopped and about to start",
		"visible":     "a control can be present and unusable",
		"covered":     "a control can be visible and unreachable",
		"disabled":    "a control can be visible, uncovered and inert",
	} {
		if !strings.Contains(js, field) {
			t.Errorf("the element report has no %s (%s)", field, why)
		}
	}
}
