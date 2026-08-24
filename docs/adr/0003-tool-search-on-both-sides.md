# ADR-003 — Tool search lives on both sides, and the runtime answers its own

**Status:** accepted, 6 August 2026.
**Decides:** §7.3 of the stage-1 MCP readiness review.

## Context

A hundred and fifteen tool schemas is a real amount of a model's context, spent
before it has read the request. Both halves of this project run into that, and
they run into it from different places.

**An external host** — Claude Code, Claude Desktop, anything speaking MCP — has
no runtime of ours. Whatever help it gets has to come over the protocol, which
is why `tool_search` is a tool in the catalogue and `MCP_DISCOVERY` trims
`tools/list`. Removing either would take a working feature away from the users
who most need it.

**The Agent Runtime** is in a different position. It runs `tools/list` once and
holds the catalogue in memory. For it, calling `tool_search` over JSON-RPC is a
round trip to ask itself a question it can already answer, and it adds a step to
every turn that fails when the socket is slow.

An earlier note said "the runtime should not use tool search". That was badly
put, and it is worth stating plainly what was and was not meant: the server's
`tool_search` stays exactly where it is. The narrow point is only about which
side *computes* the answer for the runtime's own model.

There is a concrete problem underneath, and it is not philosophical. If the
runtime forwards the server's catalogue to its model unchanged **and** offers a
search of its own, the model sees two tools called `tool_search` that do the
same thing. One of them has to go.

## Decision

Tool search exists on both sides. Neither is removed.

- **The server keeps `tool_search`** as a catalogue tool, together with
  `MCP_DISCOVERY`. External hosts are unaffected by anything the runtime does.
- **The runtime answers its own**, locally, from the catalogue it already holds.
  It does not call the server's tool to do it.
- **The runtime does not forward the server's `tool_search`** to its model. It
  presents one tool of that name, its own. This resolves the collision by
  choosing, rather than by renaming one of them into something a model has to
  guess between.
- **The ranking is one implementation.** `searchTools`, the category rules and
  the aliases move out of `internal/mcp` into a package both import, so the two
  sides rank identically and there is one place to improve them. That move
  happens when the runtime exists and imports it — not before, because a shared
  package with one consumer is a guess about the second.

## Consequences

- Someone using SentinelDesk through Claude Code today keeps exactly what they
  have. That is the point of the decision, not a side effect.
- The runtime's search costs no round trip and works while the socket is busy.
- The model behind the runtime sees one `tool_search` and cannot pick the wrong
  one, because there is only one.
- Two call sites, one ranking. If they ever disagree it is a bug with an obvious
  location, rather than two implementations that were always going to drift —
  the failure mode stage 1 spent two commits removing at the level below this.
- The runtime **may** still call the server's `tool_search` — nothing forbids it,
  and a debugging session might. It simply is not the path a turn takes.
