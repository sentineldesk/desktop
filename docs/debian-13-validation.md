# Validating the front desk on Debian 13

The front desk is developed against macOS with Docker Desktop and deployed to
Linux. Almost everything transfers; three things can only be proven on the
deployment target, and this page is the list of them and the commands that
close each one. Run it on the Debian 13 host that will serve rooms, as a user
in the `docker` group.

**Why these three cannot be closed anywhere else:**

1. **File ownership across the bind mounts.** A room's home, audit, work and
   recordings live on the host under `ROOMS_ROOT`, bind-mounted into the
   container. On Docker Desktop a VM's file sharing launders uid/gid; on
   Linux the numbers are real, and a mismatch shows up as a desktop that
   cannot write its own home directory.
2. **Bridge networking as production has it.** On Linux the host can route to
   `172.17.0.x`, which HIDES a missing `ROOM_NAT_IP` — everything works from
   the host's own browser and fails for the first remote viewer. The check
   below is therefore run from a second machine on purpose.
3. **Published UDP under real iptables**, not Docker Desktop's userland
   forwarder — the media path the WebRTC streams ride on.

## 1. Build

```bash
git clone https://github.com/sentineldesk/desktop.git && cd sentineldesk
make image            # the room image, native (no QEMU on the target)
make server           # bin/sentineldesk-server, pure Go
```

The agent is a separate repository now, and building it is a separate clone —
it reaches a desktop over the MCP socket like any other client, so it does not
have to be built here, or at the same version, or at all:

```bash
git clone https://github.com/sentineldesk/agent.git && cd agent && make build
```

## 2. The integration suite, on the target

```bash
make frontdesk-test
```

This starts a throwaway PostgreSQL on :5433, then runs
`test/integration/frontdesk_test.go`: a REAL room through the whole
lifecycle — created over the WebSocket door, container started through the
Docker API, desktop answering HTTP, ticket minted and verified against the
runtime including a tampered token, ended and burned. A pass here closes the
file-ownership question, because the test writes through the same bind
mounts production will use.

The database is disposable and the target says so — do not point
`FRONTDESK_TEST_DB` at a database whose contents matter.

## 3. The remote-viewer check

On the server:

```bash
export ADMIN_USER=... ADMIN_PASSWORD=... SESSION_SECRET=$(openssl rand -hex 32)
export DATABASE_URL=postgres://...
export PUBLIC_URL=http://<server-ip>:9090
export ROOM_NAT_IP=<server-ip>          # the address viewers actually reach
export ROOMS_ROOT=/var/lib/sentineldesk/rooms
export WEB_ROOT=$PWD/apps/web/dist      # built with VITE_TRANSPORT=live
./bin/sentineldesk-server
```

`SESSION_SECRET` is not optional in production even though the server starts
without it: an unset secret is generated per boot, and a restart then
invalidates every key in flight — guests bounce back to the door, and the
reconnect that is supposed to walk them back in cannot prove who they were.

From a DIFFERENT machine: open `http://<server-ip>:9090`, create a room,
open it, and confirm pixels move — then take the controls and type into a
terminal. If the room connects and stays black, `ROOM_NAT_IP` is wrong or
the UDP range (`ROOM_MEDIA_PORT_MIN..MAX`) is not open in the firewall;
those are the only two causes this path has.

Then share the room's `/w/<uuid>` link with the same second machine and walk
the guest flow: knock, wait, admit from the dashboard, watch, chat, ask for
the controls and drive. While the guest is inside, restart the server once:
they must walk back in by themselves within ~30 seconds, without re-approval
— that is the reclaim working, and it only holds when `SESSION_SECRET` is
pinned.

### The call, and the optional SFU

Cameras between participants default to a peer-to-peer mesh and need nothing
from the server. Past about four cameras, start the shared SFU and tell the
front desk where it is:

```bash
export LIVEKIT_KEY=sentineldesk LIVEKIT_SECRET=$(openssl rand -hex 32)
docker compose -f deploy/docker-compose.sfu.yml up -d
export LIVEKIT_URL=ws://<server-ip>:7880   # as BROWSERS reach it; wss:// behind TLS
# restart sentineldesk-server with the three LIVEKIT_* variables set
```

The check: two machines with cameras on in the same room, and
`docker logs sentineldesk-sfu` showing one room named by the workroom's uuid
with two participants. Unset `LIVEKIT_URL` and the calls quietly return to
the mesh — that fallback is a feature, not a failure to configure.

## 4. TLS

For a front desk that terminates its own TLS:

```bash
export TLS_CERT=/etc/letsencrypt/live/<host>/fullchain.pem
export TLS_KEY=/etc/letsencrypt/live/<host>/privkey.pem
export PUBLIC_URL=https://<host>:9090
```

Setting one of the pair without the other refuses to start — that is the
check working, not a bug. Behind a reverse proxy, leave both unset and let
the proxy terminate; the rooms' own ports still need to be reachable, TLS'd
by the same proxy or exposed as-is on a trusted network.

## 5. What a pass means

`make frontdesk-test` green on the target, plus a remote browser that saw
pixels and drove the desktop, plus a guest who was admitted from the
dashboard — that is the whole §14 checklist closed. Everything else about
the front desk was already proven by suites that run anywhere.
