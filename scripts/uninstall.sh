#!/usr/bin/env bash
# Remove this sovereign-dns box, in the order that keeps your devices online.
#
#   curl -fsSLO https://raw.githubusercontent.com/btcforplebs/sovereign-dns/main/scripts/uninstall.sh && bash uninstall.sh
#
# Run it where you ran install.sh (it looks for ./sovereign-dns), or as
# `bash scripts/uninstall.sh` inside the sovereign-dns folder. In order:
#
#   1. Network DNS off, when this box is the one answering for your devices
#      and no backup box will. Otherwise every device would keep sending its
#      lookups to a box that is gone, and fail (the override fails closed).
#   2. Whole-house DNS off (scripts/dns-lan.sh), with a reminder to change
#      the router back.
#   3. This box out of the network: its device ID, to remove on your first
#      box. A box that is one of several admins removes itself.
#   4. `docker compose down -v --rmi all`: containers, data and images.
#   5. The sovereign-dns folder.
#
# It says what it will do and asks once before changing anything.
#   --yes  don't ask (for scripted runs)
#   SOVEREIGN_DNS_DIR  the folder to remove, when it isn't found by itself
set -euo pipefail

die() { printf '%s\n' "$*" >&2; exit 1; }

yes=0
for arg in "$@"; do
  case "$arg" in
    --yes|-y) yes=1 ;;
    *) die "Unknown option: $arg (the only option is --yes)" ;;
  esac
done

here=$(cd "$(dirname "$0")" && pwd)
if [[ -n "${SOVEREIGN_DNS_DIR:-}" ]]; then dir=$SOVEREIGN_DNS_DIR
elif [[ -f "$here/../docker-compose.yml" && "$(basename "$here")" == scripts ]]; then dir="$here/.."
elif [[ -f sovereign-dns/docker-compose.yml ]]; then dir=sovereign-dns
else die "No sovereign-dns folder here. Run this where you ran install.sh, or set SOVEREIGN_DNS_DIR=/path/to/sovereign-dns."
fi
dir=$(cd "$dir" && pwd)
grep -q '^name: sovereign-dns$' "$dir/docker-compose.yml" 2>/dev/null \
  || die "$dir isn't a sovereign-dns folder (no sovereign-dns docker-compose.yml), so nothing was changed."
cd "$dir"

command -v docker >/dev/null 2>&1 || die "Docker isn't installed or isn't on PATH, so the box can't be stopped from here."

env_value() { [[ -f .env ]] && grep "^$1=" .env | tail -n1 | cut -d= -f2- || true; }

# What the running box says about itself. Each read is empty when the box is
# down, and then the steps that need it say so instead of guessing.
running=0
docker compose ps --status running --services 2>/dev/null | grep -qx nvpn && running=1
status=""
[[ "$running" == 1 ]] && status=$(docker compose exec -T nvpn nvpn status --json --discover-secs 0 2>/dev/null || true)
# `nvpn status --json` prints one key per line. The DNS values are read from
# its network_dns block only, so a same-named key elsewhere can't answer.
dns_block=$(sed -n '/^  "network_dns": {/,/^  }/p' <<<"$status")
json_value() { grep -m1 "^ *\"$1\":" <<<"$2" | sed -E 's/^[^:]*: *"?([^",]*)"?,?$/\1/' || true; }
device_id=$(json_value device_id "$status")
override=$(json_value override "$dns_block")
role=$(json_value role "$dns_block")
# Backup DNS boxes, listed by hex key under "backups".
backups=0
if ! grep -q '"backups": \[\]' <<<"$dns_block"; then
  backups=$(sed -n '/"backups": \[/,/\]/p' <<<"$dns_block" | grep -cE '"[0-9a-f]{64}"' || true)
fi
# The control panel's view: admin or not, and how many admins the network has.
tick=""
if [[ "$running" == 1 ]] && command -v curl >/dev/null 2>&1; then
  tick=$(curl -fsS -m 10 -X POST "http://127.0.0.1:$(env_value WEB_UI_PORT | grep . || echo 8082)/api/tick" 2>/dev/null || true)
fi
is_admin=0; admins=0
if grep -q '"localIsAdmin":true' <<<"$tick"; then is_admin=1; fi
admins=$( { grep -o '"adminNpubs":\[[^]]*\]' <<<"$tick" || true; } | head -n1 | { grep -o 'npub1[0-9a-z]*' || true; } | sort -u | wc -l | tr -d ' ')
dns_lan=0
env_value COMPOSE_PROFILES | tr ',' '\n' | grep -qx dns-lan && dns_lan=1

# The plan, decided up front so the question covers all of it.
dns_off=0
echo
echo "This removes the sovereign-dns box in $dir."
echo
if [[ "$running" == 0 ]]; then
  echo "- The box isn't running, so network DNS can't be checked or turned off from here."
  echo "  If your devices still use it, turn Network DNS off on another admin box or start"
  echo "  this one (docker compose up -d) and run this again."
elif [[ "$override" == true && "$role" == nameserver && "$backups" -gt 0 ]]; then
  echo "- Network DNS stays on: $backups backup box(es) will answer for your devices."
elif [[ "$override" == true && "$role" == nameserver && "$is_admin" == 1 ]]; then
  dns_off=1
  echo "- Turn Network DNS off, so your devices go back to their normal DNS."
elif [[ "$override" == true && "$role" == nameserver ]]; then
  echo "- WARNING: this box answers DNS for your devices, but it isn't an admin, so it"
  echo "  can't turn Network DNS off. Do that on your first box's dashboard first, or"
  echo "  your devices can't open websites once this box is gone."
fi
if [[ "$dns_lan" == 1 ]]; then
  echo "- Turn whole-house DNS off. Change your router's DNS setting back first, or every"
  echo "  device on your Wi-Fi loses DNS when the box goes."
fi
echo "- Stop the box and delete its data and images (docker compose down -v --rmi all):"
echo "  its private network identity, the device list and the Pi-hole settings. This"
echo "  can't be undone."
echo "- Delete the folder $dir."
echo

if [[ "$yes" != 1 ]]; then
  [[ -t 0 ]] || die "No terminal to ask on. Run it again with --yes to go ahead."
  printf 'Go ahead? [y/N] '
  read -r answer || answer=n
  case "$answer" in [Yy]*) ;; *) echo "Nothing was changed."; exit 0 ;; esac
fi

if [[ "$dns_off" == 1 ]]; then
  echo "Turning Network DNS off..."
  if docker compose exec -T nvpn nvpn set --network-dns-override false >/dev/null; then
    # Give the change a moment to reach the devices before the box goes.
    sleep 10
    echo "Network DNS is off."
  else
    die "Couldn't turn Network DNS off, so nothing else was changed. Turn it off on the
dashboard (http://localhost:$(env_value HUB_PORT | grep . || echo 8080), Network DNS) and run this again."
  fi
fi

if [[ "$dns_lan" == 1 ]]; then
  bash scripts/dns-lan.sh off --no-restart >/dev/null
  echo "Whole-house DNS is off. Remember: your router's DNS setting must not point at this box."
fi

# Leaving the network. Only an admin can change the device list, so a member
# box is removed on the first box; the last admin owns the network itself.
leave_msg=""
if [[ -n "$device_id" && "$is_admin" == 1 && "$admins" -gt 1 ]]; then
  if docker compose exec -T nvpn nvpn remove-device --device "$device_id" --publish >/dev/null 2>&1; then
    leave_msg="This box removed itself from your network ($device_id)."
  else
    leave_msg="This box couldn't remove itself from your network. On another admin box, remove this device: $device_id"
  fi
elif [[ -n "$device_id" && "$is_admin" == 1 ]]; then
  leave_msg="This box was your network's only admin ($device_id), so the network goes with it.
On each phone or laptop, leave the network in the Nostr VPN app."
elif [[ -n "$device_id" ]]; then
  leave_msg="This box's device ID: $device_id
Remove it on your first box: dashboard (http://localhost:8080), Devices, remove."
fi

echo "Stopping the box and deleting its data and images..."
docker compose --profile dns-lan down -v --rmi all --remove-orphans

cd /
rm -rf "$dir"
echo "Deleted $dir."

echo
[[ -n "$leave_msg" ]] && { echo "$leave_msg"; echo; }
echo "Done. Docker itself is still installed; remove it too if nothing else uses it."
echo "install.sh and uninstall.sh, if you downloaded them, can be deleted too."
