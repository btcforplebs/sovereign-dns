# Before you install

Read this page first. It takes about five minutes, and it tells you what
you're getting, what you need, what the installer will do, and how to get
out again.

## What this gives you

Every time your phone or laptop opens a website, it first asks a DNS
server "where is example.com?". Normally your internet provider, Google
or Cloudflare answers, so they see every site you visit.

Sovereign DNS puts that job on a computer you control, called **the box**.
Once it's running:

- **Ads and trackers are blocked** on every device you add, inside apps
  too.
- **Your lookups don't go to your internet provider, Google or
  Cloudflare.** The box finds the answers itself, starting from the
  internet's root servers.
- **It works away from home.** Your devices reach the box through a
  private, encrypted network (Nostr VPN), on home Wi-Fi, mobile data or a
  hotel.

> **What's been tested:** the box on a Mac, with an iPhone and a Mac as
> devices. Android, Linux and Windows are built but not tested yet.
> **Phones need the right app build.** A phone uses the box only when it
> runs a Nostr VPN build with network DNS (the same code the box runs).
> The stock app-store app joins your network but still does its own
> lookups. After you add a phone, check that its lookups show up in
> Pi-hole ([Quickstart, Step 7](quickstart.md#step-7-see-the-ads-disappear)).

It is **not** a full VPN for your browsing. Only the lookups go to the
box. The websites themselves still see your device's normal IP address.
See [Your IP address: who sees what](your-ip.md) for the full picture.

## What you need

- **A computer that stays on.** If the box is off or asleep, the devices
  you've added can't open any website until it's back. That's on
  purpose (see [If DNS stops working](#if-dns-stops-working)). See
  [Where to run it](where-to-run.md) for the options and what hardware is
  enough.
- **About 10 minutes**, most of it waiting. That's with the ready-made
  downloads, which most Macs, Linux PCs and 64-bit Raspberry Pis get. If
  it has to build everything itself, allow 20 to 40 minutes on a
  Raspberry Pi.
- **Docker**, the free program that runs the box's software. On Linux
  (Debian, Ubuntu, Linux Mint, Raspberry Pi OS) the installer offers to
  install it for you and asks first. On a Mac or Windows PC, install
  Docker Desktop and open it once before you start
  ([how to get Docker](https://docs.docker.com/get-docker/)).
- **The Nostr VPN app** on each phone or laptop you want to protect. On a
  phone, it has to be a build with network DNS (see the note above). After you add a device, check that its lookups really
  reach the box (Step 7 of the [Quickstart](quickstart.md)).
- **No changes to your router.** You don't open or forward any ports.
  See [No open ports needed](#no-open-ports-needed).

## What will happen, step by step

1. **You paste one line** into a terminal on the box. It downloads one
   file, `install.sh`, and runs it.
2. **It checks Docker** is installed and running (on Linux, if it is
   missing, it asks whether to install it), and that the
   addresses it needs on this computer (8080, 8082) are free.
3. **It unpacks a `sovereign-dns` folder** in the folder you ran it from.
   Running it again later refreshes that folder and keeps your password,
   settings and devices.
4. **It asks you for a password** for the ad-block dashboard (Pi-hole).
5. **On a box with no screen** (a Raspberry Pi over SSH), it asks whether
   to open the dashboard to your home network, behind a second password.
6. **It downloads the ready-made software**, or builds it if it can't.
   Then it starts a handful of small programs: the VPN, the VPN's
   control panel, the ad blocker (Pi-hole), the lookup engine (Unbound),
   the dashboard and the built-in guide.
7. **The box starts its own private network** with itself in charge, and
   tells every device that joins to send its lookups to the box.
8. **It prints "Done"** and the dashboard address, `http://localhost:8080`.
9. **The dashboard's setup asks "First box or another one?"** Pick *This
   is my first box*, unless you already run a box and this one should
   join it as a backup.
10. **You add your devices** one at a time (*Add your next device*).
    Nothing joins without you pressing **Add**.

The box starts again by itself whenever the computer restarts, as long
as Docker starts with it. Docker Desktop on a Mac or Windows PC has a
setting for this.

## No open ports needed

You don't touch your router. The box only connects **out** to the
internet, and your devices find it through the private network:

- Each side learns its own public address from a few public helper
  servers, then the two try to connect to each other directly. This is
  called hole punching.
- A few public relay nodes help your devices and the box find each other
  and set up that direct link. If a direct link can't form, they carry
  the encrypted traffic instead. They pass it along but can't read it.

A home box set up this way has been reached from a phone on 5G with
nothing forwarded on the router.

On a VPS, the box's VPN port (UDP 51820) is reachable from the internet.
It's an encrypted listener that only accepts authenticated peers. It is
not an admin page.

## How to undo it

The commands below assume you ran the installer in your home folder, so
the box lives in `~/sovereign-dns`. If you ran it somewhere else, use that
folder instead.

Do these in order. Steps 1 and 2 matter: if you remove the box while your
devices are still told to use it, they can't open websites.

1. **If you turned on whole-house DNS** (`dns-lan.sh`), change your
   router's DNS setting back first, then run
   `bash scripts/dns-lan.sh off` in the `sovereign-dns` folder.
   Otherwise everyone on your Wi-Fi loses DNS when the box goes.
2. **Turn network DNS off.** On the dashboard (`http://localhost:8080`),
   use the **Network DNS** switch and turn it off. Your devices go back
   to their normal DNS within a few seconds.
3. **Stop the box.** In a terminal on the box:

   ```bash
   cd ~/sovereign-dns && docker compose down
   ```

   To also delete its data (your private network's identity, the device
   list and your Pi-hole settings) so it can't be started again as it
   was, use `docker compose down -v` instead. That can't be undone.
4. **Delete the `sovereign-dns` folder** and `install.sh` if you want
   them gone.
5. **On each phone or laptop,** turn off or remove the Nostr VPN app,
   or leave the network in it.

## If DNS stops working

The commands here also assume the box lives in `~/sovereign-dns`.

If your devices suddenly can't open any website, the box is most likely
off or asleep. This is deliberate. When the box can't be reached, your
devices stop looking up sites instead of quietly going back to your
internet provider, so your lookups are never handed to someone else
without you noticing.

- **Need a device back online right now?** Turn off Nostr VPN on that
  device. It goes back to normal DNS straight away.
- **Need everyone back online while you fix it?** If the box is still
  running, turn the dashboard's **Network DNS** switch off. If you use
  whole-house DNS, set your router's DNS back to automatic.
- **Run a second box** so this doesn't happen when one is off or asleep
  ([Quickstart, A second DNS box](quickstart.md#a-second-dns-box)).
- **Wake or turn on the box,** then wait a minute.
- **Still broken?** On the box, run:

  ```bash
  cd ~/sovereign-dns && docker compose up -d
  ```

  then, after a minute, the health check:

  ```bash
  bash ~/sovereign-dns/scripts/healthcheck.sh
  ```

  It marks each part PASS or FAIL and says how to fix each failure. Red
  right after a start is normal. Give it another minute. If it says
  **"Pi-hole is orphaned"**, run the command it prints.

More fixes are in the [Quickstart's troubleshooting
section](quickstart.md#if-something-goes-wrong).

## Next

- [Where to run it](where-to-run.md): your own always-on computer, your
  laptop or a rented server (VPS), with hardware and prices.
- [Your IP address: who sees what](your-ip.md)
- [Quickstart](quickstart.md): the install, step by step.
