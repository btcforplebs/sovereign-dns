# Layer 4: Setup Wizard (Planned)

**Status: not yet built.** This is the "layman-proof" onboarding layer,
deliberately sequenced *after* the Layer 1–3 gate (the Docker e2e passing
and the override verified live on a second physical device) — the wizard
wraps a proven stack rather than polishing UX on top of an unverified core.

## Goal

A non-technical user runs one reviewable command, then does everything else
through a localhost UI — no manual `nvpn set` commands, no editing `.env`
by hand.

## Planned flow

- Onboarding stays **`git clone && docker compose up -d`** — not
  curl-pipe-to-bash. A sovereignty tool can't start by asking users to
  blindly execute a remote script; clone+compose is already reviewable and
  is the same flow used for Layers 1–3.
- Wizard runs at `localhost:<port>` after `up -d`, and walks the user
  through: mesh device naming/auth, picking the DNS node, blocklist
  selection, and the override toggle.
- Handles the port-53/systemd-resolved conflict gracefully in-UI — detects
  and explains it rather than just failing. This is the single gotcha most
  likely to trip up a non-technical host; see [Gotchas](gotchas.md).
- Plain-language copy at each step (what this step does, why it's safe,
  what "fail closed" means) is drafted alongside the wizard itself, sourced
  from this docs site so nothing drifts between the two.

## Gate before starting

1. `scripts/e2e-network-dns-docker.sh` passing (from the `nostr-vpn` repo).
2. DNS override verified live on a real second physical device.

Both are currently open — see the main [README](README.md) and the
workspace `PLANS/SOVEREIGN_DNS.md` for live status.
