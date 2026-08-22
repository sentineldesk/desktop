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

// Package toolsearch finds the tools for a task, from a description of the task.
//
// It lives here because two things need it and they are on opposite sides of the
// socket. The MCP server exposes it as `tool_search`, for an external host that
// has no runtime of ours and can only be helped over the protocol. The agent
// runtime answers its own, locally, from the catalogue it already holds after
// tools/list — asking the server would be a round trip to have it answer a
// question this side can answer from what it is already carrying.
//
// ADR-003 decided both, and said the shared implementation should move here
// "when the runtime exists to import it — not before", because a shared package
// with one consumer is a guess about the second. The runtime exists.
//
// The ranking is deliberately dumb: substring matching over name, category and
// description, plus a hand-written vocabulary. Anything cleverer would need a
// model or a dependency to answer a question that keywords already answer well
// on a corpus of a hundred and twenty short strings — and an embedding call per
// search would add cost and latency to the thing being made cheaper. It is
// measured rather than assumed: internal/mcp/search_test.go holds one
// plain-English query per tool and enforces that every tool is reachable.
package toolsearch

import (
	"sort"
	"strings"
)

// Tool is the little a ranking needs to know. Neither side's richer type: the
// server has schemas and risk levels, the runtime has annotations, and the
// ranking cares about none of it.
type Tool struct {
	Name        string
	Description string
}

// Hit is one result, with the score that put it there.
type Hit struct {
	Name        string
	Category    string
	Description string
	Score       int
}

// categoryRules maps a tool to a theme. The order matters: the first rule that
// matches wins, so the specific entries have to come before the general ones —
// window_properties is a window tool, not a properties tool.
//
// Categories exist for tool_search, which weighs a match on the category above
// one in the description: someone asking about "ssh" wants the thirteen ssh_*
// tools before every tool whose description happens to mention a remote host.
var categoryRules = []struct {
	match    func(string) bool
	category string
}{
	{func(n string) bool { return strings.HasPrefix(n, "ssh_") }, "ssh"},
	{func(n string) bool { return strings.HasPrefix(n, "shell_") }, "shell"},
	{func(n string) bool { return strings.HasPrefix(n, "terminal_") || n == "check_errors" }, "terminal"},
	{func(n string) bool { return strings.HasPrefix(n, "browser_") }, "browser"},
	{func(n string) bool { return strings.HasPrefix(n, "ui_") || n == "fill_form" }, "accessibility"},
	{func(n string) bool { return strings.HasPrefix(n, "snapshot_") }, "snapshot"},
	{func(n string) bool { return strings.HasPrefix(n, "mouse_") }, "input"},
	{func(n string) bool { return n == "type_text" || n == "key_combo" }, "input"},
	{func(n string) bool { return strings.Contains(n, "recording") }, "recording"},
	{func(n string) bool { return strings.Contains(n, "restream") }, "restream"},
	{func(n string) bool { return strings.Contains(n, "clipboard") }, "clipboard"},
	{func(n string) bool { return strings.Contains(n, "desktop") }, "desktops"},
	{func(n string) bool { return strings.Contains(n, "window") }, "windows"},
	{func(n string) bool { return strings.Contains(n, "packages") }, "packages"},
	{func(n string) bool {
		switch n {
		case "read_file", "write_file", "list_directory":
			return true
		}
		return false
	}, "files"},
	{func(n string) bool {
		switch n {
		case "get_audio_state", "set_volume":
			return true
		}
		return false
	}, "audio"},
	{func(n string) bool {
		switch n {
		case "list_processes", "kill_process", "is_running", "list_installed_apps",
			"launch_app", "run_command", "open_app_and_wait":
			return true
		}
		return false
	}, "processes"},
	{func(n string) bool {
		switch n {
		case "screenshot", "screenshot_region", "get_screen_info", "get_pixel_color",
			"read_screen_text", "find_text", "set_resolution":
			return true
		}
		return false
	}, "screen"},
	{func(n string) bool {
		switch n {
		case "room_state", "request_control", "release_control":
			return true
		}
		return false
	}, "room"},
	{func(n string) bool {
		switch n {
		case "sudo_status", "service_control":
			return true
		}
		return false
	}, "system"},
}

func CategoryOf(name string) string {
	for _, r := range categoryRules {
		if r.match(name) {
			return r.category
		}
	}
	return "general"
}

// searchStopwords are the words a plain-English request is made of. Left in,
// they dominate the result: every long description contains "the" and "a", so
// the tools with the most prose win regardless of what was asked. Dropping them
// is the difference between "record a video of the desktop" returning
// start_recording and returning whichever tool has the wordiest description.
var searchStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "can": true, "do": true, "for": true, "from": true,
	"get": true, "how": true, "i": true, "in": true, "is": true, "it": true,
	"me": true, "my": true, "of": true, "on": true, "or": true, "so": true,
	"some": true, "someone": true, "something": true, "that": true, "the": true,
	"then": true, "there": true, "this": true, "to": true, "up": true,
	"want": true, "was": true, "what": true, "when": true, "which": true,
	"with": true, "you": true, "your": true,
}

// categoryMatches reports whether a query term points at a category, either by
// naming it or through one of its aliases.
func categoryMatches(category, term string) bool {
	if strings.Contains(category, term) {
		return true
	}
	for _, alias := range categoryAliasesFor(category) {
		// Both directions. The prefix test used to run only one way, so a query
		// saying "application" could not reach the alias "app" — which is half
		// of why "open the calculator application" never found launch_app.
		if alias == term ||
			(strings.HasPrefix(alias, term) && len(term) >= 4) ||
			(strings.HasPrefix(term, alias) && len(alias) >= 4) {
			return true
		}
	}
	return false
}

// toolKeywords is the vocabulary that connects a task to the tool that does it.
//
// Categories were not enough. A category alias helps a query that already names
// the theme — "remote access" reaches the ssh_* family — but it cannot choose
// between thirteen tools inside that family, and it does nothing at all for the
// tools whose theme is obvious and whose *name* is the obstacle. "open the
// calculator application" is a request for launch_app, and before this map it
// returned browser_open, terminal_open, shell_open and open_app_and_wait,
// because those four have the query's only distinctive word in their names and
// launch_app does not have it anywhere.
//
// Three kinds of entry earn their place here, and nothing else should:
//
//   - the word for the thing that is not the word in the name — "uninstall" for
//     remove_packages, "checkpoint" for the snapshot family, "pause" for wait;
//   - the spelling the writer did not use — "color" beside "colour", "a11y"
//     beside "accessibility";
//   - the multi-word phrase that means the tool and nothing else — "port
//     forward", "always on top", "bring to front". These are matched against the
//     whole query rather than term by term, which is what makes them worth more
//     than their words separately: "forward" alone is ambiguous between the two
//     tunnel directions, "port forward" is not.
//
// What does not belong here is a word already in the tool's name or description.
// It buys nothing — those are searched — and it makes the map look authoritative
// when it is only supplementary. A tool absent from this map is not misfiled;
// it is a tool whose name says what it does, which is most of them.
//
// Every key is checked against the catalogue at startup, so a renamed tool
// cannot leave its vocabulary behind. Whether the vocabulary is *sufficient* is
// a different question, and the only honest answer to it is a measurement:
// search_test.go holds one plain-English query per tool and fails when recall
// drops. Add a tool, add its query; if it ranks, it needs nothing here.

// keywordIndex splits toolKeywords into the single words, which are compared
// against one query term at a time, and the phrases, which are compared against
// the whole query. Building it once beats re-splitting on every search.
type keywordIndex struct {
	words   map[string]map[string]bool // tool -> set of single-word keywords
	phrases map[string][]string        // tool -> multi-word keywords
}

var keywords = buildKeywordIndex()

func buildKeywordIndex() keywordIndex {
	idx := keywordIndex{
		words:   map[string]map[string]bool{},
		phrases: map[string][]string{},
	}
	for tool, list := range allToolKeywords() {
		for _, kw := range list {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if strings.Contains(kw, " ") {
				idx.phrases[tool] = append(idx.phrases[tool], kw)
				continue
			}
			if idx.words[tool] == nil {
				idx.words[tool] = map[string]bool{}
			}
			idx.words[tool][kw] = true
		}
	}
	return idx
}

// searchTools ranks the catalogue against a free-text query.
//
// The scoring is deliberately dumb — substring matching over name, category and
// description, weighted in that order. Anything cleverer (embeddings, a real
// index) would need a model or a dependency to answer a question that a handful
// of keywords already answers well, on a corpus of a hundred and twenty short
// strings that fits in a cache line's worth of cache misses. A hit on the name
// outranks the category, which outranks the description, because a tool called
// ssh_exec is a better answer to "ssh" than one that mentions ssh in passing.
func Rank(tools []Tool, query string, limit int) []Hit {
	lower := strings.ToLower(query)
	var terms []string
	for _, term := range strings.Fields(lower) {
		term = strings.Trim(term, ".,;:!?\"'()")
		if len(term) < 2 || searchStopwords[term] {
			continue
		}
		terms = append(terms, term)
	}
	if limit <= 0 {
		limit = 10
	}
	var hits []Hit
	for _, t := range tools {
		// tool_search does not answer questions about the desktop, and a model
		// running it already has it. Leaving it in means every search returns
		// itself, which is a wasted slot in a list of ten.
		if t.Name == "tool_search" {
			continue
		}
		name := strings.ToLower(t.Name)
		spaced := strings.ReplaceAll(name, "_", " ")
		cat := CategoryOf(t.Name)
		desc := strings.ToLower(t.Description)
		kwWords := keywords.words[t.Name]

		score, strong, weak := 0, 0, 0

		// Phrases first, and against the whole query rather than term by term.
		// A phrase surviving intact is the strongest signal in the file: "port
		// forward" appearing in a sentence is not an accident of vocabulary the
		// way "forward" on its own is.
		for _, phrase := range keywords.phrases[t.Name] {
			if strings.Contains(lower, phrase) {
				score += 10
				strong++
			}
		}

		for _, term := range terms {
			// Underscores are separators, not letters: "remote access" should
			// find ssh_list_remote, and it will not if the term has to survive
			// as a contiguous substring of the whole name.
			//
			// Two-letter terms are matched only as whole words. Left as
			// substrings they matched anything: "am" in "which workspace am I
			// on" hit every tool whose name embedded the fragment, and
			// four tools that had nothing to do with the question outranked the
			// one that answered it.
			hit := 0
			switch {
			case name == term:
				hit = 12
			case kwWords[term]:
				hit = 9
			case len(term) == 2:
				// whole-word only
				for _, part := range strings.Split(spaced, " ") {
					if part == term {
						hit = 8
						break
					}
				}
			case strings.Contains(spaced, term):
				hit = 8
			case strings.Contains(name, term):
				hit = 6
			}
			if categoryMatches(cat, term) {
				hit += 4
			}
			// A name, keyword or category hit is evidence about the tool. A
			// description hit is evidence about its prose, so it scores but does
			// not count towards the all-terms bonus below — otherwise the tools
			// with the longest descriptions win every vague query.
			if hit > 0 {
				strong++
			} else if len(term) >= 4 && strings.Contains(desc, term) {
				weak++
			}
			if strings.Contains(desc, term) {
				hit++
			}
			score += hit
		}

		// A tool with no name, keyword or category hit is normally not an
		// answer. The exception is the query that describes the tool in words
		// none of those three happen to hold: several distinctive terms landing
		// in one description is weak evidence, but it is evidence, and refusing
		// it outright is what left twenty-eight tools unreachable by any
		// phrasing at all. They enter far down the list, which is the right
		// place for a guess — visible to an agent reading ten results, never
		// ahead of a tool that actually matched.
		if strong == 0 {
			if weak < 2 {
				continue
			}
			score = weak
		} else {
			// Matching several terms beats matching one of them well: a query is
			// a description of one tool, not a bag of alternatives.
			score += strong * 3
		}
		hits = append(hits, Hit{
			Name: t.Name, Category: cat, Description: t.Description, Score: score,
		})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Name < hits[j].Name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// KeywordedTools lists every tool the vocabulary mentions.
//
// For the catalogue's own validation: a renamed tool must not leave its
// vocabulary behind, and a stranded entry would match nothing while nothing
// said so — the tool would simply get quietly harder to find.
func KeywordedTools() []string {
	kw := allToolKeywords()
	out := make([]string, 0, len(kw))
	for name := range kw {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
