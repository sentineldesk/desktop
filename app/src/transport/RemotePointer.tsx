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

/* The driver's cursor, for everyone who is not driving.
 *
 * The live video deliberately carries no pointer — the driver draws their own
 * locally at zero latency — so without this a viewer watched a desktop where
 * things clicked themselves. The runtime broadcasts the real X position at
 * ~30Hz; this maps it from desktop pixels to screen pixels through the
 * video's object-fit:contain geometry and draws it, named, in the runtime's
 * own peer-pointer palette (violet is reserved for the agent, because a
 * pointer moving on its own reads as a glitch until you know a model is
 * driving).
 *
 * Shared by the admin room view and the guest room view — it lived only in
 * the first for a while, which meant a collaborator never saw the admin's
 * cursor and reported the desktop as clicking itself. Same component, same
 * gating, both doors.
 *
 * Measured per render on purpose: positions arrive only while the cursor
 * moves, and caching the rect would go stale on every panel resize for the
 * price of one getBoundingClientRect. */

import styles from './RemotePointer.module.css'

export function RemotePointer({
  videoRef,
  pointer,
  name,
  agent,
  color,
}: {
  videoRef: React.RefObject<HTMLVideoElement | null>
  pointer: { x: number; y: number }
  name: string
  agent: boolean
  /** The member's ink from presence; undefined falls back to the palette's
   * yellow, and the agent's violet always wins. */
  color?: string
}) {
  const el = videoRef.current
  if (!el || !el.videoWidth || !el.parentElement) return null
  const rect = el.getBoundingClientRect()
  const parent = el.parentElement.getBoundingClientRect()
  const scale = Math.min(
    rect.width / el.videoWidth,
    rect.height / el.videoHeight,
  )
  const left =
    rect.left - parent.left + (rect.width - el.videoWidth * scale) / 2 +
    pointer.x * scale
  const top =
    rect.top - parent.top + (rect.height - el.videoHeight * scale) / 2 +
    pointer.y * scale
  /* The member's own ink, dealt by the room (yellow, cyan, magenta, key) —
   * violet stays reserved for the agent, a colour that must never mean a
   * human. The tag's text flips light on the dark inks, or the fourth
   * person's name would be black on black. */
  const colour = agent ? '#b08cf7' : (color ?? '#f9c74f')
  const n = parseInt(colour.slice(1), 16)
  const lum = (((n >> 16) & 0xff) * 299 + ((n >> 8) & 0xff) * 587 + (n & 0xff) * 114) / 1000
  const ink = lum < 128 ? '#e7edea' : '#1b1b16'
  return (
    <div
      className={styles.remotePointer}
      style={{ left, top }}
      aria-hidden="true"
    >
      <svg width="15" height="19" viewBox="0 0 15 19">
        <path
          d="M1 1 L1 15 L5 12 L8 18 L10.5 16.8 L7.6 11 L12 10 Z"
          fill={colour}
          stroke="#1b1b16"
          strokeWidth="1.2"
        />
      </svg>
      <span
        className={styles.remotePointerTag}
        style={{ background: colour, color: ink }}
      >
        {name}
      </span>
    </div>
  )
}
