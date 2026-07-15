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
./run.sh              # normal run   — Tier 1 must PASS
./run.sh --baseline   # AutoRelay off — Tier 1 must FAIL (proves the test detects the bug)
```

`KEEP=1 ./run.sh` leaves the stack up for inspection (`docker compose down -v` to
clean up). Requires Docker with Compose v2 and a working Go toolchain (the script
cross-builds a `linux/amd64` binary into this directory).

## Two tiers

**Tier 1 — the core proof (asserted; reproducible everywhere).** A NAT'd peer
obtains a circuit-relay **reservation** on the relay — verified on the relay side,
and mirrored by the peer's AutoRelay adding the relay. This is the exact mechanism
that was **entirely absent** before the cross-NAT work: `EnableRelay()` gave only
the client transport, nothing ever reserved, so no `/p2p-circuit` address existed,
nothing could dial a NAT'd peer, and hole punching (which only fires over an
existing relayed connection) could never trigger. The `--baseline` run disables
AutoRelay and asserts the relay sees **no** reservation — so Tier 1 passing while
the baseline fails proves both that the fix works and that the test can detect its
absence.

**Tier 2 — the full transfer (best-effort; reported, not asserted).** `peer-b`
caches a package the mirror then **stops** serving; `peer-a` fetches it, so its
only possible source is `peer-b`, across two NATs, via a hole punch. This needs the
peer↔relay connection to stay up across the reservation-to-transfer gap.

> **Environment note.** On **Docker Desktop for Windows**, the LinuxKit/WSL2 NAT
> drops the idle peer↔relay connection at ~28 s (QUIC idle timeout / conntrack
> expiry) — **not** a debswarm behaviour; nothing in debswarm closes connections
> on that cadence, and the drop is identical over TCP and QUIC. So Tier 2 is
> reported as *environment-limited* there rather than failing the run. On real
> Linux (a Linux host or CI) the connection persists and Tier 2 completes. Run it
> there for the end-to-end path.

## Files

- `docker-compose.yml` — the topology (networks, gateways, relay, repo, peers).
- `Dockerfile.node` — one image, three roles (relay | gateway | peer), plus
  `GOLOG_LOG_LEVEL` so libp2p's own relay/autorelay/holepunch logs are visible.
- `Dockerfile.repo` — the flat apt "mirror", built offline at image-build time.
- `entrypoint.sh` — role dispatcher: sets up NAT, writes each node's config,
  publishes the relay's peer id to the peers.
- `run.sh` — orchestration and assertions.
