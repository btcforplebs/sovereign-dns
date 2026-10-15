#!/usr/bin/env bash
# End to end: a second DNS box joins the first one through both boxes' setup
# pages (hub/boxjoin.go), with nothing typed into a terminal.
#
#   bash scripts/test-second-box-e2e.sh
#
# Brings up two throwaway boxes in one compose project (sdns-boxtest: its own
# containers, volumes and subnet, nothing published on the host). Each is an
# nvpn daemon, its control panel and its hub, and each founds its own network
# on first start, as a real install does. Then, the way a person would:
#   - box B's setup refuses a bad code and its own code
#   - box B's setup takes box A's join code and asks to join
#   - box A's setup takes box B's ID and adds it
#   - box B reads "You're set", and each box sees the other online
# Peers find each other the way a real install does (public FIPS seeds, on by
# default), so this needs the internet. Uses the ready-made nvpn image
# (docker-compose.prebuilt.yml), so nothing is compiled but the hub. Safe next
# to a running box. Tears itself down on exit; KEEP=1 leaves it up.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

P=sdns-boxtest
NET=172.31.251
tmp=$(mktemp -d)
ovr=$tmp/override.yml

cat >"$ovr" <<EOF
services:
  nvpn:
    container_name: !reset null
    ports: !reset []
    networks:
      dns-internal:
        ipv4_address: $NET.10
  web:
    container_name: !reset null
    ports: !reset []
  hub:
    container_name: !reset null
    ports: !reset []
    depends_on: !override
      web:
        condition: service_started
    environment:
      PANEL_API: http://web:8081
    networks:
      default:
      dns-internal:
        ipv4_address: $NET.12
  hub-mesh:
    container_name: !reset null
  nvpn-b:
    image: \${NVPN_IMAGE}
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun:/dev/net/tun"]
    volumes: ["nvpn-b-config:/root/.config/nvpn"]
    healthcheck:
      test: ["CMD-SHELL", "nvpn status >/dev/null 2>&1"]
      interval: 5s
      retries: 20
    networks:
      dns-internal:
        ipv4_address: $NET.20
  web-b:
    image: \${NVPN_IMAGE}
    entrypoint: ["/usr/local/bin/nvpn-web"]
    command: [--listen, 0.0.0.0:8081, --behind-trusted-proxy, --config, /root/.config/nvpn/config.toml]
    volumes: ["nvpn-b-config:/root/.config/nvpn"]
    environment:
      NVPN_CLI_PATH: /usr/local/bin/nvpn
      NVPN_EXTERNAL_DAEMON: "true"
      NVPN_DAEMON_STATUS_MODE: state-file
    depends_on:
      nvpn-b:
        condition: service_healthy
  hub-b:
    build:
      context: ./hub
    environment:
      PANEL_API: http://web-b:8081
    depends_on:
      web-b:
        condition: service_started
volumes:
  nvpn-b-config: {}
networks:
  dns-internal:
    ipam:
      config: !override
        - subnet: $NET.0/24
EOF

NVPN_IMAGE=$(sed -nE 's/^[[:space:]]*image:[[:space:]]*//p' docker-compose.prebuilt.yml | head -n1)
export NVPN_IMAGE PIHOLE_PASSWORD=unused-$RANDOM$RANDOM
C=(docker compose -p "$P" -f docker-compose.yml -f docker-compose.prebuilt.yml -f "$ovr")

pass=0 failn=0
ok() { pass=$((pass + 1)); printf 'ok    %s\n' "$1"; }
bad() { failn=$((failn + 1)); printf 'FAIL  %s\n' "$1"; }
die() { printf 'e2e: %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [[ "${KEEP:-0}" == 1 ]]; then echo "KEEP=1: left $P up. Remove: ${C[*]} down -v"; return; fi
  "${C[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  docker image rm "$P-hub" "$P-hub-b" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

# The hubs have no shell or curl, so their pages are read from the panels'
# containers, which have both. Host and Origin are what a browser on the box
# itself sends; the hub's form guards need them.
hub() { # hub <hub service> <panel service> <path> [curl args...]
  local h=$1 from=$2 path=$3; shift 3
  "${C[@]}" exec -T "$from" curl -s --max-time 25 -H 'Host: localhost:8080' -H 'Origin: http://localhost:8080' "$@" "http://$h:8080$path" || true
}
csrf() { hub "$1" "$2" /dashboard | sed -nE 's/.*name="csrf" value="([0-9a-f]+)".*/\1/p' | head -n1; }
tick() { "${C[@]}" exec -T "$1" curl -s --max-time 10 -X POST "http://$1:8081/api/tick" || true; }
own_npub() { tick "$1" | sed -nE 's/.*"ownNpub":"(npub1[0-9a-z]+)".*/\1/p'; }

echo "e2e: starting two throwaway boxes ($P, $NET.0/24)"
"${C[@]}" up -d --build nvpn web hub nvpn-b web-b hub-b >/dev/null 2>&1 \
  || { "${C[@]}" up -d --build nvpn web hub nvpn-b web-b hub-b; die "compose up failed"; }
for _ in $(seq 1 60); do
  [[ -n "$(csrf hub web)" && -n "$(csrf hub-b web-b)" ]] && break
  sleep 2
done
A=$(own_npub web)
B=$(own_npub web-b)
[[ "$A" == npub1* && "$B" == npub1* && "$A" != "$B" ]] || die "could not read both boxes' IDs (A '$A', B '$B')"
echo "e2e: box A $A, box B $B"

TA=$(csrf hub web)
TB=$(csrf hub-b web-b)
CODE=$(hub hub web "/setup?step=3&kind=box" -b "sdns_setup_role=first" | sed -nE 's/.*<pre class="copy" id="join-code">([^<]+)<\/pre>.*/\1/p' | head -n1)
[[ "$CODE" == "$A:"* ]] && ok "box A's setup shows its join code ($CODE)" || bad "box A's setup join code: '$CODE', want $A:<network>"
NETA=${CODE#*:}

page=$(hub hub-b web-b "/setup?step=3" -b "sdns_setup_role=second")
grep -q 'action="/setup/join"' <<<"$page" && ok "box B's setup, as another box, asks for the first box's code" || bad "box B's step 3 has no join form"

r=$(hub hub-b web-b /setup/join -o /dev/null -w '%{http_code}' --data-urlencode "csrf=$TB" --data-urlencode "code=$B:abcd1234")
[[ "$r" == 400 ]] && ok "  ...refuses its own ID as the code (400)" || bad "  ...own code: $r, want 400"
r=$(hub hub-b web-b /setup/join -o /dev/null -w '%{http_code}' --data-urlencode "csrf=$TB" --data-urlencode "code=not a code")
[[ "$r" == 400 ]] && ok "  ...refuses a bad code (400)" || bad "  ...bad code: $r, want 400"
r=$(hub hub-b web-b /setup/join -o /dev/null -w '%{http_code}' --data-urlencode "csrf=wrong" --data-urlencode "code=$CODE")
[[ "$r" == 403 ]] && ok "  ...refuses a form without its token (403)" || bad "  ...no token: $r, want 403"
tick web-b | grep -q "\"networkId\":\"$NETA\"" && bad "  ...yet one of those changed its network" || ok "  ...and none of them changed anything"

r=$(hub hub-b web-b /setup/join -o /dev/null -w '%{http_code} %{redirect_url}' --data-urlencode "csrf=$TB" --data-urlencode "code=$CODE")
[[ "$r" == "303 "*"/setup?step=4" ]] && ok "box B joins with box A's code ($r)" || bad "box B join: '$r', want 303 to step 4"
page=$(hub hub-b web-b "/setup?step=4")
grep -q "Let the first box add it" <<<"$page" && grep -q "$B" <<<"$page" && ! grep -q "You're set" <<<"$page" \
  && ok "  ...and waits, showing its own ID to paste into box A" || bad "  ...box B's waiting step is wrong"
r=$(hub hub-b web-b / -o /dev/null -w '%{http_code} %{redirect_url}')
[[ "$r" == "302 "*"/setup" ]] && ok "  ...its / still leads to setup while it waits" || bad "  .../ while waiting: '$r'"

r=$(hub hub web /devices/add-box -o /dev/null -w '%{http_code} %{redirect_url}' --data-urlencode "csrf=$TA" --data-urlencode "box=$B" --data-urlencode "name=box B" --data-urlencode "from=setup")
[[ "$r" == "303 "*"/setup?step=3" ]] && ok "box A adds box B by its ID ($r)" || bad "box A add-box: '$r', want 303 to step 3"

done_b=""
for _ in $(seq 1 45); do
  page=$(hub hub-b web-b "/setup?step=4")
  grep -q "You're set" <<<"$page" && { done_b=1; break; }
  sleep 2
done
[[ -n "$done_b" ]] && ok "box B's setup reads \"You're set\"" || bad "box B never read \"You're set\""

seen=""
for _ in $(seq 1 45); do
  a=$(tick web) b=$(tick web-b)
  python3 - "$A" "$B" "$a" "$b" <<'PY' && { seen=1; break; }
import json, sys
A, B, a, b = sys.argv[1:5]
def online(state, other):
    d = json.loads(state)
    net = next(n for n in d["networks"] if n["networkId"] == d["networkId"])
    return any(p["npub"] == other and p["state"] == "online" for p in net["participants"])
sys.exit(0 if online(a, B) and online(b, A) else 1)
PY
  sleep 2
done
[[ -n "$seen" ]] && ok "each box sees the other online on box A's network" || bad "the boxes never saw each other online"
r=$(hub hub-b web-b / -o /dev/null -w '%{http_code}')
[[ "$r" == 200 ]] && ok "box B's / is now its dashboard" || bad "box B / after joining: $r"
page=$(hub hub-b web-b /dashboard)
grep -q 'action="/devices/add-box"' <<<"$page" && bad "box B offers a join code of its own (it can't approve anyone)" || ok "box B hands out no join code of its own"

printf '\n%d passed, %d failed\n' "$pass" "$failn"
[[ "$failn" == 0 ]]
