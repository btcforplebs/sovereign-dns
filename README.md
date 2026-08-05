# Sovereign DNS

Self-hosted, ad-filtered, DNSSEC-validated DNS for **every device on your
NostrVPN mesh** — one `docker compose up`, one admin command, zero per-device
configuration, zero third-party control plane.

**New to this project?** `docker compose up -d` also stands up a
self-hosted docs site at `http://localhost:8081` (plain-language overview,
step-by-step quickstart, per-layer technical detail, and a
troubleshooting/gotchas page) — see [`docs/`](docs/).

```
┌────────────────────────── docker compose ──────────────────────────┐
│                                                                    │
│  ┌───────────────┐  shares netns  ┌─────────┐        ┌─────────┐   │
│  │  nvpn daemon  │◄──────────────►│ Pi-hole │───────►│ Unbound │───┼──► root
│  │ (mesh member, │   utun100:53   │ filter  │ 172.28 │recursive│   │   servers
│  │  DNS device)  │                │ + stats │ .53.11 │ DNSSEC  │   │
│  └───────┬───────┘                └─────────┘        └─────────┘   │
└──────────┼─────────────────────────────────────────────────────────┘
           │ WireGuard mesh (NostrVPN)
   ┌───────┴────────┬────────────────┐
 laptop           phone          other nodes      ← all resolve through Pi-hole,
                                                    enforced by the signed roster
```

**How it propagates:** NostrVPN's network DNS override (branch
`network-dns-gui`) carries the admin's nameserver choice on the signed
roster. The admin points the network at this container's device and enables
the override; every member then resolves through it — on any network,
anywhere — and **fails closed** if the resolver is unreachable rather than
leaking queries to the local network.

**Sovereignty chain:** device → WireGuard tunnel → Pi-hole (filtering) →
Unbound (recursive, straight from the root servers, DNSSEC-validated). No
Google, no Cloudflare, no Tailscale coordination server.

## Quickstart

Prereqs: Docker with the compose plugin, a Linux host (or anything with
`/dev/net/tun`), and an existing NostrVPN network you administer.

```bash
git clone <this-repo> sovereign-dns && cd sovereign-dns
cp .env.example .env          # set PIHOLE_PASSWORD at minimum
docker compose up -d --build  # first build compiles nvpn from source (~10 min)
```

### 1. Join this container to your mesh

The container prints its npub on first start:

```bash
docker compose logs nvpn | grep npub:
```

Configure its network membership exactly as you would any other NostrVPN
device (network id, participants, endpoints — or approve its join request
from your admin device):

```bash
docker compose exec nvpn nvpn set --network-id <your-network> ...
docker compose exec nvpn nvpn reload
```

Confirm it is meshed:

```bash
docker compose exec nvpn nvpn status
docker compose exec nvpn nvpn ip        # its mesh tunnel IP
```

### 2. Point the network at it (from your admin device)

```bash
nvpn set --network-dns-node <container-npub> --network-dns-override
nvpn reload
```

That's the whole rollout. The setting rides the signed roster to every
member. Check any device with `nvpn status` — you'll see:

```
network_dns: <name> (<mesh-ip>) override=on reachable
```

### 3. Manage Pi-hole

The admin UI is served on the mesh at `http://<container-mesh-ip>/admin`
(password from `.env`). Blocklists, per-client rules, and query stats all
work as usual — for your entire device fleet.

## Design notes

- **Pi-hole shares the nvpn container's network namespace** and binds *only*
  the mesh tunnel interface (`FTLCONF_dns_listeningMode=BIND` on `utun100`).
  nvpn's own DNS stubs live on 127.0.0.1, so the two never collide — the same
  pattern nostr-vpn's own network-DNS e2e uses.
- **The DNS device exempts itself** from the override (nvpn does this in
  core), so this container keeps its own resolver and no forwarding loop is
  possible.
- **Unbound is internal-only.** Nothing is published to the host; only
  Pi-hole can reach it. No port 53 is exposed to the internet at all —
  clients arrive over WireGuard.
- **Fail-closed is a feature.** If this stack goes down, member devices'
  lookups fail rather than silently falling back to the coffee-shop DNS.
  `nvpn status` on any member shows `UNREACHABLE, DNS fails closed` so the
  cause is visible at a glance. Turn the override off from the admin device
  to restore local DNS instantly.

## Honest caveats

- The override is **policy, not containment**: a device's owner can edit
  their local nvpn config to opt out. It's a sovereignty tool — every device
  answers to its own owner.
- One instance is one point of failure for the whole fleet's DNS (fail-closed
  by design). Run it on something reliable; a second instance + selecting it
  as the fallback admin action is the simple recovery path today.
- `network-dns-gui` is a feature branch. Pin `NVPN_REF` to a tagged release
  once it merges.
