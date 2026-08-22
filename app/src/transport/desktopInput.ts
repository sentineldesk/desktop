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

/* The keyboard and mouse, translated into the runtime's vocabulary.
 *
 * Transcribed from internal/webui/assets/client.js — the client that has been
 * driving XTEST since before this panel existed — because the failure mode of
 * paraphrasing an input protocol is not an error message, it is a desktop
 * where double-clicks land 4 pixels apart and keyboard shortcuts type
 * letters. Same event names, same button numbering, same wheel quantum.
 *
 * Two differences from that client, both deliberate:
 *
 *  - Everything here is gated on HOLDING the controls. The runtime tolerates
 *    input from watchers (it warns), but a panel that streams mouse moves it
 *    knows will be refused is spending bandwidth to generate warnings.
 *  - Key events are dropped when they belong to the panel's own widgets — a
 *    chat box, a dialog. The runtime's client owns its whole page and can
 *    take every key; this panel shares the window with the rest of the app,
 *    and a desktop that types what somebody wrote into the chat would be a
 *    faithful transcription of the wrong conversation.
 */

import type { Desktop } from './useDesktopStream'

/** Browser wheel deltas per one X scroll tick — the runtime's own quantum. */
const WHEEL_QUANTUM = 60
/** Mouse-move budget: up to 120 events a second, matching the runtime. */
const MOVE_INTERVAL_MS = 1000 / 120

/* DOM button (0 left, 1 middle, 2 right) → X button (1 left, 2 middle,
 * 3 right). The switcheroo in the middle is exactly why this is a function
 * and not arithmetic inlined at four call sites. */
function xButton(domButton: number): number {
  return domButton === 1 ? 2 : domButton === 2 ? 3 : 1
}

/* Where a viewport event lands on the REMOTE screen.
 *
 * `object-fit: contain` letterboxes the video, so the element's box and the
 * picture inside it differ; mapping through the element alone would make
 * clicks drift toward the corners. Returns null outside the picture — the
 * letterbox is the panel's furniture, not the desktop's. */
function remoteCoords(
  video: HTMLVideoElement,
  e: MouseEvent,
): { x: number; y: number } | null {
  const rect = video.getBoundingClientRect()
  const vw = video.videoWidth
  const vh = video.videoHeight
  if (!vw || !vh) return null
  const scale = Math.min(rect.width / vw, rect.height / vh)
  const dw = vw * scale
  const dh = vh * scale
  const ox = rect.left + (rect.width - dw) / 2
  const oy = rect.top + (rect.height - dh) / 2
  const x = Math.round((e.clientX - ox) / scale)
  const y = Math.round((e.clientY - oy) / scale)
  if (x < 0 || y < 0 || x >= vw || y >= vh) return null
  return { x, y }
}

/** True when a key event belongs to one of the panel's own widgets. */
function belongsToThePanel(e: KeyboardEvent): boolean {
  const el = e.target
  if (!(el instanceof HTMLElement)) return false
  return (
    el.tagName === 'INPUT' ||
    el.tagName === 'TEXTAREA' ||
    el.tagName === 'SELECT' ||
    el.isContentEditable
  )
}

/* Wire a video element to the desktop's input channel. Returns the cleanup.
 *
 * Native listeners rather than React props, for two reasons that are really
 * one: `wheel` must be non-passive to stop the page scrolling under the
 * desktop, and React's synthetic wheel listener is passive by design; and
 * the keyboard lives on `window`, which React does not manage at all. One
 * mechanism for all of them beats two mechanisms split by a framework
 * detail. */
export function attachDesktopInput(
  video: HTMLVideoElement,
  desktop: Pick<Desktop, 'sendInput' | 'control' | 'pushClipboard'>,
  isDriving: () => boolean,
): () => void {
  let lastMove = 0
  let wheelAccX = 0
  let wheelAccY = 0

  const onMouseMove = (e: MouseEvent) => {
    /* Movement is NOT gated on the controls, deliberately: a watcher's mm
     * injects nothing — the server draws it as their named peer pointer, in
     * their colour, for everyone else. That is the first client's behaviour
     * and the room's whole "you can see what each person is pointing at"
     * promise; the gate here silently unplugged it for a while, and the
     * pointers were reported missing. Clicks and keys stay gated below. */
    const now = performance.now()
    if (now - lastMove < MOVE_INTERVAL_MS) return
    lastMove = now
    const p = remoteCoords(video, e)
    if (p) desktop.sendInput({ t: 'mm', x: p.x, y: p.y })
  }

  const onMouseDown = (e: MouseEvent) => {
    if (!isDriving()) return
    const p = remoteCoords(video, e)
    if (!p) return
    /* The move first, so the click lands where the person aimed even if the
     * last throttled move is a frame stale. */
    desktop.sendInput({ t: 'mm', x: p.x, y: p.y })
    desktop.sendInput({ t: 'mb', b: xButton(e.button), d: 1 })
    e.preventDefault()
  }

  const onMouseUp = (e: MouseEvent) => {
    if (!isDriving()) return
    desktop.sendInput({ t: 'mb', b: xButton(e.button), d: 0 })
    e.preventDefault()
  }

  /* The remote desktop has its own right-click menu; the browser's on top of
   * it would be two menus for one gesture. Suppressed even when watching —
   * a context menu over somebody else's work invites "save video as…". */
  const onContextMenu = (e: Event) => e.preventDefault()

  const onWheel = (e: WheelEvent) => {
    if (!isDriving()) return
    e.preventDefault()
    wheelAccX += e.deltaX
    wheelAccY += e.deltaY
    const tx = Math.trunc(wheelAccX / WHEEL_QUANTUM)
    const ty = Math.trunc(wheelAccY / WHEEL_QUANTUM)
    if (tx || ty) {
      wheelAccX -= tx * WHEEL_QUANTUM
      wheelAccY -= ty * WHEEL_QUANTUM
      desktop.sendInput({ t: 'mw', dy: ty, dx: tx })
    }
  }

  const onKeyDown = (e: KeyboardEvent) => {
    if (!isDriving() || belongsToThePanel(e)) return
    /* The paste shortcut carries the clipboard AHEAD of itself. The focus
     * sync alone lost this race every time — the keystroke reached X before
     * the clipboard did — and on a fresh origin it never ran at all, because
     * Chrome only shows the clipboard-read prompt under user activation.
     * A keystroke IS activation: the first Ctrl+V asks, and every one after
     * pastes what was actually copied. The DataChannel is ordered, so
     * clip-then-key on the same channel cannot arrive reversed. */
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'v') {
      e.preventDefault()
      void desktop
        .pushClipboard()
        .then(() => desktop.sendInput({ t: 'kb', k: e.key, d: 1 }))
      return
    }
    desktop.sendInput({ t: 'kb', k: e.key, d: 1 })
    e.preventDefault()
  }

  const onKeyUp = (e: KeyboardEvent) => {
    if (!isDriving() || belongsToThePanel(e)) return
    desktop.sendInput({ t: 'kb', k: e.key, d: 0 })
    e.preventDefault()
  }

  /* Leaving the window with a key held would leave it held FOREVER on the
   * remote side — the keyup goes to whatever got the focus. The runtime has
   * a reset for exactly this. */
  const onBlur = () => {
    if (isDriving()) desktop.sendInput({ t: 'reset' })
  }

  video.addEventListener('mousemove', onMouseMove)
  video.addEventListener('mousedown', onMouseDown)
  video.addEventListener('mouseup', onMouseUp)
  video.addEventListener('contextmenu', onContextMenu)
  video.addEventListener('wheel', onWheel, { passive: false })
  window.addEventListener('keydown', onKeyDown)
  window.addEventListener('keyup', onKeyUp)
  window.addEventListener('blur', onBlur)

  return () => {
    video.removeEventListener('mousemove', onMouseMove)
    video.removeEventListener('mousedown', onMouseDown)
    video.removeEventListener('mouseup', onMouseUp)
    video.removeEventListener('contextmenu', onContextMenu)
    video.removeEventListener('wheel', onWheel)
    window.removeEventListener('keydown', onKeyDown)
    window.removeEventListener('keyup', onKeyUp)
    window.removeEventListener('blur', onBlur)
  }
}
