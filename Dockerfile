# Release container image for debswarm — published to ghcr.io/clintcan/debswarm
# by GoReleaser (see .goreleaser.yml `dockers:`). The binary is fully static
# (CGO_ENABLED=0), so this is a pure-COPY image on distroless: no shell, no
# package manager, no build-time network, and the arm variants build without
# QEMU emulation.
#
# GoReleaser stages the matching per-arch binary as `debswarm` at the build
# context root and copies the `extra_files` below preserving their repo-relative
# paths, so the COPY paths here match both a `docker build .` from the repo root
# and the GoReleaser build context.
FROM gcr.io/distroless/static-debian12:nonroot

COPY debswarm /usr/bin/debswarm

# Loopback-safe default config. The proxy and metrics listeners bind 127.0.0.1,
# so the image is secure by default but reachable only from inside the container.
# To run as a cache server for other containers/hosts, mount a config with a
# non-loopback proxy_bind + proxy_allowed_cidrs (see packaging/config.container.toml);
# a non-loopback bind without the allowlist is refused at startup (fail-closed).
COPY packaging/config.container.default.toml /etc/debswarm/config.toml

# Pre-create the cache and data dirs owned by the nonroot user (uid 65532):
# distroless has no shell to chown at runtime, the daemon cannot mkdir under
# root-owned /var as a non-root user, and a fresh named volume mounted at either
# path inherits this ownership. The daemon auto-selects /var/lib/debswarm for its
# persistent identity because the directory exists (see the data-dir resolution
# in cmd/debswarm/daemon.go), so `-v vol:/var/lib/debswarm` keeps a stable peer ID.
COPY --chown=65532:65532 packaging/container/cache /var/cache/debswarm
COPY --chown=65532:65532 packaging/container/data  /var/lib/debswarm

USER nonroot:nonroot
EXPOSE 9977/tcp 9978/tcp 4001/tcp 4001/udp
VOLUME ["/var/cache/debswarm", "/var/lib/debswarm"]
ENTRYPOINT ["/usr/bin/debswarm"]
CMD ["daemon"]
