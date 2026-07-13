# Backlog

Findings from a three-lens review (robustness/security, testing/operations,
product/features) conducted July 2026, plus the remainder of the performance
review that preceded it. Every item below was verified against the code at the
time of writing — file references are from that review. Items are removed as
they ship; see CHANGELOG.md for what already landed.

For longer-horizon feature direction, see `roadmap-2.0.md`. This document is
narrower: concrete, verified gaps in what exists today.

## Recently addressed (for context)

- v1.32.0 shipped three soak-validated performance batches (PRs #88–#90):
  fleet claim-window latency, real WAL mode, streaming mirror downloads,
  cache lock contention, index re-parse leak, stall-based timeouts,
  conditional GETs, log-noise cleanup.
- PR #92: chunked downloads write each byte once; parallel reannounce; fleet
  send deadlines.
- PR #93: download-state garbage collection (was dead code), cache self-heal
  after DB-corruption recovery (was: 500s until manual rebuild), eviction and
  peer-blacklist metrics, systemd watchdog (`Type=notify` + `WatchdogSec`),
  lz4/zstd/bz2 index support, CI watchdog-recovery test.
- v1.33.0 (2026-07-13): the reliability release — the apt-fallback drop-in (PR
  #94) resolves the former product gap "APT breaks when the daemon is down": the
  `.deb` now applies the proxy via `Proxy-Auto-Detect` with a liveness probe, so
  a stopped/crashed/removed daemon degrades to `DIRECT` instead of failing every
  `apt` operation (CI-tested each build).
- Trust-model honesty pass: reworded the overstated "cryptographically
  verified" / "signed index" / "a trusted host cannot serve a tampered package"
  claims across README, SECURITY.md, the docs, config examples, and code
  comments to state the real model — no daemon-side GPG check; APT's client-side
  verification is the guarantee. Robustness/security #1 below is now
  doc-complete; only the optional code hardening remains.
- **Repository metadata caching** (PR #98, resolves former product gap "metadata
  is never cached"): Release/InRelease, Packages/Sources, Translation, Contents,
  and DEP-11 are now cached in the (previously dead) `indices` table with their
  own LRU disk budget. A cold client revalidates against the local cache with a
  cheap conditional GET instead of re-downloading tens of MB per update;
  immutable `by-hash` files skip the upstream round-trip entirely, and cached
  Packages bytes re-warm the in-memory index after a restart. `[cache]
  cache_metadata` / `metadata_max_size`; metrics `debswarm_metadata_cache_*`.
- **Offline metadata serving** (PR #99, partially resolves the offline-mode gap):
  when the mirror is unreachable (network down, mirror outage, or the
  connectivity monitor reporting offline) the proxy serves the last cached
  metadata instead of failing `apt-get update`, marked `X-Debswarm-Stale: true`;
  an offline monitor short-circuits the doomed upstream call. APT still verifies
  the GPG signature and `Valid-Until`, so this is not a trust regression.
  `[cache] serve_stale_metadata` (default on); metric
  `debswarm_metadata_cache_stale_served_total`.
- **LAN cache-server mode** (resolves former product gap #1): the proxy can bind
  to a LAN interface (`[network] proxy_bind`) so a fleet shares one cache. The
  trust story is fail-closed — a non-loopback bind requires an explicit client
  CIDR allowlist (`[network] proxy_allowed_cidrs`) or the daemon refuses to start;
  the allowlist matches the client's real connection address (X-Forwarded-For is
  never trusted) and loopback is always allowed. When set it also gates the admin
  read surface, closing robustness/security #6 for opted-in operators. Design:
  `docs/design/lan-server-mode.md`.

## Product gaps (ranked by user value)

1. **`lan_only` mode: mirror suppression in the download racing path.** Offline
   `.deb` serving now works — cached metadata serves offline (PR #99), the
   in-memory index re-warms from cache after a restart, `apt-get install` of an
   already-cached package is served from disk offline, and a genuine miss while
   offline fails fast with 503 (all shipped). What remains is explicit `lan_only`
   gating: `downloadPackage` still builds a mirror source and runs the final
   mirror fallback even in `ModeLANOnly`, so a LAN-only node can still reach the
   WAN. The fix belongs inside `downloadPackage` (nil the mirror source for the
   parallel downloader and skip the final mirror `Stream`), but it touches the
   racing path — deferred to keep the offline change low-risk.
2. **Cross-NAT P2P doesn't work; docs claim it does.** Only the relay client
   transport and hole punching are enabled — no AutoRelay reservation logic,
   and no debswarm node ever runs the relay service, so DCUtR has no relayed
   connection to coordinate through. Two NAT'd peers can never connect.
   Fixing this properly means a relay story (static relays config,
   `EnableAutoRelayWithStaticRelays`, optionally `EnableRelayService()` on
   publicly reachable nodes) — and until then, `docs/comparison.md`
   ("Relay Fallback: Yes") should be corrected.
3. **No apt repository or container image for debswarm itself.** Distribution
   is GitHub releases + `curl | bash`. No signed apt repo means no
   `unattended-upgrades` and no fleet-wide upgrade path — ironic for an APT
   tool. No Dockerfile/OCI image/Helm chart exists.
4. **Source packages get zero benefit.** Sources indices are deliberately not
   parsed and `.dsc`/`.orig.tar.*` fall through to passthrough, despite
   Sources carrying SHA256s that would make verification identical to the
   `.deb` path. Build farms are a natural audience.
5. **Smaller**: `rollback fetch` from P2P is a stub while the README
   advertises it; no mirror remapping/failover (per-mirror stats are
   collected but never used for selection); no per-repo cache stats or
   quotas; cache pinning is by SHA256 prefix only.

## Robustness / security

1. **Trust model: docs corrected (this pass); daemon-side hardening remains.**
   The overstated claims were reworded across README, SECURITY.md, the docs,
   config examples, and code comments — so the model is now stated accurately.
   What's left is the *optional code* work below. The daemon performs no GPG
   verification: the SHA256 it verifies against comes from a Packages index
   fetched over the same (usually `http://`) upstream leg as the package
   bytes, so the proxy's own check provides no MITM resistance upstream. The
   end-to-end guarantee is APT's client-side GPG verification — which holds —
   but `[trusted=yes]` repos, `Acquire::AllowInsecureRepositories`, or
   `dpkg -i` of proxy-fetched files inherit attacker-controlled bytes.
   Remaining options: verify Packages against the signed Release in the daemon
   (openpgp), and default more hosts to HTTPS upstream. (The doc reword is done.)
2. **Peer blacklisting is in-memory and Sybil-trivial.** A restart clears all
   blacklists; an offender reconnects under a fresh peer ID. Verification
   prevents poisoning, so this is a deterrence gap, not a correctness one.
   Persistent reputation or per-IP throttling would raise attacker cost.
3. **No default upload-bandwidth cap.** Upload concurrency is slot-limited
   (20 global / 4 per peer) but `max_upload_rate` defaults to unlimited; a
   few peers repeatedly requesting large cached packages can saturate a
   node's uplink.
4. **Corruption-recovery orphan accounting.** PR #93 made orphaned entries
   self-heal on access, but orphaned files that are never re-requested still
   escape size accounting and eviction until a `cache rebuild`. A post-
   recovery automatic rebuild (or startup orphan sweep) would finish the job.
5. **Fleet message hash validation.** `handleFetching` inserts entries keyed
   by unvalidated remote input (any string up to 1024 bytes). Bounded by the
   message queue and reaper, and fleet peers are LAN/PSK-scoped — but
   validating 64-hex on ingest is cheap defense-in-depth.
6. **Metrics endpoints leak the package inventory when bound non-locally.**
   Mutating API routes are loopback-gated; `GET /api/cache/packages*`,
   `/stats`, and `/dashboard` are not. Binding to `0.0.0.0` is warned about
   in logs but exposes the full installed-package list.

## Testing / operations

1. **Coverage holes where failures live**: `internal/proxy` ~49% overall with
   the CONNECT tunnel, retry worker, and `handleHealth` near 0%;
   `cmd/debswarm` ~23% (daemon lifecycle, SIGHUP reload untested).
2. **CI**: no real-APT e2e (`apt-get update`/`install` driven through the
   proxy — the documented pipelining blind spot); the three fuzz targets and
   committed corpus never run in CI; no two-node P2P job; no nightly; the
   Codecov check is informational only.
3. **SQLite schema versioning**: no `PRAGMA user_version`; migrations swallow
   errors (`_, _ = db.Exec(ALTER …)`); an old binary against a new schema is
   undetected and untested.
4. **Graceful shutdown holes**: the metrics HTTP server is never shut down;
   hijacked CONNECT tunnels are invisible to `http.Server.Shutdown` and get
   hard-killed.
5. **`/health` is liveness-only**: degraded states (`dht: no_peers`,
   `p2p: no_connections`) are reported but never flip the status, so
   orchestrators cannot see degradation. (The systemd watchdog added in
   PR #93 covers hangs, not degradation.)
6. **`--log-file` has no rotation** (the audit log does). SIGHUP reload
   applies only rate limits and silently ignores every other config change.
7. **Docs**: no production sizing/capacity guidance (`MemoryMax=512M` is
   hardcoded in the unit with no rationale); `scripts/install.sh` and README
   still show the older `DynamicUser` unit variant, inconsistent with the
   packaged hardened unit (and install.sh must stay `Type=simple` until a
   release with sd_notify support ships).

## Performance (remainder)

1. **Fleet LAN downloads buffer the whole file in memory** — `p2pNode.Download`
   returns `[]byte`; fixing it needs a streaming P2P transfer API. Deferred
   from the 2026-07 performance batches as the only remaining structural item.
2. Smaller: per-chunk 4MB allocations bypass any pooling (shape depends on
   the streaming API above). (The `indices` table, formerly dead schema, is now
   the metadata cache — see Recently addressed.)

## Verified fine (don't re-audit without cause)

SSRF/redirect/CONNECT validation including DNS-rebind re-checks; P2P transfer
frame hardening (size caps, fixed-length decode, allocation caps, deadlines);
path-traversal guards on cache keys; disk-full handling in the cache write
path; identity/PSK file permissions; the packaged systemd unit hardening;
fuzz targets with committed corpus; in-process two-node e2e tests; the
ReadTimeout regression guard; by-hash and flat-repo handling; Ubuntu phased
updates (client-side, nothing for the proxy to do).
