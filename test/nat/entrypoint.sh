#!/bin/bash
# Role dispatcher for the cross-NAT topology: relay | gateway | peer.
set -eu

ROLE="${1:-peer}"
REPO_IP=11.11.11.5
RELAY_IP=11.11.11.10

case "$ROLE" in

gateway)
  # A real NAT: forward lan -> public and masquerade the source address, so the
  # peer behind us is genuinely unreachable from outside.
  #
  # The MASQUERADE rule is SUBNET-based, not interface-based, on purpose: Docker
  # does not guarantee which interface (eth0/eth1) maps to which network, and both
  # of this container's networks are ordinary bridges, so an `-o <iface>` rule
  # lands on the wrong interface non-deterministically. `-s <lan> ! -d <lan>`
  # masquerades anything leaving the LAN regardless of egress interface.
  LAN_SUBNET="${LAN_SUBNET:?LAN_SUBNET must be set}"
  echo "[gateway] enabling NAT masquerade for ${LAN_SUBNET}"
  iptables -t nat -A POSTROUTING -s "$LAN_SUBNET" ! -d "$LAN_SUBNET" -j MASQUERADE
  iptables -P FORWARD ACCEPT
  echo "[gateway] ready"
  # Idle forever; the kernel does the work.
  exec sleep infinity
  ;;

relay)
  # Publicly reachable node: runs the circuit-relay service for NAT'd peers and
  # acts as the swarm's DHT bootstrap.
  #
  # relay_service is "on" rather than "auto" on purpose: AutoNAT needs other peers
  # to dial it back before it will declare us public, which is slow and flaky in a
  # short-lived container. Here we *know* this node is public, and that is exactly
  # the case the "on" override exists for.
  mkdir -p /etc/debswarm
  cat > /etc/debswarm/config.toml <<EOF
[network]
listen_port = 4001
bootstrap_peers = []
relay_service = "on"
enable_autorelay = false        # a public node needs no reservation of its own
force_reachability = "public"   # this node IS public; skip AutoNAT's guessing
proxy_bind = "0.0.0.0"
proxy_allowed_cidrs = ["11.11.11.0/24"]

[privacy]
enable_mdns = false        # force the DHT + relay path, not a LAN shortcut

[fleet]
enabled = false            # ditto: fleet is a LAN mechanism

[cache]
path = "/var/cache/debswarm"
EOF
  # relay-data mode (run.sh --relay-data): raise the per-circuit data cap so this
  # relay will carry small package transfers for symmetric-NAT'd peers that cannot
  # hole-punch. Left unset, the relay stays coordinate-only (128KB default).
  if [ -n "${RELAY_BUFFER_SIZE:-}" ]; then
    cat >> /etc/debswarm/config.toml <<EOF

[network.relay_limits]
buffer_size = "${RELAY_BUFFER_SIZE}"
EOF
    echo "[relay] relay_limits.buffer_size = ${RELAY_BUFFER_SIZE} (relay will carry small transfers)"
  fi
  echo "[relay] starting debswarm (relay service + DHT bootstrap)"
  debswarm daemon --log-level debug &
  DAEMON_PID=$!

  # The identity key is created by the daemon, not by `identity show`, so publish
  # our peer ID only once it exists. The peers block until this file appears —
  # without it they have no relay to reserve on and no DHT to bootstrap from.
  mkdir -p /shared
  for _ in $(seq 1 60); do
    ID="$(debswarm identity show 2>/dev/null | grep -oE '12D3KooW[A-Za-z0-9]+' | head -1)"
    if [ -n "$ID" ]; then
      echo "$ID" > /shared/relay.id
      echo "[relay] peer id published: $ID"
      break
    fi
    sleep 1
  done

  wait "$DAEMON_PID"
  ;;

peer)
  # A NAT'd peer. Its only route off-net is via the gateway, which masquerades it.
  echo "[peer] routing default via ${GATEWAY}"
  ip route replace default via "${GATEWAY}"

  # Wait for the relay's peer ID, which run.sh drops here once the relay is up.
  # Without it we have no relay to reserve on and no DHT to bootstrap from.
  while [ ! -s /shared/relay.id ]; do
    echo "[peer] waiting for relay peer id..."
    sleep 1
  done
  RELAY_ID="$(cat /shared/relay.id)"
  RELAY_ADDR_TCP="/ip4/${RELAY_IP}/tcp/4001/p2p/${RELAY_ID}"
  # NOTE: TCP only, not QUIC. Docker Desktop's NAT (conntrack) expires UDP
  # mappings after ~30s, which repeatedly kills the QUIC peer<->relay connection
  # before a reservation can stabilise — an artifact of this test's masquerading
  # gateway, not of debswarm. TCP conntrack survives, so the relay reservation
  # and the hole punch that rides on it stay up. (Real NATs vary; QUIC works over
  # many of them, and debswarm still offers both transports.)
  echo "[peer] relay (TCP) = ${RELAY_ADDR_TCP}"

  # AUTORELAY may be forced off by run.sh --baseline, to prove this rig actually
  # detects the bug rather than passing regardless.
  AUTORELAY="${AUTORELAY:-true}"
  echo "[peer] enable_autorelay = ${AUTORELAY}"

  mkdir -p /etc/debswarm
  cat > /etc/debswarm/config.toml <<EOF
[network]
listen_port = 4001
bootstrap_peers = ["${RELAY_ADDR_TCP}"]
relay_peers = ["${RELAY_ADDR_TCP}"]
enable_autorelay = ${AUTORELAY}
relay_service = "off"           # we are behind NAT; relaying for others is useless
force_reachability = "private"  # we ARE behind NAT; make AutoRelay reserve at once
relayed_transfer_max_bytes = ${RELAYED_TRANSFER_MAX:-0}  # >0 (relay-data mode) fetches small pkgs over the relay when the punch fails

[privacy]
enable_mdns = false        # the peers are on different subnets anyway, but be sure

[fleet]
enabled = false            # fleet is a LAN mechanism; this must be pure DHT+relay

[proxy]
allowed_hosts = ["${REPO_IP}"]

[security]
verify_upstream_signatures = "off"   # the test mirror is unsigned; this is a NAT test, not a verify test

[cache]
path = "/var/cache/debswarm"
EOF

  # Point APT at the local proxy and at our test "mirror" (a flat, unsigned repo,
  # hence trusted=yes; debswarm still SHA256-checks every byte against its index).
  echo "Acquire::http::Proxy \"http://127.0.0.1:9977\";" > /etc/apt/apt.conf.d/90debswarm
  echo "deb [trusted=yes] http://${REPO_IP}/ ./" > /etc/apt/sources.list.d/testrepo.list
  : > /etc/apt/sources.list   # the test repo is the ONLY source

  echo "[peer] starting debswarm"
  exec debswarm daemon --log-level debug
  ;;

*)
  echo "unknown role: $ROLE" >&2
  exit 1
  ;;
esac
