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

package stream

// The embedded STUN server: the desktop answers "what is my address?" itself,
// so a deployment on a VPS works from the outside with no Google server in
// the path and nothing extra to install. On by default (STUN_EMBEDDED);
// pointing CLIENT_STUN somewhere else or switching this off are both one
// environment variable, like every other knob here.
//
// It speaks exactly ONE verb: a Binding request gets a Binding success with
// the sender's reflexive address, and everything else — allocations, channel
// binds, garbage — is dropped without an answer. That narrowness is the
// security story. STUN cannot be authenticated for browsers (the protocol's
// binding exchange is credential-less by design, and stun: URLs carry none),
// but a responder this small is also nearly worthless to an abuser: it
// relays nothing, amplifies nothing (the answer is the question's size), and
// tells you only your own address. A per-IP token bucket bounds even that,
// on its own gate so a STUN flood can never spend the WebSocket door's
// tokens or earn its bans.

import (
	"errors"
	"fmt"
	"log"
	"net"

	"github.com/pion/stun/v3"

	"github.com/sentineldesk/desktop/pkg/ratelimit"
)

// StunServer is the embedded responder. Close stops it.
type StunServer struct {
	conn net.PacketConn
	gate *ratelimit.IPGate
}

// StartStun binds the UDP port and answers Binding requests until Close.
// A busy port is an error for the caller to LOG and carry on with — the
// desktop must come up without its STUN, like every optional capability.
func StartStun(port int) (*StunServer, error) {
	conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("stun: listen udp :%d: %w", port, err)
	}
	s := &StunServer{conn: conn, gate: ratelimit.NewIPGate()}
	go s.serve()
	return s, nil
}

func (s *StunServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, addr, err := s.conn.ReadFrom(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("stun: read: %v", err)
			}
			return
		}
		udp, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		if allowed, _ := s.gate.Take(udp.IP.String()); !allowed {
			continue // silence is the whole rate-limit answer for UDP
		}
		if !stun.IsMessage(buf[:n]) {
			continue
		}
		msg := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if err := msg.Decode(); err != nil || msg.Type != stun.BindingRequest {
			continue
		}
		resp, err := stun.Build(msg, stun.BindingSuccess,
			&stun.XORMappedAddress{IP: udp.IP, Port: udp.Port},
			stun.Fingerprint,
		)
		if err != nil {
			continue
		}
		if _, err := s.conn.WriteTo(resp.Raw, addr); err != nil {
			log.Printf("stun: write to %s: %v", addr, err)
		}
	}
}

// Addr returns the bound address, mostly for tests binding port 0.
func (s *StunServer) Addr() net.Addr { return s.conn.LocalAddr() }

// Close stops the responder and releases the port.
func (s *StunServer) Close() error { return s.conn.Close() }
