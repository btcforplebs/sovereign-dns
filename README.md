# Sovereign DNS

**Your own private, ad-blocking internet "phone book" for all your devices, at home and on the go.**

Every time your phone or laptop opens a website, it first asks a DNS server
"where is this site?". Normally that question goes to your internet provider,
Google or Cloudflare, and they see every site you visit.

Sovereign DNS moves that job onto a computer you control, called **the box**.
Once it's running:

- **Ads and trackers are blocked** on every device you add, inside apps too.
- **Your lookups skip your internet provider, Google and Cloudflare.** The
  box looks sites up itself, starting from the internet's root servers.
- **It follows you.** Your phone and laptop reach the box through a private,
  encrypted network (Nostr VPN), so it works on café Wi-Fi and mobile data
  too. You don't change anything on your router.

It is not a full VPN: websites still see your device's own IP address. See
[who sees what](docs/src/your-ip.md).

> **What's been tested:** the box on a Mac, with an iPhone and a Mac as
> devices. Android, Linux and Windows are built but not tested yet.
> **Phones need the right app build.** A phone uses the box only when it
> runs a Nostr VPN build with network DNS (the same code the box runs).
> The stock app-store app joins your network but still does its own
> lookups. After you add a phone, check that its lookups show up in
> Pi-hole ([Quickstart, Step 7](docs/src/quickstart.md#step-7-see-the-ads-disappear)).

## Read this before you install

**[Before you install](docs/src/before-you-install.md)** is a five-minute
read. In short:

- **What you need:** a computer that stays on, about 10 minutes, Docker, and
  the Nostr VPN app on each phone or laptop.
- **If the box is off or asleep, your devices can't open websites** until
  it's back. That's on purpose: it never quietly hands your lookups to
  someone else. Turning off Nostr VPN on a device gets that device back
  online straight away.
- **No open ports.** The box connects out, and your devices find it through
  the private network.
- **Undoing it** is one line too, and it goes in the safe order: network
  DNS off first, so your devices aren't left without DNS. If you use
  whole-house DNS, change your router back before you run it. See
  [How to undo it](#how-to-undo-it).

## Where to run it

| | At home, always-on computer | At home, on your laptop | On a VPS (rented server) |
|---|---|---|---|
| Examples | Raspberry Pi 4/5, Mac mini, old PC | The laptop you use | DigitalOcean, Akamai/Linode, Vultr, OVHcloud |
| Works when you're away | Yes | Only while the laptop is awake | Yes |
| Outside world sees | Your home IP | Your home IP | The VPS's IP |
| Cost | Hardware once | Nothing | About $5 to $12 a month |

**A home computer that stays on is the recommended setup.** A laptop works
for trying it out, but when it sleeps every device you've added stops
opening websites. You can also run **two boxes** (say a Raspberry Pi and a
Mac), so DNS keeps working while one is off or asleep. See
[A second box](#a-second-box). Hardware minimums, Raspberry Pi storage advice (use a 64 GB
card, not 32 GB) and prices checked on 2026-10-15 are in
**[Where to run it](docs/src/where-to-run.md)**.

## Install

Install [Docker](https://docs.docker.com/get-docker/) first and open it once.
Then open a terminal on the box and paste this one line:

```bash
rm -f install.sh && curl -fsSLO https://raw.githubusercontent.com/btcforplebs/sovereign-dns/main/scripts/install.sh && bash install.sh
```

The `rm -f` clears out an `install.sh` left from an earlier install (even
one that `sudo` made), so the fresh copy can always be saved. Paste the same
line again later to update the box.

It will ask you to pick a password (for the ad-blocking dashboard), then set
everything up. When a ready-made copy of the encrypted network software exists
for your box (most Macs, Linux PCs and 64-bit Raspberry Pis), it downloads that
and the first install takes a few minutes. Otherwise it builds the software
itself, which takes a while: about 10–15 minutes on a normal computer, 20–40
minutes on a Raspberry Pi. Either way, let it run.

To build from source even when a ready-made copy exists, run
`SOVEREIGN_DNS_BUILD_FROM_SOURCE=1 bash install.sh` instead.

When it says **Done**, open a web browser **on that same box** and go to:

**http://localhost:8080**

> The dashboard only opens on the box itself, not from another computer.
> That's on purpose: it keeps strangers on your network out of it.

The full step-by-step walk-through is the
[Quickstart](docs/src/quickstart.md).

### A box with no screen (like a Raspberry Pi)

If you installed over SSH, or the box has no desktop, the installer asks:

```text
Open the dashboard to your home network? [Y/n]
```

Say yes and pick a dashboard password (at least 8 characters). When it's done
it prints the address to open from your laptop or phone, on the same Wi-Fi:

```text
From another computer on your home network, open:
    http://<box-address>:8080
    http://<box-name>.local:8080   (if your computer finds it by name)
and sign in with the dashboard password.
```

(The installer shows the real address.) Every page asks for that password, because the
dashboard can add devices to your network. On Linux (a Raspberry Pi, a Linux
server) it also refuses any request from outside your home network, even with
the password. On Docker Desktop (a Mac or Windows box) it can't tell where a
request came from, so the password is the only guard there. Pick a long one,
and never forward the dashboard's port on your router.

On a VPS, don't open the dashboard to the network. Reach it through SSH
instead: see [Where to run it](docs/src/where-to-run.md#on-a-vps).

To change your mind later, run one of these inside the `sovereign-dns` folder:

```bash
bash scripts/hub-lan.sh on    # open it to your home network (asks for a password)
bash scripts/hub-lan.sh off   # back to this box only, no password
bash scripts/hub-lan.sh url   # print the addresses again
```

From another computer you can add devices by their join link, and turn
network DNS on or off. The VPN control panel still only opens on the box
itself, since it has no password of its own. Two things to know: the
password crosses your home network unencrypted (plain `http`, like the
Pi-hole page), and anyone who knows it can add a device.

## First box or another one?

Once the dashboard is up, the installer asks this right in the terminal, so
a box with no screen is set up without opening anything:

- **1) This is my first box:** it runs your private network. Your phones,
  laptops and any other DNS box join it. Paste each new device's join link
  (below), check the device it names and type `y`; press Enter when you're
  done. If the dashboard is open to your home network, it also shows a QR
  code: open it with your phone's camera and add the phone from the phone
  itself.
- **2) I already have a box:** this one joins your first box's network as a
  second DNS box. Paste the first box's join code; it prints this box's ID to
  paste back there. See [A second box](#a-second-box).

Press Enter to skip it and use the dashboard's setup instead, which asks the
same question. Run it again any time from the `sovereign-dns` folder with
`docker compose exec hub /hub setup`. A scripted install (no terminal, or
`SOVEREIGN_DNS_SKIP_SETUP=1`) skips it, and running the installer again to
update a box that's already set up asks nothing.

## Add your next device

On the dashboard (or step 3 of the setup, *Add your next device*), pick
**A phone or computer**, then:

1. **On your phone:** open the Nostr VPN app → *Join Network* → *Share*, and
   send the link to the box (AirDrop works). No AirDrop? Tap *Copy Request*
   and get it to the box another way.
   **On a computer:** run `nvpn join-request --no-qr` and copy the link.
2. **On the box:** paste the link into *Add a device*.
3. **Check the device name and press Add.** A few seconds later, that device
   is protected.

Treat that link like a one-time password: don't post it in a group chat.
Nothing gets added until you press Add.

Then check it really works: on that device, open Pi-hole and look for its
lookups in the log ([Quickstart, Step 7](docs/src/quickstart.md#step-7-see-the-ads-disappear)).

## A second box

A second box keeps your network answering when the first one is off,
asleep or being updated. Install it the same way, and when its setup asks
**First box or another one?**, pick **I already have a box**. Then two
pastes, one each way:

1. On the **first** box, under **Add another DNS box**, copy the **join
   code** and paste it into the new box's setup. Press **Join**. The code
   isn't a password: it only tells the new box which network to ask for.
2. The new box then shows its **ID** (`npub1…`). Paste that into the first
   box, under the join code, and press **Add this box**. Nothing joins
   until you do.

Only pick *I already have a box* on a box no device uses yet. It leaves the
network it made for itself, and setup refuses if devices already use it.
Details: [Quickstart, A second DNS box](docs/src/quickstart.md#a-second-dns-box).

## Turn network DNS on or off

The dashboard has a **Network DNS** switch. **On** (the default) means
every device you've added sends its lookups to the box. **Off** means they
go back to their normal DNS, within a few seconds. Turn it off before you
take the box away for good, or whenever you need everyone back online
while the box is broken. The switch asks before it turns DNS off, and
before it turns DNS on while Pi-hole or Unbound is down.

## Your dashboard from your phone, anywhere

Any device on your network (your phone on mobile data, say) can open the
dashboard and Pi-hole through Nostr VPN. Nothing is opened to the internet
or to your home Wi-Fi. In the `sovereign-dns` folder on the box, run:

```bash
bash scripts/hub-mesh.sh on    # asks for a dashboard password if you have none yet
bash scripts/hub-mesh.sh url   # print the addresses again
bash scripts/hub-mesh.sh off   # close it again
```

It prints the address, `http://<the box's mesh IP>:8080`. Open it on your
phone with Nostr VPN connected, and sign in with the dashboard password.
Pi-hole is at `http://<the box's mesh IP>/admin`, behind the Pi-hole
password.

## DNS for the whole house (no app needed)

Your router can hand the box out as DNS to everything on your home Wi-Fi:
TVs, guests, anything that can't run Nostr VPN. Off by default. In the
`sovereign-dns` folder on the box, run:

```bash
bash scripts/dns-lan.sh on      # start, test a lookup, then print the router steps
bash scripts/dns-lan.sh status  # is it on, and does a lookup through it work?
bash scripts/dns-lan.sh off     # stop (change your router back FIRST)
```

- **Use a Linux box for this, like a Raspberry Pi.** On a Mac or Windows
  box (Docker Desktop) the box can't tell your home network from the
  internet, and Pi-hole sees the whole house as one device. So the script
  refuses there.
- **It prints the router steps.** Reserve the box's address in the router,
  then put that address in the router's DNS setting. For a second DNS,
  use your other box's home address or leave it empty. Never use Google
  or Cloudflare there: devices quietly use them and skip the blocking.
- **If this box is off, nobody in the house can load websites.** Keep it
  on.
- **Never forward port 53 on your router.**
- **Turning it off:** change your router's DNS back **first**, then run
  `dns-lan.sh off`. Otherwise the house loses DNS straight away.

## Everyday use

- **See what's being blocked:** open the dashboard and click the ad-blocker
  (Pi-hole). You'll see every blocked ad and tracker, per device.
- **A site is broken?** In Pi-hole, add it to the allow list.
- **Read the guide:** a plain-language guide is built in at
  **http://localhost:8081** on the box.

## If DNS stops working

- **The box is probably off or asleep.** Wake it and wait a minute.
- **Need a device online right now?** Turn off Nostr VPN on that device.
- **Still broken?** On the box, inside the `sovereign-dns` folder (it's in
  the folder you ran the installer from, usually your home folder), run
  `docker compose up -d`, wait a minute, then
  `bash scripts/healthcheck.sh`. It tells you what's down and how to fix it.

More in [Before you install](docs/src/before-you-install.md#if-dns-stops-working).

## How to undo it

If you use whole-house DNS, change your router's DNS setting back first.
Then, in a terminal on the box, in the folder you ran the installer from
(usually your home folder), paste this one line:

```bash
rm -f uninstall.sh && curl -fsSLO https://raw.githubusercontent.com/btcforplebs/sovereign-dns/main/scripts/uninstall.sh && bash uninstall.sh
```

It says what it will do and asks once before changing anything. In order:

1. Turns **network DNS off** when this box answers DNS for your devices and
   no backup box will, so they go back to their normal DNS.
2. Turns **whole-house DNS off**, if it was on.
3. Prints this box's **device ID**. On a second box, remove that device on
   your first box's dashboard. On your first box, the network goes with it:
   leave it in the Nostr VPN app on each phone or laptop.
4. Stops the box and deletes its data and images
   (`docker compose down -v --rmi all`). That can't be undone.
5. Deletes the `sovereign-dns` folder.

`bash uninstall.sh --yes` does the same without asking. Docker stays
installed. The step-by-step version by hand is in
[Before you install](docs/src/before-you-install.md#how-to-undo-it).

## Good to know

- **Each device's owner is in charge of that device.** Someone can turn this
  off on their own phone. It protects people who want it; it doesn't police them.
- **Running the installer again is safe.** It refreshes the box's files and
  keeps your password, settings and devices.

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

To use the ready-made images instead of building every service (when
`docker-compose.prebuilt.yml` exists), add this line to `.env` before
`docker compose up -d --build`. Every later `docker compose` command in the
folder then uses the same images:

```bash
COMPOSE_FILE=docker-compose.yml:docker-compose.prebuilt.yml
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
  the mesh tunnel interface (`utun100`) and nvpn's dns-internal address, one
  address at a time (dnsmasq `bind-dynamic`, with `FTLCONF_dns_listeningMode`
  set to `NONE`). nvpn's own DNS stubs live on 127.0.0.1, so the two never
  collide, and Pi-hole follows the mesh address when it changes, as it does
  when a second box joins the first one.
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
- The ready-made images (`docker-compose.prebuilt.yml`) are built by this
  repo's CI from the same folders an install builds (`nvpn/` at that same
  commit, `unbound/`, `hub/`, `docs/`), for amd64 and arm64, and pinned by
  digest. Each tag names a hash of its folder (and the commit, for nvpn), and
  `check-pins.sh` fails if one changes without a repin, so none can quietly
  fall behind the source. Trusting it means trusting this
  repo's GitHub Actions build. If you would rather not, build from source
  (`SOVEREIGN_DNS_BUILD_FROM_SOURCE=1`).
