#!/bin/sh
# SentinelDesk
# A collaborative operating system for people and AI agents.
#
# Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
#
# Licensed under the Apache License, Version 2.0.
#
# This product's name and logo are trademarks of Federico Pereira and are not
# covered by the license above. See the README for the trademark policy.
#
# SPDX-License-Identifier: Apache-2.0
#
# A second host on the desktop's network, so the ssh_* tools can be tested
# against somewhere that is not themselves.
#
# The sweep can bring sshd up inside the desktop container and connect to
# 127.0.0.1, and that exercises the code without proving much. A loopback
# session cannot show that a file crossed a machine boundary, and a tunnel to
# your own host forwards to the thing you are already on. Against a real peer
# the evidence is different in kind: ssh_exec returns the OTHER machine's
# hostname, an upload is confirmed by reading the far side, and a local forward
# hands back the peer's own SSH banner.
#
# It also found a bug that self-connection could not. Alpine ships
# AllowTcpForwarding no, so the server refuses the forwarding channel — and the
# refusal arrives only when something connects, long after ssh_tunnel_local has
# returned a tunnel id. That is why this peer deliberately starts with
# forwarding DISABLED and turns it on afterwards: the disabled state is a case
# worth being able to reproduce, not an accident to configure away.
#
# Usage:
#   tools/ssh-peer.sh up          start it and print the address
#   tools/ssh-peer.sh ip          just the address
#   tools/ssh-peer.sh no-forward  forbid forwarding, to test the refusal path
#   tools/ssh-peer.sh forward     allow it again
#   tools/ssh-peer.sh down        remove it

set -e

NAME=${SSH_PEER_NAME:-sentineldesk-ssh-peer}
DESKTOP=${SENTINELDESK_CONTAINER:-sentineldesk}
USER_NAME=peer
USER_PASS=peerpass

net_of_desktop() {
    docker inspect "$DESKTOP" \
        --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null
}

peer_ip() {
    docker inspect "$NAME" \
        --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null
}

case "${1:-up}" in
up)
    net=$(net_of_desktop)
    if [ -z "$net" ]; then
        echo "the desktop container '$DESKTOP' is not running — start it with make up" >&2
        exit 1
    fi
    docker rm -f "$NAME" >/dev/null 2>&1 || true

    # sshd runs as PID 1 so the container's lifetime is the server's. Killing
    # sshd to reload configuration therefore stops the container, which is why
    # the sub-commands below restart it rather than signalling it.
    docker run -d --name "$NAME" --network "$net" alpine:3.20 sh -c "
        apk add --no-cache openssh >/dev/null 2>&1
        ssh-keygen -A
        adduser -D -s /bin/sh $USER_NAME
        echo '$USER_NAME:$USER_PASS' | chpasswd
        mkdir -p /home/$USER_NAME/.ssh
        chown $USER_NAME:$USER_NAME /home/$USER_NAME/.ssh
        chmod 700 /home/$USER_NAME/.ssh
        # Something to fetch that could only have come from here.
        echo 'REMOTE-HOST-MARKER' > /home/$USER_NAME/marker.txt
        chown $USER_NAME:$USER_NAME /home/$USER_NAME/marker.txt
        exec /usr/sbin/sshd -D -e
    " >/dev/null

    # Wait for the port rather than sleeping a guessed amount: apk has to fetch
    # openssh over the network on first run and that is not a fixed duration.
    i=0
    while [ "$i" -lt 60 ]; do
        if docker exec "$NAME" sh -c 'pgrep sshd >/dev/null' 2>/dev/null; then
            break
        fi
        i=$((i + 1))
        sleep 1
    done
    ip=$(peer_ip)
    if [ -z "$ip" ]; then
        echo "the peer started but has no address on $net" >&2
        exit 1
    fi
    echo "peer ready at $ip:22   user=$USER_NAME password=$USER_PASS"
    echo "note: AllowTcpForwarding is OFF (Alpine's default) — run 'forward' to enable it"
    ;;

ip)
    peer_ip
    ;;

forward | no-forward)
    want=yes
    [ "$1" = "no-forward" ] && want=no
    # The first occurrence in sshd_config wins, so the existing line has to be
    # rewritten; appending a new one at the end has no effect at all.
    docker exec "$NAME" sh -c \
        "sed -i 's/^[#[:space:]]*AllowTcpForwarding.*/AllowTcpForwarding $want/I' /etc/ssh/sshd_config"
    docker restart "$NAME" >/dev/null
    i=0
    while [ "$i" -lt 30 ]; do
        docker exec "$NAME" sh -c 'pgrep sshd >/dev/null' 2>/dev/null && break
        i=$((i + 1))
        sleep 1
    done
    docker exec "$NAME" sh -c 'sshd -T | grep -i allowtcpforwarding'
    ;;

down)
    docker rm -f "$NAME" >/dev/null 2>&1 && echo "peer removed" || echo "no peer to remove"
    ;;

*)
    echo "usage: $0 up|ip|forward|no-forward|down" >&2
    exit 2
    ;;
esac
