#!/usr/bin/env bash
# End-to-end test: drive a REAL apt client through the debswarm proxy against a
# real Debian repository, exercising the paths unit/integration tests cannot —
# APT's default HTTP pipelining over a large index (the ~8 MB Debian `main`
# Packages), the metadata cache, and daemon-side signature verification.
#
# This is the CI incarnation of the manual Docker soak (docs/testing.md). It runs
# as the container entrypoint and exits non-zero on the first failed assertion.
# The `apt-get update` steps are wrapped in `timeout` because the failure mode
# this guards against — a proxy that hangs mid-handler on a pipelined large index
# (the pre-1.30 ReadTimeout regression) — manifests as a hang, not an error.
set -u
export HOME=/root DEBIAN_FRONTEND=noninteractive
PROXY=http://127.0.0.1:9977
METRICS=http://127.0.0.1:9978
PASS=0; FAIL=0; DAEMON_PID=""

ok(){ PASS=$((PASS+1)); printf '  PASS: %s\n' "$*"; }
bad(){ FAIL=$((FAIL+1)); printf '  FAIL: %s\n' "$*"; }
say(){ printf '\n=== %s ===\n' "$*"; }

# Route apt through the proxy, but keep the image's default sources. Those are
# already http://deb.debian.org with `Signed-By` (so apt's own GPG check works)
# and include bookworm `main` (the ~8 MB Packages that exercises pipelining), all
# over plain HTTP — so the index flows through the cache/verify path rather than
# an opaque CONNECT tunnel. Overriding sources with keyless one-line entries only
# breaks apt's verification. ForceIPv4 avoids the apt-in-container IPv6 stall;
# Retries absorbs transient mirror hiccups in CI.
setup_apt(){
  mkdir -p /etc/apt/apt.conf.d
  cat > /etc/apt/apt.conf.d/90debswarm <<EOF
Acquire::http::Proxy "$PROXY";
Acquire::https::Proxy "$PROXY";
Acquire::ForceIPv4 "true";
Acquire::Retries "3";
EOF
}

write_config(){
  mkdir -p /etc/debswarm /var/cache/debswarm
  # cache_metadata on; upstream verification left at its default (auto) by
  # omitting [security]; fleet/mDNS off for a fast, deterministic single node.
  cat > /etc/debswarm/config.toml <<'EOF'
[network]
proxy_bind = "127.0.0.1"
proxy_port = 9977
listen_port = 4001
bootstrap_peers = []
[proxy]
trust_known_repos = true
[cache]
path = "/var/cache/debswarm"
max_size = "5GB"
cache_metadata = true
metadata_max_size = "1GB"
serve_stale_metadata = true
[privacy]
enable_mdns = false
[fleet]
enabled = false
[metrics]
port = 9978
bind = "127.0.0.1"
[logging]
level = "info"
EOF
}

start_daemon(){
  : > /tmp/daemon.log
  debswarm daemon >/tmp/daemon.log 2>&1 & DAEMON_PID=$!
  local i
  for i in $(seq 1 60); do
    kill -0 "$DAEMON_PID" 2>/dev/null || return 1
    if curl -fsS -o /dev/null "$METRICS/metrics" 2>/dev/null \
       && curl -sS -o /dev/null -x "$PROXY" http://deb.debian.org/ 2>/dev/null; then
      return 0
    fi
    sleep 0.5
  done
  return 1
}

mval(){ curl -fsS "$METRICS/metrics" 2>/dev/null | awk -v m="$1" '$1==m{print $2; f=1} END{if(!f)print 0}' | tail -1; }
vval(){ curl -fsS "$METRICS/metrics" 2>/dev/null | awk -v l="$1" '$0 ~ "debswarm_upstream_verify_total\\{result=\""l"\"}"{print $2; f=1} END{if(!f)print 0}' | tail -1; }
cache_count(){ curl -fsS "$METRICS/stats" 2>/dev/null | grep -oE '"cache_count":[0-9]+' | head -1 | cut -d: -f2; }

printf 'debswarm: %s\n' "$(debswarm version 2>/dev/null | head -1)"
setup_apt
write_config
say "start daemon"
if start_daemon; then ok "daemon started and proxy is accepting connections"
else bad "daemon did not become ready"; tail -20 /tmp/daemon.log; echo "PASS=$PASS FAIL=$FAIL"; exit 1; fi
grep -q "upstream signature verification enabled" /tmp/daemon.log \
  && ok "verification is on by default (auto) with an auto-discovered keyring" \
  || bad "default verification/keyring did not initialize"

say "apt-get update through the proxy (pipelined large index — hang guard)"
if timeout 180 apt-get update >/tmp/update1.log 2>&1; then ok "cold apt-get update succeeded (no pipelining hang)"
else bad "cold apt-get update failed or HUNG"; tail -25 /tmp/update1.log; fi
kill -0 "$DAEMON_PID" 2>/dev/null && ok "daemon still alive after update" || bad "daemon died during update"

say "metadata cache + verification engaged"
[ "$(mval debswarm_metadata_cache_misses_total)" -ge 1 ] 2>/dev/null \
  && ok "metadata cache populated (misses=$(mval debswarm_metadata_cache_misses_total))" \
  || bad "metadata cache not engaged (misses=$(mval debswarm_metadata_cache_misses_total))"
[ "$(vval verified)" -ge 1 ] 2>/dev/null \
  && ok "index verified against signed Release (verified=$(vval verified))" \
  || bad "no verified indices (verified=$(vval verified))"
[ "$(vval hash_mismatch)" -eq 0 ] 2>/dev/null \
  && ok "no hash mismatch on a real repo" || bad "unexpected hash_mismatch=$(vval hash_mismatch)"

say "warm apt-get update hits the metadata cache"
h0=$(mval debswarm_metadata_cache_hits_total)
timeout 180 apt-get update >/tmp/update2.log 2>&1 && ok "warm apt-get update succeeded" || { bad "warm update failed"; tail -15 /tmp/update2.log; }
h1=$(mval debswarm_metadata_cache_hits_total)
awk -v a="$h0" -v b="$h1" 'BEGIN{exit !(b>a)}' && ok "metadata cache hits climbed ($h0 -> $h1)" || bad "cache hits did not climb ($h0 -> $h1)"

say "apt-get install pulls and caches a real .deb through the proxy"
if timeout 180 apt-get install -y --download-only --reinstall hello >/tmp/install.log 2>&1; then ok "download-only install of hello succeeded"
else bad "install through the proxy failed"; tail -20 /tmp/install.log; fi
[ "$(cache_count)" -ge 1 ] 2>/dev/null && ok "package cached (cache_count=$(cache_count))" || bad "nothing cached (cache_count=$(cache_count))"
[ "$(vval hash_mismatch)" -eq 0 ] 2>/dev/null && ok "still no hash mismatch after install" || bad "hash_mismatch after install=$(vval hash_mismatch)"

say "daemon log is clean"
grep -qiE "panic|fatal|data race" /tmp/daemon.log && { bad "panic/fatal/race in daemon log"; grep -iE "panic|fatal|data race" /tmp/daemon.log | head; } || ok "no panic/fatal/race in daemon log"

kill "$DAEMON_PID" 2>/dev/null; wait "$DAEMON_PID" 2>/dev/null
say "SUMMARY"; printf 'PASS=%d  FAIL=%d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] && { echo "E2E OK"; exit 0; } || { echo "E2E FAILED"; exit 1; }
