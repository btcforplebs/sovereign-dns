#!/usr/bin/env bash
# First run: create an identity. Every run: start the daemon and follow its
# log so docker logs / restart policies see the real process.
set -euo pipefail

CONFIG_DIR="${HOME}/.config/nvpn"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
DAEMON_LOG="${CONFIG_DIR}/daemon.log"

FRESH_INIT=0
if [[ ! -f "$CONFIG_FILE" ]]; then
  echo "sovereign-dns: no config found, creating a fresh NostrVPN identity"
  nvpn init
  FRESH_INIT=1
fi

DEVICE_NPUB=$(awk '
  /^\[nostr\]$/ { in_nostr = 1; next }
  /^\[/ { in_nostr = 0 }
  in_nostr && /^public_key[[:space:]]*=/ { print $3 }
' "$CONFIG_FILE" | tr -d '"')

echo "sovereign-dns: this device's identity:"
echo "  npub: ${DEVICE_NPUB}"

# On a fresh install this container founds its own network (admin = itself)
# rather than requiring some other already-established admin device — that
# precondition is what made this a multi-step manual dance for existing
# users. Founding here, then immediately setting itself as the network's
# DNS node with the override on, means "docker compose up" alone produces a
# fully working sovereign DNS network with zero devices pre-added.
#
# `nvpn init` alone leaves the network disabled (default_network_enabled()
# is false) — only `set --device ...` flips it on (apply_devices_override,
# command_dispatch.rs), and that has to happen in its own call because the
# DNS-override block runs earlier in the same `set` dispatch than the
# devices-override block, so a single combined call would still see no
# active network yet.
if [[ "$FRESH_INIT" == "1" ]]; then
  echo "sovereign-dns: founding a new network on this device"
  nvpn set --device "$DEVICE_NPUB"
  echo "sovereign-dns: turning on the DNS override"
  nvpn set --network-dns-node "$DEVICE_NPUB" --network-dns-override || \
    echo "sovereign-dns: warning: could not enable the DNS override yet (will retry on next restart)"
fi

NETWORK_ID=$(awk '
  /^\[\[networks\]\]/ { in_net = 1; next }
  /^\[/ { in_net = 0 }
  in_net && /^network_id[[:space:]]*=/ { print $3 }
' "$CONFIG_FILE" | tr -d '"' | head -1)

if [[ -n "$NETWORK_ID" ]]; then
  echo "sovereign-dns: to add another device to this network, run on that device:"
  echo "  nvpn join-manual --admin-device-id ${DEVICE_NPUB} --network-id ${NETWORK_ID}"
fi

nvpn start --daemon --connect || {
  echo "sovereign-dns: daemon start failed" >&2
  exit 1
}

echo "sovereign-dns: daemon started; following ${DAEMON_LOG}"
touch "$DAEMON_LOG"
exec tail -F "$DAEMON_LOG"
