#!/bin/sh
# SentinelDesk
# A collaborative operating system for people and AI agents.
#
# Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
#
# Licensed under the Apache License, Version 2.0.
#
# This product's name and logo are trademarks of Federico Pereira and are not
# covered by the license above. See the README for the trademark policy.
#
# SPDX-License-Identifier: Apache-2.0
# Initial desktop setup, run once at start.

# The image's user configuration. The home is a persistent volume, so this is
# resynchronised on every start — otherwise an old copy would stay frozen there
# and image updates would never reach an existing installation.
#
# That overwrite is only safe in a home the desktop owns. A native install can
# be pointed at somebody's existing account, and there this would replace their
# lxpanel, lxterminal and GTK settings with the defaults on every boot: not a
# resync, a nightly reset of a machine they configured. DESKTOP_OWN_HOME says
# which case this is — the installer sets it when it CREATED the account, and
# leaves it unset when it was handed one.
#
# Unset, the copy still runs but with -n, so files that are not there yet are
# filled in and files the person already has are left exactly alone.
if [ -d /etc/skel.sentineldesk ]; then
    if [ "${DESKTOP_OWN_HOME:-1}" = 1 ]; then
        cp -r /etc/skel.sentineldesk/. "$HOME/" 2>/dev/null || true
    else
        cp -rn /etc/skel.sentineldesk/. "$HOME/" 2>/dev/null || true
    fi
fi

# Sweep up what lxpanel used to leave behind.
#
# The panel's launchbar named commands inline, which lxpanel could not persist:
# at every start it invented a desktop entry per button, gave it a random name
# and mode 0600, and wrote it into ~/.local/share/applications. Three per boot,
# into a home that is a persistent volume — thirty-nine of them within three days
# on the machine where this was found, and nothing would ever have removed one.
#
# The panel now names real files shipped in the image, so lxpanel invents
# nothing and this finds nothing. It stays because the homes that already
# collected them are still out there, and an installation that fixes itself on
# the next boot is worth more than a note in a changelog nobody reads.
rm -f "$HOME"/.local/share/applications/lxpanel-launcher-*.desktop 2>/dev/null || true

# Say it now if the record has nowhere to go.
#
# These three are volumes. The two named ones inherit their ownership from the
# image, which is why the Dockerfile creates them owned by this user — but
# Recordings is a BIND, and a bind takes the host directory's ownership
# whatever the image says. On a Linux host whose ./recordings belongs to some
# other uid, this desktop cannot write there.
#
# The failure without this check arrives much later and in the wrong place: a
# recording that starts, reports a job id, and produces no file; an action log
# that quietly falls back to its in-memory ring; a job whose two streams cannot
# be opened. All three are the shape this project ranks above a crash — an
# action with no outcome and nothing explaining why — so the complaint belongs
# at boot, where somebody is reading the log for exactly this reason.
for d in /var/log/sentineldesk /tmp/sentineldesk "$HOME/Recordings"; do
    [ -d "$d" ] || mkdir -p "$d" 2>/dev/null || true
    if ! [ -w "$d" ]; then
        printf '!! %s is not writable by %s.\n' "$d" "$(id -un)" >&2
        printf '   Nothing written there will survive, and the tools that use it\n' >&2
        printf '   will report success and produce no file. If this is a bind mount,\n' >&2
        printf '   chown it to uid %s on the host.\n' "$(id -u)" >&2
    fi
done

# --- Keyboard layout ---------------------------------------------------------
#
# X decides which key produces which character, and it defaults to US. On a
# Spanish keyboard that turns ñ, á and the whole punctuation row into something
# else — and the person typing has no way to tell it is the server's fault.
#
# KEYBOARD_LAYOUT takes an X layout code — us, es (Spain), latam (Latin
# America), pt, fr, de… — or a comma list of them: the default is "us,es",
# because this desktop ships its interface in English, Spanish and
# Portuguese and a bilingual person should not need an environment variable
# to type an ñ. With more than one layout the panel's keyboard indicator
# lists them all and Alt+Shift cycles, the way a real desktop switches.
# KEYBOARD_VARIANT is optional and passed through untouched (with a comma
# list it is positional, one variant per layout, exactly as setxkbmap takes
# it).
#
# Applied to the running X server rather than baked in, so one image serves
# every keyboard, and reported either way: a layout that silently failed to
# apply is the kind of thing people spend an afternoon on.
LAYOUT="${KEYBOARD_LAYOUT:-us,es}"
case "$LAYOUT" in
    *,*) GRP_TOGGLE="grp:alt_shift_toggle" ;;
    *)   GRP_TOGGLE="" ;;
esac
if [ -n "$LAYOUT" ]; then
    if setxkbmap -layout "$LAYOUT" ${GRP_TOGGLE:+-option "$GRP_TOGGLE"} ${KEYBOARD_VARIANT:+-variant "$KEYBOARD_VARIANT"} 2>/dev/null; then
        echo "sentineldesk: keyboard $LAYOUT${KEYBOARD_VARIANT:+ ($KEYBOARD_VARIANT)}${GRP_TOGGLE:+ — Alt+Shift switches}"
    else
        echo "sentineldesk: could not apply keyboard layout '$LAYOUT' — staying on us" >&2
        setxkbmap -layout us 2>/dev/null || true
    fi
fi

# --- The XDG user directories ------------------------------------------------
#
# Debian creates these from xdg-user-dirs on a real first login, through PAM.
# Nothing here logs in — supervisord starts the session directly — so the home
# came up without them, and pcmanfm and lxpanel each said so on every boot:
#
#   The directory '~/Templates' doesn't exist, ignoring it
#
# Which is fair enough: "New file from template" is a right-click menu entry
# with nowhere to read templates from. Creating the directories is both the fix
# for the warning and the fix for the missing feature, and it costs eight empty
# directories in a home that already persists.
#
# mkdir -p, so a home carried over from an older container keeps whatever the
# person put in these, and one that already has them is left alone.
for d in Desktop Documents Downloads Music Pictures Public Templates Videos; do
    mkdir -p "$HOME/$d" 2>/dev/null || true
done

# Browser locks left by the previous container. The home persists but the
# hostname changes, so Chromium and Firefox conclude the profile is open "on
# another computer" and refuse to start.
rm -f "$HOME/.config/chromium/Singleton"* 2>/dev/null
rm -f "$HOME"/.mozilla/firefox/*/lock "$HOME"/.mozilla/firefox/*/.parentlock 2>/dev/null

# Exit-status reporting in interactive shells. /etc/profile.d only covers login
# shells and a terminal emulator opens a non-login one, so the hook has to be
# named from .bashrc as well. Appended once, and left alone afterwards.
if [ -f /etc/profile.d/99-sentineldesk-report.sh ] &&
   ! grep -q 99-sentineldesk-report "$HOME/.bashrc" 2>/dev/null; then
    printf '\n. /etc/profile.d/99-sentineldesk-report.sh\n' >> "$HOME/.bashrc"
fi

until xdpyinfo -display "${DISPLAY:-:0}" >/dev/null 2>&1; do
    sleep 0.2
done

# Wallpaper: $WALLPAPER wins, then the mounted ./wallpaper/, then the built-in
# per-workspace set. The window manager has nothing to do with this — Openbox
# draws frames, not the desktop. xfdesktop owns the root window now, and it is
# the reason the built-in default is four images, not one: it can hang a
# different wallpaper on each workspace, which pcmanfm's desktop mode never
# could.
#
# The backdrop is keyed on the RandR output name. The skeleton's
# xfce4-desktop.xml hardcodes Xvfb's "screen"; asking the running server keeps
# this correct even if that ever changes. xfconf-query talks to xfconfd over
# the session bus — supervisord hands this program the bus address — and
# xfdesktop picks the properties up live, so ordering against its start does
# not matter.
mon=$(xrandr --query 2>/dev/null | awk '/ connected/{print $1; exit}')
BASE="/backdrop/screen0/monitor${mon:-screen}"

set_all_workspaces() {
    for i in 0 1 2 3; do
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/last-image" -n -t string -s "$1"
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/image-style" -n -t int -s 5
    done
}

# A deployment brings its own images through $WALLPAPER (one file, pinned
# everywhere) or /wallpaper (a mounted pool). The pool is dealt across the
# four workspaces once, here, at start — there used to be a rotation timer
# doing this on a loop, retired as a distraction: a desktop that changes its
# own face mid-session reads as glitching, not as decorated.
override=""
if [ -n "$WALLPAPER" ] && [ -f "$WALLPAPER" ]; then
    override="$WALLPAPER"
fi

pool=""
count=0
for f in /wallpaper/*.png /wallpaper/*.jpg /wallpaper/*.jpeg /wallpaper/*.webp; do
    [ -f "$f" ] || continue
    pool="$pool$f
"
    count=$((count + 1))
done

if [ -n "$override" ]; then
    set_all_workspaces "$override"
    echo "wallpaper: $override"
elif [ "$count" -ge 2 ]; then
    i=0
    printf '%s' "$pool" | while IFS= read -r f; do
        [ "$i" -ge 4 ] && break
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/last-image" -n -t string -s "$f"
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/image-style" -n -t int -s 5
        i=$((i + 1))
    done
    # Fewer images than workspaces: the remaining ones wrap to the first.
    i="$count"
    first=$(printf '%s' "$pool" | head -1)
    while [ "$i" -lt 4 ]; do
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/last-image" -n -t string -s "$first"
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/image-style" -n -t int -s 5
        i=$((i + 1))
    done
    echo "wallpaper: $count images dealt across the workspaces"
elif [ "$count" -eq 1 ]; then
    set_all_workspaces "$(printf '%s' "$pool" | head -1)"
    echo "wallpaper: $(printf '%s' "$pool" | head -1)"
else
    # The built-in set. The skeleton config already names these; restating
    # them against the real monitor name is what makes the fallback in
    # xfce4-desktop.xml's comment a guarantee instead of a hope.
    for i in 0 1 2 3; do
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/last-image" -n -t string \
            -s "/usr/share/backgrounds/sentineldesk/ws$((i+1)).png"
        xfconf-query -c xfce4-desktop -p "$BASE/workspace$i/image-style" -n -t int -s 5
    done
    xfconf-query -c xfce4-desktop -p /backdrop/single-workspace-mode -n -t bool -s false
    echo "wallpaper: built-in per-workspace set"
fi
exit 0
