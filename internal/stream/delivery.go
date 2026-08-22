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

package stream

// Delivering a file from the container to the person watching.
//
// Screenshots and recordings can end up in two different places, and which one
// is right depends entirely on who asked:
//
//   - container: the file stays on the desktop's disk. This is what an agent
//     wants — it has no browser, and it may well want to keep working with the
//     file (re-encode it, ship it over SSH, attach it somewhere).
//   - download: the browser saves it on the machine of whoever is watching.
//     This is what a person wants — a screenshot that lands inside a container
//     they then have to go and fetch is barely a screenshot at all.
//
// Both capture from the same source, so the quality is identical; only the
// destination differs. The delivery itself is an OFFER on each recipient's
// files DataChannel: the message names an id, and the browser pulls the file
// through the channel it already holds — no HTTP, same as everything else the
// panel does. A one-use sixty-second ticket URL still rides beside the id,
// but only for the embedded dev-harness client, which has no files channel to
// pull with; it retires with that client.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Delivery hands a file on the desktop's disk to the connected browsers.
type Delivery struct {
	files *FileServer
	room  *Room
}

func NewDelivery(files *FileServer, room *Room) *Delivery {
	return &Delivery{files: files, room: room}
}

// Deliver tells the browsers to download the file at absPath.
//
// It goes to whoever holds control, because that is the person who asked — or,
// when nobody does, to every browser present. The count of clients told comes
// back so the caller can say "saved on the desktop, nobody was watching"
// instead of silently doing nothing.
//
// Two things this has to get right, and an earlier version got both wrong.
//
// The first is that not every room member has a browser. The agent is a member
// like anyone else — that is the point of the room — but it holds no Session,
// so a delivery loop that treats members and browsers as the same set
// dereferences nil and takes the whole daemon down with it. It is not an edge
// case either: when the controls are free the loop reaches every member, so the
// agent only has to be present, which it is from its first room_state onward.
//
// The second is that a ticket is good exactly once. One ticket shared between
// three browsers is one download and two dead links, so each recipient gets its
// own. That also means working out who the recipients are before minting
// anything: with nobody watching there is nothing to hand over, and a ticket
// nobody can use is just an entry sitting in the map until it expires.
func (d *Delivery) Deliver(absPath, name string) int {
	if d == nil || d.files == nil || d.room == nil {
		return 0
	}
	if name == "" {
		name = filepath.Base(absPath)
	}

	d.room.mu.RLock()
	members := d.room.snapshotMembers()
	controller := d.room.controller
	d.room.mu.RUnlock()

	// Who actually receives this. A member without a Session is in the room but
	// not at a browser, so it cannot be told about a download however much it
	// may be the one that asked for it.
	var targets []*roomMember
	for _, m := range members {
		if m.session == nil {
			continue
		}
		// A human at the controls asked for this, so it is theirs alone. When
		// the agent holds them, or nobody does, "download" can only mean the
		// people watching — there is no browser behind the agent to send it to.
		if controller != "" && controller != agentID && m.id != controller {
			continue
		}
		targets = append(targets, m)
	}
	if len(targets) == 0 {
		return 0
	}

	// The size travels with the offer so the browser can decide HOW to save
	// before it pulls a single byte: a screenshot lands in memory and downloads
	// itself, a two-hour recording asks for a place on disk first. A stat that
	// fails leaves it at zero — "unknown", which the client treats as small.
	var size int64
	if st, err := os.Stat(absPath); err == nil {
		size = st.Size()
	}

	sent := 0
	for _, m := range targets {
		// The offer points at the real path, bypassing FILES_ROOT: this is
		// the server handing over a file it just produced, not the browser
		// reaching into the filesystem, so the confinement that protects the
		// file manager does not apply here. The offer is an ID, not a URL —
		// the bytes only ever move over the recipient's own files channel.
		// (A `url` with a one-use ticket used to ride along for the old HTTP
		// door; the door closed on 2026-08-20 and the field went with it.)
		msg, err := json.Marshal(map[string]any{
			"t":       "download",
			"deliver": m.session.offerDelivery(absPath, name),
			"name":    name,
			"size":    size,
		})
		if err != nil {
			// The payload is the same shape every time, so this cannot fail
			// for one recipient and succeed for the next. Stop rather than
			// mint offers for messages that will not be sent.
			break
		}
		m.session.sendOnChannel(string(msg))
		sent++
	}
	return sent
}
