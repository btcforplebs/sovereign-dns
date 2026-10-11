# Gotchas & Troubleshooting

## Host port 53 conflicts

On many Linux hosts, `systemd-resolved` squats on port 53 by default. This
stack avoids the problem by binding Pi-hole only to the mesh tunnel
interface (`utun100`), not the host's public interface — so there's
normally nothing to fix. If you're running Pi-hole standalone outside this
compose file and hit a bind error, either disable systemd-resolved's stub
listener or bind explicitly to the mesh interface.

## Pi-hole BIND mode needs the tunnel interface to exist first

Pi-hole's `FTLCONF_dns_listeningMode=BIND` on `utun100` requires that
interface to exist, which only happens once the `nvpn` daemon has meshed.
On first boot, FTL may retry/restart a few times until the interface
appears. The healthcheck + `restart: unless-stopped` should ride through
this automatically — if Pi-hole stays unhealthy for more than a minute or
two, check `docker compose logs nvpn` to confirm it actually meshed.

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
(by design — see "fail closed" in the [Introduction](README.md)). The web
control panel has no override switch (see [Layer 3](layer-3-nostrvpn.md)),
so if the stack goes down, recovery is: restart it on the host
(`docker compose up -d`); if the host itself is down, each device owner
can opt out locally in their own nvpn config instead of waiting on the
network setting. A second Pi-hole+Unbound instance as a fallback is a
planned post-MVP improvement.

## nvpn is pinned to a commit, not a release

The stack builds `nostr-vpn` at one exact commit (`NVPN_COMMIT` in
`docker-compose.yml`), taken from the `network-dns-gui` feature branch.
The build fetches that commit and refuses anything that isn't a full
40-character SHA, so two installs never build different code. Every
container image is pinned by digest for the same reason. To move to a
newer nvpn, change the SHA in `docker-compose.yml` and `nvpn/Dockerfile`
together; `scripts/check-pins.sh` catches a mismatch.
