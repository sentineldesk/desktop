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

// Matching a phrase in tesseract's word-level output.
//
// No screen and no tesseract: the TSV below is the real format, and everything
// that decides what matched and where is arithmetic over it. The case that
// matters most is the one find_text could never do — a needle containing a
// space — which failed silently as "no match on screen" for as long as the tool
// existed, because no single row of a word-level table ever contains one.

import "testing"

// Columns are tesseract's: level, page, block, par, line, word, left, top,
// width, height, conf, text. The capture is at 2x, which is why the expected
// screen coordinates below are half of these.
const tsvHeader = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"

func tsvRow(block, par, line, word, left, top, w, h int, conf, text string) string {
	return "5\t1\t" +
		itoa(block) + "\t" + itoa(par) + "\t" + itoa(line) + "\t" + itoa(word) + "\t" +
		itoa(left) + "\t" + itoa(top) + "\t" + itoa(w) + "\t" + itoa(h) + "\t" +
		conf + "\t" + text + "\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// "Save changes" on one line, "Discard" on the next.
func sampleTSV() string {
	return tsvHeader +
		tsvRow(1, 1, 1, 1, 100, 200, 80, 20, "96.5", "Save") +
		tsvRow(1, 1, 1, 2, 190, 200, 120, 20, "88.0", "changes") +
		tsvRow(1, 1, 2, 1, 100, 300, 100, 20, "99.1", "Discard")
}

func TestOCRFindsAPhraseAcrossWords(t *testing.T) {
	hits := ocrLineMatches(sampleTSV(), "save changes", 0, 0)
	if len(hits) != 1 {
		t.Fatalf("got %d hits for a two-word phrase, want 1 — this is the case that used to return nothing", len(hits))
	}
	h := hits[0]
	if h["text"] != "Save changes" {
		t.Fatalf("text = %v, want \"Save changes\"", h["text"])
	}
	// The box spans both words: left of the first to the right edge of the
	// second, halved for the 2x capture. 100..310 becomes 50..155.
	if h["x"] != 50 || h["width"] != 105 {
		t.Fatalf("box x=%v width=%v, want 50 and 105 (the union of both words)", h["x"], h["width"])
	}
	// A phrase is only as trustworthy as its weakest word.
	if h["confidence"] != 88.0 {
		t.Fatalf("confidence = %v, want the minimum 88.0 rather than an average", h["confidence"])
	}
}

func TestOCRSingleWordStillWorks(t *testing.T) {
	hits := ocrLineMatches(sampleTSV(), "discard", 0, 0)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0]["center_x"] != 75 || hits[0]["center_y"] != 155 {
		t.Fatalf("centre = (%v,%v), want (75,155)", hits[0]["center_x"], hits[0]["center_y"])
	}
}

func TestOCRDoesNotMatchAcrossLines(t *testing.T) {
	// "changes Discard" is adjacent in the reading but not on one line. Joining
	// the whole page into a single string would find it and hand back a box
	// spanning two rows of the screen, which is not somewhere a click belongs.
	if hits := ocrLineMatches(sampleTSV(), "changes discard", 0, 0); len(hits) != 0 {
		t.Fatalf("matched across a line boundary: %v", hits)
	}
}

func TestOCRTranslatesByTheRegionOrigin(t *testing.T) {
	// The capture was of a region starting at (400,50), so screen coordinates
	// are the halved box plus that origin. Getting this wrong sends the click
	// to the wrong part of the screen, which is worse than finding nothing.
	hits := ocrLineMatches(sampleTSV(), "discard", 400, 50)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0]["x"] != 450 || hits[0]["y"] != 200 {
		t.Fatalf("position = (%v,%v), want (450,200)", hits[0]["x"], hits[0]["y"])
	}
}

func TestOCRReportsEveryOccurrence(t *testing.T) {
	tsv := tsvHeader +
		tsvRow(1, 1, 1, 1, 10, 10, 40, 10, "90", "open") +
		tsvRow(1, 1, 2, 1, 10, 40, 40, 10, "90", "open")
	if hits := ocrLineMatches(tsv, "open", 0, 0); len(hits) != 2 {
		t.Fatalf("got %d hits for a word appearing twice, want 2", len(hits))
	}
}

func TestOCRIgnoresRowsWithoutText(t *testing.T) {
	// tesseract emits structural rows — page, block, paragraph — with an empty
	// text column and a confidence of -1. Treating those as words would put
	// blanks into the reassembled line and shift every offset after them.
	tsv := tsvHeader +
		"1\t1\t0\t0\t0\t0\t0\t0\t1920\t1080\t-1\t\n" +
		tsvRow(1, 1, 1, 1, 100, 200, 80, 20, "96.5", "Save") +
		tsvRow(1, 1, 1, 2, 190, 200, 120, 20, "88.0", "changes")
	hits := ocrLineMatches(tsv, "save changes", 0, 0)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 — a structural row broke the line", len(hits))
	}
}

func TestOCREmptyInputFindsNothing(t *testing.T) {
	if hits := ocrLineMatches(tsvHeader, "anything", 0, 0); len(hits) != 0 {
		t.Fatalf("found %d hits in an empty page", len(hits))
	}
}

func TestCleanA11yTextDropsInvisibleFiller(t *testing.T) {
	// A toolbar of icons comes back as a run of object replacement characters:
	// one line per toolbar, no information in any of them.
	if got := cleanA11yText("\uFFFC\uFFFC\uFFFC\uFFFC"); got != "" {
		t.Fatalf("a line of image placeholders survived as %q", got)
	}
	if got := cleanA11yText("  \u200B \uFEFF "); got != "" {
		t.Fatalf("zero-width filler survived as %q", got)
	}
	// Text with an inline icon keeps the text.
	if got := cleanA11yText("\uFFFCSave changes"); got != "Save changes" {
		t.Fatalf("got %q, want \"Save changes\"", got)
	}
	// Ordinary text is untouched, accents included.
	if got := cleanA11yText("  Guardar cambios  "); got != "Guardar cambios" {
		t.Fatalf("got %q", got)
	}
}
