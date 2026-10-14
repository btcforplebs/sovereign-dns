# A Second Box

> **Not available yet:** this needs an nvpn build with backup support
> (`add-device --dns-backup`, `set --network-dns-backup`). The nvpn commit
> this stack pins doesn't have it yet. Until the pin moves, step 4 below fails
> with an unknown-flag error.

One box serves DNS for the whole network. If it goes down (a laptop
sleeping, a Pi unplugged), devices on the network have no DNS at all,
because the override fails closed on purpose. A second box fixes that. It
runs its own Pi-hole and Unbound, and the network lists it as a **backup**
after the first box.

## What changes on the network

The network admin keeps an ordered list of DNS boxes. The list is signed
roster data, the same as the device list:

- the **primary** (`nvpn set --network-dns-node`), as before
- one or more **backups**, in the order devices should try them

Every device is handed the whole list, primary first. A device that is itself
one of the DNS boxes is handed nothing. It answers from its own Pi-hole and
never forwards to another box, so a backup keeps working while the primary
sleeps.

Devices running an nvpn from before backups existed ignore the backup list
and keep using the primary, exactly as before.

**Every admin device needs the newer nvpn first.** An admin on an older nvpn
doesn't know about the backup list, so the next roster it publishes (say,
when it approves a phone) carries no backups and quietly drops them from
every device. On a network founded by the installer the first box is the
only admin, so this means upgrading the first box before adding a second.

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

   If both boxes sit behind the same home router, also give the second box
   its own UDP port, so the two don't fight over one port and any port
   forward can point at each box separately:

   ```sh
   NVPN_LISTEN_PORT=51821
   ```

   Also decide on `NVPN_FIPS_BOOTSTRAP`. It defaults to `on`, which makes this
   box dial two public FIPS transit seeds too (see `.env.example`). Set it to
   `off` if you don't want that.
3. Start the second box as usual. Instead of founding its own network, it
   joins the first box's network and prints its own npub.
4. On the **first** box, approve it **and** list it as a backup in one
   command:

   ```sh
   docker exec sovereign-dns-nvpn nvpn add-device --device <second box npub> --dns-backup
   ```

   Use this one command, not `add-device` followed by `set
   --network-dns-backup`. In between those two, the second box is an
   ordinary member: it points its own lookups at the first box and runs
   nvpn's DNS helper next to its Pi-hole, which is exactly what a backup
   must not do. `--dns-backup` admits it already listed as a backup.

Until step 4 is done, no device sends lookups to the second box.

To add a third box, run the same step 4 for it: `--dns-backup` adds to the
end of the list. `nvpn set --network-dns-backup <npub>[,<npub>]` is for
reordering or removing backups, and it **replaces the whole list**, so name
every backup you want to keep.

The two values are read on the very first start only. A box that already
has its own network ignores them and says so in its log. To turn an existing
box into a second box, it has to start over with a fresh config volume.

## Checking it

`scripts/healthcheck.sh` on the second box reports
`this node is a backup nameserver for the network`. Before step 4 is done, and
until the first box's roster has reached it, the healthcheck on the second
box is red ("neither the network's DNS node nor a backup", override off).
That is expected while it waits, not a failed join. On any other device on
the network, `nvpn status` shows the primary followed by `+1 backup`.

## What to avoid

- **Don't make both boxes set themselves as the DNS node.** Any admin can
  publish the roster, and the newest one replaces the whole thing, devices
  included. Two boxes fighting over the primary undo each other's changes.
  Name one primary and list the other as a backup.
- **Don't point devices at a backup by hand.** The roster carries the list,
  so devices learn about a change the next time the roster syncs.
