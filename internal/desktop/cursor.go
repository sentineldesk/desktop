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
	"encoding/base64"
	"image"
	"image/png"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

// CursorState is the pointer's current shape, ready to drop into a CSS cursor
// rule: a PNG data URL plus its hotspot.
type CursorState struct {
	DataURL string
	HotX    int
	HotY    int
	// The bitmap's edge, so a client can tell a 24px shape from a 48px one
	// and label the big ones 2x — a Retina browser otherwise stretches the
	// small bitmap and the pointer arrives looking chewed.
	Width  int
	Height int
}

// CursorTracker follows X cursor-shape changes (the XFixes extension) and
// publishes them to the sessions.
//
// This is what lets the browser show resize arrows, the hand, the text beam and
// so on while still drawing the pointer locally — which is where the perceived
// zero latency comes from. Without it a locally drawn pointer would always look
// like a plain arrow, no matter what it is hovering over.
type CursorTracker struct {
	mu      sync.Mutex
	current CursorState
	subs    map[chan CursorState]struct{}
}

func NewCursorTracker(display string) (*CursorTracker, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, err
	}
	if err := xfixes.Init(conn); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := xfixes.QueryVersion(conn, 4, 0).Reply(); err != nil {
		conn.Close()
		return nil, err
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root
	err = xfixes.SelectCursorInputChecked(conn, root,
		xfixes.CursorNotifyMaskDisplayCursor).Check()
	if err != nil {
		conn.Close()
		return nil, err
	}

	t := &CursorTracker{subs: map[chan CursorState]struct{}{}}
	t.refresh(conn)
	go t.loop(conn)
	return t, nil
}

func (t *CursorTracker) loop(conn *xgb.Conn) {
	for {
		ev, err := conn.WaitForEvent()
		if ev == nil && err == nil {
			return // connection closed
		}
		if err != nil {
			continue
		}
		if _, ok := ev.(xfixes.CursorNotifyEvent); ok {
			t.refresh(conn)
		}
	}
}

// refresh reads the current cursor image and publishes it when it is usable.
func (t *CursorTracker) refresh(conn *xgb.Conn) {
	reply, err := xfixes.GetCursorImage(conn).Reply()
	if err != nil {
		return
	}
	w, h := int(reply.Width), int(reply.Height)
	// Browsers silently ignore oversized CSS cursors, so anything above 64px
	// would just leave the pointer looking wrong. Better to keep the last shape.
	if w == 0 || h == 0 || w > 64 || h > 64 || len(reply.CursorImage) < w*h {
		return
	}

	// X gives premultiplied ARGB, and PNG stores STRAIGHT alpha — and
	// png.Encode's fast path for *image.RGBA copies the bytes raw rather
	// than unpremultiplying. Fed premultiplied data, every semi-transparent
	// pixel came out darkened: a cursor with an antialiased edge grew black
	// fringes, and one that is mostly soft shadow (the I-beam, the drag
	// hand) rendered as a solid black slab. Unpremultiply into NRGBA, which
	// the encoder writes as-is.
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		p := reply.CursorImage[i]
		a := uint32(p >> 24)
		o := i * 4
		if a == 0 {
			// Fully transparent: leave the zeroes.
			continue
		}
		un := func(c uint32) uint8 {
			v := (c*255 + a/2) / a
			if v > 255 {
				v = 255
			}
			return uint8(v)
		}
		img.Pix[o] = un((p >> 16) & 0xff)  // R
		img.Pix[o+1] = un((p >> 8) & 0xff) // G
		img.Pix[o+2] = un(p & 0xff)        // B
		img.Pix[o+3] = uint8(a)            // A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return
	}
	state := CursorState{
		DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
		HotX:    int(reply.Xhot),
		HotY:    int(reply.Yhot),
		Width:   w,
		Height:  h,
	}

	t.mu.Lock()
	t.current = state
	for ch := range t.subs {
		select {
		case ch <- state:
		default:
			// A slow subscriber simply misses this shape and picks up the next
			// one. Blocking here would stall the X event loop for everybody.
		}
	}
	t.mu.Unlock()
}

// Subscribe returns the current shape plus a channel carrying later changes.
func (t *CursorTracker) Subscribe() (CursorState, chan CursorState) {
	ch := make(chan CursorState, 4)
	t.mu.Lock()
	t.subs[ch] = struct{}{}
	current := t.current
	t.mu.Unlock()
	return current, ch
}

func (t *CursorTracker) Unsubscribe(ch chan CursorState) {
	t.mu.Lock()
	delete(t.subs, ch)
	t.mu.Unlock()
}
