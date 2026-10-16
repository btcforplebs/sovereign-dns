#!/usr/bin/env bash
# Is this box really answering DNS? Asks Pi-hole the way a device does: at the
# box's mesh address (and its home-network address when dns-lan is on), for a
# normal name and for an ad domain. The installer runs it last and says the
# answer plainly, because a box can look healthy while answering nothing: a
# second box once installed with no errors, its Pi-hole admin page answered on
# the mesh, and every lookup timed out (see the pihole service in
# docker-compose.yml).
#
#   bash scripts/dns-check.sh            from the sovereign-dns folder
#   DNS_CHECK_WAIT=0 bash scripts/dns-check.sh   ask once, no waiting
#
# Waits up to DNS_CHECK_WAIT seconds (default 90) for a just-started Pi-hole.
# Exit 0 when the mesh answers and blocks ads, 1 when it does not.
set -uo pipefail
cd "$(dirname "$0")/.."

IFACE=${NVPN_TUN_IFACE:-utun100}
WAIT=${DNS_CHECK_WAIT:-90}
[[ "$WAIT" =~ ^[0-9]+$ ]] || WAIT=90

# dig and ip run inside nvpn, which is also Pi-hole's network namespace.
in_nvpn() { docker compose exec -T nvpn "$@" 2>/dev/null; }
env_value() { sed -n "s/^$1=//p" .env 2>/dev/null | tail -n1; }

mesh_ip() { in_nvpn ip -4 -o addr show "$IFACE" | awk '{ print $4 }' | cut -d/ -f1 | head -n1; }
# answers <server>: "ok" when a normal name resolves and an ad domain is
# blocked, otherwise what went wrong.
answers() {
  local normal ad
  normal=$(in_nvpn dig +short +time=3 +tries=1 "@$1" example.com A | grep -E '^[0-9]+(\.[0-9]+){3}$' | head -n1)
  [[ -n "$normal" ]] || { echo "no answer"; return; }
  ad=$(in_nvpn dig +short +time=3 +tries=1 "@$1" doubleclick.net A | head -n1)
  [[ "$ad" == "0.0.0.0" ]] || { echo "answers, but did not block an ad domain (got '${ad:-nothing}')"; return; }
  echo ok
}

deadline=$((SECONDS + WAIT))
ip="" mesh="not checked"
while :; do
  ip=$(mesh_ip)
  if [[ -n "$ip" ]]; then
    mesh=$(answers "$ip")
    [[ "$mesh" == ok ]] && break
  else
    mesh="the VPN tunnel is not up yet, so the box has no mesh address"
  fi
  (( SECONDS < deadline )) || break
  sleep 3
done

rc=0
if [[ "$mesh" == ok ]]; then
  echo "✅ This box answers DNS on the VPN at $ip and blocks ads."
else
  rc=1
  echo "❌ This box is NOT answering DNS on the VPN${ip:+ at $ip}: $mesh."
  echo "   Devices that use it get no answers. Try, in the sovereign-dns folder:"
  echo "     docker compose restart pihole && bash scripts/dns-check.sh"
  echo "   If it still fails: bash scripts/healthcheck.sh"
fi

# The home network, only when dns-lan is on (scripts/dns-lan.sh).
if env_value COMPOSE_PROFILES | tr ',' '\n' | grep -qx dns-lan; then
  lan=$(env_value DNS_LAN_BIND)
  if [[ -n "$lan" && "$(answers "$lan")" == ok ]]; then
    echo "✅ It also answers on the home network at $lan."
  else
    echo "❌ Home-network DNS is on, but this box is NOT answering at ${lan:-its home address}."
    echo "   Check: docker compose ps dns-lan"
  fi
else
  echo "   Home-network DNS is off (devices reach this box through the VPN app)."
  echo "   To serve the home network too: bash scripts/dns-lan.sh on"
fi
exit "$rc"
