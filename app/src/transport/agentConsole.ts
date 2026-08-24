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

/* A console session: the agent's own terminal, in a window.
 *
 * # Why a browser gets a terminal when it already has a chat panel
 *
 * Because parity was a race the panel could lose. Every slash command carried
 * onto the wire is one somebody can add to the terminal tomorrow and forget to
 * carry, and a browser permanently one release behind is one people stop
 * trusting for the thing they need right now. A console makes "everything the
 * agent can do" a property of the architecture rather than a list somebody
 * maintains.
 *
 * It does NOT replace the panel, and is not meant to. The panel is the better
 * surface for everything it covers — real names, descriptions, a picker, a
 * history drawer — and this is the way out for what it does not cover yet.
 *
 * # It is not a second agent
 *
 * What runs behind the terminal is the agent's own binary attaching to the same
 * runtime, so it is one more face on the same conversation: type in it and the
 * chat beside it shows the same exchange.
 *
 * # It is not a way around where a credential may go
 *
 * A terminal carried over WebRTC is bytes carried over WebRTC — the same path a
 * form field takes, plus an echo, on a screen a room may be recording. The
 * runtime refuses to store an API key that arrived from a session opened for a
 * browser, and this is one of those. Sign-in, which never produces a secret
 * anybody could paste, works here exactly as it does in a real terminal.
 */

import { useCallback, useEffect, useRef, useState } from 'react'

export interface AgentConsoleApi {
  /** Feed one message off the DataChannel. Returns true if it was ours. */
  handle(m: Record<string, unknown>): boolean
  /** Whether a terminal is open. */
  readonly open: boolean
  /** Open one, or bring the open one forward. */
  start(): void
  /** Close it. */
  stop(): void
  /** Attach the terminal to a DOM node; returns a teardown. */
  mount(node: HTMLElement): () => void
}

/** Every `t` this module claims. */
export function isConsoleMessage(t: string): boolean {
  return t === 'agent_console_data' || t === 'agent_console_close'
}

/* Base64 both ways, and neither direction is `atob`/`btoa` on their own.
 *
 * A terminal emits partial UTF-8 sequences across reads — half a character at
 * the end of one buffer and half at the start of the next — so the bytes have
 * to stay bytes until xterm joins them. atob gives a string of char codes;
 * turning that into a Uint8Array is what keeps the halves intact. Going the
 * other way, what somebody types may be any character at all, so it is encoded
 * to UTF-8 first and only then to base64. */
function decode(b64: string): Uint8Array {
  const raw = atob(b64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

function encode(text: string): string {
  const bytes = new TextEncoder().encode(text)
  let raw = ''
  for (const b of bytes) raw += String.fromCharCode(b)
  return btoa(raw)
}

export function useAgentConsole(
  send: (event: Record<string, unknown>) => void,
): AgentConsoleApi {
  const [open, setOpen] = useState(false)

  /* The terminal itself, held in refs because it is not React state: it owns a
   * canvas, a scrollback and a cursor, and re-creating it on a render would
   * throw all three away mid-session. */
  const termRef = useRef<import('@xterm/xterm').Terminal | null>(null)
  const fitRef = useRef<import('@xterm/addon-fit').FitAddon | null>(null)
  const idRef = useRef('')
  /* Output that arrived before the terminal was mounted. The open goes out as
   * soon as somebody presses the button and the runtime starts painting
   * immediately, which is well before React has put a node on screen — without
   * this the first frame of the session, which is the whole splash, is lost. */
  const pendingRef = useRef<Uint8Array[]>([])

  const handle = useCallback((m: Record<string, unknown>): boolean => {
    const t = typeof m.t === 'string' ? m.t : ''
    if (!isConsoleMessage(t)) return false
    if (typeof m.term === 'string' && m.term !== idRef.current) return true

    if (t === 'agent_console_data') {
      const bytes = decode(typeof m.bytes === 'string' ? m.bytes : '')
      if (termRef.current) termRef.current.write(bytes)
      else pendingRef.current.push(bytes)
      return true
    }

    // Closed. The reason, when there is one, goes into the terminal rather than
    // anywhere else: it is the last thing that happened in this window, and a
    // window that simply vanishes leaves somebody wondering whether they closed
    // it themselves.
    const why = typeof m.text === 'string' ? m.text : ''
    if (termRef.current && why) {
      termRef.current.write(`\r\n\x1b[31m${why}\x1b[0m\r\n`)
    }
    idRef.current = ''
    setOpen(false)
    return true
  }, [])

  const start = useCallback(() => {
    if (idRef.current) {
      setOpen(true)
      return
    }
    idRef.current = `term-${Date.now()}`
    pendingRef.current = []
    setOpen(true)
    send({ t: 'agent_console_open', term: idRef.current, cols: 100, rows: 30 })
  }, [send])

  const stop = useCallback(() => {
    if (idRef.current) send({ t: 'agent_console_close', term: idRef.current })
    idRef.current = ''
    setOpen(false)
  }, [send])

  const mount = useCallback(
    (node: HTMLElement) => {
      let alive = true
      let teardown = () => {}

      /* Loaded on demand. A terminal emulator is a quarter of a megabyte, and
       * the overwhelming majority of sessions never open one — making everybody
       * download it to look at a chat panel would be paying for the escape
       * hatch on every page load. */
      void Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')]).then(
        ([{ Terminal }, { FitAddon }]) => {
          if (!alive) return
          const term = new Terminal({
            fontSize: 13,
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
            cursorBlink: true,
            /* Read off the panel's own tokens rather than written out here, so
             * the terminal follows the viewer's theme instead of being a dark
             * rectangle on a light page. */
            theme: themeFromPage(),
            /* The agent's view repaints whole frames and never scrolls, so a
             * scrollback buffer would only ever hold frames nobody wants to
             * scroll back to. */
            scrollback: 0,
          })
          const fit = new FitAddon()
          term.loadAddon(fit)
          term.open(node)
          termRef.current = term
          fitRef.current = fit

          for (const chunk of pendingRef.current) term.write(chunk)
          pendingRef.current = []

          const typed = term.onData((data) => {
            if (idRef.current) {
              send({ t: 'agent_console_data', term: idRef.current, bytes: encode(data) })
            }
          })

          const resize = () => {
            try {
              fit.fit()
            } catch {
              /* the node can be measured as zero mid-transition */
            }
            if (idRef.current) {
              send({
                t: 'agent_console_resize',
                term: idRef.current,
                cols: term.cols,
                rows: term.rows,
              })
            }
          }
          const observer = new ResizeObserver(resize)
          observer.observe(node)
          resize()
          term.focus()

          teardown = () => {
            observer.disconnect()
            typed.dispose()
            term.dispose()
            termRef.current = null
            fitRef.current = null
          }
        },
      )

      return () => {
        alive = false
        teardown()
      }
    },
    [send],
  )

  useEffect(() => {
    // A page being left with a terminal open. Told rather than left to time
    // out, so the runtime is not holding a shell for a browser that has gone.
    return () => {
      if (idRef.current) send({ t: 'agent_console_close', term: idRef.current })
    }
  }, [send])

  return { handle, open, start, stop, mount }
}

/* The terminal's palette, from the page's own tokens.
 *
 * Written out rather than left to xterm's default, which is a fixed dark
 * scheme: on a viewer in light mode that is a black rectangle in the middle of
 * a pale page, which reads as broken rather than as a terminal. */
function themeFromPage() {
  const css = getComputedStyle(document.documentElement)
  const pick = (name: string, fallback: string) =>
    css.getPropertyValue(name).trim() || fallback
  return {
    background: pick('--sd-panel', '#0d1110'),
    foreground: pick('--sd-ink', '#e7edea'),
    cursor: pick('--sd-drive', '#2fd39b'),
    selectionBackground: pick('--sd-hover', '#243230'),
  }
}
