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

package frontmatter

import "testing"

func TestSplitReadsFieldsAndBody(t *testing.T) {
	f, body, err := Split("---\nname: x\ndescription: what it does\n---\n\n# Title\n\ntext\n")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if f["name"] != "x" || f["description"] != "what it does" {
		t.Errorf("fields = %v", f)
	}
	if body != "# Title\n\ntext" {
		t.Errorf("body = %q", body)
	}
}

// A nested value is a shape this does not read. What matters is that its
// CHILDREN do not become top-level fields — `team: infra` under `metadata:` must
// not turn into a `team` the caller then acts on.
func TestSplitDoesNotPromoteNestedKeys(t *testing.T) {
	f, _, err := Split("---\nname: x\nmetadata:\n  team: infra\n  slash: false\n---\nbody\n")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if _, leaked := f["team"]; leaked {
		t.Error("a nested key became a top-level field")
	}
	if _, leaked := f["slash"]; leaked {
		t.Error("a nested key became a top-level field")
	}
}

func TestSplitRefusesWhatHasNoHeader(t *testing.T) {
	for _, raw := range []string{"# just markdown\n", "---\nname: x\n"} {
		if _, _, err := Split(raw); err == nil {
			t.Errorf("accepted a file with no usable frontmatter: %q", raw)
		}
	}
}

func TestSplitUnquotesAndLowercasesKeys(t *testing.T) {
	f, _, err := Split("---\nName: \"quoted\"\nPOLICY: 'readonly'\n---\nbody\n")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if f["name"] != "quoted" || f["policy"] != "readonly" {
		t.Errorf("fields = %v", f)
	}
}

func TestListSplitsAndTrims(t *testing.T) {
	got := List(" a, b ,, c ")
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("List = %#v", got)
	}
}
