#!/usr/bin/env bash
# Open the dashboard (the hub) from other computers on your home network, or
# go back to this computer only. For a box with no screen, like a Raspberry Pi.
#
#   bash scripts/hub-lan.sh on     asks for a dashboard password, then
#                                  publishes the hub on this box's network
#                                  addresses; every page asks for the password
#   bash scripts/hub-lan.sh off    back to http://localhost:8080 on this box
#                                  only, with no password (the default)
#   bash scripts/hub-lan.sh url    print the addresses to open
#
# --no-restart: only write .env (the installer starts the stack itself).
# SOVEREIGN_DNS_HUB_PASSWORD: the password, for running without a terminal.
#
# Only the hub moves. The VPN control panel keeps no password of its own, so it
# stays on this box's loopback; from another computer, the hub adds devices by
# their join link instead. The hub itself refuses to start on the network with
# no password, and serves private (home-network) addresses only (hub/auth.go).
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=lib/hub-password.sh
source scripts/lib/hub-password.sh

die() { printf '%s\n' "$*" >&2; exit 1; }
[[ -f .env ]] || die "No .env here. Run this from the sovereign-dns folder the installer made."

mode=${1:-}
restart=1
[[ "${2:-}" == "--no-restart" ]] && restart=0

hub_port() { local p; p=$(grep '^HUB_PORT=' .env | cut -d= -f2 || true); echo "${p:-8080}"; }

# This box's address on the home network: the one it would use to reach the
# internet, which is the LAN address behind a home router.
lan_ip() {
  local ip=""
  if command -v ip >/dev/null 2>&1; then
    ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n1)
  elif command -v route >/dev/null 2>&1 && command -v ipconfig >/dev/null 2>&1; then
    local ifc
    ifc=$(route -n get default 2>/dev/null | awk '/interface:/ { print $2 }')
    [[ -n "$ifc" ]] && ip=$(ipconfig getifaddr "$ifc" 2>/dev/null || true)
  fi
  echo "$ip"
}

print_urls() {
  local port ip name
  port=$(hub_port)
  if grep -q '^HUB_BIND=0.0.0.0$' .env; then
    ip=$(lan_ip)
    name=$(hostname -s 2>/dev/null || hostname)
    echo "From another computer on your home network, open:"
    [[ -n "$ip" ]] && echo "    http://${ip}:${port}"
    [[ -n "$name" ]] && echo "    http://${name}.local:${port}   (if your computer finds it by name)"
    echo "and sign in with the dashboard password."
  else
    echo "The dashboard opens on this computer only:  http://localhost:${port}"
  fi
}

# Rewrites .env without the hub's two settings, then appends the new ones.
# Same umask and copy-back as the installer, so .env stays 0600.
set_env() {
  (umask 077
   grep -Ev '^(HUB_BIND|HUB_PASSWORD)=' .env > .env.tmp || true
   printf '%s\n' "$@" >> .env.tmp
   cat .env.tmp > .env)
  rm -f .env.tmp
  chmod 600 .env
}

# Docker Desktop (macOS, Windows) hands the hub every connection from its own
# VM's private address, so the hub's home-network-only check cannot tell a
# neighbour from the internet. There the password is the only guard.
docker_desktop_note() {
  if docker info --format '{{.OperatingSystem}}' 2>/dev/null | grep -q 'Docker Desktop'; then
    echo
    echo "Note: this is Docker Desktop, so the dashboard cannot tell your home network"
    echo "from anywhere else. The password is the only thing guarding it: pick a long"
    echo "one, and don't forward port $(hub_port) on your router."
  fi
}

# Off the home network. The password goes too, unless the mesh still uses it
# (scripts/hub-mesh.sh): with HUB_MESH=on and no password the hub would not
# start at all.
lan_off() {
  if hub_env_is HUB_MESH on && [[ -n "$(hub_env_password)" ]]; then
    set_env "HUB_BIND=127.0.0.1" "$(hub_env_password)"
  else
    set_env "HUB_BIND=127.0.0.1"
  fi
}

no_password() {
  echo "No password given, so the dashboard stays on this computer only."
  echo "Run 'bash scripts/hub-lan.sh on' in the sovereign-dns folder to try again."
  lan_off
  restart_hub
}

restart_hub() {
  [[ "$restart" == 1 ]] || return 0
  echo "Restarting the dashboard..."
  docker compose up -d hub
}

case "$mode" in
  on)
    # Ctrl-D (end of input) means "never mind": stay on this computer only,
    # rather than stopping the install half done.
    hub_ask_password || { no_password; exit 0; }
    pw=$hub_pw
    # Single quotes: compose reads the value exactly, with no $ expansion.
    set_env "HUB_BIND=0.0.0.0" "HUB_PASSWORD='${pw}'"
    docker_desktop_note
    restart_hub
    # The installer prints the addresses itself once the stack is up. An
    # `if`, not `[[ ]] &&`: as the last command, a false test would be this
    # script's exit status, and the installer (set -e) would stop right here.
    if [[ "$restart" == 1 ]]; then echo; print_urls; fi
    ;;
  off)
    lan_off
    restart_hub
    # The installer prints the addresses itself once the stack is up. An
    # `if`, not `[[ ]] &&`: as the last command, a false test would be this
    # script's exit status, and the installer (set -e) would stop right here.
    if [[ "$restart" == 1 ]]; then echo; print_urls; fi
    ;;
  url)
    print_urls
    ;;
  *)
    die "usage: bash scripts/hub-lan.sh on|off|url [--no-restart]"
    ;;
esac
