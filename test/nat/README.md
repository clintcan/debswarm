# Cross-NAT P2P test

Proves that two debswarm peers behind **separate NAT gateways** — with no route
to each other except out through a public network — can discover each other and
transfer a package via a circuit-relay reservation and a hole punch.

This is the case the ordinary Docker-bridge soak (`test/e2e`) **cannot** see: on a
shared bridge, containers dial each other directly, so that soak passes even on a
build where cross-NAT P2P is completely broken. Here, a real `iptables MASQUERADE`
gateway stands between each peer and the public network.

## Topology

```
  peer-a ──[lan-a]── nat-a ──┐                     ┌── nat-b ──[lan-b]── peer-b
         (192.168.10.0/24)   ├──[ public ]─────────┤     (192.168.20.0/24)
                             │  11.11.11.0/24      │
                         relay (circuit-relay service + DHT bootstrap)
                         repo  (the "mirror": a tiny flat apt repo)
```

- `peer-a` and `peer-b` are on separate bridges Docker keeps isolated, and neither
  is attached to `public`, so each can reach the relay/repo **only** through its
  own NAT gateway (masqueraded). They cannot reach each other directly.
- The public subnet **`11.11.11.0/24`** must clear **two** independent address
  filters, or a NAT'd peer never advertises a usable relay address and Tier 2 fails
  against a *correct* implementation:
  1. **debswarm's SSRF filter** (`internal/security`) drops RFC1918/loopback/
     link-local from DHT provider records — so the relay can't sit on a private IP,
     or a relayed peer's `/ip4/<relay>/…/p2p-circuit/…` address is discarded.
  2. **libp2p autorelay's `cleanupAddressSet`** keeps only `manet.IsPublicAddr()`
     relay addresses when building a peer's `/p2p-circuit` addrs. `manet` treats the
     RFC 5737 **documentation ranges** (`192.0.2/24`, `198.51.100/24`,
     **`203.0.113/24`** — the old choice here) as *unroutable*, i.e. **not** public.
     A relay on any of those yields an **empty** circuit-addr set: the reservation is
     granted but no circuit address is ever advertised, so two NAT'd peers can never
     find each other — a silent, environment-independent failure.
  `11.11.11.0/24` is in neither `manet.Private4` nor `manet.Unroutable4` (so
  `IsPublicAddr` is true) **and** is not RFC1918 (so debswarm's filter passes it).
  It only ever routes inside the Docker networks, so borrowing otherwise-real DoD
  space is self-contained. **Do not "fix" this back to a TEST-NET range** — that is
  the very trap that makes Tier 2 fail.

## Running

```bash
cd test/nat
./run.sh               # normal run    — Tier 1 must PASS
./run.sh --baseline    # AutoRelay off  — Tier 1 must FAIL (proves the test detects the bug)
./run.sh --relay-data  # relayed transfers on — Tier 2 must COMPLETE the fetch over the relay
```

`KEEP=1 ./run.sh` leaves the stack up for inspection (`docker compose down -v` to
clean up). Requires Docker with Compose v2 and a working Go toolchain (the script
cross-builds a `linux/amd64` binary into this directory).

## Two tiers

**Tier 1 — the core proof (asserted; reproducible everywhere).** Each NAT'd peer
obtains a **usable** circuit-relay reservation: the relay grants a slot **and** a
`/p2p-circuit` address enters the peer's own advertised set (asserted via the
peer-side `debswarm_relay_reservations_obtained_total` metric, **not** just the
relay-side grant log — a grant with no resulting circuit address is a reservation
nobody can use, and asserting only the grant hides exactly that failure). This is
the mechanism that was **entirely absent** before the cross-NAT work: `EnableRelay()`
gave only the client transport, nothing ever reserved, so no `/p2p-circuit` address
existed, nothing could dial a NAT'd peer, and hole punching (which only fires over
an existing relayed connection) could never trigger. The `--baseline` run disables
AutoRelay and asserts **no** reservation/circuit address appears — so Tier 1 passing
while the baseline fails proves both that the fix works and that the test detects
its absence.

**Tier 2 — the full transfer (best-effort; reported, not asserted).** `peer-b`
caches a package the mirror then **stops** serving; `peer-a` fetches it, so its only
possible source is `peer-b`, across two NATs. With Tier 1 working, `peer-a`
**discovers `peer-b`** through the DHT (via its circuit address) and a DCUtR **hole
punch fires** — both of which the test reports. The transfer only *completes* if the
hole punch **succeeds**, and that depends on the NAT type.

> **Why Tier 2 does not complete on this rig (and it is not a debswarm bug).** The
> gateways are plain `iptables MASQUERADE`, i.e. **address-dependent ("symmetric")
> NATs**: `peer-b`'s external `ip:port` mapping is bound to the relay it dialed, so
> `peer-a`'s inbound SYN to that `ip:port` is refused (`connection refused`). DCUtR
> **cannot** punch through symmetric NAT — a fundamental property of hole punching,
> true on any host, not a WSL2/Docker artifact. And debswarm relays **coordinate the
> punch but never carry package bytes** (circuit-v2's tiny limits are a feature), so
> a failed punch falls back to the mirror rather than relaying the data. A
> *completed* cross-NAT transfer therefore needs at least one **hole-punchable**
> (endpoint-independent / full-cone) NAT — which a stock `MASQUERADE` gateway is not.
> Making the rig demonstrate a completed transfer means giving a gateway full-cone
> behaviour (e.g. an `xt_FULLCONENAT` / `nft`-based endpoint-independent mapping) —
> **or** enabling the relayed-transfer fallback, which is what `--relay-data` does.

**`--relay-data` — complete the transfer without a hole-punchable gateway.** This
run sets `relayed_transfer_max_bytes` on the peers and raises the relay's
`relay_limits.buffer_size`, so when the punch fails on the symmetric gateways the
small package rides the **relay** instead. Tier 2 then **asserts** `bytes_from_relay
> 0` — an end-to-end cross-NAT fetch proven without any full-cone NAT infrastructure.
It exercises the `relayed_transfer_max_bytes` gate (see
`docs/design/relay-data-fallback.md`): every relayed byte is still SHA256-verified,
and the relay carries only the end-to-end-encrypted stream, so this trades bandwidth
for reach, not safety.

## Files

- `docker-compose.yml` — the topology (networks, gateways, relay, repo, peers).
- `Dockerfile.node` — one image, three roles (relay | gateway | peer), plus
  `GOLOG_LOG_LEVEL` so libp2p's own relay/autorelay/holepunch logs are visible.
- `Dockerfile.repo` — the flat apt "mirror", built offline at image-build time.
- `entrypoint.sh` — role dispatcher: sets up NAT, writes each node's config,
  publishes the relay's peer id to the peers.
- `run.sh` — orchestration and assertions.
