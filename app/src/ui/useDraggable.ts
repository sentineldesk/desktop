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

/* A floating panel you can pick up by its header.
 *
 * The panel keeps whatever anchoring its stylesheet gave it and moves as a
 * transform on top, so nothing has to be converted to left/top coordinates
 * and the resting place still follows the layout when the window resizes.
 * The drag is clamped so the panel can never leave the viewport — a panel
 * whose handle is off screen is one nobody can bring back. Double-click on
 * the handle snaps it home, the same escape hatch the chat divider had.
 *
 * Pointer capture keeps the drag alive when the cursor crosses the desktop
 * video, whose own handlers would otherwise swallow the move mid-drag. */

import { useCallback, useEffect, useRef, useState } from 'react'

type Offset = { x: number; y: number }

const NONE: Offset = { x: 0, y: 0 }

export function useDraggable(persistKey?: string): {
  /** Put this on the element that moves. */
  ref: React.RefObject<HTMLDivElement | null>
  /** Spread into the moving element's style. */
  style: React.CSSProperties
  /** Put this on the handle — the title strip, not the whole panel. */
  onGrab(e: React.PointerEvent): void
  /** Double-click the handle: back to where the stylesheet put it. */
  onHome(): void
} {
  const ref = useRef<HTMLDivElement | null>(null)
  const [off, setOff] = useState<Offset>(() => {
    if (!persistKey) return NONE
    try {
      const saved = JSON.parse(localStorage.getItem(persistKey) || 'null') as
        | Offset
        | null
      if (saved && isFinite(saved.x) && isFinite(saved.y)) return saved
    } catch {
      /* an unreadable saved spot is just the default spot */
    }
    return NONE
  })
  const offRef = useRef(off)
  offRef.current = off

  /* A remembered spot from a bigger window could be entirely off screen on
   * this one; pull it back once the panel has a size to measure. */
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    const fixed = clamp(offRef.current, r, offRef.current)
    if (fixed.x !== offRef.current.x || fixed.y !== offRef.current.y) {
      setOff(fixed)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const persist = useCallback(
    (v: Offset) => {
      if (!persistKey) return
      try {
        localStorage.setItem(persistKey, JSON.stringify(v))
      } catch {
        /* storage off: still drags, just forgets */
      }
    },
    [persistKey],
  )

  const onGrab = useCallback(
    (e: React.PointerEvent) => {
      /* The handle carries live controls (close, keys). A press on one is a
       * click, not a drag. */
      if ((e.target as HTMLElement).closest('button, input, select, a')) return
      const el = ref.current
      if (!el) return
      e.preventDefault()
      const handle = e.currentTarget as HTMLElement
      handle.setPointerCapture(e.pointerId)
      const startX = e.clientX
      const startY = e.clientY
      const start = offRef.current
      const rect = el.getBoundingClientRect()

      const move = (ev: PointerEvent) => {
        const next = clamp(
          { x: start.x + ev.clientX - startX, y: start.y + ev.clientY - startY },
          rect,
          start,
        )
        setOff(next)
      }
      const up = () => {
        handle.removeEventListener('pointermove', move)
        handle.removeEventListener('pointerup', up)
        handle.removeEventListener('pointercancel', up)
        persist(offRef.current)
      }
      handle.addEventListener('pointermove', move)
      handle.addEventListener('pointerup', up)
      handle.addEventListener('pointercancel', up)
    },
    [persist],
  )

  const onHome = useCallback(() => {
    setOff(NONE)
    persist(NONE)
  }, [persist])

  return {
    ref,
    style: off.x || off.y ? { transform: `translate(${off.x}px, ${off.y}px)` } : {},
    onGrab,
    onHome,
  }
}

/* Keep the panel inside the viewport. `rect` was measured AT the given
 * offset, so the untranslated box is rect minus that offset — clamp against
 * it and the panel's edge stops at the window's, whatever the anchor. */
function clamp(next: Offset, rect: DOMRect, at: Offset): Offset {
  const baseL = rect.left - at.x
  const baseT = rect.top - at.y
  const pad = 4
  return {
    x: Math.min(
      Math.max(next.x, pad - baseL),
      window.innerWidth - rect.width - baseL - pad,
    ),
    y: Math.min(
      Math.max(next.y, pad - baseT),
      window.innerHeight - rect.height - baseT - pad,
    ),
  }
}
