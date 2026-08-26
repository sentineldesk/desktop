/* SentinelDesk
 * A collaborative operating system for people and AI agents.
 *
 * Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
 *
 * Licensed under the Apache License, Version 2.0.
 *
 * This product's name and logo are trademarks of Federico Pereira and are not
 * covered by the license above. See the README for the trademark policy.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

/* The chat panel's state, kept out of useDesktopStream.
 *
 * That file is the transport and it is already long; this is a reducer over six
 * message types and nothing else touches WebRTC. Split so the panel's rules —
 * which message replaces what, when a bubble stops streaming — can be read and
 * tested without the peer connection around them.
 *
 * The browser never learns that there is an agent process, a Unix socket or a
 * runtime. It sends `agent_say` down the same DataChannel it sends mouse moves
 * on, and messages come back. Everything about how the desktop reaches the
 * agent is the daemon's business, which is what lets the agent be absent
 * without this file having a word to say about it.
 */

import { useCallback, useRef, useState } from 'react'

/** Whether there is an agent to talk to, and if not, what to do about it.
 *
 * Three states rather than two, and the third is the one that matters:
 * `present` without `ready` means the runtime is running and has no model
 * configured. Collapsing that into either neighbour sends somebody to the wrong
 * remedy — told it is absent they reinstall what they have, told it is ready
 * they get a box that fails on the first message. */
/** One model the runtime offers. */
export interface AgentModel {
  readonly provider: string
  readonly id: string
  readonly note: string
  /** Whether its provider can be reached right now. */
  readonly ready: boolean
}

export interface AgentAvailability {
  readonly present: boolean
  readonly ready: boolean
  /** What is wrong, in a sentence. Empty when ready. */
  readonly reason: string
  /** The command that fixes it, when there is one. Carried apart from `reason`
   * so the panel can render it as a copyable line rather than the reader
   * hunting for a command inside a sentence. */
  readonly remedy: string
  readonly provider: string
  readonly model: string
  readonly mode: string

  /** What the runtime can be switched to, and which of them work right now.
   *  Empty until the first status arrives. */
  readonly models: readonly AgentModel[]
}

export const agentUnknown: AgentAvailability = {
  present: false,
  ready: false,
  reason: '',
  remedy: '',
  provider: '',
  model: '',
  mode: '',
  models: [],
}

/** One tool the agent called, drawn under the message it was working on. */
export interface AgentStep {
  readonly key: string
  readonly tool: string
  readonly detail: string
  readonly turn: number
}

/** How an exchange ended. */
export interface AgentEnding {
  readonly ok: boolean
  readonly text: string
  readonly turns: number
  readonly calls: number
  readonly inToks: number
  readonly outToks: number
  /** The ceiling that ended the run, or "stopped" when somebody pressed the
   * button. Empty when the agent simply finished — which is the distinction
   * that decides whether the footer is neutral or a warning. */
  readonly stoppedBy: string
  /** How long the exchange ran, in ms as this panel measured it. Zero on a
   * restored transcript — the wire does not carry it. */
  readonly ms: number
}

/** One bubble. */
export interface AgentMessage {
  readonly key: string
  /** Which exchange this belongs to. */
  readonly chat: string
  readonly role: 'human' | 'agent' | 'system'
  readonly text: string
  readonly at: number
  /** Still being written. The panel draws a caret and the composer offers stop. */
  readonly streaming: boolean
  readonly steps: readonly AgentStep[]
  readonly ending: AgentEnding | null
  /** True when the text came from a delta rather than from the whole-answer
   * record. It decides whether the record REPLACES this bubble or is ignored:
   * a panel that watched the run keeps its narration, one that arrived late
   * gets the finished answer. See handle(). */
  readonly streamed: boolean
}

/** One past conversation, as the history drawer lists it. */
/** HistoryKind is which of the three things an agent_history frame is.
 *
 * Split out and exported because getting it wrong is invisible: a live
 * conversation misread as the sessions list empties the history drawer, and the
 * sessions list misread as a transcript replaces what somebody is reading with
 * nothing. Neither throws. So the rule is a function with a test rather than a
 * condition inside a callback nothing can reach. */
export type HistoryKind = 'list' | 'live' | 'past'

/** classifyHistory reads an agent_history frame's kind off its contents.
 *
 * `messages` is what makes it a transcript — NOT the session number. Session 0
 * means "the conversation the runtime is in", which is a transcript, and the
 * panel read it as the list for exactly as long as continuing a conversation
 * was impossible. */
export function classifyHistory(m: {
  session?: unknown
  messages?: unknown
  [k: string]: unknown
}): HistoryKind {
  if (!Array.isArray(m.messages)) return 'list'
  return Number(m.session) === 0 ? 'live' : 'past'
}

export interface AgentSession {
  /** live marks the conversation the runtime is in — the one row that cannot
   * be deleted, because the engine is still writing to its record. */
  readonly live: boolean
  readonly id: number
  readonly title: string
  readonly turns: number
  readonly at: string
}

export interface AgentChatState {
  readonly agent: AgentAvailability
  readonly messages: readonly AgentMessage[]
  /** The exchange currently being worked on, or ''. */
  readonly busy: string
  /** When the exchange in flight began, as epoch ms; 0 when idle.
   *
   *  Kept here rather than measured in the component, because the component
   *  that shows the clock is not the one that knows a run started — and a
   *  timer started on first render would restart every time React remounted
   *  the panel, telling somebody who reopened it that a four-minute run had
   *  just begun. */
  readonly since: number
  /** Total ms of finished exchanges this session — the clock's resting value.
   * Add `now - since` while busy for the live total. */
  readonly worked: number
  readonly sessions: readonly AgentSession[]
  /** Which past conversation is on screen, or 0 for the live one. */
  readonly viewing: number
  /** Set when a delete was refused for want of the controls; holds whoever has
   *  them, or '' when nobody does. Cleared by the panel once shown. */
  readonly denied: string | null
}

/* A message off the wire, before its type is known. Deliberately loose: the
 * daemon may send fields this build has never heard of, and reading them as
 * `unknown` is what makes an older panel ignore them instead of throwing. */
type Wire = Record<string, unknown>

const str = (v: unknown): string => (typeof v === 'string' ? v : '')

/* The steps a transcript turn carried — the runtime attaches its tool calls
 * so a restored conversation folds the same way the live one did. Keys come
 * from their own counter: the hook's message counter is out of reach here
 * and these only have to be unique among themselves. */
let restoredStepSeq = 0
function stepsOf(h: Wire): AgentStep[] {
  if (!Array.isArray(h.steps)) return []
  return (h.steps as Wire[]).map((st) => ({
    key: `ts${++restoredStepSeq}`,
    tool: str(st.tool),
    detail: str(st.detail),
    turn: 0,
  }))
}
const num = (v: unknown): number => (typeof v === 'number' && isFinite(v) ? v : 0)

/* One bubble per EXCHANGE, not one per model turn.
 *
 * The runtime records a run the way it happened: a goal, then one turn for
 * every time the model spoke, each carrying the tool calls it made. The live
 * panel does not draw it that way — agent_step and agent_delta both land on
 * the one bubble that stays open from the first call until agent_end, so a run
 * of twelve turns shows as one piece of narration above one folded chain of
 * every call in it.
 *
 * Restoring turn by turn drew the same record in a different shape: twelve
 * bubbles, twelve "Tools · 2" headers, two lines behind each. Nothing threw
 * and nothing was lost — a person reloading simply watched the conversation
 * they had been reading come apart. So consecutive agent turns are merged
 * HERE. The record stays per-turn, because that is what happened; only the
 * drawing is grouped, the way the live path already groups it.
 *
 * A human turn closes the group. There is only ever one, at the top of a run,
 * in what the runtime sends today — handling it anyway costs one condition and
 * means a session holding more than one goal draws correctly the first time it
 * exists rather than the first time somebody notices it does not. */
export function restoreTurns(
  turns: Wire[],
  chat: string,
  mkKey: () => string,
): AgentMessage[] {
  const out: AgentMessage[] = []
  for (const h of turns) {
    const role = (str(h.role) || 'system') as AgentMessage['role']
    const text = str(h.text)
    const steps = stepsOf(h)
    const prev = out[out.length - 1]
    if (role === 'agent' && prev && prev.role === 'agent') {
      out[out.length - 1] = {
        ...prev,
        /* A blank line between, so two turns of prose read as two paragraphs
         * rather than one run-on sentence. A turn that only called tools has
         * no text and must not contribute an empty paragraph. */
        text: prev.text && text ? `${prev.text}\n\n${text}` : prev.text || text,
        steps: [...prev.steps, ...steps],
      }
      continue
    }
    out.push({
      key: mkKey(),
      chat,
      role,
      text,
      at: 0,
      streaming: false,
      steps,
      ending: null,
      streamed: false,
    })
  }
  return out
}



/** Every `t` this module claims. useDesktopStream routes on it, so a type
 * added here and not there is a message that arrives and does nothing. */
export function isAgentMessage(t: string): boolean {
  return (
    t === 'agent_status' ||
    t === 'agent_chat' ||
    t === 'agent_delta' ||
    t === 'agent_step' ||
    t === 'agent_end' ||
    t === 'agent_history' ||
    t === 'agent_forget_denied' ||
    t === 'agent_export'
  )
}

export interface AgentChatApi extends AgentChatState {
  /** Feed one message off the DataChannel. */
  handle(m: Wire): void
  /** Send what somebody typed. */
  ask(text: string): void
  /** Stop the exchange in flight. */
  stop(): void
  /** Start a fresh conversation, keeping the history. */
  reset(): void
  /** Ask the runtime for the list of past conversations. */
  loadSessions(): void
  /** Ask for the conversation the runtime is IN — what re-fills the thread
   * after a reload found it empty while the roster still marked a live row. */
  hydrate(): void
  /** Give one conversation the person's own name; empty clears it. The
   * runtime answers with the refreshed list, to every panel. */
  rename(id: number, title: string): void
  /** Show one past conversation, or 0 to come back to the live one. */
  openSession(id: number): void
  /** resume continues a past conversation, for every face at once. */
  resume(id: number): void
  /** Delete one past conversation from the agent's own store. */
  forget(id: number): void
  /** Delete every past conversation. */
  forgetAll(): void
  /** Acknowledge the refusal, so the notice stops showing. */
  clearDenied(): void
  /** Ask for one past conversation as a file; 0 means the one open now. */
  exportSession(id: number, format?: string): void
  /** The slash commands this panel offers, for the palette. */
  readonly commands: readonly AgentCommand[]
}

/** One slash command, as the runtime announced it. */
export interface AgentCommand {
  readonly name: string
  /** The verb the runtime knows it by; '' means this panel handles it. */
  readonly kind: string
  /** A stable id, translated here when this build knows it. */
  readonly id: string
  /** The same description as prose, for a command this build has never heard
   *  of — which is exactly the case a hard-coded list could not survive. */
  readonly what: string
}

/* The palette is what the ENGINE announced, not a list kept here.
 *
 * There was a list here, written out in TypeScript, and it was a second source
 * destined to disagree with the first: add a command to the runtime and this
 * browser would not offer it; remove one and this browser would offer something
 * that answers "this runtime does not know that". Neither failure shows up
 * until somebody tries it.
 *
 * So the runtime says what it has when a face arrives, and this draws what it
 * was told. A command added to the agent appears here with no new build of the
 * panel at all.
 *
 * Empty until the first status lands, which is correct: a palette listing
 * things before anything has said they exist is a palette making them up. */
const NO_COMMANDS: readonly AgentCommand[] = []

export function useAgentChat(send: (event: Record<string, unknown>) => void): AgentChatApi {
  const [agent, setAgent] = useState<AgentAvailability>(agentUnknown)
  const [messages, setMessages] = useState<readonly AgentMessage[]>([])
  const [busy, setBusyState] = useState('')
  const [since, setSinceState] = useState(0)
  /* Time actually spent working this session, summed exchange by exchange —
   * what the sidebar's clock shows once a task ends. Mirrored in a ref for
   * the same reason busyRef exists: agent_end must add THIS exchange's span
   * to the total from inside a DataChannel callback. */
  const [worked, setWorked] = useState(0)
  const sinceRef = useRef(0)
  const setSince = useCallback((v: number | ((prev: number) => number)) => {
    setSinceState((prev) => {
      const next = typeof v === 'function' ? v(prev) : v
      sinceRef.current = next
      return next
    })
  }, [])
  /* Mirrored in a ref because handle() runs from a DataChannel callback that
   * closed over its render, and the question it has to answer — "is this chat
   * running right now" — must be about NOW, not about whenever this callback
   * was created. */
  const busyRef = useRef('')
  const setBusy = useCallback((v: string | ((prev: string) => string)) => {
    setBusyState((prev) => {
      const next = typeof v === 'function' ? v(prev) : v
      busyRef.current = next
      return next
    })
  }, [])
  const [sessions, setSessions] = useState<readonly AgentSession[]>([])
  const [viewing, setViewing] = useState(0)
  const [denied, setDenied] = useState<string | null>(null)
  /* What the runtime says it can be asked to do. Held in a ref as well, because
   * ask() runs from a callback and has to read the CURRENT surface to decide
   * whether a typed slash is a command. */
  const [commands, setCommands] = useState<readonly AgentCommand[]>(NO_COMMANDS)
  const commandsRef = useRef<readonly AgentCommand[]>(NO_COMMANDS)

  /* The live conversation, kept aside while a past one is on screen. Reading a
   * transcript must not throw away a run in progress — the agent carries on
   * working whether or not somebody is looking at it, and coming back to find
   * the panel empty would read as the work having been lost. */
  const liveRef = useRef<readonly AgentMessage[]>([])
  /* The exchange the next message belongs to. Held in a ref rather than state
   * because handle() runs from a DataChannel callback that closes over its
   * render, and a stale value there would file a delta under the previous
   * conversation. */
  const chatRef = useRef('')
  const seq = useRef(0)
  const key = () => `m${++seq.current}`

  /* viewing, as a ref, for the same reason chatRef exists: handle() reads it
   * from a callback that closed over an older render. */
  const viewingRef = useRef(0)

  /** appendLive applies a change to the live conversation, whether or not it is
   * the one on screen. A transcript being read does not stop the run behind it
   * from being recorded, and switching back shows what happened meanwhile. */
  const appendLive = useCallback(
    (change: (cur: readonly AgentMessage[]) => readonly AgentMessage[]) => {
      if (viewingRef.current !== 0) {
        liveRef.current = change(liveRef.current)
        return
      }
      setMessages(change)
    },
    [],
  )

  const handle = useCallback((m: Wire) => {
    const t = str(m.t)

    if (t === 'agent_status') {
      /* The catalogue and which providers can be reached, both from the
       * RUNTIME. A browser cannot know either — it is a different program in a
       * different language — so without these its model picker could only be a
       * text field somebody has to already know the answer to type into. */
      const reachable = new Set(
        (Array.isArray(m.reachable) ? m.reachable : []).filter(
          (p): p is string => typeof p === 'string',
        ),
      )
      const models: AgentModel[] = (Array.isArray(m.models) ? (m.models as Wire[]) : []).map(
        (x) => ({
          provider: str(x.provider),
          id: str(x.id),
          note: str(x.note),
          ready: reachable.has(str(x.provider)),
        }),
      )
      const offered: AgentCommand[] = (Array.isArray(m.commands) ? (m.commands as Wire[]) : []).map(
        (x) => ({
          name: str(x.name),
          kind: str(x.kind),
          id: str(x.id),
          what: str(x.what),
        }),
      )
      if (offered.length > 0) {
        commandsRef.current = offered
        setCommands(offered)
      }
      setAgent({
        models,
        present: m.present === true,
        ready: m.ready === true,
        reason: str(m.reason),
        remedy: str(m.remedy),
        provider: str(m.provider),
        model: str(m.model),
        mode: str(m.mode),
      })
      /* A runtime that went away cannot still be working. Cleared here as well
       * as on agent_end, because a daemon that lost the connection mid-answer
       * sends both and either one arriving first must leave the composer
       * usable. */
      if (m.present !== true) {
        setBusy('')
        setSince(0)
        /* And close whatever bubble was open. Streaming is a PROMISE that more
         * is coming, and the only thing that could keep it has gone. Clearing
         * busy alone gave back the composer and left the spinner turning above
         * it — the panel saying, for the rest of the session, that an answer
         * was on its way from a runtime that had disconnected. */
        appendLive(settleStreaming)
      }
      return
    }

    if (t === 'agent_export') {
      /* A finished export, handed straight to the browser as a file.
       *
       * Written by the RUNTIME, not assembled here. It is an audit record, and
       * one with two implementations has two versions of what happened — a
       * panel building its own from the turns it happens to have drawn would
       * miss whatever arrived while nobody was watching. */
      const doc = str(m.document)
      if (!doc) return
      const ext = str(m.format) || 'md'
      const type = ext === 'json' ? 'application/json' : 'text/plain'
      const url = URL.createObjectURL(new Blob([doc], { type: `${type};charset=utf-8` }))
      const a = document.createElement('a')
      a.href = url
      a.download = `sentineldesk-session-${num(m.session) || 'live'}.${ext === 'text' ? 'txt' : ext}`
      document.body.appendChild(a)
      a.click()
      a.remove()
      /* Revoked on the next tick rather than immediately: the click is
       * asynchronous in some browsers, and a URL freed in the same frame gives
       * an empty file with no error anywhere. */
      window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
      return
    }

    if (t === 'agent_forget_denied') {
      /* Held as state rather than shown from inside handle(), because the
       * notice belongs to the drawer that asked and handle() has no way to
       * draw. A null means nothing was refused; an empty string means it was
       * refused and nobody currently holds the controls, which is a different
       * sentence. */
      setDenied(str(m.who))
      return
    }

    if (t === 'agent_history') {
      const session = num(m.session)
      /* Which of the two arrived, read off the field that is present rather
       * than off the session number. Session 0 used to mean "the list", and
       * that reading has no room for the frame this branch exists for: the
       * LIVE conversation is a transcript whose session is 0. */
      if (classifyHistory(m) === 'list') {
        const list = Array.isArray(m.sessions) ? (m.sessions as Wire[]) : []
        setSessions(
          list.map((s) => ({
            id: num(s.id),
            title: str(s.title),
            turns: num(s.turns),
            at: str(s.at),
            live: s.live === true,
          })),
        )
        return
      }
      const turns = m.messages as Wire[]

      /* The conversation the runtime is IN — sent when somebody continued a
       * past one, from here or from the terminal. It replaces the live thread
       * rather than opening a view onto it, and the chat id comes with it.
       *
       * Adopting that id is the part that makes continuing WORK rather than
       * merely look right. The panel mints its own id for a new conversation,
       * and holding on to the old one here would send the next message under a
       * conversation the runtime no longer has open — which the runtime reads
       * as "start a new one", throwing away the history it had just restored. */
      if (classifyHistory(m) === 'live') {
        const restored = restoreTurns(turns, str(m.chat), key)
        if (str(m.chat)) chatRef.current = str(m.chat)
        /* An EMPTY live transcript is a conversation that was deleted from
         * under every face — a fresh start, so the clocks start fresh too. */
        if (restored.length === 0) {
          setBusy('')
          setSince(0)
          setWorked(0)
        }
        liveRef.current = restored
        viewingRef.current = 0
        setViewing(0)
        setMessages(restored)
        return
      }
      /* Stashed before the replacement, and only the first time: opening a
       * second transcript without coming back must not overwrite the live
       * conversation with the first transcript. */
      setMessages((cur) => {
        if (viewingRef.current === 0) liveRef.current = cur
        return restoreTurns(turns, `past-${session}`, key)
      })
      viewingRef.current = session
      setViewing(session)
      return
    }

    /* Everything below belongs to the LIVE conversation. A transcript on screen
     * does not swallow it — the panel switches back when the person leaves the
     * drawer, and the run carries on being recorded meanwhile. */

    /* One value, two names on the wire. The panel's own frames — delta, step,
     * end — call it `chat`. agent_chat calls it `id`, because agent_chat
     * predates this panel and named the field for what it did then: tie a
     * question to its answer.
     *
     * Reading only `chat` is what made every reply appear TWICE. The deltas
     * streamed into a bubble keyed by the run id; the whole answer arrived
     * afterwards with that key under its other name, matched nothing, and was
     * appended as a second bubble saying the same thing. Both names, and the
     * finished turn lands on the bubble that was already showing it. */
    const chat = str(m.chat) || str(m.id)

    if (t === 'agent_chat') {
      const role = (str(m.role) || 'system') as AgentMessage['role']
      const text = str(m.text)
      const options = Array.isArray(m.options)
        ? (m.options as unknown[]).filter((o): o is string => typeof o === 'string')
        : []
      appendLive((cur) => {
        if (role === 'agent') {
          /* The whole answer, sent when the run ends. It REPLACES a bubble this
           * panel streamed only if the panel did not stream one — which is the
           * rule that lets a browser opened mid-run end up with the finished
           * text while one that watched keeps its narration and its steps. */
          const i = lastIndex(cur, chat, 'agent')
          if (i >= 0 && cur[i].streamed) {
            return cur.map((x, n) => (n === i ? { ...x, streaming: false } : x))
          }
          if (i >= 0) {
            return cur.map((x, n) =>
              n === i ? { ...x, text, streaming: false } : x,
            )
          }
        }
        return [
          ...cur,
          {
            key: key(),
            chat,
            role,
            text: options.length ? `${text}\n\n${options.join(' · ')}` : text,
            at: num(m.at),
            streaming: false,
            steps: [],
            ending: null,
            streamed: false,
          },
        ]
      })
      return
    }

    if (t === 'agent_delta') {
      const text = str(m.text)
      if (!text) return
      /* The clock starts when this panel SEES work, not only when it asked
       * for it — a run started from the terminal or another browser ticks
       * here all the same. `|| was` keeps the true start on the panel that
       * did ask. */
      setSince((was) => was || Date.now())
      appendLive((cur) => {
        const i = lastIndex(cur, chat, 'agent')
        /* Appended to the open bubble, or a new one. The join is a blank line
         * because today a delta is a whole turn's prose rather than a token —
         * see the note in the runtime's reporter. When real streaming lands the
         * separator becomes '' and nothing else here changes. */
        if (i >= 0 && cur[i].streaming) {
          return cur.map((x, n) =>
            n === i
              ? { ...x, text: x.text ? `${x.text}\n\n${text}` : text, streamed: true }
              : x,
          )
        }
        return [
          ...cur,
          {
            key: key(),
            chat,
            role: 'agent' as const,
            text,
            at: num(m.at),
            streaming: true,
            steps: [],
            ending: null,
            streamed: true,
          },
        ]
      })
      return
    }

    if (t === 'agent_step') {
      const step: AgentStep = {
        key: key(),
        tool: str(m.tool),
        detail: str(m.detail),
        turn: num(m.turn),
      }
      setSince((was) => was || Date.now())
      const running = isRunning(chat, busyRef.current)
      appendLive((cur) => {
        const i = lastIndex(cur, chat, 'agent')
        if (i >= 0 && cur[i].streaming) {
          return cur.map((x, n) =>
            n === i ? { ...x, steps: [...x.steps, step] } : x,
          )
        }
        /* Something happened that is NOT part of an exchange: a command's
         * answer, mostly — a conversation compacted, a fact remembered, a
         * model switched, a refusal.
         *
         * Its own row, and NOT a streaming one. This used to open the empty
         * bubble below whatever arrived, and a bubble opened that way can only
         * be closed by agent_end — which a command never sends, because a
         * command is not an exchange. So typing `/model` left a "Thinking…"
         * spinner on screen with nothing behind it and no way to clear it. */
        if (!running) {
          return [
            ...cur,
            {
              key: key(),
              chat,
              role: 'system' as const,
              text: '',
              at: num(m.at),
              streaming: false,
              steps: [step],
              ending: null,
              streamed: false,
            },
          ]
        }
        /* A tool called before the model said anything — which is most first
         * turns. The bubble opens empty and the steps go in it, so the work is
         * visible before there is any prose to show. */
        return [
          ...cur,
          {
            key: key(),
            chat,
            role: 'agent' as const,
            text: '',
            at: num(m.at),
            streaming: true,
            steps: [step],
            ending: null,
            streamed: true,
          },
        ]
      })
      return
    }

    if (t === 'agent_end') {
      /* How long the exchange ran, measured before the clock resets. The
       * wire does not carry it, and does not need to: this panel watched. */
      const ms = sinceRef.current ? Date.now() - sinceRef.current : 0
      const ending: AgentEnding = {
        ok: m.ok === true,
        text: str(m.text),
        turns: num(m.turns),
        calls: num(m.calls),
        inToks: num(m.in_toks),
        outToks: num(m.out_toks),
        stoppedBy: str(m.stopped_by),
        ms,
      }
      if (ms) setWorked((w) => w + ms)
      setBusy((b) => (b === chat ? '' : b))
      setSince(0)
      appendLive((cur) => {
        const i = lastIndex(cur, chat, 'agent')
        if (i >= 0) {
          return cur.map((x, n) =>
            n === i ? { ...x, streaming: false, ending } : x,
          )
        }
        /* An exchange that ended without the agent ever speaking — the runtime
         * was not there, or it failed before its first word. Drawn as its own
         * line rather than dropped: a message sent and nothing back is exactly
         * the case that must not look like nothing happened. */
        return [
          ...cur,
          {
            key: key(),
            chat,
            role: 'system' as const,
            text: ending.text || (ending.ok ? '' : 'the agent did not answer'),
            at: num(m.at),
            streaming: false,
            steps: [],
            ending,
            streamed: false,
          },
        ]
      })
      return
    }
  }, [])

  const ask = useCallback(
    (text: string) => {
      const body = text.trim()
      if (!body) return
      /* Reading a transcript and then typing means the person is done reading.
       * Snapping back before the send is what keeps their own message from
       * landing on a screen showing last week's conversation. */
      if (viewingRef.current !== 0) {
        viewingRef.current = 0
        setViewing(0)
        setMessages(liveRef.current)
      }
      const chat = chatRef.current || `c-${Date.now()}-${++seq.current}`
      chatRef.current = chat

      /* A leading slash is a command, exactly as it is at the terminal's own
       * prompt. Parsed here rather than given a box of its own, because the two
       * faces have to agree about what typing `/compact` does — a panel with
       * its own syntax would be a second vocabulary for one idea.
       *
       * It does NOT set busy: a command is not an exchange. Sending one while
       * the agent works is allowed and normal, and compacting mid-run is
       * exactly when somebody wants it. */
      const cmd = matchCommand(body, commandsRef.current)
      if (cmd) {
        if (cmd.kind === '') {
          send({ t: 'agent_cancel', chat })
          return
        }
        send({ t: 'agent_command', chat, kind: cmd.kind, text: cmd.text })
        return
      }

      setBusy(chat)
      /* Started here rather than on the first delta. The wait BEFORE the first
       * word is the part somebody wonders about — a model that thinks for
       * forty seconds and then writes fast would show a clock that begins at
       * forty if this waited for text to arrive. */
      setSince((was) => was || Date.now())
      send({ t: 'agent_say', chat, text: body })
    },
    [send],
  )

  const stop = useCallback(() => {
    if (!chatRef.current) return
    send({ t: 'agent_cancel', chat: chatRef.current })
  }, [send])

  const reset = useCallback(() => {
    chatRef.current = ''
    liveRef.current = []
    viewingRef.current = 0
    setViewing(0)
    setBusy('')
    setSince(0)
    setWorked(0)
    setMessages([])
  }, [])

  const hydrate = useCallback(() => {
    send({ t: 'agent_history', session: -1 })
  }, [send])

  const rename = useCallback(
    (id: number, title: string) => {
      if (id > 0) send({ t: 'agent_rename', session: id, text: title })
    },
    [send],
  )

  const loadSessions = useCallback(() => {
    send({ t: 'agent_history', session: 0 })
  }, [send])

  /* Deleting asks, and does not touch the local list. What comes back is the
   * refreshed sessions list, broadcast to everybody in the room — so a row
   * removed here disappears for the person watching in another browser at the
   * same moment, and a delete the daemon refused leaves the row visibly there
   * instead of vanishing optimistically and returning on the next load. */
  const forget = useCallback(
    (id: number) => {
      if (id > 0) send({ t: 'agent_forget', session: id })
    },
    [send],
  )

  const forgetAll = useCallback(() => {
    send({ t: 'agent_forget', all: true })
  }, [send])

  const clearDenied = useCallback(() => setDenied(null), [])

  const exportSession = useCallback(
    (id: number, format = 'md') => {
      send({ t: 'agent_export', session: id, format })
    },
    [send],
  )

  /* Continue a past conversation, which is a different act from reading one.
   * Asked of the RUNTIME rather than done here: what has to change is which
   * conversation the engine's one Runner is holding, and a browser cannot
   * reach into that. What comes back is the transcript above, under session 0,
   * for this panel and for every other face at the same time. */
  const resume = useCallback(
    (id: number) => {
      send({ t: 'agent_command', kind: 'resume', text: String(id) })
    },
    [send],
  )

  const openSession = useCallback(
    (id: number) => {
      if (id === 0) {
        viewingRef.current = 0
        setViewing(0)
        setMessages(liveRef.current)
        return
      }
      send({ t: 'agent_history', session: id })
    },
    [send],
  )

  return {
    commands,
    agent,
    messages,
    busy,
    since,
    worked,
    sessions,
    viewing,
    denied,
    handle,
    ask,
    stop,
    reset,
    loadSessions,
    hydrate,
    rename,
    openSession,
    resume,
    forget,
    forgetAll,
    clearDenied,
    exportSession,
  }
}

/** lastIndex finds the newest bubble of one role in one exchange.
 *
 * Searched from the end rather than tracked in a ref, because the thing being
 * looked for is "the bubble a delta belongs to" and that is a property of the
 * list, not a cursor that can fall out of step with it. */
/** settleStreaming closes every bubble that is still streaming.
 *
 * For the moment the thing that would have finished them is gone. Returns the
 * list unchanged when there is nothing open, so this can be applied freely
 * without causing a render.
 */
export function settleStreaming(
  cur: readonly AgentMessage[],
): readonly AgentMessage[] {
  if (!cur.some((x) => x.streaming)) return cur
  return cur.map((x) => (x.streaming ? { ...x, streaming: false } : x))
}

/** isRunning answers whether a frame belongs to the exchange in flight.
 *
 * # Why this is not `busy === chat`
 *
 * Because both sides use the empty string as a sentinel, and they mean opposite
 * things by it: an empty `busy` is "nothing is running", and an empty `chat` on
 * an incoming frame is "this belongs to no conversation" — which is what the
 * runtime sends for something that happened outside one, like emptying the
 * history while no conversation is open.
 *
 * Compared directly, those two nothings matched. The frame was read as part of
 * a live exchange, so it opened a STREAMING bubble, and a streaming bubble can
 * only be closed by the end of an exchange that was never running. Deleting the
 * history left a "Thinking…" spinner on screen for ever, with the note that the
 * delete had worked sitting inside it.
 */
export function isRunning(chat: string, busy: string): boolean {
  if (chat === '') return false
  return chat === busy
}

function lastIndex(
  list: readonly AgentMessage[],
  chat: string,
  role: AgentMessage['role'],
): number {
  for (let i = list.length - 1; i >= 0; i--) {
    if (list[i].chat === chat && list[i].role === role) return i
  }
  return -1
}


/* matchCommand reads a typed line as a slash command, or says it is not one.
 *
 * An unknown slash is NOT a command and falls through to being sent as a
 * message. That is the opposite of the terminal, which refuses one — and
 * deliberately so: a terminal's prompt is a command line where a slash is a
 * verb, while this is a chat box where somebody may reasonably begin a sentence
 * with a path. Refusing `/opt/desktop is where it lives` would be the panel
 * inventing a syntax error in the middle of a conversation. */
export function matchCommand(
  line: string,
  offered: readonly AgentCommand[],
): { kind: string; text: string } | null {
  if (!line.startsWith('/')) return null
  const [verb, ...rest] = line.split(' ')
  const found = offered.find((c) => c.name === verb)
  if (!found) return null
  return { kind: found.kind, text: rest.join(' ').trim() }
}
