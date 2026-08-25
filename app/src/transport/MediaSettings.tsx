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

/* The audio check, ported from the workroom's device dialog and narrowed to
 * what this desktop actually has: no camera. Two inputs instead of one,
 * because they are different acts — "Microphone" feeds the DESKTOP (the apps
 * and recordings in the room hear it) and "Voice" feeds the conference among
 * the people watching — and somebody with a headset and a desk mic genuinely
 * wants them on different devices.
 *
 * The meter and the self-listen loop follow whichever input was touched
 * last, so picking a device immediately answers the only question that
 * matters here: does THIS one hear me. Choices land in the same remembered
 * preferences the live paths read (mediaPrefs), and closing re-acquires any
 * media that is currently live, so a new device takes effect mid-session. */

import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { preferredDevice, rememberDevice, type MediaKind } from './mediaPrefs'
import { MatrixDie, randomMatrixName } from '../ui/NameGate'
import styles from './MediaSettings.module.css'

/* Peak amplitude is divided by this to reach 0…1 — conversational speech
 * should move the bars visibly (same scale as the workroom's preflight). */
const LEVEL_SCALE = 60

const TONE_MS = 900
const TONE_LOW = 587.33
const TONE_HIGH = 880

type Input = Extract<MediaKind, 'mic' | 'voice'>

/* The nickname's charset, mirrored on the server (sanitizeNickname): the
 * name lands in the roster, the witness log and a pointer tag drawn into X,
 * so it stays to letters, digits, space, dash and underscore. */
const NAME_OK = /[^A-Za-z0-9 _-]/g
const NAME_KEY = 'sentineldesk.name'

export function storedName(): string {
  try {
    return window.localStorage.getItem(NAME_KEY) ?? ''
  } catch {
    return ''
  }
}

export function MediaSettings({
  onClose,
  onChanged,
}: {
  onClose(): void
  /** Called on close with what changed — device kinds re-acquire live media,
   * 'name' is announced to the room. */
  onChanged(changed: readonly (Input | 'name')[]): void
}) {
  const { t } = useTranslation()
  const [devices, setDevices] = useState<readonly MediaDeviceInfo[]>([])
  const [denied, setDenied] = useState(false)
  const [level, setLevel] = useState(0)
  const [micId, setMicId] = useState(() => preferredDevice('mic'))
  const [voiceId, setVoiceId] = useState(() => preferredDevice('voice'))
  const [speakerId, setSpeakerId] = useState(() => preferredDevice('speaker'))
  const [name, setName] = useState(storedName)
  const [testing, setTesting] = useState<Input>('mic')
  const [listening, setListening] = useState(false)
  const [speakerPlaying, setSpeakerPlaying] = useState(false)

  const stream = useRef<MediaStream | null>(null)
  const audioCtx = useRef<AudioContext | null>(null)
  const frame = useRef<number | undefined>(undefined)
  const monitor = useRef<HTMLAudioElement | null>(null)
  const toneTimer = useRef<number | undefined>(undefined)
  /* What was chosen when the dialog opened — close() reports the delta. */
  const opened = useRef({
    mic: preferredDevice('mic'),
    voice: preferredDevice('voice'),
    name: storedName(),
  })

  const release = useCallback(() => {
    stream.current?.getTracks().forEach((track) => track.stop())
    stream.current = null
    if (frame.current !== undefined) cancelAnimationFrame(frame.current)
    frame.current = undefined
    void audioCtx.current?.close().catch(() => {})
    audioCtx.current = null
    if (monitor.current) {
      monitor.current.srcObject = null
      monitor.current = null
    }
    setLevel(0)
  }, [])

  /* Acquire the input under test and hang the meter (and, when asked, the
   * self-listen loop) on it. One capture at a time: the dialog is a test
   * bench, not a mixer. */
  const acquire = useCallback(
    (deviceId: string, listen: boolean, sinkId: string) => {
      release()
      void (async () => {
        try {
          let media: MediaStream
          try {
            media = await navigator.mediaDevices.getUserMedia({
              audio: deviceId ? { deviceId: { exact: deviceId } } : true,
            })
          } catch (err) {
            if ((err as DOMException)?.name !== 'OverconstrainedError') throw err
            media = await navigator.mediaDevices.getUserMedia({ audio: true })
          }
          stream.current = media
          setDenied(false)

          /* Labels only exist after permission is granted. */
          try {
            setDevices(await navigator.mediaDevices.enumerateDevices())
          } catch {
            /* enumeration is a lost feature, not a lost dialog */
          }

          const Ctor =
            window.AudioContext ??
            (window as unknown as { webkitAudioContext?: typeof AudioContext })
              .webkitAudioContext
          if (Ctor) {
            const context = new Ctor()
            audioCtx.current = context
            const analyser = context.createAnalyser()
            analyser.fftSize = 512
            context.createMediaStreamSource(media).connect(analyser)
            const buffer = new Uint8Array(analyser.frequencyBinCount)
            const tick = () => {
              analyser.getByteTimeDomainData(buffer)
              let peak = 0
              for (const sample of buffer) {
                peak = Math.max(peak, Math.abs(sample - 128))
              }
              setLevel(Math.min(1, peak / LEVEL_SCALE))
              frame.current = requestAnimationFrame(tick)
            }
            tick()
          }

          if (listen) {
            /* Hearing yourself is the proof the person asked for; the echo
             * hint under the button is the price on speakers. */
            const el = new Audio()
            el.srcObject = media
            const sinkable = el as HTMLAudioElement & {
              setSinkId?: (id: string) => Promise<void>
            }
            if (sinkId && sinkable.setSinkId) {
              void sinkable.setSinkId(sinkId).catch(() => undefined)
            }
            void el.play().catch(() => undefined)
            monitor.current = el
          }
        } catch {
          setDenied(true)
          setLevel(0)
        }
      })()
    },
    [release],
  )

  useEffect(() => {
    acquire(preferredDevice('mic'), false, preferredDevice('speaker'))
    return () => {
      release()
      window.clearTimeout(toneTimer.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const pick = (kind: Input, id: string) => {
    rememberDevice(kind, id)
    if (kind === 'mic') setMicId(id)
    else setVoiceId(id)
    setTesting(kind)
    acquire(id, listening, speakerId)
  }

  const pickSpeaker = (id: string) => {
    rememberDevice('speaker', id)
    setSpeakerId(id)
    const sinkable = monitor.current as
      | (HTMLAudioElement & { setSinkId?: (id: string) => Promise<void> })
      | null
    if (sinkable?.setSinkId && id) void sinkable.setSinkId(id).catch(() => undefined)
  }

  const toggleListen = () => {
    const next = !listening
    setListening(next)
    acquire(testing === 'mic' ? micId : voiceId, next, speakerId)
  }

  /* The two-note chime on the chosen OUTPUT — same recipe as the workroom
   * preflight, because a bundled mp3 on the default device proves nothing. */
  const testSpeaker = () => {
    try {
      const Ctor =
        window.AudioContext ??
        (window as unknown as { webkitAudioContext?: typeof AudioContext })
          .webkitAudioContext
      if (!Ctor) return
      const context = new Ctor()
      const destination = context.createMediaStreamDestination()
      const osc = context.createOscillator()
      const gain = context.createGain()
      osc.type = 'sine'
      osc.frequency.setValueAtTime(TONE_LOW, context.currentTime)
      osc.frequency.setValueAtTime(TONE_HIGH, context.currentTime + 0.22)
      gain.gain.setValueAtTime(0, context.currentTime)
      gain.gain.linearRampToValueAtTime(0.22, context.currentTime + 0.04)
      gain.gain.setValueAtTime(0.22, context.currentTime + 0.38)
      gain.gain.linearRampToValueAtTime(0, context.currentTime + 0.5)
      osc.connect(gain)
      gain.connect(destination)
      gain.connect(context.destination)
      const element = new Audio()
      element.srcObject = destination.stream
      const sink = element as HTMLAudioElement & {
        setSinkId?: (id: string) => Promise<void>
      }
      if (speakerId && sink.setSinkId) {
        void sink
          .setSinkId(speakerId)
          .then(() => gain.disconnect(context.destination))
          .catch(() => {
            /* no sink selection in this browser: default output, still a test */
          })
      }
      void element.play().catch(() => {})
      osc.start()
      osc.stop(context.currentTime + 0.52)
      setSpeakerPlaying(true)
      window.clearTimeout(toneTimer.current)
      toneTimer.current = window.setTimeout(() => {
        setSpeakerPlaying(false)
        void context.close().catch(() => {})
      }, TONE_MS)
    } catch {
      setSpeakerPlaying(false)
    }
  }

  const close = () => {
    const changed: (Input | 'name')[] = []
    if (preferredDevice('mic') !== opened.current.mic) changed.push('mic')
    if (preferredDevice('voice') !== opened.current.voice) changed.push('voice')
    const trimmed = name.trim()
    if (trimmed !== opened.current.name) {
      try {
        window.localStorage.setItem(NAME_KEY, trimmed)
      } catch {
        /* no storage is a valid machine; the rename still goes out */
      }
      changed.push('name')
    }
    onChanged(changed)
    onClose()
  }

  const inputsOf = (fallbackKey: string) => {
    const found = devices.filter((d) => d.kind === 'audioinput')
    return found.length > 0
      ? found.map((d, i) => ({
          id: d.deviceId,
          label: d.label || `${t(fallbackKey)} ${i + 1}`,
        }))
      : [{ id: '', label: t(fallbackKey) }]
  }
  const outputs = () => {
    const found = devices.filter((d) => d.kind === 'audiooutput')
    return found.length > 0
      ? found.map((d, i) => ({
          id: d.deviceId,
          label: d.label || `${t('join.speakerDefault')} ${i + 1}`,
        }))
      : [{ id: '', label: t('join.speakerDefault') }]
  }

  /* Closes only by its ✕ / Done, like the Files and Stream dialogs. */
  return (
    <div className={styles.overlay}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t('settings.title')}
        className={styles.dialog}
      >
        <div className={styles.head}>
          <span className={styles.title}>{t('settings.title')}</span>
          <span className={styles.spacer} />
          <button
            type="button"
            className={styles.close}
            aria-label={t('a11y.close')}
            onClick={close}
          >
            ✕
          </button>
        </div>

        {!navigator.mediaDevices && (
          <div className={styles.insecure} role="alert">
            {t('call.insecureOrigin')}
          </div>
        )}
        {denied && navigator.mediaDevices ? (
          <div className={styles.insecure} role="alert">
            {t('settings.denied')}
          </div>
        ) : null}

        <div className={styles.body}>
          <div className={styles.fields}>
            <label className={styles.field}>
              <span className={styles.label}>{t('settings.name')}</span>
              {/* The gate's die, here too: same list, same gesture. */}
              <div style={{ position: 'relative' }}>
                <input
                  className={styles.select}
                  value={name}
                  maxLength={48}
                  placeholder={t('settings.namePlaceholder')}
                  onChange={(e) => setName(e.target.value.replace(NAME_OK, ''))}
                  style={{ width: '100%', paddingRight: 40, boxSizing: 'border-box' }}
                />
                <MatrixDie
                  title={t('name.shuffle')}
                  onClick={() => setName(randomMatrixName(name))}
                />
              </div>
              <span className={styles.hint}>{t('settings.nameHint')}</span>
            </label>

            <label className={styles.field}>
              <span className={styles.label}>{t('settings.mic')}</span>
              <select
                className={styles.select}
                value={micId}
                onChange={(e) => pick('mic', e.target.value)}
              >
                {inputsOf('join.micDevice').map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.label}
                  </option>
                ))}
              </select>
              <span className={styles.hint}>{t('settings.micHint')}</span>
            </label>

            <label className={styles.field}>
              <span className={styles.label}>{t('settings.voice')}</span>
              <select
                className={styles.select}
                value={voiceId}
                onChange={(e) => pick('voice', e.target.value)}
              >
                {inputsOf('join.micDevice').map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.label}
                  </option>
                ))}
              </select>
              <span className={styles.hint}>{t('settings.voiceHint')}</span>
            </label>

            <label className={styles.field}>
              <span className={styles.label}>{t('join.speaker')}</span>
              <select
                className={styles.select}
                value={speakerId}
                onChange={(e) => pickSpeaker(e.target.value)}
              >
                {outputs().map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.label}
                  </option>
                ))}
              </select>
            </label>

            {/* The meter: proof the input under test HEARS. It follows the
                last-touched picker, and the line under it says which. */}
            <div className={styles.levelRow}>
              <span className={styles.label}>{t('join.level')}</span>
              <span className={styles.bars} aria-hidden="true">
                {Array.from({ length: 14 }, (_, i) => (
                  <span
                    key={i}
                    className={`${styles.bar} ${level * 14 > i ? styles.barOn : ''}`}
                  />
                ))}
              </span>
            </div>
            <span className={styles.hint}>
              {t('settings.testingNow', {
                which: t(testing === 'mic' ? 'settings.mic' : 'settings.voice'),
              })}
            </span>

            <div className={styles.testRow}>
              <button
                type="button"
                className={`${styles.test} ${listening ? styles.testOn : ''}`}
                onClick={toggleListen}
              >
                {t(listening ? 'settings.listening' : 'settings.listen')}
              </button>
              <button type="button" className={styles.test} onClick={testSpeaker}>
                {t(speakerPlaying ? 'join.playing' : 'join.testSpeaker')}
              </button>
            </div>
            <span className={styles.hint}>{t('settings.echoHint')}</span>
          </div>
        </div>

        <div className={styles.foot}>
          <button type="button" className={styles.done} onClick={close}>
            {t('media.done')}
          </button>
        </div>
      </div>
    </div>
  )
}
