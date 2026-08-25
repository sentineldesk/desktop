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

/* The top bar: the one piece both modes share, which is what makes Desktop
 * and Agent read as a single application rather than two screens glued
 * together. Three zones — identity on the left, the mode switch dead centre,
 * and the current mode's own tools on the right.
 *
 * The old instrument rail's tools all live here now, as a row of icon wells.
 * What was lost is the labels; what was gained is a desktop nobody's controls
 * float over. Every well keeps its title, and the states that mattered on the
 * rail (mic live, recording, restreaming) keep their colour language.
 *
 * The switch's active segment is raised with surface and a 1px border, never
 * with colour: phosphor green has ONE meaning in this application — who is
 * driving the desktop — and navigation does not get to spend it. */

import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { useDesktopStream } from '../transport/useDesktopStream'
import { LANGUAGES, setLanguage } from '../i18n'

export type ShellMode = 'desktop' | 'agent'

function Brand() {
  return (
    <svg viewBox="0 0 64 64" aria-hidden="true">
      <rect className="brand-scr" x="4" y="8" width="56" height="38" rx="6" />
      <rect className="brand-gls" x="10" y="14" width="44" height="26" rx="2" />
      <rect className="brand-scr" x="22" y="50" width="20" height="4" rx="2" />
      <rect className="brand-std" x="16" y="54" width="32" height="4" rx="2" />
    </svg>
  )
}

/* Initials for an avatar, from whatever name the room knows. */
function initials(name: string): string {
  const parts = name.trim().split(/\s+/).slice(0, 2)
  const two = parts.map((p) => p[0]?.toUpperCase() ?? '').join('')
  return two || '?'
}

/* A small fixed-position menu anchored under its button. The .sd-pop dress is
 * the stylesheet's own; only the coordinates are computed here. */
export function Anchored(props: {
  anchor: HTMLElement | null
  onClose(): void
  children: React.ReactNode
  minWidth?: number
}) {
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  useEffect(() => {
    const a = props.anchor
    if (!a) return
    const r = a.getBoundingClientRect()
    /* Right-aligned to the button, clamped to the viewport. */
    const w = props.minWidth ?? 180
    setPos({
      left: Math.max(8, Math.min(r.right - w, window.innerWidth - w - 8)),
      top: r.bottom + 6,
    })
  }, [props.anchor, props.minWidth])
  /* Any click outside closes it — including clicks on the desktop video,
   * which swallows events; pointerdown on window still fires first. */
  useEffect(() => {
    const off = (e: PointerEvent) => {
      const el = e.target as HTMLElement
      if (el.closest?.('.sd-pop') || el === props.anchor || props.anchor?.contains(el)) return
      props.onClose()
    }
    window.addEventListener('pointerdown', off)
    return () => window.removeEventListener('pointerdown', off)
  })
  if (!pos) return null
  return (
    <div
      className="sd-pop"
      role="menu"
      style={{ left: pos.left, top: pos.top, minWidth: props.minWidth ?? 180 }}
    >
      {props.children}
    </div>
  )
}

export function TopBar(props: {
  desktop: ReturnType<typeof useDesktopStream>
  mode: ShellMode
  onMode(mode: ShellMode): void
  /* The green dot on the Agent segment: the agent finished something while
   * the desktop was on screen. */
  agentFresh: boolean

  muted: boolean
  onAudio(): void
  onMic(): void
  onVoice(): void
  onShot(): void
  onRec(): void
  recClock: string
  onFiles(): void
  onStream(): void
  onStats(): void
  statsOpen: boolean
  onKb(): void
  kbOpen: boolean
  onPickQuality(mode: 'auto' | 'media' | 'high'): void
  onSettings(): void
  onAbout(): void
  onLogout?: (() => void) | undefined
  langCode: string
  name: string
}) {
  const { t } = useTranslation()
  const d = props.desktop
  const yours = d.control.yours
  const holder = d.control.holder
  const agentDrives = !yours && !!holder && d.control.holderIsAgent
  const watching = !yours && !!holder && !d.control.holderIsAgent
  const free = !yours && !holder

  const [qualityOpen, setQualityOpen] = useState(false)
  const [moreOpen, setMoreOpen] = useState(false)
  const [langOpen, setLangOpen] = useState(false)
  const qualityBtn = useRef<HTMLButtonElement>(null)
  const moreBtn = useRef<HTMLButtonElement>(null)

  /* Palette for the avatar colours: stable per name, so the same person keeps
   * the same colour across sessions without a server assigning one. */
  const hue = (name: string) => {
    let h = 0
    for (const c of name) h = (h * 31 + c.charCodeAt(0)) % 360
    return h
  }

  const presence = (
      <div id="tb-presence">
        <div className="tb-avatars" title={t('toolbar.presence')}>
          {d.members.slice(0, 4).map((m) =>
            m.agent ? (
              <span key={m.id} className="av agent" title={m.name}>
                IA
              </span>
            ) : (
              <span
                key={m.id}
                className="av"
                title={m.name}
                style={{
                  background: `linear-gradient(140deg, hsl(${hue(m.name)} 40% 40%), hsl(${(hue(m.name) + 40) % 360} 55% 55%))`,
                }}
              >
                {initials(m.name)}
              </span>
            ),
          )}
        </div>
        <button
          id="tb-control"
          className={
            yours ? '' : agentDrives ? 'agent-drives' : watching ? 'watching' : 'free'
          }
          onClick={d.toggleControl}
          title={yours ? t('room.release') : t('room.take')}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M5 3.5l13 8.5-5.6 1.4L15 19l-2.6 1.2-2.5-5.2L5 18.5z" />
          </svg>
          {yours
            ? t('room.youControl')
            : agentDrives
              ? t('ws.agentDrives')
              : watching
                ? t('room.controlledBy', { name: holder })
                : t('room.free')}
        </button>
      </div>
  )

  const desktopTools = (
    <>
      {d.recording ? <span className="tb-clock">{props.recClock}</span> : null}
      <button
        className={`tb-tool${d.recording ? ' live-rec' : ''}`}
        onClick={props.onRec}
        title={t(d.recording ? 'toolbar.recStop' : 'toolbar.rec')}
      >
        <svg viewBox="0 0 24 24">
          <circle cx="12" cy="12" r="7.5" />
          {d.recording ? (
            <rect x="8.8" y="8.8" width="6.4" height="6.4" rx="1" fill="currentColor" stroke="none" />
          ) : (
            <circle cx="12" cy="12" r="3" fill="currentColor" stroke="none" />
          )}
        </svg>
      </button>
      <button className="tb-tool" onClick={props.onShot} title={t('toolbar.shot')}>
        <svg viewBox="0 0 24 24">
          <path d="M4 8.5h3l1.6-2.2h6.8L17 8.5h3v9.5H4z" />
          <circle cx="12" cy="13" r="3" />
        </svg>
      </button>
      <button
        className={`tb-tool${d.micLive ? ' live' : ''}`}
        onClick={props.onMic}
        title={t('toolbar.mic')}
      >
        <svg viewBox="0 0 24 24">
          <rect x="9" y="3" width="6" height="11" rx="3" />
          <path d="M5.5 11.5a6.5 6.5 0 0013 0M12 18v3" />
        </svg>
      </button>
      <button
        className={`tb-tool${d.restreaming ? ' live' : ''}`}
        onClick={props.onStream}
        title={t('toolbar.rtmp')}
      >
        <svg viewBox="0 0 24 24">
          <path d="M4 6.5h9l1.5 8.5H5.5z" />
          <path d="M17 9l3.5-2v10L17 15" />
        </svg>
      </button>

      <div className="tb-sep" />

      <button
        className={`tb-tool${d.voiceLive ? ' live' : ''}`}
        onClick={props.onVoice}
        title={t('toolbar.voice')}
      >
        {/* A headset, not another speaker: the audio toggle two wells over
         * already wears the speaker-with-waves, and two identical icons for
         * two different verbs read as a duplicate button. */}
        <svg viewBox="0 0 24 24">
          <path d="M4 14a8 8 0 0 1 16 0" />
          <rect x="3" y="14" width="4.5" height="6" rx="2" />
          <rect x="16.5" y="14" width="4.5" height="6" rx="2" />
        </svg>
        {d.voiceLive && d.voicePeers.length > 0 ? (
          <span className="badge">{d.voicePeers.length}</span>
        ) : null}
      </button>
      <button
        className={`tb-tool${props.muted ? '' : ' live'}`}
        onClick={props.onAudio}
        title={t('toolbar.audio')}
      >
        {props.muted ? (
          <svg viewBox="0 0 24 24">
            <path d="M4.5 9.5h3l4-3.2v11.4l-4-3.2h-3z" />
            <path d="M16 9.5l5 5M21 9.5l-5 5" />
          </svg>
        ) : (
          <svg viewBox="0 0 24 24">
            <path d="M4.5 9.5h3l4-3.2v11.4l-4-3.2h-3z" />
            <path d="M15.5 9a4.2 4.2 0 010 6M17.5 6.5a7.6 7.6 0 010 11" />
          </svg>
        )}
      </button>
      <button className="tb-tool" onClick={props.onFiles} title={t('toolbar.files')}>
        <svg viewBox="0 0 24 24">
          <path d="M4 5.5h6l2 2.2h8V18H4z" />
        </svg>
      </button>
      <button
        className={`tb-tool${props.statsOpen ? ' on' : ''}`}
        onClick={props.onStats}
        title={t('toolbar.stats')}
      >
        <svg viewBox="0 0 24 24">
          <path d="M4.5 19.5V13M9.5 19.5V7.5M14.5 19.5V11M19.5 19.5V4.5" />
        </svg>
      </button>
      <button
        className={`tb-tool${props.kbOpen ? ' on' : ''}`}
        aria-disabled={!yours}
        onClick={props.onKb}
        title={t('toolbar.kb')}
      >
        <svg viewBox="0 0 24 24">
          <rect x="3" y="7" width="18" height="11" rx="2" />
          <path d="M6.5 10.5h1M10 10.5h1M13.5 10.5h1M17 10.5h1M8 15h8" />
        </svg>
      </button>

      <div className="tb-sep" />

      <button
        id="tb-quality"
        ref={qualityBtn}
        aria-disabled={!yours}
        aria-expanded={qualityOpen}
        onClick={() => {
          if (yours) setQualityOpen((v) => !v)
        }}
        title={t('toolbar.quality')}
      >
        {t('label.quality')} <b>{t(`quality.${d.quality.mode}`)}</b>
        <svg viewBox="0 0 24 24">
          <path d="M7 10l5 5 5-5" />
        </svg>
      </button>
      {qualityOpen ? (
        <Anchored anchor={qualityBtn.current} onClose={() => setQualityOpen(false)} minWidth={140}>
          {(['auto', 'media', 'high'] as const).map((mode) => (
            <button
              key={mode}
              role="menuitemradio"
              aria-checked={d.quality.mode === mode}
              onClick={() => {
                props.onPickQuality(mode)
                setQualityOpen(false)
              }}
            >
              <span>{t(`quality.${mode}`)}</span>
              <svg className="tick" viewBox="0 0 24 24">
                <path d="M5 12.5l4.5 4.5L19 7.5" />
              </svg>
            </button>
          ))}
        </Anchored>
      ) : null}

      <button
        className="tb-tool"
        onClick={() => {
          if (document.fullscreenElement) {
            void document.exitFullscreen()
            return
          }
          void document.documentElement
            .requestFullscreen()
            .then(() => {
              /* Fullscreen means the machine: with the keyboard locked,
               * Alt+Tab, Ctrl+W and even Escape belong to the DESKTOP, the
               * way they would on the machine itself. A HELD Escape (~2s)
               * is the browser's own way back out — the API always keeps
               * that. Browsers without it just keep today's behaviour. */
              const kb = (navigator as Navigator & {
                keyboard?: { lock?: () => Promise<void> }
              }).keyboard
              return kb?.lock?.()
            })
            .catch(() => undefined)
        }}
        title={t('toolbar.fullscreen')}
      >
        <svg viewBox="0 0 24 24">
          <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />
        </svg>
      </button>
    </>
  )

  const agentSide = (
    <div
      className={`tb-chip ${d.state === 'live' ? 'ok' : 'off'}`}
      title={t('toolbar.presence')}
    >
      <span className="dot" />
      {t('ws.desktopActive')}
    </div>
  )

  return (
    <header id="topbar">
      <div className="tb-side">
        <span className="tb-brand">
          <Brand />
          SentinelDesk
        </span>
        {props.mode === 'desktop' ? (
          <>
            <div className="tb-sep" />
            {presence}
          </>
        ) : null}
      </div>

      <div id="mode-switch" role="tablist" aria-label="Desktop / Agent">
        <button
          role="tab"
          aria-selected={props.mode === 'desktop'}
          className={props.mode === 'desktop' ? 'on' : ''}
          onClick={() => props.onMode('desktop')}
        >
          <svg viewBox="0 0 24 24">
            <rect x="2.5" y="4" width="19" height="13" rx="1.6" />
            <path d="M8.5 20.5h7" />
          </svg>
          Desktop
        </button>
        <button
          role="tab"
          aria-selected={props.mode === 'agent'}
          className={props.mode === 'agent' ? 'on' : ''}
          onClick={() => props.onMode('agent')}
        >
          <svg viewBox="0 0 24 24">
            <path d="M12 3l1.9 4.6 4.6 1.9-4.6 1.9L12 16l-1.9-4.6L5.5 9.5l4.6-1.9z" />
            <path d="M18.5 15.5l.9 2.1 2.1.9-2.1.9-.9 2.1-.9-2.1-2.1-.9 2.1-.9z" />
          </svg>
          Agent
          {props.agentFresh && props.mode === 'desktop' ? (
            <span className="fresh" aria-label={t('ws.agentFresh')} />
          ) : null}
        </button>
      </div>

      <div className="tb-side right">
        {props.mode === 'desktop' ? desktopTools : agentSide}

        <button
          className="tb-tool"
          ref={moreBtn}
          aria-expanded={moreOpen}
          onClick={() => {
            setMoreOpen((v) => !v)
            setLangOpen(false)
          }}
          title={t('ws.more')}
        >
          <svg viewBox="0 0 24 24">
            <circle cx="12" cy="6" r="1.4" fill="currentColor" stroke="none" />
            <circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none" />
            <circle cx="12" cy="18" r="1.4" fill="currentColor" stroke="none" />
          </svg>
        </button>
        {moreOpen ? (
          <Anchored anchor={moreBtn.current} onClose={() => setMoreOpen(false)} minWidth={200}>
            <div className="head">{props.name || t('ws.you')}</div>
            <button
              onClick={() => {
                props.onSettings()
                setMoreOpen(false)
              }}
            >
              <span>{t('ws.settings')}</span>
            </button>
            {langOpen ? (
              LANGUAGES.map((l) => (
                <button
                  key={l.code}
                  role="menuitemradio"
                  aria-checked={props.langCode === l.code}
                  onClick={() => {
                    setLanguage(l.code)
                    setLangOpen(false)
                    setMoreOpen(false)
                  }}
                >
                  <span>{l.name}</span>
                  <svg className="tick" viewBox="0 0 24 24">
                    <path d="M5 12.5l4.5 4.5L19 7.5" />
                  </svg>
                </button>
              ))
            ) : (
              <button onClick={() => setLangOpen(true)}>
                <span>{t('label.language')}</span>
                <span style={{ color: 'var(--sd-dim)', fontSize: 11 }}>
                  {LANGUAGES.find((l) => l.code === props.langCode)?.chip ??
                    props.langCode.toUpperCase()}
                </span>
              </button>
            )}
            <button
              onClick={() => {
                const dark = !document.documentElement.classList.contains('dark')
                try {
                  localStorage.setItem('sentineldesk-theme', dark ? 'dark' : 'light')
                } catch {
                  /* storage off: switches, just forgets */
                }
                document.documentElement.classList.toggle('dark', dark)
                document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
                setMoreOpen(false)
              }}
            >
              <span>{t('ws.theme')}</span>
              <span style={{ color: 'var(--sd-dim)', fontSize: 11 }}>
                {document.documentElement.classList.contains('dark')
                  ? t('ws.themeDark')
                  : t('ws.themeLight')}
              </span>
            </button>
            <button
              onClick={() => {
                setMoreOpen(false)
                props.onAbout()
              }}
            >
              <span>{t('about.item')}</span>
            </button>
            {props.onLogout ? (
              <button
                onClick={() => {
                  setMoreOpen(false)
                  props.onLogout?.()
                }}
              >
                <span style={{ color: 'var(--sd-bad-ink)' }}>{t('label.logout')}</span>
              </button>
            ) : null}
          </Anchored>
        ) : null}
      </div>
    </header>
  )
}
