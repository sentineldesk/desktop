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

/* The conversation's pieces, extracted from the old side panel so the agent
 * workspace and anything else can draw the same thread the same way. The
 * comments travelled with the code — each one explains a decision that was
 * paid for once and should not be paid for again. */

import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { AgentChatApi, AgentMessage } from './agentChat'
import type { AgentConsoleApi } from './agentConsole'
import s from './AgentChat.module.css'

/* ---- one bubble ----------------------------------------------------------- */

export function Bubble({ m, since }: { m: AgentMessage; since: number }) {
  const { t } = useTranslation()
  const human = m.role === 'human'
  const kind = human ? s.human : m.role === 'system' ? s.system : s.agent

  /* The chain of thought is FOLDED by default, and open while the run is live.
   *
   * Those are the two things somebody wants and they are not the same thing:
   * watching it work, and reading what it said afterwards. Left permanently
   * open, a finished answer sits under twenty lines of tool calls and the
   * reader scrolls past its own conversation; left permanently shut, the wait
   * is a blank bubble again. So it opens itself while streaming and folds when
   * the run ends — and once somebody has clicked it, their choice wins, which
   * is what `touched` is for: a fold that reopens on the next delta is one that
   * cannot be closed. */
  const [open, setOpen] = useState(false)
  const touched = useRef(false)
  useEffect(() => {
    if (!touched.current) setOpen(m.streaming)
  }, [m.streaming])
  const toggle = () => {
    touched.current = true
    setOpen((v) => !v)
  }

  return (
    <div className={`${s.row} ${human ? s.rowHuman : s.rowAgent}`}>
      <div className={`${s.bubble} ${kind}`}>
        {m.text}
        {m.streaming && m.text === '' ? <Thinking since={since} /> : null}
        {m.streaming ? <i className={s.caret} /> : null}

        {m.steps.length > 0 ? (
          <div className={s.chain}>
            <button className={s.chainHead} onClick={toggle} aria-expanded={open}>
              <svg
                className={`${s.chev} ${open ? s.chevOpen : ''}`}
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <path d="M9 6l6 6-6 6" />
              </svg>
              <span>{t('chat.steps', { n: m.steps.length })}</span>
              {!open && m.streaming ? (
                /* The newest tool, on the folded head. A collapsed chain during
                 * a long run would otherwise say only how many steps there
                 * are, which does not answer what it is doing right now. */
                <span className={s.chainNow}>{m.steps[m.steps.length - 1].tool}</span>
              ) : null}
            </button>
            {open ? (
              <div className={s.steps}>
                {m.steps.map((step) => (
                  <div
                    key={step.key}
                    className={`${s.step} ${step.tool === 'interrupted' ? s.stepBreak : ''}`}
                  >
                    <span className={s.tool}>{step.tool}</span>
                    <span className={s.detail}>{step.detail}</span>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        ) : null}

        {m.ending ? (
          <div
            className={`${s.ending} ${
              !m.ending.ok ? s.endingBad : m.ending.stoppedBy ? s.endingWarn : ''
            }`}
          >
            {m.ending.stoppedBy ? (
              <span>{t('chat.stoppedBy', { why: m.ending.stoppedBy })}</span>
            ) : null}
            {m.ending.turns ? (
              <span>{t('chat.turns', { n: m.ending.turns })}</span>
            ) : null}
            {m.ending.calls ? (
              <span>{t('chat.calls', { n: m.ending.calls })}</span>
            ) : null}
            {m.ending.inToks || m.ending.outToks ? (
              <span>{t('chat.tokens', { n: m.ending.inToks + m.ending.outToks })}</span>
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  )
}

/* ---- the three states that are not "ready" -------------------------------- */

/* One card for all of them, because what the reader needs is the same in each:
 * what is wrong, and the command that fixes it. Which of the three it is
 * changes only the heading — the daemon already decided, and sent the reason
 * and the remedy as separate fields precisely so this component never has to
 * find a command inside a sentence. */
export function Gate(props: {
  agent: AgentChatApi['agent']
  copied: boolean
  onCopy(): void
}) {
  const { t } = useTranslation()
  const { agent } = props
  const heading = agent.present ? t('chat.gateUnconfigured') : t('chat.gateAbsent')

  return (
    <div className={s.gate}>
      <div className={s.gateTitle}>{heading}</div>
      {agent.reason ? <div className={s.gateWhy}>{agent.reason}</div> : null}
      {agent.remedy ? (
        <div className={s.remedy}>
          <code>{agent.remedy}</code>
          <button className={s.copy} onClick={props.onCopy}>
            {props.copied ? t('chat.copied') : t('chat.copy')}
          </button>
        </div>
      ) : null}
    </div>
  )
}

/* ---- the terminal --------------------------------------------------------- */

/* A window over the thread, not a replacement for it. It keeps the chat
 * visible behind it and closes back to it, which is the whole relationship:
 * the workspace is where you work and this is where you go for what the
 * workspace cannot do yet — /connect above all. */
export function Console(props: { term: AgentConsoleApi; onClose(): void }) {
  const { t } = useTranslation()
  const host = useRef<HTMLDivElement>(null)

  /* Mounted through the hook rather than here: the terminal owns a canvas, a
   * cursor and a resize observer, and this component only knows where to put
   * them. */
  useEffect(() => {
    const node = host.current
    if (!node) return
    return props.term.mount(node)
  }, [props.term])

  return (
    <div className={s.console} role="dialog" aria-label={t('chat.console')}>
      <header className={s.consoleHead}>
        <span className={s.consoleTitle}>{t('chat.console')}</span>
        <button className={s.iconBtn} onClick={props.onClose} title={t('chat.close')}>
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M6 6l12 12M18 6L6 18" />
          </svg>
        </button>
      </header>
      <div className={s.consoleBody} ref={host} />
    </div>
  )
}

/* ---- the pieces that answer "how long, and what is it doing" -------------- */

/* Elapsed, counted from a timestamp rather than incremented.
 *
 * A counter that adds a second per tick drifts, and drifts badly in a
 * background tab where the browser throttles timers to once a minute — a
 * four-minute run would report forty seconds. Ticking is only what makes it
 * redraw; the number always comes from subtracting. */
export function useElapsed(since: number): string {
  const [, tick] = useState(0)
  useEffect(() => {
    if (!since) return
    const h = window.setInterval(() => tick((n) => n + 1), 1000)
    return () => window.clearInterval(h)
  }, [since])
  if (!since) return ''
  const secs = Math.max(0, Math.round((Date.now() - since) / 1000))
  const m = Math.floor(secs / 60)
  return m ? `${m}m ${secs % 60}s` : `${secs}s`
}

/* The monitor that blinks while the agent thinks. A screen rather than a
 * spinner, because what is happening is a desktop being driven. */
export function Thinking(props: { since: number }) {
  const { t } = useTranslation()
  const elapsed = useElapsed(props.since)
  return (
    <span className={s.thinking} role="status">
      <svg className={s.monitor} viewBox="0 0 24 24" aria-hidden="true">
        <rect className={s.monitorGlow} x="3.5" y="4.5" width="17" height="11" rx="1.5" />
        <rect x="3.5" y="4.5" width="17" height="11" rx="1.5" />
        <path d="M9 19h6M12 15.5V19" />
      </svg>
      <span>{t('chat.thinking')}</span>
      {elapsed ? <span className={s.elapsed}>{elapsed}</span> : null}
    </span>
  )
}

/* When a past conversation happened, in the reader's own locale.
 *
 * The wire carries whatever the runtime's database wrote — an RFC 3339 stamp
 * with nanoseconds. Parsed HERE and not on the way over, because the daemon
 * passes the string through without reading it and the browser is the only
 * party that knows which timezone and which language to render it in.
 * Unparseable falls back to the original text. */
export function when(iso: string): string {
  const ms = Date.parse(iso)
  if (!isFinite(ms)) return iso
  return new Date(ms).toLocaleString(undefined, {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

/* ---- the header's one line ------------------------------------------------ */

/* Built from FIELDS, never by matching on the reason text. The daemon sends
 * present and ready as booleans for exactly this: a panel that decided its own
 * state by reading English out of a sentence would break the first time
 * somebody reworded it, or translated it. */
export function statusLine(
  agent: AgentChatApi['agent'],
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  if (agent.ready) {
    const parts = [t('chat.connected')]
    if (agent.model) parts.push(agent.model)
    if (agent.mode) parts.push(agent.mode)
    return parts.join(' · ')
  }
  if (agent.present) return t('chat.noModel')
  return t('chat.offline')
}
