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

package ratelimit

// Per-origin abuse control, so the desktop can be published on the open
// internet:
//
//   - A token bucket per origin, spent during the /ws upgrade. It bounds the
//     RATE at which one origin may attempt to connect (429 with Retry-After).
//   - A ban ledger fed by failed authentications: 10 failures buy a 5 minute
//     ban, and reoffending doubles it, up to 24 hours.
//
// IPv4 is measured whole; IPv6 is measured per /64, which is the smallest block
// a residential customer gets — measuring the full address would hand a single
// attacker 2^64 distinct identities. The map is bounded (gateMaxKeys): past the
// ceiling, new origins share ONE bucket rather than evicting existing entries,
// because a forced eviction would be a way to hand yourself a fresh bucket. The
// sweep is amortised over incoming traffic, so there are no tickers to leak.

import (
	"log"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// A browser opens one WebSocket per tab and reopens it after a drop. This
	// is sized to stop scripts in a loop, not a person hitting reload.
	gateRate  = 5.0  // sustained connections per second
	gateBurst = 30.0 // burst allowance

	gateBanAfter = 10 // failures tolerated before the first ban
	// The first ban is short on purpose: behind carrier NAT a whole building
	// shares one address, so an early long ban punishes bystanders.
	gateBanFirst = 5 * time.Minute
	gateBanMax   = 24 * time.Hour
	// Must exceed gateBanMax, or the sweep would quietly pardon a repeat
	// offender at exactly the moment their ban runs out.
	gateEntryTTL   = 2 * gateBanMax
	gateMaxKeys    = 4096
	gateSweepEvery = time.Minute
)

type ipEntry struct {
	tokens float64
	refill time.Time
	seen   time.Time

	fails    int
	banUntil time.Time
	banFor   time.Duration
	noisy    bool // already logged: one bot must not fill the journal
}

type IPGate struct {
	mu       sync.Mutex
	entries  map[string]*ipEntry
	overflow ipEntry // shared bucket for origins past the ceiling
	lastKeep time.Time
}

func NewIPGate() *IPGate {
	return &IPGate{entries: map[string]*ipEntry{}}
}

// ClientIP resolves the client's real address. X-Forwarded-For is trusted only
// when the connection itself arrives from loopback — that is, from the local
// reverse proxy. Trusting it unconditionally would let anyone forge an origin.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first := strings.TrimSpace(strings.Split(fwd, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
	}
	return host
}

// GateKey reduces an address to what actually gets measured: the whole IPv4
// address, or the /64 prefix for IPv6.
func GateKey(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// Banned reports whether an origin is currently serving a ban. It is the cheap
// check at the door, before anything is allocated.
func (g *IPGate) Banned(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.entries[key]
	return e != nil && time.Now().Before(e.banUntil)
}

// Take spends a token. It reports whether the request may proceed and, if not,
// how long to wait.
func (g *IPGate) Take(key string) (ok bool, retryAfter time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	g.sweepLocked(now)

	e := g.entries[key]
	if e == nil {
		if len(g.entries) >= gateMaxKeys {
			return g.takeFrom(&g.overflow, now)
		}
		e = &ipEntry{tokens: gateBurst, refill: now}
		g.entries[key] = e
	}
	if now.Before(e.banUntil) {
		e.seen = now
		return false, time.Until(e.banUntil)
	}
	return g.takeFrom(e, now)
}

func (g *IPGate) takeFrom(e *ipEntry, now time.Time) (bool, time.Duration) {
	if e.refill.IsZero() {
		e.tokens, e.refill = gateBurst, now
	}
	if elapsed := now.Sub(e.refill).Seconds(); elapsed > 0 {
		e.tokens = math.Min(gateBurst, e.tokens+elapsed*gateRate)
		e.refill = now
	}
	e.seen = now
	if e.tokens < 1 {
		wait := time.Duration(math.Ceil((1-e.tokens)/gateRate*1000)) * time.Millisecond
		if wait < time.Second {
			wait = time.Second
		}
		return false, wait
	}
	e.tokens--
	return true, 0
}

// Fail records a failed authentication. Enough of them earn a ban, and the ban
// escalates on repeat: coming back after serving one is not an accident.
func (g *IPGate) Fail(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	g.sweepLocked(now)

	e := g.entries[key]
	if e == nil {
		if len(g.entries) >= gateMaxKeys {
			return // no room in the ban ledger; the shared bucket still measures it
		}
		e = &ipEntry{tokens: gateBurst, refill: now}
		g.entries[key] = e
	}
	e.seen = now
	e.fails++
	if e.fails < gateBanAfter {
		return
	}
	e.fails = 0
	switch {
	case e.banFor == 0:
		e.banFor = gateBanFirst
	case e.banFor < gateBanMax:
		e.banFor *= 2
		if e.banFor > gateBanMax {
			e.banFor = gateBanMax
		}
	}
	e.banUntil = now.Add(e.banFor)
	if !e.noisy {
		e.noisy = true
		log.Printf("origin banned after repeated authentication failures: %s (%s)", key, e.banFor)
	}
}

// Reset clears the failure counter after a successful authentication. The ban
// tier is deliberately KEPT: getting in once must not make the next round of
// guessing cheaper.
func (g *IPGate) Pass(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.entries[key]; e != nil {
		e.fails = 0
		e.banUntil = time.Time{}
		e.noisy = false
		e.seen = time.Now()
	}
}

// sweepLocked prunes idle entries, which is what keeps the map from growing
// without bound. A banned entry is never pruned — that would be a pardon.
func (g *IPGate) sweepLocked(now time.Time) {
	if now.Sub(g.lastKeep) < gateSweepEvery {
		return
	}
	g.lastKeep = now
	for k, e := range g.entries {
		if now.Before(e.banUntil) {
			continue
		}
		if now.Sub(e.seen) > gateEntryTTL {
			delete(g.entries, k)
		}
	}
}
