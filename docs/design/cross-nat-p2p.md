# Design: cross-NAT P2P (relay reservations, hole punching, relay service)

**Status:** Implemented (Phase 1, v1.40.0)
**Author:** debswarm maintainers
**Date:** 2026-07-15
**Backlog item:** Product gaps #2 — "Cross-NAT P2P doesn't work; docs claim it does"

## Context

debswarm's entire premise is that a package you need is probably already on a
peer near you. Today that works on a LAN (mDNS + the fleet coordinator) and
between nodes that happen to be publicly reachable. **It does not work between
two NAT'd peers — the overwhelmingly common case for home and office machines.**
Two such peers can never establish a connection, so for most of the internet
debswarm silently degrades into an HTTP caching proxy with extra steps.

`docs/comparison.md` already states this honestly ("Relay Fallback: Partial —
libp2p circuit-relay client transport only; no relay service runs yet, so two
NAT'd peers cannot yet connect through a relay"). This document is about closing
the gap rather than documenting around it.

### What exists today (verified against `HEAD`)

`internal/p2p/node.go` builds the libp2p host with:

```go
libp2p.EnableNATService(),   // always on — answer AutoNAT dial-backs for others
libp2p.NATPortMap(),         // always on — UPnP / NAT-PMP port mapping
libp2p.EnableRelay(),        // if cfg.EnableRelay      (default true)
libp2p.EnableHolePunching(), // if cfg.EnableHolePunching (default true)
```

Config surface is `[network] enable_relay` and `enable_hole_punching`
(`internal/config/config.go`), both defaulting to true, wired through
`cmd/debswarm/daemon.go` and `seed.go`.

### Why nothing connects (the broken chain)

For two NAT'd peers A and B to talk, libp2p requires **all** of these:

1. **A reserves a slot on a relay.** The relay then vouches for A, and A gains a
   circuit address: `/ip4/<relay>/tcp/4001/p2p/<relay-id>/p2p-circuit/p2p/<A-id>`.
2. **A advertises that circuit address** (via identify / the DHT), so B can find
   something dialable.
3. **B dials the circuit address**, establishing a *relayed* connection. Under
   circuit relay v2 this connection is deliberately tiny — limited to roughly
   128 KB and a couple of minutes.
4. **DCUtR (hole punching) runs over that relayed connection.** The two peers
   exchange observed addresses and simultaneously dial each other, upgrading to
   a **direct** connection.
5. Bulk transfer happens over the direct connection.

debswarm implements steps 3–5 and **nothing performs step 1**. `EnableRelay()`
only installs the *client transport*: it lets a node dial a `/p2p-circuit`
address, and lets it be reached through a relay **if it already holds a
reservation**. Nothing ever makes a reservation, because **AutoRelay is not
enabled**. No reservation means no circuit address, which means nothing for a
peer to dial, which means no relayed connection — and DCUtR only ever triggers
*on an existing relayed connection*.

**The consequence worth internalising: `EnableHolePunching()` is effectively dead
code today.** It is switched on, it is advertised in the README and the config,
and it can never fire, because the relayed connection it needs to coordinate
through is never created. The chain breaks at step 1 and everything downstream
is unreachable.

A second, independent problem sits behind the first: **even with AutoRelay
enabled, there would be no relay to reserve on.** No debswarm node runs a relay
service, and the configured libp2p bootstrap nodes do not offer open circuit-v2
reservations to arbitrary peers. Fixing this properly therefore means answering
*"whose machine relays?"*, not just flipping an option.

## What this does and does not change (trust model)

**Unchanged, and worth stating plainly:** a relay is a **rendezvous, not a trust
anchor**. It carries an end-to-end encrypted libp2p stream it cannot read, and
in the common case it carries nothing at all — its only job is to let two peers
punch a direct hole. Every byte debswarm accepts from a peer is still checked
against the SHA256 from the (GPG-verified) repository index, and APT still
verifies signatures end-to-end. **A malicious relay cannot poison the swarm**; at
worst it can decline to relay, or observe *that* two peers spoke and roughly how
much they said.

**What does change:** a node that opts into running the relay service spends a
bounded amount of its own bandwidth and connection slots carrying other peers'
coordination traffic. That is a real cost, borne by whoever opts in, and the
design below bounds it explicitly rather than hand-waving it.

## Design decisions

**D1. Fix the reservation, not the transfer. Never move packages over a relay.**
Circuit relay v2's limits (~128 KB / ~2 min) are not an obstacle to work around —
they are exactly right for our purpose. The relayed connection exists solely so
DCUtR can punch a direct path; the `.deb` then flows over that direct connection
at full speed. We must **not** be tempted to raise relay limits and stream
packages through relays: that would turn volunteer nodes into a bandwidth-funded
CDN and reintroduce a central bottleneck, which is the thing debswarm exists to
avoid. If the punch fails, we fall back to the mirror, as we do today.

**D2. The swarm supplies its own relays.** Rather than hardcode third-party relay
addresses (there is no reliable public pool offering open v2 reservations) or
operate infrastructure (a cost and a centralisation point), **publicly reachable
debswarm nodes run the relay service for the swarm.** This is a natural fit: a
seeding server or a LAN-server-mode cache box is usually already public, already
long-lived, and already altruistic. The chicken-and-egg risk is handled by D4.

**D3. Reachability decides the role; AutoNAT decides reachability.** A node
learns whether it is public from AutoNAT (`EvtLocalReachabilityChanged`). Public
→ eligible to *provide* relay service. Private → needs to *consume* one via
AutoRelay. Making this automatic avoids asking users a question they cannot
answer ("are you behind a NAT?").

**D4. Operator-configurable static relays, for the cases automation cannot
reach.** A private swarm (PSK) has no public debswarm nodes to discover, and the
public swarm needs a bootstrap period before enough upgraded nodes run the
service. Both are solved by letting an operator point at relays they control:
`[network] relay_peers`. For a private swarm with one public node, this makes
cross-NAT work immediately and with no dependence on the public DHT.

**D5. Relay service defaults to `auto`, not `on`.** A user who installs debswarm
on a public VPS should get a swarm that works, but should not be *surprised* into
carrying strangers' traffic without bound. `auto` = run the relay service **only
when AutoNAT says we are publicly reachable**, and **only within explicit
resource limits** (below). A user who wants nothing to do with it sets `off`; a
user who knows they are public behind a misbehaving AutoNAT sets `on`.

**D6. Ship observability before we ship claims.** The current situation — a
feature that is enabled, advertised, and non-functional — was possible because
nothing measured it. Reservations, DCUtR attempts/successes, and the
direct-vs-relayed breakdown of connections all become metrics, so "cross-NAT P2P
works" becomes a claim we can *check* rather than assert.

## Detailed design

### Configuration surface (`[network]`)

```toml
[network]
# Existing (unchanged)
enable_relay        = true   # circuit-relay client transport
enable_hole_punching = true  # DCUtR

# NEW — obtain a relay reservation so NAT'd peers are reachable at all.
# Without this, hole punching can never trigger. Default: true.
enable_autorelay = true

# NEW — run a circuit-relay v2 service for other peers.
#   auto (default) - only when AutoNAT reports us publicly reachable
#   on             - always (use when AutoNAT is wrong but you know you're public)
#   off            - never
relay_service = "auto"

# NEW — static relays to reserve on, in addition to any discovered from the
# swarm. Required for a private (PSK) swarm, where there is no public DHT to
# discover relays through. Full multiaddrs including /p2p/<peer-id>.
relay_peers = [
  # "/ip4/203.0.113.10/udp/4001/quic-v1/p2p/12D3KooW...",
]

# NEW — bounds on what we are willing to carry when acting as a relay.
# These are deliberately small: a relayed connection exists only long enough
# for two peers to hole-punch, never to carry a package.
[network.relay_limits]
max_reservations = 128     # concurrent peers we vouch for
max_circuits     = 16      # concurrent relayed connections
buffer_size      = "128KB" # per-circuit data cap (circuit-relay v2 default)
duration         = "2m"    # per-circuit lifetime (circuit-relay v2 default)
```

### Host construction (`internal/p2p/node.go`)

```go
// 1. Consume relays: reserve a slot so we are dialable behind NAT.
if cfg.EnableAutoRelay {
    if len(cfg.StaticRelays) > 0 {
        opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(cfg.StaticRelays))
    } else {
        // Discover relays from the swarm (see "Relay discovery" below).
        opts = append(opts, libp2p.EnableAutoRelayWithPeerSource(n.relayPeerSource))
    }
}

// 2. Provide relay: gated on reachability (see "Reachability" below).
//    EnableRelayService() is applied at construction only for relay_service="on";
//    "auto" starts the service dynamically once AutoNAT reports Public.
```

All of `EnableAutoRelayWithStaticRelays`, `EnableAutoRelayWithPeerSource`,
`EnableRelayService`, `ForceReachabilityPublic`, and `EnableAutoNATv2` are
present in the pinned `go-libp2p v0.48.0` — verified against the module, not
assumed.

### Reachability (`relay_service = "auto"`)

Subscribe to `event.EvtLocalReachabilityChanged` on the host's event bus:

- `network.ReachabilityPublic` → start the relay service (if not running).
- `network.ReachabilityPrivate` → stop it (if running), and rely on AutoRelay.
- `network.ReachabilityUnknown` → do nothing; stay in whatever state we're in.

Starting/stopping the v2 relay after host construction means holding the
`*relayv2.Relay` and calling `relay.New(host, opts...)` / `Close()` from the
reachability watcher, rather than passing `libp2p.EnableRelayService()` as a
construction option (which cannot be toggled). `ForceReachabilityPublic()` is
used by tests and by `relay_service = "on"`.

### Relay discovery (public swarm)

Public nodes running the relay service advertise themselves in the DHT under a
dedicated namespace (`/debswarm/relay/1.0.0`), reusing the existing provider
machinery. `relayPeerSource` is an `autorelay.PeerSource` that queries that
namespace and streams candidates to AutoRelay, which handles reservation,
renewal, and failover itself.

**Private swarms skip this entirely** — with a PSK there is no public DHT, so
`relay_peers` is the only mechanism, which is why D4 exists.

### What we deliberately do not do

- **No package bytes over relays** (D1). Chunk and full transfers continue to
  require a direct connection. If DCUtR fails, `downloadPackage` falls back to
  the mirror exactly as it does today — a slower path, not a broken one.
- **No raising of circuit-v2 limits.** The defaults are a feature.
- **No relay service on NAT'd nodes.** It would be useless (nobody can reach us
  to be relayed) and would waste the user's uplink.

### Metrics (`internal/metrics`)

| Metric | Why |
|---|---|
| `debswarm_relay_reservations{state="active\|failed"}` | Is step 1 — the thing that is broken today — actually happening? |
| `debswarm_holepunch_total{result="success\|failure"}` | Does DCUtR fire, and how often does it win? |
| `debswarm_connections{type="direct\|relayed"}` | Are we ending up direct (good) or stuck relayed (bad)? |
| `debswarm_relay_service_active` | Are we currently relaying for others? |
| `debswarm_relay_circuits_active` | What is that costing us? |
| `debswarm_reachability{state="public\|private\|unknown"}` | Which role did AutoNAT assign us? |

The dashboard grows a "Connectivity" row: reachability, reservation state, and
hole-punch success rate. A user should be able to answer *"is my node actually
participating in the swarm?"* at a glance — today they cannot.

## Testing

This is the part that has to be right, because **the existing test topology
cannot see this bug**. Containers on a Docker bridge can dial each other
directly, so today's soak would report success on a build where cross-NAT is
still completely broken. A test that cannot fail is not a test.

**NAT topology (`test/nat/docker-compose.yml`)**: three networks and a real NAT.

```
  peer-a ──┐                                    ┌── peer-b
           │  nat-a (iptables MASQUERADE)       │  nat-b (MASQUERADE)
           └──────────┐                ┌────────┘
                      └── public net ──┘
                              │
                          relay (public, runs relay service)
                          bootstrap/DHT
```

`peer-a` and `peer-b` sit on isolated subnets behind separate NAT gateways, with
no route to each other except through the public network. This reproduces the
real failure. Assertions:

1. **Baseline (must fail before the change):** `peer-a` cannot connect to
   `peer-b`. Guards against the fix silently regressing.
2. Both peers obtain a **relay reservation** (`relay_reservations{state=active}`
   ≥ 1) and advertise a `/p2p-circuit` address.
3. `peer-a` fetches a package that only `peer-b` has, and the transfer completes.
4. `holepunch_total{result=success}` ≥ 1 and the connection ends up
   **`type=direct`** — proving we punched through rather than quietly streaming
   over the relay (which D1 forbids and which would otherwise look like success).
5. The relay's `relay_circuits_active` returns to 0 after the punch.

**Symmetric-NAT case:** a variant where both gateways use random source ports
(true symmetric NAT). Hole punching *genuinely cannot* work here. The assertion
is that debswarm **degrades to the mirror and still serves the package** — i.e.
that failure is graceful, not a hang. This is the honest boundary of the feature
and it should be tested as such.

CI: the NAT topology runs as a new job. It is heavier than the current `e2e`, so
it may be nightly rather than per-PR if runtime demands it.

## Rollout

**Phase 1 — make it work at all.** AutoRelay (static + DHT peer source), relay
service with `auto` gating, reachability watcher, metrics, and the NAT test
topology. A private swarm with one public node, and a public swarm with any
upgraded public node, both gain working cross-NAT P2P.

**Phase 2 — make it work well.** Tune reservation churn and failover, dashboard
connectivity row, and revisit whether `relay_service` should default to `auto`
based on observed bandwidth cost from real deployments.

**Docs, on merge of Phase 1:** `docs/comparison.md` Relay Fallback flips from
"Partial" to a truthful description, and the README stops implying NAT traversal
already works. **Those edits land with the implementation, not before it** — the
current honest-but-unflattering wording is far better than a premature claim.

## Risks and open questions

- **Nobody opts in to relaying.** If every user sets `relay_service = off`, the
  public swarm has no relays and cross-NAT stays broken. Mitigations: `auto`
  default (public nodes help by default), tight resource bounds so the cost is
  genuinely small, and `relay_peers` for anyone who wants determinism. Worth
  watching the reservation metrics after release.
- **Symmetric NAT on both ends is unfixable.** Roughly a minority of home
  routers, but non-zero. The honest answer is mirror fallback (tested above), not
  a relay-streaming workaround.
- **Relay service on a metered/expensive uplink.** Bounded by
  `[network.relay_limits]`, and opt-out via `off`. Open question: should we
  auto-disable when a `max_upload_rate` is configured, on the theory that a user
  who caps their upload does not want to donate it?
- **AutoNAT can be wrong**, especially behind CGNAT that occasionally permits
  inbound. Hence `relay_service = "on"` as a manual override.
