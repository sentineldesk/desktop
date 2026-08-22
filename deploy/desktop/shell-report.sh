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

# Report every command's exit status, whoever ran it.
#
# Sourced by every interactive shell on the desktop, so the record does not
# depend on who opened the terminal. That symmetry is the point: a person hits an
# error, asks the agent to look at it, and the agent can read what actually
# happened instead of being told about it second-hand.
#
# It writes to a file rather than printing, so nothing changes on screen. The
# alternative — appending `; echo $?` to commands — would put bookkeeping in
# front of the person sharing the session.
#
# $? is captured first and restored last, so a command's status still reaches
# whatever the person types next ($?, ||, &&) exactly as it would have.

# One file per tmux pane, falling back to the shared one outside tmux.
#
# The record is last-writer-wins, so a single shared file makes two busy shells
# indistinguishable: a person running something in a split pane would overwrite
# the status of the command the agent just ran, and the agent would then report
# their exit code as its own. A wrong exit code is worse than none — it is the
# mute-failure class this whole mechanism exists to close.
#
# $TMUX_PANE is the pane's own id ("%3"), exported by tmux into every process it
# starts; the % is dropped because it is awkward in a filename. Outside tmux the
# variable is empty and the path collapses to the original shared one, so a
# terminal opened from the panel menu keeps reporting exactly as before.
# The per-pane file above answers "what just happened here", and it is
# last-writer-wins by design — the agent reads it immediately after typing.
#
# It is a terrible RECORD, and that asymmetry was the gap. Everything the agent
# does lands in an append-only trail with a timestamp; everything a person does
# landed in a one-slot buffer that the next prompt overwrote. So an agent that
# had been stopped and told "look at what I did instead" could see the last
# command and nothing before it, while its own afternoon was on disk in full.
#
# One desktop, one history. This appends the same fact to a shared log that both
# sides read: a person can see what the agent ran, and the agent can see what the
# people here ran, and neither has to take the other's word for it.
#
# Appended with a single printf under O_APPEND, which the kernel keeps atomic
# for a line this short, so two busy shells interleave rather than corrupt. The
# file is trimmed by the reader, not here — a rewrite on every prompt would put
# the cost of housekeeping in front of the person typing.
__sd_activity="/tmp/sentineldesk/shell.log"

# Who is typing, worked out ONCE when this is sourced rather than on every
# prompt. $USER is not set in a non-login shell — which is most of them here,
# since tmux panes and the agent's own terminals are not logins — and the record
# then said "unknown" for the one field an audit trail exists to carry. `id -un`
# always answers; running it per prompt would put a fork in front of every
# command somebody types.
__sd_who=${USER:-$(id -un 2>/dev/null || echo unknown)}

__sd_report() {
    local rc=$?
    local last
    last=$(HISTTIMEFORMAT= history 1 2>/dev/null | sed 's/^ *[0-9]* *//')
    printf '%s\t%s\n' "$rc" "$last" \
        > "/tmp/sentineldesk-rc${TMUX_PANE:+.${TMUX_PANE#%}}" 2>/dev/null

    # An empty command is a bare Return, or the first prompt of a fresh shell
    # reading somebody else's history. Neither is an event.
    if [ -n "$last" ]; then
        mkdir -p "${__sd_activity%/*}" 2>/dev/null
        printf '%s\t%s\t%s\t%s\t%s\n' \
            "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$__sd_who" \
            "${TMUX_PANE:-none}" "$rc" "$last" \
            >> "$__sd_activity" 2>/dev/null
    fi

    return $rc
}

case "$PROMPT_COMMAND" in
    *__sd_report*) ;;                       # already installed
    "")  PROMPT_COMMAND="__sd_report" ;;
    *)   PROMPT_COMMAND="__sd_report; $PROMPT_COMMAND" ;;
esac
export PROMPT_COMMAND

# Root shells started with `su` load root's own bashrc, which does not have
# this. Exporting the function lets `sudo -E su` and `su -p` keep reporting.
export -f __sd_report 2>/dev/null || true
