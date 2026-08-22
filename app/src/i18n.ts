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
import panelEn from './lang/panel-en.json'
import panelEs from './lang/panel-es.json'
import panelPt from './lang/panel-pt.json'

const saved = localStorage.getItem('sentineldesk.lang')
const nav = (navigator.language || 'en').slice(0, 2)
const initial = saved || (['en', 'es', 'pt'].includes(nav) ? nav : 'en')

void i18n.use(initReactI18next).init({
  resources: {
    en: { translation: { ...deskEn, ...panelEn } },
    es: { translation: { ...deskEs, ...panelEs } },
    pt: { translation: { ...deskPt, ...panelPt } },
  },
  lng: initial,
  fallbackLng: 'en',
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
