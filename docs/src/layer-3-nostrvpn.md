# Layer 3: NostrVPN

NostrVPN is the transport and propagation layer — the mesh tunnel
connecting every device, plus the mechanism that tells every device to use
this DNS stack, automatically.

- Built from the `nostr-vpn` repo, branch `network-dns-gui` — the feature
  that carries an admin's DNS-node choice on the **signed mesh roster**
  (`dns-node`/`dns-override` tags), instead of relying on a third-party
  control plane like Tailscale's admin console.
- Enforced admin-only in core (`set_network_dns`), so every client (CLI,
  macOS GUI, etc.) gets the same check — no bypass through a lighter-weight
  client.
- **Self-exclusion:** the DNS device itself is excluded from the override,
  so it keeps its own resolver and no forwarding loop is possible.
- **Fail-closed:** override on + nameserver unreachable → lookups fail
  rather than leak to local DNS. `nvpn status` reports
  `override=on UNREACHABLE, DNS fails closed` in plain text.
- **Crash recovery:** startup repair clears stale macOS resolver files and
  Windows NRPT rules after an unclean shutdown. (Linux crash-repair is
  intentionally skipped — see [Gotchas](gotchas.md).)
- **Stale-roster hygiene:** a nameserver removed from the roster is dropped,
  not trusted; a removed device can't keep serving the network's DNS.

**Milestone:** a device entirely off-LAN (cellular, hotel wifi) resolves
through the mesh DNS node. Killing the node fails DNS closed (no leak);
bringing it back auto-reverts.

## How the propagation actually works

```bash
# from your admin device, once the DNS node has joined the mesh
nvpn set --network-dns-node <container-npub> --network-dns-override
nvpn reload
```

The setting rides the signed roster to every member automatically — no
per-device configuration. Any device can confirm with `nvpn status`.

## Honest caveat: policy, not containment

The override is trust-based on members: a device owner can hand-edit their
local nvpn config to opt out. That's intentional — this is a sovereignty
tool, and every device answers to its own owner, not a central authority.

## Honest caveat: no iOS coverage yet

iOS does not implement the network DNS override. Enforcement lives only in
the CLI daemon; the crate the iOS app builds on can publish the policy but
never consumes it. Don't treat an iOS device's presence on the roster as
DNS coverage — check `nvpn status` on the device itself.

The override toggle itself is CLI/macOS-GUI only — the web control panel
(Layer 4) can approve devices and manage the roster, but has no
network-DNS control (`nvpn set --network-dns-override ...` must be run
from a shell, e.g. `docker exec sovereign-dns-nvpn nvpn set
--network-dns-override false`).
