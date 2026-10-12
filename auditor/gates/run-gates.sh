#!/usr/bin/env bash
# Phase 5 step 5.1 gates (design note section 9), run against a THROWAWAY
# Unbound this script starts itself. It never touches the live stack: its own
# container name, its own image tag, its own loopback port.
#
#   G1 quiet   : clean resolver -> the target name's checks AGREE, no flag
#   G2 plant   : local-data puts the name on another owner's IP (8.8.8.8) -> flag
#   G3 testnet : local-data puts the name on 192.0.2.1 (unrouted) -> flag
#   G4 nxdomain: local-zone always_nxdomain for the name -> flag
#
# Each scenario feeds the target as a first-seen name, so it is queued, and
# asserts the flag appears after exactly one cycle. G1 is the negative for
# G2-G4 and they are its positive controls: a comparator that always agrees
# fails G2-G4, one that always disagrees fails G1, and a dead witness 2 fails
# G1 too because G1 requires a real AGREE verdict, not merely "no flag".
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
AUD="$(cd "$HERE/.." && pwd)"
TARGET="${TARGET:-en.wikipedia.org}"   # unsigned (no AD), one stable owner (AS14907)
PORT="${PORT:-5399}"
NAME=tom-p5-unbound
IMAGE=tom-p5-unbound:gate
WORK="$(mktemp -d)"
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

printf 'FROM alpine:latest\nRUN apk add --no-cache unbound bind-tools\n' | docker build -q -t "$IMAGE" - >/dev/null
( cd "$AUD" && go build -o "$WORK/auditor" . )

start_unbound() { # $1 = extra server: lines
  cat > "$WORK/unbound.conf" <<CONF
server:
  interface: 0.0.0.0
  access-control: 0.0.0.0/0 allow
  do-ip6: no
  username: ""
  chroot: ""
  trust-anchor-file: /usr/share/dnssec-root/trusted-key.key
  qname-minimisation: yes
  logfile: ""
  verbosity: 0
$1
CONF
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker run -d --name "$NAME" -p "127.0.0.1:$PORT:53/udp" -p "127.0.0.1:$PORT:53/tcp" \
    -v "$WORK/unbound.conf:/etc/unbound/unbound.conf:ro" "$IMAGE" unbound -d -c /etc/unbound/unbound.conf >/dev/null
  for _ in $(seq 1 30); do
    if dig +time=2 +tries=1 -p "$PORT" @127.0.0.1 example.com A +short | grep -q '^[0-9]'; then return 0; fi
    sleep 1
  done
  echo "throwaway unbound did not come up"; docker logs "$NAME" | tail -20; return 1
}

# Prints: <n_flags_for_target> <target_A_verdict> <cycles>
run_auditor() {
  local st="$WORK/state-$1"; mkdir -p "$st"
  echo "$TARGET" | "$WORK/auditor" -w1 "127.0.0.1:$PORT" -state "$st" -source - -once
  python3 - "$st" "$TARGET" <<'PY'
import hashlib, json, sys
st, target = sys.argv[1], sys.argv[2]
s = json.load(open(f"{st}/status.json"))
salt = open(f"{st}/seen.salt", "rb").read()
h = hashlib.sha256(salt + target.encode()).hexdigest()[:32]
flags = [f for f in s["flags"] if f.get("name") == target]
verdict = "none"
for line in open(f"{st}/checks.jsonl"):
    c = json.loads(line)
    if c["qtype"] == "A" and (c.get("name") == target or c.get("name_hash") == h):
        verdict = c["verdict"]["result"]
decoy_flags = [f["name"] for f in s["flags"] if f.get("decoy")]
print(len(flags), verdict, s["counters"].get("cycles", 0))
if decoy_flags: print("  note: decoy flags:", decoy_flags, file=sys.stderr)
for f in flags: print("  flag:", f["qtype"], f["verdict"]["reason"], file=sys.stderr)
PY
}

fail=0
gate() { # name, unbound-lines, expect_flag(yes/no), expect_verdict
  start_unbound "$2"
  read -r n v c < <(run_auditor "$1")
  local ok=1
  if [ "$c" != 1 ]; then ok=0; fi
  if [ "$3" = yes ] && [ "$n" -lt 1 ]; then ok=0; fi
  if [ "$3" = no ] && [ "$n" -ne 0 ]; then ok=0; fi
  if [ "$v" != "$4" ]; then ok=0; fi
  if [ $ok = 1 ]; then echo "PASS $1: flags=$n A-verdict=$v cycles=$c"; else echo "FAIL $1: flags=$n A-verdict=$v cycles=$c (want flag=$3 verdict=$4 cycles=1)"; fail=1; fi
}

gate G1-quiet   ""                                                  no  agree
gate G2-plant   "  local-data: \"$TARGET. 300 IN A 8.8.8.8\""        yes disagree
gate G3-testnet "  local-data: \"$TARGET. 300 IN A 192.0.2.1\""      yes disagree
gate G4-nxdomain "  local-zone: \"$TARGET.\" always_nxdomain"        yes disagree

exit $fail
