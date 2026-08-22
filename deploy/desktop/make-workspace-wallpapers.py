#!/usr/bin/env python3
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
# Derive the four per-workspace wallpapers from wallpaper.svg at image build.
# One source of truth: the base design lives in wallpaper.svg and only there;
# this script adds what tells the workspaces apart — a huge tonal numeral, a
# "WORKSPACE n" caption, and an accent hue that also recolours the summit dot.
# Workspace 1 keeps the brand's phosphor; 2-4 shift hue but hold the same
# muted register, so the four stay one family.
#
# Run as: make-workspace-wallpapers.py <wallpaper.svg> <outdir>
# It writes ws1.svg .. ws4.svg; the Dockerfile rasterises them with
# rsvg-convert, same as the base wallpaper.

import pathlib
import sys

ACCENTS = {
    1: "#2fae74",  # phosphor — the brand's own green
    2: "#3f7fae",  # steel blue
    3: "#c08a3e",  # amber
    4: "#8a63ae",  # violet
}

# The numeral goes in just before the contour topography, so the rising arcs
# pass over it like terrain. This marker is the group's own comment in
# wallpaper.svg; if that file is restructured and the marker goes, this build
# step fails loudly here rather than shipping four identical wallpapers.
MARKER = "  <!-- Topography:"

def main() -> None:
    src, outdir = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    base = src.read_text()
    if MARKER not in base:
        sys.exit("make-workspace-wallpapers: topography marker not found in %s" % src)
    outdir.mkdir(parents=True, exist_ok=True)
    for n, accent in ACCENTS.items():
        svg = base.replace('fill="#2fae74"', 'fill="%s"' % accent)
        numeral = (
            '  <text x="1560" y="820" text-anchor="middle" '
            'font-family="Roboto, DejaVu Sans, sans-serif" font-size="640" '
            'font-weight="700" fill="{a}" fill-opacity="0.16">{n}</text>\n'
            '  <text x="1560" y="900" text-anchor="middle" '
            'font-family="Roboto, DejaVu Sans, sans-serif" font-size="26" '
            'letter-spacing="8" fill="{a}" fill-opacity="0.55">WORKSPACE {n}</text>\n'
        ).format(a=accent, n=n)
        svg = svg.replace(MARKER, numeral + MARKER)
        (outdir / ("ws%d.svg" % n)).write_text(svg)

if __name__ == "__main__":
    main()
