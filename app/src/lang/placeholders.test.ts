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

/* The brace style, which is not a matter of taste here.
 *
 * i18next is configured in i18n.ts with `prefix: '{', suffix: '}'`, because the
 * catalogues use single braces. A string written with i18next's OWN default of
 * `{{n}}` therefore does not interpolate: it parses as a variable named `{n`,
 * which does not exist, and the placeholder is left on screen exactly as typed.
 *
 * That shipped. A chat panel showed `turns: {{n}}  tools: {{n}}  tokens: {{n}}`
 * to a person for a whole session, and nothing failed anywhere — not the build,
 * not the types, not the runtime. It is invisible to everything except a pair of
 * eyes on the right screen, which is the definition of what a test is for. */

import { describe, expect, it } from 'vitest'

import deskEn from './desk-en.json'
import deskEs from './desk-es.json'
import deskPt from './desk-pt.json'
import deskZhHant from './desk-zh-Hant.json'
import deskZhHK from './desk-zh-HK.json'
import deskZhTW from './desk-zh-TW.json'
import panelEn from './panel-en.json'
import panelEs from './panel-es.json'
import panelPt from './panel-pt.json'
import panelZhHant from './panel-zh-Hant.json'
import panelZhHK from './panel-zh-HK.json'
import panelZhTW from './panel-zh-TW.json'

const catalogues: Record<string, Record<string, string>> = {
  'desk-en': deskEn, 'desk-es': deskEs, 'desk-pt': deskPt,
  'desk-zh-Hant': deskZhHant, 'desk-zh-HK': deskZhHK, 'desk-zh-TW': deskZhTW,
  'panel-en': panelEn, 'panel-es': panelEs, 'panel-pt': panelPt,
  'panel-zh-Hant': panelZhHant, 'panel-zh-HK': panelZhHK, 'panel-zh-TW': panelZhTW,
}

describe('translation placeholders', () => {
  it('never uses double braces, which this build does not interpolate', () => {
    const offenders: string[] = []
    for (const [file, strings] of Object.entries(catalogues)) {
      for (const [key, value] of Object.entries(strings)) {
        if (typeof value === 'string' && /\{\{[^}]*\}\}/.test(value)) {
          offenders.push(`${file}: ${key} = ${value}`)
        }
      }
    }
    expect(offenders).toEqual([])
  })

  /* The other half. A key that takes an argument in one language and not in
   * another produces a sentence with a hole in it for exactly the readers who
   * do not speak the language anybody tested in. */
  it('uses the same placeholders in every language of a catalogue', () => {
    const placeholders = (s: string) =>
      (s.match(/\{[a-zA-Z_][a-zA-Z0-9_]*\}/g) ?? []).sort().join(',')

    const problems: string[] = []
    for (const family of ['desk', 'panel']) {
      const base = catalogues[`${family}-en`]
      for (const [file, strings] of Object.entries(catalogues)) {
        if (!file.startsWith(family) || file === `${family}-en`) continue
        for (const [key, value] of Object.entries(strings)) {
          const want = base[key]
          if (typeof want !== 'string' || typeof value !== 'string') continue
          if (placeholders(want) !== placeholders(value)) {
            problems.push(`${file}: ${key} has [${placeholders(value)}], en has [${placeholders(want)}]`)
          }
        }
      }
    }
    expect(problems).toEqual([])
  })
})
