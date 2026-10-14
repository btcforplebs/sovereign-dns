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
  Both the page (before it asks the signer) and the box refuse a list that
  carries an address anywhere: an IPv4 or IPv6 literal in any field (escaped
  regexes, URL paths and queries, a trailing dot and the dashed `10-0-0-5`
  form included), a reverse-lookup name (`in-addr.arpa`, `ip6.arpa`), a local
  or IP-echo name (`.local`, `.lan`, `.home`, `.internal`, `nip.io`,
  `sslip.io`, ...), a single-label list host or exact domain, or a URL with a
  password in it. The two copies of this guard (`settings.go`, `page.html`)
  are tested against the same cases. Box names may be a single word ("Pi 4"),
  since a friendly name is not an address; a box name with a local name or IP
  in it is refused.
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
- **Nothing gets wiped.** A box remembers which entries it created itself.
  Applying a list adds or updates everything in it, and removes only entries
  this box created for an earlier list that the new one dropped. An entry that
  was already on a Pi-hole never becomes the box's to remove, even when the
  shared list names it too, so dropping it from the list leaves it on that box
  (the page then shows it under "Only on this box"). The same goes for entries
  someone adds in Pi-hole's own page. "Only on this box" has one button to add
  them all to the shared list. When no list exists yet, the page starts from
  the current box's Pi-hole, so the first save keeps what is already there.
  The record of what a box created is saved even when a run stops part way.
- **Two pages at once.** Each save names the list it was edited from. A box
  refuses to publish a save if a different list has been saved since, so two
  open pages can't silently drop each other's changes.
- **The page checks what the box hands it.** Before editing, the page
  recomputes the list's id and checks its author, kind and `d` tag, and it
  opens the list with the owner's own NIP-44 wrap, which only his key can
  make. It shows when the list it is editing was saved.
- **Saved state is checked again on start.** The applied list in
  `state.json` gets the same checks as one from a relay. If it fails, the box
  forgets it and the record of what it created, so it removes less, never
  more.
- **Relay fetches are bounded:** `limit` 20, at most 50 events and 1 MB per
  message per relay per poll.

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
- **The Host check is not access control.** It stops DNS rebinding from a
  browser. The service listens on `:8083` inside the container, so anything
  that can reach the container on `dns-internal`, or any process on the host,
  can read `/api/state` (this box's lists and status, no keys). It can't
  publish: that needs an owner-signed event. Follow-up: bind to loopback
  inside the container, or check the peer, once the hub proxy decides how it
  connects.
- **Taking a box out.** A box dropped from the list can no longer read new
  lists, so it keeps enforcing the last one it could read, and only its own
  page says so. A mistyped npub has the same effect. Follow-up: decide what a
  dropped box should do (a readable "no longer managed" notice, or clearing
  what it created) and warn on the page and the hub when a save drops a box.
- **Not proven in the browser.** The page checks the list's id and author
  and that the owner sealed it, but it can't check the signature or that the
  list is the newest on the relays. A box could hand it an older genuine list
  to edit. Reading the relays from the page would close that.

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
  identical. A removal reaches every box that created the entry; the box that
  had it first keeps it.
- `TestPreexistingEntryNeverRemoved`: a blocklist, deny domain and group that
  were on the Pi-hole first survive a list that names them and a later list
  that drops them, across a restart.
- `TestValidateRefusesAddresses` and `TestPageGuardMatchesGo`: the same cases
  through `settings.go` and, under node, the page's guard (skipped if node is
  missing).
- `TestSavedStateIsReverified`: a far-future or foreign list in `state.json`
  is dropped on start.
- `TestFetchListsIsCapped`: a relay that streams without end yields 50 events.
- `TestRelaysDownKeepsLastList`: with every relay down, after a restart, the
  box restores a shared entry that was removed locally.
- `TestPublishEndpoint`: the box publishes only lists the owner signed, and
  refuses a stale save, a rebinding Host and a cross-origin POST.
- `TestNIP44SpecVector`: checks against the NIP-44 spec vector.
- `TestNpubRoundTrip`: checks against the NIP-19 example.
