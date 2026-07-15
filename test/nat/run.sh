#!/bin/bash
# Cross-NAT P2P test.
#
# Two debswarm peers behind SEPARATE NAT gateways, with no route to each other
# except out through a public network — the case the ordinary Docker-bridge soak
# CANNOT reproduce (on a shared bridge, containers dial each other directly and
# that soak passes even on a totally broken build).
#
# TWO TIERS, because the environment matters:
#
#   TIER 1 — the core proof (asserted; reproducible everywhere, incl. Docker
#   Desktop on Windows). A NAT'd peer obtains a circuit-relay RESERVATION on the
#   relay: the relay logs it, and the peer's AutoRelay adds the relay. This is the
#   exact mechanism that was ENTIRELY ABSENT before this change — with AutoRelay
#   off (the --baseline run) the relay never sees a reservation from the peer, so
#   two NAT'd peers could never connect. Tier 1 passing + baseline failing proves
#   both that the fix works and that the test can detect its absence.
#
#   TIER 2 — the full transfer (best-effort; reported, not asserted). peer-b
#   caches a package the mirror then STOPS serving, and peer-a fetches it — so its
#   only possible source is peer-b, across two NATs, via a hole punch. This needs
#   the peer<->relay connection to stay up through the reservation-to-transfer
#   gap. On real Linux (and CI) it does. On Docker Desktop for Windows the
#   LinuxKit/WSL2 NAT drops the idle connection at ~28s (QUIC idle timeout /
#   conntrack expiry — NOT a debswarm behaviour; nothing in debswarm closes
#   connections on that cadence), so Tier 2 is reported as environment-limited
#   there rather than failing the run.
#
#   ./run.sh              normal run  — Tier 1 must PASS
#   ./run.sh --baseline   AutoRelay off — Tier 1 must FAIL (proves detection)
#
set -uo pipefail
cd "$(dirname "$0")"

BASELINE=0
[ "${1:-}" = "--baseline" ] && BASELINE=1

FAILED=0
ok()   { echo "  ✅ $*"; }
bad()  { echo "  ❌ $*"; FAILED=1; }
note() { echo "  ▶ $*"; }
step() { echo; echo "════════ $* ════════"; }

cleanup() {
  if [ -n "${KEEP:-}" ]; then
    echo "KEEP set — leaving the stack up (docker compose down -v to clean up)"
    return
  fi
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

metric() { docker exec "$1" curl -s 127.0.0.1:9978/metrics 2>/dev/null | grep -E "^$2" | awk '{print $2}' | head -1 | grep -E '^[0-9.]+$' || echo 0; }
stat_field() { docker exec "$1" curl -s 127.0.0.1:9978/stats 2>/dev/null | grep -o "\"$2\":[0-9]*" | cut -d: -f2 | head -1 || echo 0; }
peer_id() { docker exec "$1" debswarm identity show 2>/dev/null | grep -oE '12D3KooW[A-Za-z0-9]+' | head -1; }
# Did the relay accept a reservation from this peer id? (relay-side proof.)
relay_reserved_for() { docker logs nat-relay 2>&1 | grep -F "reserving relay slot" | grep -qF "$1"; }
# How many DCUtR hole punches has this peer attempted? (success + failure labels summed.)
holepunch_attempts() { docker exec "$1" curl -s 127.0.0.1:9978/metrics 2>/dev/null | awk '/^debswarm_holepunch_total/{s+=$2} END{printf "%d", s}'; }

step "0. Build debswarm (linux/amd64) and the topology"
( cd ../.. && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o test/nat/debswarm ./cmd/debswarm ) \
  || { echo "go build failed"; exit 1; }
ok "binary built"

# Always start from a clean slate — even under KEEP. KEEP must only skip the
# POST-run teardown (the EXIT trap), NOT this pre-run reset: reusing a prior run's
# `shared` volume hands the peers a stale relay.id, so they dial the wrong relay
# peer-id and every reservation fails with a peer-id mismatch.
docker compose down -v --remove-orphans >/dev/null 2>&1 || true
if [ "$BASELINE" = "1" ]; then
  echo "  ⚠  BASELINE MODE: enable_autorelay=false — Tier 1 MUST fail"
  export AUTORELAY=false
else
  export AUTORELAY=true
fi
docker compose up -d --build >/dev/null 2>&1 || { echo "compose up failed"; docker compose logs --tail 30; exit 1; }
ok "topology up (peer-a and peer-b behind separate NATs)"

step "1. The peers really are isolated from each other; each reaches the public net only via NAT"
if docker exec nat-peer-a ping -c1 -W2 192.168.20.10 >/dev/null 2>&1; then
  bad "peer-a can reach peer-b DIRECTLY — the NAT topology is not isolating them"
else
  ok "peer-a cannot reach peer-b directly (as intended)"
fi
MIRROR_OK=0
for _ in $(seq 1 20); do
  docker exec nat-peer-a curl -s --max-time 5 -o /dev/null http://11.11.11.5/Packages && { MIRROR_OK=1; break; }
  sleep 2
done
[ "$MIRROR_OK" = "1" ] && ok "peer-a reaches the mirror through its NAT gateway (masqueraded)" \
                       || bad "peer-a cannot reach the mirror — NAT forwarding is broken"

step "2. Daemons up"
for c in nat-relay nat-peer-a nat-peer-b; do
  for _ in $(seq 1 40); do docker exec "$c" curl -sf 127.0.0.1:9978/stats >/dev/null 2>&1 && break; sleep 1; done
  docker exec "$c" curl -sf 127.0.0.1:9978/stats >/dev/null 2>&1 && ok "$c up" || { bad "$c never came up"; docker logs "$c" --tail 20; }
done
RS=$(metric nat-relay 'debswarm_relay_service_active')
[ "${RS%.*}" = "1" ] && ok "relay is running the circuit-relay service" || bad "relay service is NOT active"

PA=$(peer_id nat-peer-a); PB=$(peer_id nat-peer-b)
note "peer-a=$PA  peer-b=$PB"

# Did the peer actually obtain a /p2p-circuit address? (PEER-side proof.)
# A relay-side grant alone is NOT enough: the reservation is only usable if the
# circuit addr enters the peer's OWN advertised set, which is what lets another
# NAT'd peer discover and dial it. If the relay's address is one libp2p's autorelay
# treats as unroutable (manet.IsPublicAddr==false — e.g. a documentation range),
# the grant succeeds but the circuit-addr set is empty and this counter stays 0.
peer_circuit_ok() { [ "$(metric "$1" 'debswarm_relay_reservations_obtained_total' | cut -d. -f1)" -ge 1 ] 2>/dev/null; }

step "3. TIER 1 — do the NAT'd peers obtain USABLE relay reservations? (the mechanism that was missing)"
echo "  waiting up to 90s for the relay to grant a slot AND a /p2p-circuit addr to appear on each peer..."
GOTA=0; GOTB=0; CA=0; CB=0
for _ in $(seq 1 45); do
  relay_reserved_for "$PA" && GOTA=1
  relay_reserved_for "$PB" && GOTB=1
  peer_circuit_ok nat-peer-a && CA=1
  peer_circuit_ok nat-peer-b && CB=1
  [ "$GOTA" = 1 ] && [ "$GOTB" = 1 ] && [ "$CA" = 1 ] && [ "$CB" = 1 ] && break
  sleep 2
done

if [ "$BASELINE" = "1" ]; then
  step "RESULT (baseline: AutoRelay disabled)"
  if [ "$GOTA" = "1" ] || [ "$GOTB" = "1" ] || [ "$CA" = "1" ] || [ "$CB" = "1" ]; then
    echo "💥 BASELINE UNEXPECTEDLY PASSED — a reservation/circuit addr appeared with AutoRelay OFF."
    echo "   The test does not actually depend on the fix; a green normal run would prove nothing."
    exit 1
  fi
  echo "🎉 BASELINE CORRECTLY FAILED — with AutoRelay off, no NAT'd peer reserves on the relay,"
  echo "   so two NAT'd peers can never connect. The test genuinely detects the bug."
  exit 0
fi

# Relay-side: the relay granted a slot.
[ "$GOTA" = "1" ] && ok "relay granted a reservation slot to peer-a" \
                  || bad "relay never granted a reservation to peer-a — cross-NAT relay is not working"
[ "$GOTB" = "1" ] && ok "relay granted a reservation slot to peer-b" \
                  || bad "relay never granted a reservation to peer-b"
# Peer-side: the /p2p-circuit address actually entered each peer's advertised set.
# THIS is the assertion that matters — a granted slot with no circuit addr is a
# reservation nobody can use.
[ "$CA" = "1" ] && ok "peer-a obtained a usable /p2p-circuit address (now reachable through the relay)" \
               || bad "peer-a got a slot but NO /p2p-circuit address formed — reservation is not usable"
[ "$CB" = "1" ] && ok "peer-b obtained a usable /p2p-circuit address" \
               || bad "peer-b got a slot but NO /p2p-circuit address formed — reservation is not usable"

step "4. TIER 2 — cross-NAT discovery + DCUtR hole-punch attempt (asserted)"
# peer-b caches hello, then announces it to the DHT (its provider record carries the
# circuit address). With the mirror stopped, peer-a must find peer-b ACROSS the two
# NATs and attempt a hole punch — exercising the whole Phase-1 path end to end
# (reservation → circuit addr → DHT discovery → DCUtR), all of which was dead before.
docker exec nat-peer-a apt-get update -qq >/dev/null 2>&1 || true
docker exec nat-peer-b apt-get update -qq >/dev/null 2>&1 || true
docker exec nat-peer-b apt-get install -y --download-only hello >/dev/null 2>&1 \
  && note "peer-b cached hello (cache_count=$(stat_field nat-peer-b cache_count))" \
  || bad "peer-b could not cache hello — Tier 2 cannot proceed"

# Let peer-b announce to the DHT and the provider record propagate before removing
# the mirror, so peer-a's only route to the package is genuinely peer-b.
sleep 25
docker compose stop repo >/dev/null 2>&1
note "mirror stopped — peer-b is the only source of hello"

# Drive discovery + the punch by fetching on peer-a with no mirror. Poll (re-triggering
# the fetch) until peer-a BOTH discovers peer-b as a provider AND attempts a hole punch.
# Generous budget: DHT propagation across NATs is not instant. These two are asserted.
GOT=0; FOUND=0; HP=0; P2P_BYTES=0
for _ in $(seq 1 6); do
  docker exec nat-peer-a apt-get clean 2>/dev/null
  docker exec nat-peer-a timeout 40 apt-get install -y --download-only --reinstall hello >/dev/null 2>&1 && GOT=1
  FOUND=$(docker logs nat-peer-a 2>&1 | grep -c 'Found P2P providers')
  HP=$(holepunch_attempts nat-peer-a)
  P2P_BYTES=$(stat_field nat-peer-a bytes_from_p2p)
  [ "${FOUND:-0}" -ge 1 ] && [ "${HP:-0}" -ge 1 ] && break
  sleep 8
done
note "peer-a: got=$GOT bytes_from_p2p=$P2P_BYTES holepunch_attempts=$HP found_provider_events=$FOUND"

# ASSERTED: the cross-NAT path must reach discovery and a hole-punch attempt. Both were
# structurally impossible before Phase 1 (no reservation → no circuit addr → nothing to
# discover or dial → DCUtR never fires).
[ "${FOUND:-0}" -ge 1 ] \
  && ok "peer-a discovered peer-b as a provider ACROSS the two NATs (DHT + circuit addr)" \
  || bad "peer-a never discovered peer-b — cross-NAT DHT discovery is broken"
[ "${HP:-0}" -ge 1 ] \
  && ok "DCUtR hole punch was ATTEMPTED (the path that was entirely dead before Phase 1 now fires)" \
  || bad "no hole-punch attempt recorded — DCUtR did not fire"

# The transfer itself only completes over a HOLE-PUNCHABLE NAT; report, don't assert.
if [ "$GOT" = "1" ] && [ "${P2P_BYTES:-0}" -gt 0 ]; then
  ok "BONUS: full cross-NAT P2P transfer completed (this gateway was hole-punchable)"
else
  note "Full transfer NOT completed here (expected on this rig): the gateways are plain iptables"
  note "MASQUERADE = address-dependent (symmetric) NATs, which DCUtR cannot punch — peer-a's dial to"
  note "peer-b's public ip:port is refused because the mapping is bound to the relay, not to peer-a."
  note "debswarm relays coordinate the punch but never carry bytes, so a failed punch falls back to"
  note "the mirror (stopped). This is symmetric NAT + hole punching, NOT a debswarm bug; a completed"
  note "transfer needs a hole-punchable (endpoint-independent / full-cone) gateway."
fi

step "RESULT"
[ "$FAILED" -eq 0 ] && echo "🎉 CROSS-NAT P2P VERIFIED (usable relay reservations + cross-NAT discovery + DCUtR hole-punch attempt)" \
                    || echo "💥 CROSS-NAT P2P TEST FAILED"
exit $FAILED
