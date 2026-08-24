# ADR-005 — The front desk is its own binary, and it drives the engine directly

**Status:** accepted, 13 August 2026.
**Decides:** §4.1, §4.2 and §13 of the workrooms design note,
and closes the "how are rooms started" question the Phase-0 proof of concept
left open.

## Context

The proof of concept ran two isolated desktops from
`deploy/docker-compose.workrooms-demo.yml` and proved the claim it was built to
prove: a host can hold several rooms as several containers, and a marker file
written in one does not exist in the other. Isolation is the container
boundary, not bookkeeping.

What it did not answer is who starts the third room. In the demo the answer is
"a person edits a YAML file", which is fine for a demonstration and impossible
for a product where a room's lifetime is decided at runtime by somebody
clicking a button.

Two shapes were available.

**Generate compose files and shell out.** The server would write YAML for each
room and run `docker compose up`. It reuses something already working and it is
readable by anyone who has used compose.

**Talk to the Docker Engine API from the server process.** The server holds a
client and calls create, start, stop, remove.

At the same time there was a second question, and it turns out to be the same
question: does the control plane ship inside the desktop image as another mode
of `sentineldesk`, or as its own binary in its own container?

## Decision

**A second binary, `sentineldesk-server`, in its own image, outside the rooms.**

The runtime IS a desktop: one X display, one room, CGO linked against GStreamer,
and it only runs on Linux with X11, uinput and PulseAudio. The front desk manages
those containers, which means it cannot be one of them — a control plane shipped
inside the image it controls could not outlive the containers it destroys.

The build requirements turn out to be a useful check on the separation. The
front desk builds with `CGO_ENABLED=0`, and its Dockerfile sets that
deliberately: a change that quietly adds a native dependency fails at the build
rather than shipping an image missing the shared libraries it now needs. If this
binary ever needs CGO, something that belongs in a room has been added to the
front desk.

**Rooms are started through the Docker Engine API, from inside the server.**
Not compose, not a generated file, not a subprocess.

**`docker-compose.frontdesk.yml` starts two services and will never start
more:** the control plane and PostgreSQL. What remains in compose is what a
human starts once, by hand.

**The door is the WebSocket that already exists**, extended rather than
duplicated. The runtime's invariant — the first frame must authenticate, and
until it does there is no SDP offer, no ICE and no DataChannel — now covers two
kinds of arrival. An administrator sends a username and a password. A guest
sends the room they hold a link to and, when the room has one, its password.
Both leave holding a signed key that states what they may reach, and everything
past the door is a message on the DataChannel (§1.6 rule 1: no REST API, ever).

## Why the engine API rather than compose

The reasons are about failure, not taste.

**Compose describes a fixed set of services declared in advance.** Driving it
from a server means generating YAML, shelling out, and inferring state from the
exit code of a program designed for a human at a terminal. Every one of those
steps is a place where a failure becomes a string nobody reads — and this
repository's severity order puts silent failure above crashes for exactly that
reason.

**A room that will not start has to say why, in a form the panel can show.**
`ContainerCreate` returns an error that names the problem. `docker compose up`
returns 1 and prints to a stream, and the difference between "no such image"
and "port already allocated" is then a substring match on output that changes
between compose versions.

**State has to be readable after a restart.** Labels on containers survive the
server; a generated file describes what was intended, not what is. The
reconciler reads the host, which is the only authority on what is actually
running.

**The cost is a large dependency.** `github.com/docker/docker` brings roughly
thirty modules into a go.mod that had twenty. That is a real price, paid for a
typed API, version negotiation with older daemons, and the exec/logs/stats
calls the next phases need anyway.

## Consequences

- **Stopping the front desk does not stop the rooms.** They are the product;
  they outlive the control plane on purpose, and the reconciler adopts them
  again on the next start. A control plane that took every desktop down with it
  would make each deployment an outage for everybody working.
- **The front desk holds the Docker socket, which is root on the host by
  another name.** So it has no open mode: `auth.NewAdmin` refuses to start
  without a password, with no flag and no environment variable to bypass it —
  unlike the desktop, which permits an open one for development. §12's rule
  about never mounting the socket into anything public-facing stands, and a
  restricted socket proxy is the answer for a deployment strangers can reach.
- **A second store to operate.** PostgreSQL is a dependency of the front desk,
  not something it manages: a service that keeps its own state in a database
  whose lifecycle it owns is a bootstrap knot, and it makes backups and
  upgrades strange for no gain. Migrations run at startup rather than from a
  step somebody has to remember.
- **Two fleets on one host must not burn each other's rooms.** Ownership is
  scoped by a `FLEET` label, checked by the reconciler before anything is
  removed. This was nearly left out, and leaving it out would have meant a
  staging instance destroying production's rooms on a shared machine.
- **`docker-compose.workrooms-demo.yml` stayed as the proof** until 2026-08-14,
  when it was retired: the front desk had long since taken over starting rooms,
  the isolation claim it demonstrated is now exercised on every `make
  frontdesk-test` run, and a compose file that describes rooms — even as a
  museum piece — invites somebody to start rooms with it. The paths it proved
  live on as constants in `internal/orchestration/spec.go`.
- **Compose is not gone from the project**, and the wording matters: rooms are
  not started by compose. The front desk itself still is, because that is a
  thing a person starts once.

## What this does not decide

Control leases, admission policies and the waiting room. The `control_leases`
table and its one-active-lease-per-room index exist already — the invariant is
in the schema before the code that would violate it — but the four policies of
§3.4 are phase 3. The commands for them are refused by name, saying which phase
they belong to, rather than falling through to "unknown command"; a client
asking for one gets information instead of the impression it sent something
malformed.
