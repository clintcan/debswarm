# Design: relayed-transfer fallback for symmetric-NAT'd peers

**Status:** Implemented (v1.40.0)
**Author:** debswarm maintainers
**Date:** 2026-07-15
**Follow-up to:** `docs/design/cross-nat-p2p.md` (cross-NAT P2P Phase 1) — closes the
gap that Phase 1 surfaced but did not solve.

## Implementation notes (as shipped)

The design below is the plan; this section records what actually landed and what was
deferred, so the doc stays honest.

- **Config:** `[network] relayed_transfer_max_bytes` (int, default `0`) with accessor
  `NetworkConfig.RelayedTransferMaxBytes()` (clamps negatives to 0), wired into
  `p2p.Config.RelayedTransferMax` in both `daemon.go` and `seed.go`.
- **Client gate (`internal/p2p`):** `Node.DownloadRange` classifies the path with
  `onlyRelayedConn` (true when every connection to the peer is `Stat().Limited`).
  `relayedTransferSkipped` refuses a relay-only peer when the cap is 0 — returning an
  error so the caller falls back to the mirror, without penalizing the peer's score.
  Otherwise the stream is opened with `network.WithAllowLimitedConn`, and after the
  size header `relayedSizeExceeded` resets the stream and refuses when `size > cap`.
  Both decisions are pure helpers in `relay.go`, unit-tested in `relay_transfer_test.go`;
  the pre-connect guard also treats `network.Limited` connectedness as "connected".
- **Effective cap:** the client bound is `relayed_transfer_max_bytes`; the relay's
  per-circuit `buffer_size` (circuit-v2 `Data` limit) is an independent hard ceiling
  the relay enforces, so the effective cap is `min(client, relay)` with no negotiation.
- **Metrics shipped:** `debswarm_bytes_from_relay_total` (a subset of the existing
  `{source="peer"}` bytes) and `debswarm_relayed_transfer_total{result="ok"|"too_large"}`.
- **Racing:** unchanged — a relayed peer participates through the existing `PeerSource`
  closure, so it joins the P2P-vs-mirror race automatically; no source-selection code
  changed.
- **NAT rig:** `test/nat` gains an opt-in `--relay-data` mode (enables the feature on
  the peers and raises the relay `buffer_size`) whose Tier 2 asserts `bytes_from_relay > 0`
  — a completed cross-NAT fetch without a hole-punchable gateway. The default rig run is
  unchanged.
- **Deferred:** the relay-side `debswarm_relay_bytes_carried_total` (the
  `relayMetricsTracer.BytesTransferred` hook is still a no-op) and the `result="failed"`
  label; a `config wizard` Private-Swarm default; and per-peer/day byte quotas for a
  public opt-in relay (see Risks).

## Context

Cross-NAT P2P Phase 1 wired up the full path: a NAT'd peer obtains a circuit-relay
reservation, advertises a `/p2p-circuit` address, another NAT'd peer discovers it
through the DHT, forms a relayed connection, and DCUtR **hole-punches** to a direct
connection over which the package transfers. Phase 1 is verified end-to-end (see the
`test/nat/` rig): reservations form, discovery works, and the hole punch fires.

But the hole punch **only succeeds through hole-punchable NATs**. DCUtR cannot punch
through a **symmetric (address-and-port-dependent) NAT**: the peer's external mapping
is bound to the *destination it dialed* (the relay), so an inbound packet from a
third party to that same `ip:port` is dropped. When **both** peers sit behind
symmetric NATs, the punch can never succeed — this was reproduced directly in the
`test/nat/` rig (`dial <peer>:4001: connect: connection refused`).

Symmetric NAT is not a corner case. Carrier-grade NAT (CGNAT), many corporate
firewalls, and some mobile networks are effectively symmetric. For every peer pair
where both endpoints are symmetric-NAT'd, debswarm's cross-NAT P2P **silently falls
back to the upstream mirror** and never transfers peer-to-peer — the exact outcome
Phase 1 set out to fix, for a large slice of the internet.

`docs/design/cross-nat-p2p.md` made a deliberate choice here — "relays coordinate
hole punches only, never carry package bytes" — treating circuit-v2's small limits
as a feature. **This document reconsiders that choice for a bounded case:** relaying
*small* packages over the already-established relayed connection when (and only when)
the hole punch has failed, so two symmetric-NAT'd peers can still exchange data.

### What exists today (verified against `HEAD`)

- **The transfer cannot use a relayed connection at all — this is a code fact, not
  just policy.** `Node.DownloadRange` (`internal/p2p/node.go:855`) connects to the
  provider and opens its stream with a plain `n.host.NewStream(ctx, id, proto)`
  (`node.go:882`). libp2p **refuses to open a stream over a "Limited" connection**
  (its flag for a circuit-v2 relayed connection — see `internal/p2p/relay.go:424`,
  `c.Stat().Limited`) unless the caller passes `network.WithAllowLimitedConn`. So on a
  failed punch the relayed connection is already open, and debswarm tears it down and
  goes to the mirror instead of using it.
- **The relay's data budget is already configurable.** `relayResourcesFrom`
  (`internal/p2p/relay.go`) sets the circuit-v2 `Resources.Limit.Data` from
  `[network] relay_limits.buffer_size` (default `DefaultRelayBufferSize` = 128 KB) and
  `Limit.Duration` from `relay_limits.duration` (default 2 min). "Coordinate-only" is
  enforced by that small default budget **plus** the client never using the
  connection. The relay side already has the knob; the missing half is client-side.
- **Source accounting already separates P2P from mirror.** `/stats` exposes
  `bytes_from_p2p` and `bytes_from_mirror`; the racing engine (small files race P2P
  vs mirror, first wins) already picks among sources.

## What this does and does not change (trust model)

The key reason this is a **cost decision, not a security decision**: relaying data is
safe by construction.

- **Integrity is unchanged.** Every byte is still checked against the SHA256 from the
  repository index before it is cached or served. A relay that flips a bit is caught
  and the source is blacklisted — identical to a malicious *peer*. A relay **cannot
  poison the swarm.**
- **Confidentiality is preserved.** A circuit-v2 relay carries the **end-to-end
  encrypted** libp2p stream (Noise/TLS) between the two peers. The relay sees
  ciphertext, not package contents — it is a blind pipe.
- **Therefore a malicious or curious relay can only drop or delay traffic**, never
  read or forge it. APT's own client-side GPG verification remains the outer
  guarantee, exactly as today.

What *is* on the table is purely: **who pays the relay's bandwidth, and does carrying
data open an abuse surface** (a public relay becomes a bandwidth proxy). Those are the
axes the design below optimizes.

### Scope and honest value

- **LAN is unaffected.** debswarm's primary case — a LAN/fleet using mDNS + the fleet
  coordinator — never touches a relay. This feature is strictly about **WAN P2P
  between two peers that are both symmetric-NAT'd**: real, but secondary to the LAN
  story.
- **"Small" means "≤ the relay's circuit budget."** Bounding to a small size helps the
  **long tail of small packages** (config packages, `-dev` headers, small utilities)
  by *count*, not by *bytes* — large packages still hit the mirror.
- **The win is mirror-offload and resilience, not raw speed.** A relay adds latency and
  is rate-limited; a peer that can already reach a fast mirror will not go faster.
  Relaying small packages helps swarms that want to **minimize upstream egress** or
  keep working when the mirror is slow/restricted/unreachable — most valuable to
  private, bandwidth-constrained deployments.

## Design decisions

1. **Opt-in, default off; align cost with beneficiary.** Relaying data is disabled by
   default. It is turned on by the operator who wants it, because the operator running
   the relay is who pays for it. This keeps a bare relay cheap to run — which the
   Phase 2 default public relay depends on — and avoids turning public relays into
   open bandwidth proxies without consent.

2. **Lead with private (PSK) swarms.** The highest-value, lowest-risk case: a PSK
   swarm's relay is the operator's own infrastructure serving **closed membership**,
   so there is no external abuse surface, and the explicit goal is to minimize WAN
   egress. This is the beachhead; the public case is a later, separate call.

3. **Bound by `min(client threshold, circuit budget)`.** The client never attempts a
   relayed transfer larger than its configured threshold, and the circuit-v2 `Data`
   limit is the hard ceiling regardless. Both are small by default.

4. **Race against the mirror; relay only when the mirror is the alternative.** A relayed
   source is used only when the fallback would otherwise be the mirror — never in
   preference to a direct (hole-punched or publicly-reachable) peer. Where debswarm
   already races P2P vs mirror for small files, the relayed source joins that race, so
   latency never regresses: whichever finishes first wins.

5. **No new relay-side "carry data" flag.** `relay_limits.buffer_size` already *is* the
   knob — a relay carries whatever a circuit's `Data` budget allows. Raising it turns a
   coordinate-only relay into a data-carrying one. We document that (with the bandwidth
   caveat) rather than add a redundant boolean.

6. **Make the carried bytes visible.** A distinct `bytes_from_relay` counter (separate
   from `bytes_from_p2p` for direct and `bytes_from_mirror`) so an operator can see
   exactly what their relay is carrying and set budgets accordingly.

## Detailed design

### Configuration surface

Client side (`[network]`, `internal/config/config.go`):

```toml
[network]
# NEW — max package size (bytes) this node will fetch over a RELAYED connection
# when a direct/hole-punched path is unavailable (e.g. both peers symmetric-NAT'd).
# 0 (default) disables relayed transfers entirely: a failed hole punch falls back
# to the mirror, as today. The effective cap is min(this, the relay's circuit
# Data budget). Keep small — this is for the long tail of small packages, and the
# bytes are carried by whoever runs the relay.
relayed_transfer_max_bytes = 0        # e.g. 262144 (256 KiB) on a private swarm
```

Relay side — **no new field.** Documented behaviour of the existing knob:

```toml
[network.relay_limits]
# A relay carries at most this many bytes per circuit. The default (128 KiB) is
# sized for hole-punch coordination only. RAISING it lets your relay carry small
# package transfers for symmetric-NAT'd peers that cannot hole-punch — at the cost
# of YOUR bandwidth. Do this on a relay you run for your own (e.g. PSK) swarm;
# leave it at the default on a public relay unless you intend to donate bandwidth.
buffer_size = "128KB"
```

### Client: allow a bounded limited-connection transfer

In the download path (`Node.DownloadRange` / its full-file sibling):

- When the chosen provider is reachable **only** over a relayed (Limited) connection —
  i.e. `Connectedness` is limited, or `Connect` yields a `Stat().Limited` connection —
  and `relayed_transfer_max_bytes > 0`:
  - Refuse early if the expected size (known from the index) exceeds
    `min(relayed_transfer_max_bytes, negotiated circuit Data budget)`; fall back to the
    mirror.
  - Otherwise open the stream with
    `network.WithAllowLimitedConn(ctx, "debswarm-transfer")` and transfer as normal.
    The existing SHA256 check runs unchanged.
- When `relayed_transfer_max_bytes == 0`, behaviour is byte-for-byte today's: a Limited
  connection is never used for transfer, and a failed punch falls back to the mirror.

Source selection / racing:

- The relayed source is added to the **existing** small-file race (P2P vs mirror) so it
  never delays a peer that can reach the mirror. It is preferred over the mirror only
  when the mirror is unreachable or explicitly deprioritized (e.g. `lan_only`).
- A direct connection (hole-punched, or a publicly-reachable peer) is always preferred
  over a relayed one; the relayed transfer is strictly a fallback.

### Relay: documentation only

No code change on the relay beyond what Phase 1 shipped. `relayResourcesFrom` already
maps `buffer_size`/`duration` onto the circuit limits. The only relay-side work is
documentation (config example + `docs/configuration.md`) explaining the bandwidth
trade-off of raising `buffer_size`, and a recommended conservative value for anyone who
opts a **public** relay into carrying data.

### Metrics (`internal/metrics`)

- `debswarm_bytes_from_relay_total` — bytes this node fetched over a relayed connection
  (client view); mirrors the existing `bytes_from_p2p` / `bytes_from_mirror`.
- `debswarm_relayed_transfer_total{result="ok"|"too_large"|"failed"}` — relayed-transfer
  attempts and why they did/didn't happen.
- On the relay: the existing `debswarm_relay_circuits_active` already reflects load; the
  circuit-v2 `MetricsTracer` (`relayMetricsTracer`) already sees `BytesTransferred` —
  wire it to a `debswarm_relay_bytes_carried_total` so an operator can bound cost.

## What we deliberately do not do

- **Not on by default, and not on public/auto relays by default.** Carrying bytes for
  strangers is opt-in. The Phase 2 default public relay stays coordinate-only unless its
  operator raises `buffer_size` deliberately.
- **No large packages over relays.** The circuit `Data` limit (and the client threshold)
  are hard caps; big packages always use the mirror or direct-peer chunked download.
- **No un-verified relayed bytes.** The SHA256 check is unchanged; relayed data is
  verified exactly like direct-peer or mirror data.
- **No preferring a relay over a reachable direct/mirror path.** Relayed transfer only
  fills the gap where the alternative is "nothing" (or a deprioritized mirror).
- **No relay-side content inspection.** The relay carries an encrypted stream; it does
  not and cannot look inside, and we add nothing that would change that.

## Testing

- **Unit:** the size-bound decision (`expected > min(threshold, budget)` ⇒ skip relay,
  go to mirror), and that `WithAllowLimitedConn` is passed exactly when a Limited
  connection is the chosen path and the feature is enabled.
- **`test/nat/` rig — this feature lets the symmetric-NAT rig finally demonstrate a
  *completed* transfer.** Today Tier 2 asserts discovery + a hole-punch *attempt* and
  reports the transfer as blocked by the rig's symmetric `MASQUERADE` gateways. With
  `relayed_transfer_max_bytes` set on the peers and `buffer_size` raised on the relay,
  the same rig should complete the transfer **over the relay** despite the punch
  failing — turning the current "reported, not asserted" line into an asserted
  `bytes_from_relay > 0`. That is a cheaper way to prove end-to-end symmetric-NAT
  transfer than building a full-cone (`xt_FULLCONENAT`) gateway.
- **Docker soak:** confirm a peer that can reach the mirror does **not** regress
  (relayed source loses the race, mirror still serves), and that with the mirror down a
  symmetric-NAT'd peer now fetches a small package over the relay.

## Rollout

1. Land behind `relayed_transfer_max_bytes = 0` (off). No behaviour change for anyone
   who does not set it.
2. Document it as a **private-swarm** feature first (`config wizard` "Private Swarm"
   profile can offer a sensible non-zero default + a raised relay `buffer_size`).
3. Evaluate a public-relay policy separately, alongside the Phase 2 default relay —
   likely still off by default there, with a documented opt-in and a conservative
   budget.

## Risks and open questions

- **Bandwidth abuse on an opted-in public relay.** Mitigated by the per-circuit `Data`
  and `Duration` limits, per-reservation caps, and (for PSK swarms) closed membership.
  Open question: do public opt-in relays also need a per-peer/day byte quota beyond the
  per-circuit limit?
- **Circuit `Duration` (2 min default) vs transfer time.** A small package well within
  the byte budget should finish in seconds, but a slow relay + a near-budget package
  could brush the duration limit. Option: derive a minimum duration from
  `buffer_size / a floor bandwidth`, or document tuning both together.
- **Interaction with the racing engine.** Must guarantee the relayed source never delays
  a peer that can reach the mirror — the race already gives this, but the wiring needs a
  test that a losing relayed source is cancelled promptly (the existing stream-reset on
  race loss, `node.go` ~889, should cover it).
- **Should PSK swarms default to on?** Leaning yes-via-wizard rather than a silent
  default, so the bandwidth choice is always explicit.
- **Does this weaken the incentive to fix reachability?** A peer that *could* be
  hole-punchable might settle for relayed transfers. Preferring direct paths in source
  selection (decision 4) keeps the incentive aligned.
