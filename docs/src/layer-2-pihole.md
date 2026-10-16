# Layer 2: Pi-hole

Pi-hole sits in front of Unbound — the filtering and visibility layer.

- **Upstream = Unbound's internal address only.** No Google/Cloudflare
  fallback.
- **Port 53 bound only to the mesh tunnel interface** (`utun100` by
  default), never the public host interface — zero exposed ports to the
  internet.
- Blocklists and per-client rules apply to every device on the mesh, not
  just the local network.
- Admin dashboard for visibility into what's being blocked, per device.

**Milestone:** a mesh client resolves through Pi-hole, ads/trackers are
blocked, and the query log shows Unbound as the only upstream.

## Design notes

- Pi-hole shares the `nvpn` container's network namespace
  (`network_mode: service:nvpn`) so it can bind the mesh tunnel interface
  directly. It binds `utun100` and `eth0` one address at a time (dnsmasq
  `bind-dynamic`; `FTLCONF_dns_listeningMode=NONE` so FTL adds nothing of
  its own), which keeps it from colliding with nvpn's own DNS stubs on
  `127.0.0.1` and lets it follow the mesh address when that changes.
- Admin UI is reachable at `http://<mesh-ip>/admin`, password set via
  `PIHOLE_PASSWORD` in `.env`.
