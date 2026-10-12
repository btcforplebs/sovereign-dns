# auditor: Phase 5 step 5.1 (background resolver auditor)

The auditor checks in the background that the box's own Unbound (witness 1) is
not being given poisoned answers. It re-checks a sample of the names the box
looked up against a public resolver reached over **Oblivious DoH** (RFC 9230,
witness 2), and it flags any disagreement. It never sits in the lookup path:
clients keep getting Unbound's answer exactly as today.

Design: `PLANS/SOVEREIGN_DNS_PHASE_5_DESIGN.md` in the Buzz workspace
(sections 6.1, 6.3 and 9; the decisions in section 10 were taken 2026-10-11).

## What it does

1. **Intake.** It follows a query log (`-source`). Each name is asked once of
   witness 1 with the DO bit set:
   - DNSSEC-secure (AD): skipped, because validation already settled it.
   - First-seen: always queued.
   - Already seen: queued with probability `-rate` (default 5%).
   - House and mesh names (`.lan`, `.local`, `.arpa`, `.fips`, ...): never
     queued, because sending them out would leak them.

   "Seen" is kept as salted hashes in the state directory, never as names.
2. **Check.** At random intervals (`-interval`, default mean 15 s), one queued
   name plus `-decoys` decoy names (default 2, from `decoys.txt`) are checked
   in shuffled order, A and AAAA each. Each check asks witness 1 and witness 2.
   Witness 2 uses a random relay/target pair whose **operators differ**: the
   Fastly relay and `odoh.crypto.sx` are both run by Frank Denis, so that pair
   is never used. If the first relay fails, it retries once through the other.
3. **Compare** (design 6.3):
   - an rcode mismatch is a disagreement, e.g. an injected NXDOMAIN
   - a shared address is agreement
   - otherwise the origin-ASN sets are compared; a common ASN is agreement
   - data on one side only, or no common owner network, is a disagreement
   - SERVFAIL or a failed lookup is **inconclusive**, never agreement
4. **Flag.** Every disagreement goes into `status.json` with both answers and
   the route. Agreements are logged by salted hash only.

## Output (for the hub)

`<state>/status.json` is rewritten atomically after every check:

```json
{
  "schema": 1,
  "started": "...", "updated": "...",
  "counters": {"checks_agree": 12, "checks_disagree": 0, "checks_inconclusive": 0,
               "cycles": 2, "sampled_first_seen": 2, "sampled_random": 0,
               "secure_skipped": 0, "not_sampled": 0, "queue_dropped": 0, "intake_errors": 0},
  "queue_len": 0,
  "last_public_ok": "...",
  "last_flag": null,
  "flags": [ { "time": "...", "name": "...", "decoy": false, "qtype": "A",
               "verdict": {"result": "disagree", "reason": "..."},
               "box": {...}, "public": {...}, "route": {"relay": "...", "target": "..."} } ]
}
```

- `last_public_ok: null`, or an old value, means witness 2 is not answering.
  The hub should show that rather than read a quiet flags list as healthy.
- Flags persist across restarts. `<state>/checks.jsonl` holds one line per
  check. `-listen 127.0.0.1:8099` also serves the same JSON at `GET /status`.

## Run

```sh
go build -o auditor .            # Go >= 1.26 (crypto/hpke)
./auditor -w1 127.0.0.1:53 -state /var/lib/sdns-auditor -source /var/log/pihole/pihole.log
```

The relay and target hostnames, and the Team Cymru ASN lookups, are resolved
through `-w1`, so the auditor does not depend on the host's resolver.

## Gates

```sh
go test ./...          # offline: ODoH seal/open round trip + tamper, relay fallback,
                       # comparator table, sampling, decoys, flag file
./gates/run-gates.sh   # live: starts its OWN Unbound (container tom-p5-unbound,
                       # 127.0.0.1:5399) and never touches the stack
```

| Gate | Throwaway Unbound has | Must see |
|---|---|---|
| G1 quiet | nothing planted | `en.wikipedia.org` A verdict **agree**, 0 flags, 1 cycle |
| G2 plant | `local-data` A 8.8.8.8 (another owner) | flag after 1 cycle |
| G3 testnet | `local-data` A 192.0.2.1 (unrouted) | flag after 1 cycle |
| G4 nxdomain | `local-zone ... always_nxdomain` | flag after 1 cycle |

G1 requires a real **agree**, so a dead witness 2 (all inconclusive) fails it
instead of passing as "no flags". Mutants were checked on 2026-10-11:
- comparator always-agree: G2, G3 and G4 fail
- comparator always-disagree: G1 fails
- both relays unreachable: all four fail
- relay retry removed: the fallback unit test fails

## Not done yet / known limits

- **Not wired into the stack.** There is no Dockerfile or compose service, on
  purpose (5.1 scope). It is not yet verified that live Pi-hole v6 writes
  `query[A]` lines to `pihole.log`. Check that before relying on `-source`.
- **The ASN test is weak.** Two customers of one CDN share an ASN. Design 6.3
  adds a TLS tie-breaker, which is not built.
- **Cymru learns an address** whenever the two witnesses returned different
  ones. A local IP-to-ASN table would remove that.
- **Witness 2's answers are not DNSSEC-validated by the box** (step 5.2).
- **The decoy list is a small built-in sample.** Pin a larger popular-names
  list later.
