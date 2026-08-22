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

// The embedded STUN responder, held to its one promise: a Binding request
// gets back the sender's own address, and anything else gets silence. Both
// tests run anywhere — loopback UDP, no display, no pipeline.

import (
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func startTestStun(t *testing.T) (*StunServer, net.Addr) {
	t.Helper()
	s, err := StartStun(0) // an ephemeral port: the test must not fight the host
	if err != nil {
		t.Fatalf("StartStun: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, s.Addr()
}

func TestStunAnswersBindingWithTheSendersAddress(t *testing.T) {
	_, addr := startTestStun(t)

	conn, err := net.Dial("udp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.Write(req.Raw); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 1500)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no answer to a Binding request: %v", err)
	}
	resp := &stun.Message{Raw: buf[:n]}
	if err := resp.Decode(); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Type != stun.BindingSuccess {
		t.Fatalf("answer type = %v, want binding success", resp.Type)
	}
	var mapped stun.XORMappedAddress
	if err := mapped.GetFrom(resp); err != nil {
		t.Fatalf("no XOR-MAPPED-ADDRESS in the answer: %v", err)
	}
	local := conn.LocalAddr().(*net.UDPAddr)
	if !mapped.IP.Equal(local.IP) || mapped.Port != local.Port {
		t.Fatalf("mapped %v:%d, want the sender's own %v", mapped.IP, mapped.Port, local)
	}
}

func TestStunIgnoresEverythingElse(t *testing.T) {
	_, addr := startTestStun(t)

	conn, err := net.Dial("udp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Garbage, then a well-formed message of the wrong type: silence for
	// both. An answer to either would make the port worth a scanner's time.
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	alloc := stun.MustBuild(stun.TransactionID,
		stun.NewType(stun.MethodAllocate, stun.ClassRequest))
	if _, err := conn.Write(alloc.Raw); err != nil {
		t.Fatalf("write allocate: %v", err)
	}

	buf := make([]byte, 1500)
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("got %d bytes back; the responder must stay silent", n)
	}
}
