# End-to-end APT test

Drives a **real APT client through the debswarm proxy** against a real Debian
repository, inside a `debian:bookworm-slim` container. It exercises the paths
unit and integration tests cannot reach:

- APT's default **HTTP pipelining** over a large index (the ~8 MB Debian `main`
  `Packages`) — the failure class behind the pre-1.30 `ReadTimeout` hang, which
  is invisible to any test that does not drive a real pipelining client. The
  `apt-get update` steps are wrapped in `timeout` so a regression surfaces as a
  failed job rather than a stuck one.
- The **metadata cache** (cold miss → warm hit across two `apt-get update`s).
- **Daemon-side signature verification** at its default (`auto`): the index is
  verified against the signed `Release`, reported via
  `debswarm_upstream_verify_total{result="verified"}`. (A real Debian repo
  verifies cleanly, so `auto` serves it exactly like `warn` here — the refuse
  path is covered by unit tests and the GPG soak.)
- A real `.deb` fetched and cached through the proxy (`apt-get install`).

Runs in CI as the `e2e` job in `.github/workflows/ci.yml`. It hits
`deb.debian.org` (a default-allow-listed mirror), so it needs outbound network.

## Run it locally

```bash
# From the repo root — build the linux binary into this dir, then build+run:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" \
  -o test/e2e/debswarm ./cmd/debswarm
docker build -t debswarm-e2e test/e2e
docker run --rm debswarm-e2e
```

A clean run ends with `E2E OK` and exit 0; any failed assertion prints
`E2E FAILED` and exits 1. The built binary (`test/e2e/debswarm`) is gitignored.
