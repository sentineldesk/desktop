// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

// The skills we ship tell the model to call tools by name. Nothing checks that
// those names are real, and the failure is quiet at every layer: the skill loads
// (it is just Markdown), the model reads it and calls what it was told, and the
// dispatcher answers "unknown tool" for a step somebody wrote down precisely
// because that work goes wrong without it. Rename a tool and every skill naming
// it becomes a set of instructions for a machine that no longer exists, with
// nothing in the build to say so.
//
// This lives in internal/mcp rather than next to the skills because the
// authority on what a tool is called is the catalogue, and the catalogue is
// here. It is the same reasoning as registry_test.go: check the thing against
// the thing, not against a second copy of the answer.
package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The skills' calling convention: a fenced block whose line begins with the
// tool name, arguments after it.
//
//	browser_click  selector: .ytp-skip-ad-button
//
// Anchoring on the first word of a line inside a fence is what keeps this quiet.
// A bare hunt for snake_case across the file would flag every parameter
// (timeout_ms, max_chars) and every CSS class, and a test that cries wolf gets
// switched off.
var toolCall = regexp.MustCompile(`(?m)^([a-z][a-z0-9]*(?:_[a-z0-9]+)+)`)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above this package")
		}
		dir = parent
	}
}

// shippedSkills returns name -> contents for every SKILL.md under skills/.
func shippedSkills(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join(repoRoot(t), "skills")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("the skills this project ships are not readable at %s: %v", root, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		out[e.Name()] = string(raw)
	}
	if len(out) == 0 {
		t.Fatal("no skills found: this test would pass by checking nothing")
	}
	return out
}

// fencedBlocks returns the contents of every ``` block in a document.
func fencedBlocks(doc string) []string {
	var blocks []string
	var cur []string
	inside := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inside {
				blocks = append(blocks, strings.Join(cur, "\n"))
				cur = nil
			}
			inside = !inside
			continue
		}
		if inside {
			cur = append(cur, line)
		}
	}
	return blocks
}

// Every tool a shipped skill tells the model to call has to exist.
func TestShippedSkillsOnlyNameToolsThatExist(t *testing.T) {
	known := map[string]bool{}
	for _, td := range (&Server{}).buildTools() {
		known[td.Name] = true
	}

	found := 0
	for skill, doc := range shippedSkills(t) {
		for _, block := range fencedBlocks(doc) {
			for _, m := range toolCall.FindAllStringSubmatch(block, -1) {
				name := m[1]
				// Only judge what looks like it is meant to be a tool call.
				// A snake_case word that matches nothing may be a shell
				// command or a filename, and this test has no business
				// having an opinion about those.
				if !known[name] && !strings.Contains(name, "_") {
					continue
				}
				if !known[name] {
					t.Errorf("%s tells the model to call %q, which is not a tool: "+
						"the call would come back 'unknown tool' and the step would be skipped",
						skill, name)
					continue
				}
				found++
			}
		}
	}
	if found == 0 {
		t.Error("no tool calls found in any skill: the extractor is broken, " +
			"so this test is green without having checked anything")
	}
}

// A tool a skill names must also be reachable under the policy a desktop is
// likely to run. A skill whose central step is refused by MCP_POLICY=safe is
// worth knowing about here rather than mid-run — this does not fail the build,
// it reports, because a skill for a full-access desktop is legitimate.
func TestShippedSkillsReportWhichToolsAPolicyWouldRefuse(t *testing.T) {
	byName := map[string]toolDef{}
	for _, td := range (&Server{}).buildTools() {
		byName[td.Name] = td
	}
	for skill, doc := range shippedSkills(t) {
		for _, block := range fencedBlocks(doc) {
			for _, m := range toolCall.FindAllStringSubmatch(block, -1) {
				td, ok := byName[m[1]]
				if !ok {
					continue
				}
				if td.Risk == riskDanger {
					t.Logf("%s calls %q, which readonly and safe both refuse", skill, td.Name)
				}
			}
		}
	}
}

// The lesson from the run where four skip clicks did nothing.
//
// The fix was prose, and prose is what a tidy-up deletes. This pins the part
// that cost a whole session to learn: a click goes through browser_click, which
// sends the pointer sequence the player listens for, and never through
// browser_eval, whose el.click() dispatches one event that YouTube ignores while
// returning a string that reads like success.
//
// It checks the instruction, not the wording, so the file can be rewritten
// freely — but not back into the shape that failed.
func TestTheYouTubeSkillDoesNotTeachClickingThroughEval(t *testing.T) {
	doc, ok := shippedSkills(t)["youtube-skip-ad"]
	if !ok {
		t.Fatal("the youtube-skip-ad skill is gone")
	}

	blocks := fencedBlocks(doc)

	clicksProperly := false
	for _, b := range blocks {
		if toolCall.MatchString(b) && strings.Contains(b, "browser_click") {
			clicksProperly = true
		}
	}
	if !clicksProperly {
		t.Error("no browser_click call in the skill: without it the model is left to invent " +
			"a way to press the button, and the way it invents does not work")
	}

	for _, b := range blocks {
		m := toolCall.FindStringSubmatch(b)
		if m == nil || m[1] != "browser_eval" {
			continue
		}
		if strings.Contains(b, ".click()") {
			t.Errorf("a browser_eval example in youtube-skip-ad calls .click():\n%s\n\n"+
				"That is the pattern that silently failed — one `click` event, which the "+
				"player does not listen for, reported as success. Use browser_click.", b)
		}
	}
}
