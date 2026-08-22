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
# Wait for the X server AND for a window manager to have registered itself
# (_NET_SUPPORTING_WM_CHECK), then run the given command.
#
# Without this the panel starts before Openbox: it disables the modules that
# depend on EWMH hints, and its own window can end up unmapped — an invisible
# panel with no error to explain it.
until xdpyinfo -display "${DISPLAY:-:0}" >/dev/null 2>&1; do
    sleep 0.2
done
until xprop -root _NET_SUPPORTING_WM_CHECK 2>/dev/null | grep -q "window id"; do
    sleep 0.2
done
exec "$@"
