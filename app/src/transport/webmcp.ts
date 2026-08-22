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

/* WebMCP: the desktop's tools, offered to whatever AI the BROWSER carries.
 *
 * A page can publish a set of callable tools that an in-browser agent — the
 * Prompt API's built-in model, Chrome's Gemini, anything that speaks the
 * emerging Web Machine Learning "model context" API — discovers and invokes.
 * We publish the very things a PERSON does here: type, click, move, read the
 * file list, take control. The point of the design is that the agent drives
 * the desktop through the SAME authenticated path a person's clicks take —
 * the WebRTC DataChannel opened after the WebSocket login — so there is no
 * new endpoint, no second credential, nothing to secure twice. If you are
 * signed in and connected, your browser's AI can act exactly as far as you
 * can, and not one step further.
 *
 * This file is the substrate: a small, self-contained polyfill that installs
 * the registry when the browser has no native one, matching the shape both
 * Chrome's native API and the webmcp-react ecosystem read. It carries no
 * dependency and knows nothing about this desktop — useWebMcp.ts is where the
 * actual tools are defined against the live session.
 *
 * The contract, distilled from the W3C explainer and Chrome's imperative API:
 * the registry lives on BOTH document.modelContext (the spec's home since the
 * May 2026 draft — tools belong to a page) and navigator.modelContext (the
 * Chrome 146–149 alias older code still reads); registerTool(def, {signal})
 * adds one and an AbortSignal removes it; a "toolchange" event fires on every
 * change; and a tool's execute() returns MCP's own { content: [...] }. */

export interface WebMcpToolResult {
  content: Array<{ type: 'text'; text: string }>
  isError?: boolean
}

export interface WebMcpTool {
  name: string
  description: string
  /** Plain JSON Schema — never Zod. Zod is a webmcp-react authoring
   * convenience it compiles to this before touching the registry. */
  inputSchema?: {
    type: 'object'
    properties: Record<string, unknown>
    required?: string[]
  }
  annotations?: { readOnlyHint?: boolean; untrustedContentHint?: boolean }
  /** The spec names this execute; webmcp-react's hook names it handler. The
   * polyfill accepts either — we author execute. */
  execute(
    input: Record<string, unknown>,
    ctx?: { signal?: AbortSignal },
  ): Promise<WebMcpToolResult | string>
}

interface ModelContextLike {
  registerTool(
    tool: WebMcpTool,
    options?: { signal?: AbortSignal },
  ): Promise<undefined> | { unregister(): void }
}

/* text() is the one-liner every tool reaches for: MCP's content-array form,
 * which Chrome accepts and the ecosystem expects. */
export function text(s: string, isError = false): WebMcpToolResult {
  return { content: [{ type: 'text', text: s }], isError }
}

/* Install the registry if — and only if — the browser lacks one. Native
 * always wins; we never shadow a real implementation. Returns the registry
 * to register against, or null when there is no document (SSR / a test with
 * no DOM), so callers degrade instead of throwing. */
export function ensureModelContext(): ModelContextLike | null {
  if (typeof document === 'undefined') return null
  const doc = document as unknown as { modelContext?: ModelContextLike }
  const nav = navigator as unknown as { modelContext?: ModelContextLike }
  if (doc.modelContext) return doc.modelContext
  if (nav.modelContext) return nav.modelContext

  type Entry = WebMcpTool & { execute: WebMcpTool['execute'] }
  class ModelContext extends EventTarget {
    private tools = new Map<string, Entry>()
    ontoolchange: ((ev: Event) => void) | null = null

    private changed() {
      const ev = new Event('toolchange')
      this.dispatchEvent(ev)
      if (typeof this.ontoolchange === 'function') this.ontoolchange(ev)
    }

    registerTool(tool: WebMcpTool, options: { signal?: AbortSignal } = {}) {
      const name = tool?.name
      if (!name || !tool.description) {
        throw new DOMException('name and description are required', 'InvalidStateError')
      }
      if (this.tools.has(name)) {
        throw new DOMException(`tool "${name}" already registered`, 'InvalidStateError')
      }
      const execute = tool.execute ?? (tool as { handler?: WebMcpTool['execute'] }).handler
      if (typeof execute !== 'function') {
        throw new DOMException('a handler is required', 'InvalidStateError')
      }
      this.tools.set(name, { ...tool, execute })
      this.changed()
      const dispose = () => {
        if (this.tools.delete(name)) this.changed()
      }
      options.signal?.addEventListener('abort', dispose, { once: true })
      return { unregister: dispose }
    }

    unregisterTool(name: string) {
      if (this.tools.delete(name)) this.changed()
    }

    provideContext({ tools = [] as WebMcpTool[] } = {}) {
      this.tools.clear()
      for (const t of tools) {
        const execute =
          (t as { execute?: WebMcpTool['execute']; handler?: WebMcpTool['execute'] }).execute ??
          (t as { handler?: WebMcpTool['execute'] }).handler
        if (execute) this.tools.set(t.name, { ...t, execute })
      }
      this.changed()
    }

    clearContext() {
      this.tools.clear()
      this.changed()
    }

    getTools() {
      return [...this.tools.values()].map(({ execute: _e, ...pub }) => pub)
    }

    async executeTool(
      tool: string | { name: string },
      args?: Record<string, unknown> | string,
      options: { signal?: AbortSignal } = {},
    ) {
      const entry = this.tools.get(typeof tool === 'string' ? tool : tool.name)
      if (!entry) throw new DOMException('no such tool', 'NotFoundError')
      const input =
        typeof args === 'string'
          ? (JSON.parse(args || '{}') as Record<string, unknown>)
          : (args ?? {})
      const out = await entry.execute(input, { signal: options.signal })
      return typeof out === 'string' ? text(out) : out
    }
  }

  const ctx = new ModelContext()
  // The same object on both globals: document is the spec home, navigator the
  // legacy alias — code from either era finds the same tools.
  Object.defineProperty(document, 'modelContext', { value: ctx, configurable: true })
  Object.defineProperty(navigator, 'modelContext', { value: ctx, configurable: true })
  return ctx as unknown as ModelContextLike
}
