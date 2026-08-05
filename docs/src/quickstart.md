# Quickstart

You'll need: a computer or small server (Linux is easiest) with
[Docker](https://docs.docker.com/get-docker/) installed, kept on most of
the time since it becomes your household's DNS.

## 1. Get the code

```bash
git clone https://simplysovereign.btcforplebs.com/git/39d8aef61514a2fd1fa12c932690eeda8a3c93ed57efedeef63d52c1379de404/sovereign-dns
cd sovereign-dns
```

## 2. Set your password

```bash
cp .env.example .env
```

Edit `.env` and set `PIHOLE_PASSWORD` — your login for the Pi-hole
dashboard. Everything else can stay default to start.

## 3. Start it up

```bash
docker compose up -d --build
```

First run takes ~10 minutes (compiling the NostrVPN piece from source).
This also starts the docs site you're reading right now, self-hosted at
`http://<this-server>:8081`.

## 4. Check it's healthy

```bash
docker compose ps
docker compose logs -f nvpn
```

## 5. Join it to your device mesh

Get this container's identity:

```bash
docker compose logs nvpn | grep npub:
```

That prints an "npub" — a public identifier, safe to share, that's this
container's name on your mesh. Add it to your NostrVPN network the same
way you'd add any other device (or approve its join request from your
admin device).

## 6. Turn on the auto-DNS for your whole fleet

From your admin device (not the server):

```bash
nvpn set --network-dns-node <the-npub-from-step-5> --network-dns-override
```

Every device on your mesh now sends DNS through this stack automatically —
no per-device settings.

## 7. Manage it

Visit `http://<this-server's-mesh-ip>/admin` for the Pi-hole dashboard —
blocklists, per-device stats, what's being blocked.

## If something breaks

- **DNS stops working entirely on a device** — that's fail-closed doing its
  job; the server is unreachable. Check `docker compose ps` on the server,
  or turn the override off from your admin device to get local DNS back
  instantly.
- See [Gotchas & Troubleshooting](gotchas.md) for known sharp edges.
