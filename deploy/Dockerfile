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

# SentinelDesk — a Linux desktop streamed to the browser over WebRTC.
#
# Debian 13 (trixie). A single Go binary (Pion WebRTC + go-gst) drives Xvfb,
# Openbox, PulseAudio and GStreamer; the desktop itself is styled after
# Raspberry Pi OS (PIXEL).
#
# Build from the repository root:
#     docker build -f deploy/Dockerfile -t sentineldesk .

# ---------- web client stage ----------
# The browser client is React (app/) and its build output IS the embed
# directory: this stage produces it, the Go stage below copies it over
# internal/webui/assets before compiling, and go:embed does the rest. Node
# exists only here — the running image never carries it.
FROM node:22-trixie-slim AS webapp
WORKDIR /app
COPY app/package.json app/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY app/ ./
RUN npm run build -- --outDir /out --emptyOutDir

# ---------- build stage ----------
FROM golang:1.26-trixie AS build

RUN apt-get update && apt-get install -y --no-install-recommends \
    pkg-config gcc libc6-dev \
    libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev \
    && rm -rf /var/lib/apt/lists/*

# The version travels in as build args and out as -ldflags -X, so the binary
# can always say what it is (`sentineldesk -version`, and the rail's footer).
# The defaults keep a bare `docker build` honest: it IS a development build.
ARG VERSION=0.0.0
ARG GIT_HASH=development
ARG BUILD_DATE=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# internal/ carries the embedded browser client too (internal/webui/assets),
# pkg/ is the distro's public Go surface (the panel compiles against it), and
# deploy/ is embedded so the binary can serve its own installation files.
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/
COPY deploy/ deploy/
# The built client lands over the (git-ignored) embed directory — after
# internal/, so the build output always wins over whatever a host checkout
# happened to hold.
COPY --from=webapp /out/ internal/webui/assets/
RUN go build -trimpath -ldflags="-s -w \
      -X github.com/sentineldesk/desktop/pkg/version.Version=${VERSION} \
      -X github.com/sentineldesk/desktop/pkg/version.GitHash=${GIT_HASH} \
      -X github.com/sentineldesk/desktop/pkg/version.BuildDate=${BUILD_DATE}" \
      -o /sentineldesk ./cmd/sentineldesk

# ---------- bin: just the binary, for buildx --output ----------
# `make release-binaries` builds this target per platform and copies the file
# out. Scratch, so the local export contains exactly one file.
FROM scratch AS bin
COPY --from=build /sentineldesk /sentineldesk

# ---------- runtime: engine ----------
# Kept as its own layer so that changing the desktop applications below does not
# invalidate the (large, slow) engine layer.
FROM debian:trixie-slim AS engine

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    # Virtual X server and utilities (wmctrl backs the MCP window tools)
    xvfb x11-utils x11-xserver-utils xdotool wmctrl xfonts-base \
    # Window manager
    openbox xterm fonts-dejavu-core \
    # The terminal tools' control channel. Named explicitly rather than left to
    # arrive as somebody else's dependency, which is how it got here: terminal_run
    # and terminal_read address the shell through a tmux pane, so a base image
    # that stopped pulling it in transitively would take the desktop's terminal
    # with it — and the failure would look like an empty read, not a missing
    # package.
    tmux \
    # Virtual audio
    pulseaudio pulseaudio-utils \
    # GStreamer: capture and codecs (VP8/H.264/Opus plus the va and nvcodec plugins)
    gstreamer1.0-tools \
    gstreamer1.0-plugins-base \
    gstreamer1.0-plugins-good \
    gstreamer1.0-plugins-bad \
    gstreamer1.0-plugins-ugly \
    gstreamer1.0-pulseaudio \
    gstreamer1.0-libav \
    # VA-API (Intel/AMD). On NVIDIA the drivers come from nvidia-container-toolkit.
    libva2 vainfo mesa-va-drivers \
    # Administration utilities
    mc screen vim htop \
    # SSH client (ssh-keygen backs the ssh_* tools)
    openssh-client \
    # FTP/SFTP client
    filezilla \
    # Two-way clipboard (browser <-> desktop)
    xclip \
    # OCR for the read_screen_text / find_text tools, in the three languages the
    # interface itself ships. A language pack does not make OCR good at reading a
    # desktop — that is a different problem, and read_screen_text says so — but
    # without one an error dialog in Portuguese comes back as noise.
    tesseract-ocr tesseract-ocr-eng tesseract-ocr-spa tesseract-ocr-por \
    # gdk-pixbuf SVG loader. Without it GTK cannot use SVG icon themes (Papirus).
    librsvg2-common \
    # System D-Bus. Chromium's CEF uses it to detect the network: without it
    # Steam sits forever on "Waiting for network…" even with connectivity.
    dbus dbus-x11 \
    # AT-SPI: the accessibility tree. This is what lets the ui_* tools KNOW what
    # is on screen (roles, names, states) and invoke actions directly, instead of
    # groping around with screenshots and coordinate clicks.
    # libatk-adaptor is essential: without it GTK apps never register.
    at-spi2-core libatk-adaptor python3-pyatspi gir1.2-atspi-2.0 \
    # Process supervision
    supervisor procps ca-certificates \
    # Privilege escalation: sudo/su for the terminal, polkit so graphical
    # administration tools can request permission instead of failing.
    sudo polkitd pkexec \
    && if [ "$(dpkg --print-architecture)" = "amd64" ]; then \
         apt-get install -y --no-install-recommends intel-media-va-driver; \
       fi \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -m -u 1000 -s /bin/bash sentineldesk \
    && (usermod -aG video,render sentineldesk || usermod -aG video sentineldesk)

# The three directories the record lives in, created HERE and owned by the
# desktop user — which is the whole reason this RUN exists rather than letting
# the daemon make them at first use.
#
# Docker seeds a fresh named volume from whatever is at the mount point in the
# image, ownership included. With nothing there it creates the directory itself,
# root-owned, and the desktop runs as uid 1000: the action log would fall back
# to its in-memory ring (silently, by design) and every job would fail trying to
# write its two streams. Both are volumes now, so the image has to hold the
# answer to "who owns this" before the first one is created.
RUN install -d -o sentineldesk -g sentineldesk -m 0755 \
      /var/log/sentineldesk \
      /tmp/sentineldesk \
      /home/sentineldesk/Recordings

# --- Privileges --------------------------------------------------------------
# The container IS the sandbox: the security boundary is the WebSocket login,
# not the inside of the desktop. Blocking root in here protects nothing and does
# prevent what anyone expects from a real desktop — installing a package,
# restarting a service, editing /etc. So `sentineldesk` gets passwordless sudo,
# and `su` works with the password the entrypoint sets.
RUN usermod -aG sudo sentineldesk \
    # SETENV matters: without that tag `sudo -E` is rejected, and a graphical app
    # launched as root loses DISPLAY / PULSE_SERVER and never starts.
    && printf 'sentineldesk ALL=(ALL:ALL) NOPASSWD:SETENV: ALL\nDefaults:sentineldesk !requiretty\nDefaults env_keep += "DISPLAY XAUTHORITY XDG_RUNTIME_DIR PULSE_SERVER DBUS_SESSION_BUS_ADDRESS"\n' \
         > /etc/sudoers.d/010-sentineldesk \
    && chmod 0440 /etc/sudoers.d/010-sentineldesk \
    && visudo -cf /etc/sudoers.d/010-sentineldesk

# polkit: let graphical administration tools (gparted, synaptic, a file manager
# running as root) obtain permission instead of opening a dialog nobody can
# answer — this desktop has no authentication agent.
COPY deploy/config/polkit.rules /etc/polkit-1/rules.d/49-sentineldesk.rules

# NOTE: the sentineldesk binary is NOT copied here. See the end of the desktop
# stage — it changes on every commit, and anything below it in the file would be
# rebuilt with it.
COPY deploy/config/pulse-daemon.pa /etc/pulse/sentineldesk.pa
COPY deploy/config/pulse-client.conf /etc/pulse/client.conf.d/sentineldesk.conf
COPY deploy/config/entrypoint.sh \
     deploy/config/wait-x11.sh \
     deploy/config/wait-wm.sh \
     deploy/config/a11y.py \
     /usr/local/bin/
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/wait-x11.sh \
             /usr/local/bin/wait-wm.sh /usr/local/bin/a11y.py

# ---------- runtime: desktop ----------
FROM engine AS desktop

ENV DEBIAN_FRONTEND=noninteractive

# The desktop and the tools that make it worth sitting at.
#
# WHAT gets installed lives in deploy/packages/, not here. Dockerfile has no
# include, and a forty-line RUN with the reasoning wedged into shell comments is
# a list nobody edits twice. A plain text file per variant, one package per
# line, comments where the choice needs defending — and this instruction only
# has to know how to read one.
#
# "Lite" does not mean headless. The desktop IS the product; lite means the
# smallest set that leaves nobody reaching for apt on their first afternoon.
COPY deploy/packages/desktop.txt /tmp/packages/desktop.txt
RUN apt-get update \
    && sed -e 's/#.*//' -e '/^[[:space:]]*$/d' /tmp/packages/desktop.txt \
       | xargs apt-get install -y --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*

# The default terminal, pinned to the one that actually works here. Debian's
# `x-terminal-emulator` alternative auto-selects lxterm (an xterm wrapper),
# and in this container xterm/lxterm start and exit immediately without ever
# mapping a window — so a console app (mc, htop, vim) launched through ANY
# path that shells out to x-terminal-emulator opened nothing at all. The
# XFCE whisker menu dodged it because it launches Terminal=true apps through
# its own exo helper (lxterminal, which works), but every other launcher —
# GTK apps, "open a terminal here", scripts — went through the broken default.
# Point the alternative at lxterminal so all of them agree on the terminal
# the desktop already uses.
RUN update-alternatives --set x-terminal-emulator /usr/bin/lxterminal

# The whole theme directory, not just the themerc: the frame buttons are .xbm
# masks that live beside it, and a missing mask is the quietest kind of failure
# — Openbox falls back to its built-in hairlines and draws something, so the
# window still has buttons and they are simply the wrong ones.
COPY deploy/desktop/openbox-theme/ /usr/share/themes/SentinelDesk/openbox-3/
COPY deploy/config/supervisord.conf /etc/supervisor/supervisord.conf
COPY deploy/desktop/desktop-init.sh /usr/local/bin/desktop-init.sh
COPY deploy/desktop/sentineldesk-hint deploy/desktop/openvpn-connect /usr/local/bin/
COPY deploy/desktop/chromium-flags.conf /etc/chromium.d/90-sentineldesk
# GTK3/4 read system settings from the XDG paths (/etc/xdg/gtk-*), not /etc/gtk-*
COPY deploy/desktop/gtk-settings.ini /etc/xdg/gtk-3.0/settings.ini
COPY deploy/desktop/gtk-settings.ini /etc/xdg/gtk-4.0/settings.ini
COPY deploy/desktop/gtkrc-2.0 /etc/gtk-2.0/gtkrc
COPY deploy/desktop/wallpaper.svg /tmp/wallpaper.svg

# Per-user configuration lives in /etc/skel.sentineldesk; desktop-init.sh syncs
# it into the home on every boot, because the home is a persistent volume.
# Exit-status reporting for EVERY interactive shell, not only the ones the agent
# opens. Dropped into profile.d and sourced from the user's bashrc, so a person's
# failed command is as readable to the agent as its own.
COPY deploy/desktop/shell-report.sh /etc/profile.d/99-sentineldesk-report.sh
# The shell's factory configuration (xfce4-panel + xfdesktop since
# 2026-08-19). The panel and the backdrops read xfconf, whose per-channel XML
# lands in the skeleton; genmon and the launchers are the two pieces that
# read plain files beside it instead — genmon its rc, each launcher the
# desktop entries in its own launcher-<id>/ directory, ids matching
# xfce4-panel.xml. galculator's entry is copied out of the package below,
# where the RUN can see the installed file.
COPY deploy/desktop/xfce4-panel.xml   /etc/skel.sentineldesk/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-panel.xml
COPY deploy/desktop/xfce4-desktop.xml /etc/skel.sentineldesk/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-desktop.xml
COPY deploy/desktop/thunar.xml        /etc/skel.sentineldesk/.config/xfce4/xfconf/xfce-perchannel-xml/thunar.xml
COPY deploy/desktop/genmon-sentineldesk.rc /etc/skel.sentineldesk/.config/xfce4/panel/genmon-19.rc
COPY deploy/desktop/launchers/sentineldesk-browser.desktop  /etc/skel.sentineldesk/.config/xfce4/panel/launcher-9/
COPY deploy/desktop/launchers/sentineldesk-files.desktop    /etc/skel.sentineldesk/.config/xfce4/panel/launcher-10/
COPY deploy/desktop/launchers/sentineldesk-terminal.desktop /etc/skel.sentineldesk/.config/xfce4/panel/launcher-11/
COPY deploy/desktop/conky.conf /etc/skel.sentineldesk/.config/conky/conky.conf
COPY deploy/desktop/conky-stream.conf /etc/skel.sentineldesk/.config/conky/conky-stream.conf
# Preferred applications: Debian ships the helpers directory empty, and with
# no candidates every "open the preferred X" — exo-open, the CPU graph's
# click — dies with "Failed to launch preferred application". The helpers are
# the candidates; the skeleton's helpers.rc is the per-room choice.
COPY deploy/desktop/helpers/ /usr/share/xfce4/helpers/
COPY deploy/desktop/helpers.rc /etc/skel.sentineldesk/.config/xfce4/helpers.rc
# The sticky note, pre-seeded. xpad greets an empty config with its full-page
# help window — which every fresh room would open on its shared screen — and
# a config that already holds one pad is what turns that off. The seed pad
# doubles as the widget's own documentation: it says where notes persist and
# that the agent can read them. The names are xpad's own convention
# (content-XXXX referenced from info-XXXX).
COPY deploy/desktop/xpad-default-style /etc/skel.sentineldesk/.config/xpad/default-style
COPY deploy/desktop/xpad-note-content  /etc/skel.sentineldesk/.config/xpad/content-SENTNL
COPY deploy/desktop/xpad-note-info     /etc/skel.sentineldesk/.config/xpad/info-SENTNL
# The file manager's sidebar favorites, seeded like the note: Thunar reads
# GTK's bookmarks file, and each XDG folder wears its own Papirus glyph — the
# arrow on Downloads, the photo on Pictures — so the Places pane opens
# looking like a sidebar instead of a stub. Ctrl+D adds more, and additions
# persist in the room's home. Deliberately the SHARED GTK file, not a
# Thunar-private store: any GTK application's file dialog shows the same
# sidebar, so "where things are" is one answer everywhere.
COPY deploy/desktop/gtk-bookmarks /etc/skel.sentineldesk/.config/gtk-3.0/bookmarks
COPY deploy/desktop/sentineldesk-logo.svg /usr/share/pixmaps/sentineldesk.svg
COPY deploy/desktop/sentineldesk-status.sh /usr/local/bin/sentineldesk-status.sh
COPY deploy/desktop/sentineldesk-stream-card.sh /usr/local/bin/sentineldesk-stream-card.sh
COPY deploy/desktop/conky-spark.lua /usr/local/bin/conky-spark.lua
COPY deploy/desktop/make-workspace-wallpapers.py /tmp/make-workspace-wallpapers.py
# The panel's launchers, as real desktop entries, installed where libfm
# TRUSTS them. Both halves were learned the hard way. `--chmod=0755` because
# libfm will not trust a launcher without the executable bit. And
# /usr/share/applications — a standard XDG applications directory — because
# the executable bit alone is not enough: the first fix shipped these to a
# directory of our own, valid and 0755, and clicking the freshly rebuilt
# panel produced the same "seems to be an executable script" dialog the files
# exist to end. They go in the IMAGE and not in skel, because they are not
# per-user configuration — nobody should be able to end up with a stale copy
# of them in a persistent home.
COPY --chmod=0755 deploy/desktop/launchers/ /usr/share/applications/
COPY deploy/desktop/lxterminal.conf /etc/skel.sentineldesk/.config/lxterminal/lxterminal.conf
COPY deploy/desktop/rofi.rasi /etc/skel.sentineldesk/.config/rofi/config.rasi
COPY deploy/desktop/tmux.conf /etc/tmux.conf
# The supervised runner. Every command the agent runs goes through this, in a
# tmux window people can watch, with its two streams kept apart on disk.
COPY deploy/desktop/job-run.sh /usr/local/bin/job-run.sh
COPY deploy/desktop/gtk-settings.ini /etc/skel.sentineldesk/.config/gtk-3.0/settings.ini
COPY deploy/desktop/gtk.css /etc/skel.sentineldesk/.config/gtk-3.0/gtk.css
COPY deploy/desktop/gtk.css /etc/skel.sentineldesk/.config/gtk-4.0/gtk.css
COPY deploy/desktop/gtkrc-2.0 /etc/skel.sentineldesk/.gtkrc-2.0

# --- our own binary, last ----------------------------------------------------
#
# Deliberately the final layer of this stage, and it is worth saying why: the
# operating system underneath changes about once a month, and this changes on
# every commit. Docker invalidates a layer and everything after it, so putting
# the binary early — where it used to be, up in the engine stage — meant that
# editing one line of Go threw away a gigabyte of installed packages and
# reinstalled them.
#
# Same reasoning, opposite direction, as the package lists sitting in their own
# files: the slow, stable thing goes underneath; the fast, volatile thing goes
# on top.
COPY --from=build /sentineldesk /usr/local/bin/sentineldesk

RUN chmod +x /usr/local/bin/desktop-init.sh \
    /usr/local/bin/job-run.sh /usr/local/bin/sentineldesk-status.sh \
    /usr/local/bin/sentineldesk-stream-card.sh \
             /usr/local/bin/sentineldesk-hint /usr/local/bin/openvpn-connect \
    # Wallpapers: rendered SVG -> PNG at build time, no external downloads.
    # The plain sentineldesk.png stays for anything that still names it; the
    # per-workspace set under sentineldesk/ is what xfdesktop actually hangs —
    # four variants of the same design, derived from the one source SVG so the
    # base design cannot drift from its variants.
    && mkdir -p /usr/share/backgrounds/sentineldesk /wallpaper \
    && rsvg-convert -w 1920 -h 1080 -o /usr/share/backgrounds/sentineldesk.png /tmp/wallpaper.svg \
    && python3 /tmp/make-workspace-wallpapers.py /tmp/wallpaper.svg /tmp/ws-svg \
    && for n in 1 2 3 4; do \
         rsvg-convert -w 1920 -h 1080 -o /usr/share/backgrounds/sentineldesk/ws$n.png /tmp/ws-svg/ws$n.svg; \
       done \
    && rm -r /tmp/wallpaper.svg /tmp/ws-svg /tmp/make-workspace-wallpapers.py \
    # The brand glyph, rasterised: the panel's menu button names the PNG by
    # path because that is the variant that verifiably renders (the SVG by
    # path drew the broken-image placeholder, and the hicolor name resolved
    # to nothing while stock names resolved fine). The hicolor copies stay
    # for every other consumer that asks for "sentineldesk" by name.
    && rsvg-convert -w 48 -h 48 -o /usr/share/pixmaps/sentineldesk.png /usr/share/pixmaps/sentineldesk.svg \
    && mkdir -p /usr/share/icons/hicolor/scalable/apps /usr/share/icons/hicolor/48x48/apps /usr/share/icons/hicolor/24x24/apps \
    && cp /usr/share/pixmaps/sentineldesk.svg /usr/share/icons/hicolor/scalable/apps/ \
    && cp /usr/share/pixmaps/sentineldesk.png /usr/share/icons/hicolor/48x48/apps/ \
    && rsvg-convert -w 24 -h 24 -o /usr/share/icons/hicolor/24x24/apps/sentineldesk.png /usr/share/pixmaps/sentineldesk.svg \
    # Folder accent: cyan, across every Papirus variant and size. The theme
    # ships every colour and points the default names at blue with symlinks;
    # re-pointing them is exactly what upstream's papirus-folders tool does,
    # done here inline because Debian does not package the tool and a build
    # must not download one.
    && for d in /usr/share/icons/Papirus*/*/places; do \
         for l in "$d"/*.svg; do \
           [ -L "$l" ] || continue; \
           t=$(readlink "$l"); \
           case "$t" in folder-blue*) c="folder-cyan${t#folder-blue}"; \
             [ -e "$d/$c" ] && ln -sf "$c" "$l";; esac; \
         done; \
       done \
    && gtk-update-icon-cache -f /usr/share/icons/Papirus 2>/dev/null; \
       gtk-update-icon-cache -f /usr/share/icons/Papirus-Dark 2>/dev/null; \
       gtk-update-icon-cache -f /usr/share/icons/hicolor \
    # The panel's calculator launcher names galculator's own desktop entry;
    # the launcher plugin reads it from its private directory, so the copy
    # happens here where the installed file exists — and unlike its three
    # siblings, no COPY instruction created this directory on the way in.
    && mkdir -p /etc/skel.sentineldesk/.config/xfce4/panel/launcher-17 \
    && cp /usr/share/applications/galculator.desktop /etc/skel.sentineldesk/.config/xfce4/panel/launcher-17/ \
    # Workspaces named 1-4: the names are what the pager's buttons display,
    # and plain numerals are the whole point of buttons view.
    && sed -i 's|<names>|<names><name>1</name><name>2</name><name>3</name><name>4</name>|' /etc/xdg/openbox/rc.xml \
    # Select the theme in Openbox's global configuration
    && sed -i 's|<name>Clearlooks</name>|<name>SentinelDesk</name>|' /etc/xdg/openbox/rc.xml \
    # Buttons to the RIGHT of a left-hand title — the browser's own layout,
    # adopted for every window (2026-08-20) so the frames and Chromium agree
    # on where close lives. The letters are Openbox's: L label, I iconify,
    # M maximise, C close; the window-icon N stays dropped because it puts an
    # application's artwork where this design wants nothing. The glyphs and
    # the hover bubbles live in the theme (openbox-theme/themerc).
    && sed -i 's|<titleLayout>[^<]*</titleLayout>|<titleLayout>LIMC</titleLayout>|' /etc/xdg/openbox/rc.xml \
    # Title bars in the same monospace as the control layer. Matched by place
    # because rc.xml carries several <font> blocks and only these two draw a
    # window title.
    && python3 -c "import re; p='/etc/xdg/openbox/rc.xml'; s=open(p).read(); s=re.sub(r'(<font place=\"(?:Active|Inactive)Window\">.*?<name>)[^<]*(</name>.*?<size>)[^<]*(</size>.*?<weight>)[^<]*(</weight>)', r'\g<1>monospace\g<2>9\g<3>normal\g<4>', s, flags=re.S); open(p,'w').write(s)" \
    # The launcher's key. W-space is the natural chord and stays, but it is not
    # the only one: a macOS guest's browser never sees Cmd+Space (Spotlight eats
    # it before the page can), so A-F2 — historically "show me a run dialog" —
    # is the chord that works from every keyboard that can reach this desktop.
    && python3 -c "import re; p='/etc/xdg/openbox/rc.xml'; s=open(p).read(); k='<keybind key=\"W-space\"><action name=\"Execute\"><command>rofi -show drun</command></action></keybind><keybind key=\"A-F2\"><action name=\"Execute\"><command>rofi -show drun</command></action></keybind>'; s=s.replace('</keyboard>', k+'</keyboard>', 1); open(p,'w').write(s)" \
    && fc-cache -f >/dev/null \
    # Hide the menu entries that open nothing or duplicate what the desktop
    # already offers, so "All Applications" holds only things that work:
    #   rofi, rofi-theme-selector — rofi is a dmenu-style launcher bound to
    #     Super+Space; its own menu entry only duplicates the Whisker menu, and
    #     the theme selector is an interactive text tool marked Terminal=false,
    #     so clicking it runs headless and shows nothing. rofi stays installed
    #     for the keybind; it just leaves the applications list.
    #   xfreerdp3, xtigervncviewer — raw CLI clients that launch with no server
    #     and quit. Remmina is the client the menu points at; the CLIs stay on
    #     PATH for scripting. (xfreerdp3-file, the rdp: scheme handler, already
    #     ships NoDisplay=true and carries a malformed Actions group that
    #     desktop-file-edit refuses to rewrite — leaving it alone is correct.)
    # NoDisplay rather than deletion: the files are package-owned, and a future
    # apt operation would notice a missing conffile — a hidden entry is the
    # change that survives.
    && for e in rofi rofi-theme-selector xfreerdp3 xtigervncviewer; do \
         f=/usr/share/applications/$e.desktop; \
         [ -f "$f" ] && desktop-file-edit --set-key=NoDisplay --set-value=true "$f"; \
       done \
    # The MIME-type -> application map. Built here rather than left to a package
    # trigger, because --no-install-recommends means nothing else runs it.
    && update-desktop-database /usr/share/applications

# A mountable home for the MCP socket. Its default is the user's XDG runtime
# dir (MCP_SOCK below), which is private to the container. Relocating it here
# with `-e MCP_SOCK=/run/sentineldesk/mcp.sock` and mounting a volume over
# /run/sentineldesk is what lets an agent on the HOST — Claude Code, or
# sentineldesk-agent — reach it directly, no `docker exec`. Created owned by the
# desktop user so the daemon can bind the socket in it; a fresh named volume
# mounted here inherits that ownership through Docker's copy-up.
RUN mkdir -p /run/sentineldesk && chown sentineldesk:sentineldesk /run/sentineldesk

# Defaults; override from docker-compose or -e
#
# LANG/LC_ALL matter more than they look: without them every process on the
# desktop runs under the POSIX locale, and a terminal in POSIX mode converts
# pasted text to Latin-1 — an ñ typed through the virtual keyboard landed in
# the shell as the byte 0xF1. C.UTF-8 ships with Debian's libc, needs no
# locales package, and supervisord hands it down to everything it starts.
ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    DISPLAY=:0 \
    DISPLAY_WIDTH=1920 \
    DISPLAY_HEIGHT=1080 \
    FPS=30 \
    ENCODER=auto \
    VIDEO_BITRATE_KBPS=4000 \
    AUDIO_BITRATE=96000 \
    HTTP_PORT=8080 \
    STUN_SERVER=stun:stun.l.google.com:19302 \
    CLIENT_STUN=stun:stun.l.google.com:19302 \
    MCP_SOCK=/run/user/1000/sentineldesk-mcp.sock \
    NVIDIA_DRIVER_CAPABILITIES=all

EXPOSE 8080
# The embedded STUN responder (STUN_EMBEDDED, on by default).
EXPOSE 3478/udp

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# ---------- runtime: full ----------
#
# Everything lite has, plus what is too large or too niche to hand somebody who
# only wanted a desktop. Built FROM desktop rather than beside it, so the two
# images share every layer up to this point: a registry holding both stores the
# difference once, and a machine that has pulled lite pulls only the extra.
#
# Steam is what forces the awkward part — contrib/non-free components, i386
# multiarch and a licence accepted in advance — so that setup lives here rather
# than in the base everybody pays for.
FROM desktop AS full

ENV DEBIAN_FRONTEND=noninteractive

COPY deploy/packages/full.txt deploy/packages/full-amd64.txt /tmp/packages/

RUN sed -i 's/^Components: .*/Components: main contrib non-free non-free-firmware/' \
        /etc/apt/sources.list.d/debian.sources \
    && lists="/tmp/packages/full.txt" \
    && if [ "$(dpkg --print-architecture)" = "amd64" ]; then \
         dpkg --add-architecture i386 \
         && echo "steam steam/question select I AGREE" | debconf-set-selections \
         && echo "steam steam/license note ''" | debconf-set-selections \
         && lists="$lists /tmp/packages/full-amd64.txt"; \
       fi \
    && apt-get update \
    && sed -e 's/#.*//' -e '/^[[:space:]]*$/d' $lists \
       | xargs apt-get install -y --no-install-recommends \
    && if [ "$(dpkg --print-architecture)" = "amd64" ]; then \
         apt-get install -y --no-install-recommends \
           libgl1:i386 libgl1-mesa-dri:i386 mesa-vulkan-drivers:i386; \
       fi \
    && rm -rf /var/lib/apt/lists/*
