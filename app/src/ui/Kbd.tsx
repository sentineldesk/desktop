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

/* The on-screen keyboard: a full board drawn at the bottom of the screen,
 * styled after the key-rollover tester the owner pointed at and liked —
 * light aluminium deck, near-black keys, red while pressed, yellow when a
 * latch is armed. Spanish ISO and English ANSI layouts, with or without the
 * numpad, a compact "mini" variant, and a minimize pill so a tablet can
 * tuck it away without losing it.
 *
 * How keys travel — three routes, chosen per key, all learned the hard way:
 *
 *  - Letters, digits and named specials (Enter, arrows, F-keys…) go as kb
 *    key events; an uppercase letter wraps a REAL Shift press around the
 *    lowercase one, which is exactly what a physical keyboard does.
 *  - Everything else printable — º ! @ ñ ç € and every composed accent —
 *    goes as a kbt run, which lands as a clipboard paste on the desktop.
 *    Bare keycodes turn '>' into '.' (the symbol lives behind Shift) and
 *    the second layout group's letters are hostage to the X group state;
 *    the paste route survives every layout, measurably.
 *  - Dead keys compose HERE, like a phone keyboard: tap ´, the latch arms,
 *    tap a vowel, and the composed á travels as one character. The X
 *    server's own dead keys live in the second keyboard group where a bare
 *    keycode cannot reliably reach them.
 *
 * The ⊞ key is the menu key, as on a real Linux desktop: a tap opens the
 * panel's applications menu (the server runs the popup — Openbox cannot
 * bind a bare modifier press). */

import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

type Send = (event: Record<string, unknown>) => void

/* The menu key wears the product's own monitor, not a Windows glyph: this is
 * a Linux desktop and the key opens ITS menu — same shapes as the Brand mark,
 * filled in the logo's green. */
function MonitorKey() {
  return (
    <svg className="kbd-logo" viewBox="0 0 64 64" aria-hidden="true">
      <rect x="4" y="8" width="56" height="38" rx="6" fill="#3FD68C" />
      <rect x="10" y="14" width="44" height="26" rx="2" fill="#0b0e14" />
      <rect x="22" y="50" width="20" height="4" rx="2" fill="#3FD68C" />
      <rect x="16" y="54" width="32" height="4" rx="2" fill="#2fa96f" />
    </svg>
  )
}

/* One key: up to three legends (base, shift, altgr). `k` is a named special
 * sent as a kb event; absent `k`, the active legend decides the route. */
interface KeyDef {
  readonly base: string
  readonly shift?: string
  readonly altgr?: string
  readonly k?: string
  readonly mod?: 'shift' | 'ctrl' | 'alt' | 'altgr' | 'caps' | 'win'
  /** Width in grid columns (1u = 4). */
  readonly w?: number
  /** Extra rows of height (the ISO Enter). */
  readonly tall?: boolean
  readonly cls?: string
}

const DEAD: Record<string, Record<string, string>> = {
  '´': { a: 'á', e: 'é', i: 'í', o: 'ó', u: 'ú', A: 'Á', E: 'É', I: 'Í', O: 'Ó', U: 'Ú' },
  '¨': { a: 'ä', e: 'ë', i: 'ï', o: 'ö', u: 'ü', A: 'Ä', E: 'Ë', I: 'Ï', O: 'Ö', U: 'Ü' },
  '`': { a: 'à', e: 'è', i: 'ì', o: 'ò', u: 'ù', A: 'À', E: 'È', I: 'Ì', O: 'Ò', U: 'Ù' },
  '^': { a: 'â', e: 'ê', i: 'î', o: 'ô', u: 'û', A: 'Â', E: 'Ê', I: 'Î', O: 'Ô', U: 'Û' },
  '~': { a: 'ã', o: 'õ', n: 'ñ', A: 'Ã', O: 'Õ', N: 'Ñ' },
}

/* What travels as a bare kb event: keys whose keysym sits on the unshifted
 * first-group level everywhere. Anything else goes as a paste run. */
const KB_SAFE = /^[a-z0-9 ,.\-]$/

const letters = (row: string): KeyDef[] =>
  [...row].map((ch) => ({ base: ch, shift: ch.toUpperCase() }))

/* ---- the Spanish ISO main block ---- */
const ES_R1: KeyDef[] = [
  { base: 'º', shift: 'ª', altgr: '\\' },
  { base: '1', shift: '!', altgr: '|' },
  { base: '2', shift: '"', altgr: '@' },
  { base: '3', shift: '·', altgr: '#' },
  { base: '4', shift: '$', altgr: '~' },
  { base: '5', shift: '%' },
  { base: '6', shift: '&', altgr: '¬' },
  { base: '7', shift: '/' },
  { base: '8', shift: '(' },
  { base: '9', shift: ')' },
  { base: '0', shift: '=' },
  { base: "'", shift: '?' },
  { base: '¡', shift: '¿' },
  { base: '⌫', k: 'Backspace', w: 8 },
]
const ES_R2: KeyDef[] = [
  { base: 'Tab ⇥', k: 'Tab', w: 7, cls: 'small' },
  ...letters('qwertyuiop'),
  { base: '`', shift: '^', altgr: '[', cls: 'dead' },
  { base: '+', shift: '*', altgr: ']' },
  { base: '⏎', k: 'Enter', w: 5, tall: true },
]
const ES_R3: KeyDef[] = [
  { base: 'Mayús', k: 'CapsLock', mod: 'caps', w: 7, cls: 'small' },
  ...letters('asdfghjkl'),
  { base: 'ñ', shift: 'Ñ' },
  { base: '´', shift: '¨', altgr: '{', cls: 'dead' },
  { base: 'ç', shift: 'Ç', altgr: '}' },
]
const ES_R4: KeyDef[] = [
  { base: '⇧', mod: 'shift', w: 4 },
  { base: '<', shift: '>' },
  ...letters('zxcvbnm'),
  { base: ',', shift: ';' },
  { base: '.', shift: ':' },
  { base: '-', shift: '_' },
  { base: '⇧', mod: 'shift', w: 12 },
]

/* ---- the English ANSI main block ---- */
const EN_R1: KeyDef[] = [
  { base: '`', shift: '~' },
  { base: '1', shift: '!' },
  { base: '2', shift: '@' },
  { base: '3', shift: '#' },
  { base: '4', shift: '$' },
  { base: '5', shift: '%' },
  { base: '6', shift: '^' },
  { base: '7', shift: '&' },
  { base: '8', shift: '*' },
  { base: '9', shift: '(' },
  { base: '0', shift: ')' },
  { base: '-', shift: '_' },
  { base: '=', shift: '+' },
  { base: '⌫', k: 'Backspace', w: 8 },
]
const EN_R2: KeyDef[] = [
  { base: 'Tab ⇥', k: 'Tab', w: 6, cls: 'small' },
  ...letters('qwertyuiop'),
  { base: '[', shift: '{' },
  { base: ']', shift: '}' },
  { base: '\\', shift: '|', w: 6 },
]
const EN_R3: KeyDef[] = [
  { base: 'Caps', k: 'CapsLock', mod: 'caps', w: 8, cls: 'small' },
  ...letters('asdfghjkl'),
  { base: ';', shift: ':' },
  { base: "'", shift: '"' },
  { base: 'Enter ⏎', k: 'Enter', w: 8, cls: 'small' },
]
const EN_R4: KeyDef[] = [
  { base: '⇧', mod: 'shift', w: 10 },
  ...letters('zxcvbnm'),
  { base: ',', shift: '<' },
  { base: '.', shift: '>' },
  { base: '/', shift: '?' },
  { base: '⇧', mod: 'shift', w: 10 },
]

const ROW5: KeyDef[] = [
  { base: 'Ctrl', mod: 'ctrl', w: 5, cls: 'small' },
  { base: '⊞', k: 'menu', mod: 'win', w: 5 },
  { base: 'Alt', mod: 'alt', w: 5, cls: 'small' },
  { base: ' ', w: 25, cls: 'space' },
  { base: 'AltGr', mod: 'altgr', w: 5, cls: 'small' },
  { base: '⊞', k: 'menu', mod: 'win', w: 5 },
  { base: '☰', k: 'ContextMenu', w: 5 },
  { base: 'Ctrl', mod: 'ctrl', w: 5, cls: 'small' },
]

const F_ROW: KeyDef[] = [
  { base: 'Esc', k: 'Escape', cls: 'small' },
  ...Array.from({ length: 12 }, (_, i) => ({
    base: `F${i + 1}`,
    k: `F${i + 1}`,
    cls: 'small',
  })),
]

const GHOST: KeyDef = { base: '', cls: 'ghost' }
const NAV: KeyDef[][] = [
  [GHOST, GHOST, GHOST],
  [
    { base: 'Ins', k: 'Insert', cls: 'small' },
    { base: '⌂', k: 'Home' },
    { base: '⇞', k: 'PageUp' },
  ],
  [
    { base: 'Del', k: 'Delete', cls: 'small' },
    { base: 'Fin', k: 'End', cls: 'small' },
    { base: '⇟', k: 'PageDown' },
  ],
  [GHOST, GHOST, GHOST],
  [GHOST, { base: '↑', k: 'ArrowUp' }, GHOST],
  [
    { base: '←', k: 'ArrowLeft' },
    { base: '↓', k: 'ArrowDown' },
    { base: '→', k: 'ArrowRight' },
  ],
]

const PAD: KeyDef[] = [
  GHOST, GHOST, GHOST, GHOST,
  GHOST, { base: '/' }, { base: '*' }, { base: '-' },
  { base: '7' }, { base: '8' }, { base: '9' }, { base: '+', tall: true },
  { base: '4' }, { base: '5' }, { base: '6' },
  { base: '1' }, { base: '2' }, { base: '3' }, { base: '⏎', k: 'Enter', tall: true },
  { base: '0', w: 2 }, { base: '.' },
]

interface Prefs {
  layout: 'es' | 'en'
  num: boolean
  mini: boolean
}

function loadPrefs(): Prefs {
  try {
    const raw = window.localStorage.getItem('sentineldesk.kbd')
    if (raw) return { layout: 'es', num: true, mini: false, ...JSON.parse(raw) }
  } catch {
    /* no storage is a valid machine */
  }
  return { layout: 'es', num: true, mini: false }
}

/* The physical KeyboardEvent.code(s) a drawn key stands for, so a real
 * keystroke can light its twin. Position-based, like the rollover tester we
 * copied: the physical Q lights the drawn Q whatever the browser's layout
 * says the letter is. Modifiers and specials cover both sides where a
 * keyboard has two. Symbols that move between layouts are left out — the
 * mirror is a "watch me type" aid, not a layout diagram. */
function codeFor(key: KeyDef): string[] {
  switch (key.mod) {
    case 'shift':
      return ['ShiftLeft', 'ShiftRight']
    case 'ctrl':
      return ['ControlLeft', 'ControlRight']
    case 'alt':
      return ['AltLeft']
    case 'altgr':
      return ['AltRight']
    case 'caps':
      return ['CapsLock']
    case 'win':
      return ['MetaLeft', 'MetaRight']
  }
  const named: Record<string, string[]> = {
    Enter: ['Enter', 'NumpadEnter'],
    Backspace: ['Backspace'],
    Tab: ['Tab'],
    Escape: ['Escape'],
    ArrowUp: ['ArrowUp'],
    ArrowDown: ['ArrowDown'],
    ArrowLeft: ['ArrowLeft'],
    ArrowRight: ['ArrowRight'],
    Home: ['Home'],
    End: ['End'],
    PageUp: ['PageUp'],
    PageDown: ['PageDown'],
    Insert: ['Insert'],
    Delete: ['Delete'],
    ContextMenu: ['ContextMenu'],
  }
  if (key.k) {
    if (named[key.k]) return named[key.k]
    if (/^F([1-9]|1[0-2])$/.test(key.k)) return [key.k]
    return []
  }
  const b = key.base
  if (b.length === 1) {
    if (b >= 'a' && b <= 'z') return [`Key${b.toUpperCase()}`]
    if (b >= 'A' && b <= 'Z') return [`Key${b}`]
    if (b >= '0' && b <= '9') return [`Digit${b}`, `Numpad${b}`]
    if (b === ' ') return ['Space']
  }
  return []
}

export function Kbd({ send, onClose }: { send: Send; onClose(): void }) {
  const { t } = useTranslation()
  const [prefs, setPrefs] = useState<Prefs>(loadPrefs)
  const [min, setMin] = useState(false)
  const [shift, setShift] = useState(false)
  const [caps, setCaps] = useState(false)
  const [ctrl, setCtrl] = useState(false)
  const [alt, setAlt] = useState(false)
  const [altgr, setAltgr] = useState(false)
  const [dead, setDead] = useState<string | null>(null)
  const [pressed, setPressed] = useState<string | null>(null)
  const downKey = useRef<string | null>(null)
  /* The mirror: which PHYSICAL keys are down right now, by KeyboardEvent.code.
   * A person with a real keyboard sees their own typing light up here — a live
   * "this is how I write", and it doubles as proof the keys are landing. Local
   * to this browser and passive: it never preventDefaults, so the same
   * keystrokes still reach the desktop through desktopInput untouched. */
  const [downCodes, setDownCodes] = useState<ReadonlySet<string>>(new Set())

  useEffect(() => {
    try {
      window.localStorage.setItem('sentineldesk.kbd', JSON.stringify(prefs))
    } catch {
      /* fine */
    }
  }, [prefs])

  useEffect(() => {
    const down = (e: KeyboardEvent) =>
      setDownCodes((prev) => {
        if (prev.has(e.code)) return prev
        const next = new Set(prev)
        next.add(e.code)
        return next
      })
    const up = (e: KeyboardEvent) =>
      setDownCodes((prev) => {
        if (!prev.has(e.code)) return prev
        const next = new Set(prev)
        next.delete(e.code)
        return next
      })
    /* A blur mid-chord would strand keys lit; clear on losing focus. */
    const clear = () => setDownCodes(new Set())
    window.addEventListener('keydown', down)
    window.addEventListener('keyup', up)
    window.addEventListener('blur', clear)
    return () => {
      window.removeEventListener('keydown', down)
      window.removeEventListener('keyup', up)
      window.removeEventListener('blur', clear)
    }
  }, [])

  const legendOf = (key: KeyDef): string => {
    if (altgr && key.altgr) return key.altgr
    const upper = shift !== caps
    if (upper && key.shift) return key.shift
    return key.base
  }

  const clearLatches = () => {
    setShift(false)
    setCtrl(false)
    setAlt(false)
    setAltgr(false)
  }

  /* One character out the door, by whichever route carries it faithfully. */
  const sendChar = (ch: string) => {
    if (ctrl || alt) {
      /* A chord: real modifiers around the base key, like a keyboard. */
      if (ctrl) send({ t: 'kb', k: 'Control', d: 1 })
      if (alt) send({ t: 'kb', k: 'Alt', d: 1 })
      if (shift) send({ t: 'kb', k: 'Shift', d: 1 })
      const base = ch.length === 1 ? ch.toLowerCase() : ch
      send({ t: 'kb', k: base, d: 1 })
      send({ t: 'kb', k: base, d: 0 })
      if (shift) send({ t: 'kb', k: 'Shift', d: 0 })
      if (alt) send({ t: 'kb', k: 'Alt', d: 0 })
      if (ctrl) send({ t: 'kb', k: 'Control', d: 0 })
      return
    }
    if (KB_SAFE.test(ch)) {
      send({ t: 'kb', k: ch, d: 1 })
      send({ t: 'kb', k: ch, d: 0 })
      return
    }
    if (/^[A-ZÑÇ]$/.test(ch) && KB_SAFE.test(ch.toLowerCase())) {
      /* An uppercase letter is Shift around the lowercase one. */
      send({ t: 'kb', k: 'Shift', d: 1 })
      send({ t: 'kb', k: ch.toLowerCase(), d: 1 })
      send({ t: 'kb', k: ch.toLowerCase(), d: 0 })
      send({ t: 'kb', k: 'Shift', d: 0 })
      return
    }
    /* Symbols and accented letters: the paste route, one character. */
    send({ t: 'kbt', k: ch })
  }

  const tap = (key: KeyDef) => {
    if (key.mod === 'caps') {
      setCaps((v) => !v)
      return
    }
    if (key.mod === 'shift') {
      setShift((v) => !v)
      return
    }
    if (key.mod === 'ctrl') {
      setCtrl((v) => !v)
      return
    }
    if (key.mod === 'alt') {
      setAlt((v) => !v)
      return
    }
    if (key.mod === 'altgr') {
      setAltgr((v) => !v)
      return
    }
    if (key.k === 'menu') {
      send({ t: 'menu' })
      clearLatches()
      return
    }
    if (key.k) {
      /* Named specials, chorded if latches are armed. */
      if (ctrl) send({ t: 'kb', k: 'Control', d: 1 })
      if (alt) send({ t: 'kb', k: 'Alt', d: 1 })
      if (shift) send({ t: 'kb', k: 'Shift', d: 1 })
      send({ t: 'kb', k: key.k, d: 1 })
      send({ t: 'kb', k: key.k, d: 0 })
      if (shift) send({ t: 'kb', k: 'Shift', d: 0 })
      if (alt) send({ t: 'kb', k: 'Alt', d: 0 })
      if (ctrl) send({ t: 'kb', k: 'Control', d: 0 })
      clearLatches()
      return
    }
    const legend = legendOf(key)
    if (key.cls === 'dead') {
      /* Arm composition — next vowel comes out accented. Twice = literal. */
      if (dead === legend) {
        sendChar(legend)
        setDead(null)
      } else {
        setDead(legend)
      }
      clearLatches()
      return
    }
    if (dead) {
      const composed = DEAD[dead]?.[legend]
      setDead(null)
      if (composed) {
        sendChar(composed)
        clearLatches()
        return
      }
      /* No composition: the accent was meant literally, then the key. */
      sendChar(dead)
    }
    sendChar(legend === ' ' ? ' ' : legend)
    clearLatches()
  }

  const rows: KeyDef[][] =
    prefs.layout === 'es'
      ? [ES_R1, ES_R2, ES_R3, ES_R4, ROW5]
      : [EN_R1, EN_R2, EN_R3, EN_R4, ROW5]

  const latched = (key: KeyDef): boolean =>
    (key.mod === 'shift' && shift) ||
    (key.mod === 'caps' && caps) ||
    (key.mod === 'ctrl' && ctrl) ||
    (key.mod === 'alt' && alt) ||
    (key.mod === 'altgr' && altgr) ||
    (key.cls === 'dead' && dead !== null && legendOf(key) === dead)

  const keyButton = (key: KeyDef, id: string) => {
    const legend = legendOf(key)
    const showThree = !key.k && !key.mod && (key.shift || key.altgr) && !/^[a-zA-ZñÑçÇ]$/.test(key.base)
    const mirrored = codeFor(key).some((c) => downCodes.has(c))
    return (
      <button
        key={id}
        type="button"
        className={[
          'kbd-key',
          key.cls === 'small' ? 'kbd-small' : '',
          key.cls === 'ghost' ? 'kbd-ghost' : '',
          key.cls === 'space' ? 'kbd-space' : '',
          key.tall ? 'kbd-tall' : '',
          pressed === id || mirrored ? 'kbd-press' : '',
          latched(key) ? 'kbd-armed' : '',
        ]
          .join(' ')
          .trim()}
        style={key.w ? { gridColumn: `span ${key.w}` } : undefined}
        onPointerDown={(e) => {
          e.preventDefault()
          if (key.cls === 'ghost') return
          setPressed(id)
          downKey.current = id
        }}
        onPointerUp={(e) => {
          e.preventDefault()
          setPressed(null)
          if (key.cls === 'ghost' || downKey.current !== id) return
          downKey.current = null
          tap(key)
        }}
        onPointerLeave={() => {
          if (pressed === id) setPressed(null)
        }}
        onContextMenu={(e) => e.preventDefault()}
      >
        {key.k === 'menu' ? (
          <MonitorKey />
        ) : showThree ? (
          <span className="kbd-legends">
            <i>{key.shift ?? ''}</i>
            <i className="kbd-altgr">{key.altgr ?? ''}</i>
            <i>{key.base}</i>
            <i />
          </span>
        ) : legend.trim() === '' ? (
          ' '
        ) : (
          legend
        )}
      </button>
    )
  }

  if (min) {
    return (
      <button
        id="kbd-pill"
        type="button"
        title={t('toolbar.kb')}
        onClick={() => setMin(false)}
      >
        ⌨
      </button>
    )
  }

  return (
    <div id="kbd" className={prefs.mini ? 'mini' : ''}>
      <div id="kbd-bar">
        <div className="kbd-tabs">
          <button
            type="button"
            className={prefs.layout === 'es' ? 'active' : ''}
            onClick={() => setPrefs((p) => ({ ...p, layout: 'es' }))}
          >
            ES
          </button>
          <button
            type="button"
            className={prefs.layout === 'en' ? 'active' : ''}
            onClick={() => setPrefs((p) => ({ ...p, layout: 'en' }))}
          >
            EN
          </button>
          <button
            type="button"
            className={prefs.num && !prefs.mini ? 'active' : ''}
            disabled={prefs.mini}
            onClick={() => setPrefs((p) => ({ ...p, num: !p.num }))}
          >
            123
          </button>
          <button
            type="button"
            className={prefs.mini ? 'active' : ''}
            onClick={() => setPrefs((p) => ({ ...p, mini: !p.mini }))}
          >
            Mini
          </button>
        </div>
        <div className="kbd-tabs">
          <button type="button" onClick={() => setMin(true)} title={t('kb.minimize')}>
            —
          </button>
          <button type="button" onClick={onClose} title={t('a11y.close')}>
            ✕
          </button>
        </div>
      </div>

      <div id="kbd-deck">
        <div className="kbd-main">
          {!prefs.mini ? (
            <>
              {F_ROW.map((k, i) => keyButton({ ...k, w: i === 0 ? 8 : 4 }, `f${i}`))}
              {keyButton({ base: '', cls: 'ghost', w: 4 }, 'f-fill')}
            </>
          ) : null}
          {rows.map((row, r) => row.map((k, i) => keyButton(k, `r${r}-${i}`)))}
        </div>
        {!prefs.mini && prefs.num ? (
          <>
            <div className="kbd-nav">
              {NAV.map((row, r) => row.map((k, i) => keyButton(k, `n${r}-${i}`)))}
            </div>
            <div className="kbd-pad">
              {PAD.map((k, i) => keyButton(k, `p${i}`))}
            </div>
          </>
        ) : null}
      </div>
    </div>
  )
}
