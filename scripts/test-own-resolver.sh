#!/usr/bin/env bash
# The nvpn container (and the daemon in it) looks names up through the box's
# own Unbound first, and through Docker's resolver (the router) only when
# Unbound does not answer. Run from the repo root against a stack that is up
# and healthy; it STOPS UNBOUND on purpose, so it is for CI and throwaway
# stacks, never a live box.
#
#   bash scripts/test-own-resolver.sh
#
# Why: the daemon's lookups (relay names such as fips1.iris.to) went to
# Docker's resolver and then the router, which on a real box points back at
# this box's own Pi-hole or at another box. A box's own DNS should not depend
# on the router or on another box when it carries its own recursive resolver.
set -uo pipefail
NVPN=${NVPN_CONTAINER:-sovereign-dns-nvpn}
UNBOUND=${UNBOUND_CONTAINER:-sovereign-dns-unbound}
UB=${UNBOUND_ADDR:-172.28.53.11}
fail=0
ok() { printf 'ok    %s\n' "$1"; }
bad() { printf 'FAIL  %s\n' "$1"; fail=1; }

# Did Unbound itself resolve <name>? Ask it without recursion: a name it has
# been asked before is answered from its cache (NXDOMAIN for a made-up label
# under a real domain), a name it has never seen is not.
unbound_knows() { docker exec "$NVPN" dig +norecurse +time=3 +tries=1 "@$UB" "$1" A 2>/dev/null | grep -q 'status: NXDOMAIN'; }
fresh() { echo "tomown-$RANDOM$RANDOM.example.com"; }

# 1. The resolver list: Unbound first, something else second.
ns=$(docker exec "$NVPN" sh -c "grep '^nameserver' /etc/resolv.conf" | awk '{print $2}')
echo "nvpn nameservers: $(echo $ns)"
[[ "$(echo "$ns" | sed -n 1p)" == "$UB" ]] && ok "Unbound ($UB) is the first nameserver" || bad "the first nameserver is '$(echo "$ns" | sed -n 1p)', not Unbound ($UB)"
[[ "$(echo "$ns" | wc -l | tr -d ' ')" -ge 2 ]] && ok "  with a second choice behind it" || bad "  and no second choice behind it"

# 2. A lookup made the way the daemon makes one reaches Unbound. The control
# first: a name nobody has looked up is not in Unbound's cache.
never=$(fresh)
unbound_knows "$never" && bad "control: Unbound claims to know $never before anyone asked" || ok "control: an unasked name is not in Unbound's cache"
name=$(fresh)
docker exec "$NVPN" getent hosts "$name" >/dev/null 2>&1
unbound_knows "$name" && ok "a lookup from the container reached Unbound" || bad "a lookup from the container never reached Unbound ($name)"

# 3. Unbound down: the lookup still works, quickly, through the second choice.
docker stop "$UNBOUND" >/dev/null || { echo "could not stop Unbound"; exit 2; }
start=$SECONDS
if docker exec "$NVPN" getent hosts example.com >/dev/null 2>&1; then
  took=$((SECONDS - start))
  [[ "$took" -le 15 ]] && ok "with Unbound stopped, a lookup falls back and answers (${took}s)" || bad "with Unbound stopped, the fallback took ${took}s"
else
  bad "with Unbound stopped, a lookup from the container fails: no fallback"
fi

# 4. Unbound back: lookups go to it again.
docker start "$UNBOUND" >/dev/null
h=""
for _ in $(seq 1 30); do
  h=$(docker inspect -f '{{.State.Health.Status}}' "$UNBOUND")
  [[ "$h" == healthy ]] && break
  sleep 2
done
[[ "$h" == healthy ]] && ok "Unbound is healthy again" || bad "Unbound is '$h' after starting again"
name=$(fresh)
docker exec "$NVPN" getent hosts "$name" >/dev/null 2>&1
unbound_knows "$name" && ok "and lookups reach it again" || bad "lookups do not return to Unbound after it comes back ($name)"

((fail == 0)) && echo "own resolver: pass" || echo "own resolver: FAIL"
exit "$fail"
