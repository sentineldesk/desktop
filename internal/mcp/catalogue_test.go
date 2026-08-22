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

package mcp

// The catalogue as a versioned contract.
//
// The tool set is what an integrator writes against, and until now it changed
// the way any private detail changes: silently, between releases, discoverable
// only by diffing tools/list. These two tests make a catalogue change a
// DECISION with a record — the same posture the wire takes (wire_test.go reads
// the TypeScript union) and migrations take (a checksummed file cannot be
// edited in place).
//
// catalogue.golden is the set of tool names, one per line, sorted. Changing the
// catalogue without regenerating it fails here with the diff; regenerating it
// without writing a docs/mcp-changelog.md entry fails the second test, because
// the newest entry's count is compared against the catalogue itself. Neither
// test can be satisfied by editing the other's file alone, which is the point:
// the record and the code move together or not at all.

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const goldenPath = "catalogue.golden"

func catalogueNames(t *testing.T) []string {
	t.Helper()
	tools := catalogue(t)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func TestTheCatalogueMatchesItsGoldenRecord(t *testing.T) {
	names := catalogueNames(t)
	want := strings.Join(names, "\n") + "\n"

	if os.Getenv("GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(goldenPath, []byte(want), 0o644); err != nil {
			t.Fatalf("could not regenerate %s: %v", goldenPath, err)
		}
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%s is missing — regenerate it with GOLDEN_UPDATE=1 go test ./internal/mcp "+
			"-run TestTheCatalogueMatchesItsGoldenRecord, and add a docs/mcp-changelog.md entry: %v",
			goldenPath, err)
	}
	if string(raw) == want {
		return
	}

	// Name what moved, because "files differ" sends somebody to a diff tool for
	// an answer two set subtractions already have.
	recorded := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if ln != "" {
			recorded[ln] = true
		}
	}
	current := map[string]bool{}
	for _, n := range names {
		current[n] = true
	}
	var added, removed []string
	for n := range current {
		if !recorded[n] {
			added = append(added, n)
		}
	}
	for n := range recorded {
		if !current[n] {
			removed = append(removed, n)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	t.Fatalf("the catalogue changed without its record: added %v, removed %v.\n"+
		"If the change is intended: GOLDEN_UPDATE=1 go test ./internal/mcp "+
		"-run TestTheCatalogueMatchesItsGoldenRecord regenerates %s, and the change "+
		"needs an entry in docs/mcp-changelog.md — the next test holds you to that.",
		added, removed, goldenPath)
}

// A host can only pin on the catalogue if the server SAYS which one it speaks,
// and it must say it before the host commits to anything — so it travels in
// initialize, and it is the full catalogue rather than the connection's
// policy-filtered view: "which catalogue is this" must not change with
// MCP_POLICY.
func TestInitializeAnnouncesTheCatalogue(t *testing.T) {
	s := testServer(t)
	c := newSession(t, s)
	res := c.call("initialize", map[string]any{
		"clientInfo": map[string]any{"name": "pinning-host", "version": "1.0"},
	})
	meta, _ := res["_meta"].(map[string]any)
	cat, ok := meta["sentineldesk/catalogue"].(map[string]any)
	if !ok {
		t.Fatalf("initialize announced no catalogue in _meta: %v", meta)
	}
	if got, want := cat["tools"], float64(len(catalogue(t))); got != want {
		t.Fatalf("initialize announced %v tools, the catalogue has %v", got, want)
	}
}

// changelogHeading is the countable part of an entry: "## 132 tools — …".
var changelogHeading = regexp.MustCompile(`(?m)^## (\d+) tools`)

func TestTheChangelogNamesTheCurrentCatalogue(t *testing.T) {
	data, err := os.ReadFile("../../docs/mcp-changelog.md")
	if err != nil {
		t.Fatalf("docs/mcp-changelog.md is unreadable — the catalogue's changelog "+
			"is part of the contract, not an optional doc: %v", err)
	}
	m := changelogHeading.FindSubmatch(data)
	if m == nil {
		t.Fatal("docs/mcp-changelog.md has no '## <n> tools' entry to check against")
	}
	newest, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("unparseable count in the newest changelog entry: %v", err)
	}
	if got := len(catalogue(t)); newest != got {
		t.Fatalf("the newest changelog entry says %d tools and the catalogue has %d — "+
			"add an entry to docs/mcp-changelog.md describing what changed", newest, got)
	}
}
