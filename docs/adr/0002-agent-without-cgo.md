# ADR-002 — `sentineldesk-agent` does not link CGO

**Status:** accepted, 6 August 2026.
**Decides:** §7.2 of the stage-1 MCP readiness review.

## Context

`sentineldesk` links GStreamer through go-gst, so it needs CGO. That single fact
shapes its whole release process: plain cross-compilation is impossible,
`make release-binaries` builds each Linux architecture inside the Debian stage
with arm64 under QEMU, and every release is bounded by how long that takes.

The agent needs none of that. Its work is HTTP to model providers, JSON-RPC over
a unix socket, and local storage. There is exactly one place where CGO could
creep in, and it is the obvious choice for the last of those:
`github.com/mattn/go-sqlite3` is the most used SQLite driver in Go and it is a C
binding.

Taking it would hand the agent the desktop's release problem for no reason
belonging to the agent.

## Decision

`sentineldesk-agent` builds with `CGO_ENABLED=0`, and its release job asserts
that rather than assuming it.

SQLite comes from `modernc.org/sqlite`, which is a pure-Go translation of the
SQLite sources rather than a binding to them.

Any dependency that would reintroduce CGO is a decision to revisit this ADR, not
a detail to notice in review.

## Consequences

- The agent cross-compiles to every platform Go supports, from any machine. Its
  release is a loop over `GOOS`/`GOARCH`, not a QEMU matrix.
- It can be built and tested on a developer's machine without GStreamer, which
  is also true of nothing else in this repository today.
- `modernc.org/sqlite` is slower than the C binding on write-heavy workloads.
  The agent's writes are conversation turns, run events and audit entries — tens
  per minute, not thousands per second — so the difference is not one this
  workload can feel. If that ever changes, it is measurable before it is
  expensive.
- It is a larger dependency in source terms and it tracks upstream SQLite on its
  own schedule. That is a real cost, accepted in exchange for the build story.
- The two binaries now have different build requirements, so the Makefile has to
  say which is which rather than treating "the binary" as one thing.
