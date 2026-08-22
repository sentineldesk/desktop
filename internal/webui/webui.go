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

// Package webui serves the browser client.
//
// The assets are embedded in the binary rather than read from disk: the server
// ships as a single file, and there is no way for the container to end up
// serving a client that disagrees with the protocol the server speaks.
//
// Since 2026-08-20 the assets are BUILT, not hand-written: desktop/app is the
// React client (the panel's transport wearing the original rail), and vite
// writes its output here — `make app` on a host, the image's node stage in
// Docker. A bare `go build` with an empty directory still compiles (the
// keep-file below sees to it) and serves nothing but 404s, which is a build
// that forgot its client saying so rather than shipping last month's.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:assets
var embedded embed.FS

// FS returns the client's files rooted at the web directory, so that "/" maps
// to index.html.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		// Impossible unless the embed directive above is wrong, which would
		// have failed the build.
		panic(err)
	}
	return sub
}

// Handler serves the embedded client with validation caching.
//
// This exists because of a specific, repeatedly confusing failure. Embedded
// files carry no modification time, so http.FileServer sends neither
// Last-Modified nor ETag. With nothing to validate against, browsers fall back
// to heuristic freshness and keep serving a copy of their own choosing — which
// meant a rebuilt image kept showing the OLD interface until somebody thought
// to hard-reload. The bug looked like "the change did not apply".
//
// An ETag over the file's own bytes fixes it at the source: the browser may
// cache as much as it likes, but it has to ask, and the answer changes the
// moment the content does.
func Handler() http.Handler {
	root := FS()
	files := http.FileServer(http.FS(root))
	tags := map[string]string{}

	// Hashed once at startup: the contents are baked into the binary and cannot
	// change while it runs.
	_ = fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		tags["/"+path] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path
		if name == "/" || strings.HasSuffix(name, "/") {
			name += "index.html"
		}
		if tag, ok := tags[name]; ok {
			w.Header().Set("ETag", tag)
			// no-cache means "reuse it, but check first" — not "do not store".
			w.Header().Set("Cache-Control", "no-cache")
			if match := r.Header.Get("If-None-Match"); match == tag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}
