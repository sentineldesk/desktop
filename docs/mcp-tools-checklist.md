# MCP server — tool checklist for driving the desktop end to end

> **Status: 134 tools implemented and verified.** Everything marked [x] works in
> the binary (`sentineldesk`). See [mcp.md](mcp.md) for how to connect it.

Goal: let an AI model use the WebRTC desktop **the way a person does** — see the
screen, move the mouse, type, open programs, manage windows, use the joystick.
The idea is a new MCP transport over the logic that **already exists**, rather
than inventing a protocol of its own.

## Architecture

```
AI host (Claude Code / Desktop / agent)
  └─ spawns:  sentineldesk -mcp-stdio        (thin bridge, JSON-RPC over stdin/stdout)
                    │  local Unix socket (0600, same user)
                    ▼
             sentineldesk (the daemon: Xvfb, WebRTC, input, …)
```

- `MCP_SOCK` on the daemon opens the socket (override with `-mcp-sock`).
- `-mcp-stdio` is the stdio↔socket bridge; killing the AI host never takes the
  desktop down with it.
- The tools drive the **same** injection and control logic as the browser's
  DataChannel.

State of the underlying logic:
- ✅ **exists** = already implemented, only needs exposing as a tool.
- 🔶 **new** = the logic has to be written (X11/EWMH, exec, and so on).

---

## 0. 🔎 The registry (finding the tools)

Every tool declares a **risk level** beside its definition — `read` observes,
`write` drives the desktop, `danger` runs code or touches the system — and a
**category** derived from its name. That declaration is the only input the three
`MCP_POLICY` levels need, and it is published in `tools/list` as the standard
`readOnlyHint` / `destructiveHint` annotations.

- [x] `tool_search` — describe a task, get the matching tools **with their input
  schemas** so they can be called without a second round trip. Filtered by the
  connection's policy before ranking; `category` lists a whole theme. 🔶 new

- [x] `ui_at_point` — what is at a screen coordinate, by descending rather than
  walking. The cheapest question after a screenshot was the most expensive to
  answer; this makes it `O(depth)`. 🔶 new
- [x] `subscribe_events` — be told when something changes instead of polling for
  it: `control`, `room`, `windows`, `focus`, `desktop`. Delivered as
  `notifications/sentineldesk/event`. Nothing is sent until it is called. 🔶 new
- [x] `unsubscribe_events` — stop receiving them on this connection. 🔶 new

`control` is the one an agent cannot work without: it is how it learns that a
person took the controls mid-task, rather than discovering it when its next
injection is refused.

`MCP_DISCOVERY=1` trims `tools/list` to a core set of twelve and leaves
`tool_search` to surface the rest. Every other tool stays callable by name —
discovery narrows what is *advertised*, never what is *permitted*.

Tools that must hold the room's controls before they run — the ones that put
events into X, plus `start_restream` / `stop_restream` — declare
`RequiresControl` in the same place, and it is published as
`sentineldesk/requiresControl`. Risk is no substitute for it: `ui_click` is
`write` and gated, `set_volume` is `write` and not.

**Adding a tool is two edits, not four.** The `toolDef` — name, description,
schema, `Risk`, and `RequiresControl` when it applies — and the `case` in the
matching `dispatchX`. The risk maps and the room-gate switch that used to be the
other two are now derived from the catalogue, and a `toolDef` written without a
`Risk` stops the daemon from starting instead of inheriting a permission nobody
chose.

---

## 1. 👁️ Observation / screen (the "eyes")

- [x] `screenshot` — captures the X screen as PNG (base64). 🔶 new (grab via ximagesrc/scrot)
- [x] `screenshot_region` — captures a crop (x, y, w, h). 🔶 new
- [x] `get_screen_info` — resolution, colour depth, number of desktops. 🔶 new
- [x] `get_pixel_color` — RGB colour at (x, y). 🔶 new
- [x] `find_text` — OCR of the screen and the coordinates of the text sought. 🔶 new (tesseract)
- [x] `read_screen_text` — OCR of the whole screen or a region → text. 🔶 new (tesseract)

## 2. 🖱️ Mouse (the "hand")

- [x] `mouse_move` — move to absolute (x, y). ✅ exists (InputInjector.Move)
- [x] `mouse_click` — click (left/middle/right), optionally at (x, y). ✅ exists (Button)
- [x] `mouse_double_click` — double click. ✅ exists (composable)
- [x] `mouse_down` / `mouse_up` — press/release a button, for drags. ✅ exists (Button)
- [x] `mouse_drag` — drag from (x1,y1) to (x2,y2). ✅ exists (composable)
- [x] `mouse_scroll` — vertical and horizontal wheel. ✅ exists (Wheel)
- [x] `get_mouse_position` — current pointer position. 🔶 new (XQueryPointer)

## 3. ⌨️ Keyboard

- [x] `type_text` — type a string, accented characters included. ✅ exists (Key + xdotool fallback)
- [x] `key_press` — one key by name (Enter, Escape, F5, …). ✅ exists (Key)
- [x] `key_combo` — a combination with modifiers (Ctrl+C, Alt+Tab, Super+D). 🔶 new (press/release sequence)
- [x] `key_down` / `key_up` — hold and release a key. ✅ exists (Key)

## 4. 🪟 Windows (Openbox / EWMH)

- [x] `list_windows` — every window: id, title, class, geometry, desktop, state. 🔶 new (EWMH _NET_CLIENT_LIST)
- [x] `get_active_window` — the focused window. 🔶 new (_NET_ACTIVE_WINDOW)
- [x] `activate_window` — focus and raise by id. 🔶 new
- [x] `move_window` — move a window to (x, y). 🔶 new
- [x] `resize_window` — resize to (w, h). 🔶 new
- [x] `close_window` — close (_NET_CLOSE_WINDOW). 🔶 new
- [x] `minimize_window` / `maximize_window` / `restore_window`. 🔶 new
- [x] `fullscreen_window` — toggle fullscreen. 🔶 new
- [x] `set_window_desktop` — move a window to another desktop. 🔶 new
- [x] `wait_for_window` — wait for a window to appear (title/class) with a timeout. 🔶 new

## 5. 🖥️ Desktops / workspaces

- [x] `list_desktops` — names and number of desktops. 🔶 new
- [x] `get_current_desktop` — the active desktop. 🔶 new
- [x] `switch_desktop` — go to desktop N. 🔶 new

## 6. 🚀 Applications / processes

- [x] `launch_app` — run a command or application (.desktop) on the desktop. 🔶 new (exec)
- [x] `list_installed_apps` — available applications (parsing /usr/share/applications). 🔶 new
- [x] `list_running_processes` — processes (pid, name, cpu, mem). 🔶 new
- [x] `kill_process` — terminate by pid or name. 🔶 new
- [x] `is_running` — is a process or window running? 🔶 new

## 7. 📋 Clipboard

- [x] `get_clipboard` — read the desktop's clipboard. ✅ exists (Clipboard.Get)
- [x] `set_clipboard` — write the clipboard. ✅ exists (Clipboard.Set)

## 8. 🎮 Joystick / gamepad — RETIRED 2026-08-20

The four `gamepad_*` tools and the uinput joystick behind them left the
product (owner's decision, no use found). The implementation survives in git
history should it earn its way back.

## 9. 🐚 Shell / filesystem (execution)

- [x] `run_command` — run a shell command and return stdout/stderr/exit. 🔶 new (exec, with a timeout)
- [x] `read_file` — read a file from the desktop. 🔶 new
- [x] `write_file` — write or create a file. 🔶 new
- [x] `list_directory` — list a directory. 🔶 new

### Remote desktops (RDP / VNC / SPICE) — the graphical counterpart to SSH

- [x] `remote_open` — open a remote machine's screen in a window on the shared
      desktop; inline or from a saved profile. Needs control. Verified against
      real RDP (xfreerdp3) and VNC (Remmina) servers.
- [x] `remote_close` — end a session and remove its window. Needs control.
- [x] `remote_list` — the sessions open right now.
- [x] `remote_profile_save` / `remote_profile_list` / `remote_profile_delete` —
      reusable connection profiles, password stored encrypted in Remmina's store.

## 10. ⏱️ Synchronisation / state (waiting the way a person does)

- [x] `wait` — sleep N milliseconds, giving the UI time to react. 🔶 new
- [x] `get_desktop_info` — WM, resolution, uptime, load, active video encoder. 🔶 new
- [x] `desktop_state` — windows, focus, desktops, screen and room in one call, so
      the picture an agent builds comes from one instant rather than four. 🔶 new
- [x] `get_audio_state` — default sink, volume, mute. 🔶 new (pactl)
- [x] `set_volume` — adjust volume / mute. 🔶 new (pactl)

## 11. 🎥 Recording / streaming (reuses the GStreamer engine)

The capture pipeline (`ximagesrc` + `pulsesrc`) already exists for WebRTC;
recording is a matter of adding a `tee` branch towards a muxer and `filesink`, or
re-broadcasting to another URL. Capture does not need reimplementing.

- [x] `start_recording` — record screen + audio to a file. Parameters: container
  (`mp4` | `webm` | `mkv`), codec (`h264` | `vp8`/`vp9` | `av1`), fps, bitrate, path. 🔶 new
- [x] `stop_recording` — stop and close the file with a correct mux. 🔶 new
- [x] `get_recording_status` — recording?, duration, size, path, codec. 🔶 new
- [x] `list_recordings` — the recordings available, with size and date. 🔶 new
- [ ] `delete_recording` — delete a recorded file. 🔶 new
- [x] `start_restream` — also send the desktop to an external destination:
  `rtmp://` / `rtmps://` for YouTube, Twitch and Facebook, `srt://` or `udp://`
  for a VLC or OBS you run yourself. It forwards the encode the room is already
  producing rather than starting a capture of its own.
- [x] `stop_restream` — detach one destination, or all of them.
- [x] `list_restreams` — where the desktop is currently being sent. Stream keys
  come back redacted.
- [ ] `record_region` — record only a region or window. 🔶 new (optional)

Technical notes:
- **Recording without interrupting WebRTC**: a `tee` after `ximagesrc` feeds both
  the WebRTC encoding and the file encoding (two encoders, or one shared). The
  file's codec is independent of the stream's — you can record H.264 while
  streaming VP8.
- **Containers**: `mp4mux`/`qtmux` (mp4, H.264/AV1), `webmmux` (webm, VP8/VP9/Opus),
  `matroskamux` (mkv, anything). Audio: AAC (`voaacenc`/`avenc_aac`) for mp4,
  Opus for webm.
- **Clean close**: stopping has to send EOS and wait for the muxer to write the
  index (the moov atom), or the mp4 is corrupt. This is new logic worth getting
  right.

---

## Suggested priorities

- **P0 (minimum human-like control)**: `screenshot`, `mouse_move`, `mouse_click`,
  `type_text`, `key_combo`, `launch_app`, `list_windows`, `activate_window`,
  `run_command`, `wait`. Those ten already drive the desktop end to end.
- **P1 (fluency)**: the rest of mouse/keyboard/windows, `get_active_window`,
  `wait_for_window`, `list_running_processes`, `kill_process`, clipboard,
  `get_mouse_position`.
- **P2 (advanced)**: OCR (`find_text`, `read_screen_text`), workspaces,
  files, audio, `get_pixel_color`, `list_installed_apps`.
- **P1-P2 (recording)**: `start_recording`/`stop_recording`/`get_recording_status`
  as P1 (native mp4/webm); `start_restream`/`record_region` as P2.

## New dependencies to install in the image

- `scrot`, or reuse ximagesrc, for `screenshot`.
- `tesseract-ocr` (plus a language pack) for the OCR tools (P2).
- EWMH/windows: doable with `jezek/xgb` (already present) or `xdotool`/`wmctrl`
  (xdotool is already there).
- `pactl` (pulseaudio-utils, already there) for audio.
- Recording and streaming: the muxers ship with
  `gstreamer1.0-plugins-good/bad`; AAC in mp4 may need `gstreamer1.0-libav`
  (avenc_aac), and RTMP its own plugin. Check `mp4mux`, `webmmux`, `x264enc`,
  `rtmpsink`.

## 12. 🔑 Administration (root)

- [x] `sudo_status` — which escalation is available: passwordless sudo, `su`, pkexec, groups.
- [x] `install_packages` — `apt install`; reports the version that landed.
- [x] `remove_packages` — uninstall, with an optional `purge`.
- [x] `search_packages` — search the Debian archive from the on-disk index; an empty index is an error, never "not found", and it is not refreshed on your behalf.
- [x] `system_updates` — pending upgrades with security counted separately, plus the build's own version and the index's age; never refreshes.
- [x] `service_control` — supervisor: status/start/stop/restart of X, audio, WM, AT-SPI, sentineldesk.
- [x] `as_root` on `run_command`, `launch_app`, `read_file`, `write_file`, `list_directory`.
- [x] `user: "root"` on `shell_open` — a persistent root terminal.

How it is put together: the `sentineldesk` user is in the `sudo` group with
`NOPASSWD:SETENV`. The `SETENV` tag is what allows `sudo -E`, without which a
graphical application launched as root loses `DISPLAY`. The root password is set
by the entrypoint from `ROOT_PASSWORD`, or reuses `AUTH_PASS`. A polkit rule
grants the `sudo` group permission, because the desktop has no graphical
authentication agent.

## Outstanding

- `delete_recording`, `record_region`.
- AV1 and zero-copy capture — deferred deliberately: they can only be measured
  meaningfully on a host with a real GPU.

## Security notes

- The MCP socket is local and `0600` (same user).
- `run_command` grants full control of the container: expose it only over the
  daemon's local socket, never over the web.
- On root inside the desktop: the container **is** the sandbox and the boundary
  is the WebSocket login. The real limits are Docker's. See
  [mcp.md § Security](mcp.md#about-root-inside-the-desktop).
