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

// Package frontmatter splits the YAML header off a Markdown file.
//
// Deliberately not a YAML library. The headers this project reads are flat
// scalar fields — a name, a description, a model — and the agents that read the
// same files in the wild accept exactly that; pulling in a parser to read
// `name:` would add a dependency for a shape that fits in thirty lines.
//
// If a header ever needs nesting, this should become a real parser rather than
// grow special cases. That is the line, and it is written down so somebody
// notices when it is crossed rather than discovering it as a bug.
//
// It lives here because it has two consumers — skills and prompts — which is
// the point at which a shared package stops being a guess about the second one.
package frontmatter

import (
	"fmt"
	"strings"
)

// Split returns the header's fields and the Markdown after it.
//
// Keys are lowercased and values are unquoted. A file with no header is an
// error rather than an empty result: every caller here is reading a file that
// is supposed to have one, and "no fields" would let a malformed file pass as
// an empty-but-valid one.
func Split(raw string) (fields map[string]string, body string, err error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return nil, "", fmt.Errorf("no YAML frontmatter: the file must start with a --- line")
	}
	rest := raw[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("the frontmatter is never closed by a --- line")
	}
	head := rest[:end]
	body = strings.TrimSpace(strings.TrimPrefix(rest[end+4:], "\n"))

	fields = map[string]string{}
	for _, line := range strings.Split(head, "\n") {
		// An indented line belongs to a nested value this does not read. Skipped
		// rather than parsed as a key, so `metadata:` followed by its children
		// does not turn `team: infra` into a top-level field.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		fields[strings.ToLower(strings.TrimSpace(key))] = value
	}
	return fields, body, nil
}

// List reads a comma-separated field, which is how this project spells a short
// list in a header rather than inventing YAML sequence parsing for it.
func List(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
