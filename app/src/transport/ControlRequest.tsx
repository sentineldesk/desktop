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

/* "The agent wants the controls" — the permission prompt, on the screen of
 * everyone who could answer it.
 *
 * The runtime broadcasts control_request and then BLOCKS, waiting; silence is
 * a refusal, not an approval (AskForControl, internal/stream/room.go). That
 * makes this card the whole mechanism: with nothing drawing it, every
 * request_control sat out its timeout against a screen that showed nothing
 * and came back "nobody answered in time" to an agent whose person was
 * looking straight at the desktop. That is exactly what happened — the React
 * port kept the strings and the stylesheet for this prompt and lost the
 * prompt itself.
 *
 * The TEXT IS THE PANEL'S, never the agent's: the wire carries only who is
 * asking and the deadline. An agent that could compose the sentence in the
 * dialog that hands it the desktop could write anything it liked there.
 *
 * Centred, over a backdrop, unlike every other card in this pane — the same
 * shape styles.css has always described for it. A decision that expires is
 * the one interruption worth covering the desktop for: a prompt nobody
 * notices is the same as no prompt at all, and here that silence answers no.
 */

import { useTranslation } from 'react-i18next'

import type { ControlRequest as Request } from './useDesktopStream'

export function ControlRequest({
  request,
  onAnswer,
}: {
  request: Request
  onAnswer(granted: boolean): void
}) {
  const { t } = useTranslation()

  return (
    /* The ids and class names are the contract with styles.css, which has
     * carried this card's design since the previous client. */
    <div
      id="ask-control"
      className="show"
      role="alertdialog"
      aria-modal="true"
      aria-label={t('ask.title')}
      /* Clicking the backdrop is not an answer. Refusing is a button, and
       * the runtime's own timeout covers the person who walks away. */
      onClick={(e) => e.stopPropagation()}
    >
      <div className="ac-card">
        <div className="ac-head">
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <rect x="4" y="10.5" width="16" height="9.5" rx="2" />
            <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
          </svg>
          <span>{t('ask.title')}</span>
        </div>
        <p
          className="ac-msg"
          /* ask.body wraps the name in <b> — the dictionary's own markup, not
           * anything that arrived over the wire. The name rides in through
           * i18next interpolation rather than string concatenation here. */
          dangerouslySetInnerHTML={{ __html: t('ask.body', { who: request.who }) }}
        />
        <div className="ac-actions">
          <button type="button" onClick={() => onAnswer(false)}>
            {t('ask.deny')}
          </button>
          <button type="button" id="ask-allow" autoFocus onClick={() => onAnswer(true)}>
            {t('ask.allow')}
          </button>
        </div>
        {request.seconds > 0 ? (
          /* The deadline made visible. The bar drains for exactly as long as
           * the runtime said it would wait; it decides nothing on its own —
           * control_request_done is what takes the card away, however it
           * ended. Keyed by id so a second request restarts the animation
           * instead of inheriting the first one's remaining sliver. */
          <div className="ac-timer" key={request.id}>
            <i style={{ animationDuration: `${request.seconds}s` }} />
          </div>
        ) : null}
      </div>
    </div>
  )
}
