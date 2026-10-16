#!/usr/bin/env bash
# Kill the VPN daemon inside a running stack and check the box brings it back
# by itself. Run from the repo root against a stack that is up and healthy
# (docker compose up -d); it is for CI and throwaway stacks, never a live box:
# it kills the daemon on purpose.
#
#   bash scripts/test-daemon-recovery.sh
#
# Why: on a 512 MB VPS the kernel's out-of-memory killer took the daemon. The
# container stayed "running" and "healthy" (`nvpn status` exits 0 whatever the
# daemon is doing), so nothing restarted it, and Pi-hole and the hub gate,
# which share its network namespace, stayed broken. Three things must hold:
#   1. the healthcheck fails while the daemon is stopped,
#   2. the daemon comes back without anyone running a command,
#   3. it comes back in the SAME container, so Pi-hole's namespace is still
#      the one it is attached to (a new container would leave it on the old one).
set -uo pipefail
NVPN=${NVPN_CONTAINER:-sovereign-dns-nvpn}
PIHOLE=${PIHOLE_CONTAINER:-sovereign-dns-pihole}
fail=0
ok() { printf 'ok    %s\n' "$1"; }
bad() { printf 'FAIL  %s\n' "$1"; fail=1; }
status() { docker exec "$NVPN" nvpn status --discover-secs 0 2>&1 | grep '^daemon:' || true; }
pid_of() { sed -n 's/^daemon: running (pid \([0-9]*\)).*/\1/p' <<<"$1"; }

before=$(status)
pid=$(pid_of "$before")
[[ -n "$pid" ]] || { echo "the daemon is not running before the test starts: '$before'"; exit 2; }
started=$(docker inspect -f '{{.State.StartedAt}}' "$NVPN")
cid=$(docker inspect -f '{{.Id}}' "$NVPN")
pihole_started=$(docker inspect -f '{{.State.StartedAt}}' "$PIHOLE")
echo "daemon pid $pid in $NVPN (started $started)"

# 1. The healthcheck, exactly as the container runs it, while the daemon is
# stopped. `nvpn stop` returns once it is down; the container's own watcher
# needs several seconds before it starts the daemon again, so this runs first.
hc=$(docker inspect -f '{{json .Config.Healthcheck.Test}}' "$NVPN" | python3 -I -c 'import json,sys; t=json.load(sys.stdin); print(t[1] if t[0]=="CMD-SHELL" else " ".join(t[1:]))')
docker exec "$NVPN" sh -c "$hc" && ok_hc_before=1 || ok_hc_before=0
[[ "$ok_hc_before" == 1 ]] && ok "healthcheck passes with the daemon running" || bad "healthcheck fails with the daemon running"
docker exec "$NVPN" nvpn stop >/dev/null 2>&1
echo "after nvpn stop: $(status)"
if docker exec "$NVPN" sh -c "$hc"; then bad "healthcheck still passes with the daemon stopped"; else ok "healthcheck fails with the daemon stopped"; fi

# 2. Killed outright, the way the out-of-memory killer does it. Whatever state
# the stop left, make sure there is a daemon to kill, then kill -9 it.
for _ in $(seq 1 30); do
  pid=$(pid_of "$(status)")
  [[ -n "$pid" ]] && break
  sleep 2
done
if [[ -n "$pid" ]]; then
  docker exec "$NVPN" kill -9 "$pid"
  echo "killed daemon pid $pid with SIGKILL"
fi

# 3. Back by itself, as a different process, in the same container.
newpid=""
for _ in $(seq 1 45); do
  newpid=$(pid_of "$(status)")
  [[ -n "$newpid" && "$newpid" != "$pid" ]] && break
  newpid=""
  sleep 2
done
if [[ -n "$newpid" ]]; then ok "the daemon is back on its own (pid $newpid)"; else bad "the daemon did not come back within 90 s: '$(status)'"; fi
[[ "$(docker inspect -f '{{.Id}} {{.State.StartedAt}}' "$NVPN")" == "$cid $started" ]] \
  && ok "  in the same container, not a restarted one" || bad "  the container was replaced or restarted, which strands Pi-hole's namespace"
[[ "$(docker inspect -f '{{.State.StartedAt}}' "$PIHOLE")" == "$pihole_started" ]] \
  && ok "  Pi-hole was left running alongside it" || bad "  Pi-hole restarted"

# 4. Healthy again, and the tunnel interface is back in the shared namespace.
h=""
for _ in $(seq 1 30); do
  h=$(docker inspect -f '{{.State.Health.Status}}' "$NVPN")
  [[ "$h" == healthy ]] && break
  sleep 3
done
[[ "$h" == healthy ]] && ok "the container reports healthy again" || bad "the container is '$h' after recovery"
for _ in $(seq 1 20); do
  docker exec "$NVPN" ip -o link show utun100 >/dev/null 2>&1 && break
  sleep 2
done
docker exec "$NVPN" ip -o link show utun100 >/dev/null 2>&1 \
  && ok "the tunnel interface utun100 is back" || bad "no utun100 after recovery"
# Pi-hole listens on the tunnel address, so ask it there (the loopback address
# is nvpn's own stub and would answer even with Pi-hole broken).
ans=""
for _ in $(seq 1 20); do
  ip=$(docker exec "$NVPN" ip -4 -o addr show utun100 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)
  [[ -n "$ip" ]] && ans=$(docker exec "$NVPN" dig +short +time=3 +tries=1 "@$ip" example.com A 2>/dev/null | grep -E '^[0-9.]+$' | head -1)
  [[ -n "$ans" ]] && break
  sleep 3
done
[[ -n "$ans" ]] && ok "Pi-hole answers DNS on the tunnel address $ip ($ans)" || bad "Pi-hole does not answer DNS on the tunnel address after recovery"

((fail == 0)) && echo "daemon recovery: pass" || echo "daemon recovery: FAIL"
exit "$fail"
