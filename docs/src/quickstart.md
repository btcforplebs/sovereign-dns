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

## 2. Join it to your mesh and turn on the override

The installer tries to do this step for you automatically, on the spot:
it gets an admin CLI with the new DNS-override flags (extracted straight
out of the container on Linux; built once from source via `cargo` on
macOS/other, since Docker runs the container as a Linux binary that
can't execute directly there), then runs both:

```bash
nvpn add-device --device <this-container's-npub> --publish
nvpn set --network-dns-node <this-container's-npub> --network-dns-override
```

This only succeeds if the machine you ran the installer on is *already*
an active admin on a NostrVPN network — that's a trust boundary (who's
allowed to add devices to your mesh), not something the installer should
skip. If it isn't yet, the installer tells you so and prints the exact
two commands to run once it is. If it is, you're done — no further
steps needed.

Every device on your mesh now sends DNS through this stack automatically —
no per-device settings.

## 3. Manage it

Visit `http://<this-server's-mesh-ip>/admin` for the Pi-hole dashboard —
blocklists, per-device stats, what's being blocked.

## If something breaks

- **DNS stops working entirely on a device** — that's fail-closed doing its
  job; the server is unreachable. Check `docker compose ps` on the server,
  or turn the override off from your admin device to get local DNS back
  instantly.
- See [Gotchas & Troubleshooting](gotchas.md) for known sharp edges.
