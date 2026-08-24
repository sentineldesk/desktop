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

package capability

import (
	"encoding/json"
	"strings"
	"testing"
)

// The MCP registry tests exercise all of this again through their aliases,
// against the real 131-entry catalogue. These run against small hand-built
// menus instead, because this package is what OTHER adapters trust without
// ever importing internal/mcp — it has to hold its rules up on its own.

func TestValidateRefusesWhatWasNeverClassified(t *testing.T) {
	ok := []Def{
		{Name: "look", Risk: RiskRead},
		{Name: "poke", Risk: RiskWrite, Visibility: VisInjects, RequiresControl: true},
		{Name: "run", Risk: RiskDanger, Visibility: VisVisible},
	}
	if err := Validate(ok); err != nil {
		t.Fatalf("a correctly classified menu was refused: %v", err)
	}

	for _, bad := range []struct {
		why  string
		defs []Def
		want string
	}{
		{"no risk", []Def{{Name: "mystery"}}, "no Risk"},
		{"duplicate name", append(append([]Def{}, ok...), ok[0]), "duplicate"},
		{"writer with no visibility", []Def{{Name: "w", Risk: RiskWrite}}, "Visibility"},
		{"read-only claiming visible",
			[]Def{{Name: "r", Risk: RiskRead, Visibility: VisVisible}}, "cannot be visible"},
		{"injects without the gate",
			[]Def{{Name: "i", Risk: RiskWrite, Visibility: VisInjects}}, "somebody else's session"},
	} {
		err := Validate(bad.defs)
		if err == nil {
			t.Errorf("%s was accepted", bad.why)
			continue
		}
		if !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: error does not mention %q: %v", bad.why, bad.want, err)
		}
	}
}

func TestCatalogueAnswersOffTheCard(t *testing.T) {
	c := NewCatalogue([]Def{
		{Name: "look", Risk: RiskRead,
			InputSchema: Schema(map[string]any{"depth": PInt("how far")})},
		{Name: "poke", Risk: RiskWrite, Visibility: VisInjects, RequiresControl: true},
	})

	if !c.Known("look") || !c.Known("poke") {
		t.Fatal("the catalogue does not know its own verbs")
	}
	if c.Known("order_off_menu") {
		t.Error("a verb nobody defined is reported as known")
	}
	if c.RiskOf("look") != RiskRead || c.RiskOf("poke") != RiskWrite {
		t.Error("risk answers do not match the definitions")
	}
	if got := c.RiskOf("order_off_menu"); got != RiskUnset {
		t.Errorf("an unknown verb has a risk classification: %v", got)
	}
	if c.RequiresControl("look") {
		t.Error("an ungated verb is reported as needing the controls")
	}
	if !c.RequiresControl("poke") {
		t.Error("a gated verb is reported as free")
	}

	// The argument check refuses what the schema does not declare, and _meta
	// belongs to the protocol rather than to any verb.
	bad := c.UnknownArgs("look", map[string]any{"depth": 3, "max_depth": 3, "_meta": "x"})
	if len(bad) != 1 || bad[0] != "max_depth" {
		t.Errorf("unknown arguments came back as %v, want [max_depth]", bad)
	}
	if got := c.DeclaredArgs("look"); len(got) != 1 || got[0] != "depth" {
		t.Errorf("declared arguments came back as %v, want [depth]", got)
	}
}

// TestTheWireFormCarriesTheGate: the published annotations are how a client
// learns to ask for control at the right moment, so a definition that reaches
// the wire without them has lost the half of itself the client cannot infer.
func TestTheWireFormCarriesTheGate(t *testing.T) {
	d := Def{Name: "poke", Description: "poke it", Risk: RiskWrite,
		Visibility: VisInjects, RequiresControl: true,
		InputSchema: Schema(map[string]any{})}

	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Name        string `json:"name"`
		Annotations struct {
			ReadOnly        bool   `json:"readOnlyHint"`
			RequiresControl bool   `json:"sentineldesk/requiresControl"`
			Visibility      string `json:"sentineldesk/visibility"`
		} `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire.Name != "poke" {
		t.Errorf("name is %q", wire.Name)
	}
	if wire.Annotations.ReadOnly {
		t.Error("a writing verb published readOnlyHint=true")
	}
	if !wire.Annotations.RequiresControl {
		t.Error("the control gate did not reach the wire")
	}
	if wire.Annotations.Visibility != "injects" {
		t.Errorf("visibility on the wire is %q, want injects", wire.Annotations.Visibility)
	}
}

func TestTimeoutMSIsPublishedOnlyWhenDeclared(t *testing.T) {
	// Absent means unbounded, and a client must be able to tell that from a
	// declared zero without a sentinel value.
	undeclared := Def{Name: "read_file", Risk: RiskRead}
	if _, ok := undeclared.Annotations()["sentineldesk/timeoutMs"]; ok {
		t.Fatal("a verb with no deadline must not publish one")
	}

	declared := Def{Name: "ui_find", Risk: RiskRead, TimeoutMS: 10000}
	got, ok := declared.Annotations()["sentineldesk/timeoutMs"]
	if !ok {
		t.Fatal("a declared deadline was not published")
	}
	if got != 10000 {
		t.Fatalf("timeoutMs = %v, want 10000", got)
	}
}

func TestTimeoutMSSurvivesTheWireForm(t *testing.T) {
	raw, err := json.Marshal(Def{Name: "browser_goto", Risk: RiskWrite,
		Visibility: VisVisible, TimeoutMS: 45000})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Annotations map[string]any `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Annotations["sentineldesk/timeoutMs"] != float64(45000) {
		t.Fatalf("the deadline did not reach the wire: %v", wire.Annotations)
	}
}
