# Security Policy

## Supported Versions

Security fixes ship in a new release rather than as backports to older lines, so
the latest release is the supported one. If you are on an older version, upgrade
to the newest release to receive security updates.

| Version  | Supported          |
| -------- | ------------------ |
| 1.34.x   | :white_check_mark: |
| < 1.34.0 | :x:                |

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, please report them via email to: clintcan@users.noreply.github.com

You should receive a response within 48 hours. If for some reason you do not, please follow up via email to ensure we received your original message.

Please include the following information (as much as you can provide):

- Type of issue (e.g., buffer overflow, hash collision, privilege escalation)
- Full paths of source file(s) related to the issue
- The location of the affected source code (tag/branch/commit or direct URL)
- Any special configuration required to reproduce the issue
- Step-by-step instructions to reproduce the issue
- Proof-of-concept or exploit code (if possible)
- Impact of the issue, including how an attacker might exploit it

## Security Model

debswarm **preserves** APT's existing security guarantees while adding P2P distribution. By default it adds no cryptographic guarantee of its own: your protection against a tampered mirror is APT's client-side signature check, which debswarm leaves intact by passing fetched bytes through unmodified for APT to verify. Optionally (v1.34+), debswarm can perform its **own** daemon-side GPG verification of the repository `Release` and index — see [Upstream Signature Verification](#upstream-signature-verification-optional-v134) below. This is opt-in; the default mode (`warn`) verifies and reports but does not block, so unless you enable `auto` or `enforce`, APT's client-side check remains the actual guarantee.

### Trust Model

1. **Release Files**: Always fetched from official mirrors. These are GPG-signed by Debian/Ubuntu and verified by APT. debswarm never serves Release files via P2P.

2. **Packages Index**: Fetched from mirrors (usually over plain HTTP) and cached. It carries the SHA256 of every package and is signed transitively via the Release file. By default debswarm does not verify that signature (APT does, client-side); with `[security] verify_upstream_signatures = "enforce"` debswarm additionally verifies the `Release` signature against a trusted keyring and refuses an index whose hash it does not vouch for.

3. **Package Downloads**: Can be served via P2P. Every package is checked against its SHA256 from the Packages index debswarm fetched, before use. This catches a bad *peer* — its bytes won't match the hash — but the hash and the index arrive over the same upstream leg, so it does **not** catch a bad *mirror* that rewrites both (see below).

4. **Peers**: A peer's bytes must match the index SHA256 or the download is rejected and the peer blacklisted, so a peer cannot poison the swarm. Hash mismatches result in:
   - Immediate rejection of the download
   - Blacklisting of the peer
   - Automatic retry from another peer or mirror

### What debswarm Does NOT Protect Against

- **A compromised or malicious upstream mirror** (or a man-in-the-middle on the mirror connection). In the default configuration, if it serves a poisoned Packages index *and* matching poisoned bytes, debswarm's SHA256 check passes — the hash it checks against came from that same poisoned index. APT's GPG verification of the signed Release catches this on install. debswarm's optional `auto` and `enforce` modes (below) additionally close this gap at the daemon for any repository whose signing key it can discover, by verifying the `Release` signature and the index hash itself. Use HTTPS mirrors to reduce the MITM surface.
- **Anything that bypasses APT's signature verification** — `[trusted=yes]` sources, `Acquire::AllowInsecureRepositories`, or `dpkg -i` on a file pulled from debswarm's cache. These forgo the one check that makes the upstream trustworthy; debswarm's SHA256 check is not equivalent. (These are exactly the cases `enforce` mode is designed to harden, since it anchors the index — and thus each `.deb`'s hash — to GPG at the daemon.)
- Compromise of the official Debian/Ubuntu signing keys
- Vulnerabilities in APT itself
- Local privilege escalation (debswarm runs as a dynamic user with restricted permissions)
- Network-level attacks on mirror connections (use HTTPS mirrors)

### Upstream Signature Verification (optional, v1.34+)

debswarm can verify each repository's GPG-signed `Release` against a trusted
keyring, and each `Packages` index against that signed `Release`, before trusting
the SHA256 it lists — anchoring the hash to GPG instead of to the mirror's word.
Configured by `[security] verify_upstream_signatures`:

- **`off`** — no daemon-side verification (unchanged behavior).
- **`warn`** (default) — verify and report (metric, log, and an
  `X-Debswarm-Unverified` response header on failure) but **always serve**.
  Behaviorally identical to `off` for APT; it adds visibility, not enforcement, so
  APT's client-side check remains the guarantee.
- **`auto`** — refuse an index **only when verification was possible and it
  failed** (a signature-verified `Release` exists for the repository but the index
  does not match it), and fall back to `warn` when verification cannot be attempted
  at all (no trusted key for the repo, a flat/no-`dists` repo, or no cached
  `Release`). This closes the tampering and bypass-case gaps for every repository
  whose signing key is discoverable, without breaking one that cannot be verified —
  the strongest setting that never turns an unverifiable repo into a hard failure.
- **`enforce`** — refuse to parse/serve an index whenever it is not verified,
  **including** when verification could not be attempted. Fully fail-closed; it
  typically needs `keyring_path` and/or `verify_exempt_hosts` for repos whose key
  debswarm cannot discover.

Trusted keys are auto-discovered from the host's APT keyrings, so debswarm trusts
exactly what APT trusts. This does **not** replace APT's own end-to-end
verification, which is unaffected in every mode. It also does not verify
`Valid-Until` (freshness is left to APT). See `docs/configuration.md` → `[security]`.

### Security Features

- **Hash Verification**: All P2P downloads verified against SHA256
- **Upstream Signature Verification** (v1.34+, optional): daemon-side GPG check of the `Release` and index; `warn` reports, `auto` refuses where a key is discoverable, `enforce` always refuses. See above.
- **Peer Blacklisting**: Peers serving bad data are automatically blacklisted
- **SSRF Protection**: URL validation blocks localhost, cloud metadata (169.254.x.x), and private networks
- **Response Limits**: Mirror responses capped at 500MB to prevent memory exhaustion
- **HTTP Security Headers**: Dashboard/metrics serve X-Content-Type-Options, X-Frame-Options, Content-Security-Policy
- **Error Disclosure Prevention**: Dashboard hides internal error details from users
- **Identity Protection**: Ed25519 keys stored with 0600 permissions
- **PSK Security**: Pre-shared keys never logged, only fingerprints displayed
- **Audit Logging** (v1.8+): Comprehensive audit trails for security events
- **Sandboxing**: systemd service runs with restricted permissions:
  - `DynamicUser=yes`
  - `ProtectSystem=strict`
  - `PrivateTmp=yes`
  - `NoNewPrivileges=yes`
  - `CapabilityBoundingSet=` (no capabilities)
- **No Root Required**: Daemon runs as unprivileged user
- **Memory Limits**: Default 512MB memory limit

## Best Practices

1. **Use HTTPS Mirrors**: Configure APT to use HTTPS mirrors for Release file integrity
2. **Verify Signatures**: Ensure APT signature verification is enabled (default)
3. **Keep Updated**: Run the latest version of debswarm
4. **Monitor Logs**: Watch for hash mismatch warnings which may indicate attacks
5. **Network Segmentation**: Consider running debswarm on a dedicated network interface
6. **Harden with `auto` (or `enforce`)**: if you use `[trusted=yes]` repositories, `dpkg -i` files from the cache, or seed packages to P2P peers, set `[security] verify_upstream_signatures = "auto"` so debswarm anchors index hashes to GPG for every repository whose signing key it can discover, without breaking one it cannot; use `enforce` to additionally refuse repositories it cannot verify at all

## Threat Model

### In Scope

- Malicious peers attempting to serve bad packages
- DoS attacks against the local daemon
- Attempts to exhaust disk space via cache
- Network-level attacks on P2P connections
- SSRF attempts via malicious repository configurations
- Memory exhaustion via oversized mirror responses

### Out of Scope

- A compromised upstream mirror, or a MITM on the mirror connection, serving a poisoned index plus matching bytes (defended by APT's client-side GPG verification; and, when `[security] verify_upstream_signatures = "enforce"` is set, by debswarm itself for repos whose signing key it can discover — see the Trust Model above)
- Physical access to the machine
- Compromise of the underlying OS
- Attacks requiring root/sudo access
- Side-channel attacks
