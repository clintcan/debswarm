# E2E test image: real Debian + real APT, driving the debswarm proxy.
# Build context is this directory; the binary is built into it by CI (or by hand
# — see README.md) before `docker build`.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
COPY debswarm /usr/local/bin/debswarm
COPY e2e.sh /e2e.sh
RUN chmod +x /usr/local/bin/debswarm /e2e.sh
ENTRYPOINT ["bash", "/e2e.sh"]
