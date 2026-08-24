# MCP catalogue changelog

One entry per release that changes the tool catalogue: a tool added, removed,
renamed, or reclassified. The newest entry's tool count is pinned by
`internal/mcp/catalogue_test.go` against the catalogue the binary actually
serves, so this file cannot fall behind by being forgotten — a catalogue change
without an entry here is a failing test, the same way a migration edited after
being applied is a startup failure.

An integrator writing against these tools pins on the count and the entry: the
server reports both in `initialize` (`serverInfo.version` and
`_meta["sentineldesk/catalogue"].tools`), so "I require `system_updates`"
becomes a check against a number instead of a crash against a missing name.

## 137 tools — 2026-08-24

- `type_secret` added: a credential into a field on screen, by ref, without the
  value passing through anything the agent can read.

  The vault already answered this for COMMANDS — `{{secret:name}}` becomes an
  environment variable and the value never enters the command text. A login
  form is the other half, and it could not be answered the same way: the value
  has to become keystrokes, and keystrokes were exactly what an agent could
  only produce by holding the secret in a `type_text` argument. That argument
  is in the conversation, the transcript and the action log, permanently.

  The `ref` is required, and that is the load-bearing half rather than a
  convenience. Typing "the secret" with no target sends it wherever focus
  happens to be, and focus is not a property this tool controls — a dialog that
  stole it between `ui_find` and the call would receive somebody's password.

  Gated on the room's controls and declared `injects` like every other verb
  that drives the keyboard, so nothing about who may type, or who can see it
  happen, is special-cased for secrets.

  A name the vault does not hold is a question rather than a failure, the same
  bargain a command reference already makes: the people here are asked to type
  it, and what they type is not written to disk, not logged, and not returned.

- `ui_find` and `browser_goto` now declare `sentineldesk/timeoutMs` (10s and
  45s). Advisory: it tells a client how long to wait before it is entitled to
  stop waiting, and stopping is not cancelling — the work may still be running
  here. Absent on every other verb, which means unbounded.

## 136 tools — 2026-08-24

- `start_recording` gained `clean` and `window`. No tool added or removed, so
  the count is unchanged — recorded here anyway, because what a recording
  CONTAINS changed and that is the kind of thing an integrator pins on.

  `clean: true` keeps the mouse cursor and the who-is-driving name tags out of
  the take. The two cost different things. The cursor was `show-pointer=true`
  hard-coded in the pipeline, so dropping it changes the file and nothing else.
  The name tags are real windows on one X display — hiding them hides them from
  everybody watching live, for as long as the recording runs — so it is an
  option and not a default, and `stop_recording` puts them back whatever ended
  the recording, including a run that was cut off.

  `window: <id or title>` records one window instead of the screen. It is the
  answer to "something might open in the middle of my recording" that does not
  depend on predicting what: nothing outside that window can be in the frame.
  A title matching more than one window is refused with the list, rather than
  recording whichever the window manager happened to name first.

  Both came from a real take: a video recorded end to end with the agent's
  pointer sitting over the picture for its whole length.


- Added `deliver_recording` (read): hand a recording already on the desktop to
  the browser of whoever is watching.

  `stop_recording` could already deliver, and does by default — but only at the
  moment it stops, and only to whoever is watching then. A recording that ended
  with nobody in a browser stayed on disk with a note naming the file manager,
  which is a person's route and not an agent's, so "download the recording you
  made earlier" had no answer at all.

  Confined to the recordings directory, checked on the resolved path so that
  `..` and a symlink both land outside and are refused: "hand this file to a
  browser", pointed at any path a caller chooses, is an exfiltration tool
  wearing a helpful name.

## 135 tools — 2026-08-24

- Added `browser_wait_until` (read): wait until a JavaScript condition is true
  in the page, and report how long it took.

  It closes a gap that cost a real run its task. `browser_wait_for` waits for an
  element to appear, which is a mutation the page can announce; a CONDITION is
  not. "This video has ended" changes with no DOM event at all, so an agent
  asked to play something and stop when it finishes had only one way to find
  out: read the clock, wait, read it again. Reading is a STEP, so a three-minute
  video cost a dozen steps to learn one fact, and the run was cut off at its
  limit with the video still playing and the recording still running.

  The waiting now happens where the answer is — the page tests the condition on
  its own timer — so it costs one step however long it takes. Capped at ten
  minutes, because it holds a socket and a step open for its duration.

## 134 tools — 2026-08-22

- Added graphical remote desktops, the deliberate counterpart to `ssh_*`: where
  SSH is a headless connection nobody in the room sees, these land the remote
  machine's screen in a window on the shared desktop. Six tools —
  `remote_open`, `remote_close`, `remote_list` (read), and
  `remote_profile_save`, `remote_profile_list` (read), `remote_profile_delete`
  for reusable connection profiles. RDP, VNC and SPICE, Remmina as the primary
  backend with the direct CLIs (`xfreerdp3`, `xtigervncviewer`) as the fallback;
  the password is never put on a command line. `remote_open` and `remote_close`
  require control, because opening or closing someone's machine in front of the
  room is driving the shared screen; the rest do not.

## 128 tools — 2026-08-20

- Removed the virtual gamepad: `gamepad_button`, `gamepad_tap`, `gamepad_axis`
  and `gamepad_state`, with the uinput joystick and the browser's `gp` verb
  behind them — the owner's decision, the feature having found no use. The
  git history keeps the implementation should it ever earn its way back.

## 132 tools — 2026-08-18

- Added `system_updates` (read): pending upgrades from the on-disk apt index,
  security updates counted separately, the build's own version, and the index's
  age. Never refreshes the index.

## 131 tools — baseline

- The catalogue as it stood when this changelog began. Earlier growth is in the
  git history of `internal/mcp/tools*.go`; from here on it is recorded per
  release, in this file.
