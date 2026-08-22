-- SentinelDesk
-- A collaborative operating system for people and AI agents.
--
-- Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
--
-- Licensed under the Apache License, Version 2.0.
--
-- This product's name and logo are trademarks of Federico Pereira and are not
-- covered by the license above. See the README for the trademark policy.
--
-- SPDX-License-Identifier: Apache-2.0
--
-- The stream card's sparkline, drawn the way the browser's Statistics panel
-- draws its own: a thin polyline over the last minute of sent bitrate,
-- redrawn whole every tick, autoscaled to its own peak. conky's execgraph
-- was tried first and refused politely — it accumulates filled bars one
-- pixel per update, which is a different instrument telling a slower story.
-- The daemon writes the history (streamstatus.go, stream.hist, oldest
-- first); this only connects the dots.
--
-- Anchored to the BOTTOM of the card, because the rows above it come and go
-- with the viewers and the card's height moves; the bottom margin does not.

require 'cairo'
-- conky 1.17+ moved the xlib surface helpers out of 'cairo' into their own
-- module; without this line cairo_xlib_surface_create is nil and the hook
-- fails on every tick, loudly in the log and invisibly on the card. pcall,
-- so an older conky that still bundles them keeps working.
pcall(require, 'cairo_xlib')

local HIST = '/tmp/sentineldesk/stream.hist'
local MARGIN = 22   -- must match border_inner_margin in conky.conf
local HEIGHT = 26   -- the band the line lives in
local GAP    = 6    -- air between the text above and the band

-- The cards' hairline: the same 1px #2f3936 the window frames wear, so the
-- widgets read as siblings of the windows rather than as cutouts. Square,
-- like everything here — the border is the decision to stay square, worn
-- visibly. The stream card overrides the colour to the deep phosphor while
-- anyone is watching: an ON AIR frame, the same grammar as the panel's red
-- REC — colour as information, never decoration.
local function draw_border(cr, w, h, live)
    if live then
        cairo_set_source_rgba(cr, 0x2a / 255, 0xa9 / 255, 0x6c / 255, 1)
    else
        cairo_set_source_rgba(cr, 0x2f / 255, 0x39 / 255, 0x36 / 255, 1)
    end
    cairo_set_line_width(cr, 1)
    cairo_rectangle(cr, 0.5, 0.5, w - 1, h - 1)
    cairo_stroke(cr)
end

-- Somebody is receiving this desktop right now: the status mirror says so.
local function stream_is_live()
    local f = io.open('/tmp/sentineldesk/stream.status', 'r')
    if f == nil then return false end
    local live = false
    for line in f:lines() do
        if line == 'offline=1' then live = false break end
        local n = line:match('^viewers=(%d+)')
        if n then live = tonumber(n) > 0 end
    end
    f:close()
    return live
end

-- The system card loads this file only for the border.
function conky_card_border()
    if conky_window == nil then return end
    local cs = cairo_xlib_surface_create(conky_window.display,
        conky_window.drawable, conky_window.visual,
        conky_window.width, conky_window.height)
    local cr = cairo_create(cs)
    draw_border(cr, conky_window.width, conky_window.height, false)
    cairo_destroy(cr)
    cairo_surface_destroy(cs)
end

function conky_spark()
    if conky_window == nil then return end
    local cs = cairo_xlib_surface_create(conky_window.display,
        conky_window.drawable, conky_window.visual,
        conky_window.width, conky_window.height)
    local cr = cairo_create(cs)
    -- The hairline draws whatever the stream is doing: an idle card without
    -- its border would flicker a different shape the moment a viewer left.
    draw_border(cr, conky_window.width, conky_window.height, stream_is_live())

    local vals = {}
    local f = io.open(HIST, 'r')
    if f then
        for line in f:lines() do
            local n = tonumber(line)
            if n then vals[#vals + 1] = n end
        end
        f:close()
    end
    if #vals < 2 then
        cairo_destroy(cr)
        cairo_surface_destroy(cs)
        return
    end

    local w  = conky_window.width - 2 * MARGIN
    local y0 = conky_window.height - MARGIN          -- bottom of the band
    local top = 1
    for _, v in ipairs(vals) do if v > top then top = v end end

    cairo_set_source_rgba(cr, 0x3f / 255, 0xd6 / 255, 0x8c / 255, 1)
    cairo_set_line_width(cr, 1.5)
    cairo_set_line_join(cr, CAIRO_LINE_JOIN_ROUND)
    cairo_set_line_cap(cr, CAIRO_LINE_CAP_ROUND)
    for i, v in ipairs(vals) do
        local x = MARGIN + (i - 1) * w / (#vals - 1)
        local y = y0 - (v / top) * (HEIGHT - 2)
        if i == 1 then cairo_move_to(cr, x, y) else cairo_line_to(cr, x, y) end
    end
    cairo_stroke(cr)

    cairo_destroy(cr)
    cairo_surface_destroy(cs)
end
