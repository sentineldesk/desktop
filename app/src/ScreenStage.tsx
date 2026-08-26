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

/* One <video>, mounted once, moved by CSS.
 *
 * The screen is drawn in three different places — full bleed in Desktop mode,
 * inside the agent's canvas card, and full bleed again when that card is
 * expanded. It used to be the SAME JSX rendered in three branches of the tree,
 * which meant React unmounted the element and mounted a new one on every
 * switch. `App.tsx` said so out loud: "The element travels between modes, so
 * it REMOUNTS."
 *
 * Remounting a media element is not free, and the part people notice is the
 * sound. Removing a <video> from the document pauses it — that is the HTML
 * spec, not a browser quirk — and putting the new one in starts it again, so
 * switching from Desktop to Agent while a video played on the remote machine
 * made an audible stop and start. It was reported as "se nota el play y stop",
 * and it is the same event that makes the picture blink.
 *
 * So the element stops travelling. It is mounted once, here, in a layer that
 * sits inside `.shell-main` and never leaves it. Each mode draws an empty
 * `<ScreenSlot/>` where the screen belongs, and the layer measures that slot
 * and positions itself over it. The WebRTC session, the srcObject, the input
 * handlers and the audio all carry on untouched — nothing has moved in the
 * tree, only a transform has changed.
 *
 * # Why the slot carries a z-index
 *
 * The layer is a sibling of the whole mode, not a child of it, so it paints in
 * `.shell-main`'s stacking context and has to be told where in the stack to
 * land. That is not one number, because the three slots sit under different
 * things:
 *
 *   1  Desktop mode. Only has to clear `#stage-desktop`'s own background.
 *      Every overlay in this mode — #status and #login at 20, #stats at 25,
 *      #dropzone at 35, #flash at 40, the ask dialogs at 45 — must stay above,
 *      and does.
 *   5  The agent's EXPANDED screen. Same, plus it must stay under the "back
 *      to the conversation" button, which is z-20 inside that view.
 *  31  The agent's canvas card. This one has to go ABOVE its container rather
 *      than under it: below 1000px the canvas <section> becomes an absolutely
 *      positioned overlay with z-30, which would otherwise cover the layer
 *      with its own `bg-card`. 31 clears it and still leaves #dropzone,
 *      #flash, #rec-badge and the ask dialogs on top.
 *
 * The layer is only ever as big as the slot, so a high z-index costs nothing:
 * it cannot cover a control it does not overlap. Every number above was read
 * off styles.css and the two components rather than guessed, and a slot that
 * asks for the wrong one shows up immediately as a screen that is invisible or
 * that swallows a button.
 */

import {
  createContext,
  useCallback,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'

interface Claim {
  el: HTMLDivElement
  z: number
  interactive: boolean
}

interface Stage {
  claim: (c: Claim) => void
  release: (el: HTMLDivElement) => void
}

const StageCtx = createContext<Stage | null>(null)

/** Where the screen goes in this mode. Draws nothing itself.
 *
 * It is an empty box whose only job is to have a position and a size the layer
 * can copy. Rendering it costs one div and it is what lets the mode keep
 * describing its own layout in its own JSX — the alternative was every mode
 * knowing the layer's coordinates, which is the same coupling written twice.
 */
export function ScreenSlot({
  z,
  interactive = true,
}: {
  /** Where in `.shell-main`'s stack the screen belongs here. See the file's
   * header — the three values in use are 1, 5 and 31, and each has a reason. */
  z: number
  /** False when this mode shows the screen but does not let anyone drive it —
   * the agent's canvas card, where the pointer belongs to the conversation. */
  interactive?: boolean
}) {
  const stage = useContext(StageCtx)
  const ref = useRef<HTMLDivElement | null>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el || !stage) return
    stage.claim({ el, z, interactive })
    return () => stage.release(el)
  }, [stage, z, interactive])

  return <div ref={ref} aria-hidden style={{ position: 'absolute', inset: 0 }} />
}

/** `.shell-main`, with the screen layer inside it.
 *
 * Owns the element rather than receiving it so that there is exactly one place
 * it can be mounted — the property this whole file exists to guarantee. `media`
 * is the video and anything that has to travel with it, drawn once.
 */
export function ScreenStage({
  media,
  children,
}: {
  media: ReactNode
  children: ReactNode
}) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const layerRef = useRef<HTMLDivElement | null>(null)
  const [claim, setClaim] = useState<Claim | null>(null)

  /* Release only clears a slot that is still the current one. React does not
   * promise the order of an unmount and the mount that replaces it, and a
   * release arriving after the new slot's claim would blank the screen for as
   * long as that mode lasted. */
  const stage = useRef<Stage>({
    claim: (c) => setClaim(c),
    release: (el) => setClaim((cur) => (cur && cur.el === el ? null : cur)),
  }).current

  const place = useCallback(() => {
    const layer = layerRef.current
    const host = hostRef.current
    if (!layer || !host) return
    if (!claim) {
      layer.style.display = 'none'
      return
    }
    const hr = host.getBoundingClientRect()
    const sr = claim.el.getBoundingClientRect()
    layer.style.display = ''
    layer.style.transform = `translate(${sr.left - hr.left}px, ${sr.top - hr.top}px)`
    layer.style.width = `${sr.width}px`
    layer.style.height = `${sr.height}px`
    layer.style.zIndex = String(claim.z)
    layer.style.pointerEvents = claim.interactive ? 'auto' : 'none'
    /* Inherited so a slot inside a rounded card does not show square corners
     * over it. Read from the slot rather than hard-coded, so a card that is
     * restyled does not leave this file behind. */
    layer.style.borderRadius = getComputedStyle(claim.el).borderRadius
  }, [claim])

  useLayoutEffect(() => {
    place()
    if (!claim) return
    const host = hostRef.current
    /* Two observers, because the slot can change size without the window
     * changing size — dragging the canvas divider is the everyday case. */
    const ro = new ResizeObserver(place)
    ro.observe(claim.el)
    if (host) ro.observe(host)
    window.addEventListener('resize', place)

    /* A short catch-up after every slot change. Layout is not always settled
     * in the frame the effect runs — a mode switch can leave a transition or a
     * scrollbar still resolving — and a layer measured one frame early sits a
     * few pixels off with nothing to correct it. Half a second of frames is
     * cheap and it ends; it is not a polling loop. */
    let raf = 0
    const until = performance.now() + 500
    const tick = () => {
      place()
      if (performance.now() < until) raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)

    return () => {
      ro.disconnect()
      window.removeEventListener('resize', place)
      cancelAnimationFrame(raf)
    }
  }, [claim, place])

  return (
    <StageCtx.Provider value={stage}>
      <div className="shell-main" ref={hostRef}>
        <div
          ref={layerRef}
          style={{ position: 'absolute', top: 0, left: 0, overflow: 'hidden' }}
        >
          {media}
        </div>
        {children}
      </div>
    </StageCtx.Provider>
  )
}
