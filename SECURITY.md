# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.5.x   | :white_check_mark: |
| 0.4.x   | :white_check_mark: |
| 0.3.x   | :x:                |
| < 0.3   | :x:                |

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, please report them via email to: security@example.com

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

debswarm is designed to maintain APT's existing security guarantees while adding P2P distribution:

### Trust Model

1. **Release Files**: Always fetched from official mirrors. These are GPG-signed by Debian/Ubuntu and verified by APT. debswarm never serves Release files via P2P.

2. **Packages Index**: Fetched from mirrors and cached. Contains SHA256 hashes of all packages, signed transitively via Release files.

3. **Package Downloads**: Can be served via P2P. Every package is verified against its SHA256 hash from the signed Packages index before use.

4. **Peers**: Zero trust. Peers cannot serve malicious packages because all downloads are cryptographically verified. Hash mismatches result in:
   - Immediate rejection of the download
   - Blacklisting of the peer
   - Automatic retry from another peer or mirror

### What debswarm Does NOT Protect Against

- Compromise of the official Debian/Ubuntu signing keys
- Vulnerabilities in APT itself
- Local privilege escalation (debswarm runs as a dynamic user with restricted permissions)
- Network-level attacks on mirror connections (use HTTPS mirrors)

### Security Features

- **Hash Verification**: All P2P downloads verified against SHA256
- **Peer Blacklisting**: Peers serving bad data are automatically blacklisted
- **SSRF Protection** (v0.5.3+): URL validation blocks localhost, cloud metadata (169.254.x.x), and private networks
- **Response Limits** (v0.5.5+): Mirror responses capped at 500MB to prevent memory exhaustion
- **HTTP Security Headers** (v0.5.5+): Dashboard/metrics serve X-Content-Type-Options, X-Frame-Options, Content-Security-Policy
- **Error Disclosure Prevention** (v0.5.3+): Dashboard hides internal error details from users
- **Identity Protection**: Ed25519 keys stored with 0600 permissions
- **PSK Security**: Pre-shared keys never logged, only fingerprints displayed
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

- Physical access to the machine
- Compromise of the underlying OS
- Attacks requiring root/sudo access
- Side-channel attacks
