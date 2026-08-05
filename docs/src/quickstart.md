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

## 2. Join it to your device mesh

The installer printed an "npub" — a public identifier, safe to share,
that's this container's name on your mesh. Add it to your NostrVPN
network the same way you'd add any other device (or approve its join
request from your admin device).

## 3. Turn on the auto-DNS for your whole fleet

The DNS-override flags below are new — newer than whatever `nvpn` you
already have installed, most likely. The installer already extracted a
matching CLI for you at `sovereign-dns/bin/nvpn`, so you don't need to
build or install anything separately. From an admin device already
joined to your network, run (from inside the `sovereign-dns` folder):

```bash
./bin/nvpn set --network-dns-node <the-npub-from-step-2> --network-dns-override
```

It reads your existing NostrVPN config, so this only works if this
device already has an active network — join one first if you haven't.
Every device on your mesh now sends DNS through this stack automatically —
no per-device settings.

## 4. Manage it

Visit `http://<this-server's-mesh-ip>/admin` for the Pi-hole dashboard —
blocklists, per-device stats, what's being blocked.

## If something breaks

- **DNS stops working entirely on a device** — that's fail-closed doing its
  job; the server is unreachable. Check `docker compose ps` on the server,
  or turn the override off from your admin device to get local DNS back
  instantly.
- See [Gotchas & Troubleshooting](gotchas.md) for known sharp edges.
