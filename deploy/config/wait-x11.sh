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
# Wait for the X server to accept connections, then run the given command.
until xdpyinfo -display "${DISPLAY:-:0}" >/dev/null 2>&1; do
    sleep 0.2
done
exec "$@"
