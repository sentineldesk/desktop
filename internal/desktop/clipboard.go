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

package desktop

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Clipboard reads and writes the X CLIPBOARD selection through xclip.
//
// Deduplication — not echoing back what the browser just sent — is a per-session
// concern and lives with the session, not here.
type Clipboard struct {
	display string
}

func NewClipboard(display string) *Clipboard {
	return &Clipboard{display: display}
}

// Get reads the CLIPBOARD selection.
func (c *Clipboard) Get() (string, bool) {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-o", "-display", c.display)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// Either the clipboard is empty or nothing currently owns the
		// selection; both are ordinary, not errors worth reporting.
		return "", false
	}
	return out.String(), true
}

// Set puts text on the CLIPBOARD selection.
//
// It returns an error because it can fail — xclip missing, the display gone —
// and this used to discard it. A write that silently did nothing was reported
// as a success, so the agent went on to paste something that was never there.
// Of the two ways to be wrong about a clipboard, that is the one that wastes
// somebody's afternoon.
func (c *Clipboard) Set(text string) error {
	return c.set("clipboard", text)
}

// SetBoth writes CLIPBOARD and PRIMARY alike. The virtual keyboard's paste
// needs it: GTK applications paste CLIPBOARD on Shift+Insert, xterm pastes
// PRIMARY, and a text that lands in only one of them types into some windows
// and vanishes into others.
func (c *Clipboard) SetBoth(text string) error {
	if err := c.set("clipboard", text); err != nil {
		return err
	}
	return c.set("primary", text)
}

func (c *Clipboard) set(selection, text string) error {
	cmd := exec.Command("xclip", "-selection", selection, "-i", "-display", c.display)
	cmd.Stdin = strings.NewReader(text)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// WaitDelay, because capturing stderr into a buffer makes Go create a pipe,
	// and xclip daemonises to own the selection: the surviving child inherits
	// that pipe and never closes it, so Run would wait for an EOF that is not
	// coming. The first version of this fix hung for sixty seconds and the tool
	// sweep caught it on the next run. tools.go already carried a comment
	// naming xclip as the example — it was right, and it was two files away.
	//
	// A QUARTER second, not two: xclip owns the selection within milliseconds
	// and everything after that is waiting on the pipe that never closes. At
	// two seconds, the virtual keyboard's paste (which sets both selections)
	// stalled the serialized input queue for four — long enough that the
	// Enter behind it overtook the paste's own landing.
	cmd.WaitDelay = 250 * time.Millisecond

	// ErrWaitDelay means the command itself finished fine and only the inherited
	// pipe was still open — which for xclip is not a failure but the whole
	// point, since the surviving child is what owns the selection. Treating it
	// as an error made this report a failure for a write that had worked, which
	// is the same dishonesty as before pointing the other way.
	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("xclip: %s", msg)
		}
		return fmt.Errorf("xclip: %w", err)
	}
	return nil
}
