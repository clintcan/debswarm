# Self-distribution: container image and signed apt repository

**Status:** Phase A (container image) shipped in v1.37.0. Phase B (signed apt
repository) infrastructure is committed but **inert** — the `apt-repo` CI job runs
only when the repository variable `APT_REPO_ENABLED=true`, so it goes live on the
first stable tag after the operator completes the key/secret/Pages setup below.

## Motivation

debswarm distributes *other* packages over P2P but had no first-class channel
for itself beyond GitHub-release tarballs/`.deb`s and `curl | bash`
(`scripts/install.sh`). That blocks `unattended-upgrades` and any fleet-wide
self-upgrade path, and there was no container image at all — see `docs/backlog.md`
product gap #3. This adds two hosting channels, layered onto the existing
tag-triggered release pipeline (GoReleaser + `build-deb` in
`.github/workflows/release.yml`), which already produces the 3-arch binaries and
`.deb`s per release.

---

## Phase A — Multi-arch container image (shipped v1.37.0)

Published to **`ghcr.io/clintcan/debswarm`** for linux `amd64`/`arm64`/`armv7`.

### Base image: `gcr.io/distroless/static-debian12:nonroot`
The binary is fully static (`CGO_ENABLED=0`), so it needs no libc, shell, or
package manager. distroless-static gives a tiny image that still ships CA
certificates (needed for HTTPS upstream fetch) and a nonroot user. Crucially the
`Dockerfile` is **pure `COPY` — no `RUN`** — so there is no build-time network
and the arm variants build on the amd64 runner **without QEMU emulation**. (Any
future `RUN` step, e.g. switching to `debian:bookworm-slim` + `apt-get`, would
reintroduce QEMU and slow emulated arm builds — a reason to stay on distroless.)

### Writable dirs and volume ownership
distroless has no shell to `chown` at runtime, and the daemon's own `MkdirAll`
cannot create under root-owned `/var/*` as the nonroot user (uid 65532). The
Dockerfile therefore pre-creates the cache and data dirs by `COPY --chown=65532`
of tracked skeleton dirs (`packaging/container/{cache,data}`). This also fixes
volume ownership: a fresh named volume mounted at either path inherits the
image dir's uid-65532 ownership, so `-v vol:/var/lib/debswarm` is writable.

The daemon auto-selects `/var/lib/debswarm` for its persistent libp2p identity
because that directory exists (data-dir resolution in `cmd/debswarm/daemon.go`:
`--data-dir` flag > `STATE_DIRECTORY` env > `/var/lib/debswarm` > `~/.local/share`).
So mounting `-v vol:/var/lib/debswarm` keeps a **stable peer ID** across restarts
with no extra env.

### Config strategy and the fail-closed bind constraint
`internal/config/config.go` `Validate()` refuses to start when `proxy_bind` is
non-loopback and `proxy_allowed_cidrs` is empty (LAN server mode is fail-closed),
and there is **no CLI flag / env var** for `proxy_allowed_cidrs`. A reachable
container therefore needs a **config file**, not just `-p`/flags. We ship two:

- **`packaging/config.container.default.toml`** — baked to `/etc/debswarm/config.toml`.
  Loopback-safe (`proxy_bind`/`metrics.bind` = `127.0.0.1`), so the image is
  secure by default but reachable only from inside the container.
- **`packaging/config.container.toml`** — opt-in **server** config the operator
  mounts over the default: `proxy_bind = "0.0.0.0"` + an RFC1918
  `proxy_allowed_cidrs` allowlist. Metrics also bind `0.0.0.0`, gated by the same
  allowlist (the metrics/admin read endpoints honor `proxy_allowed_cidrs` —
  `internal/proxy/server.go`).

Two consequences to document for users:
1. Because the default binds loopback, `-p 9977:9977` **alone does not work**
   (published ports land on the bridge interface, not loopback) — a reachable
   deployment must mount a non-loopback config.
2. **Docker SNAT caveat:** for host-published ports Docker rewrites the client
   source to the bridge gateway, so per-client CIDR filtering cannot distinguish
   external clients — restrict at the host (`-p 192.168.1.10:9977:9977` + a host
   firewall). For **container-to-container** on a user-defined bridge (an app
   service pointing `Acquire::http::Proxy` at `http://debswarm:9977`), source IPs
   are the real bridge addresses and the RFC1918 allowlist gates correctly. This
   is the supported topology.

### Build & publish (GoReleaser)
`.goreleaser.yml` gains `dockers:` (one per arch, `use: buildx`, per-arch
`--platform` + OCI labels, image `…:{{.Version}}-<arch>`) and `docker_manifests:`
assembling three manifests: `{{.Version}}` (always pushed), `{{.Major}}.{{.Minor}}`
and `latest` (both `skip_push: auto`, so a prerelease tag never clobbers the
stable moving tags — the repo already runs `prerelease: auto`). GoReleaser reuses
the already-built static binaries (no rebuild) and stages `extra_files` at their
repo-relative paths, matching the Dockerfile `COPY` paths.

The `release` job in `.github/workflows/release.yml` adds `packages: write`, a
`docker/setup-buildx-action` step, and a `docker/login-action` step (registry
`ghcr.io`, `GITHUB_TOKEN`) before GoReleaser. No PAT and no QEMU are needed.

**One-time GHCR setup (after the first push):** the package is created private
and unlinked — set it **Public** and **link to the repository** once.

---

## Phase B — Signed apt repository (infra landed, inert until enabled)

Host the repo on **GitHub Pages** (a `gh-pages` branch holding the reprepro
`dists/`+`pool/` tree + the published public key), served at
`https://clintcan.github.io/debswarm/`. Tooling: **reprepro** — the smallest
correct tool for a single-suite pool repo; one `includedeb` builds and signs
`Release`/`InRelease` and prunes superseded `.deb`s so `pool/` stays small.

The CI job and the reprepro config (`packaging/apt/conf/distributions`) are
committed but **inert**: the `apt-repo` job runs only when the repository
variable `APT_REPO_ENABLED` is `true`, so merging it cannot affect releases
until the operator opts in after completing the setup below.

### Design decisions
- **CI never hardcodes the key id.** The job derives the signing key's
  fingerprint from the imported `APT_GPG_PRIVATE_KEY` and substitutes it into the
  committed `conf/distributions` template (`SignWith: __FINGERPRINT__`).
- **CI publishes the public key** (armored `.asc` + dearmored `.gpg`) exported
  from the secret, so the operator does **not** commit a public key.
- **Passphrase-optional.** A passphrase-less key needs no signing setup; if
  `APT_GPG_PASSPHRASE` is set, the job presets it via loopback pinentry so
  reprepro's gpgme signing does not prompt. A passphrase-less dedicated,
  revocable repo key is recommended (GH secrets are encrypted at rest).

### Operator prerequisites (CI never generates or embeds a private key)
1. Generate a dedicated sign-only key — passphrase-less recommended:
   ```
   gpg --batch --gen-key <<EOF
   %no-protection
   Key-Type: RSA
   Key-Length: 4096
   Key-Usage: sign
   Name-Real: debswarm apt repository
   Name-Email: apt@debswarm
   Expire-Date: 0
   %commit
   EOF
   gpg --armor --export-secret-keys <KEYID> > debswarm-apt-private.asc   # secret; do NOT commit
   ```
2. Add the private key as Actions secret **`APT_GPG_PRIVATE_KEY`** (+ `APT_GPG_PASSPHRASE`
   only if the key has one).
3. Create an orphan `gh-pages` branch; enable Settings → Pages → Deploy from
   branch → `gh-pages` / root.
4. Set the repository **variable** `APT_REPO_ENABLED` = `true` (Settings → Secrets
   and variables → Actions → Variables). The next stable tag then publishes the repo.

### CI job (`apt-repo` in `release.yml`)
`needs: [check-branch, release, build-deb]`, `contents: write`, gated on
`vars.APT_REPO_ENABLED == 'true'` and stable tags (no `-` in the tag),
`concurrency: apt-repo`: checkout `main` (for the template) + `gh-pages`
(persistent `conf/`+`db/`+`dists/`+`pool/`), `apt-get install reprepro`, import the
key + derive the fingerprint, write `conf/distributions`, `gh release download
"$TAG" --pattern '*.deb'`, `reprepro includedeb stable *.deb` (idempotent;
supersede+prune), export the public key to the Pages root, commit/push. Cap
`gh-pages` history with a periodic orphan-squash — only the current tree is served.

### User-facing install (add to README once the repo is live)
```bash
curl -fsSL https://clintcan.github.io/debswarm/debswarm-archive-keyring.gpg \
  | sudo tee /usr/share/keyrings/debswarm-archive-keyring.gpg > /dev/null
```
```
# /etc/apt/sources.list.d/debswarm.sources
Types: deb
URIs: https://clintcan.github.io/debswarm
Suites: stable
Components: main
Signed-By: /usr/share/keyrings/debswarm-archive-keyring.gpg
```
Then `sudo apt-get update && sudo apt-get install debswarm`. For automatic
upgrades, drop in `/etc/apt/apt.conf.d/52debswarm-unattended`:
```
Unattended-Upgrade::Origins-Pattern {
  "origin=debswarm,codename=stable";
};
```

---

## Validation

**Phase A (per release + locally):** build the image from a context mirroring
GoReleaser's staging (binary at root as `debswarm` + `packaging/…` preserved);
`docker run … version` smoke; a two-container functional soak (daemon with the
server config mounted + a `debian:bookworm-slim` client driving apt through
`http://<daemon>:9977`, `cache_count` climbing in `/stats`). On Docker Desktop
the **daemon** container needs `--dns 8.8.8.8 --dns 1.1.1.1` (it reaches
deb.debian.org, which the embedded resolver returns IPv6-only). After the tagged
push, `docker buildx imagetools inspect …:<ver>` shows three platforms.

**Phase B:** in a clean container, install the key, add the `.sources`,
`apt-get update` (must validate `InRelease`, no `NO_PUBKEY`), `apt-get install
debswarm`, and `unattended-upgrade --dry-run -d | grep debswarm`.

## Risks
- **GHCR** first push is private/unlinked → set Public + link once; `packages: write` required.
- **Container reachability** is the main UX trap (loopback default; SNAT vs CIDR) — steer users to container-to-container.
- **Multi-arch manifest**: a failed per-arch push aborts the manifest; armv7 is `linux/arm/v7` (`goarm: "7"`).
- **Phase B signing**: the reprepro loopback-pinentry recipe is fragile → passphrase-less key is the robust simplification.
- **Naming**: `ghcr.io/clintcan/debswarm` / `clintcan.github.io/debswarm` diverge from the `github.com/debswarm/debswarm` module path — cosmetic; reconcile in a separate follow-up (module rename is breaking).
