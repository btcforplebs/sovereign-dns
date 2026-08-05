# Layer 1: Unbound

Unbound is the recursive resolver at the bottom of the stack — the piece
that actually finds the answer to "what's the address for this domain?".

- Container is **internal-only**, no published port. Only Pi-hole can reach it.
- **Recursive mode**: walks down from the internet's root servers itself,
  rather than forwarding to Google or Cloudflare. No third party ever sees
  the raw lookup.
- **DNSSEC validation on**, root hints baked in — answers are checked for
  cryptographic authenticity, not just accepted at face value.
- Image: `mvance/unbound`.

**Milestone:** `dig @<unbound-ip> -p 5335 example.com` (or the container's
internal address) returns a validated answer with zero forwarders
configured.

## Design notes

- Listens only on the `dns-internal` Docker network (`172.28.53.11` in the
  default compose file) — never exposed to the host or the mesh.
- Healthcheck uses `drill`/`dig` against `127.0.0.1` inside the container.
