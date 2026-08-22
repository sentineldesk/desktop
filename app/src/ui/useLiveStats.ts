// SentinelDesk
// A collaborative operating system for people and AI agents.
//
// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

// The transport, measured — RTCPeerConnection.getStats(), polled while the
// panel is open. Ported from the workroom's Desktop pane: the canvas size,
// the codecs actually negotiated, the frames actually arriving, and the
// round trip — the number latency questions start from.
import { useEffect, useRef, useState } from 'react'

export interface LiveStats {
  size: string
  vcodec: string
  acodec: string
  fps: number
  bitrate: string
  rtt: number
  loss: number
  /** The last minute of received bitrate, one sample per second — the wave. */
  history: readonly number[]
}

export function useLiveStats(
  open: boolean,
  desktop: { getStats(): Promise<RTCStatsReport | null> },
  videoRef: React.RefObject<HTMLVideoElement | null>,
): LiveStats | null {
  const [stats, setStats] = useState<LiveStats | null>(null)
  const lastBytes = useRef({ bytes: 0, at: 0 })
  const historyRef = useRef<number[]>([])

  useEffect(() => {
    if (!open) {
      setStats(null)
      return
    }
    let gone = false
    const poll = async () => {
      const report = await desktop.getStats()
      if (gone || !report) return
      let vcodec = '—'
      let acodec = '—'
      let fps = 0
      let loss = 0
      let rtt = 0
      let bytes = 0
      const codecs = new Map<string, string>()
      report.forEach((entry) => {
        if (entry.type === 'codec') {
          codecs.set(entry.id, String(entry.mimeType ?? ''))
        }
      })
      report.forEach((entry) => {
        if (entry.type === 'inbound-rtp') {
          const inbound = entry as RTCInboundRtpStreamStats & {
            framesPerSecond?: number
            codecId?: string
          }
          const mime = codecs.get(inbound.codecId ?? '') ?? ''
          if (inbound.kind === 'video') {
            vcodec = mime.replace('video/', '') || vcodec
            fps = Math.round(inbound.framesPerSecond ?? 0)
            bytes += inbound.bytesReceived ?? 0
            const total = (inbound.packetsLost ?? 0) + (inbound.packetsReceived ?? 1)
            loss = total > 0 ? ((inbound.packetsLost ?? 0) / total) * 100 : 0
          } else if (inbound.kind === 'audio') {
            acodec = mime.replace('audio/', '') || acodec
            bytes += inbound.bytesReceived ?? 0
          }
        }
        if (entry.type === 'candidate-pair') {
          const pair = entry as RTCIceCandidatePairStats
          if (pair.state === 'succeeded' && pair.currentRoundTripTime !== undefined) {
            rtt = Math.round(pair.currentRoundTripTime * 1000)
          }
        }
      })
      const now = performance.now()
      let mbps = 0
      if (lastBytes.current.at > 0 && now > lastBytes.current.at) {
        mbps =
          ((bytes - lastBytes.current.bytes) * 8) /
          ((now - lastBytes.current.at) / 1000) /
          1_000_000
      }
      lastBytes.current = { bytes, at: now }
      historyRef.current.push(mbps)
      if (historyRef.current.length > 60) historyRef.current.shift()
      const el = videoRef.current
      setStats({
        size: el && el.videoWidth ? `${el.videoWidth}×${el.videoHeight}` : '—',
        vcodec,
        acodec,
        fps,
        bitrate: mbps > 0 ? mbps.toFixed(1) : '0.0',
        rtt,
        loss,
        history: [...historyRef.current],
      })
    }
    void poll()
    const timer = window.setInterval(() => void poll(), 1000)
    return () => {
      gone = true
      window.clearInterval(timer)
      lastBytes.current = { bytes: 0, at: 0 }
      historyRef.current = []
    }
  }, [open, desktop, videoRef])

  return stats
}
