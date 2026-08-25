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

/* The About card — the "C · Split" mock the owner picked: the brand mark
 * large on a drive-tinted panel, the facts in a column beside it. Rendered
 * at the shell so both modes open the same card from their own menus. */

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { IconCheck, IconCopy } from '@tabler/icons-react'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogClose, DialogTitle } from '@/components/ui/dialog'

const REPO_URL = 'https://github.com/sentineldesk'
const AUTHOR_MAIL = 'fpereira@cnsoluciones.com'

/* The address, one press away: the mailto opens a composer for whoever has
 * one; the copy serves everybody else. */
function CopyMail() {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className="shrink-0 text-muted-foreground hover:text-foreground"
      title={AUTHOR_MAIL}
      onClick={() => {
        void navigator.clipboard.writeText(AUTHOR_MAIL).then(() => {
          setDone(true)
          setTimeout(() => setDone(false), 1600)
        })
      }}
    >
      {done ? (
        <IconCheck className="size-3.5 text-[var(--sd-drive)]" />
      ) : (
        <IconCopy className="size-3.5" />
      )}
    </button>
  )
}

export function AboutDialog(props: {
  open: boolean
  onOpenChange(v: boolean): void
  /** The build the server said it is; '' before the config arrives. */
  version: string
}) {
  const { t } = useTranslation()
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="w-[520px] max-w-[92vw] overflow-hidden p-0 sm:max-w-[520px]">
        <div className="flex min-h-[240px]">
          <div className="flex w-[176px] shrink-0 flex-col items-center justify-center gap-3 border-r bg-[linear-gradient(160deg,color-mix(in_srgb,var(--sd-drive)_14%,transparent),color-mix(in_srgb,var(--sd-drive)_3%,transparent))] py-7">
            <svg viewBox="0 0 64 64" className="size-[72px]" aria-hidden="true">
              <rect x="4" y="8" width="56" height="38" rx="6" fill="var(--sd-drive)" />
              <rect x="10" y="14" width="44" height="26" rx="2" fill="var(--background)" />
              <rect x="22" y="50" width="20" height="4" rx="2" fill="var(--sd-drive)" />
              <rect x="16" y="54" width="32" height="4" rx="2" fill="#2aa96c" />
            </svg>
            {props.version ? (
              <span className="rounded-full border border-[color-mix(in_srgb,var(--sd-drive)_40%,transparent)] px-2.5 py-0.5 text-[11px] font-semibold text-[var(--sd-drive)]">
                {props.version.startsWith('v') ? props.version : `v${props.version}`}
              </span>
            ) : null}
          </div>

          <div className="flex min-w-0 flex-1 flex-col gap-2.5 p-6 pb-5">
            <DialogTitle className="text-[17px] leading-none font-bold tracking-tight">
              SentinelDesk
            </DialogTitle>
            <p className="text-[12.5px] leading-relaxed text-pretty text-muted-foreground">
              {t('about.blurb')}
            </p>
            <div className="mt-0.5 flex flex-col gap-1.5 text-xs">
              <div className="flex gap-2">
                <span className="w-[52px] shrink-0 text-muted-foreground">{t('about.source')}</span>
                <a
                  href={REPO_URL}
                  target="_blank"
                  rel="noreferrer"
                  className="truncate text-[var(--sd-drive)] hover:underline"
                >
                  github.com/sentineldesk
                </a>
              </div>
              <div className="flex gap-2">
                <span className="w-[52px] shrink-0 text-muted-foreground">{t('about.author')}</span>
                <span className="flex min-w-0 flex-col gap-0.5">
                  <span>Federico Pereira</span>
                  <span className="flex items-center gap-1.5">
                    <a
                      href={`mailto:${AUTHOR_MAIL}`}
                      className="truncate text-[var(--sd-drive)] hover:underline"
                    >
                      {AUTHOR_MAIL}
                    </a>
                    <CopyMail />
                  </span>
                </span>
              </div>
              <div className="flex gap-2">
                <span className="w-[52px] shrink-0 text-muted-foreground">{t('about.license')}</span>
                <span>Apache-2.0</span>
              </div>
            </div>
            <div className="mt-auto flex justify-end pt-2.5">
              <DialogClose render={<Button size="sm" />}>{t('about.close')}</DialogClose>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
