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

/* The restream panel, as the embedded client drew it — literally: the logos,
 * the relabelled field, the per-destination hints and the remembered keys are
 * transcribed from internal/webui/assets (index.html + client.js), which has
 * been the shape of this dialog since before the panel existed. A first
 * rewrite reduced it to four text chips and lost the part people actually
 * used: seeing at a glance which platform is which, and reopening to find
 * the key still there.
 *
 * Pick a platform and give it the KEY (the ingest address is the platform's
 * own, published and stable, and typing it is a chance to mistype it), or
 * pick VLC/OBS and give the whole address. The broadcast runs its own capture
 * on the server — pointer included, at its own bitrate — so going live never
 * touches what the room is watching, and this dialog asks nothing about
 * quality because the server already chose it.
 */

import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import type { Desktop } from './useDesktopStream'
import styles from './MediaSettings.module.css'
import own from './StreamDialog.module.css'

/* The platforms' published ingest addresses. Stable for years; a key is
 * pasted, a custom target is typed whole. */
const PLATFORMS = {
  youtube: { ingest: 'rtmp://a.rtmp.youtube.com/live2/', key: true },
  twitch: { ingest: 'rtmp://live.twitch.tv/app/', key: true },
  facebook: { ingest: 'rtmps://live-api-s.facebook.com:443/rtmp/', key: true },
  custom: { ingest: '', key: false },
} as const

type Platform = keyof typeof PLATFORMS

/* Straight to the section that explains how to point VLC or OBS at the room,
 * in the guide — the same link the embedded client offers. */
const HELP_URL =
  'https://sentineldesk.github.io/desktop/docs/guide/index.html#capture-stream-out'

/* What was typed last time, per destination.
 *
 * The server cannot help here: it redacts the stream key before anything
 * leaves the machine, so reopening the dialog mid-stream would otherwise show
 * dots instead of what was entered — and stopping and restarting would mean
 * pasting the key again. A platform key IS a broadcast credential, so it is
 * kept the way a browser keeps a password: on this machine, for this origin,
 * and nowhere else. Same store name as the embedded client, deliberately —
 * it is the same fact about the same person. */
const STORE = 'sentineldesk_restream'

function loadAddresses(): Record<string, string> {
  try {
    return (JSON.parse(localStorage.getItem(STORE) ?? '{}') as Record<string, string>) ?? {}
  } catch {
    return {}
  }
}

function rememberAddress(platform: string, value: string) {
  try {
    const all = loadAddresses()
    if (value) all[platform] = value
    else delete all[platform]
    localStorage.setItem(STORE, JSON.stringify(all))
  } catch {
    /* private mode: the field simply starts empty next time */
  }
}

/* The logos, as the embedded client draws them (index.html). The filled marks
 * carry their own fill; the VLC/OBS monitor is stroked by the base CSS. The
 * cutout in the YouTube play button takes the dialog's surface colour. */
const LOGOS: Record<Platform, ReactNode> = {
  youtube: (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path
        fill="currentColor"
        stroke="none"
        d="M22.5 7.2a3 3 0 0 0-2.1-2.1C18.6 4.6 12 4.6 12 4.6s-6.6 0-8.4.5A3 3 0 0 0 1.5 7.2 31 31 0 0 0 1 12a31 31 0 0 0 .5 4.8 3 3 0 0 0 2.1 2.1c1.8.5 8.4.5 8.4.5s6.6 0 8.4-.5a3 3 0 0 0 2.1-2.1A31 31 0 0 0 23 12a31 31 0 0 0-.5-4.8z"
      />
      <path className={own.cut} d="M9.9 15.2l5.5-3.2-5.5-3.2z" />
    </svg>
  ),
  twitch: (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path
        fill="currentColor"
        stroke="none"
        d="M4.3 2L2.5 6.4v13.1h4.8V22h2.6l2.6-2.5h3.9l5-4.9V2zm15.1 12L16.6 17H12l-2.5 2.4V17H5.6V3.6h13.8zM15.6 7.2v5h-1.9v-5zm-4.6 0v5H9.2v-5z"
      />
    </svg>
  ),
  facebook: (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path
        fill="currentColor"
        stroke="none"
        d="M22 12a10 10 0 1 0-11.6 9.9v-7H7.9V12h2.5V9.8c0-2.5 1.5-3.9 3.8-3.9 1.1 0 2.2.2 2.2.2v2.5h-1.3c-1.2 0-1.6.8-1.6 1.6V12h2.8l-.4 2.9h-2.4v7A10 10 0 0 0 22 12z"
      />
    </svg>
  ),
  custom: (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <rect x="2.5" y="4.5" width="19" height="13" rx="2" />
      <path d="M8 20.5h8M12 17.5v3" />
      <path fill="currentColor" stroke="none" d="M10 8.6l4.5 2.4L10 13.4z" />
    </svg>
  ),
}

export function StreamDialog({ desktop, onClose }: { desktop: Desktop; onClose(): void }) {
  const { t } = useTranslation()
  /* The standalone door: the same wire the panel speaks, sent directly. */
  const state = {
    restreamError: desktop.restreamError,
    restreamAble: desktop.restreamAble,
    streaming: desktop.restreaming,
    restreams: desktop.restreams,
  }
  const actions = {
    startStream: (platform: string, url: string, audio: boolean, kf: boolean) => {
      desktop.clearRestreamError()
      desktop.sendInput({
        t: 'restream',
        rs: { action: 'start', platform, url, audio, kf },
      })
    },
    stopStream: (id?: string) => {
      const targets = id ? [{ id }] : desktop.restreams
      for (const r of targets) {
        desktop.sendInput({ t: 'restream', rs: { action: 'stop', id: r.id } })
      }
    },
  }

  const [platform, setPlatform] = useState<Platform>('youtube')
  const [typed, setTyped] = useState(() => loadAddresses()['youtube'] ?? '')
  const [audio, setAudio] = useState(true)
  const [kf, setKf] = useState(false)
  const [waiting, setWaiting] = useState(false)

  const spec = PLATFORMS[platform]

  /* The attempt's verdict, where the person who pressed the button is
   * looking. The dialog used to close itself on Transmitir, so a refused
   * start — a typo'd scheme, a missing key — vanished: the server answered
   * with a perfectly clear sentence and nobody saw it, and the person went
   * looking for a broken machine instead. The server's own words are shown
   * verbatim; only the control token is translated. */
  /* The verdict, where the person is looking. `needControl` still exists as
   * an answer — a GUEST asking to broadcast without the seat gets it — but
   * an administrator's ticket carries the role and the runtime lets the
   * broadcast through seatless, so this dialog no longer second-guesses who
   * may click: it sends, and shows whatever the authority answered. */
  const err = state.restreamError
  const status =
    state.restreamAble === false
      ? t('rs.unavailable')
      : err
        ? err === 'needControl'
          ? t('wr.needControl')
          : err
        : waiting
          ? state.streaming
            ? t('rs.live')
            : t('rs.connecting')
          : null

  const pick = (name: Platform) => {
    setPlatform(name)
    setTyped(loadAddresses()[name] ?? '')
  }

  const go = () => {
    const value = typed.trim()
    if (!value) return
    /* A platform gets its published ingest plus the key; custom is taken
     * exactly as typed — it is somebody's own address and we do not know
     * better than they do. Remembered as typed, not as sent: what comes back
     * into the field next time has to be the key, never the key with the
     * ingest address glued in front of it. */
    const url = spec.key ? spec.ingest + value : value
    rememberAddress(platform, value)
    actions.startStream(platform, url, audio, spec.key ? true : kf)
    /* Stay open. The dialog is where the answer lands — connecting, live,
     * or the server's refusal — and closing before it arrives is how a
     * refusal becomes a mystery. The person closes it when satisfied. */
    setWaiting(true)
  }

  /* The dialog closes ONLY by its ✕. A stream key is pasted into this form,
   * and a click that misses the pane — or an Escape meant for something else
   * — must not throw the form away; losing a half-filled ingest URL to a
   * stray click is how the utp:// typo era felt, from the other side. The
   * overlay is its own (own.overlay): transparent, no dim and no blur, so
   * the desktop stays fully visible behind the window. */
  return (
    <div className={own.overlay}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t('rs.title')}
        className={styles.dialog}
      >
        <div className={styles.head}>
          <span className={styles.title}>
            {t('rs.title')}
            <span className={own.sub}>{t('rs.subtitle')}</span>
          </span>
          <span className={styles.spacer} />
          <button
            type="button"
            className={styles.close}
            aria-label={t('a11y.close')}
            onClick={onClose}
          >
            ✕
          </button>
        </div>

        <div className={own.body}>
          {/* What is going out RIGHT NOW, with the stop beside it — the
              embedded client's "Streaming to" list. Everyone in the room
              sees this, because everyone in the room is in the picture
              being sent. The URLs arrive with the key already redacted. */}
          {state.streaming ? (
            <div className={own.active}>
              <div className={own.activeHead}>{t('rs.streaming')}</div>
              {(state.restreams && state.restreams.length > 0
                ? state.restreams
                : [{ id: '', platform, url: '', seconds: 0 }]
              ).map((r) => (
                <div key={r.id} className={own.activeRow}>
                  <span className={own.liveDot} aria-hidden="true" />
                  <span className={own.activeName}>
                    {t(`rs.platform.${r.platform}`, { defaultValue: r.platform })}
                  </span>
                  {r.url ? <span className={own.activeUrl}>{r.url}</span> : null}
                  <button
                    type="button"
                    className={own.stop}
                    onClick={() => actions.stopStream(r.id || undefined)}
                  >
                    {t('rs.stop')}
                  </button>
                </div>
              ))}
            </div>
          ) : null}

          <div className={own.platforms} role="radiogroup" aria-label={t('rs.title')}>
            {(Object.keys(PLATFORMS) as Platform[]).map((name) => (
              <button
                key={name}
                type="button"
                role="radio"
                aria-checked={platform === name}
                className={`${own.platform} ${platform === name ? own.platformOn : ''}`}
                onClick={() => pick(name)}
              >
                <span className={own.logo}>{LOGOS[name]}</span>
                <span className={own.name}>{t(`rs.platform.${name}`)}</span>
              </button>
            ))}
          </div>

          {/* One field, relabelled: a stream key for a platform, a whole
              address for your own receiver. Two fields would only ever have
              one filled in. */}
          <label className={styles.field}>
            <span className={styles.label}>
              {t(spec.key ? 'rs.key' : 'rs.url')}
            </span>
            <input
              className={own.input}
              type={spec.key ? 'password' : 'text'}
              value={typed}
              placeholder={
                spec.key ? t('rs.keyPlaceholder') : 'udp://host.docker.internal:5000'
              }
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') go()
              }}
            />
          </label>
          <p className={own.hint}>
            {spec.key ? t('rs.ingestHint', { ingest: spec.ingest }) : t('rs.hintCustom')}
          </p>

          <button
            type="button"
            className={own.check}
            aria-pressed={audio}
            onClick={() => setAudio(!audio)}
          >
            <span className={`${own.box} ${audio ? own.boxOn : ''}`} aria-hidden="true">
              {audio ? '✓' : ''}
            </span>
            {t('rs.audio')}
          </button>

          {/* The platforms are not asked whether viewers arrive mid-stream:
              they do, and the server forces keyframes for them regardless.
              Only a receiver you point at yourself gets the choice. */}
          {!spec.key ? (
            <>
              <button
                type="button"
                className={own.check}
                aria-pressed={kf}
                onClick={() => setKf(!kf)}
              >
                <span className={`${own.box} ${kf ? own.boxOn : ''}`} aria-hidden="true">
                  {kf ? '✓' : ''}
                </span>
                {t('rs.kf')}
              </button>
              <p className={own.hint}>{t('rs.kfNote')}</p>
            </>
          ) : null}

          <a className={own.help} href={HELP_URL} target="_blank" rel="noopener noreferrer">
            {t('rs.help')}
          </a>
        </div>

        <div className={styles.foot}>
          {status ? (
            <span
              role="status"
              className={`${own.msg} ${
                err || state.restreamAble === false ? own.msgBad : ''
              }`}
            >
              {status}
            </span>
          ) : null}
          <button
            type="button"
            className={styles.done}
            onClick={go}
            disabled={!typed.trim() || state.restreamAble === false}
          >
            {t('rs.go')}
          </button>
        </div>
      </div>
    </div>
  )
}
