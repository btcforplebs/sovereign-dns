# Quickstart

You'll need: a computer or small server (Linux is easiest) with
[Docker](https://docs.docker.com/get-docker/) installed, kept on most of
the time since it becomes your household's DNS.

## 1. Run the installer

Ask whoever gave you this stack for `scripts/install.sh` (a single file —
paste it into a new file with that name), then run:

```bash
bash scripts/install.sh
```

It asks you for one thing — a password for the Pi-hole dashboard — builds
everything, starts it, and prints your device's ID when it's ready.
First run takes ~10 minutes (compiling the NostrVPN piece from source).
It also writes out `docker-compose.yml` and the `nvpn/` build files, so
nothing else needs to be downloaded separately.

## 2. That's it — the container is its own network's admin

On a fresh install, this container founds a brand-new NostrVPN network
with itself as the sole admin, then immediately turns on the DNS
override for itself. No separate admin device, no pre-existing network
required, no manual `add-device`/`set` commands to run — the installer's
output already shows this working.

To add another device (phone, laptop) to this network, it's two steps —
deliberately not automatic, since the network ID and this container's ID
are shared as plain text and auto-approving anyone who has them would
remove the only access control this network has:

1. On that device, run the command the installer printed:
   ```bash
   nvpn join-manual --admin-device-id <this-container's-npub> --network-id <this-network's-id>
   ```
   This only queues a join request — the device isn't on the network yet.
2. Once you've confirmed it's a device you actually want on your network,
   approve it in the **control panel** on the machine running the container —
   open `http://localhost:8082` in a browser and accept the pending join
   request. (Terminal fallback, replacing `<joiner-npub>` with the printed
   npub: `docker exec sovereign-dns-nvpn nvpn add-device --device <joiner-npub> --publish`)

That device now resolves DNS through this stack automatically too.

## 3. Manage it

- `http://localhost:8082` (only on the machine running the container) —
  the **NostrVPN control panel**: approve or reject join requests, add and
  remove devices, rename the network, QR codes for pairing. It has no
  login of its own, which is exactly why it's only ever reachable from
  localhost — never from the network.
- `http://<this-server's-mesh-ip>/admin` — the Pi-hole dashboard:
  blocklists, per-device stats, what's being blocked.

## If something breaks

- **DNS stops working entirely on a device** — that's fail-closed doing its
  job; the server is unreachable. Check `docker compose ps` on the server,
  or turn the override off from your admin device to get local DNS back
  instantly.
- See [Gotchas & Troubleshooting](gotchas.md) for known sharp edges.
