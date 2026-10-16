# Gotchas & Troubleshooting

## Host port 53 conflicts

On many Linux hosts, `systemd-resolved` squats on port 53 by default. This
stack avoids the problem by binding Pi-hole only to the mesh tunnel
interface (`utun100`), not the host's public interface — so there's
normally nothing to fix. If you're running Pi-hole standalone outside this
compose file and hit a bind error, either disable systemd-resolved's stub
listener or bind explicitly to the mesh interface.

## Pi-hole must follow the mesh address

A box's mesh address changes when it joins another box's network: a second
box starts on a network of its own, and being added to the first box's
network gives its tunnel a new address. Pi-hole used to run in
`FTLCONF_dns_listeningMode=BIND`, which binds the addresses an interface has
when Pi-hole starts and never looks again. Such a box installed with no
errors, its admin page answered on the mesh (the web server listens on every
address), and every DNS lookup timed out. The stack now sets the listening
mode to `NONE` and gives dnsmasq `bind-dynamic`, which binds each address as
it appears. (`BIND` and `bind-dynamic` together stop dnsmasq from starting.)
The installer ends by asking the box for a name and an ad domain at its mesh
address and says plainly whether it answers; run that check any time with
`bash scripts/dns-check.sh`.

## Linux crash-repair is intentionally skipped

`revert_stale_system_dns` doesn't run its full repair path on Linux after
an unclean shutdown, because the pre-crash `resolv.conf` baseline doesn't
survive a crash to compare against. This is a known, accepted gap — worth
knowing since Linux servers are the most likely host for this stack. macOS
and Windows crash-repair (stale resolver files / NRPT rules) are handled.

## The override is policy, not containment

A device owner can hand-edit their local `nvpn` config to opt out of the
network DNS override. That's by design — this is a sovereignty tool, and
every device answers to its own owner. Don't rely on the override as an
enforcement mechanism against an uncooperative device owner.

## Single point of failure

One DNS stack instance is one point of failure for the whole fleet's DNS
(by design — see "fail closed" in the [Introduction](README.md)). The override
switch on the hub (see [Layer 3](layer-3-nostrvpn.md)) lives on the same box,
so it is no help once the box is down. Recovery then is: restart the stack on
the host (`docker compose up -d`); if the host itself is down, each device
owner can opt out locally in their own nvpn config instead of waiting on the
network setting. A second Pi-hole+Unbound instance as a fallback is a
planned post-MVP improvement.

## nvpn is pinned to a commit, not a release

The stack builds `nostr-vpn` at one exact commit (`NVPN_COMMIT` in
`docker-compose.yml`), taken from `master` of the btcforplebs/nostr-vpn fork.
The build fetches that commit and refuses anything that isn't a full
40-character SHA, so two installs never build different code. Every
container image is pinned by digest for the same reason. To move to a
newer nvpn, change the SHA in `docker-compose.yml` and `nvpn/Dockerfile`
together; `scripts/check-pins.sh` catches a mismatch.
