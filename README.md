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

**How it propagates:** NostrVPN's network DNS override (from the
`network-dns-gui` branch, built at a pinned commit) carries the admin's nameserver choice on the signed
roster. The admin points the network at this container's device and enables
the override; every member then resolves through it — on any network,
anywhere — and **fails closed** if the resolver is unreachable rather than
leaking queries to the local network.

**Sovereignty chain:** device → WireGuard tunnel → Pi-hole (filtering) →
Unbound (recursive, straight from the root servers, DNSSEC-validated). No
Google, no Cloudflare, no Tailscale coordination server.

## Quickstart

Prereqs: Docker with the compose plugin and a Linux host (or anything with
`/dev/net/tun`). No pre-existing NostrVPN network needed — on first start
the container founds its own network with itself as admin and DNS node,
override already on.

```bash
git clone <this-repo> sovereign-dns && cd sovereign-dns
cp .env.example .env          # set PIHOLE_PASSWORD at minimum
docker compose up -d --build  # first build compiles nvpn + web panel (~10-15 min)
```

(Or, without git: run [`scripts/install.sh`](scripts/install.sh), which
writes the same stack and walks you through it.)

### 1. Add your devices

The container prints its npub and a ready-made invite on first start:

```bash
docker compose logs nvpn | grep -A8 'to add another device'
```

On each phone/laptop, run the printed `nvpn join-manual ...` command —
that queues a join request. Then approve it in the **control panel** at
`http://localhost:8082` on the docker host (or via
`docker exec sovereign-dns-nvpn nvpn add-device --device <joiner-npub> --publish`).
Approval is deliberately manual: the invite is plain text, so auto-accepting
anyone holding it would remove the only access control the network has.

### 2. Manage the mesh — browser, not terminal

The `web` service runs NostrVPN's own control panel (`nvpn-web`, the same
one the upstream Umbrel package ships) against the same config volume as
the daemon: approve/reject join requests, add or remove devices and
admins, rename networks, QR pairing codes. It has **no login of its own**,
so compose publishes it only to the host's loopback
(`127.0.0.1:${WEB_UI_PORT:-8082}`) — do not re-publish it to the LAN or
the mesh.

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
- nvpn is built from one pinned commit (`NVPN_COMMIT` in
  `docker-compose.yml`, currently the head of the `network-dns-gui` feature
  branch), and every image is pinned by digest. `scripts/check-pins.sh`
  fails if anything is unpinned, and `make-installer.sh` won't build an
  installer until it passes.
