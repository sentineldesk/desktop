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

/* The standalone desktop client: two modes on one engine.
 *
 * DESKTOP is the default — the remote screen filling everything under a 45px
 * bar that now carries what the floating rail used to. AGENT is the same
 * session turned around: the conversation full screen, the desktop as a card
 * inside it. The switch in the bar's centre is the one element both modes
 * share, and the reason they read as one application.
 *
 * The engine did not move: useDesktopStream still owns the WebRTC session,
 * and every agent byte still rides the DataChannel (there is deliberately no
 * HTTP API to fall back to). The ONE video element travels between the modes;
 * a callback ref rebinds the stream and the input pipe wherever it lands, so
 * switching modes never renegotiates anything.
 *
 * The agent workspace itself is OpenBot's shell, vendored and re-pointed at
 * the DataChannel — see agent/AgentWorkspace.tsx. */

import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AgentWorkspace } from './agent/AgentWorkspace'
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
import { useDraggable } from './ui/useDraggable'
import { TopBar, type ShellMode } from './ui/TopBar'
import { AboutDialog } from './ui/AboutDialog'

/* ---- the logo, shared by the boot screen and the login ------------------- */

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
  const [loginError, setLoginError] = useState(false)
  useEffect(() => {
    if (loginFailed) {
      setLoginError(true)
      sessionStorage.removeItem('sentineldesk_token')
      setAuth(null)
    }
  }, [loginFailed])

  /* ---- the mode --------------------------------------------------------- */

  const [mode, setMode] = useState<ShellMode>(
    () => (localStorage.getItem('sentineldesk.mode') as ShellMode) || 'desktop',
  )
  useEffect(() => {
    localStorage.setItem('sentineldesk.mode', mode)
  }, [mode])
  /* Agent mode holds deliveries: the workspace previews them in its canvas
   * and the download is a button there. Desktop keeps the tray behaviour. */
  useEffect(() => {
    desktop.setDeliveryHold(mode === 'agent')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode])

  /* The canvas expanded inside agent mode: desktop's layout, borrowed. */
  const [expanded, setExpanded] = useState(false)
  useEffect(() => {
    if (mode !== 'agent') setExpanded(false)
  }, [mode])

  /* The dot on the Agent segment: a run finished while the desktop was on
   * screen. Cleared the moment the person looks. */
  const [agentFresh, setAgentFresh] = useState(false)
  const busyBefore = useRef('')
  useEffect(() => {
    if (busyBefore.current !== '' && desktop.chat.busy === '' && mode === 'desktop') {
      setAgentFresh(true)
    }
    busyBefore.current = desktop.chat.busy
  }, [desktop.chat.busy, mode])
  useEffect(() => {
    if (mode === 'agent') setAgentFresh(false)
  }, [mode])

  /* ---- the one video element -------------------------------------------- */

  /* WHY the audio starts off: the autoplay policy refuses unmuted sound
   * before a gesture, so playback begins muted and the FIRST CLICK lifts it;
   * a person choosing Audio off means something else entirely. `muted` is the
   * person's choice, `policyMuted` is the browser's hold. */
  const [muted, setMuted] = useState(false)
  const [policyMuted, setPolicyMuted] = useState(false)
  const mutedRef = useRef(muted)
  mutedRef.current = muted

  /* The element travels between modes, so it REMOUNTS. A callback ref feeds
   * state, and the effects below rebind the stream and the input pipe on the
   * new node — the WebRTC session itself never notices. */
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const [videoEl, setVideoEl] = useState<HTMLVideoElement | null>(null)
  const bindVideo = useCallback((el: HTMLVideoElement | null) => {
    videoRef.current = el
    setVideoEl(el)
  }, [])

  useEffect(() => {
    if (!videoEl || !desktop.stream) return
    videoEl.srcObject = desktop.stream
    return playOrRescue(videoEl, () => !mutedRef.current, setPolicyMuted)
  }, [videoEl, desktop.stream])

  const controlRef = useRef(desktop.control)
  controlRef.current = desktop.control
  useEffect(() => {
    if (!videoEl || desktop.state !== 'live') return
    const offMouse = attachDesktopInput(videoEl, desktop, () => controlRef.current.yours)
    const offTouch = attachTouchInput(videoEl, desktop, () => controlRef.current.yours)
    return () => {
      offMouse()
      offTouch()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [videoEl, desktop.state, desktop.inputReady])

  /* Drag and drop. Deliberately NOT gated on the controls: handing a file to
   * the room is provisioning, not driving. The server still owns the rule. */
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
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [aboutOpen, setAboutOpen] = useState(false)
  const stats = useLiveStats(statsOpen, desktop, videoRef)
  /* The stats panel is for watching WHILE doing something else, so it can be
   * parked anywhere and remembers the spot. */
  const statsDrag = useDraggable('sentineldesk.statsPos')

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

  const shot = () => {
    desktop.sendInput({ t: 'capture', action: 'shot' })
  }
  const rec = () => {
    desktop.sendInput({
      t: 'capture',
      action: desktop.recording ? 'rec_stop' : 'rec_start',
    })
  }
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
  /* Closing Settings applies the choices to whatever is LIVE: a changed
   * device re-acquires by toggling off and on again. */
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

  const langCode = i18n.language || 'en'
  const live = desktop.state === 'live'
  const showLogin = authRequired === true && auth === null
  const showStatus = !live && !showLogin

  const fmt = (s: number) =>
    `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`

  const logout = authRequired
    ? () => {
        sessionStorage.removeItem('sentineldesk_token')
        setAuth(null)
      }
    : undefined

  /* The X cursor's real shape rides the wire and lands here as a CSS cursor.
   * Only while driving: a resize arrow over a desktop you cannot resize is a
   * promise. */
  const screenEl = (
    <>
      <video
        id="screen"
        ref={bindVideo}
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
    </>
  )

  return (
    <div className="shell">
      {live ? (
        <TopBar
          desktop={desktop}
          mode={mode}
          onMode={setMode}
          agentFresh={agentFresh}
          muted={muted || policyMuted}
          onAudio={() => {
            /* The click targets the opposite of what the icon showed. The
             * same click, as a gesture, already lifted a policy mute before
             * this handler ran — so compute from the rendered state. */
            setMuted(!(muted || policyMuted))
            setPolicyMuted(false)
          }}
          onMic={mic}
          onVoice={voice}
          onShot={shot}
          onRec={rec}
          recClock={fmt(recSeconds)}
          onFiles={() => setFilesOpen(true)}
          onStream={() => setStreamOpen(true)}
          onStats={() => setStatsOpen((v) => !v)}
          statsOpen={statsOpen}
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
          onPickQuality={(q) => desktop.setQuality(q)}
          onSettings={() => setSettingsOpen(true)}
          onAbout={() => setAboutOpen(true)}
          onLogout={logout}
          langCode={langCode}
          name={name || t('ws.you')}
        />
      ) : null}

      <div className="shell-main">
        {live && mode === 'agent' ? (
          <AgentWorkspace
            desktop={desktop}
            screen={
              expanded ? (
                screenEl
              ) : (
                <div className="pointer-events-none absolute inset-0">{screenEl}</div>
              )
            }
            name={name || t('ws.you')}
            expanded={expanded}
            onExpand={setExpanded}
            onSettings={() => setSettingsOpen(true)}
            onAbout={() => setAboutOpen(true)}
            onLogout={logout}
            muted={muted || policyMuted}
            onAudio={() => {
              /* Same gesture as the top bar's Audio: compute from what the
               * icon showed, and a click that lifts a policy mute is a
               * choice to hear. */
              setMuted(!(muted || policyMuted))
              setPolicyMuted(false)
            }}
          />
        ) : (
          <div id="stage-desktop">{live || showStatus ? screenEl : null}</div>
        )}

        {showStatus ? (
          <div id="status" className="visible">
            <div className="brand">
              <Brand />
              <span>SentinelDesk</span>
            </div>
            <div className="scan" aria-hidden="true" />
            <div id="status-text">
              {desktop.state === 'failed'
                ? t(desktop.error ?? 'status.error')
                : t('status.connecting')}
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
            error={loginError ? t('login.failed') : ''}
            onSubmit={(user, pass) => {
              setLoginError(false)
              setAuth({ user, pass })
            }}
          />
        ) : null}

        {live && mode === 'desktop' && !desktop.control.yours ? (
          <div id="watching" className="show" role="status">
            {/* The strings carry a <b> around the load-bearing word — they
             * are our own files, not user input. */}
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

        {mode === 'desktop' && statsOpen && stats ? (
          <div
            id="stats"
            className="show"
            role="status"
            ref={statsDrag.ref}
            style={statsDrag.style}
          >
            <div
              className="st-head"
              onPointerDown={statsDrag.onGrab}
              onDoubleClick={statsDrag.onHome}
            >
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

        {desktop.recording && mode === 'desktop' ? (
          <div id="rec-badge">
            <span className="dot" />
            <span id="rec-time">{fmt(recSeconds)}</span>
          </div>
        ) : null}

        {mode === 'desktop' && filesOpen ? (
          <FilesDialog desktop={desktop} onClose={() => setFilesOpen(false)} />
        ) : null}
        {mode === 'desktop' && streamOpen ? (
          <StreamDialog desktop={desktop} onClose={() => setStreamOpen(false)} />
        ) : null}

        {settingsOpen ? (
          <MediaSettings
            onClose={() => setSettingsOpen(false)}
            onChanged={settingsChanged}
          />
        ) : null}

        <AboutDialog
          open={aboutOpen}
          onOpenChange={setAboutOpen}
          version={desktop.serverVersion}
        />

        {live && mode === 'desktop' && kbOpen && desktop.control.yours ? (
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
          deliveries={mode === 'desktop' ? desktop.deliveries : []}
          onSave={desktop.saveDelivery}
          onDismiss={desktop.dismissDelivery}
        />

        {toast ? (
          <div id="toast" className={`show${toast.err ? ' err' : ''}`} role="status">
            {toast.msg}
          </div>
        ) : null}
      </div>
    </div>
  )
}

/* ---- login --------------------------------------------------------------- */

/* The console door. No photograph and nothing to wait for: the engineering
 * grid the boot screen already wears is the ground, so login and boot are the
 * same surface seen a second apart — and the first screen a person sees never
 * waits on an asset. The focus ring is the system's cyan, not the phosphor:
 * green says who is driving, and at the door there is no session to drive. */
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
  return (
    <div id="login" className="visible">
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
        <div className="subtitle">{t('ws.loginSubtitle')}</div>
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
        <div className={`error${error ? ' show' : ''}`} id="login-error" role="alert">
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <circle cx="12" cy="12" r="7.5" />
            <path d="M12 8v4.6M12 15.8v.1" />
          </svg>
          {error}
        </div>
        <button type="submit" id="login-btn">
          {t('login.submit')}
        </button>
      </form>
      <div className="foot">
        <span>SentinelDesk</span>
        <button
          type="button"
          onClick={() => {
            /* Cycle rather than a menu: the door is one screen, and the four
             * entries fit a tap each better than a popover does. */
            const at = LANGUAGES.findIndex((l) => l.code === i18nLang())
            setLanguage(LANGUAGES[(at + 1) % LANGUAGES.length].code)
          }}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <circle cx="12" cy="12" r="7.5" />
            <path d="M4.5 12h15M12 4.5c2 2.4 2 12.6 0 15M12 4.5c-2 2.4-2 12.6 0 15" />
          </svg>
          {LANGUAGES.find((l) => l.code === i18nLang())?.name ?? i18nLang()}
        </button>
      </div>
    </div>
  )
}

/* The current language tag, read fresh so the cycle button always moves. */
function i18nLang(): string {
  return (
    (document.documentElement.getAttribute('lang') || '') ||
    localStorage.getItem('i18nextLng') ||
    'en'
  )
}
