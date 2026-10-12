# Layer 1: Unbound

Unbound is the recursive resolver at the bottom of the stack — the piece
that actually finds the answer to "what's the address for this domain?".

- Container is **internal-only**, no published port. Only Pi-hole can reach it.
- **Recursive mode**: walks down from the internet's root servers itself,
  rather than forwarding to Google or Cloudflare. No third party ever sees
  the raw lookup.
- **DNSSEC validation on**, using Unbound's built-in root hints — answers
  are checked for cryptographic authenticity, not just accepted at face
  value.
- Image: built locally from `unbound/Dockerfile` — a digest-pinned Alpine
  base with exact `unbound` and `drill` package versions, native on arm64
  and amd64. `scripts/check-pins.sh` fails if either pin goes missing.

**Milestone:** `dig @<unbound-ip> example.com` (or the container's
internal address) returns a validated answer with zero forwarders
configured.

## Design notes

- Listens only on the `dns-internal` Docker network (`172.28.53.11` in the
  default compose file) — never exposed to the host or the mesh.
- Healthcheck (`unbound/healthcheck.sh`) runs `drill` against `127.0.0.1`
  inside the container for a random, never-before-asked name under `.com`,
  so it can't pass from cache. NXDOMAIN or NOERROR passes; SERVFAIL or a
  timeout fails.
- The DNSSEC root trust anchor is refreshed at every start
  (`unbound-anchor`), and the config is checked with `unbound-checkconf`
  before Unbound runs.
- No forward-zone exists anywhere in the image's config, so nothing is
  mounted over `/etc/unbound`. `scripts/check-pins.sh` refuses any volume on
  the Unbound service.
