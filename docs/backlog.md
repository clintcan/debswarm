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

## Product gaps (ranked by user value)

1. **APT breaks when the daemon is down.** The `.deb`'s apt drop-in hard-sets
   `Acquire::http::Proxy` with no `Proxy-Auto-Detect` liveness script (the
   apt-cacher-ng pattern: probe the daemon, fall back to `DIRECT`). A stopped,
   crashed, or removed-but-not-purged daemon makes every `apt` operation fail.
   Small packaging fix; the single most likely way a deployment gets hurt.
2. **No LAN cache-server mode.** The proxy bind is hardcoded to `127.0.0.1`
   (`cmd/debswarm/daemon.go`); only the metrics endpoint is configurable.
   Other machines/containers/CI runners cannot use a debswarm box as their
   APT cache, foreclosing the one-cache-per-office/lab/CI-fleet deployment
   and any Kubernetes DaemonSet story. Needs a bind option plus a hard think
   about the trust story for remote clients.
3. **Repository metadata is never cached — only `.deb`s.** Packages files are
   parsed then discarded; Release/InRelease, Translation-*, Contents-*,
   DEP-11 are pure passthrough. Every host re-fetches all metadata from the
   WAN each update cycle (304 relay softens this only when upstream sends
   validators). Likely the biggest recurring-bandwidth delta vs apt-cacher-ng.
4. **Offline / `lan_only` mode is documented but not wired.** The
   connectivity monitor's mode is consumed exactly once — for display in
   `/health`. No download path consults it, the in-memory index is never
   persisted (`Index.cachePath` is unused), and `apt-get update` fails hard
   offline even with a full cache. `docs/comparison.md` overclaims here.
5. **Cross-NAT P2P doesn't work; docs claim it does.** Only the relay client
   transport and hole punching are enabled — no AutoRelay reservation logic,
   and no debswarm node ever runs the relay service, so DCUtR has no relayed
   connection to coordinate through. Two NAT'd peers can never connect.
   Fixing this properly means a relay story (static relays config,
   `EnableAutoRelayWithStaticRelays`, optionally `EnableRelayService()` on
   publicly reachable nodes) — and until then, `docs/comparison.md`
   ("Relay Fallback: Yes") should be corrected.
6. **No apt repository or container image for debswarm itself.** Distribution
   is GitHub releases + `curl | bash`. No signed apt repo means no
   `unattended-upgrades` and no fleet-wide upgrade path — ironic for an APT
   tool. No Dockerfile/OCI image/Helm chart exists.
7. **Source packages get zero benefit.** Sources indices are deliberately not
   parsed and `.dsc`/`.orig.tar.*` fall through to passthrough, despite
   Sources carrying SHA256s that would make verification identical to the
   `.deb` path. Build farms are a natural audience.
8. **Smaller**: `rollback fetch` from P2P is a stub while the README
   advertises it; no mirror remapping/failover (per-mirror stats are
   collected but never used for selection); no per-repo cache stats or
   quotas; cache pinning is by SHA256 prefix only.

## Robustness / security

1. **The trust model is overstated in docs.** The daemon performs no GPG
   verification: the SHA256 it verifies against comes from a Packages index
   fetched over the same (usually `http://`) upstream leg as the package
   bytes, so the proxy's own check provides no MITM resistance upstream. The
   end-to-end guarantee is APT's client-side GPG verification — which holds —
   but `[trusted=yes]` repos, `Acquire::AllowInsecureRepositories`, or
   `dpkg -i` of proxy-fetched files inherit attacker-controlled bytes.
   Options: verify Packages against the signed Release in the daemon
   (openpgp), default more hosts to HTTPS upstream, and at minimum reword
   "all packages cryptographically verified" claims.
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
   the streaming API above); the `indices` ETag table in the cache schema is
   dead code.

## Verified fine (don't re-audit without cause)

SSRF/redirect/CONNECT validation including DNS-rebind re-checks; P2P transfer
frame hardening (size caps, fixed-length decode, allocation caps, deadlines);
path-traversal guards on cache keys; disk-full handling in the cache write
path; identity/PSK file permissions; the packaged systemd unit hardening;
fuzz targets with committed corpus; in-process two-node e2e tests; the
ReadTimeout regression guard; by-hash and flat-repo handling; Ubuntu phased
updates (client-side, nothing for the proxy to do).
