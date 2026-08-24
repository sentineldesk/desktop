# ADR-006 — A guest reaches the desktop with a ticket, not a relay

**Status:** accepted, 13 August 2026.
**Decides:** the gap left open by [ADR-005](0005-the-front-desk.md) and named in
§3.3 of the workrooms design note: admission says a guest
may enter, and nothing said how they get in.

## Context

Every room's runtime has its own door. The front desk generates an
`AUTH_USER`/`AUTH_PASS` per room and sets them on the container, which is what
makes reaching a room's desktop directly — without coming past the front desk —
require a credential nobody else holds. Burning the room destroys it.

That is the right property and it had an obvious consequence nobody had
answered: there was no path from a guest's room key to that door. A guest could
be admitted to a room and then see nothing.

Three ways out.

**Hand the guest the room's credential.** One value, shared by everyone in the
room, valid until the room ends. It is the shortest path and it spends the
property that made the door worth having: a guest who is later expelled keeps a
working password, and it cannot be revoked for one person without changing it
for all.

**Relay the desktop through the front desk.** Every guest connects to the
control plane, which dials the room and forwards. One door, revocation for free.
It also puts every participant's session through the one component §4.8 says
must stay out of the hot path — and while WebRTC would still carry the pixels
peer-to-peer, the signalling socket for every participant in every room now
lives here, which is a lot of connection state in the process that is supposed
to be able to restart without anyone noticing.

**Mint a ticket the runtime already verifies.** The runtime authenticates
session tokens as an HMAC over `user|expiry` against `AUTH_SECRET`. If the front
desk chooses that secret rather than letting the runtime generate one, it can
produce tokens the runtime accepts — without any change to the runtime, and
without a relay.

## Decision

**The third.** The front desk generates the room's signing secret along with its
password, sets both on the container, and keeps the secret. `desktop.connect`
returns a short-lived token signed with it.

- **Five minutes.** It is needed for the moment between asking and connecting.
- **Per room.** Two rooms never share a secret, so a ticket is worthless
  anywhere but where it was issued.
- **Per session.** The secret lives on `workroom_sessions`, so burning a room
  destroys the runtime that trusted it. A ticket cannot outlive its desktop.
- **Its own command and its own reply.** Not a field on `Workroom`: a room
  object that carried it would publish it every time a dashboard listed rooms.
- **Only for somebody who is in.** A guest still at the door is refused; a key
  for another room is answered `not_found`, the same as a room that is not
  there.

## Consequences

- **The data plane stays direct.** Pixels and input go straight between the
  browser and the room, which §4.8 makes the single biggest reason many rooms
  does not become everything slow. The front desk issues the ticket and steps
  out of the way.
- **It cannot cut a session that is already open.** Expiry bounds how long a NEW
  connection can be made, not how long an existing one lasts. Expelling somebody
  mid-session needs the runtime to be told, and reaching into a room to say so
  is a door the front desk does not have. The runtime already has the mechanism
  — `Server.HaltConnection` — so this is a wire that is missing, not a
  capability.
- **The token format is written twice.** The runtime's `Auth.NewToken` and the
  front desk's `runtimeToken` produce the same bytes from two files. Sharing the
  code would mean importing `internal/stream`, which would pull GStreamer and
  CGO into a binary that is deliberately neither. What keeps the copies honest
  is the integration test: it mints a ticket here and presents it to a REAL
  runtime, so a format that drifts fails against the only authority that
  matters. It also presents a tampered one and a guessed password, because a
  test that only proves the door opens has not shown the door is closed.
- **A secret at rest that is not a hash.** The room's signing secret sits in the
  same database as the rooms' password hashes and differs from them in a way
  worth stating: a hash cannot be turned back into a password, and this can be
  turned into a ticket. It is read by one function, never serialised, and dies
  with its session.
- **A room started before this exists has no secret**, and asking for a ticket
  says so rather than issuing one the runtime will reject — which would look to
  a guest exactly like a broken desktop.

## What this does not decide

How the BROWSER uses the ticket. The guest's view of a room — the desktop
canvas, the filmstrip, the chat — is still to build, and until it is, an
admitted guest is told they are in and nothing navigates. The ticket is the
mechanism that view will need; having it verified against a real runtime first
means that work starts from something known to open the door.
