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

// Files arrive over their own DataChannel, in chunks.
//
// Uploads used to be HTTP multipart against /files/upload; that door is gone
// and this channel is the only way a file moves either direction. It exists
// for what the HTTP one could not do:
//
//   - Its own CHANNEL, not the input channel. A DataChannel is ordered, so a
//     gigabyte queued behind a mouse move would hold the mouse hostage;
//     "files" and "input" are separate SCTP streams and neither waits for
//     the other.
//   - The server knows WHO is sending. An HTTP upload authenticates a ticket
//     that every member of the room holds equally, so "the controller or an
//     administrator may put files on the desktop" was a rule the client
//     enforced and the server could not. The channel belongs to one session,
//     which has a member id and a privilege bit — the rule lives here now.
//   - No size ceiling. Chunks stream to a temp file beside the destination
//     and rename over it on completion, so a half-arrived file never wears
//     the name of a whole one — and an interrupted transfer leaves a
//     .upload-* corpse the sweep collects, never a truncated file that
//     looks finished.
//
// The protocol is init → chunk* → done, with abort at any point. Chunks are
// base64 in JSON text frames, sequence-checked even though the channel is
// ordered — a bug in either side's framing should fail loudly at seq 7, not
// corrupt silently at byte 200M. The client does not wait for acks to keep
// sending (the channel orders; bufferedAmount throttles); the acks carry
// server-confirmed progress and are where an error would arrive.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// uploadChunkSize is what the client is told to slice by. Base64 inflates by
// 4/3 and the JSON envelope adds more; 32 KiB raw stays comfortably under the
// 64 KiB DataChannel message ceiling every implementation honours.
const uploadChunkSize = 32 << 10

// uploadStaleAfter is how long a transfer may sit silent before the sweep
// takes its temp file. Ten minutes outlives any stall worth waiting through.
const uploadStaleAfter = 10 * time.Minute

type upload struct {
	id       string
	name     string
	tempPath string
	absPath  string
	size     int64
	got      int64
	nextSeq  int
	last     time.Time
	file     *os.File
}

// filesMsg is every frame either direction on the files channel.
type filesMsg struct {
	T         string `json:"t"`
	Ref       string `json:"ref,omitempty"` // client's handle for init, echoed back
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
	Seq       int    `json:"seq,omitempty"`
	D         string `json:"d,omitempty"` // base64 chunk
	Bytes     int64  `json:"bytes,omitempty"`
	Chunk     int    `json:"chunk,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
	Deliver   string `json:"deliver,omitempty"` // a server-offered delivery id, on dn_init

	// The manager's half: listing, the F-key operations, downloads.
	Op        string      `json:"op,omitempty"`
	To        string      `json:"to,omitempty"`
	Parent    string      `json:"parent,omitempty"`
	Entries   []fileEntry `json:"entries,omitempty"`
	Truncated int         `json:"truncated,omitempty"`
}

var uploadNameOK = regexp.MustCompile(`^[^/\x00]{1,255}$`)

// deliveredFile is a file the SERVER produced and offered to this session — a
// finished recording, a screenshot. The path is the server's own choice, so it
// is exempt from the FILES_ROOT confinement that governs everything the client
// names, for the same reason the old HTTP ticket was: this is the server
// handing over a file it just made, not the browser reaching into the
// filesystem.
type deliveredFile struct {
	path string
	name string
}

// offerDelivery registers a server-produced file this session may pull over
// the files channel, and returns the id the client will quote on dn_init.
//
// Unlike the one-use HTTP ticket this replaces, an offer stays valid until the
// session closes: the ticket guarded an unauthenticated URL that could leak,
// while this id is only speakable on a channel that already belongs to one
// authenticated session — and keeping it alive is what lets a save that failed
// halfway be retried without re-recording anything.
func (s *Session) offerDelivery(absPath, name string) string {
	s.transfersMu.Lock()
	defer s.transfersMu.Unlock()
	if s.deliveries == nil {
		s.deliveries = map[string]deliveredFile{}
	}
	s.deliverSeq++
	id := fmt.Sprintf("dv-%d-%d", time.Now().UnixNano(), s.deliverSeq)
	s.deliveries[id] = deliveredFile{path: absPath, name: name}
	return id
}

// openFilesChannel wires the second DataChannel. Called from start(), beside
// the input channel.
func (s *Session) openFilesChannel(pc *webrtc.PeerConnection) {
	ch, err := pc.CreateDataChannel("files", nil)
	if err != nil {
		s.logf("files channel unavailable: %v", err)
		return
	}
	ch.OnMessage(func(msg webrtc.DataChannelMessage) {
		var m filesMsg
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			return
		}
		s.handleFilesMsg(ch, m)
	})
}

func (s *Session) filesReply(ch *webrtc.DataChannel, m filesMsg) {
	if payload, err := json.Marshal(m); err == nil {
		_ = ch.SendText(string(payload))
	}
}

func (s *Session) handleFilesMsg(ch *webrtc.DataChannel, m filesMsg) {
	switch m.T {
	case "up_init":
		s.handleUploadInit(ch, m)
	case "up_chunk":
		s.handleUploadChunk(ch, m)
	case "up_done":
		s.handleUploadDone(ch, m)
	case "up_abort":
		s.dropUpload(m.ID)
	case "ls":
		s.handleFilesList(ch, m)
	case "op":
		s.handleFilesOp(ch, m)
	case "dn_init":
		s.handleDownloadInit(ch, m)
	case "dn_abort":
		s.abortDownload(m.ID)
	}
}

func (s *Session) handleUploadInit(ch *webrtc.DataChannel, m filesMsg) {
	refuse := func(why string) {
		s.filesReply(ch, filesMsg{T: "up_err", Ref: m.Ref, Name: m.Name, Error: why})
	}

	// Anybody in the room may put a file on the shared desktop. This used to
	// need the controls, and the rule read well until the room used it:
	// handing the driver a file is provisioning, not driving — the USB stick
	// passed across the desk, not a hand on the wheel. It moves no pointer
	// and types nothing, it lands confined under FILES_ROOT, and the witness
	// log records who sent it. Changing or deleting what is already there is
	// a different act and keeps the controls gate (see handleFilesOp).
	if !uploadNameOK.MatchString(m.Name) || m.Name == "." || m.Name == ".." {
		refuse("that is not a usable file name")
		return
	}

	s.sweepUploads()

	fs := s.sessionRoot()
	dir := m.Dir
	if dir == "" {
		// The room's Desktop folder when there is one; the home otherwise.
		// Resolved server-side — the server has the filesystem.
		dir = "/"
		if entries, err := os.ReadDir(fs.root); err == nil {
			for _, e := range entries {
				if e.IsDir() && strings.EqualFold(e.Name(), "Desktop") {
					dir = "/" + e.Name()
					break
				}
			}
		}
	}
	// The one resolver both doors share — see sessionRoot.
	realDir, err := fs.resolve(dir)
	if err != nil {
		refuse(err.Error())
		return
	}
	if st, err := os.Stat(realDir); err != nil || !st.IsDir() {
		refuse("the destination is not a directory")
		return
	}

	final := filepath.Join(realDir, m.Name)
	if !m.Overwrite {
		if _, err := os.Stat(final); err == nil {
			refuse(fmt.Sprintf("%s already exists on the desktop", m.Name))
			return
		}
	}

	id := fmt.Sprintf("up-%d-%d", time.Now().UnixNano(), len(m.Name))
	temp := final + ".upload-" + id
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		refuse(fmt.Sprintf("could not create the file: %v", err))
		return
	}

	s.transfersMu.Lock()
	if s.transfers == nil {
		s.transfers = map[string]*upload{}
	}
	s.transfers[id] = &upload{
		id: id, name: m.Name, tempPath: temp, absPath: final,
		size: m.Size, last: time.Now(), file: f,
	}
	s.transfersMu.Unlock()

	s.filesReply(ch, filesMsg{T: "up_ready", Ref: m.Ref, ID: id, Name: m.Name, Chunk: uploadChunkSize})
}

func (s *Session) handleUploadChunk(ch *webrtc.DataChannel, m filesMsg) {
	s.transfersMu.Lock()
	u := s.transfers[m.ID]
	s.transfersMu.Unlock()
	if u == nil {
		s.filesReply(ch, filesMsg{T: "up_err", ID: m.ID, Error: "no such transfer"})
		return
	}
	fail := func(why string) {
		s.dropUpload(m.ID)
		s.filesReply(ch, filesMsg{T: "up_err", ID: m.ID, Name: u.name, Error: why})
	}
	if m.Seq != u.nextSeq {
		fail(fmt.Sprintf("chunk out of order: expected %d, got %d", u.nextSeq, m.Seq))
		return
	}
	data, err := base64.StdEncoding.DecodeString(m.D)
	if err != nil {
		fail("that chunk is not base64")
		return
	}
	n, err := u.file.Write(data)
	if err != nil {
		fail(fmt.Sprintf("could not write: %v", err))
		return
	}
	u.got += int64(n)
	u.nextSeq++
	u.last = time.Now()
	s.filesReply(ch, filesMsg{T: "up_ack", ID: u.id, Seq: m.Seq, Bytes: u.got})
}

func (s *Session) handleUploadDone(ch *webrtc.DataChannel, m filesMsg) {
	s.transfersMu.Lock()
	u := s.transfers[m.ID]
	delete(s.transfers, m.ID)
	s.transfersMu.Unlock()
	if u == nil {
		s.filesReply(ch, filesMsg{T: "up_err", ID: m.ID, Error: "no such transfer"})
		return
	}
	_ = u.file.Sync()
	_ = u.file.Close()

	if u.size > 0 && u.got != u.size {
		_ = os.Remove(u.tempPath)
		s.filesReply(ch, filesMsg{T: "up_err", ID: u.id, Name: u.name,
			Error: fmt.Sprintf("size mismatch: announced %d bytes, received %d", u.size, u.got)})
		return
	}
	if err := os.Rename(u.tempPath, u.absPath); err != nil {
		_ = os.Remove(u.tempPath)
		s.filesReply(ch, filesMsg{T: "up_err", ID: u.id, Name: u.name,
			Error: fmt.Sprintf("could not finalize: %v", err)})
		return
	}

	// On the record, by name and size, never by content — the same register
	// the clipboard uses. A file appearing on a shared desktop is exactly the
	// kind of act somebody later asks "where did this come from" about.
	s.room.witness.Note(s.room.NameOf(s.memberID), "dropped a file onto the desktop",
		fmt.Sprintf("%s (%d bytes)", u.name, u.got))

	s.filesReply(ch, filesMsg{T: "up_ok", ID: u.id, Name: u.name, Path: u.absPath, Bytes: u.got})
}

// dropUpload forgets one transfer and removes its temp file.
func (s *Session) dropUpload(id string) {
	s.transfersMu.Lock()
	u := s.transfers[id]
	delete(s.transfers, id)
	s.transfersMu.Unlock()
	if u == nil {
		return
	}
	if u.file != nil {
		_ = u.file.Close()
	}
	_ = os.Remove(u.tempPath)
}

// sweepUploads collects transfers nobody has fed lately. Ran on each init
// rather than on a timer: a session with no uploads has nothing to sweep,
// and the corpse of an abandoned one only matters when disk is being asked
// for again.
func (s *Session) sweepUploads() {
	s.transfersMu.Lock()
	var stale []string
	for id, u := range s.transfers {
		if time.Since(u.last) > uploadStaleAfter {
			stale = append(stale, id)
		}
	}
	s.transfersMu.Unlock()
	for _, id := range stale {
		s.dropUpload(id)
	}
}

// closeUploads is Close's half: every temp file goes with the session.
func (s *Session) closeUploads() {
	s.transfersMu.Lock()
	ids := make([]string, 0, len(s.transfers))
	for id := range s.transfers {
		ids = append(ids, id)
	}
	s.transfersMu.Unlock()
	for _, id := range ids {
		s.dropUpload(id)
	}
}

// ---------------------------------------------------------------------------
// The manager's half: listing, the F-key operations, downloads.
//
// The panel's file manager speaks this channel for everything — there is no
// HTTP upload any more, and the panel touches no /files/* route. Deliveries
// (the runtime PUSHING a finished recording or screenshot) ride the channel
// too, by offered id; the one-use HTTP ticket is still minted beside each
// offer, but only because the embedded dev-harness client has no files
// channel to pull with — it retires with that client, not before.
// ---------------------------------------------------------------------------

// sessionRoot builds a throwaway FileServer purely for its resolve/rel logic
// — the one copy of the symlink-resolution rule every file verb goes through.
func (s *Session) sessionRoot() *FileServer {
	return NewFileServer(s.cfg.FilesRoot)
}

func (s *Session) handleFilesList(ch *webrtc.DataChannel, m filesMsg) {
	fs := s.sessionRoot()
	abs, err := fs.resolve(m.Dir)
	if err != nil {
		s.filesReply(ch, filesMsg{T: "ls_err", Ref: m.Ref, Error: err.Error()})
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		s.filesReply(ch, filesMsg{T: "ls_err", Ref: m.Ref, Error: fmt.Sprintf("could not list: %v", err)})
		return
	}
	items := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind := "file"
		switch {
		case e.IsDir():
			kind = "dir"
		case info.Mode()&os.ModeSymlink != 0:
			kind = "link"
			if st, err := os.Stat(filepath.Join(abs, e.Name())); err == nil && st.IsDir() {
				kind = "dir"
			}
		}
		items = append(items, fileEntry{
			Name: e.Name(), Type: kind, Size: info.Size(),
			Modified: info.ModTime().Format(time.RFC3339),
		})
	}
	// Directories first, then by name — the order Midnight Commander uses.
	sort.Slice(items, func(i, j int) bool {
		if (items[i].Type == "dir") != (items[j].Type == "dir") {
			return items[i].Type == "dir"
		}
		return items[i].Name < items[j].Name
	})
	// A DataChannel message tops out near 64 KiB; a directory with ten
	// thousand entries does not fit and would kill the channel silently.
	// Truncation is SAID, so the screen can show "…and 9600 more".
	truncated := 0
	if len(items) > 400 {
		truncated = len(items) - 400
		items = items[:400]
	}
	parent := ""
	if abs != fs.root {
		parent = fs.rel(filepath.Dir(abs))
	}
	s.filesReply(ch, filesMsg{
		T: "ls_ok", Ref: m.Ref, Path: fs.rel(abs), Parent: parent,
		Entries: items, Truncated: truncated,
	})
}

func (s *Session) handleFilesOp(ch *webrtc.DataChannel, m filesMsg) {
	refuse := func(why string) {
		s.filesReply(ch, filesMsg{T: "op_err", Ref: m.Ref, Error: why})
	}
	// Mutating the desktop's files is an act on the shared desktop: the
	// controls, or the administrator's standing authority — the same gate
	// the upload has.
	if !s.privileged && !s.room.IsController(s.memberID) {
		refuse("changing files needs the desktop controls (an administrator may always)")
		return
	}
	fs := s.sessionRoot()
	abs, err := fs.resolve(m.Path)
	if err != nil {
		refuse(err.Error())
		return
	}
	switch m.Op {
	case "mkdir":
		if err := os.MkdirAll(abs, 0o755); err != nil {
			refuse(err.Error())
			return
		}
	case "delete":
		if abs == fs.root {
			refuse("the root itself cannot be deleted")
			return
		}
		if err := os.RemoveAll(abs); err != nil {
			refuse(err.Error())
			return
		}
		s.room.witness.Note(s.room.NameOf(s.memberID), "deleted from the desktop", m.Path)
	case "rename":
		dst, err := fs.resolve(m.To)
		if err != nil {
			refuse(err.Error())
			return
		}
		if err := os.Rename(abs, dst); err != nil {
			refuse(err.Error())
			return
		}
	default:
		refuse(fmt.Sprintf("unknown operation: %q", m.Op))
		return
	}
	s.filesReply(ch, filesMsg{T: "op_ok", Ref: m.Ref, Op: m.Op})
}

// handleDownloadInit answers with the file's identity and starts the stream.
// Reading is like watching: any member may — the same posture the HTTP list
// and ticket always had.
//
// The file is one of two things: a path the CLIENT names, resolved under
// FILES_ROOT like every other path it speaks; or a delivery the SERVER
// offered (m.Deliver), looked up in this session's own offers and exempt
// from the confinement — see deliveredFile for why.
func (s *Session) handleDownloadInit(ch *webrtc.DataChannel, m filesMsg) {
	refuse := func(why string) {
		s.filesReply(ch, filesMsg{T: "dn_err", Ref: m.Ref, Error: why})
	}
	var abs, name string
	if m.Deliver != "" {
		s.transfersMu.Lock()
		df, ok := s.deliveries[m.Deliver]
		s.transfersMu.Unlock()
		if !ok {
			refuse("that delivery is no longer here — it went with the session that was offered it")
			return
		}
		abs, name = df.path, df.name
	} else {
		fs := s.sessionRoot()
		resolved, err := fs.resolve(m.Path)
		if err != nil {
			refuse(err.Error())
			return
		}
		abs, name = resolved, filepath.Base(resolved)
	}
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		refuse("that is not a downloadable file")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		refuse(fmt.Sprintf("could not open: %v", err))
		return
	}

	id := fmt.Sprintf("dn-%d", time.Now().UnixNano())
	stop := make(chan struct{})
	s.transfersMu.Lock()
	if s.downloads == nil {
		s.downloads = map[string]chan struct{}{}
	}
	s.downloads[id] = stop
	s.transfersMu.Unlock()

	s.filesReply(ch, filesMsg{
		T: "dn_meta", Ref: m.Ref, ID: id,
		Name: name, Size: st.Size(), Chunk: uploadChunkSize,
	})
	go s.streamDownload(ch, id, f, stop)
}

// streamDownload pushes the chunks, throttled by the channel's own buffer —
// SCTP applies no back-pressure of its own, and an unthrottled loop would
// balloon the buffered amount until the channel died.
func (s *Session) streamDownload(ch *webrtc.DataChannel, id string, f *os.File, stop chan struct{}) {
	defer f.Close()
	defer s.abortDownload(id)
	buf := make([]byte, uploadChunkSize)
	seq := 0
	var sent int64
	for {
		select {
		case <-s.done:
			return
		case <-stop:
			return
		default:
		}
		for ch.BufferedAmount() > 1<<20 {
			select {
			case <-s.done:
				return
			case <-stop:
				return
			case <-time.After(15 * time.Millisecond):
			}
		}
		n, err := f.Read(buf)
		if n > 0 {
			s.filesReply(ch, filesMsg{
				T: "dn_chunk", ID: id, Seq: seq,
				D: base64.StdEncoding.EncodeToString(buf[:n]),
			})
			seq++
			sent += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				s.filesReply(ch, filesMsg{T: "dn_end", ID: id, Bytes: sent})
			} else {
				s.filesReply(ch, filesMsg{T: "dn_err", ID: id, Error: err.Error()})
			}
			return
		}
	}
}

// abortDownload stops one stream, idempotently.
func (s *Session) abortDownload(id string) {
	s.transfersMu.Lock()
	stop := s.downloads[id]
	delete(s.downloads, id)
	s.transfersMu.Unlock()
	if stop != nil {
		close(stop)
	}
}
