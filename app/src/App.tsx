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

/* The standalone desktop client, in React — the first client's face on the
 * workroom's engine.
 *
 * Every id and class here matches the original hand-written client on
 * purpose: styles.css is that client's stylesheet verbatim, the design was
 * approved screen by screen, and the rewrite changes the engine, never the
 * face. The engine is the panel's own transport (useDesktopStream), so the
 * two clients can no longer drift apart — the drift is what this rewrite
 * exists to end. */

import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AgentQuestion } from './transport/AgentQuestion'
import { ControlRequest } from './transport/ControlRequest'
import { attachDesktopInput } from './transport/desktopInput'
import { attachTouchInput } from './transport/touchInput'
import { FilesDialog } from './transport/FilesDialog'
import { MediaSettings, storedName } from './transport/MediaSettings'
import { RemotePointer } from './transport/RemotePointer'
import { StreamDialog } from './transport/StreamDialog'
import { DropLayer, useDesktopDrop } from './transport/useDesktopDrop'
import { playOrRescue } from './transport/mediaPrefs'
import {
  useDesktopStream,
  type StandaloneAuth,
} from './transport/useDesktopStream'
import { useWebMcp } from './transport/useWebMcp'
import { LANGUAGES, setLanguage } from './i18n'
import { useLiveStats } from './ui/useLiveStats'
import { Kbd } from './ui/Kbd'

const DOCS_URL = 'https://sentineldesk.github.io/desktop/docs/guide/index.html'

/* How many wallpapers the login rotates through: public/login/1.webp …
 * N.webp, one dealt at random per visit. Bump this when art is added. */
const LOGIN_WALLPAPERS = 8

type RailMode = 'full' | 'collapsed' | 'hidden'

/* ---- the logo, shared by the boot screen and the login ------------------- */

function Brand() {
  return (
    <svg viewBox="0 0 64 64" aria-hidden="true">
      <rect className="scr" x="4" y="8" width="56" height="38" rx="6" />
      <rect className="gls" x="10" y="14" width="44" height="26" rx="2" />
      <rect className="scr" x="22" y="50" width="20" height="4" rx="2" />
      <rect className="std" x="16" y="54" width="32" height="4" rx="2" />
    </svg>
  )
}

/* ---- toasts -------------------------------------------------------------- */

function useToast() {
  const [toast, setToast] = useState<{ msg: string; err: boolean } | null>(null)
  const timer = useRef(0)
  const show = useCallback((msg: string, err = false) => {
    setToast({ msg, err })
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setToast(null), err ? 6000 : 4000)
  }, [])
  return { toast, show }
}

/* ---- the app ------------------------------------------------------------- */

export function App() {
  const { t, i18n } = useTranslation()

  /* The door's requirements, probed once: /auth only reports whether a login
   * is required — there is deliberately no HTTP login endpoint. Credentials
   * travel in the WebSocket's first frame, inside the hook. */
  const [authRequired, setAuthRequired] = useState<boolean | null>(null)
  const [auth, setAuth] = useState<StandaloneAuth | null>(null)
  useEffect(() => {
    void fetch('/auth')
      .then((r) => r.json())
      .then((d: { required?: boolean }) => {
        setAuthRequired(!!d.required)
        if (!d.required) setAuth('open')
        else {
          /* A reload is not a new person: the minted session token rides
           * sessionStorage, exactly as the first client kept it. */
          const saved = sessionStorage.getItem('sentineldesk_token')
          if (saved) setAuth({ token: saved })
        }
      })
      .catch(() => setAuthRequired(false))
  }, [])

  /* The nickname rides the auth frame on every (re)connect; changing it in
   * Settings updates this state so a reconnect keeps the new name, and the
   * live room hears about it through a rename event (settingsChanged). */
  const [name, setName] = useState(storedName)
  const desktop = useDesktopStream(auth, name, (token) => {
    sessionStorage.setItem('sentineldesk_token', token)
  })
  const { toast, show } = useToast()

  /* Publish the desktop's WebMCP tools to whatever AI the browser carries —
   * live only, same authenticated DataChannel a person's clicks take. */
  useWebMcp(desktop)

  /* Login failed: forget the credential that failed — a stale token from a
   * restarted server reads exactly like a wrong password — and drop back to
   * the form with the reason on it. */
  const loginFailed = desktop.state === 'failed' && desktop.error === 'login.failed'
  useEffect(() => {
    if (loginFailed) {
      sessionStorage.removeItem('sentineldesk_token')
      setAuth(null)
    }
  }, [loginFailed])

  const videoRef = useRef<HTMLVideoElement | null>(null)
  /* WHY the audio starts off, which turns out to be the whole question — the
   * first client's answer, kept. Two reasons want opposite treatment: the
   * autoplay policy refuses unmuted sound before a gesture, so playback
   * begins muted and the FIRST CLICK lifts it; a person choosing Audio off
   * means something else entirely, and their next click is not consent to
   * undo it. `muted` is the person's choice (default: sound ON), and
   * `policyMuted` is the browser's hold — shown on the speaker icon so it
   * never claims sound while none plays. */
  const [muted, setMuted] = useState(false)
  const [policyMuted, setPolicyMuted] = useState(false)

  /* The stream into the element, and the element into the input pipe. */
  const mutedRef = useRef(muted)
  mutedRef.current = muted
  useEffect(() => {
    const el = videoRef.current
    if (!el || !desktop.stream) return
    el.srcObject = desktop.stream
    return playOrRescue(el, () => !mutedRef.current, setPolicyMuted)
  }, [desktop.stream])

  const controlRef = useRef(desktop.control)
  controlRef.current = desktop.control
  useEffect(() => {
    const el = videoRef.current
    if (!el || desktop.state !== 'live') return
    const offMouse = attachDesktopInput(el, desktop, () => controlRef.current.yours)
    const offTouch = attachTouchInput(el, desktop, () => controlRef.current.yours)
    return () => {
      offMouse()
      offTouch()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [desktop.state, desktop.inputReady])

  /* Drag and drop. Deliberately NOT gated on the controls: handing a file to
   * the room is provisioning, not driving — the same reason the Files dialog
   * uploads without the seat. The server still owns the rule; if it refuses,
   * the drop tray shows the failure instead of silently eating the file. */
  const upload = useCallback(
    (file: File, onProgress: (bytes: number) => void) =>
      desktop.uploadFile(file, onProgress),
    [desktop],
  )
  const drop = useDesktopDrop(upload)

  /* Dialogs and panels. */
  const [filesOpen, setFilesOpen] = useState(false)
  const [streamOpen, setStreamOpen] = useState(false)
  const [statsOpen, setStatsOpen] = useState(false)
  const [kbOpen, setKbOpen] = useState(false)
  const stats = useLiveStats(statsOpen, desktop, videoRef)

  /* The rail's three postures, remembered per browser like the first client
   * remembered them. º toggles hidden, matching the handle's own hint. */
  const [rail, setRail] = useState<RailMode>(
    () => (localStorage.getItem('sentineldesk.rail') as RailMode) || 'full',
  )
  useEffect(() => {
    localStorage.setItem('sentineldesk.rail', rail)
  }, [rail])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'º' || e.key === '°') {
        setRail((r) => (r === 'hidden' ? 'full' : 'hidden'))
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  /* The recording clock. */
  const [recSeconds, setRecSeconds] = useState(0)
  useEffect(() => {
    if (!desktop.recording || !desktop.recordingSince) {
      setRecSeconds(0)
      return
    }
    const tick = () =>
      setRecSeconds(Math.max(0, Math.floor((Date.now() - desktop.recordingSince) / 1000)))
    tick()
    const timer = window.setInterval(tick, 1000)
    return () => window.clearInterval(timer)
  }, [desktop.recording, desktop.recordingSince])

  /* Named refusals arrive on the same state their dials read. */
  useEffect(() => {
    if (desktop.qualityError === 'needControl') show(t('quality.needControl'), true)
    else if (desktop.qualityError) show(desktop.qualityError, true)
  }, [desktop.qualityError, show, t])
  useEffect(() => {
    if (desktop.restreamError === 'needControl') show(t('media.needControl'), true)
    else if (desktop.restreamError) show(desktop.restreamError, true)
  }, [desktop.restreamError, show, t])

  /* Quality is a MENU like Language, not a cycle: it started as a cycling
   * button by request, and the same person counted the clicks it takes to
   * reach the mode you want and asked for the list back. */
  const [qualityOpen, setQualityOpen] = useState(false)
  const pickQuality = (mode: 'auto' | 'media' | 'high') => {
    desktop.setQuality(mode)
    setQualityOpen(false)
  }

  const shot = () => {
    desktop.sendInput({ t: 'capture', action: 'shot' })
  }
  const rec = () => {
    desktop.sendInput({
      t: 'capture',
      action: desktop.recording ? 'rec_stop' : 'rec_start',
    })
  }
  /* Which device the mic and the voice conference use is the Settings
   * dialog's business now (mediaPrefs) — the hook reads the remembered
   * choice itself, with its own fallback when the device is gone. */
  const mic = () => {
    void desktop
      .toggleMic()
      .then(() => show(t(desktop.micLive ? 'media.micOff' : 'media.micOn')))
      .catch((err: Error & { name?: string }) => {
        if (err.name === 'NotAllowedError') show(t('media.permissionDenied'), true)
        else if (err.message.startsWith('media.')) show(t(err.message), true)
        else show(err.message, true)
      })
  }
  const [settingsOpen, setSettingsOpen] = useState(false)
  /* Closing Settings applies the choices to whatever is LIVE: a changed
   * device re-acquires by toggling off and on again — the same paths the
   * buttons use, so a new mic takes effect mid-session. */
  const settingsChanged = (changed: readonly ('mic' | 'voice' | 'name')[]) => {
    if (changed.includes('name')) {
      const n = storedName()
      setName(n)
      if (n) desktop.sendInput({ t: 'rename', name: n })
    }
    if (changed.includes('mic') && desktop.micLive) {
      void desktop
        .toggleMic()
        .then(() => desktop.toggleMic())
        .catch(() => show(t('media.permissionDenied'), true))
    }
    if (changed.includes('voice') && desktop.voiceLive) {
      void desktop
        .toggleVoice()
        .then(() => desktop.toggleVoice())
        .catch(() => show(t('media.permissionDenied'), true))
    }
  }
  const voice = () => {
    void desktop.toggleVoice().catch((err: Error & { name?: string }) => {
      if (err.name === 'NotAllowedError') show(t('media.permissionDenied'), true)
      else show(err.message, true)
    })
  }
  const pause = () => {
    desktop.sendInput({ t: desktop.paused ? 'resume' : 'pause' })
  }
  const abort = () => {
    desktop.sendInput({ t: 'abort' })
    show(t('room.abortSent'))
  }

  /* The full tag, not a two-letter slice: zh-TW and zh-HK are different
   * entries in the menu and slicing would collapse them onto each other. */
  const langCode = i18n.language || 'en'
  const [langOpen, setLangOpen] = useState(false)

  const live = desktop.state === 'live'
  const showLogin = authRequired === true && auth === null
  const showStatus = !live && !showLogin

  const fmt = (s: number) =>
    `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`

  return (
    <>
      {/* The X cursor's real shape rides the wire (XFixes → a PNG with its
          hotspot) and lands here as a CSS cursor — the move cross of an
          Alt-drag, the resize arrows on a window edge, the text beam. Only
          while driving: watching keeps the ordinary arrow, because a resize
          arrow over a desktop you cannot resize is a promise. */}
      <video
        id="screen"
        ref={videoRef}
        autoPlay
        playsInline
        muted={muted || policyMuted}
        tabIndex={0}
        style={{
          visibility: live ? 'visible' : 'hidden',
          cursor:
            desktop.control.yours && desktop.cursor ? desktop.cursor : undefined,
        }}
      />

      {live && !desktop.control.yours && desktop.pointer ? (
        <RemotePointer
          videoRef={videoRef}
          pointer={desktop.pointer}
          name={desktop.control.holder ?? ''}
          agent={desktop.control.holderIsAgent}
          color={desktop.members.find((m) => m.controller)?.color}
        />
      ) : null}

      {showStatus ? (
        <div id="status" className="visible">
          <div className="brand">
            <Brand />
            <span>SentinelDesk</span>
          </div>
          <div className="scan" aria-hidden="true" />
          <div id="status-text">
            {desktop.state === 'failed' ? t(desktop.error ?? 'status.error') : t('status.connecting')}
          </div>
          {desktop.state === 'failed' ? (
            <button type="button" id="login-btn" onClick={desktop.retry} style={{ maxWidth: 240 }}>
              {t('wr.desk.retry')}
            </button>
          ) : null}
        </div>
      ) : null}

      {showLogin ? (
        <Login
          error={loginFailed ? t('login.failed') : ''}
          onSubmit={(user, pass) => setAuth({ user, pass })}
        />
      ) : null}

      {live && rail === 'hidden' ? (
        <div
          id="toolbar-handle"
          className="show"
          title={t('toolbar.show')}
          onClick={() => setRail('full')}
        >
          <span>º</span>
          <svg viewBox="0 0 24 24">
            <path d="M9 6l6 6-6 6" />
          </svg>
        </div>
      ) : null}

      {live ? (
        <Rail
          desktop={desktop}
          mode={rail}
          onCollapse={() => setRail((r) => (r === 'collapsed' ? 'full' : 'collapsed'))}
          onHide={() => setRail('hidden')}
          muted={muted || policyMuted}
          onAudio={() => {
            /* The click targets the opposite of what the icon showed. The
             * same click, as a gesture, already lifted a policy mute before
             * this handler ran — so compute from the rendered state, not by
             * toggling, or enabling sound would immediately re-mute it. */
            setMuted(!(muted || policyMuted))
            setPolicyMuted(false)
          }}
          onMic={mic}
          onVoice={voice}
          onSettings={() => setSettingsOpen(true)}
          onShot={shot}
          onRec={rec}
          recClock={fmt(recSeconds)}
          onPause={pause}
          onAbort={abort}
          onFiles={() => setFilesOpen(true)}
          onStream={() => setStreamOpen(true)}
          onStats={() => setStatsOpen((v) => !v)}
          onKb={() => {
            /* Without the controls every key would bounce off the server;
             * a board that cannot type is a lie, so the button says why. */
            if (!desktop.control.yours) {
              show(t('kb.needControl'), true)
              return
            }
            setKbOpen((v) => !v)
          }}
          kbOpen={kbOpen}
          statsOpen={statsOpen}
          onQuality={() => {
            if (desktop.control.yours) setQualityOpen((v) => !v)
          }}
          qualityOpen={qualityOpen}
          onPickQuality={pickQuality}
          onLogout={
            authRequired
              ? () => {
                  /* The first client's logout, kept: forget the session and
                   * fall back to the door. */
                  sessionStorage.removeItem('sentineldesk_token')
                  setAuth(null)
                }
              : undefined
          }
          langCode={langCode}
          langOpen={langOpen}
          onLang={() => setLangOpen((v) => !v)}
          onPickLang={(code) => {
            setLanguage(code)
            setLangOpen(false)
          }}
        />
      ) : null}

      {live && !desktop.control.yours ? (
        <div id="watching" className="show" role="status">
          {/* The strings carry a <b> around the load-bearing word — they are
           * our own files, not user input, so rendering them as markup is
           * safe and the alternative shows the tags to the person. */}
          <span
            dangerouslySetInnerHTML={{
              __html: desktop.control.holder
                ? t('room.watching')
                : t('room.freeNotice'),
            }}
          />
          <button id="btn-take-watch" onClick={desktop.toggleControl}>
            {t('room.take')}
          </button>
        </div>
      ) : null}

      {statsOpen && stats ? (
        <div id="stats" className="show" role="status">
          <div className="st-head">
            <span>WEBRTC</span>
            <span className="st-live" />
            <button id="stats-close" title={t('stats.close')} onClick={() => setStatsOpen(false)}>
              <svg viewBox="0 0 24 24">
                <path d="M6 6l12 12M18 6L6 18" />
              </svg>
            </button>
          </div>
          <div className="grid" id="stats-grid">
            <span className="k">{t('stats.size')}</span>
            <span>{stats.size}</span>
            <span className="k">{t('stats.codec')}</span>
            <span>
              {stats.vcodec} / {stats.acodec}
            </span>
            <span className="k">FPS</span>
            <span>{stats.fps}</span>
            <span className="k">{t('stats.bitrate')}</span>
            <span>{stats.bitrate} Mb/s</span>
            <span className="k">{t('stats.latency')}</span>
            <span>{stats.rtt} ms</span>
            <span className="k">{t('stats.loss')}</span>
            <span>{stats.loss.toFixed(2)}%</span>
          </div>
          <svg className="spark" width="184" height="30" viewBox="0 0 184 30">
            <polyline
              id="stats-spark"
              points={(() => {
                const h = stats.history
                if (h.length < 2) return ''
                const top = Math.max(1, ...h)
                const w = 184
                return h
                  .map(
                    (v, i) =>
                      `${((i * w) / Math.max(1, h.length - 1)).toFixed(1)},${(
                        28 -
                        (v / top) * 26
                      ).toFixed(1)}`,
                  )
                  .join(' ')
              })()}
            />
          </svg>
        </div>
      ) : null}

      {desktop.recording ? (
        <div id="rec-badge">
          <span className="dot" />
          <span id="rec-time">{fmt(recSeconds)}</span>
        </div>
      ) : null}

      {filesOpen ? <FilesDialog desktop={desktop} onClose={() => setFilesOpen(false)} /> : null}
      {streamOpen ? <StreamDialog desktop={desktop} onClose={() => setStreamOpen(false)} /> : null}

      {settingsOpen ? (
        <MediaSettings
          onClose={() => setSettingsOpen(false)}
          onChanged={settingsChanged}
        />
      ) : null}

      {live && kbOpen && desktop.control.yours ? (
        <Kbd send={desktop.sendInput} onClose={() => setKbOpen(false)} />
      ) : null}

      {desktop.question ? (
        <AgentQuestion question={desktop.question} onAnswer={desktop.answerQuestion} />
      ) : null}

      {desktop.controlRequest ? (
        <ControlRequest
          request={desktop.controlRequest}
          onAnswer={desktop.answerControlRequest}
        />
      ) : null}

      <DropLayer
        armed={drop.armed}
        drops={drop.drops}
        deliveries={desktop.deliveries}
        onSave={desktop.saveDelivery}
        onDismiss={desktop.dismissDelivery}
      />

      {toast ? (
        <div id="toast" className={`show${toast.err ? ' err' : ''}`} role="status">
          {toast.msg}
        </div>
      ) : null}
    </>
  )
}

/* ---- login --------------------------------------------------------------- */

function Login({
  error,
  onSubmit,
}: {
  error: string
  onSubmit(user: string, pass: string): void
}) {
  const { t } = useTranslation()
  const [user, setUser] = useState('')
  const [pass, setPass] = useState('')
  /* One wallpaper per visit, dealt once when the screen mounts so the whole
   * login — the full-screen blur behind, the frosted form, the framed
   * picture — wears the SAME image. LOGIN_WALLPAPERS is the count of
   * public/login/N.webp; a reload deals another. */
  const [wp] = useState(() => 1 + Math.floor(Math.random() * LOGIN_WALLPAPERS))
  const art = `/login/${wp}.webp`
  return (
    <div id="login" className="visible" style={{ '--login-art': `url(${art})` } as React.CSSProperties}>
      <form
        id="login-form"
        autoComplete="off"
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit(user, pass)
        }}
      >
        <h1>
          <Brand />
          <span>SentinelDesk</span>
        </h1>
        <div className="subtitle">{t('login.subtitle')}</div>
        <label htmlFor="login-user">{t('login.username')}</label>
        <input
          id="login-user"
          value={user}
          onChange={(e) => setUser(e.target.value)}
          autoComplete="username"
          autoFocus
        />
        <label htmlFor="login-pass">{t('login.password')}</label>
        <input
          id="login-pass"
          type="password"
          value={pass}
          onChange={(e) => setPass(e.target.value)}
          autoComplete="current-password"
        />
        <div className="error" id="login-error">
          {error}
        </div>
        <button type="submit" id="login-btn">
          {t('login.submit')}
        </button>
      </form>
      {/* The same image as the blurred ground, shown whole and crisp, in a
        * frame that shrink-wraps it so the neon ring traces its exact edge. */}
      <div id="login-art" aria-hidden="true">
        <div className="login-frame">
          <img src={art} alt="" />
        </div>
      </div>
    </div>
  )
}

/* ---- the rail ------------------------------------------------------------ */

function Rail(props: {
  desktop: ReturnType<typeof useDesktopStream>
  mode: RailMode
  onCollapse(): void
  onHide(): void
  muted: boolean
  onAudio(): void
  onMic(): void
  onVoice(): void
  onSettings(): void
  onShot(): void
  onRec(): void
  /** mm:ss while recording — the collapsed rail's #rec-mini reads it. */
  recClock: string
  onPause(): void
  onAbort(): void
  onFiles(): void
  onStream(): void
  onStats(): void
  statsOpen: boolean
  onKb(): void
  kbOpen: boolean
  onQuality(): void
  qualityOpen: boolean
  onPickQuality(mode: 'auto' | 'media' | 'high'): void
  /** Present only when the door has a login to fall back to. */
  onLogout?: () => void
  langCode: string
  langOpen: boolean
  onLang(): void
  onPickLang(code: string): void
}) {
  const { t } = useTranslation()
  const d = props.desktop
  const yours = d.control.yours
  /* The room's state lands on the rail itself: the 3px signature strip and
   * the collapsed ring read these classes — phosphor while you drive,
   * dotted amber while the controls are free, violet under the agent —
   * because they are what remains of the identity once the labels are
   * gone. Same classes the first client toggled. */
  const solo = d.members.length <= 1
  const free = !yours && !d.control.holder
  const watching = !yours && !!d.control.holder
  const stateCls = [
    solo ? 'solo' : '',
    yours ? 'you-control' : '',
    free ? 'free' : '',
    watching ? 'watching' : '',
    d.control.holderIsAgent ? 'agent-controls has-agent' : '',
  ]
    .join(' ')
    .trim()
  const cls = [
    props.mode === 'collapsed' ? 'collapsed' : '',
    props.mode === 'hidden' ? 'hidden' : '',
    stateCls,
  ]
    .join(' ')
    .trim()

  return (
    <div
      id="toolbar"
      className={cls}
      role="toolbar"
      aria-orientation="vertical"
      aria-label="SentinelDesk"
    >
      <span id="rail-sig" aria-hidden="true" />
      <div id="wordmark">
        <span className="x-only">SENTINELDESK</span>
        <span className="c-only">SD</span>
      </div>

      <div id="presence" className={`x-only ${stateCls}`}>
        <div id="pres-head">
          <span>{t('room.presence')}</span>
        </div>
        <div id="pres-chip">
          <svg className="i-solo" viewBox="0 0 24 24">
            <circle cx="12" cy="8" r="3.4" />
            <path d="M5.5 19.5c.9-3.6 3.5-5.3 6.5-5.3s5.6 1.7 6.5 5.3" />
          </svg>
          <svg className="i-drive" viewBox="0 0 24 24">
            <path d="M5.5 3.5l13.5 8-6 1.6-3.4 5.4z" />
          </svg>
          <svg className="i-watch" viewBox="0 0 24 24">
            <path d="M2.5 12S6 5.6 12 5.6 21.5 12 21.5 12 18 18.4 12 18.4 2.5 12 2.5 12z" />
            <circle cx="12" cy="12" r="2.7" />
          </svg>
          <span id="presence-text">
            {yours
              ? t('room.youControl')
              : d.control.holder
                ? t('room.controlledBy', { name: d.control.holder })
                : t('room.free')}
          </span>
        </div>
        <div id="pres-roster">
          {d.members.map((m) => (
            <div key={m.id} className="pres-row">
              {/* The dot wears the member's ink — the same colour their
                * pointer wears on the desktop — so the roster answers "whose
                * cursor is the magenta one" without anyone asking. Talking
                * keeps its green pulse on top. */}
              <span
                className={`pres-dot${
                  (m.id === d.myId && d.voiceLive) || d.voicePeers.includes(m.id)
                    ? ' talk'
                    : ''
                }`}
                style={
                  (m.id === d.myId && d.voiceLive) || d.voicePeers.includes(m.id)
                    ? undefined
                    : { background: m.color }
                }
              />
              <span className="pres-name">
                {m.name}
                {m.id === d.myId ? ` (${t('room.you')})` : ''}
              </span>
              {m.controller ? <span className="pres-drive">◆</span> : null}
            </div>
          ))}
        </div>
        <button id="btn-control" onClick={d.toggleControl}>
          {yours ? t('room.release') : t('room.take')}
        </button>
        <button
          id="btn-pause"
          onClick={props.onPause}
          title={t('room.pauseTitle')}
        >
          {d.paused ? t('room.resume', { who: d.pausedBy }) : t('room.pause')}
        </button>
        <button id="btn-abort" onClick={props.onAbort} title={t('room.abortTitle')}>
          {t('room.abort')}
        </button>
      </div>

      <div id="pres-mini" className="c-only">
        <button
          id="btn-control-mini"
          title={yours ? t('room.release') : t('room.take')}
          onClick={d.toggleControl}
        >
          <svg viewBox="0 0 24 24">
            <circle cx="12" cy="8" r="3.4" />
            <path d="M5.5 19.5c.9-3.6 3.5-5.3 6.5-5.3s5.6 1.7 6.5 5.3" />
          </svg>
        </button>
      </div>

      <div className="sd-sep" />
      <div className="sd-group x-only">{t('group.capture')}</div>
      <div className="sd-rows">
        <button
          id="btn-mic"
          className={`row${d.micLive ? ' live' : ''}`}
          aria-pressed={d.micLive}
          aria-disabled={!yours && !d.micLive}
          onClick={props.onMic}
          onContextMenu={(e) => {
            /* Right-click on the microphone keeps meaning "which one":
             * it opens the same Settings the gear does. */
            e.preventDefault()
            props.onSettings()
          }}
          title={t('toolbar.mic')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <rect x="9" y="3.5" width="6" height="11" rx="3" />
              <path d="M6 11.5a6 6 0 0 0 12 0M12 17.5v3" />
            </svg>
          </span>
          <span className="lb">{t('label.mic')}</span>
          {d.micLive ? <span className="lb live-chip">{t('chip.live')}</span> : null}
        </button>
        <button id="btn-shot" className="row" onClick={props.onShot} title={t('toolbar.shot')}>
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <path d="M4 8h3l2-3h6l2 3h3v11H4z" />
              <circle cx="12" cy="13" r="3.4" />
            </svg>
          </span>
          <span className="lb">{t('label.shot')}</span>
        </button>
        <button
          id="btn-rec"
          className={`row${d.recording ? ' live' : ''}`}
          aria-pressed={d.recording}
          onClick={props.onRec}
          title={t(d.recording ? 'toolbar.recStop' : 'toolbar.rec')}
        >
          {/* The first client's exact icon: the ring, a dot that becomes a
            * stop square while recording (CSS swaps .dot-in/.stop-in), and
            * the tiny #rec-mini clock that shows under the icon when the
            * rail is collapsed. A LIVE chip here once broke the collapsed
            * layout — the chip belongs to restreaming, not to REC. */}
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <circle className="ring" cx="12" cy="12" r="7.5" />
              <circle className="dot-in" cx="12" cy="12" r="3" />
              <rect className="stop-in" x="8.8" y="8.8" width="6.4" height="6.4" rx="1" />
            </svg>
          </span>
          <span className="lb">{t('label.rec')}</span>
          <span id="rec-mini">{props.recClock}</span>
        </button>
        <button
          id="btn-rtmp"
          className={`row${d.restreaming ? ' live' : ''}`}
          aria-pressed={d.restreaming}
          onClick={props.onStream}
          title={t('toolbar.rtmp')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <path d="M4.5 18.5v-4a7.5 7.5 0 0 1 15 0v4" />
              <circle cx="12" cy="18" r="1.6" />
              <path d="M8.2 18.5a3.8 3.8 0 0 1 7.6 0" />
            </svg>
          </span>
          <span className="lb">{t('label.rtmp')}</span>
          {d.restreaming ? <span className="lb live-chip">{t('chip.live')}</span> : null}
        </button>
      </div>

      <div className="sd-sep" />
      <div className="sd-group x-only">{t('group.session')}</div>
      <div className="sd-rows">
        <button
          id="btn-voice"
          className={`row${d.voiceLive ? ' live' : ''}`}
          aria-pressed={d.voiceLive}
          onClick={props.onVoice}
          title={t('toolbar.voice')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <circle cx="9" cy="9" r="3.2" />
              <path d="M3.5 19c.8-3 2.9-4.5 5.5-4.5s4.7 1.5 5.5 4.5" />
              <path d="M16.5 5.5a4.5 4.5 0 0 1 0 7M18.8 3.2a7.8 7.8 0 0 1 0 11.6" />
            </svg>
          </span>
          <span className="lb">{t('label.voice')}</span>
          {d.voiceLive ? (
            <span className="lb live-chip">
              {d.voicePeers.length > 0 ? String(d.voicePeers.length) : t('chip.live')}
            </span>
          ) : null}
        </button>
        <button id="btn-files" className="row" onClick={props.onFiles} title={t('toolbar.files')}>
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <path d="M3.5 6.5h6l2 2.5h9v9.5h-17z" />
            </svg>
          </span>
          <span className="lb">{t('label.files')}</span>
        </button>
        <button
          id="btn-audio"
          className="row"
          aria-pressed={!props.muted}
          onClick={props.onAudio}
          title={t('toolbar.audio')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <path d="M4 9v6h4l5 4V5L8 9H4z" />
              {props.muted ? <path d="M16 8l5 8M21 8l-5 8" /> : <path d="M16.5 8.5a5 5 0 0 1 0 7" />}
            </svg>
          </span>
          <span className="lb">{t('label.audio')}</span>
        </button>
        <button
          id="btn-settings"
          className="row"
          onClick={props.onSettings}
          title={t('toolbar.settings')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <circle cx="12" cy="12" r="3" />
              <path d="M12 4v2.2M12 17.8V20M4 12h2.2M17.8 12H20M6.3 6.3l1.6 1.6M16.1 16.1l1.6 1.6M17.7 6.3l-1.6 1.6M7.9 16.1l-1.6 1.6" />
            </svg>
          </span>
          <span className="lb">{t('label.settings')}</span>
        </button>
        <button
          id="btn-stats"
          className="row"
          aria-expanded={props.statsOpen}
          onClick={props.onStats}
          title={t('toolbar.stats')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <path d="M4.5 19.5V13M9.5 19.5V7.5M14.5 19.5V11M19.5 19.5V4.5" />
            </svg>
          </span>
          <span className="lb">{t('label.stats')}</span>
        </button>
        <button
          id="btn-quality"
          className="row"
          aria-disabled={!yours}
          onClick={props.onQuality}
          title={t('toolbar.quality')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <path d="M5.5 17.5a7.5 7.5 0 1 1 13 0" />
              <path d="M12 13.5l3.8-3.8" />
              <circle cx="12" cy="14" r="1.1" />
            </svg>
          </span>
          <span className="lb">{t('label.quality')}</span>
          <span className="lb" id="quality-code">
            {t(`quality.chip.${d.quality.mode}`)}
          </span>
        </button>
        {props.qualityOpen ? (
          <div className="sd-pop floating" role="menu" style={{ position: 'static', minWidth: 0 }}>
            {(['auto', 'media', 'high'] as const).map((mode) => (
              <button
                key={mode}
                role="menuitemradio"
                aria-checked={d.quality.mode === mode}
                onClick={() => props.onPickQuality(mode)}
              >
                <span>{t(`quality.${mode}`)}</span>
              </button>
            ))}
          </div>
        ) : null}
      </div>

      <div className="sd-sep" />
      <div className="sd-group x-only">{t('group.viewer')}</div>
      <div className="sd-rows">
        <button
          id="btn-kb"
          className="row"
          aria-pressed={props.kbOpen}
          aria-disabled={!yours}
          onClick={props.onKb}
          title={t('toolbar.kb')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <rect x="3" y="7" width="18" height="11" rx="2" />
              <path d="M6.5 10.5h1M10 10.5h1M13.5 10.5h1M17 10.5h1M6.5 13.5h1M10 13.5h1M13.5 13.5h1M17 13.5h1M8 16h8" />
            </svg>
          </span>
          <span className="lb">{t('label.kb')}</span>
        </button>
        <button
          id="btn-fs"
          className="row"
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
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />
            </svg>
          </span>
          <span className="lb">{t('label.fullscreen')}</span>
        </button>
        <button
          id="btn-lang"
          className="row"
          aria-expanded={props.langOpen}
          onClick={props.onLang}
          title={t('toolbar.language')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <circle cx="12" cy="12" r="8.2" />
              <ellipse cx="12" cy="12" rx="3.6" ry="8.2" />
              <path d="M4 12h16" />
            </svg>
          </span>
          <span className="lb">{t('label.language')}</span>
          <span className="lb" id="lang-code">
            {LANGUAGES.find((l) => l.code === props.langCode)?.chip ??
              props.langCode.toUpperCase()}
          </span>
        </button>
        {props.langOpen ? (
          <div className="sd-pop floating" role="menu" style={{ position: 'static', minWidth: 0 }}>
            {LANGUAGES.map((l) => (
              <button
                key={l.code}
                role="menuitemradio"
                aria-checked={props.langCode === l.code}
                onClick={() => props.onPickLang(l.code)}
              >
                <span>{l.name}</span>
              </button>
            ))}
          </div>
        ) : null}
        <button
          id="btn-help"
          className="row"
          onClick={() =>
            window.open(`${DOCS_URL}?lang=${encodeURIComponent(props.langCode)}`, '_blank')
          }
          title={t('toolbar.help')}
        >
          <span className="ic">
            <svg viewBox="0 0 24 24">
              <circle cx="12" cy="12" r="8.2" />
              <path d="M9.6 9.4a2.5 2.5 0 1 1 3.2 2.8c-.6.3-.8.8-.8 1.4v.5" />
              <path d="M12 17.1v.1" strokeWidth="2.4" />
            </svg>
          </span>
          <span className="lb">{t('label.help')}</span>
        </button>
        {props.onLogout ? (
          <button
            id="btn-logout"
            className="row"
            onClick={props.onLogout}
            title={t('toolbar.logout')}
          >
            <span className="ic">
              <svg viewBox="0 0 24 24">
                <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
                <path d="M16 17l5-5-5-5M21 12H9" />
              </svg>
            </span>
            <span className="lb">{t('label.logout')}</span>
          </button>
        ) : null}
      </div>

      <div id="rail-foot">
        <button
          id="toolbar-collapse"
          title={t('toolbar.collapse')}
          onClick={props.onCollapse}
        >
          <svg className="chev-out" viewBox="0 0 24 24">
            <path d="M15 6l-6 6 6 6" />
          </svg>
          <svg className="chev-in" viewBox="0 0 24 24">
            <path d="M9 6l6 6-6 6" />
          </svg>
        </button>
        <button id="toolbar-hide" title={t('toolbar.hide')} onClick={props.onHide}>
          º
        </button>
      </div>
    </div>
  )
}
