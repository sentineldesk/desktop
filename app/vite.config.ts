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

import fs from 'node:fs'
import path from 'node:path'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The build lands straight in the Go embed directory: `make app` (and the
// image's node stage) produce internal/webui/assets, and the binary carries
// the result. One artifact, no copy step to forget.
let outDir = ''

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      /* emptyOutDir sweeps the keep-file with every build, and the keep-file
       * is what lets a fresh clone's go:embed compile before npm has ever
       * run. Put it back as the last act of the build instead of trusting
       * every caller to remember (the Makefile did; `npm run build` alone
       * did not).
       *
       * Into the RESOLVED outDir, not a path assumed from the repo layout:
       * the image's node stage builds with `--outDir /out`, where
       * `../internal` does not exist, and a hardcoded path took `make
       * image` down with ENOENT. A stray keep-file in /out is harmless. */
      name: 'restore-gitkeep',
      configResolved(config) {
        outDir = config.build.outDir
      },
      closeBundle() {
        try {
          fs.writeFileSync(path.resolve(outDir, '.gitkeep'), '')
        } catch {
          /* nothing to keep alive where this build landed */
        }
      },
    },
  ],
  resolve: {
    alias: { '@': path.resolve(import.meta.dirname, 'src') },
  },
  build: {
    outDir: '../internal/webui/assets',
    emptyOutDir: true,
  },
  server: {
    // The dev loop proxies the runtime, so `npm run dev` against a live
    // desktop needs only VITE_TARGET=https://host:8080.
    proxy: process.env.VITE_TARGET
      ? {
          '/ws': { target: process.env.VITE_TARGET, ws: true, secure: false },
          '/auth': { target: process.env.VITE_TARGET, secure: false },
        }
      : undefined,
  },
})
