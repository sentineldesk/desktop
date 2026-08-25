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

/* Mode two of the shell: the agent, full screen.
 *
 * The anatomy is OpenBot's app shell — a sessions sidebar with the user's
 * menu rising from its foot, the conversation filling the centre, the
 * desktop as a card the reader can expand — built from OpenBot's own vendored
 * components (Message/Bubble, MessageScroller, ToolLine, Streamdown prose,
 * the dropdown and dialog primitives). What is OURS is the data plane: every
 * byte arrives on the WebRTC DataChannel this session already holds
 * (useDesktopStream → agentChat), because the desktop deliberately has no
 * HTTP endpoints to poll. OpenBot polls screenshots; we place the live
 * <video> element into the card instead.
 *
 * The third column is the terminal's /panel (ctrl+b there, ctrl+b here): the
 * session's state and totals, and the session verbs — compact, rewind,
 * memory — that the TUI has and the old side panel never grew. They travel
 * as the same slash commands the runtime announced on the wire.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  IconArrowRight,
  IconBox,
  IconCheck,
  IconChevronRight,
  IconCopy,
  IconChevronUp,
  IconDeviceDesktop,
  IconDots,
  IconDownload,
  IconArrowsDiagonal,
  IconExternalLink,
  IconLanguage,
  IconLogout,
  IconMoon,
  IconPencil,
  IconPhoto,
  IconPlayerPlayFilled,
  IconPlayerStopFilled,
  IconPlus,
  IconSearch,
  IconSettings,
  IconSun,
  IconTerminal2,
  IconTrash,
  IconX,
} from '@tabler/icons-react'
import { Streamdown } from 'streamdown'

import { markdownComponents } from '@/lib/markdown'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@/components/ui/input-group'
import {
  Bubble,
  BubbleContent,
} from '@/components/ui/bubble'
import {
  Message as MessageRow,
  MessageContent,
  MessageFooter,
} from '@/components/ui/message'
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from '@/components/ui/message-scroller'
import { ToolLine } from '@/components/channels/tool-line'

import i18n, { LANGUAGES, setLanguage } from '../i18n'
import type { AgentMessage, AgentSession } from '../transport/agentChat'
import type { useDesktopStream } from '../transport/useDesktopStream'
import { Console, Gate, statusLine, when } from '../transport/chatParts'

const DOCS_URL = 'https://sentineldesk.github.io/desktop/docs/guide/index.html'

/* The panel's posture, remembered like the old rail's was. */
const PANEL_KEY = 'sentineldesk.sessionPanel'
/* The stage's posture: the desktop card starts open — watching the agent work
 * is the point of the card — and remembers being folded. */
const CANVAS_KEY = 'sentineldesk.canvas'
const CANVAS_W_KEY = 'sentineldesk.canvasW'

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).slice(0, 2)
  return parts.map((p) => p[0]?.toUpperCase() ?? '').join('') || '?'
}

/* ---- one message, in OpenBot's dress -------------------------------------- */

/* A person's line is a muted bubble on the right; the agent's prose is
 * full-width markdown (Streamdown, links hardened in lib/markdown); a system
 * line sits centred and small. The steps keep ToolLine's single-line action
 * rhythm, shimmering while they run. */
/* "6m 45s", or "45s" under a minute — the shape the owner asked for. */
function fmtDur(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  const m = Math.floor(s / 60)
  return m ? `${m}m ${s % 60}s` : `${s}s`
}

function Row({ m }: { m: AgentMessage }) {
  const { t } = useTranslation()
  if (m.role === 'human') {
    return (
      <MessageRow align="end">
        <MessageContent>
          <Bubble variant="muted" align="end">
            <BubbleContent>
              <span className="whitespace-pre-wrap">{m.text}</span>
            </BubbleContent>
          </Bubble>
        </MessageContent>
      </MessageRow>
    )
  }
  if (m.role === 'system') {
    return (
      <div className="py-1 text-center text-xs text-muted-foreground">{m.text}</div>
    )
  }
  return (
    <MessageRow>
      <MessageContent>
        {m.steps.length > 0 ? (
          <Tools steps={m.steps} streaming={m.streaming} busyText={m.text === ''} />
        ) : null}

        {m.text !== '' ? (
          <div className="prose-sm min-w-0 max-w-full text-sm leading-relaxed">
            <Streamdown components={markdownComponents}>{m.text}</Streamdown>
          </div>
        ) : null}

        {m.ending ? (
          <MessageFooter
            className={cn(
              'gap-2 px-0',
              !m.ending.ok
                ? 'text-destructive'
                : m.ending.stoppedBy
                  ? 'text-amber-600 dark:text-amber-500'
                  : '',
            )}
          >
            {m.ending.stoppedBy ? (
              <span>{t('chat.stoppedBy', { why: m.ending.stoppedBy })}</span>
            ) : null}
            {m.ending.turns ? <span>{t('chat.turns', { n: m.ending.turns })}</span> : null}
            {m.ending.calls ? <span>{t('chat.calls', { n: m.ending.calls })}</span> : null}
            {m.ending.inToks || m.ending.outToks ? (
              <span>{t('chat.tokens', { n: m.ending.inToks + m.ending.outToks })}</span>
            ) : null}
            {m.ending.ms ? <span>{fmtDur(m.ending.ms)}</span> : null}
          </MessageFooter>
        ) : null}
      </MessageContent>
    </MessageRow>
  )
}

/* ---- the tool chain, folded ----------------------------------------------- */

/* Two levels of accordion, the Claude Desktop shape the owner asked for by
 * screenshot: a "Tools" header that folds the WHOLE chain — open while the
 * run streams so the work is visible, folded once it ends, a person's click
 * winning over both — and inside it one row per call, each folded to a
 * single line until ITS chevron is pressed, which reveals the call's full
 * output in a scrollable well. The transcript keeps its reading rhythm; the
 * evidence stays one press away. */
function Tools({
  steps,
  streaming,
  busyText,
}: {
  steps: AgentMessage['steps']
  streaming: boolean
  busyText: boolean
}) {
  const { t } = useTranslation()
  /* Folded until pressed, ALWAYS — streaming included. The owner asked for
   * the WhatsApp shape: while the run works, the folded header itself is the
   * typing indicator (the current tool's name, shimmering); the chain opens
   * only under a finger. */
  const [open, setOpen] = useState(false)
  const last = steps[steps.length - 1]

  return (
    <div className="min-w-0 overflow-hidden rounded-lg border bg-card">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left text-xs hover:bg-foreground/5"
      >
        <IconChevronRight
          className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')}
        />
        <span className="shrink-0 font-medium">
          {t('ws.tools')} · {steps.length}
        </span>
        {!open && streaming && last ? (
          <span
            className={cn(
              'min-w-0 truncate font-mono text-[11px] text-muted-foreground',
              busyText && 'tool-line-running',
            )}
          >
            {last.tool}
          </span>
        ) : null}
      </button>

      {open ? (
        <div className="flex flex-col">
          {steps.map((step, i) => {
            const running = streaming && busyText && i === steps.length - 1
            const broke = step.tool === 'interrupted'
            return (
              <details key={step.key} className="group/step border-t">
                {/* Native <details>: each call folds on its own, stateless,
                    and a re-render mid-stream never snaps one shut. */}
                <summary className="flex min-w-0 cursor-pointer list-none items-center gap-2 px-3 py-1.5 text-xs hover:bg-foreground/5 [&::-webkit-details-marker]:hidden">
                  <IconChevronRight className="size-3 shrink-0 text-muted-foreground transition-transform group-open/step:rotate-90" />
                  <span
                    className={cn(
                      'shrink-0 font-mono text-[11px]',
                      broke ? 'text-amber-600 dark:text-amber-500' : 'text-foreground',
                      running && 'tool-line-running',
                    )}
                  >
                    {step.tool}
                  </span>
                  {step.detail ? (
                    <span className="min-w-0 truncate text-[11px] text-muted-foreground">
                      {step.detail}
                    </span>
                  ) : null}
                </summary>
                {step.detail ? (
                  <DetailWell text={step.detail} />
                ) : (
                  <div className="mx-3 mb-2 text-[11px] text-muted-foreground">—</div>
                )}
              </details>
            )
          })}
        </div>
      ) : null}
    </div>
  )
}

/* ---- the well's contents --------------------------------------------------

   A call's output is JSON more often than not, and grey minified JSON reads
   as noise. When the detail parses it is pretty-printed and painted the way
   the owner's editor paints it — a line-number gutter, gold braces, sky-blue
   keys, salmon strings, sage numbers. Anything that does not parse (prose,
   truncated args) stays as it came. */
const JSON_TOKEN =
  /("(?:[^"\\]|\\.)*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}\[\]])/g

function paintLine(line: string, keyBase: number): React.ReactNode[] {
  const out: React.ReactNode[] = []
  let at = 0
  let k = keyBase
  let m: RegExpExecArray | null
  JSON_TOKEN.lastIndex = 0
  while ((m = JSON_TOKEN.exec(line)) !== null) {
    if (m.index > at) out.push(line.slice(at, m.index))
    if (m[1] !== undefined) {
      if (m[2] !== undefined) {
        out.push(
          <span key={k++} className="text-[#0451a5] dark:text-[#9cdcfe]">
            {m[1]}
          </span>,
          m[2],
        )
      } else {
        out.push(
          <span key={k++} className="text-[#a31515] dark:text-[#ce9178]">
            {m[1]}
          </span>,
        )
      }
    } else if (m[3] !== undefined) {
      out.push(
        <span key={k++} className="text-[#098658] dark:text-[#b5cea8]">
          {m[3]}
        </span>,
      )
    } else if (m[4] !== undefined) {
      out.push(
        <span key={k++} className="text-[#0000ff] dark:text-[#569cd6]">
          {m[4]}
        </span>,
      )
    } else if (m[5] !== undefined) {
      out.push(
        <span key={k++} className="text-[#795e26] dark:text-[#ffd700]">
          {m[5]}
        </span>,
      )
    }
    at = JSON_TOKEN.lastIndex
  }
  out.push(line.slice(at))
  return out
}

function DetailWell({ text }: { text: string }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  let value: unknown
  try {
    value = JSON.parse(text.trim())
  } catch {
    value = undefined
  }
  const parsed = typeof value === 'object' && value !== null
  const pretty = parsed ? JSON.stringify(value, null, 2) : text

  const copy = () => {
    void navigator.clipboard
      ?.writeText(pretty)
      .then(() => {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1600)
      })
      .catch(() => {})
  }

  const copyBtn = (
    <button
      type="button"
      onClick={copy}
      title={copied ? t('chat.copied') : t('chat.copy')}
      className="absolute top-1.5 right-1.5 rounded-md border bg-card p-1 text-muted-foreground opacity-0 transition-opacity group-hover/well:opacity-100 hover:text-foreground focus-visible:opacity-100"
    >
      {copied ? (
        <IconCheck className="size-3.5 text-[var(--sd-drive)]" />
      ) : (
        <IconCopy className="size-3.5" />
      )}
    </button>
  )

  if (!parsed) {
    return (
      <div className="group/well relative mx-3 mb-2">
        <pre className="max-h-64 overflow-auto rounded-md bg-background p-2.5 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-muted-foreground">
          {text}
        </pre>
        {copyBtn}
      </div>
    )
  }
  const lines = pretty.split('\n')
  const gutter = String(lines.length).length
  return (
    <div className="group/well relative mx-3 mb-2">
      <div className="max-h-64 overflow-auto rounded-md bg-background py-2 font-mono text-[11px] leading-relaxed">
        {lines.map((line, i) => (
          <div key={i} className="flex px-2.5">
            <span
              className="shrink-0 pr-3 text-right text-muted-foreground/50 select-none"
              style={{ minWidth: `${gutter + 1}ch` }}
            >
              {i + 1}
            </span>
            <span className="min-w-0 whitespace-pre-wrap text-foreground/80">
              {paintLine(line, i * 100)}
            </span>
          </div>
        ))}
      </div>
      {copyBtn}
    </div>
  )
}

/* ---- one session row ------------------------------------------------------ */

/* OpenBot's roster row, re-pointed: no router, no react-query — opening is a
 * DataChannel ask and the row menu carries the drawer's old verbs. */
/* The mock's row avatar: a gradient disc, stable per title so a
 * conversation keeps its colour without a server assigning one. */
function titleHue(text: string): number {
  let h = 0
  for (const c of text) h = (h * 31 + c.charCodeAt(0)) % 360
  return h
}

function SessionRow(props: {
  session: AgentSession
  on: boolean
  editing: boolean
  onOpen(): void
  onResume(): void
  onExport(): void
  onForget(): void
  onRename(): void
  onRenamed(title: string): void
}) {
  const { t } = useTranslation()
  const s = props.session

  if (props.editing) {
    return (
      <div className="flex w-full items-center gap-2.5 rounded-lg bg-foreground/5 px-2 py-2">
        <span
          className="size-7 shrink-0 rounded-full"
          style={{
            background: `linear-gradient(140deg, hsl(${titleHue(s.title)} 38% 38%), hsl(${(titleHue(s.title) + 45) % 360} 55% 55%))`,
          }}
        />
        <input
          autoFocus
          defaultValue={s.title}
          onKeyDown={(e) => {
            if (e.key === 'Enter') props.onRenamed((e.target as HTMLInputElement).value)
            if (e.key === 'Escape') props.onRenamed('')
            e.stopPropagation()
          }}
          onBlur={(e) => props.onRenamed(e.target.value)}
          className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1 text-[13px] outline-none focus:border-ring"
        />
      </div>
    )
  }
  return (
    <div
      className={cn(
        'group/row relative flex w-full min-w-0 items-start gap-2 rounded-lg px-2 py-2 text-left hover:bg-foreground/5',
        props.on && 'bg-foreground/5',
      )}
    >
      <button
        type="button"
        onClick={props.onOpen}
        className="flex min-w-0 flex-1 items-start gap-2.5 text-left"
      >
        <span
          className="mt-0.5 size-7 shrink-0 rounded-full"
          style={{
            background: `linear-gradient(140deg, hsl(${titleHue(s.title)} 38% 38%), hsl(${(titleHue(s.title) + 45) % 360} 55% 55%))`,
          }}
        />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-baseline justify-between gap-2">
          <span className="truncate text-[13px] font-medium">
            {s.title || t('chat.emptyTitle')}
          </span>
          <span className="shrink-0 text-[11px] text-muted-foreground">
            {when(s.at)}
          </span>
        </span>
        <span className="truncate text-[11px] text-muted-foreground">
          {t('chat.turns', { n: s.turns })}
          {s.live ? ` · ${t('chat.thisOne')}` : ''}
        </span>
        </span>
      </button>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              size="icon-xs"
              variant="ghost"
              className="absolute top-1.5 right-1.5 opacity-0 group-hover/row:opacity-100 aria-expanded:opacity-100"
            />
          }
        >
          <IconDots />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="p-1.5">
          <DropdownMenuItem className="gap-2 px-2 py-1.5" onClick={props.onRename}>
            <IconPencil />
            {t('ws.rename')}
          </DropdownMenuItem>
          {!s.live ? (
            <DropdownMenuItem className="gap-2 px-2 py-1.5" onClick={props.onResume}>
              <IconArrowRight />
              {t('chat.continueOne')}
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem className="gap-2 px-2 py-1.5" onClick={props.onExport}>
            <IconDownload />
            {t('chat.export')}
          </DropdownMenuItem>
          {/* The live row deletes like any other: the runtime stops what is
           * running, leaves a blank conversation, and drops the record. */}
          <DropdownMenuItem
            className="gap-2 px-2 py-1.5"
            variant="destructive"
            onClick={props.onForget}
          >
            <IconTrash />
            {t('chat.forgetOne')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

/* ---- the workspace -------------------------------------------------------- */

export function AgentWorkspace(props: {
  desktop: ReturnType<typeof useDesktopStream>
  /* The live desktop, as an element this component only PLACES. The stream,
   * the input pipe and the cursor all stay the shell's business. */
  screen: React.ReactNode
  name: string
  expanded: boolean
  onExpand(v: boolean): void
  onSettings(): void
  onLogout?: (() => void) | undefined
  /** The stream's sound, as the top bar shows it — one state, two buttons. */
  muted: boolean
  onAudio(): void
}) {
  const { t } = useTranslation()
  const d = props.desktop
  const chat = d.chat
  const term = d.console
  const { agent } = chat

  const [draft, setDraft] = useState('')
  const [search, setSearch] = useState('')
  const [picker, setPicker] = useState(false)
  const [wipe, setWipe] = useState(false)
  const [copied, setCopied] = useState(false)
  const [commandsOpen, setCommandsOpen] = useState(false)
  /* Which session row is being renamed, if any. */
  const [renaming, setRenaming] = useState(0)
  const [userOpen, setUserOpen] = useState(false)
  const [langOpen, setLangOpen] = useState(false)
  const [panelOpen, setPanelOpen] = useState(
    () => localStorage.getItem(PANEL_KEY) !== 'shut',
  )
  /* The canvas: the desktop as a SIDE PANEL — menu | chat | canvas, the
   * ChatGPT-canvas posture the owner picked from the three mockups. It
   * opens from the composer's Desktop chip, and opens ITSELF when the agent
   * starts working: acting on the desktop is what the window is for. */
  const [canvasOpen, setCanvasOpen] = useState(
    () => localStorage.getItem(CANVAS_KEY) === 'open',
  )
  const [canvasW, setCanvasW] = useState(() => {
    const saved = Number(localStorage.getItem(CANVAS_W_KEY) || 0)
    return saved >= 380 && saved <= 1200 ? saved : 600
  })
  /* What the canvas shows: the live desktop, or one captured file. */
  const [canvasView, setCanvasView] = useState<'live' | string>('live')
  /* Captures and recordings, held for preview: id → object URL + kind.
   * Built as deliveries arrive; the blobs outlive the delivery tray so a
   * preview never dies under the reader. */
  const [media, setMedia] = useState<
    readonly { id: string; name: string; kind: 'image' | 'video' | 'file'; url: string; size: number }[]
  >([])
  const mediaRef = useRef(media)
  mediaRef.current = media
  useEffect(() => {
    localStorage.setItem(PANEL_KEY, panelOpen ? 'open' : 'shut')
  }, [panelOpen])
  useEffect(() => {
    localStorage.setItem(CANVAS_KEY, canvasOpen ? 'open' : 'shut')
    /* Four columns is a crowd: the canvas opening folds the session panel.
     * ctrl+b brings it back for whoever has the width. */
    if (canvasOpen) setPanelOpen(false)
  }, [canvasOpen])
  useEffect(() => {
    localStorage.setItem(CANVAS_W_KEY, String(canvasW))
  }, [canvasW])

  /* A run starting is the second way the canvas opens — watching the agent
   * act is the point of the panel. Only the TRANSITION opens it, so closing
   * it mid-run stays closed. */
  const busyBefore = useRef('')
  useEffect(() => {
    if (busyBefore.current === '' && chat.busy !== '') setCanvasOpen(true)
    busyBefore.current = chat.busy
  }, [chat.busy])

  /* Deliveries — screenshots, recordings — arrive held (App set the hold),
   * get pulled into a Blob, and open in the canvas as a preview. */
  const seenDeliveries = useRef(new Set<string>())
  useEffect(() => {
    for (const dv of d.deliveries) {
      if (dv.status !== 'ready' || seenDeliveries.current.has(dv.id)) continue
      seenDeliveries.current.add(dv.id)
      const name = dv.name
      void d
        .previewDelivery(dv.id)
        .then((blob) => {
          const ext = (name.split('.').pop() || '').toLowerCase()
          const kind = ['png', 'jpg', 'jpeg', 'webp', 'gif'].includes(ext)
            ? ('image' as const)
            : ['webm', 'mp4', 'mkv', 'mov'].includes(ext)
              ? ('video' as const)
              : ('file' as const)
          const url = URL.createObjectURL(
            kind === 'video' ? new Blob([blob], { type: 'video/webm' }) : blob,
          )
          setMedia((prev) => [...prev, { id: dv.id, name, kind, url, size: dv.size }])
          setCanvasView(dv.id)
          setCanvasOpen(true)
          /* The tray card would say the same thing twice. */
          d.dismissDelivery(dv.id)
        })
        .catch(() => {
          seenDeliveries.current.delete(dv.id)
        })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [d.deliveries])
  /* Object URLs live as long as the workspace; freed on unmount. */
  useEffect(
    () => () => {
      for (const m of mediaRef.current) URL.revokeObjectURL(m.url)
    },
    [],
  )

  const dropMedia = useCallback((id: string) => {
    setMedia((prev) => {
      const gone = prev.find((m) => m.id === id)
      if (gone) URL.revokeObjectURL(gone.url)
      return prev.filter((m) => m.id !== id)
    })
    setCanvasView((v) => (v === id ? 'live' : v))
  }, [])

  const downloadMedia = useCallback((m: { url: string; name: string }) => {
    const a = document.createElement('a')
    a.href = m.url
    a.download = m.name
    document.body.appendChild(a)
    a.click()
    a.remove()
  }, [])

  /* The divider: grab, clamp so neither the chat nor the canvas can vanish,
   * remember. Pointer capture keeps the drag alive over the video. */
  const onGrabDivider = useCallback((e: React.PointerEvent) => {
    e.preventDefault()
    const handle = e.currentTarget as HTMLElement
    handle.setPointerCapture(e.pointerId)
    const startX = e.clientX
    const startW = canvasW
    const move = (ev: PointerEvent) => {
      const next = Math.min(
        Math.max(startW + (startX - ev.clientX), 380),
        Math.max(380, window.innerWidth - 260 - 380),
      )
      setCanvasW(next)
    }
    const up = () => {
      handle.removeEventListener('pointermove', move)
      handle.removeEventListener('pointerup', up)
      handle.removeEventListener('pointercancel', up)
    }
    handle.addEventListener('pointermove', move)
    handle.addEventListener('pointerup', up)
    handle.addEventListener('pointercancel', up)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canvasW])

  const inputRef = useRef<HTMLTextAreaElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  /* ctrl+b, exactly the terminal's key for the same panel. */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'b' && e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault()
        setPanelOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  /* The roster loads when the RUNTIME is there to answer, and refreshes when
   * a run ends — a finished exchange is what changes it. Gated on ready and
   * not on mount, because a request sent while the link is still coming up is
   * dropped silently and nothing ever retried it: the drawer sat on "no past
   * conversations yet" over a database holding plenty. */
  const busy = chat.busy !== ''
  useEffect(() => {
    if (!agent.ready) return
    chat.loadSessions()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [busy, agent.ready])

  /* A reloaded browser used to show an empty thread while the roster still
   * marked a live conversation — the words were in the runtime all along.
   * Once per connection, an empty live thread asks to be re-filled. */
  const hydrated = useRef(false)
  useEffect(() => {
    if (!agent.ready) {
      hydrated.current = false
      return
    }
    if (hydrated.current) return
    hydrated.current = true
    if (chat.messages.length === 0 && chat.viewing === 0) chat.hydrate()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.ready])

  /* The composer grows with the text, up to the cap below. Measured rather
   * than counted in rows, because a pasted paragraph and five short lines are
   * the same height and different row counts. */
  const grow = useCallback(() => {
    const el = inputRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`
  }, [])
  useEffect(grow, [draft, grow])

  const past = chat.viewing !== 0
  const canType = agent.ready && !past

  const submit = useCallback(() => {
    const body = draft.trim()
    if (!body || !agent.ready) return
    /* `/model` with nothing after it is a question, not a command — answered
     * with the list rather than sent to a runtime that can only say no model
     * was named. */
    if (body === '/model') {
      setPicker(true)
      setDraft('')
      return
    }
    /* `/connect` opens the TERMINAL. The flow is a conversation — a link, a
     * sign-in, a code to bring back — and the agent's own view already runs
     * the whole of it. The runtime would refuse the command from here anyway:
     * it is marked local and never announced to browsers. */
    if (body === '/connect' || body.startsWith('/connect ')) {
      term.start()
      setDraft('')
      return
    }
    chat.ask(body)
    setDraft('')
    const el = inputRef.current
    if (el) el.style.height = 'auto'
  }, [draft, agent.ready, chat, term])

  /* Only while the VERB is being typed: once there is a space the person is
   * writing an argument, and a list hovering over their sentence is noise. */
  const palette = (() => {
    const typed = draft.trim()
    if (!typed.startsWith('/') || typed.includes(' ')) return []
    return chat.commands.filter((c) => c.name.startsWith(typed))
  })()

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

  const sessions = useMemo(() => {
    const needle = search.trim().toLowerCase()
    if (!needle) return chat.sessions
    return chat.sessions.filter((s) => s.title.toLowerCase().includes(needle))
  }, [chat.sessions, search])

  /* The session's totals, accumulated from every ending on screen — the same
   * numbers the terminal's sidebar shows, from the wire this face gets. */
  const totals = useMemo(() => {
    let turns = 0
    let calls = 0
    let toks = 0
    for (const m of chat.messages) {
      if (m.ending) {
        turns += m.ending.turns
        calls += m.ending.calls
        toks += m.ending.inToks + m.ending.outToks
      }
    }
    return { turns, calls, toks }
  }, [chat.messages])

  /* What the run is DOING right now — the last tool of the open bubble, or
   * nothing, which the activity line reads as "waiting for the agent". */
  const activity = useMemo(() => {
    for (let i = chat.messages.length - 1; i >= 0; i--) {
      const m = chat.messages[i]
      if (m.role === 'agent' && m.streaming) {
        return m.steps.length ? m.steps[m.steps.length - 1].tool : ''
      }
    }
    return ''
  }, [chat.messages])

  const yours = d.control.yours
  const holder = d.control.holder
  const agentDrives = !yours && !!holder && d.control.holderIsAgent

  /* ---- expanded: the desktop borrowed, the conversation as a strip ------- */
  if (props.expanded) {
    const lastAgent = [...chat.messages].reverse().find((m) => m.role === 'agent' && m.text)
    return (
      <div className="relative flex min-h-0 min-w-0 flex-1">
        <div className="absolute inset-0">{props.screen}</div>

        <button
          type="button"
          onClick={() => props.onExpand(false)}
          className="absolute top-3 left-3.5 z-20 flex items-center gap-1.5 rounded-full border bg-background/80 py-1 pr-3 pl-2 text-xs backdrop-blur hover:bg-popover"
        >
          <IconX className="size-3.5 text-muted-foreground" />
          <span className="text-muted-foreground">{t('ws.backTo')}</span>
          <span className="font-medium">
            {chat.sessions.find((s) => s.live)?.title || t('chat.title')}
          </span>
        </button>

        <div className="absolute bottom-4 left-1/2 z-20 flex w-[min(620px,calc(100%-32px))] -translate-x-1/2 flex-col gap-2">
          {lastAgent ? (
            <div className="max-h-28 overflow-y-auto rounded-2xl border bg-background/85 px-3.5 py-2 text-[12.5px] leading-relaxed shadow-lg backdrop-blur">
              {lastAgent.text}
            </div>
          ) : null}
          <div className="flex items-center gap-2 rounded-full border bg-background/85 py-1.5 pr-1.5 pl-3.5 shadow-lg backdrop-blur">
            <input
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  submit()
                }
                e.stopPropagation()
              }}
              onKeyUp={(e) => e.stopPropagation()}
              disabled={!canType}
              placeholder={t('ws.keepTalking')}
              className="min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted-foreground"
            />
            {busy ? (
              <Button size="icon-sm" variant="outline" className="rounded-full" onClick={chat.stop} title={t('chat.stop')}>
                <IconPlayerStopFilled className="size-3" />
              </Button>
            ) : (
              <Button
                size="icon-sm"
                className="rounded-full"
                disabled={!canType || draft.trim() === ''}
                onClick={submit}
                title={t('chat.send')}
              >
                <IconChevronUp />
              </Button>
            )}
          </div>
        </div>
      </div>
    )
  }

  /* ---- the three columns ------------------------------------------------- */
  return (
    <div className="flex min-h-0 min-w-0 flex-1 bg-background text-foreground">
      {/* A. the sidebar */}
      <aside className="relative flex w-[300px] shrink-0 flex-col border-r bg-sidebar max-[860px]:hidden">
        <div className="flex items-center justify-between px-3 pt-3 pb-2">
          <span className="text-[11px] font-semibold tracking-[.09em] text-muted-foreground uppercase">
            {t('ws.sessions')}
          </span>
          <Button size="icon-xs" variant="ghost" onClick={chat.reset} title={t('chat.new')}>
            <IconPlus />
          </Button>
        </div>

        <div className="px-3 pb-2">
          <InputGroup className="h-9 rounded-lg bg-background text-sm">
            <InputGroupInput
              aria-label={t('ws.search')}
              placeholder={t('ws.search')}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <InputGroupAddon>
              <IconSearch />
            </InputGroupAddon>
          </InputGroup>
        </div>

        <div className="scroll-fade-b min-h-0 flex-1 overflow-y-auto px-1.5">
          {chat.denied !== null ? (
            <div className="m-2 flex flex-col gap-2 rounded-lg border border-amber-500/50 bg-popover p-3 text-xs" role="alert">
              <span>
                {chat.denied
                  ? t('chat.forgetNeedsControlFrom', { who: chat.denied })
                  : t('chat.forgetNeedsControl')}
              </span>
              <Button size="xs" variant="outline" className="self-end" onClick={chat.clearDenied}>
                {t('chat.ok')}
              </Button>
            </div>
          ) : null}

          {wipe ? (
            <div className="m-2 rounded-lg border border-destructive bg-popover p-3 text-xs" role="alertdialog">
              <div className="mb-1 font-semibold">{t('chat.forgetAllSure')}</div>
              <div className="leading-relaxed text-muted-foreground">
                {t('chat.forgetAllWhy', {
                  n: chat.sessions.filter((x) => !x.live).length,
                })}
                {chat.sessions.some((x) => x.live) ? ` ${t('chat.forgetAllKeepsLive')}` : ''}
              </div>
              <div className="mt-2 flex gap-1.5">
                <Button size="xs" variant="outline" className="flex-1" onClick={() => setWipe(false)}>
                  {t('chat.cancel')}
                </Button>
                <Button
                  size="xs"
                  variant="destructive"
                  className="flex-1"
                  onClick={() => {
                    chat.forgetAll()
                    setWipe(false)
                  }}
                >
                  {t('chat.forgetAllDo')}
                </Button>
              </div>
            </div>
          ) : null}

          {sessions.length === 0 ? (
            <div className="m-2 rounded-lg border border-dashed p-5 text-center text-xs leading-relaxed text-muted-foreground">
              {search.trim() ? t('ws.noMatch', { q: search.trim() }) : t('chat.noHistory')}
            </div>
          ) : (
            sessions.map((s) => (
              <SessionRow
                key={s.id}
                session={s}
                on={s.live ? chat.viewing === 0 : chat.viewing === s.id}
                editing={renaming === s.id}
                onOpen={() => chat.openSession(s.live ? 0 : s.id)}
                onResume={() => chat.resume(s.id)}
                onExport={() => chat.exportSession(s.id)}
                onForget={() => chat.forget(s.id)}
                onRename={() => setRenaming(s.id)}
                onRenamed={(title) => {
                  setRenaming(0)
                  const clean = title.trim()
                  /* Escape (empty) keeps the old name; an unchanged name is
                   * not a request. */
                  if (clean && clean !== s.title) chat.rename(s.id, clean)
                }}
              />
            ))
          )}

          {chat.sessions.some((x) => !x.live) ? (
            <button
              type="button"
              className="mx-2 my-2 text-[11px] text-muted-foreground hover:text-destructive"
              onClick={() => setWipe(true)}
            >
              {t('chat.forgetAll')}
            </button>
          ) : null}
        </div>

        {/* the foot: skills, then you — the ChatGPT gesture, our dress */}
        <div className="relative flex flex-col gap-px border-t p-1.5">
          <button
            type="button"
            className="flex h-10 items-center gap-2 rounded-lg px-2 text-[13px] hover:bg-foreground/5"
            onClick={() => setCommandsOpen(true)}
          >
            <span className="flex w-7 justify-center text-muted-foreground">
              <IconBox className="size-[17px]" />
            </span>
            {t('ws.skills')}
          </button>

          <button
            type="button"
            className={cn(
              'flex h-10 items-center gap-2 rounded-lg px-2 text-[13px] hover:bg-foreground/5',
              userOpen && 'bg-foreground/5',
            )}
            aria-expanded={userOpen}
            onClick={() => {
              setUserOpen((v) => !v)
              setLangOpen(false)
            }}
          >
            <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-muted-foreground/10 text-xs text-foreground/70">
              {initials(props.name || t('ws.you'))}
            </span>
            <span className="min-w-0 flex-1 truncate text-left">
              {props.name || t('ws.you')}
            </span>
            <IconChevronUp className={cn('size-3.5 text-muted-foreground transition-transform', userOpen && 'rotate-180')} />
          </button>

          {userOpen ? (
            <div className="absolute right-1.5 bottom-[calc(100%-4px)] left-1.5 z-20 rounded-lg border bg-popover p-1.5 shadow-xl">
              <div className="px-2 py-1.5">
                <div className="text-[13px] font-medium">{props.name || t('ws.you')}</div>
                <div className="text-[11px] text-muted-foreground">{statusLine(agent, t)}</div>
              </div>
              <div className="mx-1 mb-1 h-px bg-border" />
              <button
                type="button"
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-[13px] hover:bg-foreground/5"
                onClick={() => {
                  setUserOpen(false)
                  props.onSettings()
                }}
              >
                <IconSettings className="size-4 text-muted-foreground" />
                {t('ws.settings')}
              </button>
              {langOpen ? (
                LANGUAGES.map((l) => (
                  <button
                    key={l.code}
                    type="button"
                    className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-[13px] hover:bg-foreground/5"
                    onClick={() => {
                      setLanguage(l.code)
                      setLangOpen(false)
                      setUserOpen(false)
                    }}
                  >
                    <span className="w-4" />
                    {l.name}
                    {i18n.language === l.code ? (
                      <span className="ml-auto text-[11px] text-muted-foreground">✓</span>
                    ) : null}
                  </button>
                ))
              ) : (
                <button
                  type="button"
                  className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-[13px] hover:bg-foreground/5"
                  onClick={() => setLangOpen(true)}
                >
                  <IconLanguage className="size-4 text-muted-foreground" />
                  {t('label.language')}
                  <span className="ml-auto text-[11px] text-muted-foreground">
                    {LANGUAGES.find((l) => l.code === i18n.language)?.chip ?? ''}
                  </span>
                </button>
              )}
              <ThemeItem />
              <a
                href={DOCS_URL}
                target="_blank"
                rel="noreferrer"
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-[13px] hover:bg-foreground/5"
              >
                <IconExternalLink className="size-4 text-muted-foreground" />
                {t('ws.docs')}
              </a>
              {props.onLogout ? (
                <>
                  <div className="mx-1 my-1 h-px bg-border" />
                  <button
                    type="button"
                    className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-[13px] text-destructive hover:bg-destructive/10"
                    onClick={() => {
                      setUserOpen(false)
                      props.onLogout?.()
                    }}
                  >
                    <IconLogout className="size-4" />
                    {t('label.logout')}
                  </button>
                </>
              ) : null}
            </div>
          ) : null}
        </div>
      </aside>

      {/* B. the conversation */}
      <main className="relative flex min-w-0 flex-1 flex-col">
        {term.open ? <Console term={term} onClose={term.stop} /> : null}

        {past ? (
          <div className="mx-auto mt-2 flex w-full max-w-[760px] items-center justify-center gap-2.5 rounded-lg border border-amber-500/50 bg-popover px-3 py-2 text-xs" role="status">
            <span>{t('chat.viewingPast')}</span>
            <Button size="xs" variant="outline" onClick={() => chat.resume(chat.viewing)}>
              {t('chat.continueHere')}
            </Button>
            <Button size="xs" variant="outline" onClick={() => chat.openSession(0)}>
              {t('chat.backToLive')}
            </Button>
          </div>
        ) : null}

        <MessageScrollerProvider>
          <MessageScroller className="min-h-0 flex-1">
            <MessageScrollerViewport className="px-5">
              <MessageScrollerContent className="mx-auto w-full max-w-[760px] gap-4 pt-6 pb-2">
                {!agent.ready ? (
                  <Gate agent={agent} copied={copied} onCopy={copyRemedy} />
                ) : null}

                {chat.messages.length === 0 && agent.ready ? (
                  <div className="m-auto flex max-w-[460px] flex-col items-center gap-2 py-16 text-center">
                    <div className="text-xl font-semibold tracking-tight">
                      {t('chat.emptyTitle')}
                    </div>
                    <p className="text-[13.5px] leading-relaxed text-muted-foreground">
                      {t('chat.emptyHint')}
                    </p>
                  </div>
                ) : null}

                {chat.messages.map((m) => (
                  <MessageScrollerItem key={m.key}>
                    <Row m={m} />
                  </MessageScrollerItem>
                ))}

                {/* The one line that answers "is anything happening, and for
                 * how long" — the whole run, not only the wait before the
                 * first word. The Claude Code shape the owner pointed at. */}
                {busy && chat.viewing === 0 ? (
                  <MessageScrollerItem key="activity">
                    <ActivityLine since={chat.since} label={activity} />
                  </MessageScrollerItem>
                ) : null}
              </MessageScrollerContent>
            </MessageScrollerViewport>
            <MessageScrollerButton />
          </MessageScroller>
        </MessageScrollerProvider>

        {/* the composer */}
        <div className="mx-auto w-full max-w-[760px] shrink-0 px-5 pt-3 pb-4">
          <div className="relative">
            {picker ? (
              <div className="absolute right-0 bottom-[calc(100%+6px)] left-0 z-20 max-h-72 overflow-y-auto rounded-lg border bg-popover p-1.5 shadow-xl" role="listbox">
                {agent.models.length === 0 ? (
                  <div className="px-2 py-1.5 text-xs text-muted-foreground">{t('chat.noModels')}</div>
                ) : (
                  [...agent.models]
                    /* Ordered by how likely you are to want it: the provider
                     * you are ON first, then the reachable, then the locked —
                     * shown rather than hidden, because "which exist" and
                     * "which can I use" are different questions. */
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
                          type="button"
                          disabled={!mo.ready}
                          className="flex w-full items-baseline gap-3 rounded-md px-2 py-1.5 text-left hover:bg-foreground/5 disabled:opacity-50"
                          onClick={() => {
                            chat.ask(`/model ${name}`)
                            setPicker(false)
                          }}
                        >
                          <span className="min-w-24 shrink-0 font-mono text-xs">
                            {on ? '● ' : ''}
                            {name}
                          </span>
                          <span className="truncate text-xs text-muted-foreground">
                            {mo.ready ? mo.note : t('chat.modelLocked')}
                          </span>
                        </button>
                      )
                    })
                )}
              </div>
            ) : null}

            {palette.length > 0 ? (
              <div className="absolute right-0 bottom-[calc(100%+6px)] left-0 z-20 max-h-72 overflow-y-auto rounded-lg border bg-popover p-1.5 shadow-xl" role="listbox">
                {palette.map((c) => (
                  <button
                    key={c.name}
                    type="button"
                    className="flex w-full items-baseline gap-3 rounded-md px-2 py-1.5 text-left hover:bg-foreground/5"
                    onClick={() => {
                      setDraft(c.name + ' ')
                      inputRef.current?.focus()
                    }}
                  >
                    <span className="min-w-24 shrink-0 font-mono text-xs">{c.name}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {i18n.exists(`chat.${c.id}`) ? t(`chat.${c.id}`) : c.what}
                    </span>
                  </button>
                ))}
              </div>
            ) : null}

            <div className="rounded-2xl border bg-card px-3 pt-2.5 pb-2 focus-within:border-ring">
              <textarea
                ref={inputRef}
                rows={1}
                value={draft}
                disabled={!canType}
                placeholder={
                  past
                    ? t('chat.placeholderPast')
                    : agent.ready
                      ? t('chat.placeholder')
                      : t('chat.placeholderOff')
                }
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    submit()
                  }
                  e.stopPropagation()
                }}
                onKeyUp={(e) => e.stopPropagation()}
                className="max-h-44 w-full resize-none bg-transparent text-sm leading-relaxed outline-none placeholder:text-muted-foreground"
              />
              <div className="mt-2.5 flex items-center gap-2">
                <input
                  ref={fileRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={(e) => {
                    /* Attach = the drop layer's path, from a button: handing
                     * a file to the room is provisioning, not driving. */
                    for (const f of Array.from(e.target.files ?? [])) {
                      void d.uploadFile(f, () => {}).catch(() => {})
                    }
                    e.target.value = ''
                  }}
                />
                <button
                  type="button"
                  className="flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-[11px] text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
                  onClick={() => fileRef.current?.click()}
                >
                  <IconPlus className="size-3.5" />
                  {t('ws.attach')}
                </button>
                <button
                  type="button"
                  className={cn(
                    'flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-[11px] text-muted-foreground hover:bg-foreground/5 hover:text-foreground',
                    canvasOpen && 'bg-foreground/5 text-foreground',
                  )}
                  aria-pressed={canvasOpen}
                  onClick={() => setCanvasOpen((v) => !v)}
                >
                  <IconDeviceDesktop className="size-3.5" />
                  {t('ws.desktopChip')}
                </button>
                <span className="ml-auto flex items-center gap-1">
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    className={cn(term.open && 'bg-muted')}
                    onClick={() => (term.open ? term.stop() : term.start())}
                    title={t('chat.console')}
                    aria-pressed={term.open}
                  >
                    <IconTerminal2 />
                  </Button>
                  {busy ? (
                    <Button size="icon-sm" variant="outline" className="rounded-full" onClick={chat.stop} title={t('chat.stop')}>
                      <IconPlayerStopFilled className="size-3" />
                    </Button>
                  ) : (
                    <Button
                      size="icon-sm"
                      className="rounded-full"
                      disabled={!canType || draft.trim() === ''}
                      onClick={submit}
                      title={t('chat.send')}
                    >
                      <IconChevronUp />
                    </Button>
                  )}
                </span>
              </div>
            </div>
            <div className="mt-2 truncate text-center text-[11px] text-muted-foreground/70">
              {busy
                ? `${t('chat.working')}${agent.model ? ` · ${agent.model}` : ''}`
                : `${t('ws.composerHint')}${agent.model ? ` · ${agent.model}` : ''}${agent.mode ? ` · ${agent.mode}` : ''}`}
            </div>
          </div>
        </div>
      </main>

      {/* C. the canvas — the desktop (and every capture) as a side panel,
          the split-anchored posture: menu | chat | canvas. The divider
          drags; the chip in the composer and a starting run both open it. */}
      {canvasOpen ? (
        <>
          <div
            role="separator"
            aria-orientation="vertical"
            onPointerDown={onGrabDivider}
            className="relative z-10 -mx-[2px] w-[5px] shrink-0 cursor-col-resize"
          />
          <section
            style={{ width: canvasW }}
            className="flex min-h-0 shrink-0 flex-col border-l bg-card max-[1000px]:absolute max-[1000px]:inset-y-0 max-[1000px]:right-0 max-[1000px]:z-30 max-[1000px]:shadow-2xl"
          >
            <div className="flex h-10 shrink-0 items-center gap-2 border-b px-3">
              {canvasView === 'live' ? (
                <>
                  <span
                    className={cn(
                      'size-[5px] shrink-0 rounded-full',
                      d.state === 'live'
                        ? 'bg-[var(--sd-drive)] shadow-[0_0_6px_var(--sd-drive)]'
                        : 'bg-muted-foreground',
                    )}
                  />
                  <span className="truncate text-xs font-medium">{t('ws.stage')}</span>
                  <span
                    className={cn(
                      'ml-1 hidden truncate rounded-full px-2 py-0.5 text-[10.5px] min-[520px]:inline',
                      yours || agentDrives
                        ? 'bg-[color-mix(in_srgb,var(--sd-drive)_12%,transparent)] text-[var(--sd-drive)]'
                        : 'bg-muted text-muted-foreground',
                    )}
                  >
                    {yours
                      ? t('room.youControl')
                      : agentDrives
                        ? t('ws.agentDrives')
                        : holder
                          ? t('room.controlledBy', { name: holder })
                          : t('room.free')}
                  </span>
                  <span className="ml-auto flex items-center gap-1">
                    {/* Capture lives HERE and not in the top bar because in
                     * this mode the result lands here: the server writes the
                     * file, offers it on the files channel, and the delivery
                     * effect above opens it as the canvas view. Neither verb
                     * needs the controls — capturing is not driving. */}
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      onClick={() => d.sendInput({ t: 'capture', action: 'shot' })}
                      title={t('ws.shot')}
                    >
                      <CamGlyph />
                    </Button>
                    {d.recording ? <RecClock since={d.recordingSince} /> : null}
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      className={cn(d.recording && 'text-red-500 hover:text-red-500')}
                      onClick={() =>
                        d.sendInput({
                          t: 'capture',
                          action: d.recording ? 'rec_stop' : 'rec_start',
                        })
                      }
                      title={t(d.recording ? 'ws.recStop' : 'ws.rec')}
                    >
                      <RecGlyph on={d.recording} />
                    </Button>
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      className={cn(
                        /* Green while sound is ON — the top bar's `live`
                         * state, spoken in this header's dialect. */
                        !props.muted &&
                          'text-[var(--sd-drive)] hover:text-[var(--sd-drive)]',
                      )}
                      onClick={props.onAudio}
                      title={t('toolbar.audio')}
                    >
                      <AudioGlyph muted={props.muted} />
                    </Button>
                    <Button size="xs" variant={yours ? 'outline' : 'default'} onClick={d.toggleControl}>
                      {yours ? t('room.release') : t('room.take')}
                    </Button>
                    <Button size="icon-xs" variant="ghost" onClick={() => props.onExpand(true)} title={t('ws.expand')}>
                      <IconArrowsDiagonal />
                    </Button>
                    <Button size="icon-xs" variant="ghost" onClick={() => setCanvasOpen(false)} title={t('chat.close')}>
                      <IconX />
                    </Button>
                  </span>
                </>
              ) : (
                (() => {
                  const m = media.find((x) => x.id === canvasView)
                  if (!m) return null
                  return (
                    <>
                      <span className="truncate text-xs font-medium">{m.name}</span>
                      <span className="text-[10.5px] text-muted-foreground">
                        {m.size >= 1048576
                          ? `${(m.size / 1048576).toFixed(1)} MB`
                          : `${Math.max(1, Math.round(m.size / 1024))} KB`}
                      </span>
                      <span className="ml-auto flex items-center gap-1">
                        {/* A recording does not pause because somebody looked
                         * at a screenshot; the stop stays reachable. */}
                        {d.recording ? (
                          <>
                            <RecClock since={d.recordingSince} />
                            <Button
                              size="icon-xs"
                              variant="ghost"
                              className="text-red-500 hover:text-red-500"
                              onClick={() => d.sendInput({ t: 'capture', action: 'rec_stop' })}
                              title={t('ws.recStop')}
                            >
                              <RecGlyph on />
                            </Button>
                          </>
                        ) : null}
                        <Button size="xs" variant="outline" onClick={() => downloadMedia(m)}>
                          <IconDownload />
                          {t('ws.download')}
                        </Button>
                        <Button size="icon-xs" variant="ghost" onClick={() => dropMedia(m.id)} title={t('ws.remove')}>
                          <IconTrash />
                        </Button>
                        <Button size="icon-xs" variant="ghost" onClick={() => setCanvasOpen(false)} title={t('chat.close')}>
                          <IconX />
                        </Button>
                      </span>
                    </>
                  )
                })()
              )}
            </div>

            <div className="relative min-h-0 flex-1 bg-black">
              {canvasView === 'live' ? (
                <div className="absolute inset-0">{props.screen}</div>
              ) : (
                (() => {
                  const m = media.find((x) => x.id === canvasView)
                  if (!m) return null
                  if (m.kind === 'image') {
                    return (
                      <img
                        src={m.url}
                        alt={m.name}
                        className="absolute inset-0 h-full w-full object-contain"
                      />
                    )
                  }
                  if (m.kind === 'video') {
                    return (
                      // eslint-disable-next-line jsx-a11y/media-has-caption
                      <video src={m.url} controls autoPlay className="absolute inset-0 h-full w-full object-contain" />
                    )
                  }
                  return (
                    <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 text-muted-foreground">
                      <IconDownload className="size-6" />
                      <span className="text-xs">{m.name}</span>
                      <Button size="sm" variant="outline" onClick={() => downloadMedia(m)}>
                        {t('ws.download')}
                      </Button>
                    </div>
                  )
                })()
              )}
            </div>

            {/* the reel: the live desktop plus every capture, one chip each */}
            {media.length > 0 ? (
              <div className="flex shrink-0 items-center gap-1.5 overflow-x-auto border-t px-2 py-1.5">
                <button
                  type="button"
                  onClick={() => setCanvasView('live')}
                  className={cn(
                    'flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-1 text-[11px]',
                    canvasView === 'live'
                      ? 'bg-foreground/10 text-foreground'
                      : 'text-muted-foreground hover:bg-foreground/5',
                  )}
                >
                  <span className="size-[5px] rounded-full bg-[var(--sd-drive)]" />
                  {t('ws.live')}
                </button>
                {media.map((m) => (
                  <button
                    key={m.id}
                    type="button"
                    onClick={() => setCanvasView(m.id)}
                    className={cn(
                      'flex max-w-40 shrink-0 items-center gap-1.5 rounded-md border px-2 py-1 text-[11px]',
                      canvasView === m.id
                        ? 'bg-foreground/10 text-foreground'
                        : 'text-muted-foreground hover:bg-foreground/5',
                    )}
                    title={m.name}
                  >
                    {m.kind === 'video' ? (
                      <IconPlayerPlayFilled className="size-3" />
                    ) : (
                      <IconPhoto className="size-3" />
                    )}
                    <span className="truncate">{m.name}</span>
                  </button>
                ))}
              </div>
            ) : null}
          </section>
        </>
      ) : null}

      {/* C2. the session panel — the terminal's /panel, ctrl+b here too */}
      {panelOpen ? (
        <aside className="flex w-[270px] shrink-0 flex-col overflow-y-auto border-l bg-sidebar p-3.5 max-[1180px]:hidden">
          <div className="mb-3 flex items-center gap-2">
            <span className="flex-1 text-[11px] font-semibold tracking-[.09em] text-muted-foreground uppercase">
              {t('ws.panel')}
            </span>
            <kbd className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
              ctrl+b
            </kbd>
          </div>

          <div className="flex items-center gap-2 text-xs leading-relaxed">
            <span
              className={cn(
                'size-1.5 shrink-0 rounded-full',
                agent.ready
                  ? 'bg-[var(--sd-drive)]'
                  : agent.present
                    ? 'bg-[var(--sd-watch)]'
                    : 'bg-muted-foreground',
              )}
            />
            <span className="break-words">{statusLine(agent, t)}</span>
          </div>

          <div className="my-3 h-px shrink-0 bg-border" />

          <div className="mb-2 text-[11px] font-semibold tracking-[.09em] text-muted-foreground uppercase">
            {t('ws.totals')}
          </div>
          {(
            [
              [t('chat.turns', { n: totals.turns }), totals.turns],
              [t('chat.calls', { n: totals.calls }), totals.calls],
              [t('chat.tokens', { n: totals.toks }), totals.toks],
            ] as const
          ).map(([label]) => (
            <div key={label} className="py-1 text-xs text-muted-foreground">
              {label}
            </div>
          ))}
          <TaskClock worked={chat.worked} since={chat.since} />
          {busy ? (
            <div className="mt-1 text-xs text-[var(--sd-drive)]">{t('chat.working')}</div>
          ) : null}

          <div className="my-3 h-px shrink-0 bg-border" />

          <div className="flex flex-col gap-1.5">
            <Button
              size="sm"
              variant="outline"
              className="justify-start gap-2"
              disabled={!agent.ready}
              onClick={() => chat.ask('/compact')}
            >
              {t('ws.compact')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="justify-start gap-2"
              disabled={!agent.ready}
              onClick={() => chat.ask('/rewind')}
            >
              {t('ws.rewind')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="justify-start gap-2"
              disabled={!agent.ready}
              onClick={() => {
                setDraft('/memory ')
                inputRef.current?.focus()
              }}
            >
              {t('ws.memory')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="justify-start gap-2"
              onClick={() => chat.exportSession(0)}
            >
              {t('chat.exportLive')}
            </Button>
          </div>
          <p className="mt-2 text-[10.5px] leading-relaxed text-muted-foreground">
            {t('ws.rewindNote')}
          </p>
        </aside>
      ) : null}

      {/* the commands sheet: the / palette, laid out to read */}
      <Dialog open={commandsOpen} onOpenChange={setCommandsOpen}>
        <DialogContent className="max-h-[80vh] overflow-y-auto sm:max-w-[560px]">
          <DialogHeader>
            <DialogTitle>{t('ws.skills')}</DialogTitle>
            <DialogDescription>{t('ws.skillsWhy')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col">
            {chat.commands.length === 0 ? (
              <div className="rounded-lg border border-dashed p-5 text-center text-xs text-muted-foreground">
                {t('chat.offline')}
              </div>
            ) : (
              chat.commands.map((c) => (
                <button
                  key={c.name}
                  type="button"
                  className="flex w-full items-baseline gap-3 border-t px-2 py-2 text-left first:border-t-0 hover:bg-foreground/5"
                  onClick={() => {
                    setCommandsOpen(false)
                    setDraft(c.name + ' ')
                    inputRef.current?.focus()
                  }}
                >
                  <span className="min-w-24 shrink-0 font-mono text-xs">{c.name}</span>
                  <span className="text-xs leading-relaxed text-muted-foreground">
                    {i18n.exists(`chat.${c.id}`) ? t(`chat.${c.id}`) : c.what}
                  </span>
                </button>
              ))
            )}
          </div>
          <div className="rounded-lg border border-dashed p-3 text-[11.5px] leading-relaxed text-muted-foreground">
            <b className="font-medium text-foreground">{t('ws.connectLocalTitle')}</b>{' '}
            {t('ws.connectLocal')}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/* The top bar's own drawings, so the canvas header speaks the same icon
 * language as Desktop mode. The stroke styling the .tb-tool CSS provides up
 * there is carried inline here, because these sit inside shadcn Buttons. */
function ToolGlyph(props: { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      className="size-3.5"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {props.children}
    </svg>
  )
}

function CamGlyph() {
  return (
    <ToolGlyph>
      <path d="M4 8.5h3l1.6-2.2h6.8L17 8.5h3v9.5H4z" />
      <circle cx="12" cy="13" r="3" />
    </ToolGlyph>
  )
}

function RecGlyph({ on }: { on: boolean }) {
  return (
    <ToolGlyph>
      <circle cx="12" cy="12" r="7.5" />
      {on ? (
        <rect x="8.8" y="8.8" width="6.4" height="6.4" rx="1" fill="currentColor" stroke="none" />
      ) : (
        <circle cx="12" cy="12" r="3" fill="currentColor" stroke="none" />
      )}
    </ToolGlyph>
  )
}

function AudioGlyph({ muted }: { muted: boolean }) {
  return (
    <ToolGlyph>
      <path d="M4.5 9.5h3l4-3.2v11.4l-4-3.2h-3z" />
      {muted ? (
        <path d="M16 9.5l5 5M21 9.5l-5 5" />
      ) : (
        <path d="M15.5 9a4.2 4.2 0 010 6M17.5 6.5a7.6 7.6 0 010 11" />
      )}
    </ToolGlyph>
  )
}

/* The activity line: one glowing mark, the running clock, and what the run
 * is doing right now — a tool's name while one is in flight, "waiting for
 * the agent" between them. Lives at the FOOT of the thread for the whole
 * run, the way Claude Code's own status line does; the folded Tools rows
 * above it stay quiet. */
function ActivityLine(props: { since: number; label: string }) {
  const { t } = useTranslation()
  const [, tick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(id)
  }, [])
  const clock = props.since ? fmtDur(Date.now() - props.since) : ''
  return (
    <div className="flex items-center gap-2 py-1 text-xs text-muted-foreground" role="status">
      <span className="animate-pulse text-sm leading-none text-[var(--sd-drive)]">✳</span>
      {clock ? <span className="tabular-nums">{clock}</span> : null}
      <span className="min-w-0 truncate">
        {clock ? '· ' : ''}
        {props.label ? (
          <span className="font-mono text-[11px]">{props.label}</span>
        ) : (
          t('ws.waiting')
        )}
      </span>
    </div>
  )
}

/* The session's clock, under turns/tools/tokens: finished exchanges plus the
 * one in flight, ticking while it runs — so "how long has this taken" has an
 * answer at every moment, not only at the end. */
function TaskClock(props: { worked: number; since: number }) {
  const { t } = useTranslation()
  const [, tick] = useState(0)
  useEffect(() => {
    if (!props.since) return
    const id = setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(id)
  }, [props.since])
  const total = props.worked + (props.since ? Date.now() - props.since : 0)
  if (!total) return null
  return (
    <div className="py-1 text-xs text-muted-foreground">
      {t('chat.elapsed', { v: fmtDur(total) })}
    </div>
  )
}

/* The recording clock, the canvas header's size: mm:ss in red beside the
 * stop button, ticking on its own so the header does not re-render around
 * it. The top bar has its twin; this one exists because in Agent mode the
 * top bar's tools are not on screen. */
function RecClock(props: { since: number }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 500)
    return () => clearInterval(id)
  }, [])
  const s = Math.max(0, Math.floor((Date.now() - props.since) / 1000))
  return (
    <span className="font-mono text-[10.5px] tabular-nums text-red-500">
      {String(Math.floor(s / 60)).padStart(2, '0')}:{String(s % 60).padStart(2, '0')}
    </span>
  )
}

/* The theme row: OpenBot's binary dark/light, our storage key. */
function ThemeItem() {
  const { t } = useTranslation()
  const [dark, setDark] = useState(() =>
    document.documentElement.classList.contains('dark'),
  )
  const flip = () => {
    const next = !dark
    setDark(next)
    try {
      localStorage.setItem('sentineldesk-theme', next ? 'dark' : 'light')
    } catch {
      /* a browser with storage off still switches, it just forgets */
    }
    document.documentElement.classList.toggle('dark', next)
    document.documentElement.style.colorScheme = next ? 'dark' : 'light'
  }
  return (
    <button
      type="button"
      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-[13px] hover:bg-foreground/5"
      onClick={flip}
    >
      {dark ? (
        <IconMoon className="size-4 text-muted-foreground" />
      ) : (
        <IconSun className="size-4 text-muted-foreground" />
      )}
      {t('ws.theme')}
      <span className="ml-auto text-[11px] text-muted-foreground">
        {dark ? t('ws.themeDark') : t('ws.themeLight')}
      </span>
    </button>
  )
}
