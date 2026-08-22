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

package toolsearch

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The vocabulary, as Markdown rather than as Go maps.
//
// It was a hundred and fifty-two entries in two map literals, which made adding
// a word a rebuild and made adding a LANGUAGE a fork. That second one has a
// measured cost: a goal written in Spanish matches less of an English
// vocabulary, and when it matches nothing the runtime falls back to offering the
// whole catalogue. "cerrá esa ventana" returned zero tools where "close that
// window" returned fifteen — and the fallback is five times the input tokens of
// a run that matched.
//
// Every file in vocab/ is loaded and MERGED, so a language file adds terms
// rather than replacing them. That is the whole design: a query is matched
// against every word anybody has written for a tool, in any language, because
// the searcher has no idea which language it is being asked in and does not
// need one.
//
//go:embed vocab
var vocabFS embed.FS

// VocabDir is an optional directory of extra vocabulary files, merged on top of
// the embedded ones. Empty disables it.
//
// Set by the caller rather than derived here, because this package is imported
// by two programs that keep their files in different places — the daemon inside
// a container and the agent in somebody's home directory.
var VocabDir string

var (
	vocabOnce sync.Once
	catAlias  map[string][]string
	toolWords map[string][]string
)

func loadVocab() {
	catAlias = map[string][]string{}
	toolWords = map[string][]string{}

	if entries, err := fs.ReadDir(vocabFS, "vocab"); err == nil {
		for _, e := range entries {
			if raw, err := vocabFS.ReadFile(filepath.Join("vocab", e.Name())); err == nil {
				mergeVocab(string(raw))
			}
		}
	}
	if VocabDir != "" {
		if entries, err := os.ReadDir(VocabDir); err == nil {
			for _, e := range entries {
				if raw, err := os.ReadFile(filepath.Join(VocabDir, e.Name())); err == nil {
					mergeVocab(string(raw))
				}
			}
		}
	}
}

// mergeVocab reads one file. `## categories` and `## tools` switch which map
// the lines after them go into; a line of `key: a, b, c` is an entry and
// everything else is prose, so the file can explain itself without a parser
// that has opinions about Markdown.
func mergeVocab(raw string) {
	target := ""
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			switch {
			case strings.Contains(strings.ToLower(trimmed), "categor"):
				target = "cat"
			case strings.Contains(strings.ToLower(trimmed), "tool"):
				target = "tool"
			}
			continue
		}
		key, rest, ok := strings.Cut(trimmed, ":")
		if !ok || target == "" {
			continue
		}
		key = strings.TrimSpace(key)
		// A key is an identifier. Anything with a space is prose that happened
		// to contain a colon, and treating it as an entry is how a vocabulary
		// quietly fills with sentences.
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		var terms []string
		for _, t := range strings.Split(rest, ",") {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				terms = append(terms, t)
			}
		}
		if len(terms) == 0 {
			continue
		}
		if target == "cat" {
			catAlias[key] = append(catAlias[key], terms...)
		} else {
			toolWords[key] = append(toolWords[key], terms...)
		}
	}
}

func categoryAliasesFor(name string) []string {
	vocabOnce.Do(loadVocab)
	return catAlias[name]
}

func toolKeywordsFor(name string) []string {
	vocabOnce.Do(loadVocab)
	return toolWords[name]
}

// allToolKeywords is the whole merged table, for the indexer that builds the
// matcher once rather than asking per tool.
func allToolKeywords() map[string][]string {
	vocabOnce.Do(loadVocab)
	return toolWords
}
