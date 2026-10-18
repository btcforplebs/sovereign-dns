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

# Which resolver did a lookup go to? Count packets the container sends to each
# with two counting-only iptables rules (no target, so they change nothing).
# Unbound's cache cannot say: it refuses non-recursive queries unless
# configured to be snooped. The controls below show the counters can tell the
# two apart.
count_rule() { docker exec "$NVPN" iptables "$1" OUTPUT -p udp -d "$2" -m comment --comment "$3" 2>/dev/null; }
pkts() { docker exec "$NVPN" sh -c "iptables -nvxL OUTPUT | awk '/$1/ { print \$1; exit }'"; }
trap 'count_rule -D "$UB" tomown-ub; count_rule -D 127.0.0.11 tomown-dkr' EXIT
count_rule -I "$UB" tomown-ub && count_rule -I 127.0.0.11 tomown-dkr || { echo "no iptables counters in $NVPN"; exit 2; }
fresh() { echo "tomown-$RANDOM$RANDOM.example.com"; }
# lookup_hits <ub|dkr> <command...>: how many packets that command sent there.
hits() { local tag=$1 before after; shift; before=$(pkts "tomown-$tag"); docker exec "$NVPN" "$@" >/dev/null 2>&1; after=$(pkts "tomown-$tag"); echo $((after - before)); }

# 1. The resolver list: Unbound first, something else second.
ns=$(docker exec "$NVPN" sh -c "grep '^nameserver' /etc/resolv.conf" | awk '{print $2}')
echo "nvpn nameservers: $(echo $ns)"
[[ "$(echo "$ns" | sed -n 1p)" == "$UB" ]] && ok "Unbound ($UB) is the first nameserver" || bad "the first nameserver is '$(echo "$ns" | sed -n 1p)', not Unbound ($UB)"
[[ "$(echo "$ns" | wc -l | tr -d ' ')" -ge 2 ]] && ok "  with a second choice behind it" || bad "  and no second choice behind it"

# 2. A lookup made the way the daemon makes one (the system resolver) goes to
# Unbound and not to Docker's. Controls first: asking Docker's resolver
# directly moves only its counter, asking Unbound directly moves only Unbound's.
[[ "$(hits dkr dig +time=3 +tries=1 @127.0.0.11 "$(fresh)" A)" -ge 1 ]] && ok "control: a query to Docker's resolver is counted there" || bad "control: a query to Docker's resolver is not counted"
[[ "$(hits ub dig +time=3 +tries=1 @127.0.0.11 "$(fresh)" A)" -eq 0 ]] && ok "control: ...and not counted as Unbound" || bad "control: a query to Docker's resolver was counted as Unbound"
[[ "$(hits ub dig +time=3 +tries=1 "@$UB" "$(fresh)" A)" -ge 1 ]] && ok "control: a query to Unbound is counted there" || bad "control: a query to Unbound is not counted"
name=$(fresh)
u=$(pkts tomown-ub); d=$(pkts tomown-dkr)
docker exec "$NVPN" getent hosts "$name" >/dev/null 2>&1
u2=$(pkts tomown-ub); d2=$(pkts tomown-dkr)
[[ $((u2 - u)) -ge 1 ]] && ok "a lookup from the container goes to Unbound" || bad "a lookup from the container never went to Unbound ($name)"
[[ $((d2 - d)) -eq 0 ]] && ok "  and does not touch Docker's resolver" || bad "  but it also went to Docker's resolver ($((d2 - d)) packets)"

# 3. Unbound down: the lookup still works, quickly, through the second choice.
docker stop "$UNBOUND" >/dev/null || { echo "could not stop Unbound"; exit 2; }
d=$(pkts tomown-dkr)
start=$SECONDS
if docker exec "$NVPN" getent hosts example.com >/dev/null 2>&1; then
  took=$((SECONDS - start))
  [[ "$took" -le 15 ]] && ok "with Unbound stopped, a lookup falls back and answers (${took}s)" || bad "with Unbound stopped, the fallback took ${took}s"
else
  bad "with Unbound stopped, a lookup from the container fails: no fallback"
fi
[[ $(( $(pkts tomown-dkr) - d )) -ge 1 ]] && ok "  and it was Docker's resolver that answered" || bad "  but Docker's resolver was never asked"

# 4. Unbound back: lookups go to it again.
docker start "$UNBOUND" >/dev/null
h=""
for _ in $(seq 1 30); do
  h=$(docker inspect -f '{{.State.Health.Status}}' "$UNBOUND")
  [[ "$h" == healthy ]] && break
  sleep 2
done
[[ "$h" == healthy ]] && ok "Unbound is healthy again" || bad "Unbound is '$h' after starting again"
u=$(pkts tomown-ub)
docker exec "$NVPN" getent hosts "$(fresh)" >/dev/null 2>&1
[[ $(( $(pkts tomown-ub) - u )) -ge 1 ]] && ok "and lookups go to it again" || bad "lookups do not return to Unbound after it comes back"

((fail == 0)) && echo "own resolver: pass" || echo "own resolver: FAIL"
exit "$fail"
