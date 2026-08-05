# Glossary

**DNS (Domain Name System)** — the phonebook of the internet; translates a
name like `example.com` into the numeric address a computer needs to
connect to it.

**Recursive resolver** — a DNS server that finds the answer itself by
asking the internet's root servers, rather than forwarding the question to
someone else's server. Unbound is one.

**DNSSEC** — a cryptographic signature system for DNS answers, so a
resolver can verify an answer hasn't been tampered with in transit.

**Upstream** — the server a DNS component forwards unresolved queries to.
Pi-hole's upstream in this stack is Unbound, and only Unbound.

**Mesh** — the private network of devices connected via NostrVPN's
WireGuard tunnels.

**Roster** — the signed list of devices and settings for a NostrVPN
network. The DNS override setting rides on this roster so it propagates to
every device automatically.

**npub** — a public identifier (Nostr public key, human-readable form)
that safely names a device or identity without exposing anything private.

**Override (DNS override)** — the network-wide setting that tells every
mesh device to route DNS through a chosen node instead of its normal local
DNS.

**Fail closed** — the stack's behavior when the DNS node is unreachable:
lookups fail outright instead of silently falling back to a less private
DNS server. The opposite, "fail open," would leak queries without warning.

**Self-exclusion** — the DNS node's own exemption from the override it's
serving, so it doesn't try to forward its own queries to itself.

**Gate** — a milestone that must pass before the next phase of work starts.
See the [Layer 4 wizard](layer-4-wizard.md) page for the current gate.
