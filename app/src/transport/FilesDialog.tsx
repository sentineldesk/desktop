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

/* The file manager, the way a two-pane manager has always looked.
 *
 * Transcribed from the embedded client's Midnight-Commander homage: the
 * remote desktop on the left in the drive accent, your machine on the right
 * in amber, columns, transfer arrows in the middle, and the function-key row
 * a two-pane manager has always ended with. A first rewrite flattened it
 * into a generic list dialog and the flattening was rightly rejected — the
 * two-pane shape is not nostalgia, it is the answer to "which side is this
 * file on", which is the only question a transfer screen has.
 *
 * Every operation rides the desktop's `files` DataChannel — listing, mkdir,
 * rename, delete, uploads, downloads. There is no HTTP in this file: the
 * transfer plane is the WebRTC session itself, chunked, with the runtime
 * enforcing who may write (the controls, or the administrator's ticket).
 * The right pane shows as much of your machine as the browser honestly can:
 * with the File System Access API, a folder the person PICKED — browsable,
 * uploadable-from, downloaded-into; without it, or before picking, this
 * session's transfers and their progress. */

import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { Desktop, FsEntry } from './useDesktopStream'
import styles from './FilesDialog.module.css'
import { useDraggable } from '../ui/useDraggable'

interface Transfer {
  readonly id: string
  readonly name: string
  readonly size: number
  readonly pct: number
  readonly dir: 'up' | 'down'
  readonly status: 'busy' | 'done' | 'failed'
}

let transferSeq = 0

function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = bytes
  let u = -1
  do {
    v /= 1024
    u++
  } while (v >= 1024 && u < units.length - 1)
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[u]}`
}

function join(dir: string, name: string): string {
  return `${dir}/${name}`.replace('//', '/')
}

/* One entry of the LOCAL pane, when a real folder has been picked. */
interface LocalEntry {
  readonly name: string
  readonly type: 'dir' | 'file'
  readonly size: number
  readonly modified: number
  readonly handle: FileSystemHandle
}

/* The picker is WICG, not lib.dom — typed here, feature-detected below. */
type DirPicker = (opts?: {
  mode?: 'read' | 'readwrite'
}) => Promise<FileSystemDirectoryHandle>

export function FilesDialog({ desktop, onClose }: { desktop: Desktop; onClose(): void }) {
  const drag = useDraggable('sentineldesk.filesPos')
  const { t } = useTranslation()

  /* Through a ref, and this is a BUG FIX, not tidiness: `desktop` is a new
   * object on every provider render — a pointer move is enough — so a
   * callback depending on it is reborn constantly, and the mount effect
   * that depended on that callback kept re-running load('/'): enter a
   * folder, and two seconds later the pane snapped back to the root. */
  const desktopRef = useRef(desktop)
  desktopRef.current = desktop

  const [path, setPath] = useState('/')
  const [parent, setParent] = useState('')
  const [entries, setEntries] = useState<readonly FsEntry[]>([])
  const [truncated, setTruncated] = useState(0)
  const [sel, setSel] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [log, setLog] = useState<readonly Transfer[]>([])
  const fileInput = useRef<HTMLInputElement>(null)
  const [dropArmed, setDropArmed] = useState(false)

  /* The manager's own query box, replacing window.prompt/confirm — the one
   * native dialog left around here is Chrome's folder-permission prompt,
   * which is the browser's security surface and deliberately not ours to
   * imitate. This one is: an MC-style box inside the window, promise-based
   * so the callers read like the prompts they replace. */
  const [ask, setAsk] = useState<{
    title: string
    input: boolean
    danger: boolean
    resolve: (v: string | null) => void
  } | null>(null)
  const [askValue, setAskValue] = useState('')
  const askRef = useRef(ask)
  askRef.current = ask

  const askInput = useCallback(
    (title: string, initial = '') =>
      new Promise<string | null>((resolve) => {
        setAskValue(initial)
        setAsk({ title, input: true, danger: false, resolve })
      }),
    [],
  )

  const askConfirm = useCallback(
    (title: string) =>
      new Promise<boolean>((resolve) => {
        setAsk({
          title,
          input: false,
          danger: true,
          resolve: (v) => resolve(v !== null),
        })
      }),
    [],
  )

  const settleAsk = useCallback((v: string | null) => {
    askRef.current?.resolve(v)
    setAsk(null)
  }, [])

  /* '..' first when there is a parent — row index 0 is it. */
  const rows: readonly (FsEntry | null)[] = parent
    ? [null, ...entries]
    : [...entries]

  const load = useCallback(async (dir: string) => {
    setError(null)
    try {
      const listing = await desktopRef.current.filesList(dir)
      setPath(listing.path)
      setParent(listing.parent)
      setEntries(listing.entries)
      setTruncated(listing.truncated)
      setSel(0)
    } catch (err) {
      setError((err as Error).message)
    }
  }, [])

  useEffect(() => {
    void load('/')
  }, [load])

  const patchLog = useCallback((id: string, changes: Partial<Transfer>) => {
    setLog((prev) => prev.map((x) => (x.id === id ? { ...x, ...changes } : x)))
  }, [])

  /* The local pane's real half: a directory the person PICKED, as a stack of
   * handles so '..' walks back up without re-asking permission. Empty stack
   * means no folder chosen, and the pane shows the transfer log instead —
   * the pre-picker behaviour, and the only behaviour on browsers without
   * the File System Access API. */
  const [localStack, setLocalStack] = useState<
    readonly FileSystemDirectoryHandle[]
  >([])
  const [localEntries, setLocalEntries] = useState<readonly LocalEntry[]>([])
  const [localSel, setLocalSel] = useState(0)
  const localStackRef = useRef(localStack)
  localStackRef.current = localStack
  const dirPicker = (
    window as unknown as { showDirectoryPicker?: DirPicker }
  ).showDirectoryPicker

  const listLocal = useCallback(async (dir: FileSystemDirectoryHandle) => {
    const out: LocalEntry[] = []
    const iter = (
      dir as unknown as { values(): AsyncIterable<FileSystemHandle> }
    ).values()
    for await (const h of iter) {
      if (h.kind === 'file') {
        const f = await (h as FileSystemFileHandle).getFile()
        out.push({
          name: h.name,
          type: 'file',
          size: f.size,
          modified: f.lastModified,
          handle: h,
        })
      } else {
        out.push({ name: h.name, type: 'dir', size: 0, modified: 0, handle: h })
      }
      /* Same cap as the remote listing, for the same honesty. */
      if (out.length >= 400) break
    }
    out.sort((a, b) =>
      a.type !== b.type
        ? a.type === 'dir'
          ? -1
          : 1
        : a.name.localeCompare(b.name),
    )
    setLocalEntries(out)
    setLocalSel(0)
  }, [])

  const pickLocal = useCallback(async () => {
    if (!dirPicker) return
    try {
      const dir = await dirPicker({ mode: 'readwrite' })
      setLocalStack([dir])
      await listLocal(dir)
    } catch {
      /* The person closed the picker — not an error, just not now. */
    }
  }, [dirPicker, listLocal])

  const localRefresh = useCallback(() => {
    const top = localStackRef.current[localStackRef.current.length - 1]
    if (top) void listLocal(top)
  }, [listLocal])

  const download = useCallback(
    (entry: FsEntry) => {
      const id = `t${++transferSeq}`
      setLog((prev) => [
        { id, name: entry.name, size: entry.size, pct: 0, dir: 'down', status: 'busy' },
        ...prev,
      ])
      /* Where the copy lands is decided NOW, not at completion — a folder
       * picked mid-download must not swallow a transfer aimed at the
       * browser's download manager. */
      const dest =
        localStackRef.current[localStackRef.current.length - 1] ?? null
      desktopRef.current
        .downloadFile(join(path, entry.name), (bytes, total) => {
          patchLog(id, { pct: total > 0 ? bytes / total : 1 })
        })
        .then(async ({ name, blob }) => {
          if (dest) {
            /* Straight into the picked folder, and the listing refreshes so
             * the new file APPEARING is the completion signal. */
            const fh = await dest.getFileHandle(name, { create: true })
            const w = await fh.createWritable()
            await w.write(blob)
            await w.close()
            patchLog(id, { pct: 1, status: 'done' })
            localRefresh()
            return
          }
          patchLog(id, { pct: 1, status: 'done' })
          const url = URL.createObjectURL(blob)
          const a = document.createElement('a')
          a.href = url
          a.download = name
          document.body.appendChild(a)
          a.click()
          a.remove()
          window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
        })
        .catch((err: Error) => {
          patchLog(id, { status: 'failed' })
          setError(err.message)
        })
    },
    [path, patchLog, localRefresh],
  )

  const upload = useCallback(
    (files: FileList | File[] | null) => {
      if (!files) return
      for (const file of Array.from(files)) {
        const id = `t${++transferSeq}`
        setLog((prev) => [
          { id, name: file.name, size: file.size, pct: 0, dir: 'up', status: 'busy' },
          ...prev,
        ])
        desktopRef.current
          .uploadFile(
            file,
            (bytes) => patchLog(id, { pct: file.size > 0 ? bytes / file.size : 1 }),
            path,
          )
          .then(
            () => {
              patchLog(id, { pct: 1, status: 'done' })
              void load(path)
            },
            (err: Error) => {
              patchLog(id, { status: 'failed' })
              setError(err.message)
            },
          )
      }
    },
    [path, patchLog, load],
  )

  const open = useCallback(
    (row: FsEntry | null) => {
      if (row === null) {
        void load(parent)
        return
      }
      if (row.type === 'dir') {
        void load(join(path, row.name))
        return
      }
      download(row)
    },
    [load, parent, path, download],
  )

  /* The local pane's rows: '..' while below the picked root. Double-click a
   * folder to descend, a file to send it to the desktop. */
  const localRows: readonly (LocalEntry | null)[] =
    localStack.length > 1 ? [null, ...localEntries] : [...localEntries]

  const openLocal = useCallback(
    (row: LocalEntry | null) => {
      if (row === null) {
        const next = localStackRef.current.slice(0, -1)
        setLocalStack(next)
        const top = next[next.length - 1]
        if (top) void listLocal(top)
        return
      }
      if (row.type === 'dir') {
        const dir = row.handle as FileSystemDirectoryHandle
        setLocalStack((prev) => [...prev, dir])
        void listLocal(dir)
        return
      }
      void (row.handle as FileSystemFileHandle)
        .getFile()
        .then((f) => upload([f]))
    },
    [listLocal, upload],
  )

  const localSelected: LocalEntry | null = localRows[localSel] ?? null

  const selected: FsEntry | null = rows[sel] ?? null

  const doMkdir = useCallback(() => {
    void askInput(t('fm.mkdirPrompt')).then((name) => {
      if (!name) return
      desktopRef.current
        .filesOp('mkdir', join(path, name))
        .then(() => void load(path))
        .catch((err: Error) => setError(err.message))
    })
  }, [askInput, path, load, t])

  const doRename = useCallback(() => {
    if (!selected) return
    void askInput(t('fm.renamePrompt'), selected.name).then((name) => {
      if (!name || name === selected.name) return
      desktopRef.current
        .filesOp('rename', join(path, selected.name), join(path, name))
        .then(() => void load(path))
        .catch((err: Error) => setError(err.message))
    })
  }, [askInput, selected, path, load, t])

  const doDelete = useCallback(() => {
    if (!selected) return
    void askConfirm(t('fm.confirmDelete', { name: selected.name })).then(
      (yes) => {
        if (!yes) return
        desktopRef.current
          .filesOp('delete', join(path, selected.name))
          .then(() => void load(path))
          .catch((err: Error) => setError(err.message))
      },
    )
  }, [askConfirm, selected, path, load, t])

  const doCopy = useCallback(() => {
    if (selected && selected.type !== 'dir') download(selected)
  }, [selected, download])

  /* The function keys, live while the dialog is open — the keycaps below
   * are not decoration, they are the manual. */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      /* While the query box is open, the keyboard belongs to it — its input
       * needs the arrows, and an F8 fired into a half-typed rename would be
       * the F-keys acting behind the person's back. */
      if (askRef.current) return
      switch (e.key) {
        /* Escape deliberately does nothing: a transfer log and a working
         * directory are state worth keeping, and the ✕ is the only way to
         * spend them. (MC's own Esc-quits was tried and retired here.) */
        case 'F2':
          void load(path)
          break
        case 'F5':
          doCopy()
          break
        case 'F6':
          doRename()
          break
        case 'F7':
          doMkdir()
          break
        case 'F8':
          doDelete()
          break
        case 'ArrowUp':
          setSel((s) => Math.max(0, s - 1))
          break
        case 'ArrowDown':
          setSel((s) => Math.min(rows.length - 1, s + 1))
          break
        case 'Enter':
          open(rows[sel] ?? null)
          break
        default:
          return
      }
      e.preventDefault()
      e.stopPropagation()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [load, path, doCopy, doRename, doMkdir, doDelete, open, rows, sel])

  const active = log.filter((x) => x.status === 'busy')
  const progress =
    active.length > 0
      ? active.reduce((a, x) => a + x.pct, 0) / active.length
      : 0

  const dirs = entries.filter((e) => e.type === 'dir').length
  const bytes = entries.reduce((a, e) => a + (e.type === 'dir' ? 0 : e.size), 0)

  /* Closes ONLY by its ✕ — a click that misses the pane, or an Escape meant
   * for something else, must not cost an open transfer log and a working
   * directory. The overlay carries no dim and no blur (see the css): the
   * desktop stays fully visible behind the manager. */
  return (
    <div className={styles.overlay}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t('fm.title')}
        className={styles.window}
        ref={drag.ref}
        style={drag.style}
      >
        <div
          className={styles.title}
          onPointerDown={drag.onGrab}
          onDoubleClick={drag.onHome}
        >
          <span className={styles.tt}>{t('fm.title')}</span>
          <span className={styles.sub}>{t('fm.subtitle')}</span>
          <button
            type="button"
            className={styles.close}
            aria-label={t('a11y.close')}
            onClick={onClose}
          >
            ✕
          </button>
        </div>

        <div className={styles.panes}>
          {/* Left: the remote desktop, in the drive accent. */}
          <section className={`${styles.pane} ${styles.paneRemote}`}>
            <header className={styles.paneHead}>
              <span className={`${styles.side} ${styles.sideRemote}`}>
                {t('fm.remote')}
              </span>
              <span className={styles.cwd}>{path}</span>
            </header>
            <div className={styles.colhead}>
              <span>{t('fm.colName')}</span>
              <span>{t('fm.colSize')}</span>
              <span>{t('fm.colModified')}</span>
            </div>
            <ul className={styles.list}>
              {rows.map((row, i) => (
                <li
                  key={row === null ? '..' : row.name}
                  className={`${row === null ? styles.up : ''} ${
                    row !== null && row.type === 'dir' ? styles.dir : ''
                  } ${i === sel ? styles.selRow : ''}`}
                  onClick={() => setSel(i)}
                  onDoubleClick={() => open(row)}
                >
                  <span className={styles.nm}>
                    {row === null ? '/..' : row.type === 'dir' ? `/${row.name}` : row.name}
                  </span>
                  <span>{row === null || row.type === 'dir' ? '' : humanSize(row.size)}</span>
                  <span>
                    {row === null ? '' : new Date(row.modified).toLocaleString()}
                  </span>
                </li>
              ))}
              {error ? <li className={styles.err}>{error}</li> : null}
            </ul>
            <footer className={styles.foot}>
              {truncated > 0
                ? t('fm.truncated', { n: truncated })
                : t('fm.summary', {
                    items: entries.length,
                    dirs,
                    size: humanSize(bytes),
                  })}
            </footer>
          </section>

          {/* Middle: which way the transfer goes. */}
          <div className={styles.mid}>
            <button
              type="button"
              title={t('fm.download')}
              disabled={!selected || selected.type === 'dir'}
              onClick={doCopy}
            >
              ▶
            </button>
            <button
              type="button"
              title={t('fm.upload')}
              onClick={() => {
                /* A file selected in the picked local folder goes straight
                 * up; with no folder (or a dir selected) the classic file
                 * chooser still answers. */
                if (localSelected && localSelected.type === 'file') {
                  openLocal(localSelected)
                } else {
                  fileInput.current?.click()
                }
              }}
            >
              ◀
            </button>
          </div>

          {/* Right: your machine, in amber. With the File System Access API
              this is a REAL pane — pick a folder, browse it, send from it,
              and downloads land in it. Without the API (or before picking),
              it shows this session's transfers, which is all a browser can
              honestly show of your disk. */}
          <section
            className={`${styles.pane} ${styles.paneLocal} ${
              dropArmed ? styles.paneDrop : ''
            }`}
            onDragEnter={(e) => {
              e.preventDefault()
              e.stopPropagation()
              setDropArmed(true)
            }}
            onDragOver={(e) => {
              e.preventDefault()
              e.stopPropagation()
            }}
            onDragLeave={(e) => {
              e.preventDefault()
              e.stopPropagation()
              setDropArmed(false)
            }}
            onDrop={(e) => {
              e.preventDefault()
              e.stopPropagation()
              setDropArmed(false)
              upload(e.dataTransfer.files)
            }}
          >
            <header className={styles.paneHead}>
              <span className={`${styles.side} ${styles.sideLocal}`}>
                {t('fm.local')}
              </span>
              <span className={styles.cwd}>
                {localStack.length > 0
                  ? localStack.map((d) => d.name).join('/')
                  : t('fm.downloadsFolder')}
              </span>
              {dirPicker ? (
                <button
                  type="button"
                  className={styles.pick}
                  onClick={() => void pickLocal()}
                >
                  {t('fm.pickFolder')}
                </button>
              ) : null}
            </header>
            <div className={styles.colhead}>
              <span>{t('fm.colName')}</span>
              <span>{t('fm.colSize')}</span>
              <span>
                {localStack.length > 0 ? t('fm.colModified') : t('fm.colStatus')}
              </span>
            </div>
            {localStack.length > 0 ? (
              <ul className={styles.list}>
                {localRows.map((row, i) => (
                  <li
                    key={row === null ? '..' : row.name}
                    className={`${row === null ? styles.up : ''} ${
                      row !== null && row.type === 'dir' ? styles.dir : ''
                    } ${i === localSel ? styles.selLocal : ''}`}
                    onClick={() => setLocalSel(i)}
                    onDoubleClick={() => openLocal(row)}
                  >
                    <span className={styles.nm}>
                      {row === null
                        ? '/..'
                        : row.type === 'dir'
                          ? `/${row.name}`
                          : row.name}
                    </span>
                    <span>
                      {row === null || row.type === 'dir'
                        ? ''
                        : humanSize(row.size)}
                    </span>
                    <span>
                      {row === null || row.modified === 0
                        ? ''
                        : new Date(row.modified).toLocaleString()}
                    </span>
                  </li>
                ))}
              </ul>
            ) : (
              <ul className={styles.list}>
                {log.map((x) => (
                  <li key={x.id} className={styles.plain}>
                    <span className={styles.nm}>{x.name}</span>
                    <span>{humanSize(x.size)}</span>
                    <span>
                      {x.status === 'busy'
                        ? `${Math.round(x.pct * 100)}%`
                        : x.status === 'failed'
                          ? t('drop.failed')
                          : x.dir === 'up'
                            ? t('fm.uploaded')
                            : t('fm.saved')}
                    </span>
                  </li>
                ))}
                {log.length === 0 ? (
                  <li className={styles.plainDim}>{t('fm.noTransfers')}</li>
                ) : null}
              </ul>
            )}
            <div className={styles.localHint}>
              <b>{t('fm.dropHereStrong')}</b> {t('fm.dropHereRest')}
            </div>
          </section>
        </div>

        <div className={styles.progress} aria-hidden="true">
          <div style={{ width: `${Math.round(progress * 100)}%` }} />
        </div>

        <div className={styles.keys}>
          <button type="button" onClick={doCopy}>
            <b>F5</b> {t('fm.keyCopy')}
          </button>
          <button type="button" onClick={doRename}>
            <b>F6</b> {t('fm.keyRename')}
          </button>
          <button type="button" onClick={doMkdir}>
            <b>F7</b> {t('fm.keyMkdir')}
          </button>
          <button type="button" onClick={doDelete}>
            <b>F8</b> {t('fm.keyDelete')}
          </button>
          <button type="button" onClick={() => void load(path)}>
            <b>F2</b> {t('fm.keyRefresh')}
          </button>
        </div>

        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          onChange={(e) => {
            upload(e.target.files)
            e.target.value = ''
          }}
        />

        {ask ? (
          <div className={styles.ask}>
            <form
              className={`${styles.askBox} ${ask.danger ? styles.askDanger : ''}`}
              onSubmit={(e) => {
                e.preventDefault()
                settleAsk(ask.input ? askValue : 'yes')
              }}
            >
              <div className={styles.askTitle}>{ask.title}</div>
              {ask.input ? (
                <input
                  autoFocus
                  className={styles.askInput}
                  aria-label={ask.title}
                  value={askValue}
                  onChange={(e) => setAskValue(e.target.value)}
                />
              ) : null}
              <div className={styles.askRow}>
                <button
                  type="submit"
                  autoFocus={!ask.input}
                  className={styles.askOk}
                >
                  {t('fm.ok')}
                </button>
                <button
                  type="button"
                  className={styles.askCancel}
                  onClick={() => settleAsk(null)}
                >
                  {t('fm.cancel')}
                </button>
              </div>
            </form>
          </div>
        ) : null}
      </div>
    </div>
  )
}
