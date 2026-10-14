# settings-sync: one shared list for every DNS box

Every DNS box runs the same Pi-hole filtering: the same blocklists, allowed and
blocked domains, and groups. You can change them from any box's page, and no
box is the main one.

## How it works

- **The list is one Nostr event signed by the owner's own key.** It is an
  addressable event: kind `30078` with `d` = `sovereign-dns/settings`. A box
  never holds the owner's key. The edit page asks the owner's signer (a
  NIP-07 browser extension) to seal and sign it.
- **It is sealed.** A random 32-byte key encrypts the list with AES-256-GCM.
  That key is then wrapped with NIP-44 v2 for each reader: the owner, and each
  box the list names. Wraps are filed under a salted hash, not a pubkey, so
  the public event shows how many readers there are but not which boxes. The
  list carries no clients, local DNS records, DHCP settings or box wiring.
  Both the page and the box refuse a list with an IP address, a local name
  (`.local`, `.lan`, a single-label host, and so on) or a URL with a password
  in it.
- **It is stored on the owner's relays** (`SETTINGS_RELAYS`). Each box checks
  every `SETTINGS_SYNC_INTERVAL` (2 minutes by default).
- **Newest wins, and a box never goes back.** A box only accepts an event if
  its kind and `d` tag match, its id and BIP-340 signature verify, its author
  is `SETTINGS_OWNER`, and it is dated no more than 10 minutes ahead of the
  box's clock. The newest accepted event wins (later `created_at`, then the
  lowest id). A box ignores any list older than the one it already applied,
  even if a relay serves nothing newer, and even after a restart.
- **A list the box can't use is ignored.** That covers a list that isn't
  sealed to this box, won't decrypt or fails the address checks. The box keeps
  its current list and shows why on its page. It does not fall back to an
  older list it can read, because that would be a rollback.
- **Relays down: nothing changes.** The box keeps its last list in its
  volume. It applies that copy again on every run, so it also restores a
  shared entry that someone removed in Pi-hole's own page.
- **Nothing gets wiped.** A box remembers which entries came from the shared
  list. Applying a list adds or updates everything in it. It removes only
  entries that an earlier list added and this one dropped. Entries that were
  already on a Pi-hole are never removed, and neither are entries someone adds
  in Pi-hole's own page. The edit page lists them under "Only on this box"
  with one button to add them to the shared list. When no list exists yet, the
  page starts from the current box's Pi-hole, so the first save keeps what is
  already there.
- **Two pages at once.** Each save names the list it was edited from. A box
  refuses to publish a save if a different list has been saved since, so two
  open pages can't silently drop each other's changes.

## Keys

| Key | Where it lives | What it can do |
|---|---|---|
| Owner's key | His signer only | Sign and seal the list |
| Box key | `settings-sync-data:/data/box.key`, mode 0600, created on first start | Open lists sealed to this box. It can't sign a list any box would accept. |

The box key is separate from nvpn's key. The page shows this box's npub. Add
it to the list's boxes, from any box's page, so this box can read the list.

## Setup (per box)

In `.env`:

```
SETTINGS_OWNER=npub1...                      # the owner's npub
SETTINGS_RELAYS=wss://relay.one,wss://relay.two
# SETTINGS_SYNC_PORT=8083                    # edit page, loopback only
```

Open `http://localhost:8083` on the box. To add a box, open its page, unlock
with the signer, and save. The page adds the box to the list itself. The page
needs a secure context for WebCrypto: use `http://localhost`, not a LAN
address over plain http.

## Not done here

- **Hub link.** Add a link from the hub to this page. A box without a screen
  needs a hub route that proxies to `settings-sync:8083`, after Tom's LAN-hub
  PR lands. When that route is added, put its Host name in
  `SETTINGS_SYNC_HOSTS`. That page must be served over HTTPS (or reached at
  localhost), or the browser won't run the page's crypto.
- **`scripts/check-pins.sh`** needs rules for the new service, since it
  default-denies unknown services. The exact lines are in the PR description.
- **Signers.** Only NIP-07 signers are supported, not NIP-46 (bunker) or
  Amber. The signer must support `nip44`.

## Tests

`go test ./...` runs against a fake Pi-hole v6 API and in-memory relays:

- `TestNewerSignedListReplacesOlder`: v2 replaces v1. A relay that serves only
  v1 can't roll the box back, before or after a restart.
- `TestIgnoresForeignAndUnreadableLists`: the box ignores lists that are
  - signed by someone else
  - an old list of the owner's re-signed by someone else
  - an old list re-dated
  - edited after signing
  - not sealed to this box
  - unreadable
  - carrying a LAN address
  - dated in the future
  - published under another `d` tag

  In each case the Pi-hole stays the same.
- `TestTwoBoxesConverge`: two boxes start from different Pi-holes. After the
  shared list is applied and the extras are adopted, both Pi-holes are
  identical, and a removal reaches both. A box's own entries survive until the
  list takes them over.
- `TestRelaysDownKeepsLastList`: with every relay down, after a restart, the
  box restores a shared entry that was removed locally.
- `TestPublishEndpoint`: the box publishes only lists the owner signed, and
  refuses a stale save, a rebinding Host and a cross-origin POST.
- `TestNIP44SpecVector`: checks against the NIP-44 spec vector.
- `TestNpubRoundTrip`: checks against the NIP-19 example.
