# Your IP address: who sees what

This page is the honest version. Sovereign DNS moves your lookups away from
your internet provider, Google and Cloudflare. It does **not** make you
invisible, and it's not a VPN for your browsing. Here is who can see what.

"The box's IP" means your home IP if the box is at home, or the VPS's IP if
it runs on a rented server.

## What it hides

This section assumes a device whose lookups really go through the box:
a computer, or a phone running a Nostr VPN build with network DNS. The
stock app-store app doesn't send them yet, so check yours
([Quickstart, Step 7](quickstart.md#step-7-see-the-ads-disappear)).

- **Which websites your devices look up, from the network they're on.**
  When a device is on café Wi-Fi, mobile data or your home network, its
  lookups travel encrypted to the box. That network sees encrypted VPN
  traffic, not the names of the sites.
- **Your lookups from Google, Cloudflare and other public DNS services.**
  The box doesn't use them. It finds every answer itself, starting from
  the internet's root servers.

## What it doesn't hide

- **The websites you visit still see your device's own IP address.** Only
  lookups go to the box. Your browsing goes straight from your device to
  the website, as it always did.
- **The network your device is on still sees which IP addresses you
  connect to**, and usually the site's name too, because most connections
  send it in the open when they start. Hiding that takes a full VPN, which
  this isn't.

## Who sees what

| Who | What they see | What they don't see |
|---|---|---|
| **Your device's network** (home Wi-Fi, café, mobile carrier) | That the device uses an encrypted VPN, and the IP addresses it browses to | The names it looks up (they go to the box, encrypted) |
| **The box's internet provider** (your home ISP, or the VPS company's network) | The box's IP, and the names **the box** looks up, because the box asks the internet's DNS servers in plain, unencrypted DNS. A repeat lookup is answered from the box's memory and not asked again. | Which of your devices asked |
| **The VPS company** (VPS option only) | Everything on the machine, in principle, including the Pi-hole log of which device looked up what | Nothing is hidden from whoever controls the machine |
| **The internet's DNS servers** (root, `.com` and the like, and each site's own DNS server) | The box's IP and the name being looked up. The box only tells each server as much as it needs: a root server hears `com`, not the full name. | Which device asked, or your devices' IPs |
| **Nostr relays** the VPN uses to find your devices (`relay.damus.io`, `nos.lol`, `offchain.pub`, `temp.iris.to` by default) | The box's IP and your devices' IPs when they connect, and public notes saying that a given device key runs this VPN. By default those notes don't contain anyone's IP address. | Your lookups, and the list of who's in your network |
| **Address-check servers** (STUN: `stun.iris.to`, plus servers run by Google and Cloudflare) | The IP of the box or device asking "what's my public address?" | Any lookups. They only answer that one question. |
| **Public relay nodes** (`fips1.iris.to`, `fips2.iris.to`) | The box's IP and its device key. They introduce your devices to the box, and when no direct link can form they carry your encrypted traffic. | The contents. The traffic is encrypted end to end. |
| **The other devices in your network** | Each other's public and local IP addresses, so they can connect directly | Each other's lookups. Only whoever runs the box sees those, in Pi-hole. |
| **Your home Wi-Fi, with whole-house DNS on** (`dns-lan.sh`) | Lookups from devices on the Wi-Fi go to the box in plain, unencrypted DNS, so anyone who can watch your home network can read them. Lookups from devices running Nostr VPN still travel encrypted. | |
| **Whoever runs the box** (you) | Every lookup from every device, in the Pi-hole log | |

## Choices that change the picture

- **Run the box on a VPS** and the outside world sees the VPS's IP
  instead of your home's. The VPS company becomes the one who could see
  the box (see [Where to run it](where-to-run.md#on-a-vps)).
- **Turning off the public relay nodes** (`NVPN_FIPS_BOOTSTRAP=off` in
  `sovereign-dns/.env`) stops the box contacting them. **Don't, unless you
  know you need to.** They're how your devices find the box and set up a
  direct link. Without them, a device away from home will most likely
  never reach the box. Because the box fails closed, that device then
  can't open websites at all.
- **Clear the Pi-hole log** or turn off its query logging in the Pi-hole
  settings if you'd rather not keep a history of lookups on the box.
