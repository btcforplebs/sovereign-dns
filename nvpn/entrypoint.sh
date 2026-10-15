#!/usr/bin/env bash
# First run: create an identity. Every run: start the daemon and follow its
# log so docker logs / restart policies see the real process.
set -euo pipefail

CONFIG_DIR="${HOME}/.config/nvpn"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
DAEMON_LOG="${CONFIG_DIR}/daemon.log"

# A container start is a fresh PID namespace, so no daemon from a previous run
# can still be alive and any daemon.pid on the volume is stale. Left in place
# it can crash-loop the container: nvpn treats the recorded pid as "already
# running" when it is live here with a daemon command line, which includes the
# new daemon's own thread ids (/proc/<tid> resolves but is not listed). Any
# change in how many processes start before the daemon shifts the pids onto
# that collision.
rm -f "${CONFIG_DIR}/daemon.pid"

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

# Every start, not just the first: the endpoint the daemon advertises is what
# other devices dial. Left alone it advertises this container's Docker-internal
# address, which nothing outside the host can reach — peers then look "online"
# (they announce themselves over the relays) while no data path ever forms.
# Set NVPN_ADVERTISED_ENDPOINT to the host's LAN or public address when the
# host is behind a NAT that STUN can't map for you.
if [[ -n "${NVPN_ADVERTISED_ENDPOINT:-}" ]]; then
  echo "sovereign-dns: advertising endpoint ${NVPN_ADVERTISED_ENDPOINT}"
  nvpn set --endpoint "$NVPN_ADVERTISED_ENDPOINT" || \
    echo "sovereign-dns: warning: could not set the advertised endpoint"
fi

# nvpn dials two public FIPS transit seeds by default (fips1.iris.to and
# fips2.iris.to, compiled into nvpn), which joins this box to a public FIPS mesh.
# NVPN_FIPS_BOOTSTRAP=off sets nvpn's own master switch, fips_bootstrap_enabled,
# so the list is kept but never dialed. Emptying the list would not work: nvpn
# re-adds the default seeds every time it loads the config. The file is edited
# by hand because `nvpn set` rewrites the whole config and drops fields it does
# not model.
set_fips_bootstrap() {
  local tmp="${CONFIG_FILE}.tmp"
  awk -v want="$1" '
    /^fips_bootstrap_enabled[[:space:]]*=/ { next }
    !done && /^\[/ { if (want == "false") print "fips_bootstrap_enabled = false"; done = 1 }
    { print }
    END { if (!done && want == "false") print "fips_bootstrap_enabled = false" }
  ' "$CONFIG_FILE" > "$tmp" && mv "$tmp" "$CONFIG_FILE"
}
case "${NVPN_FIPS_BOOTSTRAP:-on}" in
  off)
    set_fips_bootstrap false
    echo "sovereign-dns: public FIPS transit seeds: OFF (NVPN_FIPS_BOOTSTRAP=off)" ;;
  on)
    set_fips_bootstrap true
    echo "sovereign-dns: public FIPS transit seeds: ON. This node dials fips1.iris.to and"
    echo "  fips2.iris.to and joins the public FIPS mesh. Set NVPN_FIPS_BOOTSTRAP=off to stop." ;;
  *)
    echo "sovereign-dns: NVPN_FIPS_BOOTSTRAP must be on or off, got '${NVPN_FIPS_BOOTSTRAP}'" >&2
    exit 1 ;;
esac

NETWORK_ID=$(awk '
  /^\[\[networks\]\]/ { in_net = 1; next }
  /^\[/ { in_net = 0 }
  in_net && /^network_id[[:space:]]*=/ { print $3 }
' "$CONFIG_FILE" | tr -d '"' | head -1)

if [[ -n "$NETWORK_ID" ]]; then
  echo "sovereign-dns: to add another device to this network:"
  echo "  1. On that device, run:"
  echo "       nvpn join-manual --admin-device-id ${DEVICE_NPUB} --network-id ${NETWORK_ID}"
  echo "     This only queues a join request — it does NOT add the device yet."
  echo "  2. Once you've confirmed you want that specific device on your network,"
  echo "     approve it in the browser control panel on this machine:"
  echo "       http://localhost:${WEB_UI_PORT:-8082}"
  echo "     or from the terminal (replace <joiner-npub> with its printed npub):"
  echo "       docker exec sovereign-dns-nvpn nvpn add-device --device <joiner-npub> --publish"
  echo "     Deliberately not automatic: the network ID + this device's ID are"
  echo "     shared as plain text, so auto-approving anyone who has them would"
  echo "     remove the only access control this network has."
fi

nvpn start --daemon --connect || {
  echo "sovereign-dns: daemon start failed" >&2
  exit 1
}

echo "sovereign-dns: daemon started; following ${DAEMON_LOG}"
touch "$DAEMON_LOG"
exec tail -F "$DAEMON_LOG"
