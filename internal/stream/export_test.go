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

// The bridge for the external test package. The verb tables stay unexported —
// they are the DataChannel adapter's private translation, not an API — but the
// parity test that compares them against the real MCP catalogue has to live
// OUTSIDE this package, because internal/mcp imports internal/stream and a
// test inside it importing mcp back would be a cycle.
var (
	CaptureVerbForTest  = captureVerb
	RestreamVerbForTest = restreamVerb
)
