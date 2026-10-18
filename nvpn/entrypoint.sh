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

# The daemon forks away from this script, so nothing here noticed when it died:
# the kernel's out-of-memory killer took it on a 512 MB box, `tail` kept the
# container "running", and Pi-hole and the hub gate, which share this
# container's network namespace, stayed broken until someone restarted it by
# hand. So this script now watches the daemon and starts it again in place.
# In place, not by exiting: a restarted container gets a new network namespace
# and leaves Pi-hole attached to the dead one (see Pi-hole's healthcheck in
# docker-compose.yml), while a daemon restarted inside this container brings
# the tunnel back into the namespace they already share. Only when it will not
# stay up does the script exit, and the restart policy takes over. The
# container healthcheck reads the same line, so a daemon that is down shows
# as unhealthy meanwhile.
daemon_running() {
  local out
  out=$(nvpn status --discover-secs 0 2>/dev/null || true)
  grep -q '^daemon: running' <<<"$out"
}

# Pi-hole shares this network namespace and listens on the tunnel's address.
# When the tunnel comes back after the daemon died, Pi-hole has sometimes
# logged "listening on <tunnel address>" without ever opening the socket
# (measured in CI: connection refused for minutes, until some later network
# change made it look again), so DNS stayed dead on a box whose daemon had
# recovered. A harmless address added to and removed from the tunnel is a
# network change Pi-hole acts on. Only when the tunnel address refuses DNS, at
# most every 30 seconds, and only once the daemon is running.
TUN_IFACE="${NVPN_TUN_IFACE:-utun100}"
last_nudge=0
refused=0
tunnel_refuses_dns() {
  local ip out
  ip=$(ip -4 -o addr show dev "$TUN_IFACE" 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n1)
  [[ -n "$ip" ]] || return 1
  out=$(dig +time=2 +tries=1 +short "@$ip" doubleclick.net A 2>&1 || true)
  grep -qi 'connection refused' <<<"$out"
}
nudge_listeners() {
  echo "sovereign-dns: nothing answers DNS on the tunnel address; nudging Pi-hole to listen again"
  ip addr add 169.254.255.253/32 dev "$TUN_IFACE" scope link 2>/dev/null || true
  sleep 1
  ip addr del 169.254.255.253/32 dev "$TUN_IFACE" 2>/dev/null || true
}

tail -F "$DAEMON_LOG" &
TAIL_PID=$!
trap 'kill "$TAIL_PID" 2>/dev/null; exit 0' TERM INT

down=0
attempts=0
while :; do
  sleep 5 & wait $! || true
  kill -0 "$TAIL_PID" 2>/dev/null || { echo "sovereign-dns: log follower died, exiting" >&2; exit 1; }
  if daemon_running; then
    down=0
    attempts=0
    if tunnel_refuses_dns; then
      refused=$((refused + 1))
      if [[ "$refused" -ge 2 && $((SECONDS - last_nudge)) -ge 30 ]]; then
        nudge_listeners
        last_nudge=$SECONDS
        refused=0
      fi
    else
      refused=0
    fi
    continue
  fi
  down=$((down + 1))
  # Two checks in a row (about 10 s), so a status call that merely raced a
  # daemon restart does not trigger one.
  [[ "$down" -ge 2 ]] || continue
  down=0
  attempts=$((attempts + 1))
  if [[ "$attempts" -gt 5 ]]; then
    echo "sovereign-dns: daemon will not stay up after 5 restarts, exiting so the container restarts" >&2
    exit 1
  fi
  echo "sovereign-dns: daemon is not running (probably killed, often by the kernel out of memory); starting it again (attempt ${attempts} of 5)"
  rm -f "${CONFIG_DIR}/daemon.pid"
  nvpn start --daemon --connect || echo "sovereign-dns: daemon start failed, will retry" >&2
done
