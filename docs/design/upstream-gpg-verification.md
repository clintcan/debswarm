# Design: daemon-side upstream GPG verification

**Status:** Proposed
**Author:** debswarm maintainers
**Date:** 2026-07-13
**Backlog item:** Robustness/security #1 — "Trust model: docs corrected; daemon-side hardening remains"

## Context

debswarm performs **no** signature verification today. The SHA256 it checks a
package's bytes against comes from a `Packages` index that debswarm fetched over
the **same upstream leg** (usually plain `http://`) as the bytes themselves. A
mirror — or a MITM on that leg — that rewrites *both* the `Packages` index and
the package bytes passes debswarm's SHA256 check cleanly. Nothing in the daemon
notices.

The end-to-end guarantee that saves the normal case is **APT's own client-side
GPG verification**, which debswarm preserves by passing bytes through unmodified.
That guarantee holds for a plain `apt-get install`. But it does **not** cover the
cases APT itself skips:

- **`[trusted=yes]` / `Acquire::AllowInsecureRepositories`** repositories, where
  APT does not verify signatures at all.
- **`dpkg -i`** of a file pulled from debswarm's cache — no APT verification in
  that path.
- **The P2P swarm seed.** The SHA256 check means a malicious *peer* cannot poison
  the swarm (verification catches mismatched bytes). But the *origin* of every
  cached, announced, shared object is a mirror fetch whose only anchor is "the
  mirror said so." If that first fetch was tampered, debswarm caches and
  re-serves attacker-controlled bytes to every peer that asks; each peer's APT
  still catches it on install, but debswarm has spent bandwidth and disk seeding
  poison and vouching for it by hash.

This design adds an **optional, opt-in** daemon-side verification that anchors the
SHA256 debswarm trusts to a **GPG-signed `Release`**: verify the `Release`
signature against a trusted keyring, confirm the `Packages` index hash matches
what that signed `Release` lists, and only then trust the per-package SHA256s
parsed out of it. When it is enabled in enforcing mode, the bytes debswarm
caches and shares are cryptographically anchored, not merely mirror-asserted.

The doc-honesty half of this backlog item already shipped (README/SECURITY/docs
reworded to state the real model). This is the remaining **code** half.

### What exists today (verified against `HEAD`)

- **No GPG anywhere.** No OpenPGP/PGP/keyring/signature code in the tree, and no
  GPG dependency in `go.mod`. (`internal/verify/verifier.go` is a *DHT
  multi-source* checker, not a signature verifier.) `golang.org/x/crypto` is
  present but indirect, and its `openpgp` subpackage no longer exists — a GPG
  library must be **added**.
- **Release/InRelease/Release.gpg are fetched mirror-only, cached, never parsed.**
  `classifyRequest` (`internal/proxy/server.go`) tags them `requestTypeRelease`;
  `handleReleaseRequest` is a bare alias for `handlePassthrough` →
  `serveMetadata(..., isIndex=false)`. With metadata caching on they land in the
  cache (URL-keyed `indices` table) but no signature is ever checked.
- **Packages is fetched + parsed in `serveFreshBody`**, which calls
  `index.LoadFromData` → `parseForRepo`; that is the *only* place package hashes
  enter the system (`idx.packages[pkg.SHA256] = pkg`). The `Packages` file's
  **own** hash is never computed or stored — only the per-package hashes inside
  it. `PackageInfo` has no field for its parent index file's hash.
- **The Release body's `SHA256:` hash-list section is never parsed.** `parseForRepo`
  understands only the `Packages` stanza format, not Release's indented
  `<hash> <size> <path>` lines, `Valid-Until`, `Suite`, or `Acquire-By-Hash`.
- **A "verify-or-refuse" precedent already exists.** `internal/cache/metadata.go`
  content-verifies immutable `by-hash` URLs: `MetadataWriter.Commit` compares the
  streamed body's SHA256 against the hash in the URL and returns `ErrHashMismatch`
  on mismatch, refusing to cache. The Release-driven `Packages` gate below follows
  the same shape.
- **Repo/dist identity helpers exist.** `index.ExtractRepoFromURL` (→
  `deb.debian.org/debian`) and `index.indexFileKey` (collapses compression and
  `by-hash` variants to one logical file) give us the pieces to map a Release's
  listed index path to a fetched `Packages` URL.
- **Config trust surface is host-allowlisting only** (`ProxyConfig.AllowedHosts`,
  `TrustKnownRepos`, `HTTPSUpstreamHosts`). There is **no** `[security]` section
  and no keyring config. `security/url.go` already enumerates
  `release`/`inrelease`/`release.gpg`/`packages`/`sources` filenames in
  `isAPTFileName` — a natural home for a shared Release classifier.

### What this does and does not change (trust model)

- The **default is unchanged behavior.** With verification `off` — and, crucially,
  with the recommended default `warn` — debswarm serves exactly what it serves
  today: nothing is ever refused. `warn` only *observes*; it never changes the
  bytes APT receives.
- APT's client-side end-to-end verification is **untouched** in every mode. This
  feature is defense for the cases APT does not cover (above), not a replacement
  for it.
- The SSRF/mirror allowlist and the existing SHA256 peer-poisoning defense are
  unchanged and orthogonal.
- Verification anchors **metadata**, and through it the package hashes. It does
  not re-sign or alter anything; APT still sees pristine upstream bytes.

## Design decisions

1. **Default `warn`, enforce opt-in (`off | warn | enforce`).** Considering the
   old behavior, `warn` is chosen as the default because it is **behaviorally
   identical to today from APT's perspective** — the daemon verifies each Release
   and the Packages-vs-Release hash, emits a metric + log + an `X-Debswarm-Unverified`
   response header on failure, but **still serves**. No repo can break; the operator
   gains real visibility (a tampered or unverifiable repo becomes observable).
   `enforce` (refuse to parse/serve an index whose Release fails signature or hash)
   is the opt-in that delivers actual fail-closed protection for the bypass/seed
   cases. `off` fully disables the verification work for operators who want
   literally zero change. This mirrors the backward-compatible posture chosen for
   `metrics.bind` in LAN server mode. A future `auto` mode (enforce where a key is
   discoverable, warn where not) is a natural stronger default once field data
   confirms key-discovery coverage — **not** the initial default.

2. **Trusted keys: auto-discover APT keyrings, with an optional `keyring_path`
   override.** On a normal Debian/Ubuntu host, reading the standard APT keyring
   locations makes debswarm trust **exactly what APT already trusts** — including
   third-party repos the operator configured via `signed-by=` — with zero config.
   This mirrors the `trust_known_repos` philosophy (curated default + explicit
   override). Bundling Debian/Ubuntu keys is rejected: it cannot cover third-party
   repos and carries a key-rotation/staleness maintenance burden. The optional
   `[security] keyring_path` covers the **cache-server / minimal-container** case,
   where the daemon's host may have empty keyrings (it never ran apt against the
   client repos) and the operator provisions a keyring directory — on such a host
   in `enforce`, `keyring_path` is effectively required.

3. **Library: `github.com/ProtonMail/go-crypto/openpgp`.** The historical
   `golang.org/x/crypto/openpgp` is deprecated and no longer shipped; ProtonMail's
   maintained fork is the de-facto standard, keeps debswarm **pure-Go** (no `gpgv`
   runtime dependency, consistent with the pure-Go SQLite ethos), and reads both
   binary keyrings (`ReadKeyRing`) and armored `.asc` (`ReadArmoredKeyRing`),
   clearsigned `InRelease` (`clearsign`), and detached `Release.gpg`
   (`CheckDetachedSignature`).

4. **Verify the signature; leave freshness (`Valid-Until`) to APT.** The daemon
   checks that the Release is validly *signed* by a trusted key. It does **not**
   hard-fail on an expired `Valid-Until`, because the offline stale-metadata
   serving shipped in the previous release intentionally serves an expired Release
   when the mirror is unreachable and relies on APT to reject it. Daemon-side
   expiry enforcement would break that feature. An expired-but-signed Release is
   still GPG-anchored, so debswarm serves it (optionally emitting an
   `expired`-labelled metric); APT does the freshness rejection. This keeps the
   two features consistent.

5. **`InRelease` preferred, `Release` + `Release.gpg` fallback.** Modern repos
   ship the inline-signed `InRelease`; some still use the detached pair. The
   verifier handles both and prefers `InRelease`.

## Detailed design

### Configuration surface (new `[security]` section)

```toml
[security]
# off | warn | enforce   (default: warn)
#   off     - no verification (pre-1.34 behavior)
#   warn    - verify; on failure log + metric + X-Debswarm-Unverified header, still serve
#   enforce - refuse to parse/serve an index whose Release fails signature or hash
verify_upstream_signatures = "warn"

# Optional file or directory of additional trusted public keys (binary .gpg or
# armored .asc), appended to the auto-discovered APT keyrings. Required in
# enforce mode on a host whose APT keyrings are empty (e.g. a cache-server).
keyring_path = ""

# Optional escape hatch: hosts served even when unverifiable, effective in
# enforce mode. For a repo whose signing key genuinely cannot be provisioned.
# verify_exempt_hosts = ["internal-repo.example.com"]
```

- Auto-discovered keyring locations (read once at startup): `/etc/apt/trusted.gpg.d/*.gpg`,
  `/etc/apt/trusted.gpg` (legacy), `/usr/share/keyrings/*.gpg`, `/etc/apt/keyrings/*`
  (`.gpg` and `.asc`). All loaded into one keyring.
- A single `[security]` section, distinct from the network/host allowlist surface,
  keeps "who may connect" (LAN mode) separate from "what upstream do we trust"
  (this feature).

### The verified-Release trust store

A per-dist store on the `Server`, populated when a Release is verified:

```
verifiedRelease {
    hashes     map[string]fileHash   // relPath (e.g. "main/binary-amd64/Packages.gz") -> {sha256,size}
    hashSet    map[string]struct{}   // all sha256 values, for O(1) by-hash lookup
    validUntil time.Time             // parsed, informational (not enforced; see decision #4)
    verifiedAt time.Time
}
store map[string]*verifiedRelease    // key: dist base URL, ".../dists/<dist>/"
```

- **Population (live):** when `InRelease`/`Release` is fetched through the proxy
  and passes signature verification, parse its `SHA256:` section into `hashes` +
  `hashSet` and store under its dist base URL.
- **Population (lazy, from cache):** when a `Packages` file (or the
  `warmIndexFromCacheOnce` path) needs a dist whose store entry is missing — the
  common case after a restart, where no `apt-get update` ran this session — read
  the already-cached `InRelease` from the metadata cache, verify it, and populate.
  This reuses the metadata cache (`InRelease` is already cached there) so enforce
  works across restarts without a mandatory live fetch.
- **On-demand (enforce only):** if neither a live nor a cached verified Release is
  available for a needed dist, enforce mode fetches `InRelease` for that dist
  before trusting the `Packages`. If offline and no verified Release is cached,
  enforce **refuses** (that is the point of fail-closed).

### Release verification flow (hooks `handleReleaseRequest`)

`handleReleaseRequest` today is a bare passthrough. It becomes:

1. Fetch the body as now (mirror-only, cached).
2. If mode ≠ `off`: verify.
   - `InRelease`: `clearsign.Decode`, verify the signature over the body against
     the keyring, take the verified plaintext as the Release content.
   - `Release`: pair with a `Release.gpg` fetch; `CheckDetachedSignature`.
3. On success: parse the `SHA256:` section, populate the store, record an
   `upstream_verify_total{result="verified"}` metric + audit event.
4. On failure (bad signature / no trusted key):
   - `warn`: log once per repo, metric (`sig-failed` / `no-key`), set
     `X-Debswarm-Unverified` on the response, serve anyway.
   - `enforce`: this dist is now un-anchored; its `Packages` gate (below) will
     refuse. The Release body itself is still served to APT (APT will reject it),
     so debswarm does not mask the failure from the client.

Verification never blocks or alters the Release bytes APT receives; it only
decides whether debswarm will *trust* the derived hashes.

### The Packages-vs-Release gate (hooks `serveFreshBody`, before `LoadFromData`)

When `isPackagesIndexURL(url)` is true and mode ≠ `off`, before
`index.LoadFromData`:

- Derive `distBaseURL` and `relPath` from the Packages URL.
- **`by-hash` URL** (`.../by-hash/SHA256/<h>`): verified iff `<h> ∈ store[dist].hashSet`.
  (by-hash guarantees content == `<h>`; Release vouches for `<h>`.) No re-hashing
  needed.
- **Plain URL**: compute the fetched body's SHA256 (stream through a
  `hashutil.HashingReader`, already used elsewhere) and compare to
  `store[dist].hashes[relPath]`.
- Result handling:
  - **verified** → parse into the index as today; the per-package SHA256s are now
    GPG-anchored.
  - **hash mismatch** → always refuse (`enforce` and `warn` alike: a *mismatch* is
    active tampering, not a missing key). Do not load into the index; do not cache.
    Metric `result="hash-mismatch"`. This is the `ErrHashMismatch` precedent.
  - **no verified Release for this dist** → `warn`: load + `X-Debswarm-Unverified`;
    `enforce`: attempt on-demand Release fetch, else refuse.

Because the `.deb`'s SHA256 is drawn from a now-anchored `Packages`, the existing
`.deb`-vs-SHA256 check transitively anchors the package too. **No separate `.deb`
verification step is required** — verifying the index is sufficient to close the
chain, including for the P2P swarm seed and `dpkg -i` from cache.

### Keyring loading

- One loader (new `internal/gpg` package, or `internal/security/keyring.go`):
  discover the standard dirs + `keyring_path`, detect binary vs armored per file,
  load all entities into one `openpgp.EntityList`. Log the count of keys loaded
  and the sources; a zero-key keyring in `warn` degrades to "unverified" (with a
  loud one-time warning), in `enforce` is a startup error (nothing could ever
  verify).
- Loaded once at startup. SIGHUP reload is out of scope for v1 (noted below).
- Keyring files hold public keys, so file-permission strictness matters less than
  the PSK; a light readability check is enough.

### Metrics, headers, audit

- Metric `debswarm_upstream_verify_total{result}` with `result ∈ {verified,
  no_key, sig_failed, hash_mismatch, no_release, expired}`. **Labelled by result
  only, never by repo/URL** (metric-cardinality is a tracked concern).
- Response header `X-Debswarm-Unverified: <reason>` on metadata served unverified
  in `warn` mode (visibility without breakage).
- Audit events `upstream_verify_ok` / `upstream_verify_failed` (with repo + reason)
  reuse the existing audit logger.

### Degradation / failure safety

- Any *internal* verification error that is not a definitive tampering signal
  (keyring unreadable, malformed signature library error, transient Release fetch
  failure) degrades to the mode's non-fatal path: `warn`/`off` serve as today;
  `enforce` refuses only when it cannot establish a positive anchor, never on an
  ambiguous internal error it can retry. The daemon must not become *less* robust
  than today for operators who leave the default.

### Out of scope for v1 (with rationale)

- **`Sources` / `.dsc` verification.** Sources indices are not parsed today
  (product gap #4). Verifying them is the same pattern and a natural follow-up,
  but adds parsing scope; deferred.
- **SIGHUP keyring/mode reload.** SIGHUP currently only reloads rate limits.
  Restart to change keys/mode in v1.
- **Keyserver auto-fetch of missing keys.** Deliberately not done — fetching keys
  over the network to decide trust is its own attack surface. Keys come from the
  host/operator only.
- **Revocation/expiry policy beyond the library's built-in checks**, and OpenPGP
  web-of-trust levels. v1 trusts any key in the configured keyring, as APT does.
- **Per-repo keyring pinning** (`signed-by` equivalent — restricting *which* key
  may sign *which* repo). APT supports this; v1 trusts the union keyring. A
  hardening follow-up.

## Implementation plan

Each phase builds and tests independently. File references are by
function/struct; the patterns to copy are named.

### Phase 1 — keyring loader + dependency
- Add `github.com/ProtonMail/go-crypto` to `go.mod` (`go get`, `go mod tidy`).
- New `internal/gpg` (or `internal/security/keyring.go`): discover standard APT
  keyring dirs + optional `keyring_path`; load binary + armored keys into one
  `EntityList`; expose `LoadKeyring(paths...) (*Keyring, error)` and a
  `Keyring.Empty()` check. Unit tests with a generated test key (binary + armored).

### Phase 2 — Release parser + signature verify
- New `internal/release` (or `internal/index/release.go`): parse the Release
  `SHA256:`/size/path section → `map[relPath]fileHash` + `Valid-Until`; verify
  clearsigned `InRelease` and detached `Release`+`Release.gpg` against a
  `*Keyring`. Pure functions, fixture-driven unit tests (valid, tampered body,
  wrong key, expired, both signing forms).

### Phase 3 — config surface
- `internal/config/config.go`: add `SecurityConfig` (`VerifyUpstreamSignatures
  string`, `KeyringPath string`, `VerifyExemptHosts []string`) and a top-level
  `[security]` block; default `VerifyUpstreamSignatures = "warn"` in
  `DefaultConfig()`; `Validate()` accepts only `off|warn|enforce` and checks
  `keyring_path` existence/readability when set (reuse `checkFilePermissions`
  idiom); accessor(s) for the parsed mode. Config-test coverage for each.

### Phase 4 — proxy wiring (the store + the two gates)
- `internal/proxy/server.go`: `verifiedRelease` store + mutex on `Server`; keyring
  + mode fields on `Config`/`Server`. Extend `handleReleaseRequest` to verify +
  populate the store; add the Packages-vs-Release gate in `serveFreshBody` before
  `LoadFromData`; lazy-populate from cached `InRelease`; on-demand fetch in
  enforce; wire the `warmIndexFromCacheOnce` path through the same gate. `off` is
  a fast bypass identical to today.
- `cmd/debswarm/daemon.go`: load the keyring, pass mode + keyring into
  `proxy.Config`; a `--verify-upstream` flag mirroring the config (optional).

### Phase 5 — metrics, headers, audit
- `internal/metrics`: `UpstreamVerifyTotal` counter vec (result label only).
- Set `X-Debswarm-Unverified` in the warn paths; emit audit events.

### Phase 6 — docs & examples (closes the trust-model code half)
- `packaging/config.example.toml` + `config.system.toml`: `[security]` block with
  the SECURITY-comment style.
- `docs/configuration.md`: new `[security]` section; the three modes; keyring
  discovery; the `Valid-Until`/offline-stale interaction.
- `SECURITY.md`: describe the new **optional** daemon-side verification and what
  each mode does — carefully, without over-claiming (default `warn` still relies
  on APT; `enforce` adds the anchor). This is the honesty-critical doc.
- `CHANGELOG.md` + README bullet; `docs/backlog.md`: move robustness/security #1's
  code half to "Recently addressed".

### Phase 7 — tests + soak
- **Deterministic Go tests** for tampering (the cases a live mirror won't
  reproduce): valid repo → verified + anchored; flipped Packages byte →
  `hash-mismatch` refused in both warn and enforce; unknown-key repo → warn serves
  + header, enforce refuses (unless `verify_exempt_hosts`); expired Release →
  signature-verified, served, `expired` metric, offline-stale still works.
- **Docker soak** (extends the existing harness): against a **real** Debian repo
  with auto-discovered keyrings — `apt-get update`/`install` succeed and log
  `result=verified`; against a real third-party repo (e.g. `pkgs.k8s.io`) confirm
  key discovery via `signed-by=`; a cache-server with empty keyrings + `enforce`
  refuses until `keyring_path` is provisioned; default (`warn`) run is
  byte-for-byte unchanged for APT and offline stale serving still works.

## Verification summary

Success = with the default (`warn`) the daemon behaves exactly as today for APT
while surfacing verification results via metrics/headers; with `enforce` a
tampered or unverifiable index is refused (so the swarm seed, `[trusted=yes]`
repos, and `dpkg -i`-from-cache are GPG-anchored) while a validly signed repo —
including third-party repos whose key is in the host's APT keyrings — works
untouched; expired-but-signed Release files still serve so offline stale-metadata
serving is preserved; and APT's own end-to-end verification is unaffected in every
mode.
