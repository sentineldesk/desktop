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

// The role a ticket may carry, and what it must survive.
//
// The front desk stamps `admin` or `moderator` into the tickets it signs, and
// the runtime honours it for exactly one act: restreaming without holding the
// controls. That makes the role field a privilege escalation surface, so the
// tests here are less about the happy path than about the tampering: a role
// glued onto a signed token, a signature moved between payloads, a token the
// runtime itself minted (which never carries a role).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mintLikeTheFrontDesk reproduces runtimeToken from
// internal/controlplane/control.go. A copy, on purpose: the two packages do
// not import each other, and this test is the tripwire that fires when one
// side changes the format without the other. The migration file
// 0002_desktop_tickets.sql records the same contract in prose.
func mintLikeTheFrontDesk(user, role, secret string, expires time.Time) string {
	payload := fmt.Sprintf("%s|%d", user, expires.Unix())
	if role != "" {
		payload += "|" + role
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestTheFrontDeskTokenFormatIsOurs(t *testing.T) {
	auth := NewAuth("room", "pw", "secret", time.Hour)
	later := time.Now().Add(time.Minute)

	// A guest's ticket: the bare shape every runtime version accepts.
	role, ok := auth.ParseToken(mintLikeTheFrontDesk("room", "", "secret", later))
	if !ok || role != "" {
		t.Fatalf("a bare ticket parsed as (role=%q, ok=%v), want (\"\", true)", role, ok)
	}

	// A privileged ticket carries its role through.
	role, ok = auth.ParseToken(mintLikeTheFrontDesk("room", "admin", "secret", later))
	if !ok || role != "admin" {
		t.Fatalf("an admin ticket parsed as (role=%q, ok=%v), want (\"admin\", true)", role, ok)
	}
}

func TestARoleCannotBeGluedOntoASignedToken(t *testing.T) {
	auth := NewAuth("room", "pw", "secret", time.Hour)
	later := time.Now().Add(time.Minute)

	// Take a valid guest token and append a role to the payload while keeping
	// the guest signature: the signature covers the payload, so this must die.
	bare := mintLikeTheFrontDesk("room", "", "secret", later)
	parts := strings.SplitN(bare, ".", 2)
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	forged := base64.RawURLEncoding.EncodeToString(append(raw, []byte("|admin")...)) +
		"." + parts[1]
	if _, ok := auth.ParseToken(forged); ok {
		t.Fatal("a token with a role glued past the signature was accepted")
	}
}

func TestTheRuntimesOwnTokensCarryNoRole(t *testing.T) {
	// The runtime mints tokens for its own reconnects (NewToken). A standalone
	// desktop has one credential and no hierarchy, so those must never come
	// back privileged — a role here would be a power nobody assigned.
	auth := NewAuth("room", "pw", "secret", time.Hour)
	role, ok := auth.ParseToken(auth.NewToken())
	if !ok {
		t.Fatal("the runtime's own token did not validate")
	}
	if role != "" {
		t.Fatalf("the runtime's own token carries role %q", role)
	}
}

func TestAnExpiredPrivilegedTicketIsStillExpired(t *testing.T) {
	auth := NewAuth("room", "pw", "secret", time.Hour)
	old := mintLikeTheFrontDesk("room", "admin", "secret", time.Now().Add(-time.Minute))
	if _, ok := auth.ParseToken(old); ok {
		t.Fatal("an expired ticket was accepted because it carried a role")
	}
}
