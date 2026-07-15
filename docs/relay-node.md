# Deploying a Public Relay Node (DigitalOcean / AWS)

A **relay node** is a publicly-reachable debswarm instance that lets peers behind
NAT reach each other. NAT'd peers reserve a slot on it and obtain a `/p2p-circuit`
address; another NAT'd peer discovers that address through the DHT and the two
coordinate a DCUtR **hole punch** to a direct connection. A cloud VM — a
DigitalOcean droplet or an AWS EC2 instance — is the natural home for it: the relay
role *is* "the publicly-reachable node," which is exactly what a cloud VM with a
public IP is (unlike a NAT'd home box).

A relay is a superset of a [bootstrap node](bootstrap-node.md): it should also
bootstrap the DHT, and the base steps (install, firewall, systemd, retrieving the
multiaddr) are shared. This doc covers the **relay-specific** configuration and the
**cloud-provider** specifics; see `bootstrap-node.md` for the common material.

## When you need one

- Your swarm has peers behind NAT that need to reach each other (home/office
  machines, CI runners on NAT'd networks).
- **Especially** if peers sit behind *symmetric* NAT (CGNAT, many corporate
  firewalls): DCUtR cannot hole-punch symmetric NAT, so those pairs need the relay
  to **carry** small packages — see [Carrying data](#optional-carry-data-for-symmetric-natd-peers).
- A **private (PSK) swarm** always needs at least one relay you run, because it has
  no public DHT to discover relays through.

## Sizing and cost

| Mode | VM | Bandwidth cost |
|------|----|----------------|
| **Coordinate-only** (default) | Smallest available — DO `s-1vcpu-512mb-10gb`, AWS `t4g.nano`/`t3.micro` | Negligible — only hole-punch *signaling* crosses it, not package bytes |
| **Data-carrying** (raised `buffer_size`) | 1 vCPU / 1 GB, more if busy | Real — package bytes flow through; watch egress (AWS bills per-GB, DO gives each droplet a monthly transfer allowance) |

Start coordinate-only. Only turn on data-carrying if you have symmetric-NAT'd peers
that must exchange packages P2P (see the last section).

## 1. Provision the VM

- **OS:** Ubuntu 22.04+ or Debian 12+.
- **DigitalOcean:** create a droplet; its public IPv4 is assigned directly to the
  interface. Optionally attach a **Reserved IP** so the address survives droplet
  replacement.
- **AWS:** launch an EC2 instance, then **allocate and associate an Elastic IP** so
  the address is stable. Note the NAT nuance in step 3.

## 2. Open the firewall — TCP *and* UDP

The relay listens on `listen_port` (default **4001**). You must open it for **both**
TCP (fallback) and UDP (QUIC, preferred). The missing-UDP-rule is the single most
common reason a "public" relay is unreachable.

**Cloud-level** (do this first — it fronts the VM):

- **DigitalOcean Cloud Firewall** — add two inbound rules: `TCP 4001` and `UDP 4001`,
  source `All IPv4`/`All IPv6`.
- **AWS Security Group** — add two inbound rules: `Custom TCP 4001` and `Custom UDP
  4001`, source `0.0.0.0/0` (and `::/0` if using IPv6).

**Host-level** (defense in depth):

```bash
sudo ufw allow 4001/tcp comment 'debswarm P2P TCP'
sudo ufw allow 4001/udp comment 'debswarm P2P QUIC'
sudo ufw reload
```

Keep the metrics endpoint (9978) and the APT proxy (9977) **closed** to the internet;
a dedicated relay does not expose either. See `bootstrap-node.md` for remote
monitoring via an SSH tunnel or `--metrics-bind` behind a firewall.

## 3. Public IP reachability

The relay only ever **accepts inbound** connections (NAT'd peers dial *out* to it to
reserve), so it needs its port reachable at a stable public IP.

- **DigitalOcean:** the droplet's public IPv4 is on the interface, so libp2p sees and
  advertises it directly. Nothing extra to do.
- **AWS:** the instance's OS sees only its **private** IP; the Elastic IP is 1:1
  NAT'd by AWS and is *not* on the interface. This is fine — libp2p binds `0.0.0.0`
  and accepts the NAT'd inbound traffic. Because you distribute the relay's multiaddr
  with the **Elastic IP hard-coded** (step 7), peers dial the right address; and once
  peers connect, AutoNAT/identify teaches the relay its observed public address so it
  is discoverable through the DHT too. With `force_reachability = "public"` (below)
  the relay advertises without waiting on AutoNAT.

## 4. Install debswarm

Any of the [bootstrap-node install options](bootstrap-node.md#installation) (release
binary, `.deb`, or build from source), **or** the container image — see step 6.

## 5. Relay configuration

`/etc/debswarm/config.toml`:

```toml
[network]
listen_port = 4001

# This node runs the circuit-relay service for NAT'd peers, and knows it is public,
# so it skips AutoNAT's slow/flaky guessing and needs no reservation of its own.
relay_service = "on"
force_reachability = "public"
enable_autorelay = false

# Bootstrap the public DHT (so the relay is discoverable and can discover peers).
# Omit or point at your own nodes for a private swarm.
bootstrap_peers = [
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
]

[privacy]
enable_mdns = false   # internet relay; no LAN discovery

# A dedicated relay doesn't proxy APT for anyone, so the proxy stays on its
# loopback-only default (nothing to configure). The libp2p host binds 0.0.0.0
# automatically — there is no separate listen-address setting.
```

libp2p already listens on `0.0.0.0` for the P2P port, so there is **no**
`listen_address` key — only `listen_port`.

For a **private (PSK) swarm**, add the PSK the same way as any node (`[privacy]
psk_file = "/etc/debswarm/swarm.key"`, generated with `debswarm psk generate`); the
relay must be in the same swarm as the peers it serves.

## 6. Run it

### Native (systemd)

Use the [systemd unit from bootstrap-node.md](bootstrap-node.md#systemd-service-production)
unchanged — it runs `debswarm daemon --config /etc/debswarm/config.toml` as an
unprivileged `debswarm` user with `ReadWritePaths=/var/lib/debswarm`. The identity
key persists at `/var/lib/debswarm/identity.key`, so the relay's peer ID is stable
across restarts (essential — peers pin it by ID; see step 7).

### Container (GHCR)

```yaml
# docker-compose.yml on the VM
services:
  relay:
    image: ghcr.io/clintcan/debswarm:latest
    restart: unless-stopped
    network_mode: host          # simplest way to accept inbound QUIC/TCP on 4001
    volumes:
      - ./relay.config.toml:/etc/debswarm/config.toml
      - debswarm-data:/var/lib/debswarm   # PERSIST the identity — stable peer ID
volumes:
  debswarm-data:
```

`network_mode: host` avoids Docker's port-mapping quirks for UDP/QUIC. If you prefer
bridge networking, publish **both** protocols: `-p 4001:4001/tcp -p 4001:4001/udp`.
The `debswarm-data` volume is what keeps the peer ID stable — without it, every
container recreate mints a new identity and invalidates peers' config.

## 7. Get the relay's multiaddr

```bash
# native
debswarm identity show
# container
docker compose exec relay debswarm identity show
```

Build the multiaddr from your **public IP** (DO droplet IP / AWS Elastic IP) or, better,
a DNS name, plus the peer ID:

```
/ip4/YOUR_PUBLIC_IP/udp/4001/quic-v1/p2p/YOUR_PEER_ID
/ip4/YOUR_PUBLIC_IP/tcp/4001/p2p/YOUR_PEER_ID
# or, recommended, a stable DNS name:
/dns4/relay1.example.com/udp/4001/quic-v1/p2p/YOUR_PEER_ID
```

## 8. Point peers at it

On each NAT'd peer, add the relay to **both** `relay_peers` (to reserve on) and
`bootstrap_peers` (to bootstrap the DHT):

```toml
[network]
relay_peers = [
  "/ip4/YOUR_PUBLIC_IP/udp/4001/quic-v1/p2p/YOUR_PEER_ID",
]
bootstrap_peers = [
  "/ip4/YOUR_PUBLIC_IP/udp/4001/quic-v1/p2p/YOUR_PEER_ID",
]
# A peer that knows it is NAT'd can assert this to reserve immediately instead of
# waiting for AutoNAT to reach a verdict (which a small swarm may never do):
force_reachability = "private"
```

For a **public** swarm, peers can also discover the relay through the DHT (it
advertises itself), so `relay_peers` is optional but speeds up the first reservation.
For a **private (PSK)** swarm, `relay_peers` is **required** — there is no public DHT
to discover through.

## 9. Verify it is relaying

On the relay, check the metrics (locally or over an SSH tunnel):

```bash
curl -s http://localhost:9978/metrics | grep -E 'relay_service_active|relay_circuits_active|relay_reservations'
```

- `debswarm_relay_service_active 1` — the relay service is running.
- `debswarm_relay_reservations{state="active"}` climbs as NAT'd peers reserve slots.
- `debswarm_relay_circuits_active` shows connections it is currently relaying.

On a peer, `debswarm_relay_reservations_obtained_total >= 1` confirms it got a usable
circuit address through your relay. The `test/nat/` topology exercises this whole path
end to end.

## Optional: carry data for symmetric-NAT'd peers

By default a relay **coordinates hole punches but never carries package bytes** —
circuit-v2's small per-circuit limits are a feature, and every byte is still
SHA256-verified regardless. That is enough whenever at least one peer's NAT is
hole-punchable.

When **both** peers are behind symmetric NATs, DCUtR cannot punch, and by default the
transfer falls back to the mirror. To let such a pair exchange **small** packages over
the relay instead (see [`docs/design/relay-data-fallback.md`](design/relay-data-fallback.md)):

- On the **relay**, raise the per-circuit data cap:
  ```toml
  [network.relay_limits]
  buffer_size = "1MB"   # default 128KB is coordination-only; raise to carry small pkgs
  ```
- On the **peers**, opt in with a size cap:
  ```toml
  [network]
  relayed_transfer_max_bytes = 262144   # 256 KiB; 0 (default) = disabled
  ```

The effective cap is `min(relayed_transfer_max_bytes, buffer_size)`. This trades
**bandwidth for reach, not safety**: the relayed bytes are still SHA256-verified
against the signed index (a relay cannot poison the swarm), and the relay carries
only the end-to-end-encrypted libp2p stream, so it is a blind pipe that can only drop
or delay, never read or forge.

**Cost / abuse caveat.** Now real package bytes flow through the relay, so its egress
becomes your bill. On a relay you run for **your own** (e.g. PSK) swarm this is just
your own traffic. On a **public** relay carrying data for strangers, keep
`buffer_size` conservative and watch the metrics — the `debswarm_bytes_from_relay_total`
counter on the peers and `debswarm_relay_circuits_active` on the relay tell you what
is actually being carried.

## Notes

- **Redundancy:** run two relays in different regions and list both in peers'
  `relay_peers`/`bootstrap_peers`. AutoRelay fails over between them.
- **DNS over IP:** prefer a `/dns4/…` multiaddr so you can replace the VM without
  reissuing every peer's config (keep the same identity key, though, so the peer ID
  is unchanged).
- **Firewall reminder:** if reservations never form, 90% of the time it is a missing
  **UDP** 4001 rule at the cloud firewall.
