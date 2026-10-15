#!/usr/bin/env bash
# Open the dashboard (the hub) from your other devices over NostrVPN: a phone
# on mobile data, a laptop away from home. Only devices on this box's network
# get there, and every page asks for the dashboard password.
#
#   bash scripts/hub-mesh.sh on     uses the dashboard password you already
#                                   picked (scripts/hub-lan.sh), or asks for
#                                   one, then opens the hub on the mesh
#   bash scripts/hub-mesh.sh off    closes it again (the default)
#   bash scripts/hub-mesh.sh url    print the addresses to open
#
# --no-restart: only write .env.
# SOVEREIGN_DNS_HUB_PASSWORD: the password, for running without a terminal.
#
# The address is http://<this box's mesh IP>:8080. Pi-hole's own page is at
# http://<this box's mesh IP>/admin with the Pi-hole password, mesh only, on
# or off. Nothing is published on the home network or the internet: the
# hub-mesh service listens inside the VPN container only (hub/mesh.go).
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=lib/hub-password.sh
source scripts/lib/hub-password.sh

die() { printf '%s\n' "$*" >&2; exit 1; }
[[ -f .env ]] || die "No .env here. Run this from the sovereign-dns folder the installer made."

mode=${1:-}
restart=1
[[ "${2:-}" == "--no-restart" ]] && restart=0

# Same umask and copy-back as the installer, so .env stays 0600. Drops the
# keys named in $1 (a regex alternation), then appends the rest.
set_env() {
  local drop=$1; shift
  (umask 077
   grep -Ev "^(${drop})=" .env > .env.tmp || true
   printf '%s\n' "$@" >> .env.tmp
   cat .env.tmp > .env)
  rm -f .env.tmp
  chmod 600 .env
}

mesh_ip() {
  local tun
  tun=$(grep '^NVPN_TUN_IFACE=' .env | cut -d= -f2 || true)
  docker compose exec -T nvpn ip -4 -o addr show dev "${tun:-utun100}" 2>/dev/null \
    | awk '{ sub(/\/.*/, "", $4); print $4; exit }'
}

print_urls() {
  local ip
  ip=$(mesh_ip || true)
  ip=${ip:-"<this box's mesh IP>"}
  if hub_env_is HUB_MESH on; then
    echo "From any device on your NostrVPN network, open:"
    echo "    http://${ip}:8080        the dashboard (dashboard password)"
  else
    echo "The dashboard is not open to the mesh. 'bash scripts/hub-mesh.sh on' opens it."
    echo "From any device on your NostrVPN network:"
  fi
  echo "    http://${ip}/admin       Pi-hole (Pi-hole password)"
}

restart_hub() {
  [[ "$restart" == 1 ]] || return 0
  echo "Restarting the dashboard..."
  docker compose up -d hub hub-mesh
}

case "$mode" in
  on)
    pw_line=$(hub_env_password)
    if [[ -n "$pw_line" && -z "${SOVEREIGN_DNS_HUB_PASSWORD:-}" ]]; then
      echo "Using the dashboard password you already picked."
    else
      hub_ask_password || { echo "No password given, so the dashboard stays off the mesh."; exit 0; }
      pw_line="HUB_PASSWORD='${hub_pw}'"
    fi
    set_env 'HUB_MESH|HUB_PASSWORD' "HUB_MESH=on" "$pw_line"
    restart_hub
    if [[ "$restart" == 1 ]]; then echo; print_urls; fi
    ;;
  off)
    # The password stays while the home network still uses it (hub-lan.sh).
    if hub_env_is HUB_BIND 0.0.0.0; then
      set_env 'HUB_MESH' "HUB_MESH=off"
    else
      set_env 'HUB_MESH|HUB_PASSWORD' "HUB_MESH=off"
    fi
    restart_hub
    if [[ "$restart" == 1 ]]; then echo; print_urls; fi
    ;;
  url)
    print_urls
    ;;
  *)
    die "usage: bash scripts/hub-mesh.sh on|off|url [--no-restart]"
    ;;
esac
