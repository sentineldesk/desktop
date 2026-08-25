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

/* The name gate — the screen the owner picked from three mocks: after the
 * login (or straight away when there is no login), one question before the
 * desktop. The credential is SHARED here, so the login says you may enter
 * and this says who walked in — the name on your cursor and your presence.
 *
 * Remembered in localStorage under the SAME key Settings writes, so either
 * place can change it and the other agrees. First visit offers a name drawn
 * from the Matrix trilogy's crew; the die redraws, typing overrides. */

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

/* The credited cast of the four films, deduplicated — read off IMDb's full
 * credits (through this desktop's own browser, fittingly), kept to names
 * short enough to live on a cursor. Grouped by first appearance. */
const MATRIX = [
  // The Matrix (1999)
  'Neo', 'Trinity', 'Morpheus', 'Smith', 'Oracle', 'Cypher', 'Tank',
  'Dozer', 'Mouse', 'Switch', 'Apoc', 'Choi', 'Dujour', 'Rhineheart',
  'Brown', 'Jones',
  // The Matrix Reloaded (2003)
  'Niobe', 'Link', 'Keymaker', 'Persephone', 'Merovingian', 'Architect',
  'Seraph', 'Zee', 'Cas', 'Kid', 'Bane', 'Lock', 'Ballard', 'Soren',
  'Vector', 'Binary', 'Malachi', 'Corrupt', 'Cain', 'Abel', 'Axel', 'Ajax',
  'Ice', 'Kali', 'Mauser', 'Wurm', 'Tirant', 'Colt', 'AK', 'Johnson',
  'Jackson', 'Thompson', 'Hamann', 'West', 'Dillard', 'Wirtz', 'Maggie',
  'Roland', 'Ghost', 'Mifune', 'Rama-Kandra',
  // The Matrix Revolutions (2003)
  'Sati', 'Trainman', 'Kamala', 'Charra', 'Grace', 'Sparks',
  // The Matrix Resurrections (2021)
  'Bugs', 'Analyst', 'Sequoia', 'Sheperd', 'Berg', 'Lexy', 'Zen', 'Astra',
  'Echo', 'Freya', 'Calliope', 'Quillion', 'Ellster', 'Hanno', 'Bobbi',
  'Gwyn', 'Jude', 'Skroce', 'Fiona', 'Kush', 'Mercy',
]

const NAME_OK = /[^A-Za-z0-9 _-]/g
const NAME_KEY = 'sentineldesk.name'

export function randomMatrixName(not?: string): string {
  const pool = not ? MATRIX.filter((n) => n !== not) : MATRIX
  return pool[Math.floor(Math.random() * pool.length)]
}
const draw = randomMatrixName

/* The die, as a button an input wears: absolute inside a relative wrapper,
 * centred on the input's height. The gate uses it; Settings borrows it. */
export function MatrixDie(props: { title: string; onClick(): void }) {
  return (
    <button
      type="button"
      onClick={props.onClick}
      title={props.title}
      style={{
        position: 'absolute', right: 6, top: '50%',
        transform: 'translateY(-50%)', width: 28, height: 28, padding: 0,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        background: 'transparent', border: 0, color: 'var(--sd-dim)',
        cursor: 'pointer',
      }}
    >
      <svg viewBox="0 0 24 24" style={{ width: 16, height: 16, fill: 'none', stroke: 'currentColor', strokeWidth: 1.5, strokeLinecap: 'round', strokeLinejoin: 'round' }}>
        <rect x="4" y="4" width="16" height="16" rx="3" />
        <circle cx="9" cy="9" r="1" fill="currentColor" stroke="none" />
        <circle cx="15" cy="9" r="1" fill="currentColor" stroke="none" />
        <circle cx="9" cy="15" r="1" fill="currentColor" stroke="none" />
        <circle cx="15" cy="15" r="1" fill="currentColor" stroke="none" />
      </svg>
    </button>
  )
}

export function NameGate(props: { stored: string; onDone(name: string): void }) {
  const { t } = useTranslation()
  /* Their remembered name first; a Matrix name only for a first visit. */
  const opening = useMemo(() => props.stored || draw(), [props.stored])
  const [value, setValue] = useState(opening)

  const go = () => {
    const clean = value.replace(NAME_OK, '').trim().slice(0, 48) || draw()
    try {
      window.localStorage.setItem(NAME_KEY, clean)
    } catch {
      /* no storage is a valid machine; the name still rides this session */
    }
    props.onDone(clean)
  }

  /* Wears the login's OWN id so every rule of the console door — the grid,
   * the 360px column, the input and button dress — applies verbatim. The
   * two are never on screen together: this one exists only after the door. */
  return (
    <div id="login" className="visible">
      <form
        onSubmit={(e) => {
          e.preventDefault()
          go()
        }}
      >
        <h1>
          <svg viewBox="0 0 64 64" aria-hidden="true">
            <rect className="brand-scr" x="4" y="8" width="56" height="38" rx="6" />
            <rect className="brand-gls" x="10" y="14" width="44" height="26" rx="2" />
            <rect className="brand-scr" x="22" y="50" width="20" height="4" rx="2" />
            <rect className="brand-std" x="16" y="54" width="32" height="4" rx="2" />
          </svg>
          {t('name.title')}
        </h1>
        <div className="subtitle">{t('name.hint')}</div>

        <div style={{ position: 'relative' }}>
          <input
            autoFocus
            value={value}
            maxLength={48}
            onChange={(e) => setValue(e.target.value.replace(NAME_OK, ''))}
            onFocus={(e) => e.target.select()}
            style={{ paddingRight: 44, marginTop: 0, textAlign: 'center' }}
            aria-label={t('name.title')}
          />
          <MatrixDie title={t('name.shuffle')} onClick={() => setValue(draw(value))} />
        </div>

        <button type="submit">{t('name.go')}</button>
        <div className="subtitle" style={{ marginBottom: 0, fontSize: 11.5 }}>
          {t('name.note')}
        </div>
      </form>
    </div>
  )
}
