#!/usr/bin/env bash
# End to end: the dashboard and Pi-hole, opened from another device over
# NostrVPN (scripts/hub-mesh.sh, hub/mesh.go).
#
#   bash scripts/test-hub-mesh-e2e.sh
#
# Brings up a throwaway copy of this stack (compose project sdns-meshtest:
# its own containers, volumes and subnet, nothing published on the host) plus
# a "phone", a second nvpn node that joins the box's network over WireGuard.
# Then, from the phone, at the box's mesh address:
#   - :8080 asks for the dashboard password, and signs in with it
#   - /admin is Pi-hole's own login
#   - the same ports at the box's Docker address are refused
# and, with HUB_MESH=off, that :8080 opens nothing.
#
# Needs Docker with compose 2.24 or later. Uses the ready-made nvpn image
# (docker-compose.prebuilt.yml), so nothing is compiled but the hub. Safe next
# to a running box: different names, different subnet, no host ports. Tears
# itself down on exit; KEEP=1 leaves it up to poke at.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

P=sdns-meshtest
NET=172.31.253
HUB_PW=mesh-e2e-$RANDOM$RANDOM
PIHOLE_PW=pihole-e2e-$RANDOM$RANDOM
tmp=$(mktemp -d)
ovr=$tmp/override.yml

cat >"$ovr" <<EOF
services:
  nvpn:
    container_name: !reset null
    ports: !reset []
    environment:
      NVPN_FIPS_BOOTSTRAP: "off"
    networks:
      dns-internal:
        ipv4_address: $NET.10
  web:
    container_name: !reset null
    ports: !reset []
  pihole:
    container_name: !reset null
    environment:
      FTLCONF_dns_upstreams: "$NET.11#53"
  unbound:
    container_name: !reset null
    networks:
      dns-internal:
        ipv4_address: $NET.11
  hub:
    container_name: !reset null
    ports: !reset []
    environment:
      PANEL_API: http://web:8081
      DOCS_ADDR: docs:80
      PIHOLE_ADDR: "$NET.10:80"
      UNBOUND_ADDR: "$NET.11:53"
      HUB_MESH_GATE: "$NET.10"
    networks:
      dns-internal:
        ipv4_address: $NET.12
  hub-mesh:
    container_name: !reset null
    environment:
      HUB_MESH_TARGET: "$NET.12:8090"
  docs:
    container_name: !reset null
    ports: !reset []
  phone:
    image: \${NVPN_IMAGE}
    entrypoint: ["sleep", "infinity"]
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun:/dev/net/tun"]
    networks:
      dns-internal:
        ipv4_address: $NET.20
networks:
  dns-internal:
    ipam:
      config: !override
        - subnet: $NET.0/24
EOF

NVPN_IMAGE=$(sed -nE 's/^[[:space:]]*image:[[:space:]]*//p' docker-compose.prebuilt.yml | head -n1)
export NVPN_IMAGE PIHOLE_PASSWORD=$PIHOLE_PW HUB_PASSWORD=$HUB_PW HUB_MESH=on
C=(docker compose -p "$P" -f docker-compose.yml -f docker-compose.prebuilt.yml -f "$ovr")

pass=0 failn=0
ok() { pass=$((pass + 1)); printf 'ok    %s\n' "$1"; }
bad() { failn=$((failn + 1)); printf 'FAIL  %s\n' "$1"; }
die() { printf 'e2e: %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [[ "${KEEP:-0}" == 1 ]]; then echo "KEEP=1: left $P up. Remove: ${C[*]} down -v"; return; fi
  "${C[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  docker image rm "$P-hub" "$P-hub-mesh" "$P-unbound" "$P-docs" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

npub_of() {
  "${C[@]}" exec -T "$1" awk '/^\[nostr\]$/ { n = 1; next } /^\[/ { n = 0 }
    n && /^public_key[[:space:]]*=/ { gsub(/"/, "", $3); print $3; exit }' /root/.config/nvpn/config.toml
}

echo "e2e: starting the throwaway box ($P, $NET.0/24)"
"${C[@]}" up -d --build nvpn web pihole unbound hub hub-mesh phone >/dev/null 2>&1 \
  || { "${C[@]}" up -d --build nvpn web pihole unbound hub hub-mesh phone; die "compose up failed"; }

BOX=$(npub_of nvpn)
NETWORK_ID=$("${C[@]}" exec -T nvpn awk '/^\[\[networks\]\]/ { n = 1; next } /^\[/ { n = 0 }
  n && /^network_id[[:space:]]*=/ { gsub(/"/, "", $3); print $3; exit }' /root/.config/nvpn/config.toml)
"${C[@]}" exec -T phone nvpn init --force >/dev/null
PHONE=$(npub_of phone)
[[ -n "$BOX" && -n "$PHONE" && -n "$NETWORK_ID" ]] || die "could not read the box's and phone's identities"

# The box keeps the network its entrypoint founded; the phone joins it. Peers
# find each other by address, not over relays, like nostr-vpn's own docker e2e.
"${C[@]}" exec -T nvpn nvpn set --participant "$BOX" --participant "$PHONE" \
  --endpoint "$NET.10:51820" --fips-advertise-endpoint true --fips-nostr-discovery-enabled false \
  --fips-peer-endpoint "$PHONE=$NET.20:51820" >/dev/null
"${C[@]}" exec -T nvpn nvpn reload >/dev/null
"${C[@]}" exec -T phone nvpn set --network-id "$NETWORK_ID" --participant "$BOX" --participant "$PHONE" \
  --endpoint "$NET.20:51820" --listen-port 51820 --fips-advertise-endpoint true \
  --fips-nostr-discovery-enabled false --fips-bootstrap-enabled false \
  --fips-peer-endpoint "$BOX=$NET.10:51820" >/dev/null
"${C[@]}" exec -T phone nvpn add-admin --device "$BOX" >/dev/null
"${C[@]}" exec -T phone sh -c 'nvpn start --daemon --connect >/tmp/start.log 2>&1'

MESH_IP=""
for _ in $(seq 1 60); do
  MESH_IP=$("${C[@]}" exec -T nvpn nvpn ip 2>/dev/null | tr -d '\r' | sed 's|/.*||')
  [[ "$MESH_IP" == 10.44.* ]] && "${C[@]}" exec -T phone ping -c1 -W1 "$MESH_IP" >/dev/null 2>&1 && break
  sleep 2
done
"${C[@]}" exec -T phone ping -c1 -W2 "$MESH_IP" >/dev/null 2>&1 || die "the phone never reached the box over the mesh (box mesh IP '$MESH_IP')"
PHONE_IP=$("${C[@]}" exec -T phone nvpn ip | tr -d '\r' | sed 's|/.*||')
echo "e2e: phone $PHONE_IP reaches the box at $MESH_IP over the mesh"

# The gate shares nvpn's namespace; wait for it and Pi-hole to come up there.
for _ in $(seq 1 60); do
  "${C[@]}" exec -T phone curl -s -o /dev/null --max-time 3 "http://$MESH_IP:8080/login" \
    && "${C[@]}" exec -T phone curl -s -o /dev/null --max-time 3 "http://$MESH_IP/admin/login" && break
  sleep 2
done

# From the phone. curl -w prints the status; -c/-b keep the session.
phone_curl() { "${C[@]}" exec -T phone curl -s -o /dev/null --max-time 5 -w '%{http_code} %{redirect_url}' "$@" || true; }

r=$(phone_curl "http://$MESH_IP:8080/")
[[ "$r" == "303 http://$MESH_IP:8080/login"* ]] && ok "phone -> http://$MESH_IP:8080/ asks for the dashboard password ($r)" || bad "phone -> :8080/ without signing in: '$r', want 303 to /login"
r=$(phone_curl "http://$MESH_IP:8080/api/status")
[[ "$r" == "401 "* ]] && ok "  ...the status API too (401)" || bad "  ...status API signed out: '$r', want 401"
r=$(phone_curl -X POST -d "password=wrong-password" -H "Origin: http://$MESH_IP:8080" "http://$MESH_IP:8080/login")
[[ "$r" == "401 "* ]] && ok "  ...a wrong password is refused (401)" || bad "  ...wrong password: '$r', want 401"
r=$(phone_curl -c /tmp/jar -X POST --data-urlencode "password=$HUB_PW" -d next=/dashboard -H "Origin: http://$MESH_IP:8080" "http://$MESH_IP:8080/login")
[[ "$r" == "303 http://$MESH_IP:8080/dashboard" ]] && ok "  ...the dashboard password signs in ($r)" || bad "  ...signing in: '$r', want 303 to /dashboard"
body=$("${C[@]}" exec -T phone curl -s --max-time 5 -b /tmp/jar "http://$MESH_IP:8080/dashboard" || true)
grep -q "Network DNS" <<<"$body" && ok "  ...and the dashboard opens" || bad "  ...dashboard after sign-in did not render"
grep -q "http://$MESH_IP/admin" <<<"$body" && ok "  ...with its Pi-hole link at the mesh address" || bad "  ...dashboard has no Pi-hole link to http://$MESH_IP/admin"
grep -q "http://$MESH_IP:8080" <<<"$body" && ok "  ...and its own phone address" || bad "  ...dashboard does not show http://$MESH_IP:8080"
"${C[@]}" logs hub 2>&1 | grep -q "wrong hub password from $PHONE_IP" \
  && ok "  ...the hub sees the phone's own mesh address ($PHONE_IP), not the gate's" \
  || bad "  ...hub log does not name the phone ($PHONE_IP) for the wrong password"

r=$(phone_curl "http://$MESH_IP/admin/")
[[ "$r" == 30[12]" "*"/admin/login"* || "$r" == "200 "* ]] && ok "phone -> http://$MESH_IP/admin is Pi-hole ($r)" || bad "phone -> /admin: '$r'"
body=$("${C[@]}" exec -T phone curl -sL --max-time 5 "http://$MESH_IP/admin/login" || true)
grep -qi "pi-hole" <<<"$body" && ok "  ...its login page" || bad "  .../admin/login is not Pi-hole's page"
r=$("${C[@]}" exec -T phone curl -s --max-time 5 -X POST -H 'Content-Type: application/json' -d '{"password":"nope"}' "http://$MESH_IP/api/auth" || true)
grep -q '"valid":false' <<<"$r" && ok "  ...and it refuses a wrong Pi-hole password" || bad "  ...Pi-hole auth with a wrong password: '$r'"

# Not from outside the mesh: the same ports at the box's Docker address.
r=$(phone_curl "http://$NET.10:8080/")
[[ "$r" == "403 "* ]] && ok "phone -> $NET.10:8080 (not the mesh) is refused (403)" || bad "phone -> gate off-mesh: '$r', want 403"
r=$(phone_curl "http://$NET.12:8090/")
[[ "$r" == "403 "* ]] && ok "phone -> the hub's mesh port directly is refused (403)" || bad "phone -> hub :8090 directly: '$r', want 403"
r=$(phone_curl -H "X-Forwarded-For: $PHONE_IP" "http://$NET.12:8090/")
[[ "$r" == "403 "* ]] && ok "  ...even naming a mesh device (403)" || bad "  ...hub :8090 with a forged X-Forwarded-For: '$r', want 403"
ports=$(docker ps --filter "label=com.docker.compose.project=$P" --format '{{.Ports}}' | grep -E '0\.0\.0\.0|127\.0\.0\.1|\[::\]' || true)
[[ -z "$ports" ]] && ok "nothing in the throwaway stack is published on the host" || bad "host ports published: $ports"
st=$(docker inspect -f '{{.State.Health.Status}}' "$("${C[@]}" ps -q hub-mesh)")
[[ "$st" == healthy || "$st" == starting ]] && ok "hub-mesh healthcheck sees the tunnel ($st)" || bad "hub-mesh health: $st"

# Off: the hub has no mesh port, and the gate says how to open it.
HUB_MESH=off "${C[@]}" up -d hub >/dev/null 2>&1
for _ in $(seq 1 30); do
  r=$(phone_curl "http://$MESH_IP:8080/")
  [[ "$r" == "502 "* ]] && break
  sleep 1
done
[[ "$r" == "502 "* ]] && ok "HUB_MESH=off: phone -> :8080 opens nothing (502, 'not open to the mesh')" || bad "HUB_MESH=off: '$r', want 502"
r=$(phone_curl -H "X-Forwarded-For: $PHONE_IP" "http://$NET.12:8090/")
[[ "$r" == "000 "* ]] && ok "  ...the hub's mesh port is closed" || bad "  ...hub :8090 with HUB_MESH=off: '$r', want no answer"

# On with no password: the hub must not start at all.
HUB_MESH=on HUB_PASSWORD= "${C[@]}" up -d hub >/dev/null 2>&1 || true
sleep 4
"${C[@]}" logs hub 2>&1 | tail -n 5 | grep -q "HUB_MESH=on opens the hub to the mesh but HUB_PASSWORD is empty" \
  && ok "HUB_MESH=on with no password: the hub refuses to start" \
  || bad "HUB_MESH=on with no password: no refusal in the hub log"

printf '\n%d passed, %d failed\n' "$pass" "$failn"
[[ "$failn" == 0 ]]
