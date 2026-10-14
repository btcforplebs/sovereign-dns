# A Second Box

> **Not available yet:** this needs an nvpn build with backup support
> (`--network-dns-backup`). The nvpn commit this stack pins doesn't have it
> yet. Until the pin moves, step 3 below fails with an unknown-flag error.

One box serves DNS for the whole network. If it goes down (a laptop
sleeping, a Pi unplugged), devices on the network have no DNS at all,
because the override fails closed on purpose. A second box fixes that. It
runs its own Pi-hole and Unbound, and the network lists it as a **backup**
after the first box.

## What changes on the network

The network admin keeps an ordered list of DNS boxes. The list is signed
roster data, the same as the device list:

- the **primary** (`nvpn set --network-dns-node`), as before
- one or more **backups** (`nvpn set --network-dns-backup <npub>[,<npub>]`),
  in the order devices should try them

Every device is handed the whole list, primary first. A device that is itself
one of the DNS boxes is handed nothing. It answers from its own Pi-hole and
never forwards to another box, so a backup keeps working while the primary
sleeps.

Devices running an nvpn from before backups existed ignore the backup list
and keep using the primary, exactly as before.

This list is what lets a device fall back to a backup. How quickly each kind
of device actually switches when the primary stops answering is up to that
device's nvpn. Keeping the two Pi-holes' blocklists the same is a separate
step, not covered here.

## Setting one up

1. On the **first** box, note the two values it prints at startup:
   its `npub` and its network ID (they are in the
   `nvpn join-manual --admin-device-id <npub> --network-id <id>` line).
2. On the **second** box, before its first start, put them in `.env`:

   ```sh
   SOVEREIGN_DNS_JOIN_ADMIN=<first box npub>
   SOVEREIGN_DNS_JOIN_NETWORK=<network id>
   ```

   Then start it as usual. Instead of founding its own network, it joins the
   first box's network and prints its own npub.
3. On the **first** box, approve it and list it as a backup:

   ```sh
   docker exec sovereign-dns-nvpn nvpn add-device --device <second box npub> --publish
   docker exec sovereign-dns-nvpn nvpn set --network-dns-backup <second box npub>
   ```

Until step 3 is done, no device sends lookups to the second box.

The two values are read on the very first start only. A box that already
has its own network ignores them and says so in its log. To turn an existing
box into a second box, it has to start over with a fresh config volume.

## Checking it

`scripts/healthcheck.sh` on the second box reports
`this node is a backup nameserver for the network`. On any other device on
the network, `nvpn status` shows the primary followed by `+1 backup`.

## What to avoid

- **Don't make both boxes set themselves as the DNS node.** Any admin can
  publish the roster, and the newest one replaces the whole thing, devices
  included. Two boxes fighting over the primary undo each other's changes.
  Name one primary and list the other as a backup.
- **Don't point devices at a backup by hand.** The roster carries the list,
  so devices learn about a change the next time the roster syncs.
