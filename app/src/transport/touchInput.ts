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

/* Touch on the desktop, for tablets and phones.
 *
 * The model is direct manipulation, the way every touch remote-desktop
 * settles on it: one finger IS the pointer with the left button held — a tap
 * clicks, a drag drags — two fingers scroll, and a quick two-finger tap is
 * the right button. The browser's own gestures (pan, zoom, long-press menus)
 * are suppressed on the video, because two interpreters for one finger is
 * how a tap becomes a mystery.
 *
 * Coordinates go through the same letterbox-aware mapping the mouse uses:
 * a finger and a pointer must not disagree about where the desktop is. */

import type { Desktop } from './useDesktopStream'

const TWO_FINGER_TAP_MS = 250
const SCROLL_QUANTUM = 30

function coords(
  video: HTMLVideoElement,
  t: { clientX: number; clientY: number },
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
  const x = Math.round((t.clientX - ox) / scale)
  const y = Math.round((t.clientY - oy) / scale)
  if (x < 0 || y < 0 || x >= vw || y >= vh) return null
  return { x, y }
}

export function attachTouchInput(
  video: HTMLVideoElement,
  desktop: Pick<Desktop, 'sendInput'>,
  isDriving: () => boolean,
): () => void {
  let mode: 'none' | 'drag' | 'scroll' = 'none'
  let started = 0
  let moved = false
  let lastY = 0
  let scrollAcc = 0

  const onStart = (e: TouchEvent) => {
    if (!isDriving()) return
    e.preventDefault()
    started = performance.now()
    moved = false
    if (e.touches.length === 1) {
      const p = coords(video, e.touches[0])
      if (!p) return
      mode = 'drag'
      desktop.sendInput({ t: 'mm', x: p.x, y: p.y })
      desktop.sendInput({ t: 'mb', b: 1, d: 1 })
    } else if (e.touches.length === 2) {
      /* The second finger cancels the press: this became a scroll. */
      if (mode === 'drag') desktop.sendInput({ t: 'mb', b: 1, d: 0 })
      mode = 'scroll'
      lastY = (e.touches[0].clientY + e.touches[1].clientY) / 2
      scrollAcc = 0
    }
  }

  const onMove = (e: TouchEvent) => {
    if (!isDriving()) return
    e.preventDefault()
    moved = true
    if (mode === 'drag' && e.touches.length === 1) {
      const p = coords(video, e.touches[0])
      if (p) desktop.sendInput({ t: 'mm', x: p.x, y: p.y })
    } else if (mode === 'scroll' && e.touches.length === 2) {
      const y = (e.touches[0].clientY + e.touches[1].clientY) / 2
      scrollAcc += lastY - y
      lastY = y
      const ticks = Math.trunc(scrollAcc / SCROLL_QUANTUM)
      if (ticks) {
        scrollAcc -= ticks * SCROLL_QUANTUM
        desktop.sendInput({ t: 'mw', dy: ticks, dx: 0 })
      }
    }
  }

  const onEnd = (e: TouchEvent) => {
    if (!isDriving()) return
    e.preventDefault()
    if (e.touches.length > 0) return // fingers remain; the gesture goes on
    if (mode === 'drag') {
      desktop.sendInput({ t: 'mb', b: 1, d: 0 })
    } else if (
      mode === 'scroll' &&
      !moved &&
      performance.now() - started < TWO_FINGER_TAP_MS
    ) {
      /* A quick two-finger tap that never scrolled: the right button. */
      desktop.sendInput({ t: 'mb', b: 3, d: 1 })
      desktop.sendInput({ t: 'mb', b: 3, d: 0 })
    }
    mode = 'none'
  }

  video.addEventListener('touchstart', onStart, { passive: false })
  video.addEventListener('touchmove', onMove, { passive: false })
  video.addEventListener('touchend', onEnd, { passive: false })
  video.addEventListener('touchcancel', onEnd, { passive: false })
  return () => {
    video.removeEventListener('touchstart', onStart)
    video.removeEventListener('touchmove', onMove)
    video.removeEventListener('touchend', onEnd)
    video.removeEventListener('touchcancel', onEnd)
  }
}
