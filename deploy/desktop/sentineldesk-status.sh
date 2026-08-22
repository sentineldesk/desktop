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
#
# The panel's SentinelDesk status line, run by genmon every two seconds. This
# is the panel speaking the product's own language: is a recording running,
# and is the agent doing anything right now. No other distro can put these two
# facts in a panel because no other distro has them.
#
# Everything here is read from observable state — a pgrep and a directory
# listing — never invented. The recording pipeline is a `gst-launch-1.0` child
# whose argv carries the recordings directory (internal/media/recording.go);
# a running agent job is a directory under /tmp/sentineldesk/jobs that
# job-run.sh has started (`started` exists) and not finished (`rc` does not).
# What this script cannot observe honestly, it does not display.
#
# Output is genmon's dialect: one <txt> line for the panel, one <tool> line
# for the hover. Pango markup inside <txt> carries the palette.

REC_DIR="${RECORDINGS_DIR:-$HOME/Recordings}"

if pgrep -f "gst-launch-1\.0.*${REC_DIR}" >/dev/null 2>&1; then
    rec="<span foreground='#e05252' weight='bold'>● REC</span>"
    rec_tip="recording ON"
else
    rec="<span foreground='#5c6864'>○ rec</span>"
    rec_tip="not recording"
fi

jobs=0
for d in /tmp/sentineldesk/jobs/*/; do
    [ -f "${d}started" ] || continue
    [ -f "${d}rc" ] && continue
    jobs=$((jobs + 1))
done

if [ "$jobs" -gt 0 ]; then
    ag="<span foreground='#3fd68c' weight='bold'>agent</span> ${jobs} job$([ "$jobs" -gt 1 ] && printf s)"
    ag_tip="the agent is running ${jobs} job(s) — watch the tmux pane on screen"
else
    ag="<span foreground='#5c6864'>agent idle</span>"
    ag_tip="the agent is running nothing"
fi

printf "<txt> %s <span foreground='#5c6864'>|</span> %s </txt>\n" "$rec" "$ag"
printf "<tool>SentinelDesk — %s · %s</tool>\n" "$rec_tip" "$ag_tip"
