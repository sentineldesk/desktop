#!/bin/bash
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
# job-run.sh — run one command where a person can watch it happen.
#
# usage: job-run.sh <id> <shell command>
#
# The agent never runs anything the people sharing this desktop cannot see. That
# is not a preference, it is the shape of the product: a supervisor who cannot
# observe an action cannot judge it, and cannot decide to stop it. So every
# command the agent runs arrives here, in a tmux window of its own, with its
# output on screen while it happens and on disk afterwards.
#
# Three things are true of every job and each one was chosen:
#
#   stdout and stderr stay APART on disk. A supervisor reading a failure needs
#   to know which stream said what; `2>&1` throws that away and no later parsing
#   gets it back. They are interleaved only in the live view, where a human is
#   reading them with their eyes and the ordering is what matters.
#
#   The FILES are written by the command itself, not by a tee in the middle.
#   tee buffers, and a buffered tee means the exit code can land on disk before
#   the last line of output does — so a reader that polls for `rc` and then
#   reads `out` sees a truncated result and no way to know it. The live view is
#   a `tail -F` onto those files, which is the direction that cannot lose data:
#   worst case the window is a moment behind the record.
#
#   The pane STAYS after the command ends (remain-on-exit, set by the caller).
#   A job that finishes and vanishes is a job nobody got to read.
set -u

id=${1:?job id}
cmd=${2:?command}
dir=/tmp/sentineldesk/jobs/$id

# Keep this pane after the command exits, and set it from IN HERE.
#
# The caller tried to do this and could not, which is worth recording because it
# looks like it should work. `tmux new-window` then `set-option remain-on-exit`
# is two calls, and everything that finishes between them — `echo`, `id`, any
# check an agent runs to orient itself — closes its window before the option
# lands. The result was the opposite of the intent: fast commands, the common
# case, were exactly the ones nobody got to see.
#
# This script is the first thing in the pane, so there is no gap to race with.
[ -n "${TMUX_PANE:-}" ] && tmux set-option -t "$TMUX_PANE" -w remain-on-exit on 2>/dev/null

mkdir -p "$dir"
: > "$dir/out"
: > "$dir/err"
printf '%s' "$cmd" > "$dir/cmd"
date -u +%Y-%m-%dT%H:%M:%SZ > "$dir/started"

# A header, because this pane is read by a person who did not necessarily ask
# for it to appear and deserves to know what it is and who to blame.
printf '\033[1;36m▸ job %s\033[0m — started by the agent\n\033[2m$ %s\033[0m\n\n' "$id" "$cmd"

# Secrets, if this job needed any.
#
# Sourced and then DELETED, before the command runs. The daemon wrote it 0600
# and the value never appeared in the command text, in this pane, or in any
# argv — which was the whole point of routing it through a file instead of
# through `tmux new-window -e`, where `ps aux` would have shown it to every user
# in the container.
#
# Deleting it immediately bounds the window in which the value exists on disk to
# a few milliseconds, and means a job whose transcript somebody reads later
# cannot hand them the password along with it.
if [ -f "$dir/env" ]; then
    . "$dir/env"
    rm -f "$dir/env"
fi

# pipefail so a failing stage of a pipeline is a failing job. Without it
# `curl ... | tar x` reports the exit code of tar and a 404 downloaded into an
# empty archive looks like success — which is the silent-failure class this
# project treats as worse than a crash.
bash -o pipefail -c "$cmd" > "$dir/out" 2> "$dir/err" &
pid=$!
echo "$pid" > "$dir/pid"

# Deliberately NOT `tail --pid=$pid`, which is the obvious way to write this and
# is wrong in the common case. --pid makes tail exit the moment the command
# does; a command that finishes quickly — `echo`, `id`, any check an agent runs
# to orient itself — therefore had its tail killed before the poll that would
# have read what it wrote. Measured: the pane showed the two `==>` headers and
# neither line of output, while both sat correctly on disk. A watcher saw an
# empty window and no reason to think anything was missing.
#
# So tail is killed explicitly below, after the command has exited and after a
# grace period long enough for one more poll. -s 0.1 makes that poll cheap
# enough to wait for.
tail -F -n +1 -s 0.1 "$dir/out" "$dir/err" 2>/dev/null &
tailpid=$!

wait "$pid"
rc=$?

# The last read happens here, before the exit code becomes visible: a reader
# that sees `rc` treats the job as complete and stops waiting for more output.
sleep 0.5
kill "$tailpid" 2>/dev/null
wait "$tailpid" 2>/dev/null

date -u +%Y-%m-%dT%H:%M:%SZ > "$dir/ended"
echo "$rc" > "$dir/rc"

if [ -f "$dir/aborted" ]; then
    printf '\n\033[1;31m■ job %s aborted by %s\033[0m (exit %s)\n' \
        "$id" "$(cat "$dir/aborted")" "$rc"
elif [ "$rc" -eq 0 ]; then
    printf '\n\033[1;32m✓ job %s finished\033[0m (exit 0)\n' "$id"
else
    printf '\n\033[1;31m✗ job %s failed\033[0m (exit %s)\n' "$id" "$rc"
fi
