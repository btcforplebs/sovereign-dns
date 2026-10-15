#!/usr/bin/env bash
# Let your router use this box as the DNS server for the whole house, or stop.
# Every device on the home network (TVs and guests too) then gets Pi-hole's
# blocking with no app on it. Best on a box that is always on, like a Pi.
#
#   bash scripts/dns-lan.sh on      answer DNS on this box's home-network
#                                   address (port 53), then print what to type
#                                   into the router
#   bash scripts/dns-lan.sh off     stop answering the home network (the
#                                   default); the mesh is not affected
#   bash scripts/dns-lan.sh status  is it on, and does a lookup through it work
#
# --no-restart: only write .env.
# SOVEREIGN_DNS_LAN_IP: the address to answer on, instead of the one found.
# SOVEREIGN_DNS_LAN_DESKTOP=1: turn it on under Docker Desktop anyway (below).
#
# Never forward port 53 on the router. On Linux (a Raspberry Pi, a Linux
# server) only private (home-network) senders get answers (hub/dnslan.go), and
# that holds even through a port forward. On Docker Desktop (macOS, Windows)
# no sender check is possible: Docker Desktop hands the box every query from
# its own private address, so a port forward there would make an open
# resolver for the whole internet. That is why `on` refuses under Docker
# Desktop unless SOVEREIGN_DNS_LAN_DESKTOP=1.
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=lib/lan-ip.sh
source scripts/lib/lan-ip.sh

die() { printf '%s\n' "$*" >&2; exit 1; }
[[ -f .env ]] || die "No .env here. Run this from the sovereign-dns folder the installer made."

mode=${1:-}
restart=1
[[ "${2:-}" == "--no-restart" ]] && restart=0

env_value() { grep "^$1=" .env | tail -n1 | cut -d= -f2- || true; }

# COMPOSE_PROFILES as a list without dns-lan, so on/off keep any other profile.
other_profiles() {
  env_value COMPOSE_PROFILES | tr ',' '\n' | grep -vx 'dns-lan' | grep -v '^$' | paste -sd, - || true
}

# Rewrites .env without this switch's two settings, then appends the new ones.
# Same umask and copy-back as the installer, so .env stays 0600.
set_env() {
  (umask 077
   grep -Ev '^(COMPOSE_PROFILES|DNS_LAN_BIND)=' .env > .env.tmp || true
   [[ $# -eq 0 ]] || printf '%s\n' "$@" >> .env.tmp
   cat .env.tmp > .env)
  rm -f .env.tmp
  chmod 600 .env
}

is_on() { env_value COMPOSE_PROFILES | tr ',' '\n' | grep -qx 'dns-lan'; }

# A real lookup through the published port, from this box. Proves the whole
# path: the port, the forwarder and Pi-hole behind it.
lookup_works() {
  local ip=$1
  if command -v dig >/dev/null 2>&1; then
    dig +time=3 +tries=2 +short @"$ip" example.com A 2>/dev/null | grep -Eq '^[0-9.]+$'
  elif command -v nslookup >/dev/null 2>&1; then
    nslookup -timeout=3 example.com "$ip" 2>/dev/null | grep -q '^Address: [0-9]'
  else
    # No dig or nslookup (a fresh Raspberry Pi OS has neither): a raw query
    # for example.com A, id 0xABCD. The forwarder only ever sends back
    # Pi-hole's answer, so any reply with our id means the path works.
    local b=""
    exec 3<>"/dev/udp/${ip}/${2:-53}" || return 1
    printf '\xab\xcd\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01' >&3
    LC_ALL=C IFS= read -r -t 3 -n 1 -u 3 b 2>/dev/null || true
    exec 3>&-
    [[ "$b" == $'\xab' ]]
  fi
}

router_steps() {
  local ip=$1
  cat <<EOF

Now tell your router to hand out this box as DNS:
  1. In the router's settings, find DHCP reservation (or "fixed IP") and
     reserve ${ip} for this box, so its address never changes.
  2. Find the DNS setting (eero: Settings -> Network settings -> DNS) and
     enter ${ip}.
     For a second DNS, use your other DNS box's home address, or leave it
     empty. Never Google or Cloudflare: devices quietly use it and skip
     the blocking.
  3. Devices pick it up when they rejoin the Wi-Fi (or after a restart).

If this box is off, nobody in the house can load websites. Keep it on.
EOF
}

docker_desktop() { docker info --format '{{.OperatingSystem}}' 2>/dev/null | grep -q 'Docker Desktop'; }

# What Docker Desktop costs, said before anything changes.
desktop_warning() {
  cat <<'WARN'
WARNING: this box runs Docker Desktop (macOS or Windows).

Docker Desktop hands this box every DNS query from its own private address,
so the box cannot tell your home network from the internet:
  - If port 53 is ever forwarded on your router, anyone on the internet can
    use this box as a DNS server, and it cannot refuse them.
  - Pi-hole sees the whole house as one device: its log shows one client,
    and its limit of 5000 lookups a minute applies to every device at once.
    A busy house can reach it, and then Pi-hole refuses lookups and nobody
    can load websites, with nothing on screen to say why.

A Linux box (a Raspberry Pi, say) has none of these problems. It is the
better home for home-network DNS.
WARN
}

case "$mode" in
  on)
    if docker_desktop; then
      desktop_warning
      if [[ "${SOVEREIGN_DNS_LAN_DESKTOP:-}" != 1 ]]; then
        echo
        echo "Nothing was changed. To turn it on here anyway, run:"
        echo "    SOVEREIGN_DNS_LAN_DESKTOP=1 bash scripts/dns-lan.sh on"
        exit 1
      fi
      echo
      echo "SOVEREIGN_DNS_LAN_DESKTOP=1: turning it on anyway. Never forward port 53."
      echo
    fi
    ip=${SOVEREIGN_DNS_LAN_IP:-$(lan_ip)}
    [[ "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] \
      || die "Could not find this box's home-network address. Set SOVEREIGN_DNS_LAN_IP=<address> and run again."
    profiles=$(other_profiles)
    set_env "COMPOSE_PROFILES=${profiles:+$profiles,}dns-lan" "DNS_LAN_BIND=${ip}"
    if [[ "$restart" == 1 ]]; then
      echo "Starting home-network DNS on ${ip}..."
      docker compose up -d --build dns-lan
      ok=""
      for _ in 1 2 3 4 5 6 7 8 9 10; do
        if lookup_works "$ip"; then ok=1; break; fi
        sleep 2
      done
      if [[ -z "$ok" ]]; then
        echo
        echo "Home-network DNS is on, but a test lookup through ${ip} did not answer."
        echo "Don't change the router yet. Check: docker compose logs dns-lan"
        exit 1
      fi
      echo "A test lookup through ${ip} worked."
      router_steps "$ip"
      if docker_desktop; then
        echo
        echo "Reminder: this is Docker Desktop. Never forward port 53 (see the warning above)."
      fi
    fi
    ;;
  off)
    profiles=$(other_profiles)
    if [[ -n "$profiles" ]]; then set_env "COMPOSE_PROFILES=${profiles}"; else set_env; fi
    if [[ "$restart" == 1 ]]; then
      echo "Change your router's DNS back first, or the house loses DNS now."
      docker compose rm -sf dns-lan >/dev/null
      echo "Home-network DNS is off. The mesh still uses this box as before."
    fi
    ;;
  status)
    if ! is_on; then echo "Home-network DNS is off."; exit 0; fi
    ip=$(env_value DNS_LAN_BIND)
    echo "Home-network DNS is on, at ${ip}."
    if lookup_works "$ip"; then echo "A test lookup through it worked."
    else echo "A test lookup through it did NOT answer. Check: docker compose logs dns-lan"; exit 1
    fi
    ;;
  *)
    die "usage: bash scripts/dns-lan.sh on|off|status [--no-restart]"
    ;;
esac
