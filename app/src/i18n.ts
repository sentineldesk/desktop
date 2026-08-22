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

// Two vocabularies, one dictionary. The desk-* files are the standalone
// client's own strings (the rail, the login, the file manager keys); the
// panel-* files carry the wr.* keys the components ported from the workroom
// still speak. The key spaces never collided — plain names on one side, a
// wr. prefix on the other — so merging them is a spread, not a negotiation.
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

import deskEn from './lang/desk-en.json'
import deskEs from './lang/desk-es.json'
import deskPt from './lang/desk-pt.json'
import deskZhHant from './lang/desk-zh-Hant.json'
import deskZhHK from './lang/desk-zh-HK.json'
import deskZhTW from './lang/desk-zh-TW.json'
import panelEn from './lang/panel-en.json'
import panelEs from './lang/panel-es.json'
import panelPt from './lang/panel-pt.json'
import panelZhHant from './lang/panel-zh-Hant.json'
import panelZhHK from './lang/panel-zh-HK.json'
import panelZhTW from './lang/panel-zh-TW.json'

/** Every language the client can be switched to, in menu order. `chip` is what
 *  the rail shows in the two or three characters it has for a language: a code
 *  reads fine in Latin script, but ZH-HANT does not, so the Chinese variants
 *  name themselves in their own script. */
export const LANGUAGES = [
  { code: 'en', name: 'English', chip: 'EN' },
  { code: 'es', name: 'Español', chip: 'ES' },
  { code: 'pt', name: 'Português', chip: 'PT' },
  { code: 'zh-Hant', name: '繁體中文', chip: '繁' },
  { code: 'zh-TW', name: '繁體中文（台灣）', chip: '繁臺' },
  { code: 'zh-HK', name: '繁體中文（香港）', chip: '繁港' },
] as const

export type LanguageCode = (typeof LANGUAGES)[number]['code']

const CODES: readonly string[] = LANGUAGES.map((l) => l.code)

// zh-TW and zh-HK are NOT full dictionaries. Traditional Chinese is one
// written language with regional vocabulary, so the variant files carry only
// the strings the two regions say differently (品質/質素, 帳號/帳戶,
// 網路/網絡, 擷圖/截圖…) and inherit the rest from zh-Hant. That is what the
// fallback chain below is for: variant → zh-Hant → English.
const CHINESE_CHAIN = ['zh-Hant', 'en']

/** The browser's preferred language, mapped onto what the client actually has.
 *  Chinese arrives in more shapes than any other tag — zh-TW, zh-Hant-HK,
 *  zh-MO, bare zh — so it gets read by parts instead of by prefix: Macau reads
 *  Hong Kong, and anything else Chinese lands on the general Traditional
 *  dictionary rather than dropping the user into English. */
function fromNavigator(): LanguageCode {
  const tag = (navigator.language || 'en').toLowerCase()
  if (tag.startsWith('zh')) {
    if (tag.includes('tw')) return 'zh-TW'
    if (tag.includes('hk') || tag.includes('mo')) return 'zh-HK'
    return 'zh-Hant'
  }
  const two = tag.slice(0, 2)
  return (CODES.includes(two) ? two : 'en') as LanguageCode
}

// A code saved by an older build (or edited by hand) must not strand the UI on
// a language that no longer exists, hence the membership test.
const saved = localStorage.getItem('sentineldesk.lang')
const initial = saved && CODES.includes(saved) ? saved : fromNavigator()

void i18n.use(initReactI18next).init({
  resources: {
    en: { translation: { ...deskEn, ...panelEn } },
    es: { translation: { ...deskEs, ...panelEs } },
    pt: { translation: { ...deskPt, ...panelPt } },
    'zh-Hant': { translation: { ...deskZhHant, ...panelZhHant } },
    'zh-TW': { translation: { ...deskZhTW, ...panelZhTW } },
    'zh-HK': { translation: { ...deskZhHK, ...panelZhHK } },
  },
  lng: initial,
  fallbackLng: { 'zh-TW': CHINESE_CHAIN, 'zh-HK': CHINESE_CHAIN, default: ['en'] },
  // The resource keys are cased tags (zh-Hant, not zh-hant) and i18next matches
  // them literally, so keep the case it was given and do not let it resolve
  // zh-TW down to a bare "zh" bundle that does not exist — the fallback map
  // above is the only chain there is.
  cleanCode: false,
  lowerCaseLng: false,
  load: 'currentOnly',
  supportedLngs: [...CODES],
  // The desk strings use {placeholder} single braces; i18next defaults to
  // double. Configure the interpolation to the files rather than rewriting
  // three files to the library.
  interpolation: { escapeValue: false, prefix: '{', suffix: '}' },
})

export function setLanguage(code: string) {
  localStorage.setItem('sentineldesk.lang', code)
  void i18n.changeLanguage(code)
}

export default i18n
