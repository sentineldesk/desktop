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

// Other participants' pointers, drawn onto the X display itself.
//
// Until now these lived only as DOM overlays in each browser: you saw the other
// person's cursor, but it was not really there. Nothing downstream could see it
// — not ximagesrc, so not the recording, not a screenshot, and not the stream
// anyone else was watching.
//
// Making them real X windows fixes all of that at once. The pointer becomes
// part of the desktop, so every consumer gets it for free, and a recording of a
// shared session actually shows who was doing what.
//
// Each pointer is a small override-redirect window — no title bar, ignored by
// the window manager — shaped with the SHAPE extension so it reads as an arrow
// with a name tag rather than a rectangle sitting on top of the screen.

import (
	"fmt"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
)

// Colours cycle per participant, the way collaborative editors do it: the shape
// says "someone is pointing", the colour says who.
// Reserved for the agent, and deliberately not in the rotation below: a person
// must never be handed the colour that means "this is not a person".
const AgentColour = 0xb08cf7 // violet

// The owner's palette, print-shop order with yellow dealt first: the first
// person in the room keeps the amber the product always used, and the next
// three take cyan, magenta and near-black — CMYK, the four inks everyone
// already knows apart. The room assigns these per member at join (lowest
// free, like the Viewer numbers), so a colour follows its person and never
// depends on who happened to move a mouse first.
var pointerColours = []uint32{
	0xf9c74f, // yellow — the brand's amber
	0x00aeef, // cyan
	0xec008c, // magenta
	0x262626, // key (near-black: pure black would lose its own name tag)
}

// PointerColour returns the palette entry for a member slot, wrapping when a
// room somehow outgrows the inks.
func PointerColour(slot int) uint32 {
	if slot < 0 {
		slot = 0
	}
	return pointerColours[slot%len(pointerColours)]
}

// tagInk picks the name tag's text colour by the ground it sits on: dark ink
// on the light inks, light ink on the dark ones — a black pointer with black
// text was no name at all.
func tagInk(colour uint32) uint32 {
	r, g, b := (colour>>16)&0xff, (colour>>8)&0xff, colour&0xff
	if (r*299+g*587+b*114)/1000 < 128 {
		return 0xe7edea
	}
	return 0x1b2133
}

// The arrow, as a bitmap rather than a formula.
//
// A generated triangle read as a wedge stuck to the corner of the name tag, not
// as a pointer. This is the familiar cursor silhouette — angled head, notch,
// tail — which the eye recognises immediately even when it is small.
var arrowMask = []string{
	"X...........",
	"XX..........",
	"XXX.........",
	"XXXX........",
	"XXXXX.......",
	"XXXXXX......",
	"XXXXXXX.....",
	"XXXXXXXX....",
	"XXXXXXXXX...",
	"XXXXXXXXXX..",
	"XXXXXXXXXXX.",
	"XXXXXXXXXXXX",
	"XXXXXXXX....",
	"XXXXXXX.....",
	"XXXX.XXX....",
	"XXX..XXX....",
	"XX....XXX...",
	"X.....XXX...",
	".......XX...",
}

const (
	// Drawn at double size. At 1:1 the head survives on a still desktop but the
	// video encoder, which spends almost no bits on a static background, smears
	// a one-pixel diagonal into the wallpaper — and the pointer only matters
	// once it has been through the encoder.
	ptrScale = 2
	ptrPadX  = 7 // breathing room either side of the name
	ptrPadY  = 4 // above and below it
)

var (
	ptrArrowW = len(arrowMask[0]) * ptrScale
	ptrArrowH = len(arrowMask) * ptrScale
)

// arrowCells walks the mask, handing each solid cell to fn along with whether
// it sits on an edge. Deriving the outline from the shape itself means the two
// never drift apart when the silhouette is edited.
func arrowCells(fn func(x, y int, edge bool)) {
	solid := func(r, c int) bool {
		return r >= 0 && r < len(arrowMask) && c >= 0 && c < len(arrowMask[r]) &&
			arrowMask[r][c] == 'X'
	}
	for r := range arrowMask {
		for c := range arrowMask[r] {
			if !solid(r, c) {
				continue
			}
			edge := !solid(r-1, c) || !solid(r+1, c) || !solid(r, c-1) || !solid(r, c+1)
			fn(c*ptrScale, r*ptrScale, edge)
		}
	}
}

type peerWindow struct {
	id       string
	win      xproto.Window
	gc       xproto.Gcontext
	name     string
	width    uint16
	height   uint16
	labelW   uint16
	labelH   uint16
	colour   uint32
	baseline int16
}

// PeerPointers keeps one small window per remote participant.
type PeerPointers struct {
	mu    sync.Mutex
	conn  *xgb.Conn
	root  xproto.Window
	depth byte
	font  xproto.Font
	peers map[string]*peerWindow
	fixed map[string]uint32 // identities whose colour is not from the rotation
	seq   int
	ok    bool

	// Real metrics, read from the server once. Guessing the width of a name
	// from its character count produces a tag that is too wide for short names
	// and clips long ones.
	fontAscent  int16
	fontDescent int16
	charWidths  map[byte]int16
	defaultChar int16
}

// NewPeerPointers connects to the display and prepares the SHAPE extension.
//
// It returns an error rather than a half-working object when SHAPE is missing:
// without it every pointer would be an opaque rectangle covering the desktop,
// which is worse than not drawing them at all.
func NewPeerPointers(display string) (*PeerPointers, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, err
	}
	if err := shape.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("SHAPE extension unavailable: %w", err)
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)

	p := &PeerPointers{
		conn:  conn,
		root:  screen.Root,
		depth: screen.RootDepth,
		peers: map[string]*peerWindow{},
		ok:    true,
	}

	// A bitmap font is enough for a name tag and costs nothing to load. Several
	// candidates are tried because the font path of a minimal container is not
	// guaranteed to carry any particular family; the fixed misc fonts are the
	// last resort and are present almost everywhere.
	for _, pattern := range []string{
		"-*-helvetica-bold-r-normal--12-*-*-*-*-*-iso8859-1",
		"-*-dejavu sans-bold-r-normal--12-*-*-*-*-*-iso8859-1",
		"-misc-fixed-bold-r-normal--13-*-*-*-c-*-iso8859-1",
		"fixed",
	} {
		fid, err := xproto.NewFontId(conn)
		if err != nil {
			break
		}
		if xproto.OpenFontChecked(conn, fid, uint16(len(pattern)), pattern).Check() != nil {
			continue
		}
		p.font = fid
		p.loadMetrics(fid)
		break
	}
	return p, nil
}

// loadMetrics reads the font's per-character widths so the tag can be sized to
// the text it will actually hold.
func (p *PeerPointers) loadMetrics(fid xproto.Font) {
	reply, err := xproto.QueryFont(p.conn, xproto.Fontable(fid)).Reply()
	if err != nil || reply == nil {
		return
	}
	p.fontAscent, p.fontDescent = reply.FontAscent, reply.FontDescent
	p.defaultChar = reply.MaxBounds.CharacterWidth
	p.charWidths = make(map[byte]int16, len(reply.CharInfos))
	first := int(reply.MinCharOrByte2)
	for i, ci := range reply.CharInfos {
		code := first + i
		if code > 255 {
			break
		}
		p.charWidths[byte(code)] = ci.CharacterWidth
	}
}

// textWidth measures a label, falling back to the font's widest glyph for any
// character the font does not define.
func (p *PeerPointers) textWidth(s string) int16 {
	if p.charWidths == nil {
		return int16(len(s)) * 7
	}
	var w int16
	for i := 0; i < len(s); i++ {
		if cw, ok := p.charWidths[s[i]]; ok && cw > 0 {
			w += cw
		} else {
			w += p.defaultChar
		}
	}
	return w
}

// SetColoured is Set with the colour chosen by the caller rather than by the
// rotation. Used for the agent, whose identity must not depend on join order.
func (p *PeerPointers) SetColoured(id, name string, x, y int, colour uint32) {
	p.forceColour(id, colour)
	p.Set(id, name, x, y)
}

func (p *PeerPointers) forceColour(id string, colour uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fixed == nil {
		p.fixed = map[string]uint32{}
	}
	p.fixed[id] = colour
}

// Set creates or moves the pointer belonging to one participant.
func (p *PeerPointers) Set(id, name string, x, y int) {
	if p == nil || !p.ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	w, ok := p.peers[id]
	if !ok {
		var err error
		if w, err = p.create(id, name); err != nil {
			return
		}
		p.peers[id] = w
	} else if w.name != name {
		w.name = name
		p.draw(w)
	}

	// Keep it on screen: a pointer half off the edge is worse than one nudged
	// back inside, because the name tag is what identifies it.
	xproto.ConfigureWindow(p.conn, w.win,
		xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowStackMode,
		[]uint32{uint32(int32(x)), uint32(int32(y)), xproto.StackModeAbove})
	p.conn.Sync()
}

// Remove takes a participant's pointer off the screen.
func (p *PeerPointers) Remove(id string) {
	if p == nil || !p.ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if w, ok := p.peers[id]; ok {
		xproto.DestroyWindow(p.conn, w.win)
		delete(p.peers, id)
		p.conn.Sync()
	}
}

// Clear removes every pointer, for when the room empties.
func (p *PeerPointers) Clear() {
	if p == nil || !p.ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, w := range p.peers {
		xproto.DestroyWindow(p.conn, w.win)
		delete(p.peers, id)
	}
	p.conn.Sync()
}

func (p *PeerPointers) create(id, name string) (*peerWindow, error) {
	win, err := xproto.NewWindowId(p.conn)
	if err != nil {
		return nil, err
	}
	colour, pinned := p.fixed[id]
	if !pinned {
		colour = pointerColours[p.seq%len(pointerColours)]
		p.seq++
	}

	// The tag is sized from the font, not guessed from the character count.
	ascent, descent := p.fontAscent, p.fontDescent
	if ascent == 0 {
		ascent, descent = 10, 3
	}
	labelH := uint16(ascent + descent + ptrPadY*2)
	labelW := uint16(p.textWidth(name)) + ptrPadX*2
	width := uint16(ptrArrowW/2) + labelW
	height := uint16(ptrArrowH) + labelH

	// override-redirect keeps the window manager out of it: no frame, no focus
	// stealing, no entry in the task list.
	err = xproto.CreateWindowChecked(p.conn, p.depth, win, p.root,
		0, 0, width, height, 0,
		xproto.WindowClassInputOutput, xproto.WindowClassCopyFromParent,
		xproto.CwBackPixel|xproto.CwOverrideRedirect,
		[]uint32{0x000000, 1}).Check()
	if err != nil {
		return nil, err
	}

	gc, err := xproto.NewGcontextId(p.conn)
	if err != nil {
		return nil, err
	}
	values := []uint32{colour, 0x000000}
	mask := uint32(xproto.GcForeground | xproto.GcBackground)
	if p.font != 0 {
		mask |= xproto.GcFont
		values = append(values, uint32(p.font))
	}
	if err := xproto.CreateGCChecked(p.conn, gc, xproto.Drawable(win), mask, values).Check(); err != nil {
		return nil, err
	}

	w := &peerWindow{id: id, win: win, gc: gc, name: name,
		width: width, height: height, labelW: labelW, labelH: labelH,
		colour: colour, baseline: int16(ptrArrowH) + ptrPadY + ascent}
	p.shapeWindow(w)
	xproto.MapWindow(p.conn, win)
	p.draw(w)
	return w, nil
}

// shapeWindow cuts the window down to an arrow plus a label, so what lands on
// the desktop looks like a cursor instead of a coloured box.
func (p *PeerPointers) shapeWindow(w *peerWindow) {
	rects := make([]xproto.Rectangle, 0, len(arrowMask)*len(arrowMask[0])+1)
	arrowCells(func(x, y int, _ bool) {
		rects = append(rects, xproto.Rectangle{
			X: int16(x), Y: int16(y), Width: ptrScale, Height: ptrScale,
		})
	})
	// The name tag hangs below and to the right of the arrow's tip.
	rects = append(rects, xproto.Rectangle{
		X: int16(ptrArrowW / 2), Y: int16(ptrArrowH),
		Width: w.labelW, Height: w.labelH,
	})
	shape.Rectangles(p.conn, shape.SoSet, shape.SkBounding, xproto.ClipOrderingUnsorted,
		w.win, 0, 0, rects)
}

func (p *PeerPointers) draw(w *peerWindow) {
	// Everything the shape left visible gets the participant's colour.
	xproto.ChangeGC(p.conn, w.gc, xproto.GcForeground, []uint32{w.colour})
	xproto.PolyFillRectangle(p.conn, xproto.Drawable(w.win), w.gc,
		[]xproto.Rectangle{{X: 0, Y: 0, Width: w.width, Height: w.height}})

	// A dark edge around both parts. Without it a pale colour on a pale desktop
	// has no boundary at all, and the pointer dissolves into the wallpaper —
	// which is exactly when someone needs to find it.
	xproto.ChangeGC(p.conn, w.gc, xproto.GcForeground, []uint32{0x1b2133})
	xproto.PolyRectangle(p.conn, xproto.Drawable(w.win), w.gc,
		[]xproto.Rectangle{{X: int16(ptrArrowW / 2), Y: int16(ptrArrowH),
			Width: w.labelW - 1, Height: w.labelH - 1}})

	// Every cell of the arrow that touches empty space, in the same dark tone.
	edges := make([]xproto.Rectangle, 0, 64)
	arrowCells(func(x, y int, edge bool) {
		if edge {
			edges = append(edges, xproto.Rectangle{
				X: int16(x), Y: int16(y), Width: ptrScale, Height: ptrScale,
			})
		}
	})
	xproto.PolyFillRectangle(p.conn, xproto.Drawable(w.win), w.gc, edges)

	if p.font == 0 || w.name == "" {
		return
	}

	// ImageText8 paints the glyph box with the GC's BACKGROUND before drawing
	// the text in the foreground. Leaving the background at the window's black
	// was the whole bug: dark text landed on a black box and the name was
	// invisible. Both colours have to be set together here.
	xproto.ChangeGC(p.conn, w.gc, xproto.GcForeground|xproto.GcBackground,
		[]uint32{tagInk(w.colour), w.colour})
	xproto.ImageText8(p.conn, byte(len(w.name)), xproto.Drawable(w.win), w.gc,
		int16(ptrArrowW/2+ptrPadX), w.baseline, w.name)
}

// Close releases the X connection.
func (p *PeerPointers) Close() {
	if p == nil || !p.ok {
		return
	}
	p.Clear()
	p.conn.Close()
	p.ok = false
}
