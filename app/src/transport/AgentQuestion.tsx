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

/* The runtime's question, on the screen of the people who can answer it.
 *
 * Two things arrive as this one shape: the agent's own ask_human, and — under
 * MCP_POLICY=approve — the server's "the agent wants to run X, allow it?"
 * prompt. The card renders whatever the wire carried: the question text with
 * its buttons when there are options, a text field when there are none, and a
 * masked field for a secret. Anyone present may answer; the first answer wins
 * and question_done clears the card for everyone else.
 *
 * Deliberately NOT a modal. The person deciding whether to allow run_command
 * needs to see the desktop the command would run on, and a backdrop that
 * blocks the room to force a decision is the opposite of the supervision the
 * prompt exists for. It sits above the film strip and stays out of the way.
 */

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { AgentQuestion as Question } from './useDesktopStream'
import styles from './AgentQuestion.module.css'

export function AgentQuestion({
  question,
  onAnswer,
}: {
  question: Question
  onAnswer(answer: string): void
}) {
  const { t } = useTranslation()
  const [text, setText] = useState('')

  /* A new question must not inherit the half-typed answer to the last one. */
  useEffect(() => setText(''), [question.id])

  const send = () => {
    if (text.trim() === '') return
    onAnswer(text)
    setText('')
  }

  return (
    <div className={styles.card} role="alertdialog" aria-label={t('call.ask.title')}>
      <div className={styles.head}>
        <span className={styles.mark} aria-hidden="true">
          AI
        </span>
        <span className={styles.title}>{t('call.ask.title')}</span>
      </div>
      <p className={styles.text}>{question.text}</p>
      {question.options.length > 0 ? (
        <div className={styles.options}>
          {question.options.map((option) => (
            <button
              key={option}
              type="button"
              className={styles.option}
              onClick={() => onAnswer(option)}
            >
              {option}
            </button>
          ))}
        </div>
      ) : (
        <div className={styles.free}>
          <input
            className={styles.input}
            type={question.secret ? 'password' : 'text'}
            value={text}
            placeholder={t('call.ask.placeholder')}
            autoComplete="off"
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') send()
            }}
          />
          <button type="button" className={styles.send} onClick={send}>
            {t('call.ask.send')}
          </button>
        </div>
      )}
    </div>
  )
}
