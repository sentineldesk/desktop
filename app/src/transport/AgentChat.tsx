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

/* The chat panel: a conversation with the agent, beside the desktop it drives.
 *
 * What makes this different from a chat box is that the agent ACTS, so the
 * steps are drawn inline with the prose rather than hidden behind a
 * disclosure. Somebody watching this panel is watching work happen on the
 * screen next to it, and the two have to line up.
 *
 * It knows nothing about how the desktop reaches the agent. Everything arrives
 * on the DataChannel this session already had, which is why the panel behaves
 * identically whether the runtime is a process in the same container or is not
 * there at all.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import i18n from '../i18n'

import type { AgentChatApi, AgentMessage } from './agentChat'
import type { AgentConsoleApi } from './agentConsole'
import s from './AgentChat.module.css'

export function AgentChat(props: {
  chat: AgentChatApi
  console: AgentConsoleApi
  onClose(): void
}) {
  const { t } = useTranslation()
  const { chat } = props
  const term = props.console
  const { agent } = chat

  const [draft, setDraft] = useState('')
  const [drawer, setDrawer] = useState(false)
  /* The "delete everything" confirmation, and it lives here rather than inside
   * the drawer so that closing the drawer forgets it: a half-answered
   * confirmation waiting behind a closed panel is one somebody finishes by
   * accident later. */
  const [wipe, setWipe] = useState(false)
  const [copied, setCopied] = useState(false)
  const width = useChatWidth()

  const threadRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  /* Stick to the bottom while the answer grows, and STOP sticking the moment
   * somebody scrolls up. An agent's answer arrives over a minute or two; a
   * panel that yanks the reader back down every time a delta lands makes it
   * impossible to read what already arrived. */
  const stick = useRef(true)
  const onScroll = useCallback(() => {
    const el = threadRef.current
    if (!el) return
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
  }, [])
  useLayoutEffect(() => {
    const el = threadRef.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [chat.messages])

  /* Opening the panel puts the caret in the box. Somebody who pressed the
   * button meant to type. */
  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  /* The composer grows with the text, up to the cap the stylesheet sets.
   * Measured rather than counted in rows, because a pasted paragraph and five
   * short lines are the same height and different row counts. */
  const grow = useCallback(() => {
    const el = inputRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }, [])
  useEffect(grow, [draft, grow])

  /* The model picker, opened by `/model` with nothing after it.
   *
   * A list rather than the runtime answering "no model named", which is what it
   * did and which is useless: somebody typing `/model` is asking WHICH, and
   * being told they did not say is a program answering a question nobody had.
   * The terminal has had a picker for this all along; this is the same question
   * with the same answer. */
  const [picker, setPicker] = useState(false)

  /* Only while the VERB is being typed. See the palette's own note. */
  const palette = (() => {
    const typed = draft.trim()
    if (!typed.startsWith('/') || typed.includes(' ')) return []
    return chat.commands.filter((c) => c.name.startsWith(typed))
  })()

  const busy = chat.busy !== ''
  const past = chat.viewing !== 0
  /* Typing is allowed while the agent works: the message is steered into the
   * run in progress, which is what a correction is. It is NOT allowed with no
   * runtime — there is nowhere for it to go, and a box that accepts input it
   * cannot deliver is the failure this whole panel exists to make visible. */
  const canType = agent.ready && !past

  const submit = useCallback(() => {
    const body = draft.trim()
    if (!body || !agent.ready) return
    /* `/model` with nothing after it is a question, not a command. Answered
     * here with the list rather than sent to the runtime, which can only say
     * that no model was named. */
    if (body === '/model') {
      setPicker(true)
      setDraft('')
      return
    }
    /* `/connect` opens the TERMINAL rather than starting a sign-in from here.
     *
     * The flow is a conversation — a link to open, a page to sign in on, a code
     * to bring back — and the agent's own view already runs the whole of it,
     * with the link clickable and a field for the code. Reproducing that in the
     * panel would be a second implementation of an interactive flow whose only
     * job is to be identical to the first.
     *
     * Sending the command instead is what this used to do, and it was worse
     * than useless: the URL came back as one line of a step row, with nowhere
     * to type the code, so the person was left holding half a flow. */
    if (body === '/connect' || body.startsWith('/connect ')) {
      term.start()
      setDraft('')
      return
    }
    chat.ask(body)
    setDraft('')
    /* Reset by hand: the effect above only shrinks it on the next render, and
     * the box would stay tall for a frame after sending a long message. */
    const el = inputRef.current
    if (el) el.style.height = 'auto'
  }, [draft, agent.ready, chat])

  const openDrawer = useCallback(() => {
    chat.loadSessions()
    setWipe(false)
    chat.clearDenied()
    setDrawer(true)
  }, [chat])

  const copyRemedy = useCallback(() => {
    if (!agent.remedy) return
    void navigator.clipboard
      ?.writeText(agent.remedy)
      .then(() => {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1600)
      })
      .catch(() => {})
  }, [agent.remedy])

  return (
    <aside className={s.panel} aria-label={t('chat.title')}>
      {/* Double-click puts it back. A drag with no way home is one somebody
          has to guess their way out of. */}
      <div
        className={s.grip}
        onPointerDown={width.onGrab}
        onDoubleClick={width.reset}
        role="separator"
        aria-orientation="vertical"
        aria-label={t('chat.resize')}
      />
      {/* Drawn here rather than left to the browser's `title`, which appears
          after a delay, at the pointer, over the thing being resized. */}
      <div className={s.gripHint} aria-hidden="true">
        {t('chat.resize')}
      </div>
      <header className={s.head}>
        <button
          className={s.iconBtn}
          onClick={drawer ? () => setDrawer(false) : openDrawer}
          title={t('chat.history')}
          aria-expanded={drawer}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M4 7h16M4 12h16M4 17h10" />
          </svg>
        </button>

        <div className={s.headText}>
          <span className={s.title}>{t('chat.title')}</span>
          <span className={s.state}>
            <i className={`${s.dot} ${agent.ready ? s.dotReady : agent.present ? s.dotHalf : ''}`} />
            {statusLine(agent, t)}
          </span>
        </div>

        {busy ? (
          <button className={s.iconBtn} onClick={chat.stop} title={t('chat.stop')}>
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <rect x="7" y="7" width="10" height="10" rx="2" />
            </svg>
          </button>
        ) : null}

        {/* The way out. Everything the agent's own terminal can do, including
            what this panel has not learned yet — see agentConsole.ts. */}
        <button
          className={`${s.iconBtn} ${term.open ? s.iconOn : ''}`}
          onClick={() => (term.open ? term.stop() : term.start())}
          title={t('chat.console')}
          aria-pressed={term.open}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M4 5h16v14H4zM7 9l3 3-3 3M13 15h4" />
          </svg>
        </button>

        <button
          className={s.iconBtn}
          onClick={() => chat.exportSession(0)}
          title={t('chat.exportLive')}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M12 4v11M8 11l4 4 4-4M5 19h14" />
          </svg>
        </button>

        <button className={s.iconBtn} onClick={chat.reset} title={t('chat.new')}>
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M12 5v14M5 12h14" />
          </svg>
        </button>

        <button className={s.iconBtn} onClick={props.onClose} title={t('chat.close')}>
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M6 6l12 12M18 6L6 18" />
          </svg>
        </button>
      </header>

      {term.open ? <Console term={term} onClose={term.stop} /> : null}

      {past ? (
        <div className={s.viewing} role="status">
          <span>{t('chat.viewingPast')}</span>
          {/* The reason somebody opened an old conversation is usually to pick
              it back up, so the offer is here — beside what they are reading —
              rather than only as an icon in the drawer they have just left. */}
          <button onClick={() => chat.resume(chat.viewing)}>{t('chat.continueHere')}</button>
          <button onClick={() => chat.openSession(0)}>{t('chat.backToLive')}</button>
        </div>
      ) : null}

      <div className={s.thread} ref={threadRef} onScroll={onScroll}>
        {!agent.ready ? <Gate agent={agent} copied={copied} onCopy={copyRemedy} /> : null}

        {chat.messages.length === 0 && agent.ready ? (
          <div className={s.empty}>
            <div className={s.emptyTitle}>{t('chat.emptyTitle')}</div>
            <div>{t('chat.emptyHint')}</div>
          </div>
        ) : null}

        {chat.messages.map((m) => (
          <Bubble key={m.key} m={m} since={chat.since} />
        ))}
      </div>

      <div className={s.composer}>
        {/* The models, when somebody asked which. Reachable ones first and the
            rest below, marked — because "which exist" and "which can I use" are
            different questions, and a person deciding whether to go and find a
            key needs to see the locked ones rather than have them hidden. */}
        {picker ? (
          <div className={s.palette} role="listbox">
            {agent.models.length === 0 ? (
              <div className={s.paletteRow}>
                <span className={s.paletteWhat}>{t('chat.noModels')}</span>
              </div>
            ) : (
              [...agent.models]
                /* Ordered by how likely you are to want it, which is not the
                 * catalogue's order.
                 *
                 * The provider you are ON comes first. Somebody who opens this
                 * while running claude-code is almost always switching between
                 * its models, and having to scroll past nineteen entries from
                 * providers they have no key for to find the three that answer
                 * is the picker doing the opposite of picking.
                 *
                 * Then the rest of what can be reached, then what cannot —
                 * shown rather than hidden, because "which exist" and "which
                 * can I use" are different questions and somebody deciding
                 * whether to go and find a key needs to see the locked ones. */
                .sort((a, b) => {
                  const mine = (x: typeof a) => Number(x.provider === agent.provider)
                  return mine(b) - mine(a) || Number(b.ready) - Number(a.ready)
                })
                .map((mo) => {
                  const name = `${mo.provider}/${mo.id}`
                  const on = name === `${agent.provider}/${agent.model}`
                  return (
                    <button
                      key={name}
                      className={s.paletteRow}
                      disabled={!mo.ready}
                      onClick={() => {
                        chat.ask(`/model ${name}`)
                        setPicker(false)
                      }}
                    >
                      <span className={s.paletteName}>
                        {on ? '● ' : ''}
                        {name}
                      </span>
                      <span className={s.paletteWhat}>
                        {mo.ready ? mo.note : t('chat.modelLocked')}
                      </span>
                    </button>
                  )
                })
            )}
          </div>
        ) : null}

        {/* The palette, while a slash is being typed and only then. Once there
            is a space the person is writing an argument, and a list hovering
            over their sentence is noise covering the thing they are working
            on — the same rule the terminal's own palette follows. */}
        {palette.length > 0 ? (
          <div className={s.palette} role="listbox">
            {palette.map((c) => (
              <button
                key={c.name}
                className={s.paletteRow}
                onClick={() => {
                  setDraft(c.name + ' ')
                  inputRef.current?.focus()
                }}
              >
                <span className={s.paletteName}>{c.name}</span>
                {/* Translated when this build knows the id, and the runtime's
                    own prose when it does not — which is exactly the case a
                    hard-coded palette could not survive: a command added to the
                    agent still describes itself here, in English, rather than
                    appearing as a blank row or not appearing at all. */}
                <span className={s.paletteWhat}>
                  {i18n.exists(`chat.${c.id}`) ? t(`chat.${c.id}`) : c.what}
                </span>
              </button>
            ))}
          </div>
        ) : null}

        <div className={s.box}>
          <textarea
            ref={inputRef}
            className={s.input}
            rows={1}
            value={draft}
            disabled={!canType}
            placeholder={
              past ? t('chat.placeholderPast') : agent.ready ? t('chat.placeholder') : t('chat.placeholderOff')
            }
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              /* Enter sends, Shift+Enter breaks the line. The desktop is one
               * key away and the key handler there is global, so the stop is
               * what keeps a message from also typing into the screen. */
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                submit()
              }
              e.stopPropagation()
            }}
            onKeyUp={(e) => e.stopPropagation()}
          />
          <button
            className={s.send}
            onClick={submit}
            disabled={!canType || draft.trim() === ''}
            title={t('chat.send')}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <path d="M4 12l16-8-6 8 6 8z" />
            </svg>
          </button>
        </div>
        <div className={s.hint}>
          <span>{busy ? t('chat.working') : t('chat.enterToSend')}</span>
          {agent.model ? <span>{agent.model}</span> : null}
        </div>
      </div>

      {drawer ? (
        <div className={s.drawer}>
          <header className={s.head}>
            <button
              className={s.iconBtn}
              onClick={() => setDrawer(false)}
              title={t('chat.back')}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M15 6l-6 6 6 6" />
              </svg>
            </button>
            <div className={s.headText}>
              <span className={s.title}>{t('chat.history')}</span>
            </div>
            {chat.sessions.length > 0 ? (
              <button
                className={s.iconBtn}
                onClick={() => setWipe(true)}
                title={t('chat.forgetAll')}
              >
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path d="M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12" />
                </svg>
              </button>
            ) : null}
            <button
              className={s.iconBtn}
              onClick={() => {
                chat.reset()
                setDrawer(false)
              }}
              title={t('chat.new')}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M12 5v14M5 12h14" />
              </svg>
            </button>
          </header>

          {/* The refusal, when the controls are somebody else's. Shown in the
              drawer that asked rather than as a toast, because that is where
              the button is and where the person is looking. */}
          {chat.denied !== null ? (
            <div className={s.denied} role="alert">
              <span>
                {chat.denied
                  ? t('chat.forgetNeedsControlFrom', { who: chat.denied })
                  : t('chat.forgetNeedsControl')}
              </span>
              <button onClick={chat.clearDenied}>{t('chat.ok')}</button>
            </div>
          ) : null}

          {/* Deleting everything ASKS. One button that empties the whole
              ledger, next to a button that opens a conversation, is a misclick
              with nothing to undo it — the rows are gone from the runtime's
              database, not hidden. Deleting ONE does not ask: it names what it
              is about to remove, it is one row, and a confirmation on every
              small destructive act teaches people to click through them. */}
          {wipe ? (
            <div className={s.confirm} role="alertdialog">
              <div className={s.confirmTitle}>{t('chat.forgetAllSure')}</div>
              <div className={s.confirmWhy}>
                {/* What will ACTUALLY go, not how many rows are on screen. The
                    runtime keeps the conversation it is in — it is still
                    writing to that record — so counting every row promised one
                    more deletion than the confirmation could deliver. A
                    confirmation for something irreversible is the one number
                    that has to be right. */}
                {t('chat.forgetAllWhy', { n: chat.sessions.filter((x) => !x.live).length })}
                {chat.sessions.some((x) => x.live) ? ` ${t('chat.forgetAllKeepsLive')}` : ''}
              </div>
              <div className={s.confirmRow}>
                <button onClick={() => setWipe(false)}>{t('chat.cancel')}</button>
                <button
                  className={s.danger}
                  onClick={() => {
                    chat.forgetAll()
                    setWipe(false)
                  }}
                >
                  {t('chat.forgetAllDo')}
                </button>
              </div>
            </div>
          ) : null}

          <div className={s.drawerList}>
            {chat.sessions.length === 0 ? (
              <div className={s.empty}>{t('chat.noHistory')}</div>
            ) : (
              chat.sessions.map((session) => (
                <div
                  key={session.id}
                  className={`${s.session} ${chat.viewing === session.id ? s.sessionOn : ''}`}
                >
                  <button
                    className={s.sessionOpen}
                    onClick={() => {
                      chat.openSession(session.id)
                      setDrawer(false)
                    }}
                  >
                    <span className={s.sessionTitle}>{session.title}</span>
                    <span className={s.sessionMeta}>
                      {when(session.at)} · {t('chat.turns', { n: session.turns })}
                      {session.live ? ` · ${t('chat.thisOne')}` : ''}
                    </span>
                  </button>
                  {session.live ? null : (
                    <button
                      className={s.sessionDrop}
                      onClick={() => {
                        chat.resume(session.id)
                        setDrawer(false)
                      }}
                      title={t('chat.continueOne')}
                      aria-label={t('chat.continueOne')}
                    >
                      <svg viewBox="0 0 24 24" aria-hidden="true">
                        <path d="M4 12h13M13 7l5 5-5 5" />
                      </svg>
                    </button>
                  )}
                  <button
                    className={s.sessionDrop}
                    onClick={() => chat.exportSession(session.id)}
                    title={t('chat.export')}
                    aria-label={t('chat.export')}
                  >
                    <svg viewBox="0 0 24 24" aria-hidden="true">
                      <path d="M12 4v11M8 11l4 4 4-4M5 19h14" />
                    </svg>
                  </button>
                  {/* Not offered on the conversation the runtime is in. It
                      would be refused — the engine is still writing to that
                      record — and a button whose only outcome is a refusal is
                      worse than no button: it reads as broken. */}
                  {session.live ? null : (
                    <button
                      className={s.sessionDrop}
                      onClick={() => chat.forget(session.id)}
                      title={t('chat.forgetOne')}
                      aria-label={t('chat.forgetOne')}
                    >
                      <svg viewBox="0 0 24 24" aria-hidden="true">
                        <path d="M6 6l12 12M18 6L6 18" />
                      </svg>
                    </button>
                  )}
                </div>
              ))
            )}
          </div>
        </div>
      ) : null}
    </aside>
  )
}

/* ---- the terminal --------------------------------------------------------- */

/* A window over the thread, not a replacement for it.
 *
 * Over rather than beside, because the panel is already the narrow column and
 * splitting it again would give a terminal forty characters — narrower than the
 * agent's own view lays out for, so every line would wrap and the thing would
 * be unreadable in exactly the situation somebody opened it for.
 *
 * It keeps the chat visible behind it and closes back to it, which is the whole
 * relationship: the panel is where you work and this is where you go for what
 * the panel cannot do yet. */
function Console(props: { term: AgentConsoleApi; onClose(): void }) {
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

/* ---- one bubble ----------------------------------------------------------- */

function Bubble({ m, since }: { m: AgentMessage; since: number }) {
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
function Gate(props: {
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

/* ---- the drag handle ------------------------------------------------------ */

/* Where the width lives, and why it is one CSS variable.
 *
 * --chat-w already drives both sides of the split: the panel is that wide and
 * the canvas is `calc(100% - var(--chat-w))`. So dragging sets ONE number and
 * the two move together — no second measurement to keep in step, and no frame
 * where the desktop and the panel disagree about where the seam is.
 *
 * Written to document.body because that is where the stylesheet's own rule
 * puts it (body.chat-open), so an inline value overrides the default without
 * either of them having to know about the other. */
const WIDTH_KEY = 'sentineldesk.chatWidth'
const MIN_W = 300

/* Not a fixed maximum. On a laptop, half the screen is a reasonable panel; on
 * an ultrawide, a panel of half the screen is absurd and a desktop squeezed to
 * 300px is unusable. Both ends are stated as what must be LEFT rather than what
 * may be taken. */
function clampWidth(px: number): number {
  const most = Math.max(MIN_W, window.innerWidth - 360)
  return Math.min(most, Math.max(MIN_W, px))
}

function useChatWidth(): { onGrab(e: React.PointerEvent): void; reset(): void } {
  const apply = useCallback((px: number | null) => {
    if (px === null) {
      document.body.style.removeProperty('--chat-w')
      try {
        localStorage.removeItem(WIDTH_KEY)
      } catch {
        /* a browser with storage switched off still resizes, it just forgets */
      }
      return
    }
    document.body.style.setProperty('--chat-w', `${px}px`)
    try {
      localStorage.setItem(WIDTH_KEY, String(px))
    } catch {
      /* as above */
    }
  }, [])

  /* Restored on mount, and re-clamped against THIS window rather than trusted.
   * A width saved on an external monitor and reopened on a laptop would
   * otherwise cover the desktop entirely, with the handle off-screen and no
   * way to drag it back. */
  useEffect(() => {
    let saved = 0
    try {
      saved = Number(localStorage.getItem(WIDTH_KEY) || 0)
    } catch {
      saved = 0
    }
    if (saved > 0) document.body.style.setProperty('--chat-w', `${clampWidth(saved)}px`)
    const onResize = () => {
      const now = parseFloat(getComputedStyle(document.body).getPropertyValue('--chat-w'))
      if (isFinite(now) && now > 0) {
        const fixed = clampWidth(now)
        if (fixed !== now) document.body.style.setProperty('--chat-w', `${fixed}px`)
      }
    }
    window.addEventListener('resize', onResize)
    /* The inline width goes when the panel goes. It lives on the body because
     * the video is `calc(100% - var(--chat-w))` and both sides must move
     * together — but an inline value beats the stylesheet, so leaving it
     * behind means closing the chat removes `body.chat-open` and the desktop
     * still stays narrowed by a panel that is no longer there. The number
     * survives in localStorage, so the next opening is the width they dragged. */
    return () => {
      window.removeEventListener('resize', onResize)
      document.body.style.removeProperty('--chat-w')
    }
  }, [])

  const onGrab = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      const handle = e.currentTarget as HTMLElement
      /* Pointer capture, so the drag survives the cursor crossing the video.
       * Without it the desktop's own pointer handlers swallow the move the
       * instant it leaves the handle, and the panel sticks. */
      handle.setPointerCapture(e.pointerId)
      document.body.classList.add('chat-resizing')

      const move = (ev: PointerEvent) => apply(clampWidth(window.innerWidth - ev.clientX))
      const up = () => {
        handle.removeEventListener('pointermove', move)
        handle.removeEventListener('pointerup', up)
        handle.removeEventListener('pointercancel', up)
        document.body.classList.remove('chat-resizing')
      }
      handle.addEventListener('pointermove', move)
      handle.addEventListener('pointerup', up)
      handle.addEventListener('pointercancel', up)
    },
    [apply],
  )

  return { onGrab, reset: () => apply(null) }
}

/* ---- the pieces that answer "how long, and what is it doing" -------------- */

/* Elapsed, counted from a timestamp rather than incremented.
 *
 * A counter that adds a second per tick drifts, and drifts badly in a
 * background tab where the browser throttles timers to once a minute — a
 * four-minute run would report forty seconds. Ticking is only what makes it
 * redraw; the number always comes from subtracting.
 */
function useElapsed(since: number): string {
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

/* The monitor that blinks while the agent thinks.
 *
 * A screen rather than a spinner, because what is happening is a desktop being
 * driven — and the blink is the screen's own glow coming up and down, which is
 * why it animates the fill and not the whole icon. Marked aria-hidden and
 * paired with the elapsed time in text: the animation is decoration, the number
 * is the information. */
function Thinking(props: { since: number }) {
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
 * with nanoseconds — and the drawer used to print it raw, so a row was dated
 * `2026-08-23T21:10:09.325109947Z`. Parsed HERE and not on the way over,
 * because the daemon passes the string through without reading it (see
 * agentlink.SessionInfo) and the browser is the only party that knows which
 * timezone and which language to render it in.
 *
 * Unparseable falls back to the original text. A date this code cannot read is
 * still a date somebody may recognise, and showing nothing would be worse. */
function when(iso: string): string {
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
function statusLine(
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
