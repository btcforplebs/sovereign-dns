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

# Second-box mode: a box installed to back up an existing sovereign-dns box
# joins that box's network instead of founding its own. Both values are
# printed by the first box at startup (its npub and network ID). A half-set
# pair is refused rather than guessed at: founding a network by mistake
# leaves the box serving a network no device uses, with nothing in the logs.
JOIN_ADMIN="${SOVEREIGN_DNS_JOIN_ADMIN:-}"
JOIN_NETWORK="${SOVEREIGN_DNS_JOIN_NETWORK:-}"
if [[ -n "$JOIN_ADMIN" && -z "$JOIN_NETWORK" ]] || [[ -z "$JOIN_ADMIN" && -n "$JOIN_NETWORK" ]]; then
  echo "sovereign-dns: set both SOVEREIGN_DNS_JOIN_ADMIN and SOVEREIGN_DNS_JOIN_NETWORK, or neither" >&2
  exit 1
fi
# Survives restarts, so a joined box never later prints itself as the admin.
JOINED_MARKER="${CONFIG_DIR}/sovereign-dns-joined"
# Set while a join has been attempted and not yet succeeded, so a restart
# retries the join with the same identity instead of minting a new one.
JOIN_PENDING="${CONFIG_DIR}/sovereign-dns-join-pending"

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
if [[ -n "$JOIN_ADMIN" && ( "$FRESH_INIT" == "1" || -f "$JOIN_PENDING" ) ]]; then
  echo "sovereign-dns: joining the existing network ${JOIN_NETWORK} as a second DNS box"
  touch "$JOIN_PENDING"
  if ! nvpn join-manual --admin-device-id "$JOIN_ADMIN" --network-id "$JOIN_NETWORK"; then
    # Keep the identity: an operator may already have approved this npub on
    # the first box, and a new key each restart would leave dead devices on
    # its roster. The pending marker makes the next start retry the join, and
    # never found a network. The pause keeps a restart policy from spinning.
    echo "sovereign-dns: could not join network ${JOIN_NETWORK}; check SOVEREIGN_DNS_JOIN_ADMIN" >&2
    echo "  and SOVEREIGN_DNS_JOIN_NETWORK. Retrying on next start with the same identity." >&2
    sleep 30
    exit 1
  fi
  rm -f "$JOIN_PENDING"
  touch "$JOINED_MARKER"
elif [[ -f "$JOIN_PENDING" ]]; then
  # A join was started and the variables have since been removed. Founding a
  # network now would put this box in a network nobody else is in, so stop.
  echo "sovereign-dns: a join to another network never finished and SOVEREIGN_DNS_JOIN_* is" >&2
  echo "  no longer set. Set them again to finish it, or remove the nvpn-config volume to" >&2
  echo "  start over as a first box." >&2
  sleep 30
  exit 1
elif [[ "$FRESH_INIT" == "1" ]]; then
  # A marker left on the volume from an earlier joined life must not make a
  # box that is founding its own network describe itself as a backup.
  rm -f "$JOINED_MARKER"
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

# nvpn dials two public FIPS transit seeds by default (185.18.221.232 and
# 65.109.48.91, compiled into nvpn), which joins this box to a public FIPS mesh.
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
    echo "sovereign-dns: public FIPS transit seeds: ON. This node dials 185.18.221.232 and"
    echo "  65.109.48.91 and joins the public FIPS mesh. Set NVPN_FIPS_BOOTSTRAP=off to stop." ;;
  *)
    echo "sovereign-dns: NVPN_FIPS_BOOTSTRAP must be on or off, got '${NVPN_FIPS_BOOTSTRAP}'" >&2
    exit 1 ;;
esac

NETWORK_ID=$(awk '
  /^\[\[networks\]\]/ { in_net = 1; next }
  /^\[/ { in_net = 0 }
  in_net && /^network_id[[:space:]]*=/ { print $3 }
' "$CONFIG_FILE" | tr -d '"' | head -1)

if [[ -n "$JOIN_ADMIN" && "$FRESH_INIT" == "0" && ! -f "$JOINED_MARKER" && ! -f "$JOIN_PENDING" ]]; then
  echo "sovereign-dns: warning: SOVEREIGN_DNS_JOIN_* is set but this box already has its own"
  echo "  network, so it was ignored. Joining only happens on a fresh install."
fi

if [[ -f "$JOINED_MARKER" ]]; then
  echo "sovereign-dns: this box joined another box's network to serve as a backup."
  echo "  On the FIRST box (the one that founded the network), run this one command:"
  echo "    docker exec sovereign-dns-nvpn nvpn add-device --device ${DEVICE_NPUB} --dns-backup"
  echo "  It approves this box and lists it as a backup DNS box in the same roster."
  echo "  Don't approve it as an ordinary device first: until it is listed as a"
  echo "  backup, this box would send its own lookups to the first box. Other"
  echo "  devices still join through the first box's invite, not this one."
elif [[ -n "$NETWORK_ID" ]]; then
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
