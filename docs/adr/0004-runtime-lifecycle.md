# ADR-004 — The runtime is a supervised process, and the desktop outlives it

**Status:** accepted, 6 August 2026.
**Decides:** §7.4 of the stage-1 MCP readiness review.

## Context

The desktop is the product. The agent is something the desktop can offer, and it
brings with it every way a piece of software can fail that the desktop currently
cannot: a model provider that stops answering, an API key that expires, a loop
that will not terminate, a database that will not open.

None of those is a reason for somebody's screen to go away.

The MCP plane already works this way and it is worth naming why, because the
same reasoning applies. `sentineldesk -mcp-stdio` is a separate process on
purpose: killing the AI host never takes the desktop down. The runtime is the
same shape of thing, one layer up.

## Decision

`sentineldesk-agent` runs as its own supervised process, alongside the daemon
rather than inside it.

- **In its own container**, from its own image, beside the desktop's. See the
  amendment below: this said "under supervisord in the container" until the
  repositories were split, and the split turned a packaging choice into a
  containment property.
- **Under systemd on a native install**, as a separate unit, so the same
  property holds where there is no supervisord.
- **It is optional.** `AGENT_ENABLED` off, a missing provider key, or a runtime
  that will not start, all mean the Agent Console is unavailable. None of them
  stops the desktop from booting or a person from working.
- **It connects; the daemon listens.** The daemon owns the socket and the
  authority to cut the connection — which is what
  `Server.HaltConnection` already provides — and the runtime reconnects as a
  client. The party that must survive is the party that holds the door.

The invariant, in one line: **restarting `sentineldesk-agent` does not disturb
WebRTC, the Room, or anyone's session.**

## Consequences

- A provider outage, a wedged loop or a corrupt database costs the Agent
  Console and nothing else.
- Both deployment paths change together. Fixing only Docker and leaving the
  native installer behind is the failure this repository has already had once,
  and the compose files, the embedded deployment tree and `install.sh` are all
  part of "done".
- Two processes need two log streams. `make logs` shows the desktop; the
  runtime's own log has to be findable without knowing where to look.
- In-process would have been less to wire. It would also have made every agent
  failure a desktop failure, which is the one trade this project does not make:
  optional capabilities degrade instead of taking everything with them, and that
  rule has already caught real bugs — `callRoom` claiming every tool when no
  room was attached was exactly this mistake at a smaller scale.
- The first vertical slice may run the runtime in-process for speed of
  development. That is allowed only if the protocol between browser and runtime
  is unchanged by moving it out, because otherwise the shortcut becomes the
  design.


## Amendment, 23 August 2026 — where the process lives

**Superseded:** "under supervisord in the container, as its own program".
**Everything else in this ADR stands**, including the invariant.

When this was written the desktop and the agent were one repository, and
"beside the daemon" meant another supervisord program in the same image. They
are now three repositories with three release cycles, and the agent has its own
image. The decision is therefore:

**The desktop image contains one binary, `/usr/local/bin/sentineldesk`. The
agent runs in `cnsoluciones/sentineldesk-agent`, its own container.**

The first reason is mechanical: an agent inside the desktop image is an agent
that can only ever be the same version as the desktop, which is the one property
the repository split exists to prevent.

The second is the one that matters, and it is not about packaging.

### The container IS the boundary

The agent is where a language model's output turns into actions. Everything it
can do, it does through MCP over a Unix socket — and if it lives in its own
container, that is not a convention it follows but the only route it has. There
is no X display in that image, no desktop session, no browser, no file manager,
no window it can reach except by asking the daemon for it. The sandbox and the
protocol say the same thing, so the protocol cannot be gone around.

Put the same binary inside the desktop image and that stops being true. It
would sit next to the display, the home directory, the audit log and the
sockets, one `exec` away from all of them, and "the agent only acts through
MCP" would become a description of what it happens to do rather than a
statement of what it can do. Nothing would have to go wrong for that to
matter; it is a weaker claim from the moment it is made.

And the weaker claim costs the project its point. This exists so that what an
AI does on a desktop can be **watched, arbitrated and learned from** — every
action a tool call, every tool call through the room's turn-taking and into the
audit trail. That record is only complete if there is no other way to act. Two
containers is what makes it complete.

### What follows

- The two services are separate and one connects to the other, which ADR-004
  already decided and this only reinforces: the daemon holds the door, the
  runtime knocks. The desktop boots and works with the agent never started.
- Nothing in the desktop image needs to know the agent exists. There is no
  supervisord program for it, no wrapper script, and no binary to look for.
- `AGENT_ENABLED` stays, on the desktop side, and now means "offer the chat
  plane at all" rather than "start the runtime".
- A binary on the HOST, run by hand, is still supported and is the lightest way
  to work while changing the agent. It gives up the containment described above
  in exchange for a faster loop, which is a reasonable trade for a person
  developing and a bad one for a deployment.
