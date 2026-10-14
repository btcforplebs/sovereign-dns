# Quickstart

This page takes you from nothing to a home where ads and trackers are
blocked on every device you own. You don't need to have used a terminal
before. Set aside about **30 minutes**. Most of that is waiting while the
computer builds the software.

## What you're setting up

You'll turn one computer in your home into your own **DNS box**.

Every time your phone or laptop opens a website, it first asks "where is
example.com?" Normally your internet provider, Google or Cloudflare
answers that question, and so they see every site you visit. Your box
answers it instead. On the way it drops the lookups for ads and trackers,
so they never load.

It follows your devices wherever they are: home Wi-Fi, mobile data, a
hotel. They reach the box through a private, encrypted network
(NostrVPN). The dashboard calls this network your **mesh**. You don't
have to change anything on your router.

> **Today this covers computers.** iPhones and iPads join the network
> but don't send their lookups through the box yet. Phone support is
> being worked on (see [Step 6](#step-6-add-your-first-device)).

## Before you start

- **A computer that stays on.** A Mac, a Linux machine or a small home
  server. If the box is off, the devices you've added can't look up any
  website until it's back (see [If something goes wrong](#if-something-goes-wrong)).
  On a Mac, turn off sleep in **System Settings → Energy** (on a laptop,
  **Battery → Options**).
- **An internet connection.** The first run downloads and builds all of
  the software.
- **Docker**, the free program that runs the box's software.
  *Placeholder: the installer will soon set Docker up for you. Until
  then, install [Docker Desktop](https://docs.docker.com/get-docker/)
  first and open it once, so it's running.*
- **Windows:** *placeholder, the Windows steps are still being decided.*

<!-- MAINTAINERS: when install.sh sources scripts/lib/ (installer piece 1:
     installs Docker, turns on autostart), replace the Docker bullet with
     "The administrator password for that computer; the installer may ask
     for it while it sets up Docker", and drop the Docker entries under
     "If something goes wrong". -->

## Step 1: Open a terminal

A terminal is a window where you type or paste commands.

- **Mac:** press <kbd>⌘ Command</kbd> + <kbd>Space</kbd>, type
  `Terminal` and press <kbd>Return</kbd>.
- **Linux:** press <kbd>Ctrl</kbd> + <kbd>Alt</kbd> + <kbd>T</kbd>, or
  open **Terminal** from your apps.

A window opens with a blinking cursor. That's all you need.

## Step 2: Paste the one line

Copy this line, paste it into the terminal and press <kbd>Return</kbd>:

```bash
curl -fsSLO https://INSTALLER-URL/install.sh && bash install.sh
```

<!-- MAINTAINERS: no public URL yet (Logen, 2026-10-11: local for now).
     Replace INSTALLER-URL when the release host exists. Keep the
     download-then-run form: install.sh reads its own file to unpack the
     stack, so `curl ... | bash` cannot work. -->

> **INSTALLER-URL is a placeholder.** There's no public download address
> yet. Until there is, whoever set this up for you will give you the
> `install.sh` file. Move it into your home folder (on a Mac: in Finder,
> choose **Go → Home** and drag the file there), then paste
> `bash install.sh` instead.

The line does two things: it downloads one file, `install.sh`, into your
home folder, and then runs it.

### Optional: check the file before you run it

The installer is a single file, and all of the box's setup files are
packed inside it, so you can check it before it touches anything. (While
it builds, it also downloads the building blocks, such as Pi-hole, from
their official sources.) To check it, run the two halves of the line one
at a time:

1. Download only. This fetches the installer and its fingerprint file,
   `install.sh.sha256`, which is published next to it:

   ```bash
   curl -fsSLO https://INSTALLER-URL/install.sh
   curl -fsSLO https://INSTALLER-URL/install.sh.sha256
   ```

2. Check the file against its fingerprint:

   ```bash
   shasum -a 256 -c install.sh.sha256
   ```

   (On Linux, if `shasum` isn't found, use `sha256sum -c install.sh.sha256`.)

   It should print `install.sh: OK`. If it prints `FAILED`, stop. Don't
   run the file.

   The fingerprint catches a download that was damaged or swapped on the
   way to you. It can't catch a swap on the download site itself, because
   both files come from there. That needs a signature, and releases are
   not signed yet.

3. To read every file before anything starts, unpack without running.
   This writes the files to a `sovereign-dns` folder and stops before
   building anything. Docker must already be running, and this step
   asks for your password (see [Step 3](#step-3-choose-a-password)):

   ```bash
   SOVEREIGN_DNS_EXTRACT_ONLY=1 bash install.sh
   ```

4. When you're happy with it, run it for real. It won't ask for the
   password again:

   ```bash
   bash install.sh
   ```

## Step 3: Choose a password

The installer asks you for one thing:

```text
Pick a password for your Pi-hole admin dashboard (typing is hidden):
```

Type a password and press <kbd>Return</kbd>. **Nothing appears on screen
while you type.** No dots, no stars. That's normal. Write the password
down: you'll use it to open the ad-blocking dashboard later.

The installer only asks this the first time. If you run it again later,
it keeps your password and settings.

## Step 4: Wait while it builds (10 to 15 minutes)

Then you'll see:

```text
Building and starting (the first run compiles from source and can take 10-15 minutes; later runs are fast)...
```

and a lot of scrolling text. **That's normal, and you don't need to read
it.** The computer is downloading the parts and building the encrypted
network software from its source code. Leave the window open and let it
finish. Later runs take seconds, because the build is kept.

When it's done, you'll see a box like this:

```text
==========================================
Done! Open your dashboard:

    http://localhost:8080

Everything lives there: what's running, this device's ID, the invite
command for adding your phone or laptop, the VPN control panel, the
Pi-hole ad-block dashboard, and the docs.
==========================================
```

If it says **"Started. The dashboard may need another minute"** instead,
wait a minute and carry on. Nothing is wrong.

Everything the installer wrote lives in a folder called `sovereign-dns`
in your home folder. Leave it there.

## Step 5: Open your dashboard

Open a web browser **on the same computer** and go to:

```text
http://localhost:8080
```

This is your dashboard. It only opens on the box itself, never from
another device, so nobody else on your network can reach it.

<!-- MAINTAINERS: installer piece 2 turns the hub into a step-by-step
     wizard (parts running, add a device, approve it, check it uses the
     Pi-hole). When it lands, match Steps 5 to 7 to its wording. Tile
     names below are hub/main.go on main @ 7c3adeb. -->

At the top, **Status** shows whether each part of the box is running:

| Tile | What it is |
|---|---|
| **VPN daemon** | The encrypted network your devices use to reach the box from anywhere. |
| **Control panel** | Where you approve and manage devices (Step 6). |
| **Pi-hole** | Blocks ad and tracker lookups, and shows you what was blocked. |
| **Unbound** | Answers lookups itself, never asking Google or Cloudflare. |
| **Docs** | This guide, served from your own box. |

All of them should turn green within a couple of minutes of the build
finishing. The page doesn't update by itself, so reload it to see the
latest. If a tile stays red, see
[If something goes wrong](#if-something-goes-wrong).

## Step 6: Add your first device

Adding a device takes three steps. You do the first on the new device,
and the other two on the box.

1. **Ask to join.** Under *Bring another device onto the mesh*, the
   dashboard shows an invite: a command to run on the device you want to
   add (see below for each kind of device). This only *asks* to join.
   The device isn't on your network yet.
2. **Approve it.** Back on the box, the dashboard shows
   *"1 device(s) waiting to join — approve"*. Click it. That opens the
   **Control Panel** (`http://localhost:8082`). Accept the request, but
   only if you recognise the device.

   Approval is never automatic, on purpose. The invite is plain text that
   could be forwarded to anyone, so approving each device by hand is what
   keeps strangers off your network.
3. **Check it's connected.** Reload the dashboard. The device appears
   under *Devices on this network*. If it says **"not covered by this
   resolver"**, it's approved but can't reach the box yet; give it a
   minute and reload.

### Getting the invite onto each kind of device

> **Placeholder: these steps are still being decided.** How each kind of
> device joins (which app to install, and whether you scan a QR code or
> paste the invite) is being tested on real phones and computers right
> now. This section will give exact steps for each one once that's
> settled.
>
> - **iPhone and iPad:** *placeholder*
> - **Android:** *placeholder*
> - **Mac:** *placeholder*
> - **Windows:** *placeholder*
> - **Linux:** *placeholder*

## Step 7: See the ads disappear

On the device you just added, browse a few websites you normally use.

Then open Pi-hole **on that same device**. On the box's dashboard, the
**Pi-hole** link shows its address: `http://`, then a set of numbers,
then `/admin`. Write it down and type it into a browser on the device
you added. Pi-hole lives on your private network, so it opens from the
devices you've added, not from the box itself. Sign in with the password
from Step 3.

You'll see your device's lookups counting up, and some of them marked
**blocked**. Those are the ads and trackers that never loaded. You're
done.

From here, add each of your other devices the same way (Step 6).

## Everyday use

- **You don't have to do anything.** The box starts by itself when the
  computer starts, and your devices use it automatically.
- **Your dashboard** at `http://localhost:8080` (on the box) shows what's
  running, which devices are on your network and links to everything
  else.
- **Pi-hole** (its address is on the dashboard; open it from any device
  you've added) is where you can add blocklists, let a site through that
  you need, or see what each device looked up.
- **The Control Panel** at `http://localhost:8082` (on the box) is where
  you approve, rename or remove devices.

## If something goes wrong

**Devices can't open any website.** Your box is probably off or asleep.
This is deliberate: when the box can't be reached, your devices stop
looking up sites instead of quietly going back to your internet
provider, so your browsing is never handed to someone else without you
noticing. Turn the box on (or wake it up) and wait a minute.

If the computer is on but the problem continues, open a terminal on it
and run:

```bash
cd ~/sovereign-dns && docker compose up -d
```

Then wait about a minute and run the health check below. Right after
startup it shows red, because each part has not passed its first check
yet; that is normal. If anything is still red, wait another minute and
run it again. If it says **"Pi-hole is orphaned"**, run the command it
prints, which is:

```bash
cd ~/sovereign-dns && docker compose up -d --force-recreate pihole
```

If you need a device back online while the box is out of action, that
device's owner can switch the box off for that device alone.
*Placeholder: how depends on the device and will be added with the
per-device steps.*

**A tile stays red on the dashboard.** Run this on the box. It checks
each part and marks it PASS, FAIL or SKIP, and says how to fix each
failure. It doesn't check the Docs tile. Right after the box starts, it
shows red until each part passes its first check, which takes about a
minute. If anything is still red, wait another minute and run it again.

```bash
bash ~/sovereign-dns/scripts/healthcheck.sh
```

**"Docker isn't installed"** or **"Docker is installed but not
running."** Install or open Docker Desktop, wait until it says it's
running, then run `bash install.sh` again.

**"Port 8080 (or 8081, 8082) is already in use by another program on
this computer."** Another app on the box is using the same address.
Close it and run `bash install.sh` again. If you can't close it, change
the matching `_PORT` line in the `sovereign-dns/.env` file to a free
number and run the installer again.

**Linux: "permission denied" from a `docker` command.** Type `sudo` and a
space in front of the command, then enter your password.

**Running the installer again is safe.** It refreshes the box's setup
files and keeps your password, settings and devices.

For the technical details behind each part, see
[Gotchas & Troubleshooting](gotchas.md) and the layer pages in the sidebar.
