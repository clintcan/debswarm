# Design: LAN server mode

**Status:** Proposed
**Author:** debswarm maintainers
**Date:** 2026-07-13
**Backlog item:** Product gap #1 — "No LAN cache-server mode"

## Context

Today the debswarm HTTP proxy binds hardcoded to `127.0.0.1` (`cmd/debswarm/daemon.go`,
the `proxy.Config` literal: `Addr: fmt.Sprintf("127.0.0.1:%d", cfg.Network.ProxyPort)`).
Only the machine running the daemon can use it as an APT cache, so every host that
wants caching must run a full daemon. This forecloses the highest-value deployment
shapes: one cache per office/lab, a shared CI-fleet cache, or a Kubernetes
DaemonSet/sidecar. Now that repository **metadata** is cached (PRs #98/#100), a shared
cache is far more valuable — a cold client fetches both packages *and* metadata from a
nearby box instead of the WAN — which makes this the next natural step.

The bind change itself is trivial. The real work, and the reason this needs a design,
is the **access-control story for remote clients**: the proxy currently has *zero*
inbound client gating, and exposing it on a network without a deliberate control is a
foot-gun. **Security is the priority for this feature**, so the design is secure by
default: exposure must be an explicit, validated operator decision.

### What this does and does not change (trust model)

- Upstream fetches remain allowlist/SSRF-gated to known Debian mirrors
  (`security.IsAllowedMirrorURLWithHosts`, applied per request in `handleRequest`;
  CONNECT is gated by `IsAllowedConnectTargetWithHosts` plus a post-resolution
  DNS-rebind recheck). LAN clients cannot drive fetches to arbitrary hosts.
- LAN clients still get APT's **end-to-end GPG verification** — debswarm passes bytes
  through unmodified. Serving a LAN client is therefore no weaker than serving
  localhost. The SHA256 check still protects the swarm from poisoned peers.
- The **only** new question this feature introduces is *which clients may connect*.
  That is exactly what the access control below governs.

## Design decisions

1. **Fail-closed exposure.** Binding the proxy to a non-loopback address *requires* an
   explicit `proxy_allowed_cidrs`; the daemon refuses to start otherwise. Exposure is a
   deliberate act, never a side effect of setting a bind address. (A "default to RFC1918
   ranges" alternative is safe and lower-friction, but fail-closed is the secure-by-default
   choice and was selected because security is the priority.)
2. **Gate the admin read surface too — opt-in, backward-compatible.** The metrics/admin
   server (`/stats`, `/dashboard`, `GET /api/cache/*`, `/metrics`) binds independently and
   stays loopback-only by default. When an allowlist is configured, it is applied to the
   admin read endpoints as well, closing the known unauthenticated-inventory-leak gap
   (backlog robustness/security #6). Unlike `proxy_bind`, a non-loopback `metrics.bind` is
   **not** fail-closed: it already exists today with warn-only behavior, so hard-failing it
   would break existing deployments on upgrade. With no allowlist set it keeps that
   behavior (strengthened warning); operators close the gap by opting into
   `proxy_allowed_cidrs`. Mutating endpoints stay loopback-only regardless.
3. **CIDR allowlist as the mechanism (v1).** Enforced against the real TCP peer address
   (never `X-Forwarded-For`). This is the right security/complexity balance for a LAN
   cache and matches apt-cacher-ng's model. Token/mTLS auth is a stronger but heavier
   future layer, out of scope here.

## Detailed design

### Configuration surface (`[network]`)

```toml
[network]
proxy_bind = "127.0.0.1"          # default = loopback-only (unchanged, no exposure)
# proxy_bind = "0.0.0.0"          # or a specific interface IP, e.g. "192.168.1.10"
proxy_allowed_cidrs = []          # REQUIRED when proxy_bind is non-loopback
# proxy_allowed_cidrs = ["192.168.1.0/24", "10.42.0.0/16", "fd00::/8"]
```

- A single canonical "trusted client" list, reused for **both** the proxy-port gate and
  the admin read-endpoint gate. One concept, one place to reason about.
- Loopback (`127.0.0.0/8`, `::1`) is **always** implicitly allowed so local APT and the
  local admin/watchdog paths keep working with zero configuration.

### Fail-closed validation (`(*Config).Validate()`)

Follows the existing `ValidationError` accumulation pattern (all failures returned
together). Mirror the port-range and indexed-slice (`bootstrap_peers`) validators
already in `Validate()`:

- Parse `proxy_bind` with `net.ParseIP`; empty → treated as the loopback default.
- Parse each `proxy_allowed_cidrs[i]` with `net.ParseCIDR`; invalid → indexed field error
  (`network.proxy_allowed_cidrs[%d]`).
- **Hard error** if `proxy_bind` is non-loopback and `proxy_allowed_cidrs` is empty:
  `"non-loopback proxy_bind requires proxy_allowed_cidrs to be set"`.
- **Loud WARN** (not an error) if any allowlisted CIDR covers public IP space — allowed
  for the operator who really means it, but never silent. Reuse the `net.IP` classifier
  idiom behind `security.IsBlockedIP` to decide "is this range private".
- `metrics.bind` is **not** fail-closed (backward compatibility). Unlike `proxy_bind`
  (brand-new config surface), the admin server already supports non-loopback binds today
  with warn-only behavior — the config wizard even offers a `0.0.0.0` profile. Hard-failing
  it would break existing deployments on upgrade. Instead, the admin read surface is gated
  *only when* an allowlist is configured (see "Admin read-surface gating"); a non-loopback
  `metrics.bind` with no allowlist keeps today's warn-only behavior, with the warning
  strengthened to recommend setting `proxy_allowed_cidrs`.
- Requires adding `"net"` to the `config.go` imports.

### Enforcement — the client gate

One small reusable gate, generalizing the existing `requireLoopback`
(`internal/proxy/api.go`):

```
clientAllowed(remoteAddr):
    ip := parse IP from r.RemoteAddr        // real TCP peer; NEVER trust X-Forwarded-For
    return ip.IsLoopback() || any(n.Contains(ip) for n in s.allowedClientNets)
```

- CIDRs are parsed **once at startup** into `s.allowedClientNets []*net.IPNet` (stored on
  the `Server` struct next to `allowedHosts`), never re-parsed per request.
- `gateClient(next http.Handler) http.Handler` returns `403` for disallowed clients,
  otherwise delegates. It is the proxy analogue of `requireLoopback` but allowlist-aware.
- Wrap the proxy server's handler in `NewServer` (`Handler: s.gateClient(mux)` where the
  mux currently registers only `/` → `handleRequest`). This covers **both** proxy GET and
  CONNECT, since both flow through `handleRequest`.
- Add the same non-loopback WARN the metrics path already logs, for the proxy bind.
- Use `net.JoinHostPort(bind, port)` to build the listen address (IPv6-safe; the current
  `fmt.Sprintf("%s:%d")` would mangle IPv6 literals).

### Admin read-surface gating

- When an allowlist **is** configured, wrap the entire metrics mux handler in
  `startMetricsServer` with the same `gateClient`. Loopback is always allowed, so the
  default (loopback-bound) admin server is unaffected, and an operator who binds it to the
  LAN gets the same allowlist protection as the proxy port.
- When the allowlist is **empty** and `metrics.bind` is non-loopback, the reads are **not**
  gated — this preserves the existing warn-only behavior so current deployments keep
  working after upgrade (backward compatibility). The existing non-loopback warning is
  strengthened to recommend setting `proxy_allowed_cidrs` to close the exposure.
- Mutating routes keep their inner `requireLoopback` wrappers as defense-in-depth — they
  remain loopback-only regardless, even for allowlisted LAN clients.
- Net effect: cache inventory, peer IDs, and Prometheus metrics stop being
  world-readable **for operators who opt into the allowlist**; existing exposed setups are
  not broken, only warned.

### Out of scope for v1 (with rationale)

- **Per-client request-rate limiting.** No HTTP-layer request throttle exists today
  (rate limiting is P2P byte-rate only). An allowlisted-but-abusive client could still
  drive WAN fetches — but it is on the operator's trust list. Track as a follow-up.
- **Token / basic / mTLS auth.** Stronger than CIDR but awkward for APT proxies and
  heavier to implement; a future hardening layer.
- **L2 spoofing resistance.** CIDR allowlisting assumes a non-hostile local segment;
  stronger isolation is a network/VPN concern, not debswarm's.

## Implementation plan

Each phase builds and tests independently. File references are by function/struct (line
numbers drift); the patterns to copy are named.

### Phase 1 — config
- `internal/config/config.go`:
  - Add to `NetworkConfig`: `ProxyBind string \`toml:"proxy_bind"\`` and
    `ProxyAllowedCIDRs []string \`toml:"proxy_allowed_cidrs"\``.
  - Set `ProxyBind: "127.0.0.1"` in the `Network` block of `DefaultConfig()` (mirrors how
    `Metrics.Bind` is defaulted).
  - Extend `Validate()` with the `proxy_bind` fail-closed rule and CIDR parsing (copy the
    port-range block and the `bootstrap_peers` indexed-loop block). `metrics.bind` is *not*
    fail-closed (backward compatibility — see Design decision #2).
  - Add a helper `(*NetworkConfig) ParsedAllowedCIDRs() ([]*net.IPNet, error)` (or parse
    in the daemon) so the proxy receives ready-to-use `*net.IPNet` values.
  - Add `"net"` import.

### Phase 2 — flag wiring
- `cmd/debswarm/main.go`: declare `proxyBind string` next to `metricsBind`.
- `cmd/debswarm/daemon.go`:
  - Register `cmd.Flags().StringVar(&proxyBind, "proxy-bind", "127.0.0.1", "Proxy bind address (SECURITY: non-loopback requires network.proxy_allowed_cidrs)")`.
  - Add `if cmd.Flags().Changed("proxy-bind") { cfg.Network.ProxyBind = proxyBind }` to the
    existing `Changed()` override block.
  - Change the `proxy.Config` literal:
    - `Addr: net.JoinHostPort(cfg.Network.ProxyBind, strconv.Itoa(cfg.Network.ProxyPort))`
      (both imports already present).
    - Add `AllowedClientCIDRs: <parsed []*net.IPNet from cfg.Network.ProxyAllowedCIDRs>`.

### Phase 3 — proxy enforcement
- `internal/proxy/server.go`:
  - Add `AllowedClientCIDRs []*net.IPNet` to `Config`; store as `allowedClientNets` on the
    `Server` struct (next to `allowedHosts`). Update `DefaultConfig()`'s hardcoded
    `Addr: "127.0.0.1:9977"` for consistency.
  - Implement `clientAllowed(r *http.Request) bool` and `gateClient(next http.Handler) http.Handler`.
  - In `NewServer`, wrap the proxy mux: `Handler: s.gateClient(mux)`.
  - Log the non-loopback bind WARN for the proxy (mirror the metrics warn).

### Phase 4 — admin read gating
- `internal/proxy/server.go`: in `startMetricsServer`, wrap the mux handler with
  `s.gateClient(...)` **only when an allowlist is configured** (`len(allowedClientNets) >
  0`); otherwise leave reads ungated and strengthen the existing non-loopback warning to
  recommend `proxy_allowed_cidrs` (backward compatibility). Keep `requireLoopback` on the
  mutating API routes regardless.
- `internal/proxy/api.go`: no route changes required (mutating routes already
  loopback-gated); confirm the read routes sit behind the outer gate when it is active.

### Phase 5 — docs & examples
- `packaging/config.example.toml` + `packaging/config.system.toml`: add `proxy_bind` and
  `proxy_allowed_cidrs` to `[network]`, using the SECURITY-WARNING comment style already
  used for `[metrics] bind`.
- `docs/configuration.md`: document both keys, the fail-closed rule, and the trust model
  (LAN clients still get APT GPG verification).
- `docs/comparison.md`: note LAN cache-server support now exists.
- `CHANGELOG.md`: `[Unreleased]` entry; README feature bullet.
- `docs/backlog.md`: move product gap #1 to "Recently addressed".

### Phase 6 — tests
- `internal/config/config_test.go`: fail-closed rejects non-loopback bind without
  allowlist; valid CIDRs pass; invalid CIDR surfaces an indexed error; loopback bind
  needs no allowlist.
- `internal/proxy/` (new `lan_gate_test.go`): `clientAllowed`/`gateClient` — loopback
  always allowed; in-CIDR allowed; out-of-CIDR → 403; `X-Forwarded-For` is ignored
  (spoofed header does not grant access); IPv4 and IPv6 CIDRs; gate is a no-op when
  bound loopback.

### Phase 7 — verification (soak)
- `go build ./... && go vet ./... && gofmt -l && go test` for the touched packages.
- **Docker two-node soak** (mirrors the existing offline soak harness): node A binds its
  proxy to the bridge IP with node B's subnet in `proxy_allowed_cidrs`; B's `apt-get
  update` + `apt-get install hello` succeed through A. Then:
  - a request from an IP outside the allowlist → `403`;
  - `GET /api/cache/packages` from a non-allowlisted host → `403`;
  - a config with a non-loopback `proxy_bind` and empty `proxy_allowed_cidrs` → daemon
    refuses to start with the fail-closed error;
  - default config (loopback bind) → unchanged behavior, local APT still works.

## Verification summary

Success = a second machine on an allowlisted subnet can `apt-get update`/`install`
through the cache; a machine outside the allowlist is refused at the proxy and the admin
read endpoints; a non-loopback bind without an allowlist fails to start; and the
loopback-default path is byte-for-byte unchanged.
