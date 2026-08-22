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

package stream

// The file tree's confinement, and nothing else.
//
// This file used to be the browser file manager's whole HTTP surface: listing,
// operations, one-use download tickets, and before that a multipart upload.
// That surface is gone — files move over the dedicated DataChannel
// (transfers.go), which knows WHO is asking because the channel belongs to one
// session, and the web door closed behind them on 2026-08-20: an endpoint
// nobody calls is attack surface nobody audits, and the golden rule of the
// clients — nothing but the page itself over the web — deserves to be
// structural on the server rather than a habit of the clients.
//
// What remains is the part every mover of files still stands on: the ROOT and
// the discipline of staying inside it. FILES_ROOT bounds what any file verb
// may reach, and resolve() is the one gate through it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// FileServer confines the desktop's file tree beneath a fixed root.
type FileServer struct {
	root string
}

func NewFileServer(root string) *FileServer {
	abs, err := filepath.Abs(root)
	if err != nil || abs == "" {
		abs = "/home/sentineldesk"
	}
	return &FileServer{root: filepath.Clean(abs)}
}

// resolve turns a client path into a real one, confined to the root.
//
// Symlinks are resolved BEFORE the comparison: without that, a link inside the
// home pointing at / would be a back door straight out of the confinement.
func (f *FileServer) resolve(p string) (string, error) {
	if p == "" {
		p = "/"
	}
	// The client path is always relative to the root, even when it starts "/".
	joined := filepath.Join(f.root, filepath.Clean("/"+strings.TrimPrefix(p, f.root)))
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// It does not exist yet — an upload, a mkdir. Validate the parent instead.
		parent, err2 := filepath.EvalSymlinks(filepath.Dir(joined))
		if err2 != nil {
			return "", fmt.Errorf("no such path: %s", p)
		}
		if !withinRoot(parent, f.root) {
			return "", fmt.Errorf("outside the permitted root")
		}
		return joined, nil
	}
	if !withinRoot(real, f.root) {
		return "", fmt.Errorf("outside the permitted root")
	}
	return real, nil
}

func withinRoot(p, root string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// rel returns the path as the client sees it: relative to the root, "/"-rooted.
func (f *FileServer) rel(abs string) string {
	r, err := filepath.Rel(f.root, abs)
	if err != nil || r == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(r)
}

// Shared by the HTTP surfaces that remain (the log viewer's endpoints).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf(format, args...)})
}

type fileEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // dir | file | link
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Mode     string `json:"mode"`
}
