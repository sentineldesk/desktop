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

// The credential vocabulary, as Markdown rather than as Go literals.
//
// Same move the search vocabulary made, for the same two reasons and one more.
// A term somebody cannot add without recompiling is a term they do not add; a
// language nobody can add without a fork is a language that never arrives. The
// third reason is specific to this: what a secret is CALLED is local knowledge.
// A team names its variables in its own language, its own conventions, its own
// abbreviations, and the person who knows that is never the person building
// this binary.
//
// # Why a large dictionary is safe here
//
// A big list of words matched on its own would be a disaster — "cambié la
// contraseña ayer" is a sentence, not a leak, and a detector that warns about
// it teaches people to dismiss warnings. The dictionary is not matched on its
// own. Every term is the LEFT side of a structure:
//
//	<name>  <separator>  <value that is long enough and is not a placeholder>
//
// So terms can be added without limit. Each one widens what is CAUGHT without
// widening what is GUESSED at, because none of them fires alone. That property
// is what makes "just add more words" a sound instruction here and a terrible
// one in most detectors.
//
// # Three lists, and the third is the one people forget
//
// names       what appears before the value: password, contraseña, senha, token
// phrases     prose that introduces one: "the password is", "la clave es"
// placeholders what a value can be while meaning nothing: changeme, cambiame
//
// The placeholders have to be multilingual too, and were not. Everything in
// that list was English, so `clave = cambiame` in a sample config raised a
// credential warning. A detector that cries wolf at the machine's own example
// files is one somebody switches off in the first week — which costs more than
// the words were worth.
package mcp

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed credentials
var credentialFS embed.FS

// credentialLexicon is the merged vocabulary, built once.
type credentialLexicon struct {
	names        []string
	phrases      []string
	placeholders []string

	// Built from the lists above: the assignment shape, the prose shape, and
	// the test for a value that means nothing.
	assignment  *regexp.Regexp
	introduced  *regexp.Regexp
	placeholder *regexp.Regexp
}

var (
	lexiconOnce sync.Once
	lexicon     *credentialLexicon
)

// CredentialVocabDir is an optional directory of extra vocabulary files, merged
// on top of the embedded ones. Empty disables it.
var CredentialVocabDir = "/etc/sentineldesk/credentials"

func credentialWords() *credentialLexicon {
	lexiconOnce.Do(func() { lexicon = loadLexicon() })
	return lexicon
}

func loadLexicon() *credentialLexicon {
	lx := &credentialLexicon{}
	seen := map[string]bool{}

	add := func(section string, terms []string) {
		for _, term := range terms {
			key := section + "\x00" + term
			if seen[key] {
				continue
			}
			seen[key] = true
			switch section {
			case "names":
				lx.names = append(lx.names, term)
			case "phrases":
				lx.phrases = append(lx.phrases, term)
			case "placeholders":
				lx.placeholders = append(lx.placeholders, term)
			}
		}
	}

	// Embedded first, then the operator's own directory on top. Merged rather
	// than replaced, so adding a file adds terms — the same rule the search
	// vocabulary follows, and for the same reason: nothing here knows which
	// language the machine it is watching was configured in.
	_ = fs.WalkDir(credentialFS, "credentials", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, err := credentialFS.ReadFile(path)
		if err != nil {
			return nil
		}
		for section, terms := range parseLexicon(string(raw)) {
			add(section, terms)
		}
		return nil
	})

	if CredentialVocabDir != "" {
		matches, _ := filepath.Glob(filepath.Join(CredentialVocabDir, "*.md"))
		for _, path := range matches {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for section, terms := range parseLexicon(string(raw)) {
				add(section, terms)
			}
			fmt.Fprintf(os.Stderr, "mcp: credential vocabulary extended from %s\n", path)
		}
	}

	lx.compile()
	return lx
}

// parseLexicon reads the `## section` headings and the comma-separated terms
// under them. Everything else in the file is prose, which is the point of using
// Markdown: the reasoning lives beside the data instead of in a commit message
// nobody will find.
func parseLexicon(raw string) map[string][]string {
	out := map[string][]string{}
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			section = strings.ToLower(strings.TrimSpace(trimmed[3:]))
			continue
		}
		if section == "" || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A line that is prose rather than data: sentences end in a full stop
		// and contain spaces around their commas. Terms do not.
		if strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, ":") {
			continue
		}
		for _, term := range strings.Split(trimmed, ",") {
			term = strings.ToLower(strings.TrimSpace(term))
			if term != "" {
				out[section] = append(out[section], term)
			}
		}
	}
	return out
}

// alternation builds a regexp branch from terms, longest first.
//
// Longest first because Go's alternation prefers the leftmost branch that
// matches: with `pass|password` in that order, `password=x` matches `pass` and
// the rest of the word is then read as part of the separator, which fails.
// Sorting by length makes the longest name win, which is the one somebody meant.
func alternation(terms []string) string {
	sorted := make([]string, len(terms))
	copy(sorted, terms)
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i]) != len(sorted[j]) {
			return len(sorted[i]) > len(sorted[j])
		}
		return sorted[i] < sorted[j]
	})
	quoted := make([]string, 0, len(sorted))
	for _, term := range sorted {
		// Spaces in a term match any run of whitespace, so "api key" also
		// matches "api  key" and a name split across an alignment column.
		quoted = append(quoted, strings.ReplaceAll(regexp.QuoteMeta(term), `\ `, `\s+`))
	}
	return strings.Join(quoted, "|")
}

func (lx *credentialLexicon) compile() {
	if len(lx.names) > 0 {
		// The leading [\p{L}0-9_.-]* is what makes `db_password` and
		// `SENHA_DO_BANCO` match. \b does NOT work here: an underscore is a word
		// character, so there is no boundary inside db_password and the single
		// most common naming convention in every config file went undetected.
		//
		// \p{L} rather than a-z because the names are not all ASCII —
		// `contraseña_db` has to match too.
		// Affixes on BOTH sides, which is not symmetry for its own sake. The
		// prefix catches db_password; the suffix catches contraseña_db and
		// SENHA_DO_BANCO, where the name comes first and the qualifier after —
		// which is the ordinary word order in Spanish and Portuguese, so a
		// prefix-only rule quietly worked in English and failed in the two
		// languages the vocabulary was added for.
		//
		// It widens what matches, and structuralPlaceholder pays for it: the
		// values that arrive because of this are mostly paths and URLs
		// (passwords_file = /etc/shadow), and both are excluded there.
		lx.assignment = regexp.MustCompile(
			`(?i)[\p{L}0-9_.-]*(?:` + alternation(lx.names) +
				`)[\p{L}0-9_.-]*\s*[=:]\s*["'` + "`" + `]?([^\s"'` + "`" + `#,;]{8,})`)
	}
	if len(lx.phrases) > 0 {
		// A shorter minimum than an assignment. Somebody who writes "the
		// password is hunter2" has quoted the whole of it, and a phrase is a far
		// stronger signal than a variable name — nobody writes that sentence
		// about a value that is not a secret.
		lx.introduced = regexp.MustCompile(
			`(?i)(?:` + alternation(lx.phrases) +
				`)\s*[:=]?\s*["'` + "`" + `]?([^\s"'` + "`" + `#,;]{6,})`)
	}
	if len(lx.placeholders) > 0 {
		lx.placeholder = regexp.MustCompile(`(?i)^(?:` + alternation(lx.placeholders) + `)$`)
	}
}

// shapesFromLexicon turns the vocabulary into rules the watcher runs.
func shapesFromLexicon() []secretShape {
	lx := credentialWords()
	var out []secretShape
	if lx.assignment != nil {
		out = append(out, secretShape{
			id: "config_password", kind: "password in a configuration value",
			re: lx.assignment,
		})
	}
	if lx.introduced != nil {
		out = append(out, secretShape{
			id: "spoken_password", kind: "password written out in plain text",
			re: lx.introduced,
		})
	}
	return out
}

// isPlaceholder reports whether a value means nothing.
func isPlaceholder(value string) bool {
	if lx := credentialWords(); lx.placeholder != nil && lx.placeholder.MatchString(value) {
		return true
	}
	return structuralPlaceholder.MatchString(value)
}

// structuralPlaceholder is the shape-based half, which no dictionary covers:
// runs of x or asterisks, an angle-bracket or brace template, a dotted
// ellipsis. These are language-independent and belong in code rather than in a
// word list somebody would have to repeat per language.
var structuralPlaceholder = regexp.MustCompile(`(?i)^(?:` +
	`x{3,}|\*{3,}|\.{3,}|-{3,}|_{3,}|` +
	`<[^>]*>|\$\{[^}]*\}|\{\{[^}]*\}\}|%[a-z_]+%|` +
	`true|false|nil|0+|` +
	// A filesystem path is not a password. This is what pays for the suffix
	// allowance above: `passwords_file = /etc/shadow` and
	// `chave_do_certificado = ./certs/server.key` both match the name rule and
	// are both configuration pointing AT a secret rather than containing one.
	`[~.]?/[^\s]*|` +
	// A bare URL likewise — but only a BARE one. The [^\s@]* is load-bearing:
	// written as [^\s]* it also matched postgres://user:password@host, and the
	// url_password shape's own finding was then discarded as a placeholder. The
	// exclusion swallowed exactly the case it was written to leave alone, and
	// the only symptom was one fewer warning.
	`[a-z][a-z0-9+.-]*://[^\s@]*` +
	`)$`)
