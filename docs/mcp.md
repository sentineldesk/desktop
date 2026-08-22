# MCP — driving the WebRTC desktop from an AI

The backend exposes an **MCP server** so a model (Claude Code, Claude Desktop, an
agent) can use the desktop the way a person does: see the screen, move the mouse,
type, open programs, manage windows, run commands.

The daemon opens a **local Unix socket** and the `-mcp-stdio` sub-command is a
thin stdio↔socket bridge that the AI host spawns.

```
AI host (Claude Code)
  └─ spawns:  sentineldesk -mcp-stdio -mcp-sock <socket>   (JSON-RPC bridge over stdin/stdout)
                    │  local Unix socket, 0600 (user sentineldesk)
                    ▼
             sentineldesk (daemon)  ── MCP_SOCK=/run/user/1000/sentineldesk-mcp.sock
```

The daemon opens the socket by itself because supervisord passes it
`MCP_SOCK=/run/user/1000/sentineldesk-mcp.sock`. No extra flag is needed.

## Connecting Claude Code (or Claude Desktop)

Two transports, one door. **Remote** — the everyday path in the workrooms
deployment — goes through the front desk's MCP gateway over Streamable HTTP,
scoped to one room, and one command sets everything up:

```bash
sentineldesk-cli connect claude-code \
  --url http://<front-desk>:9090 --room <workroom-uuid> \
  --user <admin> --pass <password>          # or --name <alias> for a guest key
```

It authenticates at the WebSocket door (the only place credentials become
keys), verifies the gateway end to end, registers the server in Claude Code
when the `claude` CLI is on PATH, and prints how to disconnect. The remote
host gets the ten `workroom_*` control-plane tools plus the room's own 128
desktop tools, behind the same policy ceiling and the same control gate a
local host meets — the gateway proxies to the very bridge described below.

**Local** — an AI host on the room's own machine — runs the bridge
**inside the container** with `docker exec`. Add this to your MCP
configuration:

```json
{
  "mcpServers": {
    "sentineldesk": {
      "command": "docker",
      "args": [
        "exec", "-i", "-u", "sentineldesk", "sentineldesk",
        "/usr/local/bin/sentineldesk", "-mcp-stdio",
        "-mcp-sock", "/run/user/1000/sentineldesk-mcp.sock"
      ]
    }
  }
}
```

- `-i` keeps stdin open (that is the MCP transport).
- `-u sentineldesk` runs as the socket's owner (uid 1000).
- The container has to be named `sentineldesk` (or adjust the name).

**On a native install the path is not `/run/user/1000`.** The installer puts the
desktop's user on whatever uid is free — 1000 usually belongs to a person
already — so the socket follows it. Read it rather than guessing:

```bash
grep MCP_SOCK /etc/sentineldesk/env     # e.g. /run/user/1001/sentineldesk-mcp.sock
```

There is no `docker exec` in front of it either; the bridge is the same binary
run as the same user:

```bash
sudo -u sentineldesk /usr/local/bin/sentineldesk -mcp-stdio \
  -mcp-sock "$(. /etc/sentineldesk/env; echo "$MCP_SOCK")"
```

## Available tools (134)

**The catalogue is versioned.** `initialize` reports the build in
`serverInfo.version` and the catalogue size in
`_meta["sentineldesk/catalogue"].tools` — the full catalogue, not the
connection's policy-filtered view, because "which catalogue does this server
speak" must not change with `MCP_POLICY`. Every change to the set is recorded
in [`mcp-changelog.md`](mcp-changelog.md), and a test pins that file's newest
entry against the catalogue the binary actually serves, so the record cannot
fall behind by being forgotten. A host that depends on a particular tool checks
the count and the changelog instead of discovering a missing name three calls
in.

Every tool carries a **risk level** — `read`, `write` or `danger` — declared next
to its definition. It is what the four `MCP_POLICY` levels are built on, and it
is published in `tools/list` as the standard `readOnlyHint` and
`destructiveHint` annotations, so a host that understands them can shape its own
confirmation prompts without knowing anything about SentinelDesk.

Tools that need the room's controls before they run declare that too, and it is
published alongside:

```json
"annotations": {
  "readOnlyHint": false,
  "destructiveHint": false,
  "sentineldesk/requiresControl": true
}
```

`sentineldesk/requiresControl` is not part of the specification — it is
namespaced so it cannot collide with something that later is — and it answers a
question no standard hint does: **will this call be held until the agent holds
the desktop?** Risk is no substitute. `ui_click` is `write` and gated,
`set_volume` is `write` and not; `start_restream` is `danger` and gated,
`write_file` is `danger` and not. A client that wants to call `request_control`
at the right moment reads this rather than carrying its own copy of the list.

**🔎 Finding the right tool**

| Tool | What it does |
|---|---|
| `ui_at_point` | What is at these screen coordinates: the element plus the chain containing it. Descends to the point instead of walking every window, so it answers *what is that thing* in one call rather than a tree walk |
| `tool_search` | Describe a task in plain words and get back the tools that do it, with their full input schemas — see [Finding tools without loading all of them](#finding-tools-without-loading-all-of-them) |

**🔔 Being told, instead of asking**

| Tool | What it does |
|---|---|
| `ask_human` | Ask the people watching a question and wait for the answer — with buttons if you pass `options`, free text otherwise. A timeout is reported as a timeout, never as a default: silence is not agreement |
| `subscribe_events` | Ask to be notified when something changes: `control` (who is driving), `room` (who joined or left), `windows` (a window appeared — how a dialog interrupts), `focus`, `desktop`. Events arrive as `notifications/sentineldesk/event` |
| `unsubscribe_events` | Stop receiving them on this connection |

Nothing is sent until `subscribe_events` is called, so a host that does not know
about the extension is unaffected. The topic that matters most is `control`:
without it, an agent learns that a person took the desktop away by having its
next click refused — a denial where there should have been a notice.

**👁️ Seeing the screen**

| Tool | What it does |
|---|---|
| `screenshot` | Capture the screen as PNG |
| `screenshot_region` | Capture one rectangle only (cheaper) |
| `get_screen_info` | Resolution, display, number of desktops |
| `get_pixel_color` | RGB of one pixel — assert state without an image |
| `read_screen_text` | **OCR**: the text on screen, or in a region |
| `find_text` | **OCR**: screen coordinates of a string, ready to click |
| `check_errors` | Every error dialog, alert and message box on screen, with its text and buttons |

`check_errors` is there because a graphical program does not fail with an exit
code — it puts a box on the screen. Call it after launching something, or
whenever a step did not do what it was supposed to.

**🌲 The accessibility tree** — structure instead of pixels

| Tool | What it does |
|---|---|
| `ui_tree` | Every window and widget with its role, name, text, state, coordinates and available actions |
| `ui_find` | Elements by role, name or text — returns a `ref` for each, plus coordinates |
| `ui_click` | Invoke an element's action by `ref`. The pointer never moves, so it cannot miss and a partly covered window does not matter |
| `ui_set_text` | Write into an editable field by `ref`, whatever has focus |
| `ui_get_text` | Read an element's text or label, no OCR involved |
| `ui_focus` | Give an element keyboard focus, so `type_text` lands where intended |
| `ui_wait_for` | Wait until an element matching role/name/text exists — the honest alternative to guessing a `wait` |

This is the family to reach for before `screenshot` when the job is to *operate*
an application rather than to look at one. A screenshot has to be interpreted;
the tree already says what each thing is and where it is.

**🌐 The browser, through DevTools** — Chromium on port 9222

| Tool | What it does |
|---|---|
| `browser_open` | Launch Chromium with the DevTools Protocol enabled, optionally at a URL. Reports it if already running |
| `browser_tabs` | The open tabs, with title and URL |
| `browser_goto` | Navigate the active tab and wait for the load to finish |
| `browser_text` | The visible text of the page, or of one CSS selector — what replaces OCR for web content |
| `browser_click` | Click by CSS selector: exact, no coordinates |
| `browser_type` | Type into an input or textarea by selector, firing the events a real page expects |
| `browser_wait_for` | Wait until a selector appears |
| `browser_eval` | Run JavaScript in the page and return the result — the most capable of the set |

These drive the real DOM. Where `ui_*` reads the desktop as an accessibility
tree, this reads a page as a document, which is both smaller and exact.

**🖱️ Mouse and ⌨️ keyboard**

| Tool | What it does |
|---|---|
| `mouse_move`, `mouse_click` | Move and click (button, double click) |
| `mouse_down`, `mouse_up` | Press / release a button |
| `mouse_drag` | Drag in steps, for applications that ignore jumps |
| `mouse_scroll` | Vertical and horizontal wheel |
| `get_mouse_position` | Current pointer position |
| `type_text` | Type text, accented characters included |
| `key_combo` | A key or a combination (`ctrl+c`, `alt+Tab`, `super+d`) |

**🪟 Windows and desktops**

| Tool | What it does |
|---|---|
| `list_windows` | id, desktop, geometry, class and title |
| `get_active_window` | The focused window |
| `activate_window` | Focus and raise |
| `move_window`, `resize_window` | Move / resize |
| `maximize_window`, `restore_window`, `minimize_window`, `fullscreen_window` | States |
| `close_window` | Close |
| `set_window_desktop` | Move to another desktop |
| `wait_for_window` | **Wait** for a window to appear, instead of guessing a `wait` |
| `list_desktops`, `switch_desktop` | Workspaces |

**🚀 Applications, processes and shell**

| Tool | What it does |
|---|---|
| `launch_app` | Run a program (detached) — `as_root` for administration GUIs |
| `list_installed_apps` | Applications with a `.desktop` entry |
| `list_processes` | pid, cpu%, mem%, command, with an optional filter |
| `kill_process`, `is_running` | Kill / check |
| `run_command` | Shell command with stdout/stderr/exit — runs in a terminal window on the shared screen, `as_root` for privileges |
| `read_file`, `write_file`, `list_directory` | Files — `as_root` for `/etc`, `/root`, and so on |

**⏱️ Background jobs** — everything the agent runs, run where it can be stopped

| Tool | What it does |
|---|---|
| `job_start` | Start a command in a terminal window on the shared screen; returns a job id immediately |
| `job_status` | `running`, `done`, `failed` or `aborted`, with the exit code and who stopped it |
| `job_output` | stdout, stderr or both — kept apart on disk — optionally just the tail |
| `job_wait` | Wait for it. **A timeout returns what there is so far and leaves the job running** |
| `job_abort` | Stop one: signalled first, killed if it will not go |
| `job_list` | Everything on this desktop, newest first, including work somebody else started |

There is no invisible way to run a command here, and that is the point rather
than a side effect. Every command opens a window people can watch, its two
streams land in `/tmp/sentineldesk/jobs/<id>/` where they can be read afterwards,
and anyone in the room can stop it mid-run. `run_command` is a job that waits;
`job_start` is a job that does not.

The distinction between a timeout and a kill is the one worth internalising.
`run_command` with a 15-second timeout used to *destroy* the process at fifteen
seconds, so `curl -O` on anything large was not a slow command — it was a command
that could not be expressed. Now the timeout bounds how long the CALL waits: the
answer comes back partial, with a `job_id`, and the download is still going.
Calling the tool again instead of `job_wait` starts a second download.

**🔑 Administration (root)** — a real desktop can be administered

| Tool | What it does |
|---|---|
| `sudo_status` | Which escalation paths exist: passwordless sudo, `su`, pkexec, groups |
| `install_packages` | `apt install` whatever is missing; reports the version that landed |
| `remove_packages`, `search_packages` | Uninstall / search the Debian archive |
| `system_updates` | Pending upgrades from the on-disk index, security counted separately, plus this build's version — with the index's age, because it never refreshes |
| `service_control` | supervisor: status, start, stop, restart of X, audio, WM, AT-SPI, sentineldesk |

On top of that, `as_root: true` on `run_command`, `launch_app`, `read_file`,
`write_file` and `list_directory`, and `user: "root"` on `shell_open` for a
persistent root terminal.

**📋 Clipboard · 🔊 audio · ⏱️ state**

| Tool | What it does |
|---|---|
| `get_clipboard`, `set_clipboard` | The desktop's clipboard |
| `get_audio_state`, `set_volume` | Sink, volume and mute |
| `wait` | Sleep N ms |
| `get_desktop_info` | WM, resolution, uptime, memory, encoder, joystick, recording |
| `desktop_state` | Windows, focus, desktops, screen and room in one snapshot |

**🖥️ Persistent terminal** — `run_command` is one-shot; this is a real shell

| Tool | What it does |
|---|---|
| `shell_open` | Opens a shell on a real PTY (keeps cwd, variables, history); `user:"root"` for a root terminal |
| `shell_exec` | Runs a command; **state persists** between calls |
| `shell_input` | Sends keys without Enter: answering prompts, passwords, Ctrl+C |
| `shell_read` | Reads the accumulated output, to follow a long command |
| `shell_list`, `shell_close` | Session management |

This is for interactive programs `run_command` cannot handle: `sudo`, `vim`,
`top`, installers that ask yes/no.

**🖳 Terminal on the desktop** — the one a person can watch

| Tool | What it does |
|---|---|
| `terminal_open` | Opens a terminal **window on the screen**, visible to everyone in the room |
| `terminal_run` | Runs a command in it and reports the output and the exit status |
| `terminal_read` | Reads what a terminal is showing right now, plus the last exit status — **including a command a person typed** |

`shell_*` is private and headless; this is the same desktop everybody is looking
at. Use it when the work should be witnessed, and `terminal_read` when somebody
says "look at this error" — the agent reads what actually happened instead of
being told about it second-hand. Every interactive shell in the image reports
its exit status, which is what makes that possible; use `sudo -E su` rather than
plain `sudo su` to carry it into a root shell.

`terminal_run` waits for the shell prompt to come back, so it reports what the
command printed AND its exit status. When the command ends the shell instead —
`exit`, `logout`, anything that closes the emulator — there is no prompt coming
back; it notices the window went away and answers `terminal_closed: true` in a
couple of seconds rather than waiting out the timeout and then guessing.

**🔐 SSH** — connections, transfers and tunnels

| Tool | What it does |
|---|---|
| `ssh_connect` | Connects with **a password or a private key** (optional passphrase) |
| `ssh_exec` | Remote command with stdout, stderr and exit code |
| `ssh_upload`, `ssh_download`, `ssh_list_remote` | Transfers over SFTP |
| `ssh_tunnel_local` | Forward tunnel (`-L`): reach a service only the remote can see |
| `ssh_tunnel_remote` | **Reverse tunnel** (`-R`): publish this desktop from behind NAT |
| `ssh_tunnels`, `ssh_tunnel_close` | Inspect and close tunnels, with connections served |
| `ssh_keygen`, `ssh_copy_id` | Generate a key and install it on the server |
| `ssh_list`, `ssh_disconnect` | Session management |

Tunnels live inside the process (Go's native SSH library), so they can be listed
and closed — no stray `ssh` processes are left behind.

**🖥️ Remote desktops (RDP/VNC/SPICE)** — the graphical counterpart to SSH

| Tool | What it does |
|---|---|
| `remote_open` | Opens a remote desktop **in a window on the shared screen** — RDP, VNC or SPICE, inline or from a saved profile. Needs control. |
| `remote_close` | Ends a session and removes its window. Needs control. |
| `remote_list` | The sessions open right now: id, protocol, host, backend, window |
| `remote_profile_save`, `remote_profile_list`, `remote_profile_delete` | Reusable connection profiles (password stored encrypted) |

Remmina is the backend by default (one path for every protocol, the password
encrypted into a profile and never on a command line); the direct clients
(`xfreerdp3`, `xtigervncviewer`) are the fallback. Saved profiles live in
Remmina's own store, so a person's Remmina lists them too. Unlike `ssh_*`, which
is headless, a remote desktop lands on the screen the whole room is watching —
which is why opening and closing one require control.

**🪟 Low-level windows (EWMH/X11)**

| Tool | What it does |
|---|---|
| `window_properties` | Every EWMH property: type, `_NET_WM_STATE`, pid, class, allowed actions |
| `window_set_state` | above, below, sticky, shaded, fullscreen, skip_taskbar, modal… |
| `window_hierarchy` | The raw X11 tree (parents, children, override-redirect) |

**⚡ Fewer round trips**

| Tool | What it does |
|---|---|
| `set_resolution` | Changes the resolution **without restarting anything** (it can only shrink below the size reserved at start) |
| `wait_for_idle` | Waits for the screen to stop changing **and** the CPU to settle, instead of a guessed `wait` |
| `open_app_and_wait` | Launch + wait for the window + focus + wait for the paint, in **one** call |
| `fill_form` | Fills several fields by accessibility name and optionally presses a button |
| `ui_diff` | Returns **only what changed** in the tree since the last call — a fraction of the size of `ui_tree` |

**📼 Auditing and restore points**

| Tool | What it does |
|---|---|
| `action_log` | A record of every call: time, arguments, result, duration. While recording it also carries the **minute within the video** |
| `snapshot_create` | Restore point: the home plus the list of installed packages |
| `snapshot_list`, `snapshot_delete` | Management |
| `snapshot_restore` | Returns the home to the saved state and reports which packages were installed afterwards |

**🎥 Recording and streaming**

| Tool | What it does |
|---|---|
| `start_recording`, `stop_recording` | Record to **mp4 / webm / mkv**, closed cleanly |
| `get_recording_status`, `list_recordings` | Status and files |
| `start_restream`, `stop_restream`, `list_restreams` | Also send the desktop to **RTMP** (YouTube/Twitch/Facebook), **SRT** or **UDP** (VLC/OBS), reusing the live encode |

Full checklist and design notes: [mcp-tools-checklist.md](mcp-tools-checklist.md).

## Finding tools without loading all of them

A hundred and twenty schemas is a real amount of a model's context, spent
before it has read the request. `tool_search` is the way around it: describe the
task and get back the handful of tools that do it.

```json
{"name": "tool_search", "arguments": {"query": "give someone remote access"}}
```

```json
{
  "matched": 6, "of": 120,
  "tools": [
    {"name": "ssh_connect",  "category": "ssh", "risk": "danger", "description": "…", "inputSchema": {…}},
    {"name": "ssh_copy_id",  "category": "ssh", "risk": "danger", "description": "…", "inputSchema": {…}},
    {"name": "ssh_list_remote", "category": "ssh", "risk": "read", "description": "…", "inputSchema": {…}}
  ]
}
```

The schema comes back with the hit, so the tool can be called straight away —
there is no second round trip to fetch it. Pass `category` to list a whole
theme, on its own or alongside a query:

```json
{"name": "tool_search", "arguments": {"category": "ssh"}}
```

The themes are `ssh`, `shell`, `terminal`, `browser`, `accessibility`,
`windows`, `input`, `screen`, `files`, `processes`, `packages`, `snapshot`,
`recording`, `restream`, `room`, `audio`, `clipboard`, `desktops`,
`system` and `general`.

Results are filtered by the connection's policy before ranking: a `readonly`
connection searching for "run a command" gets the read-only tools that match and
not `run_command`, because turning up a tool the connection may never call is a
worse answer than turning up none.

### MCP_DISCOVERY — trimming what gets advertised

Searching only saves context if something is not being listed in the first
place. Most hosts already handle that themselves: Claude Code defers tool
schemas and loads them on demand, and where it does, this is unnecessary. For
hosts that do not:

```
MCP_DISCOVERY=1
```

`tools/list` then answers with a **core set of twelve** — `tool_search`,
`screenshot`, `ui_tree`, `ui_find`, `ui_click`, `mouse_click`, `type_text`,
`key_combo`, `list_windows`, `run_command`, `wait` and `room_state` — enough to
look at the desktop, read its structure, click, type and run something.

**Everything else stays callable by name.** Discovery narrows what is
*advertised*, never what is *permitted*; the only thing that can refuse a call
is the policy, and it is applied separately. A tool left out of the list is one
the model has not been told about yet, not one it is forbidden to use — which is
precisely what makes searching for it worth doing.

It is **off by default**, deliberately. A host that defers tool loading does
this better than the server can, because it can see the conversation.

## Trying it without Claude Code

You can speak the protocol by hand over the socket:

```bash
printf '%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
| docker exec -i -u sentineldesk sentineldesk \
    /usr/local/bin/sentineldesk -mcp-stdio -mcp-sock /run/user/1000/sentineldesk-mcp.sock
```

`tools/mcp-cli.py` wraps this for one-off calls. `tools/mcp-validate.py` is
the **real** end-to-end check: it exercises a broad slice of the catalogue and
reads every EFFECT back — a file typed through XTEST is read from disk, a
written file is round-tripped, a screenshot is measured, the accessibility
tree is parsed — so a tool that returns ok while doing nothing fails it.

```bash
tools/mcp-validate.py --container sentineldesk
```

`tools/mcp-coverage.py` is the breadth pass to that depth: it INVOKES all 128
tools with safe arguments and sorts each into responded, refused (a sensible
guard), or skipped — the destructive and externally-dependent ones
(`install_packages`, `ssh_*` with no host, `start_restream` with no
destination…), each named, never dropped silently. Anything that crashes,
times out or comes back unhandled is a failure. Last run on the deployed
image: 134/134 accounted for, 0 failures.

```bash
tools/mcp-coverage.py --container sentineldesk
```

## Sharing the session with the agent

Now that capture lives in a **room**, the desktop takes several participants at
once and **one of them drives**. That changes how it is worth working with the
MCP: the agent and the person can be on the same desktop, seeing the same thing,
and hand control back and forth without restarting anything.

**🚪 The room**

| Tool | What it does |
|---|---|
| `room_state` | Who is here, who holds control, and whether this connection may inject input |
| `request_control` | Ask for the desktop. **The people watching decide**: a prompt appears on their screen and this waits for the answer. No answer means no. Granted immediately when nothing is driving |
| `release_control` | Hand the controls back when the task is done, so nobody has to take them |

**The agent is arbitrated like everybody else.** Every tool that reaches XTEST —
mouse, keyboard, `ui_click`, `fill_form`, `terminal_run` — goes through
the same gate the browsers do, so the agent must hold the controls before it can
act. It never takes them implicitly, not even with the room empty: call
`request_control` (granted at once when nothing is driving) and `release_control`
when the task ends. Releasing leaves the controls free rather than handing them
to anybody.

Room control and the policy answer different questions, and only one of them is
about right now. `-mcp-policy readonly` is what stops an agent from touching
anything at all — a read-only connection cannot act even while holding the
controls.

## WebMCP — the browser's own AI, over the same session

The Unix socket above is one plane: an AI **host** the operator runs (Claude
Code, Claude Desktop) spawns a bridge into the container. There is a second,
newer way in that needs no host at all — the tools the **browser** publishes to
whatever model it carries, which is the emerging *Web Machine Learning* "model
context" API that Chrome's built-in Gemini reads.

The standalone React client registers a small set of tools on
`document.modelContext` (and the legacy `navigator.modelContext` alias) the
moment a session goes **live**, and drops them on disconnect. A polyfill
installs the registry when the browser has no native one, so the same tools are
discoverable whether or not the browser ships the API yet.

The design rule is the load-bearing part: **these tools do exactly what the
signed-in person does, over exactly the path a person's clicks take** — the
authenticated WebRTC DataChannel the WebSocket login opened. No new endpoint, no
second credential, nothing to secure twice. The browser's AI can act precisely
as far as the person it is sitting with can, and no further: every tool that
writes to the desktop refuses — in the same words the server would — until the
controls are held, so the model learns to call `take_control` first.

| Tool | Reads / writes | What it does |
|---|---|---|
| `desktop_status` | read | Who holds control, who is in the room, quality, recording |
| `take_control` / `release_control` | write | Claim or free the desktop controls |
| `type_text` | write | Type a run of text, accents and symbols included |
| `press_key` | write | Press one key or a named special (Enter, Tab, arrows, F-keys) |
| `move_mouse` / `click` / `scroll` | write | Pointer, at desktop pixels |
| `set_clipboard` | write | Put text on the desktop clipboard |
| `screenshot` | write | Capture the screen to the download tray |
| `set_quality` | write | Stream quality: auto / media / high |
| `list_files` | read | List a directory under `FILES_ROOT` |
| `make_dir` / `rename_path` | write | Create, rename or move a path |

The list is deliberately the human's surface, not the 134-tool catalogue the
socket plane serves: a browser agent inherits a *person's* session, so it gets a
person's reach. The full catalogue, and the host-spawned bridge that serves it,
stay exactly as documented above.

## Security

- The socket is local and `0600` (the `sentineldesk` user only).
- The browser's file manager (`/files/*`) requires the same session token the
  WebSocket login issues, and is confined to `FILES_ROOT` (`/home/sentineldesk`
  by default; `FILES_ROOT=/` opens the whole container). Symlinks are resolved
  **before** comparing against the root, so a link pointing outside is no way
  out. Downloads use a one-use ticket with a 60-second life instead of carrying
  the token in the URL.
- `run_command` grants full control of the container: it is reachable only over
  the daemon's local socket, never over the web.

### The permission model

With root available the MCP has complete power over the container. That is right
when a person is driving it and wrong when an unsupervised agent is, so there are
four levels and two lists. The **daemon sets the ceiling** through the
environment:

```
MCP_POLICY=full       (default) everything allowed
MCP_POLICY=approve    dangerous tools wait for a person in the room to allow each call
MCP_POLICY=safe       everything except running code or touching the system; as_root is out
MCP_POLICY=readonly   observation only: see the screen, read the tree, list things

MCP_DENY=run_command,ssh_*    additionally deny these (a * suffix matches by prefix)
MCP_ALLOW=screenshot,ui_*     when set, ONLY these
```

The levels are decided by each tool's **risk level**, which is declared on
the tool itself:

| Risk | What it means | `readonly` | `safe` | `approve` | `full` |
|---|---|:-:|:-:|:-:|:-:|
| `read` | Observes; changes nothing | ✅ | ✅ | ✅ | ✅ |
| `write` | Drives the desktop — input, windows, volume, the clipboard — but cannot reach the system underneath | ❌ | ✅ | ✅ | ✅ |
| `danger` | Runs code, touches the system, or moves data outward | ❌ | ❌ | 🙋 | ✅ |

**`approve` is the level between the two answers `safe` and `full` force you to
choose from.** Under it, a dangerous call (and any call with `as_root: true`)
is put to the people in the room as a prompt — the tool's name and its
arguments, composed by the server, never by the agent — with **Allow** and
**Deny** buttons; anyone present may answer, exactly as anyone may answer a
control request. Every way the question can end without a yes is a no with its
own sentence: a person declining, nobody answering within a minute, nobody
present, no room at all. An approval gate that waved calls through an empty
room would be `full` with extra steps. An allowed call is marked
`approved: true` in the action log, so the audit shows the permission next to
the act.

A name that is not in the catalogue is refused below `full` rather than waved
through, and a tool defined without a risk level stops the daemon from starting.
Both are deliberate: the classification used to live in two hand-kept maps in
another file, and a tool missing from them was refused under `readonly` and
allowed under `safe` with nothing to indicate either. `terminal_run`, which
types a command line into a shell and presses Return, spent a while on the
permitted side of `safe` for exactly that reason.

And **each connection can restrict itself further**, never widen:

```bash
sentineldesk -mcp-stdio -mcp-sock … -mcp-policy readonly
```

This is how you hand an agent a read-only endpoint against the same daemon you
use with full permissions. `tools/list` returns only what is allowed: offering a
forbidden tool is inviting the model to walk into a wall.

Denied attempts land in `action_log` with the reason.

### Why a call failed, in a form a program can read

The sentence a caller gets back is written for a model to read, and it is
reworded whenever a better one is found. So the reason travels twice — the prose
in the content, and a kind beside it:

```json
{
  "content": [{"type": "text", "text": "denied by the server policy: MCP_POLICY=readonly: \"run_command\" changes the system"}],
  "isError": true,
  "_meta": { "sentineldesk/denial": "policy" }
}
```

| Kind | What happened | What to do about it |
|---|---|---|
| `policy` | `MCP_POLICY`, `MCP_DENY` or `MCP_ALLOW` refused it | Final for this connection — the capability is not available |
| `room` | The tool needs the desktop's controls and the agent does not hold them | Call `request_control`, or wait for whoever is driving |
| `unknown_tool` | No such tool in the catalogue | Check `tools/list` |
| `tool_error` | The tool ran and reported failure | May be worth retrying |
| `cancelled` | The call was stopped — see below | Nothing; you asked for this |
| `emergency` | This connection has been halted | Nothing; an operator lifts it |
| `approval` | `MCP_POLICY=approve` asked the room and did not get a yes | Not final like `policy`: if a person declined, ask them what they want instead; if nobody was present, try again when somebody is |

The three refusals need genuinely different responses, and matching substrings
to tell them apart is one wording change away from breaking. A successful call
carries no `_meta` at all.

`unknown_tool` is decided before policy, so a name that does not exist reports
the same thing at every level rather than "not in the tool catalogue" under
`safe` and "unknown tool" under `full`. A tool that *does* exist and is hidden
by the level still reports `policy`.

The kind is written into `action_log` as well, so an audit can be read by
machine without parsing prose there either.

### Connections have names, and can be stopped one at a time

Every connection gets a number, and `initialize` hands it back:

```json
{
  "protocolVersion": "2024-11-05",
  "serverInfo": {"name": "sentineldesk", "version": "…"},
  "_meta": {"sentineldesk/connectionId": 3}
}
```

Send `clientInfo` in `initialize` and the name is recorded with it. Both end up
on every entry in `action_log`:

```json
{"time": "…", "tool": "run_command", "conn": 3, "client": "agent-runtime 1.0", "ok": true}
```

This matters because of how the room works. Every MCP connection shares the room
identity `agent` — deliberately, so that a runtime can fan several sub-agents
out across connections and have them act under **one** claim on the desktop. The
cost is that "the agent did this" stops being a useful sentence the moment there
is more than one of them. The connection number is what tells them apart.

It is not a second room identity and must not become one: several agents acting
as one participant is the property, and this only lets the log and the emergency
stop distinguish them.

The number is also the handle for stopping one client without stopping the rest.
A halted connection has every `tools/call` refused with the `emergency` kind,
before the catalogue is even consulted — a client that is supposed to be doing
nothing should not be able to map what exists. Other connections are untouched,
and so is the desktop.

It is deliberately not a kill: calls already running are left to end under their
own cancellation, and nothing reaches into X. It refuses what has not started.
Lifting it is explicit, because an emergency stop that expires by itself is not
one.

### Progress on a long call

`install_packages` can run for minutes and `snapshot_create` for longer, and
until they finished they said nothing — which looks exactly like a hang. Ask for
progress by putting a token in the call's `_meta`:

```json
{"jsonrpc": "2.0", "id": 7, "method": "tools/call",
 "params": {
   "name": "install_packages",
   "arguments": {"packages": ["gimp"]},
   "_meta": {"progressToken": "install-1"}
 }}
```

and notifications arrive while it runs:

```json
{"jsonrpc": "2.0", "method": "notifications/progress",
 "params": {"progressToken": "install-1", "progress": 3,
            "message": "running, 6s elapsed: Setting up gimp (2.10.38-1)"}}
```

The message carries **the command's own last line of output**, because that is
the only honest progress a shell command has: `apt` does not know what fraction
of the way through it is, but "Setting up gimp" tells a person watching far more
than a spinner would. `progress` counts the reports rather than the seconds, so
that it always increases even when the command goes quiet for a while.

Only the shell-based tools report — the same set that can be cancelled —
because they are the ones that run long enough to be worth reporting on.

**Nothing is sent unless you asked.** A client that omits the token gets no
notifications at all, rather than a stream it has to discard. The token is
echoed back exactly as it arrived, string or number; it is your handle, not
ours to normalise.

`message` is an extension here: the declared protocol version defines
`progressToken`, `progress` and `total`, and a client that does not understand
`message` can ignore it and still track that something is happening.

### Cancelling a call

Every `tools/call` runs under a context that ends when the client says so:

```json
{"jsonrpc": "2.0", "method": "notifications/cancelled",
 "params": {"requestId": 7, "reason": "user pressed stop"}}
```

The call comes back with `isError` and the `cancelled` kind, carrying your
`reason` so the model reading the transcript knows why it stopped. Closing the
connection cancels everything it had running, so a host that dies mid-call no
longer leaves work going with nobody to answer to.

**The acknowledgement is immediate**, whatever the tool is doing. That is the
part worth relying on: the reply does not wait for the tool to notice, so a
client is never left unable to tell "still stopping" from "ignored me". Whatever
the tool eventually returns is discarded — one request, one response.

**Whether the *work* stops is a separate question, and the answer is not
uniform.** Two honest lists:

*Stops:* `run_command`, `install_packages`, `remove_packages`,
`search_packages`, `system_updates`, `service_control`, `set_resolution`, `snapshot_create`,
`snapshot_restore` (the process is killed), `wait`, `terminal_run`,
`terminal_read`, `browser_open`, `browser_wait_for`, `wait_for_window`,
`wait_for_idle`, `open_app_and_wait`, `fill_form` (the polling loop stops).

*Carries on to completion:* the accessibility bridge behind the `ui_*` tools,
OCR in `read_screen_text` and `find_text`, a CDP request already sent, the
persistent shells and SSH sessions, and the short `xdotool` / `wmctrl`
invocations — those last finish in milliseconds, so there is nothing to
interrupt.

So `cancelled` means *the request is over*, not *the machine is back where it
was*. A client should not assume a mutating call left no trace. Nothing here is
a false claim of a clean stop: a partial cancellation reported as a total one is
exactly the sort of comfort the rest of this design tries not to offer.

### About root inside the desktop

The `sentineldesk` user has **passwordless sudo** and a working `su`. That is
deliberate: the container *is* the sandbox, and the security boundary is the
WebSocket login (`AUTH_USER`/`AUTH_PASS` plus per-IP rate limiting), not
something inside the desktop. Anyone who already holds a graphical session in a
container can read everything that matters to that container; denying them
`apt install` protects nothing while blocking what a real desktop is for.

What still holds:

- The container **is not root on the host**: the real limits are Docker's
  (capabilities, seccomp, mounts, network). That is where to tighten things.
- **Do not mount sensitive host sockets or directories** (`/var/run/docker.sock`,
  `/`, the host's SSH keys) unless you trust whoever will use the desktop.
- The root password comes from `ROOT_PASSWORD`; without it `AUTH_PASS` is reused.
  On an instance published to the internet, set both.

For a hardened deployment, run the container with `--read-only`, `--cap-drop
ALL` and a user without `sudo`: the administration tools then return a clear
error ("this image has no passwordless sudo") instead of failing opaquely.
