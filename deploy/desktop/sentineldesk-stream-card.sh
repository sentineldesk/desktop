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
# The STREAM section of the desktop's system card, formatted for conky's
# execpi from the status mirror the daemon writes (internal/stream/
# streamstatus.go). Two halves on one card: what this desktop is sending
# (viewers, encoder bitrate, delivered fps, the quality position) and what
# each viewer reports it is receiving. When nothing streams, it says so —
# stale numbers dressed as current ones are the bug class this repository
# ranks worst.

F=/tmp/sentineldesk/stream.status

if [ ! -r "$F" ] || grep -q '^offline=1' "$F" 2>/dev/null; then
    printf '${color2}idle — nobody is watching${color}\n'
    exit 0
fi

viewers=$(sed -n 's/^viewers=//p' "$F")
kbps=$(sed -n 's/^sent=//p' "$F")
[ -z "$kbps" ] && kbps=$(sed -n 's/^kbps=//p' "$F")
fps=$(sed -n 's/^fps=//p' "$F")
quality=$(sed -n 's/^quality=//p' "$F")
cap=$(sed -n 's/^cap=//p' "$F")
mbps=$(awk "BEGIN{printf \"%.1f\", ${kbps:-0}/1000}")

printf '${color2}viewers ${color}%s      ${color2}sending ${color}%s Mbps\n' \
    "${viewers:-0}" "$mbps"
printf '${color2}fps ${color}%s ${color2}of %s   quality ${color}%s\n' \
    "${fps:-0}" "${cap:-?}" "${quality:-auto}"

# Each viewer's own report: name, received fps, round trip, loss. The daemon
# puts whoever holds the controls FIRST and flags the row — with several
# people watching, the driver's reception is the one that matters most, so
# it leads and wears the phosphor. At most three rows: the card is a glance,
# not a table; the file holds them all for anyone (the agent included) who
# wants the rest.
grep '^viewer ' "$F" | head -3 | while read -r _ name vfps _vkbps vrtt _vbehind vloss vdrv; do
    if [ "$vdrv" = "1" ]; then
        printf '${color1}%.10s drives${color} %sfps %sms %s‰\n' \
            "$name" "$vfps" "$vrtt" "$vloss"
    else
        printf '${color}%.10s${color2} %sfps %sms %s‰${color}\n' \
            "$name" "$vfps" "$vrtt" "$vloss"
    fi
done
extra=$(grep -c '^viewer ' "$F")
[ "${extra:-0}" -gt 3 ] && printf '${color2}… and %s more${color}\n' "$((extra - 3))"
exit 0
