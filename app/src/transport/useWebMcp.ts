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

/* The desktop's WebMCP tools, defined against the LIVE session.
 *
 * Every tool here is something a person already does through the rail or by
 * clicking the screen, and it travels the same way: over the authenticated
 * DataChannel the WebSocket login opened. No tool reaches past what the
 * signed-in person can do — the ones that write to the desktop refuse in the
 * same words the server would when the controls are not held, so the browser
 * agent learns to call take_control first instead of walking into a wall.
 *
 * They exist only while the session is live. Registered on connect, dropped
 * on disconnect (one AbortController, aborted in cleanup — the spec's own
 * unregister path, and StrictMode-safe because a double mount aborts the
 * first registration before making the second). A page that is merely
 * loaded, or a login that has not happened, publishes nothing. */

import { useEffect, useRef } from 'react'

import type { Desktop } from './useDesktopStream'
import { ensureModelContext, text, type WebMcpTool } from './webmcp'

/* Named keys the desktop understands beyond single characters — the set the
 * server's injector maps (see internal/desktop/input.go). Listed in the
 * schema so the agent knows what press_key accepts. */
const NAMED_KEYS =
  'Enter Backspace Tab Escape ArrowUp ArrowDown ArrowLeft ArrowRight ' +
  'Home End PageUp PageDown Insert Delete ' +
  'F1 F2 F3 F4 F5 F6 F7 F8 F9 F10 F11 F12'

export function useWebMcp(desktop: Desktop) {
  const live = desktop.state === 'live'
  /* The hook's engine returns a fresh object every render, so an effect
   * keyed on it re-registered and re-aborted the whole catalogue on every
   * state change — and every abort rejected 27 registration promises nobody
   * was listening to (the AbortError spam in the console). The tools close
   * over this ref instead and register once per connection. */
  const ref = useRef(desktop)
  ref.current = desktop

  useEffect(() => {
    if (!live) return
    const ctx = ensureModelContext()
    if (!ctx) return

    const controller = new AbortController()
    const { signal } = controller

    /* The controls gate, worded so an agent knows the remedy. Mirrors the
     * server, which refuses the same writes — this is a courtesy that saves
     * a round trip, not the enforcement. */
    const needControl = () =>
      text(
        'the desktop controls are not held — call take_control first, then retry',
        true,
      )

    const tools: WebMcpTool[] = [
      {
        name: 'desktop_status',
        description:
          'Report the shared desktop: who holds the controls, who is in the room, the stream quality, and whether a recording is running.',
        annotations: { readOnlyHint: true },
        execute: async () => {
          const driver = ref.current.control.holder ?? '(nobody)'
          const roster = ref.current.members
            .map((m) => `${m.name}${m.controller ? ' [driving]' : ''}${m.agent ? ' [AI]' : ''}`)
            .join(', ')
          return text(
            [
              `controls: ${ref.current.control.yours ? 'you hold them' : `held by ${driver}`}`,
              `members: ${roster || '(only you)'}`,
              `quality: ${ref.current.quality.mode} (${ref.current.quality.fps} fps)`,
              `recording: ${ref.current.recording ? 'yes' : 'no'}`,
            ].join('\n'),
          )
        },
      },
      {
        name: 'take_control',
        description:
          'Take the desktop controls so input can be sent. Cooperative: whoever held them is simply told they no longer do.',
        execute: async () => {
          if (ref.current.control.yours) return text('you already hold the controls')
          ref.current.toggleControl()
          return text('control requested — you now hold the desktop controls')
        },
      },
      {
        name: 'release_control',
        description: 'Release the desktop controls back to nobody.',
        execute: async () => {
          if (!ref.current.control.yours) return text('you do not hold the controls')
          ref.current.toggleControl()
          return text('controls released')
        },
      },
      {
        name: 'type_text',
        description:
          'Type a run of text into the focused window on the desktop, exactly as typed — accents and symbols included.',
        inputSchema: {
          type: 'object',
          properties: { text: { type: 'string', description: 'the text to type' } },
          required: ['text'],
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          const s = String(input.text ?? '')
          if (!s) return text('nothing to type', true)
          ref.current.sendInput({ t: 'kbt', k: s })
          return text(`typed ${s.length} characters`)
        },
      },
      {
        name: 'press_key',
        description:
          'Press one key or a named special key on the desktop (a keydown followed by a keyup).',
        inputSchema: {
          type: 'object',
          properties: {
            key: {
              type: 'string',
              description: `a single character, or one of: ${NAMED_KEYS}`,
            },
          },
          required: ['key'],
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          const k = String(input.key ?? '')
          if (!k) return text('no key given', true)
          ref.current.sendInput({ t: 'kb', k, d: 1 })
          ref.current.sendInput({ t: 'kb', k, d: 0 })
          return text(`pressed ${k}`)
        },
      },
      {
        name: 'move_mouse',
        description: 'Move the desktop pointer to a pixel position on the screen.',
        inputSchema: {
          type: 'object',
          properties: {
            x: { type: 'number', description: 'x in desktop pixels' },
            y: { type: 'number', description: 'y in desktop pixels' },
          },
          required: ['x', 'y'],
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          const x = Math.round(Number(input.x))
          const y = Math.round(Number(input.y))
          ref.current.sendInput({ t: 'mm', x, y })
          return text(`moved to ${x},${y}`)
        },
      },
      {
        name: 'click',
        description:
          'Click on the ref.current. With x and y it moves there first; button 1 is left, 2 middle, 3 right.',
        inputSchema: {
          type: 'object',
          properties: {
            x: { type: 'number' },
            y: { type: 'number' },
            button: { type: 'number', description: '1 left (default), 2 middle, 3 right' },
          },
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          const b = [1, 2, 3].includes(Number(input.button)) ? Number(input.button) : 1
          if (input.x !== undefined && input.y !== undefined) {
            ref.current.sendInput({ t: 'mm', x: Math.round(Number(input.x)), y: Math.round(Number(input.y)) })
          }
          ref.current.sendInput({ t: 'mb', b, d: 1 })
          ref.current.sendInput({ t: 'mb', b, d: 0 })
          return text(`clicked button ${b}`)
        },
      },
      {
        name: 'scroll',
        description: 'Scroll the desktop by wheel steps: dy vertical, dx horizontal.',
        inputSchema: {
          type: 'object',
          properties: {
            dy: { type: 'number', description: 'vertical steps (down positive)' },
            dx: { type: 'number', description: 'horizontal steps' },
          },
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          const dy = Math.trunc(Number(input.dy) || 0)
          const dx = Math.trunc(Number(input.dx) || 0)
          if (!dy && !dx) return text('nothing to scroll', true)
          ref.current.sendInput({ t: 'mw', dy, dx })
          return text(`scrolled dy=${dy} dx=${dx}`)
        },
      },
      {
        name: 'set_clipboard',
        description: "Put text on the desktop's clipboard, so it can be pasted into an application.",
        inputSchema: {
          type: 'object',
          properties: { text: { type: 'string' } },
          required: ['text'],
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          ref.current.sendInput({ t: 'clip', clip: String(input.text ?? '') })
          return text('clipboard set')
        },
      },
      {
        name: 'screenshot',
        description:
          'Capture the desktop to an image file. It is offered in the download tray, at framebuffer quality.',
        execute: async () => {
          ref.current.sendInput({ t: 'capture', action: 'shot' })
          return text('screenshot captured — it is waiting in the download tray')
        },
      },
      {
        name: 'set_quality',
        description: 'Set the stream quality: auto, media (medium), or high.',
        inputSchema: {
          type: 'object',
          properties: {
            mode: { type: 'string', enum: ['auto', 'media', 'high'] },
          },
          required: ['mode'],
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          const mode = String(input.mode)
          if (!['auto', 'media', 'high'].includes(mode)) {
            return text('mode must be auto, media, or high', true)
          }
          ref.current.setQuality(mode as 'auto' | 'media' | 'high')
          return text(`quality set to ${mode}`)
        },
      },
      {
        name: 'list_files',
        description:
          "List a directory on the desktop's filesystem (the file manager's own view, bounded by FILES_ROOT).",
        inputSchema: {
          type: 'object',
          properties: {
            dir: { type: 'string', description: 'directory path; empty for the default' },
          },
        },
        annotations: { readOnlyHint: true },
        execute: async (input) => {
          try {
            const listing = await ref.current.filesList(String(input.dir ?? ''))
            const lines = listing.entries.map(
              (e) => `${e.type === 'dir' ? 'd' : '-'} ${e.name}${e.type === 'file' ? ` (${e.size} B)` : ''}`,
            )
            const tail = listing.truncated ? `\n… ${listing.truncated} more not listed` : ''
            return text(`${listing.path}\n${lines.join('\n') || '(empty)'}${tail}`)
          } catch (err) {
            return text(`could not list: ${(err as Error).message}`, true)
          }
        },
      },
      {
        name: 'make_dir',
        description: 'Create a directory on the ref.current. Needs the controls.',
        inputSchema: {
          type: 'object',
          properties: { path: { type: 'string' } },
          required: ['path'],
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          try {
            await ref.current.filesOp('mkdir', String(input.path))
            return text('directory created')
          } catch (err) {
            return text(`could not create: ${(err as Error).message}`, true)
          }
        },
      },
      {
        name: 'rename_path',
        description: 'Rename or move a file or directory on the ref.current. Needs the controls.',
        inputSchema: {
          type: 'object',
          properties: {
            path: { type: 'string', description: 'current path' },
            to: { type: 'string', description: 'new path' },
          },
          required: ['path', 'to'],
        },
        execute: async (input) => {
          if (!ref.current.control.yours) return needControl()
          try {
            await ref.current.filesOp('rename', String(input.path), String(input.to))
            return text('renamed')
          } catch (err) {
            return text(`could not rename: ${(err as Error).message}`, true)
          }
        },
      },
    ]

    /* Registration may reject (a name already taken by a native tool, a
     * StrictMode double mount racing its own abort). Each is independent, so
     * one failure never sinks the rest. */
    for (const tool of tools) {
      try {
        /* The PROMISE also rejects — with AbortError — when the controller
         * aborts on cleanup; the try only ever caught the synchronous
         * throw. Unwatched, every unmount printed one uncaught rejection
         * per tool. */
        const r = ctx.registerTool(tool, { signal })
        if (r instanceof Promise) void r.catch(() => {})
      } catch {
        /* already registered, or the browser refused this one */
      }
    }

    return () => controller.abort()
  }, [live])
}
