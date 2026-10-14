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

restart_hub() {
  [[ "$restart" == 1 ]] || return 0
  echo "Restarting the dashboard..."
  docker compose up -d hub
}

case "$mode" in
  on)
    pw=${SOVEREIGN_DNS_HUB_PASSWORD:-}
    if [[ -z "$pw" ]]; then
      [[ -t 0 ]] || die "No terminal to ask for a password. Set SOVEREIGN_DNS_HUB_PASSWORD and run again."
      while :; do
        echo "Pick a password for the dashboard, at least 8 characters (typing is hidden):"
        read -rs pw; echo
        echo "Type it again:"
        read -rs pw2; echo
        if [[ "$pw" != "$pw2" ]]; then echo "Those didn't match. Try again."; continue; fi
        if [[ ${#pw} -lt 8 ]]; then echo "That's shorter than 8 characters. Try again."; continue; fi
        if [[ "$pw" == *"'"* ]]; then echo "Please pick one without a ' (single quote) in it."; continue; fi
        break
      done
    fi
    [[ ${#pw} -ge 8 ]] || die "The dashboard password must be at least 8 characters."
    [[ "$pw" != *"'"* && "$pw" != *$'\n'* ]] || die "The dashboard password can't contain a ' or a line break."
    # Single quotes: compose reads the value exactly, with no $ expansion.
    set_env "HUB_BIND=0.0.0.0" "HUB_PASSWORD='${pw}'"
    restart_hub
    # The installer prints the addresses itself once the stack is up.
    [[ "$restart" == 1 ]] && { echo; print_urls; }
    ;;
  off)
    set_env "HUB_BIND=127.0.0.1"
    restart_hub
    # The installer prints the addresses itself once the stack is up.
    [[ "$restart" == 1 ]] && { echo; print_urls; }
    ;;
  url)
    print_urls
    ;;
  *)
    die "usage: bash scripts/hub-lan.sh on|off|url [--no-restart]"
    ;;
esac
