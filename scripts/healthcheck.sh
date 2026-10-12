#!/usr/bin/env bash
# Acceptance checks for a *running* sovereign-dns stack.
#
# Answers the question "is this thing actually doing what it claims?" rather
# than "are the containers up?". Every check here corresponds to a promise the
# stack makes: recursion instead of forwarding, real DNSSEC validation, real
# filtering, a resolver reachable only over the mesh, a control panel reachable
# only from this machine.
#
#   bash scripts/healthcheck.sh              # read-only, safe any time
#   bash scripts/healthcheck.sh --disruptive # also runs the leak test, which
#                                            # briefly stops Unbound
#
# Exits non-zero if any check fails, so it can gate a release or run in CI.
set -uo pipefail

DISRUPTIVE=0
[[ "${1:-}" == "--disruptive" ]] && DISRUPTIVE=1

NVPN=sovereign-dns-nvpn
PIHOLE=sovereign-dns-pihole
UNBOUND=sovereign-dns-unbound
WEB=sovereign-dns-web
UNBOUND_IP=172.28.53.11

PASS=0
FAIL=0
SKIP=0

ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; [[ -n "${2:-}" ]] && printf '        %s\n' "$2"; FAIL=$((FAIL + 1)); }
skip() { printf '  \033[33mSKIP\033[0m  %s\n' "$1"; SKIP=$((SKIP + 1)); }
head_() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# Query helper: runs dig inside the nvpn container, which is also Pi-hole's
# network namespace, so "the mesh IP" means the same thing here as it does to a
# real mesh client.
dig_() { docker exec "$NVPN" dig "$@" 2>/dev/null; }

# Runs a command with a deadline, so a stuck `docker exec` fails the check
# instead of hanging it. macOS has no coreutils `timeout`; perl's alarm does
# the same job there. Neither present means the command cannot run, which the
# caller reports as a failure.
with_timeout() {
  local secs=$1; shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$secs" "$@"
  else
    perl -e 'alarm shift; exec @ARGV' "$secs" "$@"
  fi
}

# Recursing from the root on a cold cache can take longer than dig's default
# timeout, so a freshly restarted Unbound will fail a single-shot query even
# though it is perfectly healthy. Retry before calling it a failure.
resolves_() {
  local server=$1 name=$2
  for _ in 1 2 3 4 5; do
    dig_ +short +time=5 +tries=1 "@$server" "$name" A | grep -qE '^[0-9]+\.' && return 0
    sleep 2
  done
  return 1
}

head_ "Containers"
for c in "$NVPN" "$WEB" "$PIHOLE" "$UNBOUND"; do
  state=$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null)
  if [[ "$state" == "running" ]]; then
    ok "$c is running"
  else
    bad "$c is not running" "state: ${state:-not found}"
  fi
done

if [[ $FAIL -gt 0 ]]; then
  printf '\nStack is not up; skipping the behavioural checks.\n'
  exit 1
fi

head_ "Layer 1 — Unbound: recursion, not forwarding"

# The whole sovereignty claim. A `forward-zone` or `stub-zone` anywhere in
# Unbound's config (a base image change, a stray mount, an edit) sends every
# query to a third party while everything else still looks perfectly healthy.
# So does a `root-hints:` file: our config uses Unbound's built-in hints, and
# any other file means recursing down from someone else's roots. Recursion can
# stay genuine and still leak: `dnstap-enable`/`dnstap-ip` stream every query
# name in cleartext to a collector, and `redis-server-host` points the cache at
# another machine. Neither belongs in this config, so both are red. Unbound's
# keywords are case-insensitive, so the match is too. Read the whole config
# directory, not one name, and refuse an include: that points outside it, since
# that file would forward just the same without being read here. The include
# path itself stays case-sensitive: /ETC/UNBOUND is a different place.
# Any symlink under /etc/unbound fails outright: a symlinked directory makes an
# include look inside while the file lives anywhere. The symlink check runs
# first so the dereferencing grep -R below can never meet a loop, and -R stays
# so a loosened symlink check still cannot hide a file from the grep.
# Fails closed: stderr is kept, the inner script must exit 0, and a timeout
# bounds the exec, so a container that cannot exec, a grep that cannot read or
# an exec that hangs reports as a failure rather than as "no forwarder found".
forwarders=$(with_timeout 20 docker exec "$UNBOUND" sh -c '
  test -r /etc/unbound/unbound.conf || { echo "cannot read /etc/unbound/unbound.conf"; exit 0; }
  links=$(find /etc/unbound -type l 2>&1) || { echo "find could not read /etc/unbound: $links"; exit 0; }
  [ -z "$links" ] || { echo "symlink in /etc/unbound:" $links; exit 0; }
  grep -RhiE "^[[:space:]]*((forward|stub)-(zone|addr|host)|root-hints|dnstap-(enable|ip)|redis-server-host):" /etc/unbound/; [ $? -le 1 ] || echo "grep could not read /etc/unbound"
  grep -RhiE "^[[:space:]]*include(-toplevel)?:" /etc/unbound/ | grep -vE "[Ii][Nn][Cc][Ll][Uu][Dd][Ee](-[Tt][Oo][Pp][Ll][Ee][Vv][Ee][Ll])?:[[:space:]]*\"?/etc/unbound/"
  exit 0
  ' 2>&1)
rc=$?
if [[ $rc -ne 0 ]]; then
  bad "could not inspect Unbound's config (exit $rc — timed out or exec failed)" \
      "$(echo "$forwarders" | tr '\n' ' ')"
elif [[ -z "$forwarders" ]]; then
  ok "no forward-zone, stub-zone, root-hints, dnstap or redis cache configured (Unbound recurses from the root)"
else
  bad "Unbound config sends lookups off the box (forwarder, foreign roots, dnstap or remote cache)" \
      "$(echo "$forwarders" | tr '\n' ' ')"
fi

# Independent confirmation: ask an authoritative nameserver which resolver is
# talking to it. Recursing means it sees this machine's own address; forwarding
# means it sees the forwarder's. Informational — the answer varies by ISP, so
# it prints rather than asserts.
egress=$(docker exec "$UNBOUND" sh -lc \
  'drill @127.0.0.1 o-o.myaddr.l.google.com TXT 2>/dev/null | awk "/^o-o.myaddr/ {print \$5}"' 2>/dev/null | tr -d '"')
if [[ -n "$egress" ]]; then
  printf '  \033[36mINFO\033[0m  resolver egress seen by an authoritative NS: %s\n' "$egress"
  printf '        (should be your own public IP — a Cloudflare/Google IP means forwarding)\n'
fi

if resolves_ "$UNBOUND_IP" example.com; then
  ok "Unbound resolves from the root servers"
else
  bad "Unbound did not resolve example.com"
fi

head_ "Layer 1 — DNSSEC validation"

if dig_ +dnssec "@$UNBOUND_IP" cloudflare.com A | grep -q 'flags:.* ad'; then
  ok "signed domain validates (ad flag set)"
else
  bad "no ad flag on a signed domain — DNSSEC validation is not working"
fi

# The negative case matters more than the positive one: a resolver that returns
# answers for deliberately-broken signatures is not validating at all.
if dig_ "@$UNBOUND_IP" dnssec-failed.org A | grep -q 'status: SERVFAIL'; then
  ok "deliberately-broken signature is rejected (SERVFAIL)"
else
  bad "dnssec-failed.org was answered — bad signatures are NOT being rejected"
fi

head_ "Layer 2 — Pi-hole: filtering and upstream chain"

# Pi-hole runs inside nvpn's network namespace, and that binding does not
# survive nvpn being restarted or recreated. When it breaks, Pi-hole is left in
# a namespace with nothing but loopback: it still reports healthy, still
# answers on 127.0.0.1, and DNS is dead for every real client. Comparing the
# namespaces catches it directly rather than inferring it from symptoms.
nvpn_ns=$(docker exec "$NVPN" sh -lc 'readlink /proc/self/ns/net' 2>/dev/null)
pihole_ns=$(docker exec "$PIHOLE" sh -lc 'readlink /proc/self/ns/net' 2>/dev/null)
if [[ -n "$nvpn_ns" && "$nvpn_ns" == "$pihole_ns" ]]; then
  ok "Pi-hole shares nvpn's network namespace"
else
  bad "Pi-hole is orphaned from nvpn's network namespace — DNS is down for every client" \
      "recreate it: docker compose up -d --force-recreate pihole"
fi

MESH_IP=$(docker exec "$NVPN" sh -lc \
  "ip -4 -o addr show utun100 2>/dev/null | awk '{print \$4}' | cut -d/ -f1" 2>/dev/null)
if [[ -z "$MESH_IP" ]]; then
  bad "no mesh IP on utun100 — the tunnel is not up"
  MESH_IP=""
else
  ok "mesh IP assigned: $MESH_IP"
fi

if [[ -n "$MESH_IP" ]]; then
  if resolves_ "$MESH_IP" example.com; then
    ok "Pi-hole answers on the mesh address"
  else
    bad "Pi-hole did not answer on $MESH_IP — mesh clients cannot resolve"
  fi

  blocked=$(dig_ +short "@$MESH_IP" doubleclick.net A | head -1)
  if [[ "$blocked" == "0.0.0.0" || "$blocked" == "::" || -z "$blocked" ]]; then
    ok "known ad domain is blocked (doubleclick.net -> ${blocked:-NXDOMAIN})"
  else
    bad "doubleclick.net resolved to $blocked — filtering is not active"
  fi

  # DNSSEC has to survive the hop through Pi-hole, not just work in isolation.
  if dig_ +dnssec "@$MESH_IP" cloudflare.com A | grep -q 'flags:.* ad'; then
    ok "DNSSEC survives the full chain (client -> Pi-hole -> Unbound)"
  else
    bad "ad flag lost through Pi-hole — validation is not reaching clients"
  fi
fi

upstreams=$(docker exec "$PIHOLE" sh -lc \
  'sed -n "/^\s*upstreams\s*=/,/]/p" /etc/pihole/pihole.toml' 2>/dev/null)
if grep -q "$UNBOUND_IP" <<<"$upstreams" && ! grep -qE '"(8\.8\.|1\.1\.1|9\.9\.9|208\.67)' <<<"$upstreams"; then
  ok "Pi-hole's only upstream is Unbound"
else
  bad "Pi-hole has an upstream other than Unbound" "$(echo "$upstreams" | tr -d '\n ')"
fi

# Port 53 must be bound to the mesh interface only. A wildcard bind would make
# this an open resolver to anything that can reach the host.
# Field position for the local address differs between ss invocations (the
# Netid column only appears when more than one protocol is selected), so scan
# every field rather than assuming a column.
listeners=$(docker exec "$NVPN" sh -lc \
  'ss -lnu 2>/dev/null | awk "{for (i = 1; i <= NF; i++) if (\$i ~ /:53\$/) print \$i}"' 2>/dev/null)
if grep -q '0\.0\.0\.0:53' <<<"$listeners"; then
  bad "Pi-hole is listening on 0.0.0.0:53 — it is an open resolver" "$(echo "$listeners" | tr '\n' ' ')"
elif grep -q "${MESH_IP}:53" <<<"$listeners"; then
  ok "port 53 is bound to the mesh address only"
else
  bad "port 53 is not bound to the mesh address" "$(echo "$listeners" | tr '\n' ' ')"
fi

head_ "Layer 3 — NostrVPN: network state and override"

status=$(docker exec "$NVPN" nvpn status --json --discover-secs 0 2>/dev/null)

if grep -q '"running": true' <<<"$status"; then
  ok "daemon is running"
else
  bad "daemon is not running"
fi

if grep -q '"override": true' <<<"$status"; then
  ok "network DNS override is on"
else
  bad "network DNS override is off — members will use their own resolvers"
fi

if grep -q '"role": "nameserver"' <<<"$status"; then
  ok "this node is the network's nameserver"
else
  bad "this node is not the network's DNS node"
fi

# A device only actually gets sovereign DNS if the mesh can carry packets to
# it. The daemon's own reachability count is the honest measure here: a peer
# can be a fully approved roster member and still have no working path, which
# looks identical to "working" from the container's side. Parsed from the text
# status rather than the JSON, which repeats each peer under several keys.
peer_line=$(docker exec "$NVPN" nvpn status --discover-secs 0 2>/dev/null | grep -E '^peers: ')
peer_total=$(grep -oE '[0-9]+ total' <<<"$peer_line" | cut -d' ' -f1)
peer_reachable=$(grep -oE '[0-9]+ reachable' <<<"$peer_line" | cut -d' ' -f1)
if [[ -z "${peer_total:-}" || "$peer_total" == "0" ]]; then
  skip "no other devices in the network yet (nothing to propagate to)"
elif [[ "$peer_reachable" == "$peer_total" ]]; then
  ok "all $peer_total member device(s) reachable over the mesh"
else
  bad "only ${peer_reachable}/${peer_total} member devices are reachable over the mesh" \
      "unreachable devices cannot use this resolver — check that their VPN is connected"
  docker exec "$NVPN" nvpn status --discover-secs 0 2>/dev/null \
    | grep -E '^\s+[✓✗]' | sed 's/^/        /'
fi

head_ "Control panel exposure"

ports=$(docker port "$WEB" 2>/dev/null)
if grep -qE '^\s*8081/tcp -> 127\.0\.0\.1:' <<<"$ports"; then
  ok "panel is published to localhost only"
elif [[ -z "$ports" ]]; then
  bad "panel has no published port"
else
  bad "panel is published beyond localhost — it has NO login of its own" "$(echo "$ports" | tr '\n' ' ')"
fi

web_port=$(grep -oE '127\.0\.0\.1:[0-9]+' <<<"$ports" | head -1 | cut -d: -f2)
if [[ -n "$web_port" ]]; then
  if [[ "$(curl -s -m 5 "http://127.0.0.1:${web_port}/api/health" 2>/dev/null)" == "ok" ]]; then
    ok "panel is serving live daemon data (port $web_port)"
  else
    bad "panel is not answering on 127.0.0.1:${web_port}"
  fi
fi

if [[ $DISRUPTIVE -eq 1 && -n "$MESH_IP" ]]; then
  head_ "Leak test (disruptive)"

  # With the resolver dead, Pi-hole must fail rather than answer from anywhere
  # else. An answer here means queries are escaping to some other upstream --
  # the failure mode this design exists to prevent, and one that is invisible
  # in normal operation because everything still "works".
  docker stop "$UNBOUND" >/dev/null 2>&1
  sleep 2
  probe="leak-probe-$$.example.com"
  # `dig +short` prints its "communications error ... timed out" notice on
  # stdout, so emptiness is not the test — an actual A record is.
  leak=$(dig_ +short +time=3 +tries=1 "@$MESH_IP" "$probe" A | grep -E '^[0-9]+\.[0-9.]+$' | head -1)
  fresh=$(dig_ +short +time=3 +tries=1 "@$MESH_IP" wikipedia.org A | grep -E '^[0-9]+\.[0-9.]+$' | head -1)
  docker start "$UNBOUND" >/dev/null 2>&1

  if [[ -z "$leak" && -z "$fresh" ]]; then
    ok "resolver down -> no answers (fails closed, no leak to another upstream)"
  else
    bad "got answers with Unbound stopped — queries are leaking to another upstream" \
        "probe=${leak:-none} wikipedia.org=${fresh:-none}"
  fi

  printf '  waiting for Unbound to come back...\n'
  for _ in $(seq 1 20); do
    dig_ +short "@$UNBOUND_IP" example.com A | grep -qE '^[0-9]+\.' && break
    sleep 2
  done
  if resolves_ "$MESH_IP" example.com; then
    ok "resolution recovered after Unbound restarted"
  else
    bad "resolution did NOT recover — run: docker compose up -d"
  fi
else
  skip "leak test (re-run with --disruptive to include it)"
fi

head_ "Result"
printf '  %d passed, %d failed, %d skipped\n\n' "$PASS" "$FAIL" "$SKIP"
[[ $FAIL -eq 0 ]]
