# Sovereign DNS

**Your own private, ad-blocking internet "phone book" for all your devices — at home and on the go.**

Every time your phone or laptop opens a website, it first asks a DNS server
"where is this site?". Normally that question goes to your internet provider,
Google, or Cloudflare, and they see every site you visit.

Sovereign DNS moves that job onto a small computer you own (a Raspberry Pi
works great). Once it's running:

- **Ads and trackers are blocked** on every device, inside apps too.
- **Nobody else sees your lookups.** Your box asks the internet's root servers
  directly, with no Google or Cloudflare in the middle.
- **It follows you.** Your phone and laptop connect back to your box through a
  private, encrypted link, so you're protected on café or hotel Wi-Fi too.

## What you need

- **A computer that stays on.** A Raspberry Pi 4 or 5 with 2 GB of memory or
  more is ideal. Any Linux machine works. A Mac or Windows PC works too, but
  needs about 4 GB of free memory.
- **Docker** installed and running. ([How to get Docker](https://docs.docker.com/get-docker/))
- **About 8 GB of free disk space.**
- **The Nostr VPN app** on each phone or laptop you want to protect.

## Install

Open a Terminal on the box and paste this one line:

```bash
curl -fsSLO https://raw.githubusercontent.com/btcforplebs/sovereign-dns/main/scripts/install.sh && bash install.sh
```

It will ask you to pick a password (for the ad-blocking dashboard), then build
everything. **The first install takes a while:** about 10–15 minutes on a
normal computer, 20–40 minutes on a Raspberry Pi. Let it run.

When it says **Done**, open a web browser **on that same box** and go to:

**http://localhost:8080**

> The dashboard only opens on the box itself, not from another computer.
> That's on purpose: it keeps strangers on your network out of it.

## Add your phone or laptop

On the dashboard, click **Add a device**, then:

1. **On your phone:** open the Nostr VPN app → *Join Network* → *Share*, and
   send the link to the box (AirDrop works). No AirDrop? Tap *Copy Request*
   and get it to the box another way.
   **On a computer:** run `nvpn join-request --no-qr` and copy the link.
2. **On the box:** paste the link into *Add a device*.
3. **Check the device name and press Add.** A few seconds later, that device
   is protected.

Treat that link like a one-time password: don't post it in a group chat.
Nothing gets added until you press Add.

## Everyday use

- **See what's being blocked:** open the dashboard and click the ad-blocker
  (Pi-hole). You'll see every blocked ad and tracker, per device.
- **A site is broken?** In Pi-hole, add it to the allow list.
- **Read the guide:** a plain-language guide is built in at
  **http://localhost:8081** on the box.

## Good to know

- **If the box is off, your devices can't look up websites.** That's
  deliberate: rather than quietly falling back to someone else's DNS, it stops.
  Keep the box plugged in, or turn off the VPN on your phone until
  it's back.
- **Each device's owner is in charge of that device.** Someone can turn this
  off on their own phone. It protects people who want it; it doesn't police them.
- **Something not working?** Run `bash sovereign-dns/scripts/healthcheck.sh`
  on the box and it will tell you what's down.

---

## For the technically curious

### How it fits together

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

### What's inside

- **Nostr VPN** — the private, encrypted link between your devices and the box.
- **Pi-hole** — blocks ads and trackers, and shows you stats.
- **Unbound** — looks sites up straight from the internet's root servers and
  checks they haven't been tampered with (DNSSEC).

### Manual install (with git)

Prereqs: Docker with the compose plugin and a Linux host (or anything with
`/dev/net/tun`). No pre-existing NostrVPN network needed — on first start
the container founds its own network with itself as admin and DNS node,
override already on.

```bash
git clone https://github.com/btcforplebs/sovereign-dns.git && cd sovereign-dns
cp .env.example .env          # set PIHOLE_PASSWORD at minimum
docker compose up -d --build  # first build compiles nvpn + web panel
```

The container prints its npub and a ready-made invite on first start:
`docker compose logs nvpn | grep -A8 'to add another device'`. The old
terminal path for adding a device still works: on the device, run the
`nvpn join-manual ...` command from `docker compose logs nvpn`, then on the
box run
`docker exec sovereign-dns-nvpn nvpn add-device --device <joiner-npub> --publish`.

Approval is deliberately manual: the join link is plain text, so
auto-accepting anyone holding it would remove the only access control the
network has. Pressing Add is that approval.

### The VPN control panel

The `web` service runs NostrVPN's own control panel (`nvpn-web`, the same
one the upstream Umbrel package ships) against the same config volume as
the daemon: approve/reject join requests, add or remove devices and
admins, rename networks. It has **no login of its own**,
so compose publishes it only to the host's loopback
(`127.0.0.1:${WEB_UI_PORT:-8082}`) — do not re-publish it to the LAN or
the mesh.

### Pi-hole on the mesh

The Pi-hole admin UI is also served on the mesh at
`http://<container-mesh-ip>/admin` (password from `.env`).

### Design notes

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

### Honest caveats

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
