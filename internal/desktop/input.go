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
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// Browser event.key values that have no direct Latin-1 keysym.
var specialKeysyms = map[string]uint32{
	"Enter": 0xff0d, "Backspace": 0xff08, "Tab": 0xff09, "Escape": 0xff1b,
	" ": 0x0020, "Spacebar": 0x0020,
	"ArrowUp": 0xff52, "ArrowDown": 0xff54, "ArrowLeft": 0xff51, "ArrowRight": 0xff53,
	"Home": 0xff50, "End": 0xff57, "PageUp": 0xff55, "PageDown": 0xff56,
	"Insert": 0xff63, "Delete": 0xffff,
	"Shift": 0xffe1, "Control": 0xffe3, "Alt": 0xffe9, "AltGraph": 0xfe03,
	"Meta": 0xffeb, "OS": 0xffeb, "ContextMenu": 0xff67,
	"CapsLock": 0xffe5, "NumLock": 0xff7f, "ScrollLock": 0xff14,
	// The Spanish layout's dead keys, sent by name from the on-screen
	// keyboard: a dead keysym COMPOSES (´ then a → á) where the plain
	// character U+00B4 would just print an accent mark and move on. The
	// keycodes exist because the display loads us,es (KEYBOARD_LAYOUT).
	"DeadAcute": 0xfe51, "DeadGrave": 0xfe50, "DeadCircumflex": 0xfe52,
	"DeadDiaeresis": 0xfe57, "DeadTilde": 0xfe53,
	"PrintScreen": 0xff61, "Pause": 0xff13,
	"F1": 0xffbe, "F2": 0xffbf, "F3": 0xffc0, "F4": 0xffc1,
	"F5": 0xffc2, "F6": 0xffc3, "F7": 0xffc4, "F8": 0xffc5,
	"F9": 0xffc6, "F10": 0xffc7, "F11": 0xffc8, "F12": 0xffc9,
}

// InputInjector injects keyboard and mouse events into the X display via
// XTEST. It talks the X protocol directly (jezek/xgb, no CGo) rather than
// shelling out to xdotool per event, which would cost a process per keystroke.
type InputInjector struct {
	conn *xgb.Conn
	root xproto.Window

	mu             sync.Mutex
	keycodeFor     map[uint32]byte // keysym -> keycode, per the server's keymap
	pressedKeys    map[byte]bool
	pressedButtons map[byte]bool
}

// NewInputInjector connects to the display, retrying for up to 60 seconds:
// supervisor may start this before Xvfb has finished coming up.
func NewInputInjector(display string) (*InputInjector, error) {
	var conn *xgb.Conn
	var err error
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err = xgb.NewConnDisplay(display)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err := xtest.Init(conn); err != nil {
		return nil, fmt.Errorf("XTEST extension: %w", err)
	}
	if _, err := xtest.GetVersion(conn, 2, 2).Reply(); err != nil {
		return nil, fmt.Errorf("XTEST version: %w", err)
	}

	setup := xproto.Setup(conn)
	inj := &InputInjector{
		conn:           conn,
		root:           setup.DefaultScreen(conn).Root,
		pressedKeys:    map[byte]bool{},
		pressedButtons: map[byte]bool{},
	}
	if err := inj.loadKeymap(); err != nil {
		return nil, err
	}
	return inj, nil
}

// loadKeymap reads the server's keyboard map and builds keysym -> keycode.
//
// Columns are walked in order so that the unmodified level wins: otherwise a
// letter could resolve to the keycode that only produces it with Shift.
func (in *InputInjector) loadKeymap() error {
	setup := xproto.Setup(in.conn)
	min, max := setup.MinKeycode, setup.MaxKeycode
	count := byte(max - min + 1)
	reply, err := xproto.GetKeyboardMapping(in.conn, min, count).Reply()
	if err != nil {
		return fmt.Errorf("GetKeyboardMapping: %w", err)
	}
	per := int(reply.KeysymsPerKeycode)
	m := map[uint32]byte{}
	for col := 0; col < per; col++ {
		for i := 0; i < int(count); i++ {
			sym := uint32(reply.Keysyms[i*per+col])
			if sym == 0 {
				continue
			}
			if _, seen := m[sym]; !seen {
				m[sym] = byte(min) + byte(i)
			}
		}
	}
	in.keycodeFor = m
	return nil
}

func (in *InputInjector) fake(typ byte, detail byte, x, y int16) {
	xtest.FakeInput(in.conn, typ, detail, xproto.TimeCurrentTime, in.root, x, y, 0)
}

func (in *InputInjector) Move(x, y int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.fake(xproto.MotionNotify, 0, int16(x), int16(y))
}

func (in *InputInjector) Button(btn int, down bool) {
	if btn < 1 || btn > 3 {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	typ := byte(xproto.ButtonPress)
	if !down {
		typ = xproto.ButtonRelease
	}
	in.fake(typ, byte(btn), 0, 0)
	if down {
		in.pressedButtons[byte(btn)] = true
	} else {
		delete(in.pressedButtons, byte(btn))
	}
}

// Wheel emits scroll clicks: buttons 4/5 vertically, 6/7 horizontally.
func (in *InputInjector) Wheel(ticksY, ticksX int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	clicks := func(n int, negBtn, posBtn byte) {
		btn := negBtn
		if n > 0 {
			btn = posBtn
		}
		if n < 0 {
			n = -n
		}
		if n > 10 {
			n = 10
		}
		for i := 0; i < n; i++ {
			in.fake(xproto.ButtonPress, btn, 0, 0)
			in.fake(xproto.ButtonRelease, btn, 0, 0)
		}
	}
	clicks(ticksY, 4, 5)
	clicks(ticksX, 6, 7)
}

func keysymFor(key string) uint32 {
	if sym, ok := specialKeysyms[key]; ok {
		return sym
	}
	runes := []rune(key)
	if len(runes) != 1 {
		return 0
	}
	cp := uint32(runes[0])
	// Latin-1 keysyms are numerically equal to their Unicode codepoint; every
	// other character uses the 0x01000000 | codepoint convention.
	if cp <= 0xff {
		return cp
	}
	return 0x01000000 | cp
}

func (in *InputInjector) Key(key string, down bool) {
	sym := keysymFor(key)
	if sym == 0 {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	keycode, ok := in.keycodeFor[sym]
	if !ok {
		// Not in the keymap at all — a character the active layout has no key
		// for. xdotool remaps a spare keycode on the fly; do it only on press,
		// since by release time the temporary mapping is already gone.
		if down {
			runes := []rune(key)
			if len(runes) == 1 {
				exec.Command("xdotool", "type", "--clearmodifiers", "--", key).Start()
			}
		}
		return
	}
	typ := byte(xproto.KeyPress)
	if !down {
		typ = xproto.KeyRelease
	}
	in.fake(typ, keycode, 0, 0)
	if down {
		in.pressedKeys[keycode] = true
	} else {
		delete(in.pressedKeys, keycode)
	}
}

// There is deliberately no TypeText here. Typing a whole string through
// XTEST was tried and retired: synthesising bare keycodes turns '>' into '.'
// (the keysym lives behind Shift), xdotool type drops the second keyboard
// group's letters ("echo ñandú" arrived as "echo and" with us,es loaded),
// and remapping a spare keycode per rune races every client's cached keymap
// through asynchronous MappingNotify — and once carried a panic into
// production for one test keystroke. Text runs travel as a clipboard paste
// instead (see the kbt handler in stream/session.go), which survives every
// layout because no keymap is consulted at all.

// Pointer returns the pointer's current position on screen.
func (in *InputInjector) Pointer() (int, int, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	reply, err := xproto.QueryPointer(in.conn, in.root).Reply()
	if err != nil {
		return 0, 0, err
	}
	return int(reply.RootX), int(reply.RootY), nil
}

// Pixel reads the RGB colour of one point on screen.
func (in *InputInjector) Pixel(x, y int) (r, g, b uint8, err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	img, err := xproto.GetImage(in.conn, xproto.ImageFormatZPixmap,
		xproto.Drawable(in.root), int16(x), int16(y), 1, 1, 0xffffffff).Reply()
	if err != nil {
		return 0, 0, 0, err
	}
	if len(img.Data) < 3 {
		return 0, 0, 0, fmt.Errorf("empty image reply")
	}
	// Little-endian 24/32-bit TrueColor: bytes arrive as B,G,R[,X].
	return img.Data[2], img.Data[1], img.Data[0], nil
}

// Screen returns the screen size in pixels.
//
// It queries the root window's REAL geometry on every call rather than reading
// the connection setup, which is frozen at connect time. After a RandR resize
// (set_resolution) the setup would still report the old size — and every
// coordinate computed from it would be wrong.
func (in *InputInjector) Screen() (int, int) {
	setup := xproto.Setup(in.conn)
	s := setup.DefaultScreen(in.conn)
	geom, err := xproto.GetGeometry(in.conn, xproto.Drawable(s.Root)).Reply()
	if err != nil || geom == nil {
		return int(s.WidthInPixels), int(s.HeightInPixels)
	}
	return int(geom.Width), int(geom.Height)
}

// ReleaseAll drops anything still held down, on disconnect or focus loss.
// Without it a key held when the tab closes stays pressed on the desktop.
func (in *InputInjector) ReleaseAll() {
	in.mu.Lock()
	defer in.mu.Unlock()
	for keycode := range in.pressedKeys {
		in.fake(xproto.KeyRelease, keycode, 0, 0)
	}
	in.pressedKeys = map[byte]bool{}
	for btn := range in.pressedButtons {
		in.fake(xproto.ButtonRelease, btn, 0, 0)
	}
	in.pressedButtons = map[byte]bool{}
}
