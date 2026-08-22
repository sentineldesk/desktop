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

/* Which microphone, camera and speaker this PERSON uses, remembered.
 *
 * localStorage, not the session: a device choice is about the machine and
 * its owner ("the USB mic, not the laptop's"), it outlives any one room,
 * and it is the same choice whether they arrived as an administrator or as
 * a guest. The join preflight writes here, the in-room settings dialog
 * writes here, and the call reads here — one place, so choosing a mic at
 * the door IS choosing it for the call.
 *
 * A stored id can go stale — the headset is unplugged tomorrow — so every
 * consumer treats it as a preference, not a promise: ask for it exactly,
 * and fall back to the default when the device is gone rather than failing
 * a call over yesterday's hardware.
 */

const KEYS = {
  cam: 'sentineldesk.media.cam',
  mic: 'sentineldesk.media.mic',
  /* The Voice conference's input, remembered apart from the mic on purpose:
   * "Microphone" feeds the DESKTOP (apps and recordings hear it) and "Voice"
   * feeds the people watching, and somebody with a headset and a desk mic
   * genuinely wants them on different devices. */
  voice: 'sentineldesk.media.voice',
  speaker: 'sentineldesk.media.speaker',
} as const

export type MediaKind = keyof typeof KEYS

function storage(): Storage | null {
  try {
    return window.localStorage
  } catch {
    return null // Safari private mode throws; no memory is a valid machine
  }
}

export function preferredDevice(kind: MediaKind): string {
  return storage()?.getItem(KEYS[kind]) ?? ''
}

/* Background blur, remembered beside the devices for the same reason they
 * are: preferring a blurred background is about this person and their room,
 * not about any one call. The preference is honoured only when the SFU
 * carries the call — the processor hooks LiveKit's track pipeline — so a
 * mesh call reads it and ignores it, and the Devices dialog says why. */
const BLUR_KEY = 'sentineldesk.media.blur'

export function blurWanted(): boolean {
  return storage()?.getItem(BLUR_KEY) === '1'
}

export function rememberBlur(on: boolean): void {
  const s = storage()
  if (!s) return
  if (on) s.setItem(BLUR_KEY, '1')
  else s.removeItem(BLUR_KEY)
}

export function rememberDevice(kind: MediaKind, id: string): void {
  const s = storage()
  if (!s) return
  if (id) s.setItem(KEYS[kind], id)
  else s.removeItem(KEYS[kind])
}

/* Constraints for getUserMedia honouring the preference. `exact` on
 * purpose: `ideal` lets the browser quietly substitute a device, and a
 * person who chose the USB mic and got the laptop's would rightly call
 * that a lie. The caller retries WITHOUT the preference when the device is
 * gone — an explicit fallback, visible in code, not a silent one. */
export function audioConstraints(): MediaTrackConstraints | boolean {
  const id = preferredDevice('mic')
  return id ? { deviceId: { exact: id } } : true
}

export function voiceConstraints(): MediaTrackConstraints | boolean {
  const id = preferredDevice('voice')
  return id ? { deviceId: { exact: id } } : true
}

export function videoConstraints(): MediaTrackConstraints {
  const id = preferredDevice('cam')
  const base: MediaTrackConstraints = {
    width: { ideal: 640 },
    height: { ideal: 360 },
  }
  return id ? { ...base, deviceId: { exact: id } } : base
}

/* Point an <audio>/<video> element at the chosen speaker, where the
 * browser allows it. Firefox has no setSinkId; failing quietly is right —
 * the sound still plays, on the default output. */
export function applySpeaker(el: HTMLMediaElement): void {
  const id = preferredDevice('speaker')
  const sinkable = el as HTMLMediaElement & {
    setSinkId?: (id: string) => Promise<void>
  }
  if (id && typeof sinkable.setSinkId === 'function') {
    void sinkable.setSinkId(id).catch(() => undefined)
  }
}

/* Play the desktop's stream, surviving the browser's autoplay policy.
 *
 * Chrome refuses to start a video WITH sound on a page the user has not
 * touched yet — which is exactly what a reload of the room is. The refusal
 * is silent: `autoplay` simply never fires, and the desktop sits there as a
 * black rectangle until something else happens to nudge the element. That
 * black screen was reported as a streaming bug; it was this.
 *
 * So: try to play as configured. If the browser says no, play muted — the
 * picture matters more than the sound — and lift the mute at the first real
 * gesture, which is the moment the browser starts trusting the page.
 * `soundWanted` is read at gesture time, not captured, so a speaker switch
 * flipped while waiting is honoured.
 *
 * `onPolicy` reports when the POLICY holds the sound (true on the muted
 * fallback, false once the gesture lifts it), so the UI can show the speaker
 * as off while it factually is — the state is the browser's doing, and the
 * icon claiming sound while none plays reads as broken audio.
 *
 * Returns a cleanup for the gesture listeners, effect-shaped. */
export function playOrRescue(
  el: HTMLVideoElement,
  soundWanted: () => boolean,
  onPolicy?: (muted: boolean) => void,
): () => void {
  let disposed = false
  const detach = () => {
    window.removeEventListener('pointerdown', restore, true)
    window.removeEventListener('keydown', restore, true)
  }
  const restore = () => {
    if (!disposed && soundWanted()) {
      el.muted = false
      onPolicy?.(false)
    }
    detach()
  }
  const rescue = () => {
    if (disposed) return
    el.muted = true
    onPolicy?.(true)
    void el.play().catch(() => undefined)
    window.addEventListener('pointerdown', restore, true)
    window.addEventListener('keydown', restore, true)
  }
  /* Nothing in this client ever pauses the element on purpose — the room's
   * pause stops the PIPELINE, server-side. So a pause event here is always
   * the browser's own economising (muted video in an occluded window, a
   * tablet coming back from its lock screen), and the answer is to play on:
   * a desktop that silently freezes while the room keeps moving was reported
   * as a broken stream, because that is exactly what it looks like. */
  const onPause = () => {
    if (!disposed) void el.play().catch(rescue)
  }
  el.addEventListener('pause', onPause)
  void el.play().catch(rescue)
  return () => {
    disposed = true
    el.removeEventListener('pause', onPause)
    detach()
  }
}
