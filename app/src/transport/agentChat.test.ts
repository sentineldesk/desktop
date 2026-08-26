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

/* The chat panel's pure logic.
 *
 * This file exists because of two bugs that shipped, and both of them were the
 * kind a test here catches in a second: an answer drawn twice because one wire
 * field has two names, and a row of `{{n}}` because a placeholder was written in
 * the wrong brace style. Neither needed a browser, a desktop or an agent to
 * reproduce — only the functions below, called directly.
 *
 * What is tested here is deliberately the part with no React in it. The panel's
 * rendering is verified by looking at it; what cannot be verified by looking is
 * whether a slash is a command, and that is exactly what this covers. */

import { describe, expect, it } from 'vitest'

import {
  classifyHistory,
  isRunning,
  matchCommand,
  restoreTurns,
  settleStreaming,
  type AgentCommand,
  type AgentMessage,
} from './agentChat'

/* The surface as an engine announces it. Written out rather than imported from
 * the panel, because there is nothing in the panel to import any more — the
 * list lives in the runtime, and that is the property being relied on. */
const OFFERED: readonly AgentCommand[] = [
  { name: '/compact', kind: 'compact', id: 'cmd.compact', what: 'fold the conversation' },
  { name: '/model', kind: 'model', id: 'cmd.model', what: 'switch model' },
  { name: '/stop', kind: '', id: 'cmd.stop', what: 'stop the agent' },
]

describe('matchCommand', () => {
  it('recognises a command the engine announced', () => {
    expect(matchCommand('/compact', OFFERED)).toEqual({ kind: 'compact', text: '' })
  })

  it('carries the argument', () => {
    expect(matchCommand('/model claude-code/opus', OFFERED)).toEqual({
      kind: 'model',
      text: 'claude-code/opus',
    })
  })

  it('keeps an argument that has spaces in it', () => {
    expect(matchCommand('/model  a  b ', OFFERED)?.text).toBe('a  b')
  })

  /* The rule that goes the opposite way from the terminal's, on purpose.
   *
   * A terminal prompt is a command line where a slash is a verb, so an unknown
   * one is a mistake and is refused. This is a chat box, where somebody may
   * reasonably begin a sentence with a path — refusing "/opt/desktop is where
   * it lives" would be the panel inventing a syntax error in the middle of a
   * conversation. */
  it('treats an unknown slash as a message, not an error', () => {
    expect(matchCommand('/opt/desktop is where it lives', OFFERED)).toBeNull()
    expect(matchCommand('/nonsense', OFFERED)).toBeNull()
  })

  it('is not fooled by a slash inside a sentence', () => {
    expect(matchCommand('use and/or', OFFERED)).toBeNull()
    expect(matchCommand('what about /compact', OFFERED)).toBeNull()
  })

  /* Nothing is offered until the engine has said what it has. A palette that
   * listed commands before anything announced them would be making them up —
   * and would send verbs to a runtime that may never have had them. */
  it('offers nothing before the engine has announced anything', () => {
    expect(matchCommand('/compact', [])).toBeNull()
  })

  /* An empty kind means the PANEL handles it — /stop is a cancel, not a
   * command — and the caller has to be able to tell that from "not a command"
   * without guessing. */
  it('distinguishes a panel-handled command from an unknown one', () => {
    expect(matchCommand('/stop', OFFERED)).toEqual({ kind: '', text: '' })
    expect(matchCommand('/unknown', OFFERED)).toBeNull()
  })
})

/** classifyHistory is the rule that decides what an agent_history frame IS.
 *
 * The bug it exists to prevent had no symptom a stack trace could show: a
 * transcript of the live conversation — the frame the runtime sends when
 * somebody continues a past chat, or when a terminal attaches and asks what is
 * going on — carries session 0, and the panel used to read every session-0
 * frame as the sessions list. The restored conversation went into the history
 * drawer as an empty list, and the thread stayed as it was.
 */
describe('classifyHistory', () => {
  it('reads a frame with no messages as the sessions list', () => {
    expect(classifyHistory({ session: 0, sessions: [] })).toBe('list')
  })

  it('reads session 0 WITH messages as the live conversation', () => {
    expect(classifyHistory({ session: 0, messages: [{ role: 'human', text: 'hi' }] })).toBe('live')
  })

  it('reads a numbered session with messages as one to look at', () => {
    expect(classifyHistory({ session: 24, messages: [] })).toBe('past')
  })

  it('does not mistake an empty live conversation for the list', () => {
    // The runtime answers a transcript request with an empty array when there
    // is nothing to show, and that is still a transcript. Reading it as the
    // list would wipe the history drawer every time a terminal attached to an
    // idle engine.
    expect(classifyHistory({ session: 0, messages: [] })).toBe('live')
  })
})

/** isRunning decides whether an incoming frame joins the bubble in flight.
 *
 * The bug: both sides spell "nothing" as the empty string and mean opposite
 * things by it. Nothing is running is busy === ''. This frame belongs to no
 * conversation is chat === ''. Compared directly they matched, so a note that
 * arrived outside any conversation — emptying the history, with no chat open —
 * opened a streaming bubble. Nothing closes a streaming bubble except the end
 * of an exchange, and there was no exchange: "Thinking…" stayed on screen for
 * ever with "forgot 15 conversation(s)" inside it.
 */
describe('isRunning', () => {
  it('does not read a frame with no conversation as the running one', () => {
    expect(isRunning('', '')).toBe(false)
  })

  it('is true only for the exchange actually in flight', () => {
    expect(isRunning('c-5', 'c-5')).toBe(true)
    expect(isRunning('c-5', 'c-4')).toBe(false)
    expect(isRunning('c-5', '')).toBe(false)
  })

  it('does not read a conversationless frame as running even when one is', () => {
    expect(isRunning('', 'c-5')).toBe(false)
  })
})

/** settleStreaming closes bubbles the runtime can no longer finish.
 *
 * Clearing `busy` gave the composer back and left the spinner turning above it.
 * A streaming bubble is a promise that more text is coming; when the runtime
 * disconnects, nothing will ever send it, and the panel goes on claiming an
 * answer is on its way for the rest of the session.
 */
describe('settleStreaming', () => {
  const row = (streaming: boolean): AgentMessage => ({
    key: 'k' + String(streaming),
    chat: 'c-1',
    role: 'agent',
    text: '',
    at: 0,
    streaming,
    steps: [],
    ending: null,
    streamed: streaming,
  })

  it('closes an open bubble', () => {
    const out = settleStreaming([row(true)])
    expect(out[0].streaming).toBe(false)
  })

  it('returns the same array when nothing is open, so no render is caused', () => {
    const quiet = [row(false)]
    expect(settleStreaming(quiet)).toBe(quiet)
  })

  it('leaves the text and the steps alone — only the promise is withdrawn', () => {
    const mid: AgentMessage = { ...row(true), text: 'half an ans' }
    const out = settleStreaming([mid])
    expect(out[0].text).toBe('half an ans')
    expect(out[0].streamed).toBe(true)
  })
})

/** restoreTurns is the shape a reloaded conversation comes back in.
 *
 * The bug it fixes was purely visual and therefore invisible to every layer
 * that could have caught it: the record was right, the wire was right, the
 * panel drew each turn faithfully — as its own bubble, with its own fold. A
 * twelve-turn run that had been ONE chain of tool calls while it ran came back
 * from history as twelve headers reading "Tools · 2".
 *
 * So what is asserted here is the property the eye was checking: consecutive
 * agent turns become one bubble, carrying every step in order, and a human
 * turn still stands on its own.
 */
describe('restoreTurns', () => {
  let n = 0
  const mkKey = () => `k${++n}`
  const turn = (role: string, text: string, tools: string[] = []) => ({
    role,
    text,
    steps: tools.map((tool) => ({ tool, detail: '' })),
  })

  it('folds a run into one bubble however many turns it took', () => {
    const out = restoreTurns(
      [
        turn('human', 'play the video'),
        turn('agent', 'reading the skill', ['skill_read']),
        turn('agent', 'taking control', ['request_control']),
        turn('agent', 'opening it', ['browser_open']),
      ],
      'past-79',
      mkKey,
    )
    expect(out).toHaveLength(2)
    expect(out[0].role).toBe('human')
    expect(out[1].steps.map((s) => s.tool)).toEqual([
      'skill_read',
      'request_control',
      'browser_open',
    ])
  })

  it('reads as paragraphs, not as one run-on sentence', () => {
    const out = restoreTurns([turn('agent', 'first'), turn('agent', 'second')], 'c', mkKey)
    expect(out[0].text).toBe('first\n\nsecond')
  })

  it('does not add an empty paragraph for a turn that only called tools', () => {
    const out = restoreTurns(
      [turn('agent', 'said something'), turn('agent', '', ['browser_wait_until'])],
      'c',
      mkKey,
    )
    expect(out[0].text).toBe('said something')
    expect(out[0].steps).toHaveLength(1)
  })

  it('starts a new bubble after a human turn, so a continued conversation is not one blob', () => {
    const out = restoreTurns(
      [turn('agent', 'done'), turn('human', 'now this'), turn('agent', 'on it')],
      'c',
      mkKey,
    )
    expect(out.map((m) => m.role)).toEqual(['agent', 'human', 'agent'])
  })

  it('gives every bubble a key of its own', () => {
    const out = restoreTurns([turn('human', 'a'), turn('agent', 'b')], 'c', mkKey)
    expect(new Set(out.map((m) => m.key)).size).toBe(out.length)
  })

  it('restores nothing as nothing rather than as an empty bubble', () => {
    expect(restoreTurns([], 'c', mkKey)).toEqual([])
  })
})
