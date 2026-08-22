# MCP server validation — closing out v1

Run against a real desktop brought up from cold (`make down` + `make up`),
image `dfb7f40c5af8`, binary `v1.0.1 (dd25509)`, with the Chromium sandbox on.

Every tool was called by the same path an agent uses: the
`sentineldesk -mcp-stdio` bridge over the local Unix socket, one request at a
time.

This is a **record of one run**, not a live document. The evidence column holds
what the tools actually returned that day, verbatim — including the few replies
that were still in Spanish then and have since been translated in the source.
The catalogue has moved on: `tool_search` and the risk classification came after
this run and are not covered here. See [mcp.md](mcp.md) for the current state.

| # | Tool | Status | Evidence |
|---:|---|:--:|---|
| 1 | `screenshot` | ✅ | { "path": "/tmp/val-shot.png", "size_bytes": 434369 } |
| 2 | `mouse_move` | ✅ | moved to (960, 760) |
| 3 | `mouse_click` | ✅ | clicked button 1 x1 |
| 4 | `type_text` | ⏸️ | injects input: needs a person to grant control from their bar |
| 5 | `key_combo` | ✅ | pressed Escape |
| 6 | `launch_app` | ✅ | { "as_root": false, "command": "xterm -T VALIDACION -e sleep 600", "note": "still running after 700 ms. A window may tak |
| 7 | `list_windows` | ✅ | [ { "id": "0x00c00006", "desktop": -1, "x": 0, "y": 0, "w": 1920, "h": 36, "class": "panel.lxpanel", "title": "panel" }, |
| 8 | `activate_window` | ✅ | activated window 0x0120000c |
| 9 | `run_command` | ✅ | { "as_root": false, "exit_code": 0, "stderr": "", "stdout": "validacion-ok\nsentineldesk\n", "timed_out": false } |
| 10 | `wait` | ✅ | waited 50 ms |
| 11 | `start_recording` | ✅ | recording to /home/sentineldesk/Recordings/rec-20260805-103814.mp4 |
| 12 | `stop_recording` | ✅ | { "path": "/home/sentineldesk/Recordings/rec-20260805-103814.mp4", "size_bytes": 43842 } |
| 13 | `get_recording_status` | ✅ | { "recording": false } |
| 14 | `list_recordings` | ✅ | [ { "modified": "2026-08-04T15:50:40Z", "path": "/home/sentineldesk/Recordings/rec-20260804-155019.mp4", "size_bytes": 1 |
| 15 | `get_clipboard` | ✅ | validacion-mcp |
| 16 | `set_clipboard` | ✅ | clipboard set |
| 17 | `get_active_window` | ✅ | { "geometry": "Window 25165827\n Position: 10,46 (screen: 0)\n Geometry: 1791x1024", "id": "0x01800003", "id_dec": "2516 |
| 18 | `move_window` | ✅ | moved 0x0120000c (0,120,120,-1,-1) |
| 19 | `resize_window` | ✅ | resized 0x0120000c (0,-1,-1,700,420) |
| 20 | `close_window` | ✅ | closed window |
| 21 | `minimize_window` | ✅ | minimized |
| 22 | `maximize_window` | ✅ | maximized |
| 23 | `restore_window` | ✅ | restored |
| 24 | `fullscreen_window` | ✅ | toggled fullscreen |
| 25 | `set_window_desktop` | ✅ | moved to desktop |
| 26 | `wait_for_window` | ✅ | { "class": "xterm.XTerm", "found": true, "id": "0x0120000c", "title": "VALIDACION" } |
| 27 | `list_desktops` | ✅ | [ { "current": true, "name": "1920x1044 desktop 1", "number": 0 }, { "current": false, "name": "1920x1044 desktop 2", "n |
| 28 | `switch_desktop` | ✅ | switched desktop |
| 29 | `list_processes` | ✅ | [ { "command": "/usr/lib/chromium/chromium --disable-dev-shm-usage --remote-debugging-port=9222 --remote-allow-origins=* |
| 30 | `kill_process` | ✅ | killed processes matching "sleep" |
| 31 | `is_running` | ✅ | { "pids": [ 35 ], "running": true } |
| 32 | `list_installed_apps` | ✅ | [ { "exec": "/usr/bin/chromium %U", "name": "Chromium Web Browser" }, { "exec": "uxterm", "name": "UXTerm" }, { "exec":  |
| 33 | `mouse_drag` | ✅ | dragged (940,740) -> (1000,780) |
| 34 | `mouse_scroll` | ✅ | scrolled dy=-1 dx=0 |
| 35 | `get_mouse_position` | ✅ | { "x": 808, "y": 1070 } |
| 36 | `mouse_down` | ✅ | mouse_down button 1 |
| 37 | `mouse_up` | ✅ | mouse_up button 1 |
| 38 | `screenshot_region` | ✅ | <image> |
| 39 | `get_screen_info` | ✅ | { "desktops": 4, "display": ":0", "height": 1080, "width": 1920 } |
| 40 | `get_pixel_color` | ✅ | { "b": 214, "g": 207, "hex": "#c3cfd6", "r": 195 } |
| 41 | `read_screen_text` | ✅ | *. Ee SJ] JC VALIDACION Gl sentineldesk@ofdab241.. @val. htm! - chromium // Paoae vy @ valhtml x + C An @ File /tmp/val. |
| 42 | `find_text` | ✅ | no match for "files" on screen |
| 43 | `gamepad_button` | ✅ | button 0 down |
| 44 | `gamepad_tap` | ✅ | tapped button 1 |
| 45 | `gamepad_axis` | ✅ | axis 0 = 0.30 |
| 46 | `gamepad_state` | ✅ | gamepad state applied |
| 47 | `read_file` | ✅ | contenido de prueba |
| 48 | `write_file` | ✅ | wrote 20 bytes to /tmp/val-mcp.txt |
| 49 | `list_directory` | ✅ | [ { "modified": "2026-08-05T10:26:02Z", "name": ".X0-lock", "size": 11, "type": "file" }, { "modified": "2026-08-05T10:2 |
| 50 | `get_audio_state` | ✅ | { "mute": false, "sink": "sentineldesk", "volume": "Volume: front-left: 39321 / 60% / -13.31 dB, front-right: 39321 / 60 |
| 51 | `set_volume` | ✅ | volume 60% |
| 52 | `start_restream` | ✅ | streaming to custom (udp://•••, audio=false) — reusing the live encode, no second capture |
| 53 | `stop_restream` | ✅ | stopped 1 destination(s) |
| 54 | `list_restreams` | ✅ | not streaming anywhere |
| 55 | `get_desktop_info` | ✅ | { "display": ":0", "encoder": "auto", "joystick": true, "memory_used": "2229/7833 MB", "recording": false, "resolution": |
| 56 | `ui_tree` | ✅ | { "count": 11, "elements": [ { "name": "lxpanel", "ref": "0", "role": "application" }, { "center_x": 960, "center_y": 18 |
| 57 | `ui_find` | ✅ | { "count": 0, "elements": [] } |
| 58 | `ui_click` | ⏸️ | injects input: needs a person to grant control from their bar |
| 59 | `ui_set_text` | ⏸️ | injects input: needs a person to grant control from their bar |
| 60 | `ui_get_text` | ✅ | { "name": "Aceptar", "ref": "3/0/0/0/5/2/0/1/3/3", "role": "button", "text": "Aceptar" } |
| 61 | `ui_focus` | ⏸️ | injects input: needs a person to grant control from their bar |
| 62 | `ui_wait_for` | ✅ | { "elements": [ { "actions": [ "press", "showContextMenu" ], "center_x": 317, "center_y": 264, "height": 25, "name": "Ac |
| 63 | `shell_open` | ✅ | { "id": "sh2", "shell": "/bin/bash", "user": "" } |
| 64 | `shell_exec` | ✅ | { "completed": true, "output": "abierto" } |
| 65 | `shell_input` | ✅ | enviados 17 bytes |
| 66 | `shell_read` | ✅ | echo desde-input desde-input |
| 67 | `shell_list` | ✅ | [ { "alive": true, "id": "sh2", "pending": 0, "seconds": 1, "user": "" } ] |
| 68 | `shell_close` | ✅ | session closed |
| 69 | `ssh_connect` | ✅ | { "host": "127.0.0.1:22", "id": "ssh1", "user": "sentineldesk" } |
| 70 | `ssh_exec` | ✅ | { "exit_code": 0, "stderr": "", "stdout": "0fd3b2417759\nssh-ok\n" } |
| 71 | `ssh_upload` | ✅ | subidos 20 bytes a /tmp/val-subido.txt |
| 72 | `ssh_download` | ✅ | descargados 20 bytes en /tmp/val-bajado.txt |
| 73 | `ssh_list_remote` | ✅ | [ { "modified": "2026-08-05T10:26:01Z", "name": "supervisor.sock", "size": 0, "type": "file" }, { "modified": "2026-08-0 |
| 74 | `ssh_tunnel_local` | ✅ | { "spec": "127.0.0.1:18080 → 127.0.0.1:8080 (via 127.0.0.1:22)", "tunnel_id": "ssh1-l1" } |
| 75 | `ssh_tunnel_remote` | ✅ | { "spec": "127.0.0.1:22:127.0.0.1:18081 → 127.0.0.1:8080 (inverso)", "tunnel_id": "ssh1-r2" } |
| 76 | `ssh_tunnels` | ✅ | [ { "connections": 0, "id": "ssh1-l1", "kind": "local", "spec": "127.0.0.1:18080 → 127.0.0.1:8080 (via 127.0.0.1:22)" }  |
| 77 | `ssh_tunnel_close` | ✅ | tunnel closed |
| 78 | `ssh_list` | ✅ | [ { "host": "127.0.0.1:22", "id": "ssh1", "seconds": 0, "tunnels": 1, "user": "sentineldesk" } ] |
| 79 | `ssh_disconnect` | ✅ | SSH session closed |
| 80 | `ssh_keygen` | ✅ | { "path": "/home/sentineldesk/.ssh/val_key", "public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO3cP0MefXCFpM2oV4WDnkBN |
| 81 | `ssh_copy_id` | ✅ | clave instalada en sentineldesk@127.0.0.1:22: instalada |
| 82 | `window_properties` | ✅ | { "WM_CLASS": [ "xterm", "XTerm" ], "WM_CLIENT_MACHINE": "0fd3b2417759", "WM_COMMAND": [ "{ \"/usr/bin/xterm", "-T", "VA |
| 83 | `window_set_state` | ✅ | add above en 0x0120000c |
| 84 | `window_hierarchy` | ✅ | xwininfo: Window id: 0x21f (the root window) (has no name) Root window id: 0x21f (the root window) (has no name) Parent  |
| 85 | `sudo_status` | ✅ | { "groups": [ "sentineldesk", "sudo", "video" ], "hint": "as_root:true en run_command / launch_app / write_file / read_f |
| 86 | `install_packages` | ✅ | { "as_root": true, "exit_code": 0, "installed": { "openssh-server": "1:10.0p1-7+deb13u4" }, "log": "…\nSetting up runit- |
| 87 | `remove_packages` | ✅ | { "as_root": true, "exit_code": 0, "log": "Reading package lists...\nBuilding dependency tree...\nReading state informat |
| 88 | `search_packages` | ✅ | { "count": 2, "note": "the apt index was empty, so apt-get update was run", "query": "openssh-server", "results": [ { "d |
| 89 | `service_control` | ✅ | { "action": "status", "as_root": true, "exit_code": 3, "service": "all", "stderr": "", "stdout": "at-spi RUNNING pid 38, |
| 90 | `set_resolution` | ✅ | { "applied": true, "resolution": "1600x900" } |
| 91 | `wait_for_idle` | ✅ | { "cpu_percent": 79, "idle": true, "reason": "the screen went still and the CPU settled", "waited_ms": 1184 } |
| 92 | `open_app_and_wait` | ✅ | { "opened": true, "waited_ms": 2301, "window": { "id": "0x0120000c", "desktop": 0, "x": 1436, "y": 92, "w": 484, "h": 31 |
| 93 | `fill_form` | ⏸️ | injects input: needs a person to grant control from their bar |
| 94 | `ui_diff` | ✅ | { "baseline": true, "nodes": 284, "note": "first call: the reference snapshot was stored; the next one returns only the  |
| 95 | `action_log` | ✅ | { "count": 5, "entries": [ { "time": "2026-08-05T10:36:47.963Z", "tool": "get_clipboard", "ok": true, "ms": 4 }, { "time |
| 96 | `snapshot_create` | ✅ | { "created": "validacion-v1", "note": "prueba de cierre", "path": "/home/sentineldesk/.sentineldesk-snapshots/validacion |
| 97 | `snapshot_list` | ✅ | { "dir": "/home/sentineldesk/.sentineldesk-snapshots", "snapshots": [ { "created": "2026-08-05T10:39:50Z", "name": "vali |
| 98 | `snapshot_restore` | ⏭️ | destructiva por diseño: sobrescribe /home |
| 99 | `snapshot_delete` | ✅ | { "deleted": "validacion-v1" } |
| 100 | `room_state` | ✅ | { "controller": "Viewer 1", "controller_id": "u1", "humans_present": true, "may_inject": false, "note": "Input is arbitr |
| 101 | `request_control` | ✅ | { "granted": true, "reason": "a person granted it" } |
| 102 | `release_control` | ✅ | control released to Viewer 1 |
| 103 | `terminal_run` | 🟡 | ejecutada y reportada (confirmado por terminal_read), pero la respuesta superó mi timeout de 60 s |
| 104 | `terminal_open` | ✅ | { "exit_codes": true, "note": "exit codes are reported. Use `sudo -E su` rather than `sudo su` to keep them across a roo |
| 105 | `check_errors` | ✅ | { "errors_on_screen": false, "note": "nothing is reporting a failure. This only sees graphical dialogs — a command that  |
| 106 | `terminal_read` | ✅ | { "last_command": "", "last_exit_code": 0, "last_succeeded": true, "text": "sentineldesk@0fd3b2417759:/$" } |
| 107 | `browser_open` | ✅ | navegando |
| 108 | `browser_tabs` | ✅ | [ { "id": "A618B562BC288AEF42F95E0ABE34D695", "title": "val.html", "url": "file:///tmp/val.html" } ] |
| 109 | `browser_goto` | ✅ | navegando a file:///tmp/val.html |
| 110 | `browser_eval` | ✅ | /2 |
| 111 | `browser_click` | ✅ | clicked #b |
| 112 | `browser_type` | ✅ | escrito en #i |
| 113 | `browser_text` | ✅ | Titulo MCP |
| 114 | `browser_wait_for` | ✅ | #t appeared |


**107 verified · 1 partial · 5 waiting for a turn at the controls · 1 skipped as destructive — 114 in total.**
