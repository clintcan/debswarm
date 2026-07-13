# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.11.x  | :white_check_mark: |
| 1.10.x  | :white_check_mark: |
| 1.9.x   | :x:                |
| < 1.9   | :x:                |

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

debswarm **preserves** APT's existing security guarantees while adding P2P distribution. It does not add a cryptographic guarantee of its own: **debswarm performs no GPG verification.** Your protection against a tampered mirror is APT's client-side signature check, which debswarm leaves intact by passing fetched bytes through unmodified for APT to verify.

### Trust Model

1. **Release Files**: Always fetched from official mirrors. These are GPG-signed by Debian/Ubuntu and verified by APT. debswarm never serves Release files via P2P.

2. **Packages Index**: Fetched from mirrors (usually over plain HTTP) and cached. It carries the SHA256 of every package and is signed transitively via the Release file — but **debswarm does not verify that signature**; APT does, client-side.

3. **Package Downloads**: Can be served via P2P. Every package is checked against its SHA256 from the Packages index debswarm fetched, before use. This catches a bad *peer* — its bytes won't match the hash — but the hash and the index arrive over the same upstream leg, so it does **not** catch a bad *mirror* that rewrites both (see below).

4. **Peers**: A peer's bytes must match the index SHA256 or the download is rejected and the peer blacklisted, so a peer cannot poison the swarm. Hash mismatches result in:
   - Immediate rejection of the download
   - Blacklisting of the peer
   - Automatic retry from another peer or mirror

### What debswarm Does NOT Protect Against

- **A compromised or malicious upstream mirror** (or a man-in-the-middle on the mirror connection). If it serves a poisoned Packages index *and* matching poisoned bytes, debswarm's SHA256 check passes — the hash it checks against came from that same poisoned index. Only APT's GPG verification of the signed Release catches this; debswarm is not a substitute for it. Use HTTPS mirrors to reduce the MITM surface.
- **Anything that bypasses APT's signature verification** — `[trusted=yes]` sources, `Acquire::AllowInsecureRepositories`, or `dpkg -i` on a file pulled from debswarm's cache. These forgo the one check that makes the upstream trustworthy; debswarm's SHA256 check is not equivalent.
- Compromise of the official Debian/Ubuntu signing keys
- Vulnerabilities in APT itself
- Local privilege escalation (debswarm runs as a dynamic user with restricted permissions)
- Network-level attacks on mirror connections (use HTTPS mirrors)

### Security Features

- **Hash Verification**: All P2P downloads verified against SHA256
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

## Threat Model

### In Scope

- Malicious peers attempting to serve bad packages
- DoS attacks against the local daemon
- Attempts to exhaust disk space via cache
- Network-level attacks on P2P connections
- SSRF attempts via malicious repository configurations
- Memory exhaustion via oversized mirror responses

### Out of Scope

- A compromised upstream mirror, or a MITM on the mirror connection, serving a poisoned index plus matching bytes (defended by APT's client-side GPG verification, not by debswarm — see the Trust Model above)
- Physical access to the machine
- Compromise of the underlying OS
- Attacks requiring root/sudo access
- Side-channel attacks
