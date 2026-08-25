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

/* The room's actual desktop, arriving as pixels.
 *
 * This is the second peer connection a client holds, and it goes somewhere
 * different from the first: the front desk's DataChannel carries commands, and
 * this goes DIRECTLY to the room's own runtime. That is the arrangement
 * docs/workrooms-design.md §4.8 calls the single biggest reason many rooms does
 * not become everything slow — the control plane issues a ticket and then stays
 * out of the path the video takes.
 *
 * Three things had to exist before this could: the ticket (ADR-006), which gets
 * a guest through a door whose credential they were never given; presence,
 * which is what makes them entitled to ask; and the room's runtime already
 * knowing how to verify a token the front desk signed.
 *
 * THE PROTOCOL IS THE RUNTIME'S, not the front desk's. They are two different
 * doors with two different frames, and this speaks the older one: authenticate,
 * take the config, answer the offer. It is transcribed from
 * internal/webui/assets/client.js, which is the authority — that client has
 * been driving this handshake since before the front desk existed, and a
 * paraphrase of it would be a second dialect nobody tests.
 */

import { useCallback, useEffect, useRef, useState } from 'react'

import { isAgentMessage, useAgentChat, type AgentChatApi } from './agentChat'
import { isConsoleMessage, useAgentConsole, type AgentConsoleApi } from './agentConsole'
import { applySpeaker, audioConstraints, voiceConstraints } from './mediaPrefs'

/* Standalone credentials for the runtime's own door: a user/password pair
 * (the login form), a session token (a resumed session), or 'open' when the
 * desktop runs with no authentication at all. null means "do not connect
 * yet" — the login screen is still collecting the answer. */
export type StandaloneAuth =
  | { user: string; pass: string }
  | { token: string }
  | 'open'

export type DesktopState =
  /** No room, or the room is not running: there is nothing to connect to. */
  | 'idle'
  /** Asking the front desk for a ticket, then shaking hands with the room. */
  | 'connecting'
  /** Pixels are arriving. */
  | 'live'
  /** It did not work, and `error` says which part. */
  | 'failed'

/* Who is driving the desktop, as the RUNTIME sees it.
 *
 * This is the runtime's own arbitration — one controller per room, claimed
 * and never inherited — not the front desk's lease table. The two answer the
 * same question at different ranges: the runtime decides whose events reach
 * X right now, the front desk decides who may ask at all. This hook carries
 * the runtime's answer because that is the plane the input travels on. */
export interface DesktopControl {
  /** This connection holds the controls. */
  readonly yours: boolean
  /** Whoever does, by name — null when the controls are free. */
  readonly holder: string | null
  /** The holder is an AI agent, which is worth saying out loud: a pointer
   * moving on its own reads as a glitch until you know a model is driving. */
  readonly holderIsAgent: boolean
}

const NOBODY: DesktopControl = { yours: false, holder: null, holderIsAgent: false }

/* One person (or the agent) in the room, as presence lists them. */
export interface RoomMember {
  readonly id: string
  readonly name: string
  readonly controller: boolean
  readonly agent: boolean
  /** The member's ink, CSS hex — the same colour their pointer wears on the
   * desktop, dealt by the room (yellow, cyan, magenta, key; violet is the
   * agent's and never a person's). */
  readonly color: string
}

/* One leg of the voice mesh: the pair connection, the element its audio
 * plays through, and the flags perfect negotiation needs. */
interface VoicePeer {
  pc: RTCPeerConnection
  audio: HTMLAudioElement
  polite: boolean
  makingOffer: boolean
}

/* One outbound restream, as the runtime reports it. The URL arrives already
 * REDACTED — the runtime strips the key before the list leaves the machine —
 * so this is safe to show and useless to copy, which is exactly right. */
export interface RestreamInfo {
  readonly id: string
  readonly platform: string
  readonly url: string
  readonly seconds: number
}

/* A file the RUNTIME pushed at this session — a finished recording, a
 * screenshot — offered by id on the files channel and pulled through it.
 * `ready` waits in the tray for a click (big files); `saving` has chunks in
 * flight; `done` and `failed` close the story, one quietly, one staying put
 * until read. */
export interface DeskDelivery {
  readonly id: string
  readonly name: string
  readonly size: number
  readonly status: 'ready' | 'saving' | 'done' | 'failed'
  readonly bytes: number
}

/* A question the RUNTIME put to the room, waiting on whoever answers first.
 *
 * Two things arrive this way: the agent's own ask_human, and — under
 * MCP_POLICY=approve — the server's "the agent wants to run X, allow it?"
 * elicitation prompt. The panel cannot tell them apart and does not need to:
 * both are a text, optional buttons, and a deadline, and anyone present may
 * answer (same rule as a control request — they all got in with the same
 * credential). Until this existed the panel silently ignored the message, so
 * a workroom agent asking a question waited out its timeout against a screen
 * that showed nothing. */
export interface AgentQuestion {
  readonly id: number
  readonly text: string
  readonly options: readonly string[]
  readonly seconds: number
  /** The answer is a secret: mask the field, and expect no options. */
  readonly secret: boolean
}

/* The agent asking the ROOM for the controls, waiting on whoever answers
 * first. Not a question the agent composed: the runtime owns every word of
 * it (see AskForControl in internal/stream/room.go), which is exactly why it
 * arrives as its own message instead of riding `question` — an agent that
 * could write the text of the dialog granting it the desktop could write
 * anything at all into it.
 *
 * The runtime broadcasts it and then WAITS; silence is a refusal. Until this
 * existed the panel dropped the message on the floor, so every
 * request_control sat out its timeout against a screen that showed nothing
 * and answered "nobody answered in time" to an agent whose person was
 * looking straight at the desktop. */
export interface ControlRequest {
  readonly id: number
  /** Who is asking, by the name the room knows them under. */
  readonly who: string
  /** How long the runtime will wait before taking silence for a no. */
  readonly seconds: number
}

export interface Desktop {
  readonly state: DesktopState
  readonly stream: MediaStream | null
  /** An i18n key, so the screen says it in the reader's language. */
  readonly error: string | null
  readonly control: DesktopControl
  /** The input channel is open — the moment sendInput stops dropping.
   * Load-bearing for anyone syncing the §3.4 lease to the runtime seat:
   * `state` goes 'live' on the VIDEO track, which lands before the
   * server-opened DataChannel finishes its SCTP handshake, and a
   * take_control fired into that gap evaporates with nothing retrying it.
   * That was a real reload bug: the lease said yours, the desktop never
   * claimed, and the only cure was releasing and asking again. */
  readonly inputReady: boolean
  /** The build the server said it is, from the config frame; '' until then. */
  readonly serverVersion: string
  /** The RUNTIME is recording (server-side MP4). */
  readonly recording: boolean
  /** Epoch ms when the current recording began; 0 while not recording. */
  readonly recordingSince: number
  /** At least one outbound restream is running. */
  readonly restreaming: boolean
  /** The running restreams themselves, ids included — what a stop button
   * needs to name its target. */
  readonly restreams: readonly RestreamInfo[]
  /** The runtime's answer when a restream could not start — the server's own
   * sentence ("unsupported destination …"), or the token "needControl". Null
   * when the last answer was clean. This surfaced the hard way: a typo'd
   * udp:// scheme was refused with a perfectly clear message that the panel
   * swallowed, and the person went looking for a broken machine instead. */
  readonly restreamError: string | null
  /** Forget the last refusal — called when a new attempt goes out, so the
   * old sentence is not read as the new attempt's verdict. */
  clearRestreamError(): void
  /** Whether this session's encoder can feed a restream at all (H.264). */
  readonly restreamAble: boolean
  /** The stream's quality position — the room's, not this viewer's: the
   * screen is encoded once and fanned out, so the dial adjusts what that one
   * encode costs. `fps` is the cap currently in force (Auto moves it). */
  readonly quality: { mode: 'auto' | 'media' | 'high'; fps: number }
  /** The runtime's refusal of the last quality change ("needControl", or its
   * own sentence). Null when the last answer was clean. */
  readonly qualityError: string | null
  /** Choose the room's quality position. The runtime gates it on the
   * controls (or a privileged ticket) and answers with the state either
   * way, so the dial always ends up showing the truth. */
  setQuality(mode: 'auto' | 'media' | 'high'): void
  /** The real X cursor as a CSS value, pushed by the runtime — resize
   * arrows, text beams — or null before the first one arrives. */
  readonly cursor: string | null
  /** The real X cursor's POSITION in desktop pixels, broadcast by the
   * runtime. The live video deliberately carries no pointer (the driver
   * draws their own locally, at zero latency), so this is how a VIEWER
   * sees where whoever is driving — person or agent — actually is. */
  readonly pointer: { x: number; y: number } | null
  /** Try again, for a person who would rather press a button than reload. */
  retry(): void
  /** Claim the controls, or hand them back when they are yours. */
  toggleControl(): void
  /** One input event, in the runtime's own vocabulary ({t:'mm',x,y} …).
   * Dropped silently when the channel is not open — input is a stream, and
   * queueing stale mouse moves for a desktop that just reconnected would
   * replay a ghost. */
  sendInput(event: Record<string, unknown>): void
  /** A snapshot of the peer connection's stats, or null before there is
   * one. The panel polls this while its stats view is open. */
  getStats(): Promise<RTCStatsReport | null>
  /** Files the runtime pushed at this session — finished recordings,
   * screenshots — offered and pulled over the files channel, never HTTP.
   * Small ones save themselves; big ones wait here for saveDelivery. */
  readonly deliveries: readonly DeskDelivery[]
  /** Pull one offered delivery and save it. From a click, a big file streams
   * straight to disk where the browser allows a save dialog; everything else
   * assembles in memory and downloads itself. Retries a failed one. */
  saveDelivery(id: string): void
  /** Drop one delivery from the tray. The file stays on the desktop. */
  dismissDelivery(id: string): void
  /** Pull a delivery's bytes into a Blob for an inline preview. The offer
   * stays valid server-side, so the caller keeps the Blob and later saves
   * from it without pulling twice. */
  previewDelivery(id: string): Promise<Blob>
  /** While held, deliveries stop saving themselves — the agent workspace
   * previews them in the canvas and the download becomes a button there. */
  setDeliveryHold(hold: boolean): void
  /** The agent chat panel: availability, transcript, history and the three
   * things a person can do with it. See agentChat.ts.
   *
   * One object rather than a dozen fields spread through this interface,
   * because it is a feature somebody either uses or does not — and a panel that
   * is not open should not be able to reach half of it by accident. */
  readonly chat: AgentChatApi
  readonly console: AgentConsoleApi
  /** The runtime's open question to the room, or null. See AgentQuestion. */
  readonly question: AgentQuestion | null
  /** Answer the open question. Clears it locally at once; the runtime's own
   * question_done still arrives and clears it for everyone else. */
  answerQuestion(answer: string): void
  /** The agent's open request for the controls, or null. See ControlRequest. */
  readonly controlRequest: ControlRequest | null
  /** Allow or refuse it. Clears the card locally at once; the runtime's own
   * control_request_done still arrives and clears it for everyone else. */
  answerControlRequest(granted: boolean): void
  /** Everyone in the room, as presence reports them — the collaborators
   * view and the voice mesh both read from here. */
  readonly members: readonly RoomMember[]
  /** This connection's own member id, from the same presence. */
  readonly myId: string
  /** The voice conference: THIS browser is publishing and hearing. */
  readonly voiceLive: boolean
  /** Member ids whose voice link is connected right now. */
  readonly voicePeers: readonly string[]
  /** Join or leave the peer-to-peer voice conference. The audio flows
   * browser to browser — it never touches the server, PulseAudio, a
   * recording or a restream; the server only relays sealed envelopes. */
  toggleVoice(): Promise<void>
  /** The AGENT is paused, room-wide — presence carries it. */
  readonly paused: boolean
  /** Who paused it, for the resume button's sentence. */
  readonly pausedBy: string
  /** The browser's microphone is live INTO the desktop (the virtual mic
   * every app in the room can record from). */
  readonly micLive: boolean
  /** Toggle the microphone into the desktop. Needs the controls to turn ON
   * (only the controller may publish); turning OFF is always allowed. Pass a
   * deviceId to publish a specific microphone. */
  toggleMic(deviceId?: string): Promise<void>
  /** Offer the LOCAL clipboard to the desktop, deduplicated. Resolves once
   * the value is on the wire (or once it is clear nothing will be sent), so
   * a caller can order a paste keystroke AFTER it. */
  pushClipboard(): Promise<void>
  /** Send one file to the room's desktop over the dedicated files channel —
   * chunked, any size, progress in server-confirmed bytes. Rejects with the
   * server's own sentence when refused (no controls, name taken…). Pass a
   * dir to aim somewhere specific; empty means the Desktop folder. */
  uploadFile(
    file: File,
    onProgress: (bytes: number) => void,
    dir?: string,
  ): Promise<void>
  /** List a directory on the desktop, over the same channel. */
  filesList(dir: string): Promise<FsListing>
  /** mkdir / delete / rename — the F-key operations. Needs the controls or
   * the administrator's ticket; the runtime enforces. */
  filesOp(op: 'mkdir' | 'delete' | 'rename', path: string, to?: string): Promise<void>
  /** Pull one file off the desktop, chunked, assembled into a Blob. */
  downloadFile(
    path: string,
    onProgress: (bytes: number, total: number) => void,
  ): Promise<{ name: string; blob: Blob }>
}

export interface FsEntry {
  readonly name: string
  readonly type: 'dir' | 'file' | 'link'
  readonly size: number
  readonly modified: string
}

export interface FsListing {
  readonly path: string
  readonly parent: string
  readonly entries: readonly FsEntry[]
  /** How many entries did not fit in the frame — said, never hidden. */
  readonly truncated: number
}

/* How long the whole handshake gets before it is called failed.
 *
 * A desktop that is booting answers its HTTP port before it can negotiate, so
 * this has to outlast the gap — and it has to END, because the alternative is a
 * black rectangle that is indistinguishable from a room where nothing is
 * happening. Silence is the one outcome this must never produce. */
const HANDSHAKE_TIMEOUT_MS = 30_000

/* Deliveries at or under this size save themselves the moment they arrive,
 * assembled in memory — the zero-click experience screenshots always had.
 * Above it the tray waits for a click, because only a click (transient user
 * activation) lets the browser show a real save dialog whose stream goes to
 * disk instead of ballooning in a tab's memory. */
const AUTO_SAVE_LIMIT = 256 * 1024 * 1024


/* Whether this browser takes image-set() inside cursor, and under which
 * spelling. Probed once: a cursor list with one unparseable entry is dropped
 * WHOLE, so guessing wrong costs the pointer entirely. */
const IMAGE_SET = (() => {
  try {
    for (const fn of ['image-set', '-webkit-image-set']) {
      if (CSS.supports('cursor', `${fn}(url("data:,") 2x) 0 0, default`)) return fn
    }
  } catch {
    /* no CSS.supports: ancient — plain bitmaps only */
  }
  return ''
})()
const HIDPI_CURSORS = IMAGE_SET !== ''

export function useDesktopStream(
  auth: StandaloneAuth | null,
  name: string,
  onToken?: (token: string) => void,
): Desktop {
  /* The member's display name, through a ref: it only matters at the moment
   * the auth frame goes out. */
  const nameRef = useRef(name)
  nameRef.current = name
  const onTokenRef = useRef(onToken)
  onTokenRef.current = onToken

  const [state, setState] = useState<DesktopState>('idle')
  const [stream, setStream] = useState<MediaStream | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)
  const [control, setControl] = useState<DesktopControl>(NOBODY)
  const controlRef = useRef(control)
  controlRef.current = control
  const [inputReady, setInputReady] = useState(false)
  const [cursor, setCursor] = useState<string | null>(null)
  const [pointer, setPointer] = useState<{ x: number; y: number } | null>(null)
  const [recording, setRecording] = useState(false)
  const [serverVersion, setServerVersion] = useState('')
  const [recordingSince, setRecordingSince] = useState(0)
  const [restreams, setRestreams] = useState<readonly RestreamInfo[]>([])
  const [restreamError, setRestreamError] = useState<string | null>(null)
  const [restreamAble, setRestreamAble] = useState(true)
  const [quality, setQualityState] = useState<{
    mode: 'auto' | 'media' | 'high'
    fps: number
  }>({ mode: 'auto', fps: 0 })
  const [qualityError, setQualityError] = useState<string | null>(null)
  const [deliveries, setDeliveries] = useState<readonly DeskDelivery[]>([])
  const [question, setQuestion] = useState<AgentQuestion | null>(null)
  const [controlRequest, setControlRequest] = useState<ControlRequest | null>(null)
  const [micLive, setMicLive] = useState(false)
  const [members, setMembers] = useState<readonly RoomMember[]>([])
  const [myId, setMyId] = useState('')
  const [voiceLive, setVoiceLive] = useState(false)
  const [voicePeers, setVoicePeers] = useState<readonly string[]>([])
  const voiceRef = useRef<{ stream: MediaStream | null; peers: Map<string, VoicePeer> }>({
    stream: null,
    peers: new Map(),
  })
  const membersRef = useRef<readonly RoomMember[]>([])
  const myIdRef = useRef('')
  const iceServersRef = useRef<RTCIceServer[]>([])
  const [paused, setPaused] = useState(false)
  const [pausedBy, setPausedBy] = useState('')
  const micStreamRef = useRef<MediaStream | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  /* The input channel and the current control state, as refs, because the
   * senders below are handed to DOM listeners that live across renders. */
  const channelRef = useRef<RTCDataChannel | null>(null)
  /* The last clipboard value that crossed the wire, in EITHER direction —
   * one ref for both, because its whole job is stopping the echo: what the
   * desktop just sent must not be sent back on the next focus, and what we
   * just pushed must not come back as news. */
  const lastClipRef = useRef('')
  const pcRef = useRef<RTCPeerConnection | null>(null)
  /** name+size per offered delivery id — what saveDelivery needs after the
   * push that announced it has long been handled. */
  const deliveryMetaRef = useRef(new Map<string, { name: string; size: number }>())
  const yoursRef = useRef(false)
  yoursRef.current = control.yours

  const retry = useCallback(() => setAttempt((n) => n + 1), [])

  const sendInput = useCallback((event: Record<string, unknown>) => {
    const channel = channelRef.current
    if (!channel || channel.readyState !== 'open') return
    channel.send(JSON.stringify(event))
  }, [])

  /* The chat panel's own state. It rides THIS DataChannel — the browser has no
   * idea there is an agent process behind the daemon, and sends `agent_say`
   * exactly the way it sends a mouse move. Everything about how the desktop
   * reaches the runtime is the daemon's business, which is what lets the agent
   * be absent without a line of this file caring. */
  const chat = useAgentChat(sendInput)
  /* Read through a ref by the DataChannel handler below, which is installed
   * once per connection and would otherwise keep calling into the render it was
   * created in. The same pattern questionRef uses, a few hundred lines down,
   * and for the same reason. */
  const chatRef = useRef(chat)
  chatRef.current = chat

  /* The console session — the agent's own terminal in a window. Held the same
   * way and for the same reason: handle() runs from a DataChannel callback that
   * closed over its render. */
  const console_ = useAgentConsole(sendInput)
  const consoleRef = useRef(console_)
  consoleRef.current = console_

  const toggleControl = useCallback(() => {
    sendInput({ t: yoursRef.current ? 'release_control' : 'take_control' })
  }, [sendInput])

  const setQuality = useCallback(
    (mode: 'auto' | 'media' | 'high') => {
      /* A new choice is not answered by the last one's refusal. */
      setQualityError(null)
      sendInput({ t: 'quality', mode })
    },
    [sendInput],
  )

  /* The viewer's half of the desktop's stream card: every couple of seconds
   * report what THIS client actually receives — fps, bitrate, round trip,
   * behind-live, loss — and the runtime merges every viewer's report with
   * its own sending side into the widget on the desktop (and a file the
   * agent can read). Telemetry about oneself, integers only, nothing anyone
   * must act on. */
  useEffect(() => {
    if (!inputReady) return
    let prevBytes = 0
    let prevTime = 0
    const timer = setInterval(() => {
      const pc = pcRef.current
      const channel = channelRef.current
      if (!pc || !channel || channel.readyState !== 'open') return
      void pc
        .getStats()
        .then((report) => {
          const out: Record<string, unknown> = { t: 'viewstats' }
          report.forEach((r) => {
            const s = r as unknown as Record<string, number | string>
            if (s.type === 'inbound-rtp' && s.kind === 'video') {
              const bytes = Number(s.bytesReceived ?? 0)
              const ts = Number(s.timestamp ?? 0)
              if (prevTime) {
                out.kbps = Math.max(
                  0,
                  Math.round(((bytes - prevBytes) * 8) / (ts - prevTime)),
                )
              }
              prevBytes = bytes
              prevTime = ts
              if (s.framesPerSecond !== undefined)
                out.fps = Math.round(Number(s.framesPerSecond))
              const emitted = Number(s.jitterBufferEmittedCount ?? 0)
              if (emitted)
                out.behind = Math.round(
                  (Number(s.jitterBufferDelay ?? 0) / emitted) * 1000,
                )
              const total =
                Number(s.packetsReceived ?? 0) + Number(s.packetsLost ?? 0)
              if (total)
                out.losspm = Math.round(
                  (Number(s.packetsLost ?? 0) / total) * 1000,
                )
            }
            if (
              s.type === 'candidate-pair' &&
              s.state === 'succeeded' &&
              s.currentRoundTripTime !== undefined
            )
              out.rtt = Math.round(Number(s.currentRoundTripTime) * 1000)
          })
          channel.send(JSON.stringify(out))
        })
        .catch(() => {})
    }, 1000)
    return () => clearInterval(timer)
  }, [inputReady])

  /* The files channel and its in-flight transfers. Uploads speak the
   * runtime's chunk protocol (internal/stream/transfers.go): init → chunk*
   * → done, each transfer's replies routed to its own handler by the ref we
   * chose or the id the server minted. A SEPARATE channel from input on
   * purpose — DataChannels are ordered, and a gigabyte queued behind a
   * mouse move would hold the mouse hostage. */
  const filesChRef = useRef<RTCDataChannel | null>(null)
  const upHandlersRef = useRef(
    new Map<string, (m: Record<string, unknown>) => void>(),
  )
  const upSeqRef = useRef(0)

  const uploadFile = useCallback(
    (
      file: File,
      onProgress: (bytes: number) => void,
      dir?: string,
    ): Promise<void> => {
      const ch = filesChRef.current
      if (!ch || ch.readyState !== 'open') {
        return Promise.reject(new Error('the desktop is not connected'))
      }
      const handlers = upHandlersRef.current
      const ref = `r${++upSeqRef.current}`
      return new Promise<void>((resolve, reject) => {
        let id = ''
        let chunkSize = 32 * 1024
        const cleanup = () => {
          handlers.delete(ref)
          if (id) handlers.delete(id)
        }
        const pump = async () => {
          try {
            let seq = 0
            for (let off = 0; off < file.size; off += chunkSize) {
              const buf = new Uint8Array(
                await file.slice(off, off + chunkSize).arrayBuffer(),
              )
              /* Back-pressure by bufferedAmount, not by waiting for acks:
               * the channel is ordered and the server checks seq, so
               * pipelining is safe — and it is the difference between
               * RTT-bound and wire-speed. */
              while (ch.bufferedAmount > 1_000_000) {
                await new Promise((r) => setTimeout(r, 15))
              }
              if (ch.readyState !== 'open') throw new Error('disconnected')
              ch.send(
                JSON.stringify({ t: 'up_chunk', id, seq: seq++, d: toB64(buf) }),
              )
            }
            ch.send(JSON.stringify({ t: 'up_done', id }))
          } catch (err) {
            cleanup()
            try {
              ch.send(JSON.stringify({ t: 'up_abort', id }))
            } catch {
              /* the channel died with the transfer */
            }
            reject(err instanceof Error ? err : new Error(String(err)))
          }
        }
        const handle = (m: Record<string, unknown>) => {
          switch (m.t) {
            case 'up_err':
              cleanup()
              reject(new Error(String(m.error ?? 'upload failed')))
              break
            case 'up_ready':
              id = String(m.id ?? '')
              if (typeof m.chunk === 'number' && m.chunk > 0) chunkSize = m.chunk
              handlers.set(id, handle)
              void pump()
              break
            case 'up_ack':
              if (typeof m.bytes === 'number') onProgress(m.bytes)
              break
            case 'up_ok':
              cleanup()
              resolve()
              break
          }
        }
        handlers.set(ref, handle)
        ch.send(
          JSON.stringify({
            t: 'up_init',
            ref,
            name: file.name,
            dir: dir ?? '',
            size: file.size,
            overwrite: true,
          }),
        )
      })
    },
    [],
  )

  /* One round-trip on the files channel: send, resolve on the reply routed
   * back by ref. Every _err shape rejects, including the synthetic one the
   * teardown broadcasts so no promise outlives the connection. */
  const filesRequest = useCallback(
    (msg: Record<string, unknown>): Promise<Record<string, unknown>> => {
      const ch = filesChRef.current
      if (!ch || ch.readyState !== 'open') {
        return Promise.reject(new Error('the desktop is not connected'))
      }
      const handlers = upHandlersRef.current
      const ref = `r${++upSeqRef.current}`
      return new Promise((resolve, reject) => {
        handlers.set(ref, (m) => {
          handlers.delete(ref)
          if (String(m.t ?? '').endsWith('_err')) {
            reject(new Error(String(m.error ?? 'refused')))
          } else {
            resolve(m)
          }
        })
        ch.send(JSON.stringify({ ...msg, ref }))
      })
    },
    [],
  )

  const filesList = useCallback(
    async (dir: string): Promise<FsListing> => {
      const m = await filesRequest({ t: 'ls', dir })
      return {
        path: String(m.path ?? '/'),
        parent: String(m.parent ?? ''),
        entries: (Array.isArray(m.entries) ? m.entries : []) as FsEntry[],
        truncated: typeof m.truncated === 'number' ? m.truncated : 0,
      }
    },
    [filesRequest],
  )

  const filesOp = useCallback(
    async (op: 'mkdir' | 'delete' | 'rename', path: string, to?: string) => {
      await filesRequest({ t: 'op', op, path, to })
    },
    [filesRequest],
  )

  const downloadFile = useCallback(
    (
      path: string,
      onProgress: (bytes: number, total: number) => void,
    ): Promise<{ name: string; blob: Blob }> => {
      const ch = filesChRef.current
      if (!ch || ch.readyState !== 'open') {
        return Promise.reject(new Error('the desktop is not connected'))
      }
      const handlers = upHandlersRef.current
      const ref = `r${++upSeqRef.current}`
      return new Promise((resolve, reject) => {
        let id = ''
        let name = ''
        let total = 0
        let got = 0
        const parts: Uint8Array[] = []
        const cleanup = () => {
          handlers.delete(ref)
          if (id) handlers.delete(id)
        }
        const handle = (m: Record<string, unknown>) => {
          const t = String(m.t ?? '')
          if (t.endsWith('_err')) {
            cleanup()
            if (id) {
              try {
                ch.send(JSON.stringify({ t: 'dn_abort', id }))
              } catch {
                /* gone with the channel */
              }
            }
            reject(new Error(String(m.error ?? 'download failed')))
            return
          }
          if (t === 'dn_meta') {
            id = String(m.id ?? '')
            name = String(m.name ?? 'file')
            total = typeof m.size === 'number' ? m.size : 0
            handlers.set(id, handle)
            return
          }
          if (t === 'dn_chunk') {
            const part = fromB64(String(m.d ?? ''))
            parts.push(part)
            got += part.length
            onProgress(got, total)
            return
          }
          if (t === 'dn_end') {
            cleanup()
            resolve({ name, blob: new Blob(parts as BlobPart[]) })
          }
        }
        handlers.set(ref, handle)
        ch.send(JSON.stringify({ t: 'dn_init', ref, path }))
      })
    },
    [],
  )

  const patchDelivery = useCallback(
    (id: string, changes: Partial<DeskDelivery>) => {
      setDeliveries((prev) =>
        prev.map((d) => (d.id === id ? { ...d, ...changes } : d)),
      )
    },
    [],
  )

  /* Pull one OFFERED delivery through the files channel into a sink. Shared
   * by the two saves: the auto path collects parts for a Blob, the click
   * path hands each part to a FileSystemWritableFileStream. */
  const pullDelivery = useCallback(
    (
      id: string,
      write: (part: Uint8Array) => void | Promise<void>,
      onProgress: (bytes: number) => void,
    ): Promise<void> => {
      const ch = filesChRef.current
      if (!ch || ch.readyState !== 'open') {
        return Promise.reject(new Error('the desktop is not connected'))
      }
      const handlers = upHandlersRef.current
      const ref = `r${++upSeqRef.current}`
      return new Promise((resolve, reject) => {
        let dn = ''
        let got = 0
        /* Writes settle strictly in order even when the sink is async — a
         * writable-stream write is a promise, and two chunks racing it
         * would interleave bytes. */
        let chain = Promise.resolve()
        const cleanup = () => {
          handlers.delete(ref)
          if (dn) handlers.delete(dn)
        }
        const handle = (m: Record<string, unknown>) => {
          const t = String(m.t ?? '')
          if (t.endsWith('_err')) {
            cleanup()
            reject(new Error(String(m.error ?? 'download failed')))
            return
          }
          if (t === 'dn_meta') {
            dn = String(m.id ?? '')
            handlers.set(dn, handle)
            return
          }
          if (t === 'dn_chunk') {
            const part = fromB64(String(m.d ?? ''))
            chain = chain.then(async () => {
              await write(part)
              got += part.length
              onProgress(got)
            })
            return
          }
          if (t === 'dn_end') {
            cleanup()
            void chain.then(resolve, reject)
          }
        }
        handlers.set(ref, handle)
        ch.send(JSON.stringify({ t: 'dn_init', ref, deliver: id }))
      })
    },
    [],
  )

  const saveDelivery = useCallback(
    (id: string, fromClick = true) => {
      const meta = deliveryMetaRef.current.get(id)
      if (!meta) return
      patchDelivery(id, { status: 'saving', bytes: 0 })
      const finish = (ok: boolean) => {
        patchDelivery(id, { status: ok ? 'done' : 'failed' })
        if (ok) {
          deliveryMetaRef.current.delete(id)
          window.setTimeout(() => {
            setDeliveries((prev) => prev.filter((d) => d.id !== id))
          }, 4000)
        }
        /* A failed save keeps its meta and its place: the same button that
         * started it tries again, because the offer server-side outlives
         * the attempt on purpose. */
      }
      void (async () => {
        try {
          /* Only a click carries the transient user activation a save
           * dialog needs, so only the click path can stream to disk. The
           * auto path — and any browser without the API — assembles a Blob
           * and lets the anchor download it. */
          const picker = (
            window as unknown as {
              showSaveFilePicker?: (opts: { suggestedName?: string }) => Promise<{
                createWritable(): Promise<{
                  write(d: Uint8Array): Promise<void>
                  close(): Promise<void>
                  abort?(): Promise<void>
                }>
              }>
            }
          ).showSaveFilePicker
          if (fromClick && meta.size > AUTO_SAVE_LIMIT && picker) {
            let handle
            try {
              handle = await picker({ suggestedName: meta.name })
            } catch {
              /* The person closed the dialog — not a failure, just not now. */
              patchDelivery(id, { status: 'ready', bytes: 0 })
              return
            }
            const writable = await handle.createWritable()
            try {
              await pullDelivery(
                id,
                (part) => writable.write(part),
                (bytes) => patchDelivery(id, { bytes }),
              )
              await writable.close()
            } catch (err) {
              try {
                await writable.abort?.()
              } catch {
                /* already gone */
              }
              throw err
            }
            finish(true)
            return
          }
          const parts: Uint8Array[] = []
          await pullDelivery(
            id,
            (part) => {
              parts.push(part)
            },
            (bytes) => patchDelivery(id, { bytes }),
          )
          const url = URL.createObjectURL(new Blob(parts as BlobPart[]))
          const a = document.createElement('a')
          a.href = url
          a.download = meta.name
          document.body.appendChild(a)
          a.click()
          a.remove()
          window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
          finish(true)
        } catch {
          finish(false)
        }
      })()
    },
    [patchDelivery, pullDelivery],
  )

  /* Through a ref for the message handler in the effect below, which is
   * wired once per connection and must not be re-run per render. */
  const saveDeliveryRef = useRef(saveDelivery)
  saveDeliveryRef.current = saveDelivery

  const dismissDelivery = useCallback((id: string) => {
    deliveryMetaRef.current.delete(id)
    setDeliveries((prev) => prev.filter((d) => d.id !== id))
  }, [])

  /* While held, a delivery does NOT save itself: the agent workspace shows
   * it in the canvas instead, and the download is a button there. Desktop
   * mode keeps the old behaviour — small files save on arrival. */
  const deliveryHoldRef = useRef(false)
  const setDeliveryHold = useCallback((hold: boolean) => {
    deliveryHoldRef.current = hold
  }, [])

  /* Pull a delivery's bytes into a Blob for an inline preview — the offer
   * stays valid server-side, so a later download pulls nothing twice: the
   * caller keeps the Blob and saves from it. */
  const previewDelivery = useCallback(
    (id: string): Promise<Blob> => {
      const parts: Uint8Array[] = []
      return pullDelivery(
        id,
        (part) => {
          parts.push(part)
        },
        () => {},
      ).then(() => new Blob(parts as BlobPart[]))
    },
    [pullDelivery],
  )

  /* Local clipboard → desktop, shared by two triggers with ONE dedupe.
   *
   * The focus/visibility sync (below, in the effect) is the legacy client's
   * recipe and it works — once the clipboard-read permission exists. But
   * Chrome only SHOWS the permission prompt under user activation, and an
   * alt-tab is not one: on a fresh origin every focus read was refused
   * silently, which is how "copy from my Mac into the room" shipped working
   * on the old origin and dead on this one. So the paste shortcut itself is
   * the second trigger (see desktopInput): a keystroke IS activation, the
   * prompt appears the first time, and ordering the read before the
   * forwarded Ctrl+V also closes the race the focus sync could never win —
   * the keystroke used to reach X before the clipboard did. */
  const pushClipboard = useCallback(async () => {
    const channel = channelRef.current
    if (!channel || channel.readyState !== 'open') return
    if (!navigator.clipboard?.readText) return
    try {
      const text = await navigator.clipboard.readText()
      if (text && text !== lastClipRef.current) {
        lastClipRef.current = text
        channel.send(JSON.stringify({ t: 'clip', clip: text }))
      }
    } catch {
      /* no permission or not focused: the keystroke still goes */
    }
  }, [])

  /* Through a ref so the callback handed to the prompt card never goes
   * stale: the question changes without the card remounting. */
  const questionRef = useRef<AgentQuestion | null>(null)
  questionRef.current = question
  const answerQuestion = useCallback(
    (answer: string) => {
      const q = questionRef.current
      if (!q) return
      sendInput({ t: 'question_answer', req: q.id, answer })
      setQuestion(null)
    },
    [sendInput],
  )

  /* Same ref trick, same reason: the card holds this callback across the
   * life of one request, and the request's id must not go stale in it. */
  const controlRequestRef = useRef<ControlRequest | null>(null)
  controlRequestRef.current = controlRequest
  const answerControlRequest = useCallback(
    (granted: boolean) => {
      const req = controlRequestRef.current
      if (!req) return
      sendInput({ t: 'control_answer', req: req.id, grant: granted })
      setControlRequest(null)
    },
    [sendInput],
  )

  /* ---- the voice mesh (fase 3) ---------------------------------------
   *
   * Peer-to-peer audio between the people watching, a separate layer from
   * the desktop's own sound: your YouTube keeps playing from the server's
   * track while your colleagues' voices arrive on direct browser-to-browser
   * connections and mix in your own audio output. The server relays sealed
   * envelopes (type:"voice") and can neither hear nor record any of it.
   *
   * The mesh uses the browser's perfect-negotiation pattern: both ends may
   * offer (both add their track), and glare resolves by politeness — the
   * lexicographically larger member id yields. Membership is announced with
   * hello/bye envelopes; a peer connection exists only between two members
   * who BOTH joined the conference. */

  const sendVoice = useCallback((to: string, data: Record<string, unknown>) => {
    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'voice', to, data }))
    }
  }, [])

  const dropVoicePeer = useCallback((id: string) => {
    const peer = voiceRef.current.peers.get(id)
    if (!peer) return
    voiceRef.current.peers.delete(id)
    try {
      peer.pc.onicecandidate = null
      peer.pc.ontrack = null
      peer.pc.onnegotiationneeded = null
      peer.pc.close()
    } catch {
      /* already gone */
    }
    peer.audio.srcObject = null
    peer.audio.remove()
    setVoicePeers((prev) => prev.filter((p) => p !== id))
  }, [])

  const ensureVoicePeer = useCallback(
    (id: string): VoicePeer => {
      const existing = voiceRef.current.peers.get(id)
      if (existing) return existing
      const pc = new RTCPeerConnection({ iceServers: iceServersRef.current })
      const audio = document.createElement('audio')
      audio.autoplay = true
      audio.style.display = 'none'
      applySpeaker(audio)
      document.body.appendChild(audio)
      const peer: VoicePeer = {
        pc,
        audio,
        /* The larger id yields in a glare — deterministic on both ends. */
        polite: myIdRef.current > id,
        makingOffer: false,
      }
      voiceRef.current.peers.set(id, peer)
      const stream = voiceRef.current.stream
      if (stream) {
        for (const track of stream.getTracks()) pc.addTrack(track, stream)
      }
      pc.onnegotiationneeded = async () => {
        try {
          peer.makingOffer = true
          await pc.setLocalDescription()
          sendVoice(id, { t: 'sdp', desc: pc.localDescription })
        } catch {
          /* a torn-down pc mid-negotiation */
        } finally {
          peer.makingOffer = false
        }
      }
      pc.onicecandidate = (e) => {
        if (e.candidate) sendVoice(id, { t: 'ice', cand: e.candidate.toJSON() })
      }
      pc.ontrack = (e) => {
        audio.srcObject = e.streams[0] ?? new MediaStream([e.track])
        void audio.play().catch(() => undefined)
      }
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === 'connected') {
          setVoicePeers((prev) => (prev.includes(id) ? prev : [...prev, id]))
        } else if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
          dropVoicePeer(id)
        }
      }
      return peer
    },
    [dropVoicePeer, sendVoice],
  )

  const handleVoiceEnvelope = useCallback(
    async (from: string, data: { t?: string; desc?: RTCSessionDescriptionInit; cand?: RTCIceCandidateInit }) => {
      if (!voiceRef.current.stream) return // not in the conference: envelopes are noise
      if (data.t === 'bye') {
        dropVoicePeer(from)
        return
      }
      if (data.t === 'hello') {
        ensureVoicePeer(from)
        return
      }
      const peer = ensureVoicePeer(from)
      if (data.t === 'sdp' && data.desc) {
        const offerCollision =
          data.desc.type === 'offer' &&
          (peer.makingOffer || peer.pc.signalingState !== 'stable')
        if (offerCollision && !peer.polite) return // the impolite end ignores glare
        try {
          if (offerCollision) {
            await Promise.all([
              peer.pc.setLocalDescription({ type: 'rollback' }),
              peer.pc.setRemoteDescription(data.desc),
            ])
          } else {
            await peer.pc.setRemoteDescription(data.desc)
          }
          if (data.desc.type === 'offer') {
            await peer.pc.setLocalDescription()
            sendVoice(from, { t: 'sdp', desc: peer.pc.localDescription })
          }
        } catch {
          /* a peer that vanished mid-handshake */
        }
      } else if (data.t === 'ice' && data.cand) {
        try {
          await peer.pc.addIceCandidate(data.cand)
        } catch {
          /* stale candidate for a rolled-back description */
        }
      }
    },
    [dropVoicePeer, ensureVoicePeer, sendVoice],
  )

  const leaveVoice = useCallback(() => {
    for (const id of [...voiceRef.current.peers.keys()]) {
      sendVoice(id, { t: 'bye' })
      dropVoicePeer(id)
    }
    if (voiceRef.current.stream) {
      voiceRef.current.stream.getTracks().forEach((tr) => tr.stop())
      voiceRef.current.stream = null
    }
    setVoiceLive(false)
    setVoicePeers([])
  }, [dropVoicePeer, sendVoice])

  const toggleVoice = useCallback(async () => {
    if (voiceRef.current.stream) {
      leaveVoice()
      return
    }
    /* The remembered device, exactly — and an explicit fallback to the
     * default when it is gone, rather than failing the conference over
     * yesterday's headset. */
    let stream: MediaStream
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: voiceConstraints() })
    } catch (err) {
      if ((err as DOMException)?.name !== 'OverconstrainedError') throw err
      stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    }
    voiceRef.current.stream = stream
    setVoiceLive(true)
    /* Announce to everyone with a browser; whoever is in the conference
     * answers by building the pair. */
    for (const m of membersRef.current) {
      if (m.id !== myIdRef.current && !m.agent) sendVoice(m.id, { t: 'hello' })
    }
  }, [leaveVoice, sendVoice])

  /* The microphone INTO the desktop — the return path. Ported from the
   * first client: the session was negotiated with an inactive audio m-line
   * waiting for exactly this, so going live is replaceTrack + declaring the
   * direction + asking the server for a fresh offer. Only the controller
   * may publish (the server enforces it too); switching OFF is always
   * allowed, because a hot microphone nobody can silence is not a feature. */
  const toggleMic = useCallback(async (deviceId?: string) => {
    const pc = pcRef.current
    const ws = wsRef.current
    if (!pc || pc.connectionState !== 'connected' || !ws) {
      throw new Error('media.notConnected')
    }
    if (micStreamRef.current) {
      micStreamRef.current.getTracks().forEach((t) => t.stop())
      const tr = pc
        .getTransceivers()
        .find((t) => t.sender && t.sender.track && t.sender.track.kind === 'audio')
      if (tr) {
        await tr.sender.replaceTrack(null)
        tr.direction = 'inactive'
        ws.send(JSON.stringify({ type: 'renegotiate' }))
      }
      micStreamRef.current = null
      setMicLive(false)
      return
    }
    if (!controlRef.current.yours) throw new Error('media.needControl')
    const constraints: MediaStreamConstraints = deviceId
      ? { audio: { deviceId: { exact: deviceId } } }
      : { audio: audioConstraints() }
    let stream: MediaStream
    try {
      stream = await navigator.mediaDevices.getUserMedia(constraints)
    } catch (err) {
      /* The remembered device is gone. Fall back to the default explicitly —
       * the settings dialog shows what is actually in use. */
      if ((err as DOMException)?.name !== 'OverconstrainedError') throw err
      stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    }
    const track = stream.getTracks()[0]
    const tr = pc
      .getTransceivers()
      .find(
        (t) =>
          t.receiver &&
          t.receiver.track &&
          t.receiver.track.kind === 'audio' &&
          t.sender &&
          !t.sender.track &&
          (t.currentDirection === 'inactive' || t.currentDirection === null),
      )
    if (!tr) {
      stream.getTracks().forEach((t) => t.stop())
      throw new Error('media.noSlot')
    }
    await tr.sender.replaceTrack(track)
    tr.direction = 'sendonly'
    ws.send(JSON.stringify({ type: 'renegotiate' }))
    micStreamRef.current = stream
    setMicLive(true)
    track.addEventListener('ended', () => {
      micStreamRef.current = null
      setMicLive(false)
    })
  }, [])

  const getStats = useCallback(async () => {
    const pc = pcRef.current
    if (!pc) return null
    try {
      return await pc.getStats()
    } catch {
      return null
    }
  }, [])

  useEffect(() => {
    if (!auth) {
      setState('idle')
      setStream(null)
      return
    }

    let closed = false
    let ws: WebSocket | null = null
    let pc: RTCPeerConnection | null = null
    let timer: number | undefined

    const fail = (key: string) => {
      if (closed) return
      setError(key)
      setState('failed')
    }

    const teardown = () => {
      closed = true
      window.clearTimeout(timer)
      window.removeEventListener('focus', syncLocalClipboard)
      document.removeEventListener('visibilitychange', onVisible)
      channelRef.current = null
      pcRef.current = null
      setControl(NOBODY)
      setInputReady(false)
      setCursor(null)
      setPointer(null)
      setRecording(false)
      setRecordingSince(0)
      setRestreams([])
      setRestreamError(null)
      setRestreamAble(true)
      setQuestion(null)
      setControlRequest(null)
      /* Offers die with the session that held them — the runtime keeps the
       * files, but these ids answer to nobody now. */
      setDeliveries([])
      deliveryMetaRef.current.clear()
      wsRef.current = null
      for (const id of [...voiceRef.current.peers.keys()]) dropVoicePeer(id)
      if (voiceRef.current.stream) {
        voiceRef.current.stream.getTracks().forEach((tr) => tr.stop())
        voiceRef.current.stream = null
      }
      setVoiceLive(false)
      setVoicePeers([])
      if (micStreamRef.current) {
        micStreamRef.current.getTracks().forEach((t) => t.stop())
        micStreamRef.current = null
      }
      setMicLive(false)
      filesChRef.current = null
      /* Every in-flight transfer learns the connection died — a promise
       * that never settles is a progress bar frozen at 60% forever. */
      for (const handle of upHandlersRef.current.values()) {
        handle({ t: 'up_err', error: 'disconnected' })
      }
      upHandlersRef.current.clear()
      if (pc) {
        pc.ontrack = null
        pc.onicecandidate = null
        pc.ondatachannel = null
        pc.close()
        pc = null
      }
      if (ws) {
        ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null
        ws.close()
        ws = null
      }
    }

    setState('connecting')
    setError(null)

    /* The focus/visibility trigger of pushClipboard — see its comment for
     * why the paste shortcut is the trigger that actually carries the day. */
    const syncLocalClipboard = () => void pushClipboard()
    const onVisible = () => {
      if (document.visibilityState === 'visible') syncLocalClipboard()
    }
    window.addEventListener('focus', syncLocalClipboard)
    document.addEventListener('visibilitychange', onVisible)

    void (async () => {
      timer = window.setTimeout(() => fail('wr.desk.slow'), HANDSHAKE_TIMEOUT_MS)

      /* The desktop that served this page is the desktop to talk to: the
       * standalone client always connects back to its own origin. */
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/ws`)
      wsRef.current = ws

      ws.onerror = () => fail('wr.desk.unreachable')
      ws.onclose = () => {
        /* Only a surprise close is a failure. One that follows a successful
         * teardown is this hook doing its job. */
        if (!closed) fail('wr.desk.dropped')
      }

      ws.onopen = () => {
        // The runtime's door: the first frame authenticates, and until it
        // validates there is no SDP, no ICE, no DataChannel. The name rides
        // along so the roster and the witness say who this is.
        const hello: Record<string, unknown> = { type: 'auth' }
        if (auth !== 'open' && auth) {
          if ('token' in auth) hello.token = auth.token
          else {
            hello.user = auth.user
            hello.pass = auth.pass
          }
        }
        if (nameRef.current) hello.name = nameRef.current
        ws?.send(JSON.stringify(hello))
      }

      ws.onmessage = async (event) => {
        let msg: {
          type?: string
          ok?: boolean
          reason?: string
          sdp?: string
          candidate?: string
          sdpMLineIndex?: number
          iceServers?: RTCIceServer[]
        }
        try {
          msg = JSON.parse(String(event.data))
        } catch {
          return
        }

        if (msg.type === 'auth') {
          /* A refusal here is wrong credentials — a different failure from a
           * broken network, and the login screen wants to say which. */
          if (!msg.ok) {
            fail('login.failed')
            return
          }
          /* The door mints a session token on success; whoever is driving
           * this hook decides whether a reload should remember it. */
          const token = (msg as { token?: string }).token
          if (token) onTokenRef.current?.(token)
          return
        }
        if (msg.type === 'fatal') {
          fail('wr.desk.refused')
          return
        }
        if (msg.type === 'voice') {
          const env = msg as unknown as { from?: string; data?: Record<string, unknown> }
          if (env.from && env.data) void handleVoiceEnvelope(env.from, env.data)
          return
        }

        if (msg.type === 'config') {
          /* stunPort says the desktop answers STUN itself. The URL is built
           * HERE, from the hostname this client already reached the desktop
           * by — the one address that is right through every NAT and tunnel
           * the server cannot see. Configured servers ride along after. */
          const stunPort = (msg as { stunPort?: number }).stunPort
          const servers: RTCIceServer[] = [
            ...(stunPort
              ? [{ urls: [`stun:${window.location.hostname}:${stunPort}`] }]
              : []),
            ...(msg.iceServers ?? []),
          ]
          iceServersRef.current = servers
          /* The build, for the About card. Sent after auth on purpose. */
          const v = (msg as { version?: string }).version
          if (v) setServerVersion(v)
          pc = new RTCPeerConnection({ iceServers: servers })
          pcRef.current = pc
          pc.ontrack = (e) => {
            /* Latency over smoothness, the same trade the runtime's own client
             * makes: this is a desktop somebody is about to type into, and a
             * jitter buffer is half a second of politeness nobody asked for. */
            try {
              const receiver = e.receiver as RTCRtpReceiver & {
                jitterBufferTarget?: number
                playoutDelayHint?: number
              }
              if ('jitterBufferTarget' in receiver) receiver.jitterBufferTarget = 0
              if ('playoutDelayHint' in receiver) receiver.playoutDelayHint = 0
            } catch {
              /* browser-dependent, and not worth failing over */
            }
            if (closed) return
            window.clearTimeout(timer)
            setStream(e.streams[0] ?? null)
            setState('live')
          }
          pc.onicecandidate = (e) => {
            if (!e.candidate || !ws) return
            ws.send(
              JSON.stringify({
                type: 'ice',
                candidate: e.candidate.candidate,
                sdpMLineIndex: e.candidate.sdpMLineIndex,
              }),
            )
          }
          /* The SERVER opens the input channel; this side only receives it.
           * Same direction as the runtime's own client, so neither side has
           * to agree on who offers what — the runtime always does. */
          pc.ondatachannel = (e) => {
            if (e.channel.label === 'files') {
              /* The upload channel: every message belongs to one transfer,
               * routed by the ref this side chose or the id the server
               * minted. See uploadFile above. */
              filesChRef.current = e.channel
              e.channel.onmessage = (ev) => {
                let m: Record<string, unknown>
                try {
                  m = JSON.parse(String(ev.data)) as Record<string, unknown>
                } catch {
                  return
                }
                const key = String(m.ref ?? m.id ?? '')
                upHandlersRef.current.get(key)?.(m)
              }
              return
            }
            if (e.channel.label !== 'input') return
            channelRef.current = e.channel
            /* The channel ANNOUNCES itself before it can carry anything —
             * open comes later, and inputReady is what lets the lease sync
             * retry instead of firing into the gap. */
            const hello = () => {
              if (closed) return
              setInputReady(true)
              /* Whatever is on the local clipboard is offered right away, so
               * a person who copied a command BEFORE opening the room can
               * paste it without alt-tabbing once to trigger the focus sync. */
              syncLocalClipboard()
              /* Ask for the restream list once the channel can carry the
               * question. The runtime broadcasts changes, but a page that
               * loads MID-stream saw none of them — without this, the live
               * badge and the stop button both think nothing is running. */
              try {
                e.channel.send(JSON.stringify({ t: 'restream', rs: { action: 'list' } }))
              } catch {
                /* closed between check and send: the next broadcast covers it */
              }
            }
            e.channel.onopen = hello
            if (e.channel.readyState === 'open') hello()
            e.channel.onmessage = (ev) => {
              if (closed) return
              let m: {
                t?: string
                d?: string
                x?: number
                y?: number
                w?: number
                h?: number
                you?: string
                paused?: boolean
                pausedBy?: string
                members?: {
                  id?: string
                  name?: string
                  controller?: boolean
                  agent?: boolean
                  color?: string
                }[]
              }
              try {
                m = JSON.parse(String(ev.data))
              } catch {
                return
              }
              if (m.t === 'download') {
                /* A finished screenshot or recording, OFFERED by id on the
                 * files channel and pulled through it — no HTTP. The `url`
                 * that rides beside the id is the embedded dev client's
                 * one-use ticket; the panel ignores it. */
                const offer = String((m as { deliver?: string }).deliver ?? '')
                const name = String((m as { name?: string }).name ?? 'file')
                const size = Number((m as { size?: number }).size ?? 0)
                if (!offer) return
                deliveryMetaRef.current.set(offer, { name, size })
                setDeliveries((prev) => [
                  ...prev,
                  { id: offer, name, size, status: 'ready', bytes: 0 },
                ])
                /* Small files save themselves, as the ticket click used to;
                 * big ones wait in the tray for the click that lets the
                 * save stream to disk. */
                if (size <= AUTO_SAVE_LIMIT && !deliveryHoldRef.current) {
                  saveDeliveryRef.current(offer, false)
                }
                return
              }
              if (m.t === 'capture_state') {
                const on = !!(m as { recording?: boolean }).recording
                setRecording(on)
                setRecordingSince(on ? Date.now() : 0)
                return
              }
              if (m.t === 'quality') {
                /* The room's position and the cap in force; `refused` rides
                 * the same message when this session's change was rejected,
                 * so the dial ends up showing the truth either way. */
                const q = m as { mode?: string; fps?: number; refused?: string }
                const mode = q.mode
                if (mode === 'auto' || mode === 'media' || mode === 'high') {
                  setQualityState({ mode, fps: Number(q.fps ?? 0) })
                }
                setQualityError(
                  typeof q.refused === 'string' && q.refused !== ''
                    ? q.refused
                    : null,
                )
                return
              }
              if (m.t === 'restreams') {
                /* A refused start answers only the client that asked, with
                 * the reason in `error`; a clean broadcast clears it. `able`
                 * says whether this session's encoder can restream at all. */
                const extra = m as { error?: string; able?: boolean }
                setRestreamError(
                  typeof extra.error === 'string' && extra.error !== ''
                    ? extra.error
                    : null,
                )
                if (typeof extra.able === 'boolean') setRestreamAble(extra.able)
                const list = (m as { list?: unknown[] }).list
                setRestreams(
                  Array.isArray(list)
                    ? list.flatMap((item) => {
                        const r = item as Partial<RestreamInfo> | null
                        return r && typeof r.id === 'string'
                          ? [
                              {
                                id: r.id,
                                platform: String(r.platform ?? 'custom'),
                                url: String(r.url ?? ''),
                                seconds: Number(r.seconds ?? 0),
                              },
                            ]
                          : []
                      })
                    : [],
                )
                return
              }
              if (m.t === 'clip') {
                /* Something was copied on the desktop → the local clipboard.
                 * writeText needs the page focused; when it is not, the value
                 * is remembered anyway so the focus sync does not bounce a
                 * stale local value back over it. */
                const text = String((m as { d?: string }).d ?? '')
                if (text) {
                  lastClipRef.current = text
                  navigator.clipboard?.writeText?.(text).catch(() => {})
                }
                return
              }
              if (isConsoleMessage(m.t as string)) {
                /* A terminal's bytes, addressed to THIS browser — a console is
                 * the one thing on this plane that is not broadcast. Routed
                 * before the chat, because the two vocabularies do not overlap
                 * and checking the narrower one first keeps the common case a
                 * single comparison. */
                consoleRef.current.handle(m as Record<string, unknown>)
                return
              }
              if (isAgentMessage(m.t as string)) {
                /* The chat panel, in one line. Routed rather than handled here
                 * because none of it touches the peer connection, and this
                 * file is the transport. */
                chatRef.current.handle(m as Record<string, unknown>)
                return
              }
              if (m.t === 'question') {
                /* The runtime says whether the people at the DESKTOP are the
                 * ones who must answer. Today it always says yes; the day a
                 * console-answered route lands, honoring the flag here is
                 * what keeps a modal off a tab nobody needs to look at. */
                const q = m as {
                  id?: number
                  text?: string
                  options?: string[] | null
                  seconds?: number
                  secret?: boolean
                  desktop?: boolean
                }
                if (q.desktop === false) return
                setQuestion({
                  id: q.id ?? 0,
                  text: String(q.text ?? ''),
                  options: Array.isArray(q.options)
                    ? q.options.filter((o): o is string => typeof o === 'string')
                    : [],
                  seconds: q.seconds ?? 0,
                  secret: !!q.secret,
                })
                return
              }
              if (m.t === 'question_done') {
                /* Whichever way it ended — answered here, answered by someone
                 * else, or timed out — the prompt leaves everyone's screen. */
                const q = m as { id?: number }
                setQuestion((cur) => (cur && cur.id === (q.id ?? 0) ? null : cur))
                return
              }
              if (m.t === 'control_request') {
                /* The agent wants the mouse and the keyboard. Every word of
                 * the prompt is the panel's own — the wire carries only who
                 * is asking and how long the runtime will wait. */
                const c = m as { id?: number; who?: string; seconds?: number }
                setControlRequest({
                  id: c.id ?? 0,
                  who: String(c.who ?? ''),
                  seconds: Number(c.seconds ?? 0),
                })
                return
              }
              if (m.t === 'control_request_done') {
                /* Answered here, answered by someone else in the room, or
                 * timed out — either way the card leaves every screen. */
                const c = m as { id?: number }
                setControlRequest((cur) =>
                  cur && cur.id === (c.id ?? 0) ? null : cur,
                )
                return
              }
              if (m.t === 'presence') {
                const members = m.members ?? []
                const me = members.find((member) => member.id === m.you)
                const driver = members.find((member) => member.controller)
                setControl({
                  yours: !!me?.controller,
                  holder: driver?.name ?? null,
                  holderIsAgent: !!driver?.agent,
                })
                setPaused(!!m.paused)
                setPausedBy(String(m.pausedBy ?? ''))
                const roster: RoomMember[] = members.map((member) => ({
                  id: String(member.id ?? ''),
                  name: String(member.name ?? ''),
                  controller: !!member.controller,
                  agent: !!member.agent,
                  color: String(member.color ?? '#f9c74f'),
                }))
                membersRef.current = roster
                setMembers(roster)
                myIdRef.current = String(m.you ?? '')
                setMyId(myIdRef.current)
                /* A voice pair whose other end left the room is a dead leg. */
                for (const id of [...voiceRef.current.peers.keys()]) {
                  if (!roster.some((member) => member.id === id)) dropVoicePeer(id)
                }
              } else if (m.t === 'cursor') {
                /* The real X cursor shape, as a PNG with its hotspot. A big
                 * bitmap (the 48px cursors XCURSOR_SIZE=48 hands out) is
                 * declared 2x so a Retina screen draws it crisp at 24 CSS px
                 * instead of stretching a 24px one into mush; the hotspot
                 * scales with it. Browsers without image-set on cursors fall
                 * through to the plain url() at native size, then default. */
                if (!m.d) {
                  setCursor(null)
                } else if ((m.w ?? 0) >= 40 && HIDPI_CURSORS) {
                  const hx = Math.round((m.x ?? 0) / 2)
                  const hy = Math.round((m.y ?? 0) / 2)
                  setCursor(`${IMAGE_SET}(url(${m.d}) 2x) ${hx} ${hy}, default`)
                } else {
                  setCursor(`url(${m.d}) ${m.x ?? 0} ${m.y ?? 0}, default`)
                }
              } else if (m.t === 'pointer') {
                /* The real cursor's position, for the viewer overlay. */
                setPointer({ x: m.x ?? 0, y: m.y ?? 0 })
              }
              /* Anything else the runtime pushes on this channel belongs to
               * a feature this panel has not grown yet. Ignoring it is
               * deliberate — acting on half of one would be worse than
               * waiting — but the list has a history of hiding real gaps:
               * "clipboard, downloads, capture state" sat in this comment
               * while all three were reported broken, one by one, because a
               * message nobody handles looks exactly like a feature nobody
               * misses. When something is ignored here, name it. */
            }
          }
          return
        }

        if (msg.type === 'offer') {
          /* Only the FIRST offer builds the connection; later ones are
           * renegotiations over the same peer — switching a microphone on
           * should not cut the video and redo ICE. */
          if (!pc) pc = new RTCPeerConnection()
          try {
            await pc.setRemoteDescription({ type: 'offer', sdp: msg.sdp ?? '' })
            const answer = await pc.createAnswer()
            await pc.setLocalDescription(answer)
            ws?.send(JSON.stringify({ type: 'answer', sdp: answer.sdp }))
          } catch {
            fail('wr.desk.negotiation')
          }
          return
        }

        if (msg.type === 'ice' && pc && msg.candidate) {
          try {
            await pc.addIceCandidate({
              candidate: msg.candidate,
              sdpMLineIndex: msg.sdpMLineIndex,
            })
          } catch {
            /* One rejected candidate is normal; ICE succeeds on any pair. */
          }
        }
      }
    })()

    return teardown
    /* Deliberately NOT `actions`: see ticketRef above. This effect must run
     * when the ROOM changes, and at no other time. */
  }, [auth, attempt])

  return {
    state,
    stream,
    error,
    control,
    micLive,
    toggleMic,
    members,
    myId,
    voiceLive,
    voicePeers,
    toggleVoice,
    paused,
    pausedBy,
    inputReady,
    pointer,
    cursor,
    recording,
    serverVersion,
    recordingSince,
    restreaming: restreams.length > 0,
    restreams,
    restreamError,
    clearRestreamError: () => setRestreamError(null),
    restreamAble,
    quality,
    qualityError,
    setQuality,
    retry,
    toggleControl,
    sendInput,
    getStats,
    deliveries,
    saveDelivery,
    dismissDelivery,
    previewDelivery,
    setDeliveryHold,
    question,
    answerQuestion,
    controlRequest,
    answerControlRequest,
    pushClipboard,
    uploadFile,
    filesList,
    filesOp,
    downloadFile,
    chat,
    console: console_,
  }
}

/* Uint8Array → base64, in slices: String.fromCharCode over a whole chunk
 * would blow the argument limit on some engines. */
function toB64(bytes: Uint8Array): string {
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  }
  return btoa(s)
}

function fromB64(b64: string): Uint8Array {
  const s = atob(b64)
  const out = new Uint8Array(s.length)
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i)
  return out
}
