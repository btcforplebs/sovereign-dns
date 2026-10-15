# Sovereign DNS

Self-hosted, ad-filtered, DNSSEC-validated DNS for **every device on your
NostrVPN mesh** — one `docker compose up`, one admin command, zero
per-device configuration, zero third-party control plane.

## The problem this solves

Every time a device looks up a website, that lookup ("what's the address
for example.com?") normally goes to your ISP, or to Google/Cloudflare if
you've set a custom DNS. Whoever answers that lookup sees every site you
visit — a browsing history sitting with a third party.

Sovereign DNS answers those lookups yourself, filters out ads/trackers on
the way, and does it automatically for every device you own — home wifi,
cell data, a hotel — without touching each device's settings by hand.

## How a lookup travels

```
your device → NostrVPN (tunnel) → Pi-hole (filter) → Unbound (answer)
```

| Layer | Component | Role |
|---|---|---|
| 1 | **Unbound** | Recursive resolver — asks the internet's root servers directly, never Google/Cloudflare. Validates DNSSEC. |
| 2 | **Pi-hole** | Filtering + visibility. Blocks ad/tracker domains before they're looked up. Upstream = Unbound only. |
| 3 | **NostrVPN** | Transport + propagation. The mesh tunnel connecting devices, plus a signed roster setting that tells every device "send DNS here" automatically. |
| 4 | **Setup wizard** | Guided localhost onboarding for non-technical users (post-gate, not yet built). |

**Why "sovereign"?** Every piece here is something *you* run and control.
No third party sits in the loop deciding your DNS, seeing your lookups, or
holding a kill switch over the whole thing.

**"Fail closed"** — if the stack goes down, devices' lookups fail rather
than silently falling back to whatever DNS is available. You'll notice
immediately instead of silently losing protection; flip the override off
from your admin device to restore local DNS instantly.

New here? Start with [Before you install](before-you-install.md), then
[Where to run it](where-to-run.md). See [Quickstart](quickstart.md) to get
running, or read each layer's page for the technical detail behind it.
