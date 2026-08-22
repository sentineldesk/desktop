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

/* Drag a file anywhere over the room, drop it, and it lands on the room's
 * desktop — the embedded client's dropzone, regrown for the panel, where it
 * had quietly not survived the move to workrooms.
 *
 * The transfer rides the dedicated `files` DataChannel, chunked — see
 * internal/stream/transfers.go for the protocol and Desktop.uploadFile for
 * the client half. Any size (chunks stream to a temp file server-side),
 * progress in server-confirmed bytes, one transfer per file so two people
 * upload at once without either waiting — the admin dropping while a
 * collaborator's copy is mid-flight is just two transfers.
 *
 * Who may drop is enforced in BOTH places: the caller's gate arms the veil
 * (whoever holds the controls; the administrator always — provisioning the
 * room is the owner's act), and the runtime refuses an up_init from anyone
 * else, because the channel knows who is sending — the thing the old HTTP
 * door never did. */

import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { DeskDelivery } from './useDesktopStream'
import styles from './useDesktopDrop.module.css'

export interface DropTransfer {
  readonly id: string
  readonly name: string
  readonly size: number
  /** 0..1 while uploading. */
  readonly pct: number
  readonly status: 'uploading' | 'done' | 'failed'
}

let dropSeq = 0

function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let v = bytes
  let u = -1
  do {
    v /= 1024
    u++
  } while (v >= 1024 && u < units.length - 1)
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[u]}`
}

export function useDesktopDrop(
  upload: (file: File, onProgress: (bytes: number) => void) => Promise<void>,
): { armed: boolean; drops: readonly DropTransfer[] } {
  const [armed, setArmed] = useState(false)
  const [drops, setDrops] = useState<readonly DropTransfer[]>([])

  /* Nested dragenter/dragleave need a counter, not a boolean — dragging
   * across child elements fires a leave for every enter. */
  const depth = useRef(0)
  const uploadRef = useRef(upload)
  uploadRef.current = upload

  const patch = useCallback((id: string, changes: Partial<DropTransfer>) => {
    setDrops((prev) => prev.map((d) => (d.id === id ? { ...d, ...changes } : d)))
  }, [])

  const uploadOne = useCallback(
    (file: File) => {
      const id = `d${++dropSeq}`
      setDrops((prev) => [
        ...prev,
        { id, name: file.name, size: file.size, pct: 0, status: 'uploading' },
      ])
      const finish = (ok: boolean) => {
        patch(id, { pct: 1, status: ok ? 'done' : 'failed' })
        /* A finished bar says what it had to say; a failed one stays until
         * the next drop replaces the tray, because an error that vanishes
         * on its own was never read. */
        if (ok) {
          window.setTimeout(() => {
            setDrops((prev) => prev.filter((d) => d.id !== id))
          }, 4000)
        }
      }
      uploadRef.current(file, (bytes) => {
        patch(id, { pct: file.size > 0 ? bytes / file.size : 1 })
      }).then(
        () => finish(true),
        () => finish(false),
      )
    },
    [patch],
  )

  useEffect(() => {
    const hasFiles = (e: DragEvent) =>
      !!e.dataTransfer && Array.from(e.dataTransfer.types).includes('Files')

    const onEnter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth.current++
      setArmed(true)
    }
    const onOver = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
    }
    const onLeave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth.current = Math.max(0, depth.current - 1)
      if (depth.current === 0) setArmed(false)
    }
    const onDrop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth.current = 0
      setArmed(false)
      const files = Array.from(e.dataTransfer?.files ?? [])
      /* The destination is the server's decision: an empty dir on up_init
       * means "the Desktop folder if the home has one, the home otherwise"
       * — the server has the filesystem, and probing it over HTTP from
       * every client was the old way of asking. */
      for (const file of files) uploadOne(file)
    }

    window.addEventListener('dragenter', onEnter)
    window.addEventListener('dragover', onOver)
    window.addEventListener('dragleave', onLeave)
    window.addEventListener('drop', onDrop)
    return () => {
      window.removeEventListener('dragenter', onEnter)
      window.removeEventListener('dragover', onOver)
      window.removeEventListener('dragleave', onLeave)
      window.removeEventListener('drop', onDrop)
    }
  }, [uploadOne])

  return { armed, drops }
}

/* The two visible halves: the veil while a drag hovers, and the transfer
 * tray while anything is moving — uploads dropped onto the desktop, and
 * deliveries the runtime pushed the other way. Rendered by both room views.
 * A `ready` delivery is the one item waiting on a person: its Save button
 * carries the click whose activation lets a big file stream to disk. */
export function DropLayer({
  armed,
  drops,
  deliveries = [],
  onSave,
  onDismiss,
}: {
  armed: boolean
  drops: readonly DropTransfer[]
  deliveries?: readonly DeskDelivery[]
  onSave?: (id: string) => void
  onDismiss?: (id: string) => void
}) {
  const { t } = useTranslation()
  return (
    <>
      {armed ? (
        <div className={styles.veil} aria-hidden="true">
          <span className={styles.veilTag}>{t('drop.hint')}</span>
        </div>
      ) : null}
      {drops.length > 0 || deliveries.length > 0 ? (
        <div className={styles.tray}>
          {drops.map((d) => (
            <div key={d.id} className={styles.item}>
              <span className={styles.itemName}>{d.name}</span>
              <span className={styles.itemSize}>
                {d.status === 'failed' ? t('drop.failed') : humanSize(d.size)}
              </span>
              <span className={styles.barTrack} aria-hidden="true">
                <span
                  className={`${styles.bar} ${
                    d.status === 'failed' ? styles.barBad : ''
                  }`}
                  style={{ width: `${Math.round(d.pct * 100)}%` }}
                />
              </span>
            </div>
          ))}
          {deliveries.map((d) => (
            <div key={d.id} className={styles.item}>
              <span className={styles.itemName}>{d.name}</span>
              <span className={styles.itemSize}>
                {d.status === 'failed' ? t('dl.failed') : humanSize(d.size)}
              </span>
              {d.status === 'ready' || d.status === 'failed' ? (
                <span className={styles.itemActions}>
                  <button
                    type="button"
                    className={styles.itemSave}
                    onClick={() => onSave?.(d.id)}
                  >
                    {d.status === 'failed' ? t('dl.retry') : t('dl.save')}
                  </button>
                  <button
                    type="button"
                    className={styles.itemDismiss}
                    aria-label={t('dl.dismiss')}
                    onClick={() => onDismiss?.(d.id)}
                  >
                    ×
                  </button>
                </span>
              ) : (
                <span className={styles.barTrack} aria-hidden="true">
                  <span
                    className={styles.bar}
                    style={{
                      width: `${
                        d.status === 'done'
                          ? 100
                          : d.size > 0
                            ? Math.round((d.bytes / d.size) * 100)
                            : 100
                      }%`,
                    }}
                  />
                </span>
              )}
            </div>
          ))}
        </div>
      ) : null}
    </>
  )
}
