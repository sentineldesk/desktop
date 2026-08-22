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
