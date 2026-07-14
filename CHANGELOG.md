# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Flat-layout repositories are now GPG-verified.** Daemon-side signature verification previously covered only dist-layout repositories (those with a `/dists/<suite>/` tree); a flat-layout repository such as `pkgs.k8s.io` was reported `no-dist` and served unverified. debswarm now anchors a flat repo's index to the signature-verified `Release`/`InRelease` that sits in the same directory (and lists index files by bare name), exactly as it does for a dist repo. A flat repository whose signing key debswarm can discover therefore gets full `auto`/`enforce` protection with no extra configuration. Only a flat repo with no cached, verified `Release` falls back to the indecisive `no-release` result (served-and-flagged under `warn`/`auto`).

### Changed
- **The default `https_upstream_hosts` set now covers more common HTTPS repositories**: `download.docker.com`, `deb.nodesource.com`, `packages.microsoft.com`, `apt.releases.hashicorp.com`, and `apt.postgresql.org` join `pkgs.k8s.io` (all already in the default allowlist). With `trust_known_repos` on, pointing a `sources.list` entry for one of these at `http://` lets debswarm fetch it over HTTPS and cache, verify, and P2P-share it — instead of it becoming an opaque, uncacheable `CONNECT` tunnel. Only `http://` requests are upgraded; APT's own signature verification is unaffected.

## [1.35.0] - 2026-07-14

### Added
- **`auto` mode for upstream signature verification**: a new value for `[security] verify_upstream_signatures`, between `warn` and `enforce`. `auto` refuses an index **only when verification was possible and it failed** — a signature-verified `Release` exists for the repository but the index does not match it (a hash mismatch, or a file the `Release` does not list) — and otherwise behaves like `warn` (serve and flag) when verification could not be attempted at all: no trusted key for the repository, a flat/no-`dists` repository, or no cached `Release`. The effect is real, fail-closed protection for every repository whose signing key debswarm can discover (Debian, Ubuntu, and any third-party repo with a `signed-by=` key), while a repository debswarm cannot verify is never turned into a hard failure. Unlike `enforce`, `auto` needs no `keyring_path`/`verify_exempt_hosts` for repos it cannot verify and never fails daemon startup on a missing keyring or metadata cache (it degrades to `warn`). New metric label `debswarm_upstream_verify_total{result="not-listed"}` distinguishes "the signed Release does not list this index" from "no verified Release available" (`no-release`).

### Changed
- **Upstream signature verification now defaults to `auto`** (previously `warn`). Out of the box, debswarm will now refuse an index that a signature-verified `Release` proves has been tampered with (returning `502` so APT retries/fails cleanly), while still serving — and flagging with `X-Debswarm-Unverified` — any index it simply cannot verify (no discoverable key, a flat repo, or no cached `Release`). This turns on real anti-tampering protection for Debian, Ubuntu, and every third-party repo with a discoverable signing key without breaking unverifiable repositories. It is safe as a default precisely because it degrades to the old `warn` behavior whenever verification is not possible, and it never fails daemon startup on a missing keyring or metadata cache. Set `verify_upstream_signatures = "warn"` to restore the pre-`auto` observe-only behavior, or `"off"` to disable verification entirely.

## [1.34.0] - 2026-07-14

### Added
- **Repository metadata caching**: debswarm now caches repository index files (Release/InRelease, Packages/Sources, Translation, Contents, DEP-11) in addition to `.deb` packages. Previously all metadata was pure passthrough, so every host re-fetched the full set — often tens of MB per `apt-get update` — from the WAN. Now a cold client (a fresh CI container, a reimaged host, any machine with an empty `/var/lib/apt/lists`) fetches metadata from the local cache after a cheap conditional GET. Under normal operation every cached file is revalidated against the mirror before use, so the proxy does not serve stale metadata, and APT's own signature verification is unaffected. Immutable `by-hash` index files are served with no upstream round-trip at all, and cached Packages bytes also re-warm the in-memory index after a daemon restart. Controlled by `[cache] cache_metadata` (default on) and `[cache] metadata_max_size` (default `1GB`, a budget kept separate from the package cache so the two never evict each other). New metrics: `debswarm_metadata_cache_hits_total`, `debswarm_metadata_cache_misses_total`, `debswarm_metadata_cache_bytes_saved_total`, `debswarm_metadata_cache_size_bytes`. This activates the previously-unused `indices` table in the cache database.
- **`apt-get update` keeps working when the mirror is unreachable**: building on metadata caching, when the mirror is down — the network is offline, the mirror is having an outage, or the connectivity monitor reports offline — the proxy now serves the last cached copy of the metadata instead of failing the update, and marks the response with an `X-Debswarm-Stale: true` header. When the monitor already knows it is offline, the doomed upstream request is skipped entirely. This does not weaken security: APT still verifies the GPG signature **and** the `Valid-Until` field of whatever is served, so a genuinely expired `Release` file is rejected by APT itself. Controlled by `[cache] serve_stale_metadata` (default on; set false to make an unreachable mirror a hard error even when a cached copy exists). New metric: `debswarm_metadata_cache_stale_served_total`.
- **LAN server mode**: the proxy can now bind to a LAN interface so other machines use one debswarm box as their shared APT cache — one cache per office, lab, or CI fleet — instead of every host running its own daemon. Set `[network] proxy_bind` to a LAN IP (or `0.0.0.0`) and list the permitted client networks in `[network] proxy_allowed_cidrs`. Security is fail-closed: a non-loopback bind **requires** an allowlist or the daemon refuses to start, the allowlist is matched against the client's real connection address (a spoofed `X-Forwarded-For` cannot grant access), and loopback is always allowed so local APT is unaffected. The trust model is unchanged — the proxy still fetches only from allow-listed Debian mirrors and each client's APT still verifies GPG end-to-end, so a LAN client is no less safe than a local one. When an allowlist is set it also gates the admin read endpoints (`/stats`, `/dashboard`, cache inventory) if the metrics server is bound non-loopback, closing that unauthenticated-exposure gap; mutating API routes stay loopback-only. New flag `--proxy-bind`. Default (`127.0.0.1`) behavior is unchanged.
- **`apt-get install` of a cached package works offline on a cache-server**: completing the offline story for the LAN server mode above. The in-memory index that maps a package URL to its SHA256 is normally warmed at startup by the aptlists watcher (from `/var/lib/apt/lists`) and by live `Packages` requests — but a dedicated debswarm cache-server never runs `apt-get update` locally, so its `/var/lib/apt/lists` is empty and, after a restart, the only record of a cached package's hash is debswarm's own metadata cache. Previously the proxy couldn't resolve those packages and fell through to an uncached passthrough that failed offline. The proxy now warms the index once from cached `Packages` metadata on the first unresolved package request, then serves the hit entirely from disk with no network. (On a normal host whose apt lists are present this warm is a no-op — the index is already warm.) Independently, when a package is genuinely not cached and the node is offline (no internet and no mDNS peers), the request now fails fast with `503` instead of grinding through the doomed fleet/DHT/P2P/mirror chain. (`lan_only` mirror suppression in the download path remains a follow-up.)
- **Daemon-side upstream GPG verification (optional)**: debswarm can now verify each repository's GPG-signed `Release` against a trusted keyring, and each `Packages` index against that signed `Release`, before trusting the SHA256 it lists — anchoring the hash to GPG instead of to the mirror's word. This hardens the cases APT's own client-side check does not cover: `[trusted=yes]` / `AllowInsecureRepositories` repos, `dpkg -i` of a file pulled from the cache, and the packages debswarm caches and seeds to P2P peers. Controlled by `[security] verify_upstream_signatures`: `off`, `warn` (default — verify and report via a metric, a log, and an `X-Debswarm-Unverified` header, but always serve, so behavior is identical to before from APT's point of view), or `enforce` (fail-closed: refuse an index whose `Release` fails signature or hash verification). Trusted keys are auto-discovered from the host's APT keyrings (`/etc/apt/trusted.gpg{,.d}`, `/usr/share/keyrings`, `/etc/apt/keyrings`), so debswarm trusts exactly what APT trusts; `[security] keyring_path` adds more (needed on an apt-less cache-server in `enforce`). Verification reads the signed `Release` from the metadata cache, so it needs `[cache] cache_metadata`; the daemon fails fast on `enforce` with no keys or no metadata cache. APT's own end-to-end verification is unaffected in every mode, and `Valid-Until` is left to APT so offline stale serving still works. New dependency `github.com/ProtonMail/go-crypto`; new metric `debswarm_upstream_verify_total{result}`.

## [1.33.0] - 2026-07-13

### Added
- **APT keeps working when the daemon is down**: the packaged apt configuration now applies the proxy via `Acquire::http(s)::Proxy-Auto-Detect` with a liveness probe (`/usr/lib/debswarm/apt-proxy-detect`, a 1-second TCP connect) instead of a hard `Acquire::http::Proxy` setting. A stopped, crashed, mid-upgrade, or removed-without-purge daemon now degrades to direct mirror access instead of making every `apt` operation fail — and unlike the old failure mode (which returned exit 0 with warnings, so scripts never noticed), the fallback is exercised in CI on every build. Per-host `DIRECT` overrides keep working.
- **systemd watchdog**: the service now runs as `Type=notify` with `WatchdogSec=90`. The daemon reports readiness via sd_notify and feeds the watchdog only while its own HTTP loop answers `/health`, so a deadlocked-but-alive daemon is automatically killed and restarted by systemd instead of hanging APT until a human notices. CI includes a recovery test that freezes the daemon with SIGSTOP and verifies systemd restarts it.
- **Operator metrics for the events that matter**: new `debswarm_cache_evictions_total` (sustained growth means the cache is undersized), `debswarm_peers_blacklisted_total` (the primary security-operational signal — the corresponding audit event, previously dead code, is now emitted too), and `debswarm_cache_max_size_bytes` (so dashboards can compute fill percentage). The already-recorded DHT query and multi-source verification metrics are now actually exported at `/metrics`, along with an aggregate peer-latency histogram.
- **lz4-, zstd-, and bzip2-compressed APT lists are now parsed**: Ubuntu minimized/cloud images write `/var/lib/apt/lists` as `.lz4` by default; those files were previously scanned as raw binary and silently contributed zero index entries, quietly disabling restart recovery on those platforms. By-hash payloads also detect zstd/lz4 magic bytes.
- **Verified backlog document** (`docs/backlog.md`): concrete, code-verified gaps from a July 2026 three-lens review (product, robustness/security, testing/operations), ranked, with an explicit "verified fine" list.

### Changed
- **Chunked downloads write each byte once**: chunks now go straight into the preallocated assembly file at their offsets instead of into per-chunk files that were re-read and re-written — half the disk writes and reads for every large download (measured: a 200MB download went from 401MB written/400MB read to 201MB/200MB), which matters on the SD-card-class storage fleet nodes often run on. Resume is tracked in the state database plus the partial assembly file; chunk files persisted by older versions are re-downloaded and cleaned up.
- **Large downloads use full parallelism in the common topology**: chunk-worker concurrency is no longer capped by the number of sources, so one peer plus a mirror now runs up to 8 chunks in flight instead of 2.
- **Package reannouncement is 4-way concurrent**: a cache of thousands of packages previously took hours per reannounce cycle (one multi-second DHT walk at a time), undermining the announce interval.
- **Fleet messages have a 5-second write deadline**: one peer with a full flow-control window (crashed, suspended, overloaded) could previously block fleet coordination for everyone indefinitely.
- **`debswarm_bytes_uploaded_total` is no longer labeled per peer** and peer latency is an aggregate histogram: full peer IDs made the metric series set grow without bound on a public-DHT node. Dashboards using the per-peer label must switch to the aggregate.
- **Codecov statuses are informational**: coverage is reported, not gated, matching how the project already treats it.

### Fixed
- **Failed downloads no longer leak disk and database rows forever**: the cleanup routine existed but was never called, and partial assembly files are deliberately kept for resume — so downloads that exhausted their retry window accumulated indefinitely. An hourly task now purges stale state and orphaned partial directories (live downloads exempt).
- **The cache self-heals after database-corruption recovery**: recovery creates a fresh database but leaves package files on disk, and every such package previously returned HTTP 500 to APT until a manual `cache rebuild`. An unreadable cache entry is now treated as a miss — the re-download re-caches the package and restores its metadata row automatically.
- **Client disconnect noise, continued**: fetch failures caused by APT hanging up mid-request are logged at DEBUG (completing the v1.32.0 change for the remaining code paths).

## [1.32.0] - 2026-07-13

### Added
- **Conditional index revalidation (HTTP 304)**: APT's `If-Modified-Since`/`If-None-Match` headers are now forwarded to the mirror, upstream `304 Not Modified` responses are relayed, and `Last-Modified`/`ETag` validators are passed back so clients can revalidate next time. Release/InRelease requests revalidate unconditionally; Packages requests revalidate only when the daemon's in-memory index already holds that file (after a daemon restart, APT's cache is warm but the proxy's is empty — accepting a 304 then would leave every package unverifiable, so the proxy fetches in full instead). Measured effect: a repeat `apt-get update` against unchanged repositories now transfers **0 bytes** from the mirror (previously ~254KB of InRelease bodies every run), and a direct index refetch costs a zero-byte 304 instead of a full 8.8MB download.
- **Fleet "don't have" replies**: fleet peers that neither have nor are fetching a wanted package now answer with an explicit NACK (new `MsgDontHave` message), so the requester resolves its claim window as soon as every reached peer has answered — at LAN round-trip speed — instead of always waiting out the full `claim_timeout`. Older peers ignore the unknown message type and the timer remains as the backstop, so mixed-version fleets keep working.

### Changed
- **Mirror downloads stream through the cache**: the mirror fallback — the default download path on any node without P2P providers — no longer buffers the whole package in memory. The response body streams into the cache (hashed and verified while writing to disk) and is served from the cached file. Measured effect: downloading a 96MB package previously spiked the daemon's peak memory by ~309MB; it now adds nothing. Packages with no signed index entry stream directly from the mirror to the client for the same reason.
- **Each download is hashed once**: the fleet and P2P paths verified downloads with a separate hashing pass and then hashed the same bytes again while caching; verification now happens inside the single cache write. Corrupt data still blacklists the serving peer and is never served.
- **Cache reads no longer serialize behind writes**: a cache hit used to take the cache's exclusive lock and issue a synchronous database write before serving, so concurrent hits queued behind each other and behind any in-flight store; storing a package held that lock across the entire disk copy. Cache hits now take a shared lock and record access times in memory (persisted in batches — flushed on shutdown, before eviction ranking, and whenever stats are read), and the store's disk copy runs outside the lock.
- **Mirror timeouts bound stalls, not whole transfers**: the previous blanket 60-second HTTP timeout aborted any healthy download that simply took longer — a large package on a slow link died mid-body and was re-downloaded from byte zero on each retry, up to three times, before failing APT. The fetcher now bounds time-to-first-byte and per-read progress instead: a transfer that stalls for the window is aborted and retried, while a slow-but-steady one runs to completion.
- **Cache eviction is indexed**: eviction candidates are ranked via a matching partial expression index instead of a full-table scan and sort on every over-budget store.

### Fixed
- **The cache database now actually runs in WAL mode**: the SQLite journal-mode parameter used a DSN syntax the bundled driver silently ignores, so the cache has always run in rollback-journal mode with `synchronous=FULL` — paying journal-file churn and double fsyncs on every commit, with writers blocking readers. The pragmas are now applied correctly (WAL, `synchronous=NORMAL`, 10s busy timeout) and a regression test queries the live connection so a silently-ignored parameter can never ship again.
- **Cold package downloads no longer stall for the fleet claim window**: with fleet enabled (the default since v1.30), every download of a package no fleet peer had waited the full 5-second `claim_timeout` before falling back to the mirror — and the fleet treated **all** connected peers (including unrelated public-DHT peers) as fleet members, both broadcasting to them and waiting on them. Fleet coordination now addresses only genuinely mDNS-discovered LAN peers (in a PSK private swarm with mDNS disabled, all connected peers still count — the key guarantees they are swarm members), and combined with the new NACK replies, cold-package latency dropped from ~5s to ~0.1s in a two-node soak.
- **Losing downloads stop promptly**: when debswarm races multiple peers (and the mirror) for a small package, the losers' transfers were never actually canceled — each kept receiving the entire file, wasting bandwidth and holding the remote peers' limited upload slots. Canceled transfers now reset the stream immediately, and a canceled transfer no longer counts against the peer's score (losing a race is not the peer's fault).
- **Repository index re-parses no longer leak memory**: every `apt-get update` that re-fetched a Packages file kept the entire previous parse reachable (roughly 20–30MB per re-parse of Debian main), growing without bound in a long-running daemon. Re-parses now replace exactly the entries the same index file contributed previously — tracked per index file, since multiple dists and architectures of one repository share a repository key and must never clear each other. Measured effect: live heap across 10 re-parses went from ~398MB (unbounded growth) to ~106MB (flat). The never-used `Description` and `SHA512` fields are also no longer parsed or stored.
- **Client disconnects are no longer logged as server errors**: APT routinely abandons redundant index requests during `apt-get update`; the aborted fetch was logged as `ERROR Failed to fetch index … context canceled` on every update. Fetch failures caused by the client hanging up are now logged at DEBUG.

## [1.31.0] - 2026-07-13

### Added
- **Uncached-serve observability**: when a package is proxied straight from the mirror without caching, verification, or P2P sharing (because no signed index entry was found for it), debswarm now increments `debswarm_packages_served_uncached_total` (also exposed in `/stats` as `packages_served_uncached`) and logs an INFO notice once per repository host explaining the cause and the fix (fetch the repository's index through the proxy via `apt-get update`). Previously this was only visible at DEBUG with no metric, so a stalled cache / no-P2P situation was undiagnosable at the default log level.
- **Per-`.deb` checksums in releases**: each released `.deb` now ships a `<deb>.sha256` sidecar so downloads can be verified with `sha256sum -c` (goreleaser's `checksums.txt` covers only the tarballs).

### Changed
- **Connectivity probe now uses HTTP**: the auto-detect connectivity check defaults to `http://deb.debian.org/debian/` instead of `https://deb.debian.org`, so it measures mirror reachability rather than TLS trust. An HTTPS probe fails on hosts without a CA bundle even when the mirror is reachable, mis-reporting an online node as `lan_only`/`offline`. Override with `connectivity_check_url`.
- **Proxy passthrough streams instead of buffering**: the passthrough path now streams the upstream response to the client rather than reading the whole body into memory; an upstream failure still returns `502` before any bytes are sent.
- **Dropped `apt-transport-https`** from `Recommends`: it has been a transitional dummy package since apt 1.5 (HTTPS support is built into apt), so it installed nothing useful on supported targets.
- **CI now smoke-tests the built `.deb`**: the packaging job installs the freshly built package and verifies the binary, dpkg conffiles, the `ca-certificates` dependency, the systemd unit/user, that the daemon starts from the packaged config with fleet on, and that it serves a request through the proxy — catching packaging regressions before release.

## [1.30.1] - 2026-07-13

### Fixed
- **Package now depends on `ca-certificates`**: the daemon opens its own HTTPS connections — for the upstream-HTTPS fetch feature (e.g. `pkgs.k8s.io`), for any `https://` mirror, and for the connectivity-check probe — all of which validate against the system CA trust store. On minimal images that ship without `ca-certificates` (many cloud and container base images), those connections failed with `x509: certificate signed by unknown authority`: the v1.30.0 flagship upstream-HTTPS feature could not verify TLS, and the connectivity monitor mis-reported the node as `lan_only`/`offline`. The `.deb` now declares `Depends: … , ca-certificates`, so a CA bundle is present on install. Plain-HTTP mirror traffic and SHA256 package verification were unaffected and are unchanged.

## [1.30.0] - 2026-07-12

### Added
- **Upstream HTTPS fetch**: the proxy can now open its own HTTPS connection to a mirror while APT talks plain HTTP to the local proxy, so HTTPS-only repositories (e.g. `pkgs.k8s.io`) can be cached, SHA256-verified, and shared over P2P — none of which an opaque HTTPS `CONNECT` tunnel can do. Enabled per-host via the new `[proxy] https_upstream_hosts` option; known HTTPS-only repos (`pkgs.k8s.io`) are included automatically when `trust_known_repos` is enabled. Only `http://` requests to listed hosts are upgraded; cache keys, index lookups, and P2P content addressing are unaffected.
- **Trusted third-party repositories by default**: common repositories now work through the proxy without configuration — Launchpad PPAs (`ppa.launchpad.net`, `ppa.launchpadcontent.net`, `launchpadlibrarian.net`), `download.docker.com`, `apt.postgresql.org`, `deb.nodesource.com`, `packages.microsoft.com`, `apt.releases.hashicorp.com`, `mirrors.kernel.org`, and `pkgs.k8s.io`. Controlled by the new `[proxy] trust_known_repos` option (default `true`; set to `false` for a strict Debian/Ubuntu/Mint-only posture). SSRF protection and SHA256 verification are unchanged.
- **Flat-layout repository support**: repositories that serve metadata and packages directly, without a `dists/pool` tree (e.g. Kubernetes `pkgs.k8s.io`), are now recognized by APT request shape (`Release`, `InRelease`, `Packages*`, `Sources*`, `by-hash/`, `*.deb`) and proxied. Arbitrary non-repository files on an allowed host remain blocked.
- **Configuration wizard: repositories step**: `debswarm config wizard` now asks whether to trust the curated third-party repository set (`trust_known_repos`) and lets you list additional hosts (`allowed_hosts`). Both are written explicitly to the generated config rather than left implicit, and the summary reports which HTTPS-only repos will be fetched over HTTPS upstream. Previously the wizard never mentioned repositories, so users with a private mirror or an HTTPS-only repo had no way to discover those options.
- **Configuration wizard: edit or start over**: when an existing config is detected, the wizard now asks up front whether to edit it (every prompt pre-filled with your current value) or start from scratch (discard it and begin from the defaults, after a confirmation). Previously the only way to reset was to pick a deployment profile, which reset *some* fields (cache size, rates, mDNS, fleet, metrics bind) while silently keeping others (ports, bootstrap peers, PSK path, allowed hosts) — not a real reset, and not obvious which was which.

### Changed
- **LAN peer-to-peer sharing is now on by default**: `[fleet] enabled` defaults to `true` (was `false`). mDNS discovery was already on by default, so nearby nodes discovered each other but did not actually share; now nodes on the same LAN share cached packages over P2P and coordinate to avoid redundant WAN downloads of the same package — debswarm's core dedup feature now works out of the box. Every shared package is still SHA256-verified against the signed index. Set `[fleet] enabled = false` for an isolated node that should not share on the LAN.
- **Packaged default config reflects the v1.30 defaults**: the config the `.deb` installs to `/etc/debswarm/config.toml` now includes a `[proxy]` section documenting `trust_known_repos`, `allowed_hosts`, and `https_upstream_hosts`, and makes `[fleet] enabled = true` explicit with a note that LAN sharing is on by default and how to opt out. Previously these were absent, so a fresh install ran with the (correct) code defaults but gave no sign of them in the installed config. The redundant, out-of-date inline config in the Debian post-install script was removed so the shipped conffile is the single source of truth. A regression test loads the packaged config and asserts the recommended defaults.
- **Configuration wizard: cleaner yes/no prompts**: yes/no questions no longer print the `[Y/n]` hint twice (e.g. `Enable LAN discovery (mDNS)? [Y/n] [Y/n]:`).
- **Clearer blocked-request errors**: when the proxy refuses a repository it now returns `403` with a message naming the host and pointing to `proxy.allowed_hosts` (and distinguishes SSRF-blocked internal addresses), instead of an opaque `400 "Invalid request"`. The HTTPS `CONNECT` rejection is similarly descriptive.

### Fixed
- **`apt-get update` hung on large indices with default APT pipelining**: the proxy's HTTP server set a blanket `ReadTimeout` of 30s. `ReadTimeout` is a deadline on a connection's whole request cycle, and on the keep-alive/pipelined connections APT uses by default it fired mid-handler once a large index (e.g. the ~8 MB Debian `main` `Packages` file) plus APT's pipelining pushed the cycle past 30s — canceling the request and stalling `apt-get update` (the small indices, and single non-pipelined fetches, were unaffected). Switched to `ReadHeaderTimeout` (slow-loris protection on headers only), so large indices fetch cleanly under default APT. Found via a Docker soak with a real APT client.
- **A failed fleet send made a peer permanently unreachable**: the fleet protocol caches one stream per peer, but when a send failed (peer disconnected or reset the stream) the dead stream stayed cached, so every later send to that peer reused the broken stream and failed — even after the peer reconnected. A failed send now evicts and resets the stream, so the next send dials a fresh one.
- **Cache size accounting drifted when a file could not be deleted**: cache eviction deleted the DB row and decremented the tracked size before removing the file, and if the removal failed (e.g. a file locked by another process on Windows) it only logged the error — leaving an untracked orphan file on disk while `currentSize` under-reported real usage. Because `ensureSpace` gates on `currentSize`, the physical cache could then grow past `max_size`. Deletion now removes the file first and only drops the row / adjusts the size when that succeeds; a locked file is left intact and skipped by the eviction loop, keeping the accounting consistent with what is on disk.
- **Announcement worker could panic on shutdown**: `Shutdown` closed `announceChan` before the HTTP server drained, so an in-flight request or retry goroutine finishing a download during shutdown could `announceAsync` a send on the closed channel and panic (recovered per-request by `net/http`, but aborting that request with a stack trace). The worker now exits on context cancellation and the channel is never closed, so late sends are simply dropped.
- **Debug/installer packages (`.ddeb`/`.udeb`) were passed through unverified**: `classifyRequest` only treated `.deb` as a package, so `.udeb` (installer) and `.ddeb` (debug-symbol) files fell through to the passthrough path and were fetched from the mirror without SHA256 verification, caching, or P2P — even though the security layer already recognizes them as package files. They are now classified as packages.
- **Empty download-assembly directories leaked**: after a chunked download was moved into the cache, its per-download directory (`partial/{hash}/`, or a temp dir when resume is disabled) was left behind — the assembly file had been renamed away and the dir sat empty forever. The proxy now removes that directory once it is done with the assembly file.
- **Midnight-crossing schedule windows matched the wrong days**: a spanning window (e.g. `weekday` `22:00`–`06:00`) applied the same `current >= start || current < end` test whether the day matched today or via "yesterday", conflating the evening and morning halves. So a "weeknights only" window was also active on Saturday nights and early Monday mornings. `Contains` now checks each half against the correct day — the evening half against a configured start-day, the morning half against the day after one.
- **P2P range transfers silently failed for files larger than ~160 MB**: a range request was written as a fixed 81-byte binary frame (hash + big-endian start/end offsets + newline) but the receiving peer parsed it with `ReadBytes('\n')`. When an offset byte equalled `0x0A` (`'\n'`) — which happens for chunk offsets at multiples of 4 MB starting around 160 MB — the frame was truncated, failed the length check, and the peer answered "not found", so those chunks silently fell back to the mirror. The request is now encoded and decoded through a shared fixed-size helper (`encodeRangeRequest`/`decodeRangeRequest`) that reads by length instead of scanning for a newline. Correctness was never at risk (mirror fallback plus SHA256 verification), but P2P range sharing now works for large packages.
- **Concurrent fleet messages could corrupt the peer stream (data race)**: `SendMessage` encoded onto a per-peer cached stream with no write lock, while `Message.Encode` issues several sequential writes. Different goroutines send concurrently — per-request `WantPackage`/`Notify*`, the progress-broadcaster ticker, and inbound-message responses — so two sends to the same peer could interleave their writes and, because framing is length-prefixed, permanently desync that peer's stream (as well as being a plain data race on `Write`). Writes to each cached stream are now serialized by a per-stream mutex; the stream cache also no longer leaks a stream when two goroutines create one for the same peer at once.
- **Simultaneous LAN requests for the same uncached package all hit the mirror (fleet dedup defeated)**: when two or more fleet nodes wanted a package that no peer had cached yet, each only answered a peer's `WantPackage` if it already had the file or was already fetching it — neither true during the brief window before anyone had started — so every node timed out and downloaded from the WAN independently, defeating the coordination fleet exists to provide (the `Nonce` election field was effectively dead in this path). Nodes now settle the race with the nonces carried on the `WantPackage` broadcasts: the lowest nonce fetches once from the mirror and the rest wait and pull the package over the LAN. Losers wait on the globally lowest-nonce fetcher directly rather than an intermediate, so three or more simultaneous requesters cannot form a wait chain; an elected fetcher that fails or never reports releases its waiters to fall back to a normal download (bounded by the wait cap). Also fixes a related waiter-loss bug: `NotifyFetching` overwrote the in-flight entry wholesale and discarded callers already queued as waiters (stranding them until the 5-minute cap); it now updates the entry in place. Matters more now that LAN sharing is on by default.
- **Stale fleet fetch entries from a silent peer lingered**: when a peer was recorded as fetching a package (via the election above or a `Fetching` broadcast) but then went quiet — it crashed, or satisfied the request from its own cache/LAN without announcing completion — the in-flight entry was never cleaned up (`FetchState.IsStale` was defined but never called). A local caller waiting on that peer only recovered after the full `max_wait_time`, and a later request for the same package would attach to the dead fetcher. A periodic reaper now drops peer-fetcher entries that have gone `StaleTimeout` (60s) without a progress update and releases their waiters so they fall back immediately; our own in-flight downloads (`Fetcher == ""`) are never touched. Live fetchers broadcast progress every second, so a slow-but-alive download is not mistaken for a dead one.
- **Flat-layout repos were never cached, verified, or P2P-shared**: `GetByURLPath` returned no package for a flat-repo URL (e.g. `pkgs.k8s.io`) because such URLs have no `dists/`/`pool/` path, so it bailed before the basename fallback. The proxy then computed an empty expected hash and streamed the `.deb` straight from the mirror with no SHA256 verification, caching, or DHT announce — silently defeating the caching/verification/P2P goal for exactly the flat repos the upstream-HTTPS feature targets. `GetByURLPath` now falls back to a repo-preferred basename lookup when the URL yields no path.
- **Packages with URL-encoded characters (notably `+`) were never cached, verified, or P2P-shared**: APT percent-encodes `+` as `%2B` in package URLs, and `+` appears in a large fraction of Debian/Ubuntu versions (`+deb12u2`, `+dfsg`, `+b1`, …). `ExtractPathFromURL` sliced the raw URL without decoding, so the extracted path (`…vim-runtime_9.0.1378-2%2bdeb12u2_all.deb`) never matched the index key, which is stored from the unescaped Packages `Filename:` (`…+deb12u2…`). `GetByURLPath` returned nil, the proxy computed an empty expected hash, and the package was streamed straight from the mirror with no SHA256 verification, no caching, and no P2P — silently, since APT still received a working file (its own signed-index check backstopped safety). The path is now percent-decoded before lookup, restoring caching, P2P sharing, and debswarm's independent hash check for the affected packages. Found via a Docker soak: a simultaneous four-node download of a `+`-versioned package hit the mirror four times and cached nothing; with the fix one node fetches from the mirror and the other three pull it over the LAN.
- **Per-peer rate limiting aborted P2P transfers and blamed the peer**: the composed (global + per-peer) rate-limited reader/writer called `rate.WaitN` with the full byte count, which errors whenever a single read/write exceeds a finite limiter's burst. A stream read larger than the per-peer burst (common for bulk transfers; the per-peer burst floor is 64 KB) therefore failed the transfer and recorded a spurious failure against the innocent peer. The composed reader/writer now split into burst-sized waits (sized to the smaller of the two limiters), matching the non-composed `LimitedReader`/`LimitedWriter` that already did this.
- **A verified download returned HTTP 500 to APT when it could not be cached**: after a chunked download completed and passed SHA256 verification, `processDownloadSuccess` only logged a failed `PutFile` (e.g. cache full) but still told the caller to serve from cache, so `servePackageResult` did a `cache.Get`, missed, and returned `500 "Cache error"` — for a package that had downloaded fine. The verified file is now served directly (read into memory, consistent with the racing/mirror paths, and the temp copy removed) when caching fails, so APT gets its package.
- **Download retry worker never retried anything**: resume state was persisted with an empty URL, so the retry worker — which is enabled by default (`retry_max_attempts = 3`, `retry_interval = "5m"`) — skipped every failed download it found via its `state.URL == ""` guard and silently did nothing. The mirror URL is now stored alongside the download state, so failed downloads are actually retried. Affects large chunked downloads, the only path that persists resume state.
- **Configuration wizard reset your settings instead of editing them**: re-running `debswarm config wizard` always started from the built-in defaults and never read your existing config, so pressing Enter through every prompt — the natural way to "keep my settings" — silently overwrote them (cache size, ports, rate limits, log level, allowed hosts, everything). The prompts even showed the *default* as the current value, giving no hint that anything was being discarded. This also made the wizard destructive when run without a TTY: `debswarm config wizard < /dev/null` overwrote an existing config with defaults, with no user input at all. The wizard now loads the existing config (same precedence as the daemon: `--config`, `/etc/debswarm/config.toml`, `~/.config/debswarm/config.toml`), defaults every prompt to your current value, and saves back to the file it read. Step 1 now defaults to "Keep current settings"; choosing a deployment profile still overwrites profile-managed values, but only after an explicit confirmation.

### Security
- **Mirror redirect SSRF protection**: HTTP redirects returned by mirrors are now validated on every hop; redirects to loopback, private, link-local, or cloud-metadata addresses are refused, while legitimate public cross-host redirects (e.g. PPA → CDN) still work
- **Loopback-only cache mutation API**: the `pin`, `unpin`, and `delete` cache endpoints now reject non-loopback clients with 403 (the metrics server may bind to a non-local address and these endpoints have no authentication)
- **Hard-fail on mirror hash mismatch**: a mirror download whose SHA256 does not match the signed Packages index is now rejected (error, `VerificationFailures` metric, and audit event) instead of only being logged

### Tests
- Added tests for mirror redirect safety (including hex-encoded loopback) and loopback API enforcement (IPv4/IPv6)
- Added tests for upstream HTTPS fetch (scheme upgrade, subdomain/case handling, explicit `:80` stripping) and `EffectiveHTTPSUpstreamHosts` merging
- Covered blank entries in `https_upstream_hosts`: a stray `""` in the host list is skipped rather than treated as a wildcard matching every host. `upstreamFetchURL`, `isHTTPSUpstreamHost`, and `EffectiveHTTPSUpstreamHosts` are now all at 100% statement coverage.

### Dependencies
- Bumped `modernc.org/sqlite` from 1.44.3 to 1.53.0
- Bumped `github.com/libp2p/go-libp2p-kad-dht` from 0.37.1 to 0.41.0
- Bumped `github.com/pelletier/go-toml/v2` from 2.2.4 to 2.4.3
- Bumped `github.com/fsnotify/fsnotify` from 1.9.0 to 1.10.1
- Bumped `golang.org/x/sys` from 0.40.0 to 0.47.0

### CI
- Updated CI and release workflows to Go 1.25 (matches the `go 1.25.0` directive in `go.mod`)
- Upgraded the `gosec` security scanner to v2.27.1 and annotated 16 verified false-positive findings (intentional byte conversions and operator-supplied directory paths)
- Bumped `actions/checkout` from 6.0.2 to 7.0.0
- Bumped `actions/setup-go` from 6.4.0 to 6.5.0
- Bumped `codecov/codecov-action` from 5.5.2 to 7.0.0
- Bumped `golangci/golangci-lint-action` from 9.2.0 to 9.3.0
- Bumped `goreleaser/goreleaser-action` from 7.2.1 to 7.2.3

## [1.29.0] - 2026-02-08

### Added
- **Configuration wizard**: Interactive setup via `debswarm config wizard` for new installations
  - 3 deployment profiles: Home user, Seeding server, Private swarm
  - Guided prompts for cache size, bandwidth limits, ports, mDNS, fleet, log level
  - Inline validation with re-prompt on invalid input (cache size, rates, ports)
  - PSK generation integrated for private swarm profile
  - Summary display with confirm-before-save
  - `--output` flag for custom config file path
  - Standard library only — no new dependencies

### Tests
- 8 new wizard tests with simulated stdin: all profiles, custom ports, invalid input re-prompt, abort save, custom output path, debug log level

## [1.28.0] - 2026-02-08

### Added
- **Dashboard charts**: Real-time throughput visualization with 4 live-updating canvas charts
  - Throughput chart: P2P vs mirror bytes/sec (dual area, green/orange)
  - Request rate chart: Requests per second (area, blue)
  - P2P ratio chart: Percentage of traffic from P2P (area, green)
  - Connected peers chart: Peer count over time (line, blue)
  - 5-minute rolling window (60 data points at 5s intervals)
  - Client-side history accumulation with counter-diff rate derivation
  - Custom canvas renderer (~150 lines inline JS, no external libraries)
  - HiDPI/Retina display support via devicePixelRatio scaling
  - Responsive 2x2 grid layout (1-column on mobile)
- **Nonce-based CSP**: Dashboard uses per-request `crypto/rand` nonce for `script-src` instead of `script-src 'none'`; API endpoints retain `script-src 'none'`
- **`<noscript>` fallback**: When JavaScript is disabled, dashboard falls back to the original 5-second meta-refresh behavior and hides chart canvases
- **Live DOM updates**: All stat values update in real-time via polling without full page reload
- **Dashboard API routing fix**: `/dashboard/api/stats` now correctly routes through `http.StripPrefix`

### Tests
- 6 new dashboard tests: CSP nonce uniqueness/matching, no meta-refresh outside noscript, noscript fallback presence, chart canvas presence, API endpoint CSP isolation

## [1.27.0] - 2026-02-08

### Added
- **Web API expansion**: 7 REST endpoints for programmatic cache management on the metrics server (port 9978)
  - `GET /api/cache`: Cache statistics (total packages, size, usage percent, bandwidth saved, pinned count)
  - `GET /api/cache/packages`: List packages with filters (`?pinned=true`, `?name=curl`, `?limit=N`)
  - `GET /api/cache/packages/popular`: Top packages by access count
  - `GET /api/cache/packages/recent`: Recently accessed packages
  - `POST /api/cache/packages/{hash}/pin`: Pin a package to prevent eviction
  - `POST /api/cache/packages/{hash}/unpin`: Unpin a package
  - `DELETE /api/cache/packages/{hash}`: Delete a package from cache
- SHA256 hash validation on all hash-based endpoints (400 on invalid format)
- Proper error mapping: 404 for missing packages, 409 for packages currently being read, 500 for internal errors
- Security headers on all API responses

### Tests
- 17 new API tests covering all endpoints, filters, error cases, validation, and security headers

## [1.26.0] - 2026-02-08

### Added
- **CLI `stats --watch`**: Live-updating statistics in terminal via `debswarm stats --watch`
  - Refreshes every 2 seconds with real-time P2P ratio, cache size, and connection counts
  - `--json` flag for machine-readable output
- **Prometheus alerting rules**: Ready-to-use alert configurations in `packaging/prometheus/`
  - `HighVerificationFailureRate`: Triggers when verification failures exceed 0.1/s
  - `NoPeersConnected`: Triggers when peer count drops to zero
  - `CacheNearlyFull`: Triggers when cache usage exceeds 90%
  - Includes setup guide in `packaging/prometheus/README.md`

## [1.25.0] - 2026-02-08

### Added
- **Fleet coordination wired up**: LAN download deduplication is now fully functional
  - When multiple LAN nodes request the same package simultaneously, only one fetches from WAN; the others wait and grab it from that peer's cache via P2P transfer
  - Fleet protocol stream handler (`/debswarm/fleet/1.0.0`) registered at daemon startup
  - `FleetSender` interface with `BroadcastMessage` for coordinator-to-peer messaging
  - `WantPackage()` broadcasts to mDNS peers with nonce-based election to designate a single WAN fetcher
  - Proxy consults fleet coordinator before downloading: returns `ActionFetchLAN` (peer has it cached), `ActionWaitPeer` (peer is fetching, wait then grab from LAN), or `ActionFetchWAN` (this node fetches)
  - `NotifyFetching`, `NotifyComplete`, `NotifyFailed` now automatically broadcast to fleet peers
  - `downloadFromFleetPeer()` helper with SHA256 hash verification and peer blacklisting on mismatch
  - Progress broadcaster goroutine started for in-flight download status updates

### Tests
- 7 new fleet coordination tests: broadcast verification, HavePackage response routing, Fetching lower-nonce election, timeout fallback to WAN, NotifyFetching/Complete broadcast verification, GetMaxWaitTime

## [1.24.0] - 2026-02-08

### Security
- **SSRF hardening**: Complete rewrite of host validation in `internal/security/url.go`
  - Block alternate IP encodings: hex (`0x7f000001`), octal (`0177.0.0.01`), decimal integer (`2130706433`), mixed dotted (`0x7f.0.0.1`)
  - IP blocking now uses `net.IP` methods (`IsLoopback`, `IsPrivate`, `IsLinkLocalUnicast`, etc.) instead of string matching
  - Domain matching uses exact match or `.`-prefixed subdomain suffix — prevents `attack-ubuntu.com` from matching `ubuntu.com`
  - URL paths no longer trigger false positives (host extracted via `url.Parse` before checking)
  - Exported `IsBlockedIP()` for post-DNS-resolution checks
- **DNS rebinding prevention**: CONNECT tunnel handler now checks resolved IP after `dialer.DialContext` — blocks connections that resolve to private/internal addresses
- **pprof endpoint restriction**: Debug profiling endpoints only registered when metrics bind address is localhost
- **Decompression bomb protection**: All gzip/xz Packages file readers wrapped in `io.LimitReader` (512MB limit)
- **Bounded peer input**: P2P stream requests limited to 256 bytes via `io.LimitReader` — prevents memory exhaustion from malicious peers
- **SSRF bypass on proxy-mode requests**: Added `IsAllowedMirrorURL` validation when `r.URL.Host` is set
- **Permissive mirror prefix removal**: `mirrors.*`/`mirror.*`/`ftp.*` prefixes no longer auto-trusted; third-party mirrors must use `allowed_hosts`

### Fixed
- **P2P protocol fixes**:
  - `writeSize()` now returns errors — prevents peers from hanging on failed size writes
  - Client-side streams now have two-phase deadlines: 30s for handshake, size-proportional for transfer
  - `DownloadRange` supports offset-to-EOF (`end=-1` encoded as `0` in protocol, server treats `end<=0` as to-EOF)
  - 500MB allocation cap: peer-controlled size header capped at 10MB initial alloc, grows incrementally
  - TOCTOU race in upload tracking: merged `canAcceptUpload` + `trackUploadStart` into atomic `tryAcceptUpload`
- **Cache correctness**:
  - `trackedReader.Close()` uses `sync.Once` to prevent data race on concurrent close
  - `packagePath()` guards against panic on empty/short hash strings
  - `deleteUnlocked()` deletes from DB first, then removes file — prevents phantom DB entries
  - `currentSize` double-counting on duplicate `Put` — checks for existing entry before INSERT
  - Cache eviction formula: popularity boost changed from 3600 (1hr) to 86400 (1day) per access
- **Rate limiter fixes**:
  - `rate.WaitN` panic when `n > burst` — splits large reads into burst-sized chunks
  - `LimitedWriter.Write` failing on large buffers — splits writes into burst-sized chunks with per-chunk WaitN
- **Mirror/download fixes**:
  - `FetchToWriter` data corruption on retry — removed retry (writer can't rewind), added `io.LimitReader` size cap
  - Off-by-one in chunk range boundaries — convert exclusive end to inclusive end in MirrorSource
  - Division by zero producing +Inf throughput — guarded with `if duration > 0`
- **Config/daemon fixes**:
  - `ParseSize` now returns errors for empty input, non-numeric values, unrecognized units, and integer overflow
  - `ParseSize` digit parsing uses `strconv.ParseInt` instead of manual loop (overflow-safe), with multiplication overflow check
  - `reloadConfig` now applies rate limit changes via `UpdateRateLimits()`
  - `checkFilePermissions` checks world-writable (0002) before world-readable (0004) — more severe issue reported first
  - Flag override logic uses `cmd.Flags().Changed()` — correctly distinguishes "not set" from "set to default value"
  - SIGHUP signal handling skipped on Windows with informational log message
- **Audit log fixes**:
  - Byte counting uses `countingWriter` wrapper tracking actual bytes written (replaces hardcoded `+= 200` estimate)
  - Rotation failure no longer silently drops events — continues writing to current file
- **Timeout tracker**: Ring buffer pattern replaces FIFO reslicing — prevents unbounded backing array memory growth
- **Downloader fixes**:
  - Assembly file no longer created in CWD when resume is disabled — uses `os.MkdirTemp` fallback
  - `New(nil)` no longer panics — all config field accesses moved inside nil guard
- **Index parser**: Package count capped at 500,000 per repo to prevent unbounded memory growth

### Tests
- Added SSRF test cases for hex/octal/decimal IP bypass vectors
- Updated `DurationTracker` overflow test for ring buffer semantics
- Updated mirror prefix test expectations after security hardening

## [1.23.0] - 2026-02-03

### Added
- **Package pinning**: Prevent important packages from being automatically evicted
  - `debswarm cache pin <hash>`: Pin a package to protect it from eviction
  - `debswarm cache unpin <hash>`: Remove pin to allow eviction
  - `debswarm cache unpin --all`: Unpin all packages
  - `debswarm cache list --pinned`: Show only pinned packages
  - Pinned packages marked with `*` in cache list output
  - New cache methods: `Pin()`, `Unpin()`, `IsPinned()`, `ListPinned()`, `PinnedCount()`

### Changed
- `debswarm cache list` now shows pinned count and marks pinned packages with `*`

## [1.22.0] - 2026-02-03

### Added
- **Cache analytics commands**: New CLI commands to analyze cache usage and popular packages
  - `debswarm cache stats`: Enhanced statistics with total accesses, bandwidth savings, and optional top packages (`-p N`)
  - `debswarm cache popular`: List most frequently accessed packages sorted by access count (`-n` for limit)
  - `debswarm cache recent`: List most recently accessed packages (`-n` for limit)

- **Cache analytics API**: New methods in the cache package for programmatic access
  - `Stats()` returns `CacheStats` with total packages, size, accesses, bandwidth saved, and metadata stats
  - `PopularPackages(limit)` returns packages sorted by access count
  - `RecentPackages(limit)` returns packages sorted by last access time

### Example
```bash
# Show detailed cache statistics
debswarm cache stats

# Show stats with top 5 popular packages
debswarm cache stats -p 5

# Show top 10 most accessed packages
debswarm cache popular

# Show 20 most recently used packages
debswarm cache recent -n 20
```

## [1.21.1] - 2026-02-03

### Tests
- **Improved test coverage for `allowed_hosts` feature**
  - Security package: 90.6% → 97.6% coverage
  - Audit package: 78.7% → 82.7% coverage
  - Index package: 73.6% → 80.3% coverage
- **E2E test for `allowed_hosts`**: Comprehensive validation of the feature
  - Third-party repos blocked by default
  - Third-party repos allowed when configured (with Debian URL patterns)
  - Built-in mirrors always work (Debian, Ubuntu, Mint)
  - SSRF protection cannot be bypassed
  - Case-insensitive host matching

## [1.21.0] - 2026-02-02

### Added
- **Linux Mint repository support**: Native support for Linux Mint repositories (`packages.linuxmint.com`)
  - Added `linuxmint.com` and `packages.linuxmint.com` to allowed mirror patterns
  - HTTPS CONNECT tunneling works for Linux Mint mirrors
  - No APT bypass configuration needed for Linux Mint repos
  - Added `/linuxmint/` URL pattern to repository detection

- **Configurable allowed repository hosts**: New `[proxy] allowed_hosts` configuration option
  - Allow third-party Debian-style repositories (Docker, PPAs, PostgreSQL, etc.) through the proxy
  - Configured hosts must still use `/dists/` or `/pool/` URL patterns (security)
  - Private/internal hosts remain blocked (SSRF protection)
  - Alternative to APT `DIRECT` bypass for centralized configuration

### Configuration
New `[proxy]` section in config.toml:
```toml
[proxy]
allowed_hosts = [
  "download.docker.com",
  "ppa.launchpad.net",
  "apt.postgresql.org",
]
```

### Documentation
- Added `[proxy]` section documentation to configuration guide
- Updated troubleshooting guide with `allowed_hosts` as recommended solution
- Updated APT proxy config example with `[proxy]` section

## [1.20.0] - 2026-02-01

### Added
- **HTTPS CONNECT tunnel support**: APT can now use HTTPS repositories through the debswarm proxy
  - Implements HTTP CONNECT method for transparent TCP tunneling
  - Encrypted traffic passes through without caching (end-to-end TLS)
  - Security validation restricts tunnels to known Debian/Ubuntu mirrors only
  - Only ports 443 and 80 allowed; private/internal hosts blocked
  - New timeout operations: `tunnel_connect` (10s) and `tunnel_idle` (120s)
  - New Prometheus metrics: `debswarm_connect_requests_total`, `debswarm_active_tunnels`, `debswarm_tunnel_bytes_in/out_total`, `debswarm_tunnel_duration_seconds`
  - New audit events: `connect_tunnel_start`, `connect_tunnel_end`, `connect_tunnel_blocked`

### Configuration
The existing APT proxy config (`/etc/apt/apt.conf.d/90debswarm`) already supports HTTPS:
```
Acquire::http::Proxy "http://127.0.0.1:9977";
Acquire::https::Proxy "http://127.0.0.1:9977";
```

## [1.19.0] - 2026-02-01

### Added
- **APT archives import**: Automatically import packages from APT's local cache (`/var/cache/apt/archives`) on startup. This pre-populates debswarm's cache with packages you already have, making you an immediate contributor to the P2P network.
  - New `internal/aptarchives` package with import logic
  - Scans APT archives directory for `.deb` files
  - Skips `partial/` directory (incomplete downloads)
  - Verifies packages against the hash index before importing
  - Copies verified packages to debswarm's cache
  - Runs in background to avoid blocking daemon startup

### Configuration
New options in `config.toml` under the `[index]` section:
```toml
[index]
# Path to APT's package cache (default: /var/cache/apt/archives)
apt_archives_path = "/var/cache/apt/archives"
# Whether to import packages from APT's cache on startup (default: true)
import_apt_archives = true
```

## [1.18.0] - 2026-01-31

### Added
- **APT lists auto-indexing**: Automatically parse APT's local package lists (`/var/lib/apt/lists`) on startup and watch for changes. This enables P2P downloads even when `apt update` doesn't go through the proxy (e.g., when APT has cached Packages files locally).
  - New `internal/aptlists` package with file watcher
  - Parses all `*_Packages*` files on daemon startup
  - Uses fsnotify to detect changes and re-parse updated files
  - Debounces rapid changes during `apt update`
  - Extracts repository identifiers from APT list filenames

### Configuration
New options in `config.toml`:
```toml
[index]
# Path to APT's package lists directory (default: /var/lib/apt/lists)
apt_lists_path = "/var/lib/apt/lists"
# Whether to watch APT lists for changes (default: true)
watch_apt_lists = true
```

## [1.17.3] - 2026-01-31

### Fixed
- **APT Acquire-By-Hash compression detection**: Fixed package index parsing for by-hash URLs by detecting compression from magic bytes instead of URL suffix. Gzip-compressed Packages files fetched via `/by-hash/SHA256/xxx` URLs were not decompressed, resulting in 0 packages parsed.

## [1.17.2] - 2026-01-31

### Fixed
- **APT Acquire-By-Hash support**: Fixed cache not filling when APT uses by-hash URLs (default since Debian 9/Ubuntu 16.04). URLs like `/binary-amd64/by-hash/SHA256/xxx` were not recognized as Packages files, causing the hash index to remain empty and preventing package caching.

## [1.17.1] - 2026-01-31

### Fixed
- **Cache not filling**: Fixed race condition where index parsing was asynchronous, causing package hash lookups to fail when APT requests arrived before parsing completed
- **Benchmark 0-byte file size**: Fixed benchmark command running with 0-byte files when `--file-size` flag was not provided
- **Benchmark hash mismatch**: Fixed hash verification for chunked downloads by reading from `FilePath` when `Data` is nil

## [1.17.0] - 2026-01-29

### Added
- **Fuzz testing for parsing functions**: Native Go fuzz tests for robustness
  - `FuzzParseDebFilename` in `internal/cache/parser_fuzz_test.go`
  - `FuzzParsePackagesFile`, `FuzzExtractRepoFromURL`, `FuzzExtractPathFromURL` in `internal/index/index_fuzz_test.go`
  - `FuzzIsValid`, `FuzzGenerate` in `internal/requestid/requestid_fuzz_test.go`
- **Load testing CLI commands**: Performance testing utilities
  - `debswarm benchmark stress` - Concurrent download stress testing
  - `debswarm benchmark concurrency` - Find optimal worker count
  - `debswarm benchmark proxy` - HTTP proxy load testing with latency percentiles (P50/P95/P99)
  - New `internal/benchmark/proxy_loadtest.go` for proxy load testing

### Documentation
- New `docs/testing.md` guide covering fuzz testing, benchmarking, and load testing

## [1.16.0] - 2026-01-29

### Added
- **Request tracing with correlation IDs**: End-to-end request tracking for debugging multi-hop P2P downloads
  - New `requestid` package for ID generation and context utilities
  - Generate time-sortable 24-char hex IDs (8 bytes timestamp + 4 bytes random)
  - Propagate request ID through context to all handlers
  - Include `requestID` field in all log messages for a request
  - Add `RequestID` field to audit events with `WithRequestID()` chaining method
  - Return `X-Request-ID` header in HTTP responses
  - Preserve valid incoming `X-Request-ID` headers from clients

## [1.15.0] - 2026-01-29

### Added
- **Package rollback commands**: List and fetch old package versions from cache or P2P peers
  - `debswarm rollback list <package>` - Show all cached versions of a package
  - `debswarm rollback fetch <package> <version>` - Download specific version from cache
  - `debswarm rollback migrate` - Populate metadata for existing cache entries
  - Cache schema extended with `package_name`, `package_version`, `architecture` columns
  - New `ParseDebFilename()` utility to extract metadata from Debian package filenames
  - Useful for downgrading after problematic updates or testing compatibility

### Fixed
- **Double-close in proxy shutdown**: Fixed verifier being closed twice during proxy server shutdown
- **Metrics formatting**: Corrected gofmt alignment in metrics.go

### Changed
- **Audit events**: Added dedicated `ProviderCount` field to audit Event struct (previously embedded in details)

### Documentation
- Added multi-source verification section to security hardening guide

## [1.14.0] - 2026-01-28

### Added
- **Multi-source verification**: Asynchronous verification of downloaded packages by querying the DHT for other providers
  - Near-zero bandwidth overhead - only queries DHT for provider list, doesn't re-download data
  - Non-blocking verification runs after successful download and caching
  - Configurable minimum providers for "verified" status (default: 2)
  - New audit events: `multi_source_verified`, `multi_source_unverified`
  - New metrics: `debswarm_verification_results`, `debswarm_verification_providers`, `debswarm_verification_duration`
  - Part of v2.0 Security & Resilience roadmap

## [1.13.0] - 2026-01-28

### Added
- **Configurable NAT traversal**: New configuration options for circuit relay and hole punching
  - `network.enable_relay`: Use circuit relays to reach NAT'd peers (default: true)
  - `network.enable_hole_punching`: Enable direct NAT hole punching (default: true)
  - Both features were already enabled but are now configurable and documented
  - Client-only relay mode - uses public relays but doesn't act as one

### Documentation
- Added NAT Traversal section to configuration.md explaining relay and hole punching options

## [1.12.1] - 2026-01-28

### Fixed
- **Fleet coordination message responses**: Complete implementation of WantPackage handler responses
  - Add `MessageSender` interface for coordinator to send responses back to peers
  - Implement `MsgHavePackage` response when we have the requested package cached
  - Implement `MsgFetching` response when we're currently downloading from WAN
- **Data race in handleWantPackage**: Fixed race condition reading `state.Fetcher` outside lock
- **Lock contention in StartProgressBroadcaster**: Fixed network I/O while holding coordinator lock
  - Now collects progress data under lock, broadcasts outside lock
  - Prevents lock starvation and potential deadlocks during slow network conditions

### Added
- Tests for `handleWantPackage` response handling (`TestHandleWantPackageWithCache`, `TestHandleWantPackageWhileFetching`)

## [1.12.0] - 2026-01-28

### Added
- **IPv6 validation in CI**: Added comprehensive IPv6 connectivity tests to validate P2P functionality
  - `TestNew_IPv6Addresses`: Verifies nodes listen on IPv6 addresses (TCP and QUIC)
  - `TestNew_IPv6WithQUIC`: Verifies IPv6 QUIC addresses when QUIC is preferred
  - `TestNode_TwoNodes_ConnectIPv6`: Tests two nodes connecting over IPv6 only
  - `TestNode_Download_IPv6`: Tests full content transfer over IPv6
- Completes all Medium Priority roadmap items

## [1.11.5] - 2026-01-28

### Security
- **Integer overflow fixes**: Resolve all gosec high-severity integer overflow warnings (G115)
  - Add overflow validation for uint64/int64 conversions in P2P transfer protocol
  - Add bounds checking before int-to-uint16 conversion in fleet messages
  - Add explicit bitmask for int64-to-uint32 truncation in fleet coordinator
  - Add nosec annotations for intentional conversions (benchmark math/rand, diskspace)

## [1.11.4] - 2026-01-28

### Security
- **GitHub Actions hardening**: Fix security vulnerabilities in CI/CD workflows
  - Fix script injection vulnerability in release.yml workflow_dispatch input
  - Add high-severity check to gosec scanner (fail CI on HIGH findings)
  - SHA-pin all GitHub Actions to prevent supply chain attacks
    - actions/checkout@v4.3.0
    - actions/setup-go@v5.6.0
    - actions/upload-artifact@v6.0.0
    - codecov/codecov-action@v5.5.2
    - golangci/golangci-lint-action@v7.0.1
    - goreleaser/goreleaser-action@v6.4.0

## [1.11.3] - 2025-12-31

### Added
- **Performance benchmarks**: Added benchmark tests for downloader buffer operations
- **GoDoc examples**: Added example_test.go files for internal libraries
  - `internal/retry/` - Examples for Do(), NonRetryable(), backoff strategies
  - `internal/lifecycle/` - Examples for Manager, Go(), GoN(), RunTicker()
  - `internal/hashutil/` - Examples for HashingWriter, HashingReader, Verify()
  - `internal/httpclient/` - Examples for New(), Default(), WithTimeout()

### Changed
- **Performance**: Added buffer pooling for chunk assembly in downloader
  - Reuses 4MB buffers via sync.Pool instead of allocating per chunk
  - 55,000x faster buffer operations, zero allocations in hot path
  - Reduces GC pressure during large file downloads
- **Error handling**: Standardized error message patterns across codebase
  - Lowercase error messages per Go conventions (e.g., "http 404" not "HTTP 404")
  - Fixed error wrapping in downloader to use %w for proper error chain support

## [1.11.2] - 2025-12-29

### Changed
- **Internal refactoring**: Extracted common patterns into reusable libraries
  - `internal/retry/` - Generic retry with exponential/linear/constant backoff (Go generics)
  - `internal/lifecycle/` - Goroutine lifecycle management with context + waitgroup
  - `internal/hashutil/` - Streaming hash computation (HashingWriter/HashingReader)
  - `internal/httpclient/` - HTTP client factory with connection pooling and sensible defaults
- Refactored `mirror/fetcher.go` to use `retry.Do()` instead of inline retry loops
- Refactored `downloader/downloader.go` to use `retry.Do()` for chunk retries
- Refactored `ratelimit/peer_limiter.go` to use `lifecycle.Manager` for goroutine management
- Refactored `proxy/server.go` announcement worker to use `lifecycle.Manager`
- Refactored `cache/cache.go` to use `hashutil.HashingWriter` for hash computation
- Refactored `mirror/fetcher.go`, `index/index.go`, `connectivity/monitor.go` to use `httpclient`

## [1.11.1] - 2025-12-23

### Fixed
- Updated `packaging/config.system.toml` with missing configuration sections
  - Added connectivity_mode comment
  - Added per-peer rate limiting comments
  - Added audit logging section (v1.8+)
  - Added scheduler section (v1.9+)
  - Added fleet coordination section (v1.9+)

## [1.11.0] - 2025-12-23

### Added
- **Parallel Imports**: New `--parallel N` flag for `seed import` command
  - Process multiple .deb files concurrently (up to 32 workers)
  - Dramatically faster imports for large mirrors (8x+ speedup typical)
- **Dry-Run Mode**: New `--dry-run` flag to preview changes without making them
  - Shows what would be imported, skipped, and removed
  - Essential for validating sync operations before execution
- **Incremental Sync**: New `--incremental` flag for faster daily syncs
  - Only processes files modified since last successful sync
  - Tracks sync state per source path
  - Reduces sync time from hours to seconds for large mirrors
- **Watch Mode**: New `--watch` flag for continuous monitoring
  - Automatically imports new .deb files as they appear
  - Uses filesystem notifications (fsnotify) for efficiency
  - Debounces rapid changes to batch imports
  - Eliminates need for cron-based polling
- **Progress Bar**: New `--progress` flag for large imports
  - Shows visual progress bar with statistics
  - Displays imported, skipped, and failed counts in real-time

### Changed
- `seed import` now uses worker pool pattern for better resource utilization
- Improved error handling with graceful degradation in watch mode

### Dependencies
- Added `github.com/fsnotify/fsnotify` v1.9.0 for watch mode

## [1.10.0] - 2025-12-23

### Added
- **Cache Verification Command**: `debswarm cache verify` to check integrity of all cached packages
  - Computes SHA256 hash of each cached file and compares against expected value
  - Reports missing, corrupted, and verified packages
  - Useful for incident response and cache integrity auditing
- **Peer Blocklist**: New `privacy.peer_blocklist` configuration option
  - Block specific peer IDs from connecting
  - Blocklist is checked before allowlist (blocked peers always rejected)
  - Useful for blocking malicious or misbehaving peers
  - New gater methods: `BlockPeer()`, `UnblockPeer()`, `ListBlockedPeers()`

### Fixed
- **Documentation**: Corrected security-hardening.md claims that didn't match implementation
  - Removed non-existent CSP header from security headers list
  - Removed non-existent granular audit logging fields (`log_downloads`, `log_uploads`, etc.)

### Configuration
New option in `config.toml`:
```toml
[privacy]
# Block specific peers (connections always rejected)
peer_blocklist = [
  "12D3KooWMaliciousPeerIdHere...",
]
```

## [1.9.0] - 2025-12-21

### Added
- **Scheduled Sync Windows**: Time-based download scheduling with rate limiting
  - Configure sync windows for off-peak downloading (e.g., nights, weekends)
  - Rate limiting outside windows (default 100KB/s) instead of blocking
  - Security updates always get full speed regardless of schedule
  - Timezone-aware scheduling with flexible day patterns (weekday, weekend, specific days)
  - New `internal/scheduler/` package with full test coverage
- **Fleet Coordination**: LAN fleet coordination for download deduplication
  - Peers coordinate to avoid redundant WAN downloads of the same package
  - Election-based fetcher selection using random nonces
  - Progress broadcasting across fleet peers
  - Automatic fallback if coordination fails
  - New `internal/fleet/` package with protocol handler
- **New Prometheus metrics**:
  - `debswarm_scheduler_window_active` - 1 if currently in sync window
  - `debswarm_scheduler_current_rate_bytes` - Current rate limit in bytes/sec
  - `debswarm_scheduler_urgent_downloads_total` - Security updates at full speed
  - `debswarm_fleet_peers` - Number of fleet peers
  - `debswarm_fleet_wan_avoided_total` - Downloads served from fleet vs WAN
  - `debswarm_fleet_bytes_avoided_total` - Bytes saved by fleet coordination
  - `debswarm_fleet_in_flight` - Current in-flight fleet downloads
- **P2P node enhancements**: `GetMDNSPeers()` and `Host()` methods for fleet integration

### Changed
- Updated golangci-lint config for v2 format
- CI now uses golangci-lint-action v7 with golangci-lint v2.7.2

### Fixed
- Fix errcheck lint issues with explicit error discarding in defer statements

### Configuration
New options in `config.toml`:
```toml
[scheduler]
enabled = true
timezone = "America/New_York"
outside_window_rate = "100KB/s"
inside_window_rate = "unlimited"
urgent_always_full_speed = true

[[scheduler.windows]]
days = ["weekday"]
start_time = "22:00"
end_time = "06:00"

[[scheduler.windows]]
days = ["saturday", "sunday"]
start_time = "00:00"
end_time = "23:59"

[fleet]
enabled = true
claim_timeout = "5s"
max_wait_time = "5m"
allow_concurrent = 1
refresh_interval = "1s"
```

## [1.8.0] - 2025-12-18

### Added
- **LAN Peer Priority**: mDNS-discovered peers now receive a scoring boost for proximity
  - New `WeightProximity` factor (15%) in peer scoring algorithm
  - mDNS peers get proximity score of 1.0, DHT peers get 0.3
  - Unknown mDNS peers start at score 0.65 (vs 0.5 for DHT peers)
  - Peers discovered via mDNS are automatically marked for priority selection
- **Offline-First Mode**: Automatic detection and graceful fallback when internet is unavailable
  - Three connectivity modes: `online` (full), `lan_only` (mDNS peers only), `offline` (cache only)
  - Configurable `connectivity_mode`: "auto" (default), "lan_only", or "online_only"
  - Background connectivity monitoring with configurable check interval
  - Health endpoint now includes `connectivity_mode` field
  - New `internal/connectivity/` package with full test coverage
- **Audit Log Export**: Structured JSON audit logging for compliance and monitoring
  - Events logged: download complete/failed, upload complete, cache hits, verification failures, peer blacklisting
  - JSON Lines format for easy parsing by log analysis tools (jq, ELK, Splunk)
  - Automatic file rotation with configurable max size and backup count
  - New `internal/audit/` package with Logger interface and NoopLogger for disabled state
  - Configurable via `[logging.audit]` section in config.toml

### Changed
- Peer scoring weights adjusted: Latency 25%, Throughput 25%, Reliability 20%, Freshness 15%, Proximity 15%
  - (Previously: Latency 30%, Throughput 30%, Reliability 25%, Freshness 15%)

### Configuration
New options in `config.toml`:
```toml
[network]
connectivity_mode = "auto"           # "auto", "lan_only", "online_only"
connectivity_check_interval = "30s"
# connectivity_check_url = "https://deb.debian.org"

[logging.audit]
enabled = false
path = "/var/log/debswarm/audit.json"
max_size_mb = 100
max_backups = 5
```

## [1.7.0] - 2025-12-18

### Changed
- **Streaming downloads**: Large file downloads (≥10MB) now stream directly to disk instead of buffering in memory
  - Eliminates memory exhaustion for large packages (500MB+ files no longer allocate 500MB RAM)
  - Chunks written to assembly file on disk, verified by streaming hash computation
  - Memory usage for chunked downloads reduced from ~file_size to ~32MB (chunks in flight only)
  - Racing strategy for small files (<10MB) unchanged for best latency
- **Score cache TTL**: Increased peer score cache from 1 minute to 5 minutes to reduce CPU overhead

### Added
- `cache.PutFile()` method for atomic file moves from pre-verified temp files

## [1.6.0] - 2025-12-18

### Security
- **Eclipse attack mitigation**: Block connections to/from private/reserved IP addresses in multiaddrs
  - Prevents attackers from announcing private IPs in DHT provider records
  - Filters multiaddrs in `InterceptAccept` and `InterceptAddrDial`
- **DHT info leakage prevention**: Skip DHT announcements in private swarm mode (when peer allowlist is active)
- **Range request validation**: Validate byte range bounds in transfer requests to prevent invalid ranges
- **Provider address filtering**: Filter blocked addresses from DHT provider results before connecting

### Added
- `internal/security/multiaddr.go`: Multiaddr validation functions for blocking private/reserved IPs
- `IsBlockedMultiaddr()` and `FilterBlockedAddrs()` functions

## [1.5.1] - 2025-12-17

### Fixed
- Fix golangci-lint gofmt errors in rate limiting code
- Fix ineffassign error in peer limiter test

## [1.5.0] - 2025-12-17

### Added
- **Per-peer rate limiting**: Rate limit individual peers to prevent bandwidth monopolization
  - Configurable `per_peer_upload_rate` and `per_peer_download_rate`
  - Auto mode divides global limit by expected number of peers
  - Both global and per-peer limits enforced simultaneously (stricter wins)
  - Idle peer limiters automatically cleaned up after 30 seconds
- **Adaptive rate limiting**: Automatic rate adjustment based on peer performance
  - Integrates with peer scoring system (latency, throughput, reliability)
  - High-performing peers get boosted rates (up to 1.5x)
  - Poorly-performing peers get reduced rates (down to configurable minimum)
  - Congestion detection reduces rates when latency exceeds 500ms
  - Enabled by default when per-peer limiting is active
- New configuration options in `[transfer]` section:
  - `per_peer_upload_rate`: "auto", "0" (disabled), or specific rate like "5MB/s"
  - `per_peer_download_rate`: "auto", "0" (disabled), or specific rate
  - `expected_peers`: Number of peers for auto-calculation (default: 10)
  - `adaptive_rate_limiting`: Enable/disable adaptive adjustment
  - `adaptive_min_rate`: Floor rate for adaptive reduction (default: "100KB/s")
  - `adaptive_max_boost`: Maximum boost factor (default: 1.5)
- New Prometheus metrics:
  - `debswarm_peer_rate_limiters`: Number of active per-peer limiters
  - `debswarm_peer_rate_limit_bytes_per_second`: Current rate per peer
  - `debswarm_adaptive_adjustments_total`: Count of rate adjustments by type

## [1.4.2] - 2025-12-17

### Added
- Enhanced mDNS discovery logging to help debug local peer discovery
  - Log listen addresses when mDNS starts
  - Log when mDNS is explicitly disabled
  - Log discovered peer addresses (not just peer ID)
  - Log successful mDNS peer connections at Info level

## [1.4.1] - 2025-12-17

### Fixed
- Fix `identity show` to use same data directory resolution as daemon
- Fix `status` command to use configured metrics bind/port instead of hardcoded values
- Fix `peers` command hardcoded metrics URL
- Fix `config show` to display all configuration fields (was missing many sections)

### Added
- Comprehensive configuration documentation (`docs/configuration.md`)

### Changed
- Config show now displays resolved `data_directory` path
- Rate limits show "unlimited" instead of empty string
- Fixed documentation: grep pattern for peerID in bootstrap-node.md
- Fixed documentation: removed incorrect CGO/GCC build requirements

## [1.4.0] - 2025-12-17

### Added
- **Automatic retry for failed downloads**: Failed P2P downloads are automatically retried on subsequent APT requests
  - Configurable `retry_max_attempts` (default: 3)
  - Configurable `retry_interval` with exponential backoff (default: 5m)
  - Configurable `retry_max_age` to expire old failures (default: 1h)

## [1.3.3] - 2025-12-17

### Security
- **Log sanitization**: Sanitize peer IDs and file paths in log output to prevent log injection attacks

## [1.3.2] - 2025-12-16

### Added
- `--cache-path` flag for seed command to override default cache path when importing packages

## [1.3.1] - 2025-12-16

### Fixed
- Fix contextcheck lint error in keepalive goroutine

## [1.3.0] - 2025-12-16

### Added
- **Keepalive pings**: Periodic pings (every 5 minutes) to all connected peers prevent idle connections from being pruned by the connection manager
- **Longer grace period**: Connection manager grace period increased from 1 to 10 minutes

### Fixed
- Connected peers no longer drop to 0 after periods of inactivity

## [1.2.6] - 2025-12-16

Re-release of v1.2.5 (CI asset conflict).

## [1.2.5] - 2025-12-16

### Fixed
- **Proxy URL extraction**: Fix handling of APT proxy requests to correctly extract target URL from `r.URL.Host`
- **Systemd service**: Remove all `*Directory=` directives to avoid STATE_DIRECTORY errors
- **Systemd service**: Switch from `DynamicUser=yes` to static `debswarm` user for reliable directory permissions
- **Debian package**: postinst now creates `debswarm` system user/group and sets directory ownership
- **Data directory**: Auto-detect `/var/lib/debswarm` for system installs instead of deriving from cache path

## [1.2.1] - 2025-12-16

### Fixed
- Systemd `CACHE_DIRECTORY` now correctly overrides config file path setting

## [1.2.0] - 2025-12-16

### Added
- **Systemd compatibility**: Automatic detection of `CACHE_DIRECTORY` and `STATE_DIRECTORY` environment variables
  - Running under systemd with `CacheDirectory=` and `StateDirectory=` now works out-of-box
  - No manual config file changes needed for systemd deployments

### Fixed
- Fix directory validation to not check parent directory writability (fixes systemd `ProtectSystem=strict`)

## [1.1.1] - 2025-12-16

### Fixed
- Fix import ordering in cache.go for golangci-lint

## [1.1.0] - 2025-12-16

### Changed
- **Pure Go SQLite**: Replaced `mattn/go-sqlite3` with `modernc.org/sqlite`
  - No longer requires CGO or a C compiler to build
  - Enables cross-compilation without CGO toolchain
  - Works out-of-box on Windows without MinGW/TDM-GCC

## [1.0.1] - 2025-12-15

### Fixed
- **TOML config parsing**: Fixed DHT duration fields (`provider_ttl`, `announce_interval`) to parse correctly from TOML strings like "24h"

## [1.0.0] - 2025-12-15

### Added
- **Runtime profiling**: pprof endpoints at `/debug/pprof/` on metrics server for production debugging
- **E2E integration tests**: Full end-to-end tests for proxy, cache, index, and P2P transfer flows
  - `TestE2E_ProxyMirrorFallback`: Tests mirror serving and proxy handler
  - `TestE2E_CacheHit`: Verifies cache serves packages without mirror hits
  - `TestE2E_IndexAutoPopulation`: Tests Packages file parsing
  - `TestE2E_TwoNodeP2PTransfer`: Two-node P2P transfer with DHT discovery
  - `TestE2E_HashVerification`: Validates hash verification rejects bad packages
- **CLI smoke tests**: Test coverage for CLI commands (version, config, psk, etc.)

### Security
- **MaxHeaderBytes**: Added 1MB limit to all HTTP servers to prevent header-based DoS

### Changed
- All critical, high, and medium priority items for 1.0 are now complete
- Project is production-ready for general use

## [0.8.2] - 2025-12-15

### Added
- Enforce `transfer.max_concurrent_uploads` config option in P2P upload handler
- Enforce `transfer.max_concurrent_peer_downloads` config option in downloader

### Fixed
- Fix debian package build (remove redundant debian/install entries)
- Fix golangci-lint errors (shadow, contextcheck, unconvert, rowserrcheck)
- Fix goimports import ordering across codebase

### Changed
- CI now tests deb package building on every push

## [0.8.0] - 2025-12-15

### Added
- Pre-flight directory validation for systemd compatibility
  - Validates cache and data directories exist or can be created
  - Tests write permissions before daemon startup
  - Catches StateDirectory/CacheDirectory issues early with clear error messages

## [0.7.0] - 2025-12-15

### Added
- Config file validation on startup with detailed error messages
- Crash recovery for corrupted partial download state
- SIGHUP handler for config reload without restart
- Troubleshooting guide in documentation

## [0.6.2] - 2025-12-15

### Added
- **Health endpoint**: `/health` endpoint on metrics server returns JSON with system health status
  - Checks P2P node, DHT, and cache availability
  - Returns 200 OK when healthy, 503 when not
- **MaxConnections enforcement**: P2P node now enforces `network.max_connections` config using libp2p connection manager
- **MinFreeSpace enforcement**: Cache now respects `cache.min_free_space` config, preventing disk exhaustion

### Changed
- Updated roadmap with completed critical items for 1.0 release

## [0.6.1] - 2025-12-15

### Added
- **Peers table in dashboard**: Connected peers now displayed with score, latency, throughput, and bytes transferred
- **Score color coding**: Visual indicators for peer quality (excellent/good/fair/poor/blacklisted)
- **Verification failures display**: Hash verification failures shown in dashboard overview

### Changed
- New Prometheus metrics:
  - `debswarm_downloads_resumed_total`: Count of resumed downloads
  - `debswarm_chunks_recovered_total`: Chunks recovered from disk
  - `debswarm_errors_total{type}`: Error breakdown by type (timeout, connection, verification)
  - `debswarm_peers_joined_total` / `debswarm_peers_left_total`: Peer churn tracking
  - `debswarm_upload_bytes_per_second` / `debswarm_download_bytes_per_second`: Bandwidth rates

## [0.6.0] - 2025-12-15

### Added
- **Download resume support**: Interrupted chunked downloads can now resume from where they left off
  - Chunks persisted to disk during download
  - Download state tracked in SQLite database
  - Automatic recovery on daemon restart
- **HTTP Range request support**: Mirror fetcher now supports byte-range requests for partial content
- **Configurable chunked download threshold**: `MinChunkedSize` can be configured for testing

### Changed
- Improved test coverage across all packages (73-100% coverage)
- Enhanced CI/CD workflows with better caching and security scanning

### Security
- Fixed unhandled errors in cleanup paths (gosec G104)
- Restricted directory permissions from 0755 to 0750
- Restricted file permissions from 0644 to 0600 for sensitive files
- Proper error handling for Close() and Remove() operations

## [0.5.6] - 2025-12-15

### Fixed
- Fixed DHT lifecycle issues: context leak and channel drain
- Improved DHT shutdown handling

### Changed
- Added comprehensive test coverage for cache, peers, and downloader packages
- Updated documentation for v0.5.x releases

## [0.5.5] - 2025-12-14

### Security
- Added HTTP security headers to dashboard and metrics endpoints (X-Content-Type-Options, X-Frame-Options, Cache-Control, X-XSS-Protection)
- Added Content-Security-Policy for dashboard
- Added response size limit (500MB) to mirror fetcher to prevent memory exhaustion

### Changed
- Extracted SSRF validation to shared `internal/security` package
- Removed redundant `min()` function (use Go 1.21+ built-in)

## [0.5.3] - 2025-12-14

### Security
- **SSRF vulnerability fix**: Block requests to localhost, cloud metadata services, private networks
- Validate URLs match Debian/Ubuntu repository patterns
- Fixed information disclosure in dashboard error messages
- Added documentation for metrics endpoint exposure risks

### Added
- Test coverage for SSRF URL validation

## [0.5.2] - 2025-12-14

### Changed
- Updated libp2p to v0.46.0
- Updated go-sqlite3 to v1.14.32
- Fixed debian cross-compilation for arm64
- Made version dynamic in debian/rules

## [0.5.1] - 2025-12-14

### Changed
- Updated libp2p to v0.45.0 and kad-dht to v0.36.0
- Updated cobra to v1.10.2
- Updated GoReleaser config to v2 format

### Fixed
- Fixed identity key loading for libp2p v0.45+ compatibility (use generic unmarshal)

### Infrastructure
- CI now uses Go 1.24.6

## [0.5.0] - 2025-12-14

### Added
- **Benchmark command**: New `debswarm benchmark` command with simulated peers for performance testing
- New `internal/benchmark` package for reproducible download performance testing

### Changed
- **Go 1.24 required**: Updated minimum Go version from 1.22 to 1.24.6

### Fixed
- Fixed race condition in cache reader tracking (TOCTOU bug)
- Fixed goroutine leak on chunk download failure
- Fixed blacklist flag inconsistency after expiration
- Fixed stream deadline error handling in P2P transfers
- Improved error context in download retry loops
- Added context propagation to rate limiter for proper cancellation
- Added proper goroutine cleanup in announcement worker

## [0.4.0] - 2025-12-13

### Added
- **Multi-repository support**: Proper isolation of package indexes from different repositories (deb.debian.org, archive.ubuntu.com, third-party repos)
- **Auto-indexing**: Packages files are automatically parsed when APT fetches them, enabling P2P for all configured repos
- **Persistent identity**: Stable peer IDs across daemon restarts via Ed25519 key persistence
- **Identity CLI**: New `debswarm identity show` and `debswarm identity regenerate` commands
- **Config security warnings**: Warnings for world-readable config files containing inline PSK
- **Remote monitoring**: New `--metrics-bind` flag to expose dashboard/metrics on non-localhost (for seeding servers)

### Changed
- Daemon now persists identity key to `<data-dir>/identity.key` for stable peer IDs
- Version command now includes "Persistent identity" in feature list

### Security
- **SSRF mitigation**: Proxy now validates URLs to only allow legitimate Debian/Ubuntu mirror requests, blocking access to private networks and cloud metadata endpoints
- **Metrics server hardening**: Added ReadTimeout (10s), WriteTimeout (30s), IdleTimeout (60s) to prevent slowloris attacks
- **CSS injection fix**: Dashboard source values sanitized to prevent CSS class name injection
- Identity keys stored with 0600 permissions
- Config file permission check warns about world-readable files with secrets
- Identity key format includes version header for forward compatibility

## [0.3.0] - 2025-12-13

### Added
- **Bandwidth limiting**: Control upload/download rates with `--max-upload-rate` and `--max-download-rate` CLI flags or config
- **Web dashboard**: Real-time HTML dashboard at `http://localhost:9978/dashboard` showing stats, peers, and transfers
- **Private swarms (PSK)**: Pre-shared key support for isolated networks via `psk_path` config option
- **Peer allowlist**: Restrict connections to specific peer IDs via `peer_allowlist` config option
- **PSK management CLI**: New `debswarm psk generate` and `debswarm psk show` commands
- **Mirror sync mode**: New `--sync` flag for `seed import` removes cached packages not in source directory
- **Download resume infrastructure**: SQLite schema for tracking download state and partial files
- **Rate limit package**: New `internal/ratelimit` package with token bucket rate limiting
- **Dashboard package**: New `internal/dashboard` package with embedded HTML template
- **Connection gater**: `internal/p2p/gater.go` for peer allowlist enforcement

### Changed
- P2P node now supports PSK and connection gating options
- Dashboard auto-refreshes every 5 seconds
- Version command now lists all features

### Security
- PSK files created with 0600 permissions (owner read/write only)
- PSK values never logged, only fingerprints
- Inline PSK config generates a warning recommending file-based PSK

## [0.2.0] - 2025-12-12

### Added
- **Parallel chunked downloads**: Large files (>10MB) are now split into 4MB chunks and downloaded simultaneously from multiple peers
- **Adaptive timeout system**: Network timeouts automatically adjust based on observed performance
- **Peer scoring and selection**: Peers are ranked by latency, throughput, and reliability for optimal selection
- **QUIC transport preference**: QUIC is now preferred over TCP for better NAT traversal and performance
- **Prometheus metrics endpoint**: Full observability at `http://localhost:9978/metrics`
- **JSON stats endpoint**: Quick status check at `http://localhost:9978/stats`
- **Racing strategy**: Small files race P2P vs mirror simultaneously
- **Debian packaging**: Full debian/ directory for building .deb packages
- **GitHub Actions workflows**: CI, release, and Debian package building
- **GoReleaser configuration**: Automated release builds

### Changed
- Improved NAT traversal with QUIC as primary transport
- Better peer selection algorithm with diversity for exploration
- Enhanced logging with structured fields
- Updated systemd service with stricter security settings

### Fixed
- Connection handling edge cases in P2P node
- Cache eviction scoring for better LRU behavior
- Index parsing for edge cases in Packages files

## [0.1.0] - 2025-12-01

### Added
- Initial release
- HTTP proxy for APT integration
- P2P package distribution via libp2p
- Kademlia DHT for peer discovery
- SHA256 verification of all downloads
- Automatic mirror fallback
- mDNS local network discovery
- SQLite-backed content-addressed cache
- TOML configuration file support
- systemd service with security hardening
- CLI with daemon, status, cache, and config commands

### Security
- All P2P downloads verified against signed repository metadata
- Peer blacklisting on hash mismatch
- No trust placed in peers
- Sandboxed systemd service

[Unreleased]: https://github.com/clintcan/debswarm/compare/v1.20.0...HEAD
[1.20.0]: https://github.com/clintcan/debswarm/compare/v1.19.0...v1.20.0
[1.19.0]: https://github.com/clintcan/debswarm/compare/v1.18.0...v1.19.0
[1.18.0]: https://github.com/clintcan/debswarm/compare/v1.17.3...v1.18.0
[1.17.3]: https://github.com/clintcan/debswarm/compare/v1.17.2...v1.17.3
[1.17.2]: https://github.com/clintcan/debswarm/compare/v1.17.1...v1.17.2
[1.17.1]: https://github.com/clintcan/debswarm/compare/v1.17.0...v1.17.1
[1.11.5]: https://github.com/clintcan/debswarm/compare/v1.11.4...v1.11.5
[1.11.4]: https://github.com/clintcan/debswarm/compare/v1.11.3...v1.11.4
[1.11.3]: https://github.com/clintcan/debswarm/compare/v1.11.2...v1.11.3
[1.11.2]: https://github.com/clintcan/debswarm/compare/v1.11.1...v1.11.2
[1.11.1]: https://github.com/clintcan/debswarm/compare/v1.11.0...v1.11.1
[1.11.0]: https://github.com/clintcan/debswarm/compare/v1.10.0...v1.11.0
[1.10.0]: https://github.com/clintcan/debswarm/compare/v1.9.0...v1.10.0
[1.9.0]: https://github.com/clintcan/debswarm/compare/v1.8.0...v1.9.0
[1.8.0]: https://github.com/clintcan/debswarm/compare/v1.7.0...v1.8.0
[1.7.0]: https://github.com/clintcan/debswarm/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/clintcan/debswarm/compare/v1.5.1...v1.6.0
[1.5.1]: https://github.com/clintcan/debswarm/compare/v1.5.0...v1.5.1
[1.5.0]: https://github.com/clintcan/debswarm/compare/v1.4.2...v1.5.0
[1.4.2]: https://github.com/clintcan/debswarm/compare/v1.4.1...v1.4.2
[1.4.1]: https://github.com/clintcan/debswarm/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/clintcan/debswarm/compare/v1.3.3...v1.4.0
[1.3.3]: https://github.com/clintcan/debswarm/compare/v1.3.2...v1.3.3
[1.3.2]: https://github.com/clintcan/debswarm/compare/v1.3.1...v1.3.2
[1.3.1]: https://github.com/clintcan/debswarm/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/clintcan/debswarm/compare/v1.2.6...v1.3.0
[1.2.6]: https://github.com/clintcan/debswarm/compare/v1.2.5...v1.2.6
[1.2.5]: https://github.com/clintcan/debswarm/compare/v1.2.1...v1.2.5
[1.2.1]: https://github.com/clintcan/debswarm/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/clintcan/debswarm/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/clintcan/debswarm/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/clintcan/debswarm/compare/v1.0.1...v1.1.0
[1.24.0]: https://github.com/clintcan/debswarm/compare/v1.23.0...v1.24.0
[1.0.1]: https://github.com/clintcan/debswarm/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/clintcan/debswarm/compare/v0.8.2...v1.0.0
[0.8.2]: https://github.com/clintcan/debswarm/compare/v0.8.0...v0.8.2
[0.8.0]: https://github.com/clintcan/debswarm/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/clintcan/debswarm/compare/v0.6.2...v0.7.0
[0.6.2]: https://github.com/clintcan/debswarm/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/clintcan/debswarm/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/clintcan/debswarm/compare/v0.5.6...v0.6.0
[0.5.6]: https://github.com/clintcan/debswarm/compare/v0.5.5...v0.5.6
[0.5.5]: https://github.com/clintcan/debswarm/compare/v0.5.3...v0.5.5
[0.5.3]: https://github.com/clintcan/debswarm/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/clintcan/debswarm/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/clintcan/debswarm/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/clintcan/debswarm/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/clintcan/debswarm/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/clintcan/debswarm/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/clintcan/debswarm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/clintcan/debswarm/releases/tag/v0.1.0
