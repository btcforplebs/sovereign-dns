#!/usr/bin/env bash
# Tests for scripts/lib/docker-ready.sh and scripts/lib/free-port.sh.
#
#   bash scripts/lib/test-libs.sh
#
# Safe on a machine running the live stack: it never starts, stops or
# restarts Docker, installs nothing (installs run under DR_DRY_RUN=1), and
# writes the login agent and Docker Desktop settings only into a temp dir.
# Runs on macOS and Linux; checks that need one of them say so and skip.

set -u
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=docker-ready.sh
source "$here/docker-ready.sh"
# shellcheck source=free-port.sh
source "$here/free-port.sh"

pass=0 fail=0 skip=0
ok() { pass=$((pass + 1)); printf 'ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL  %s\n' "$1"; }
skp() { skip=$((skip + 1)); printf 'skip  %s (%s)\n' "$1" "$2"; }
not_in() { ! grep -q "$1" "$2"; }
check() { local name=$1; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }

tmp=$(mktemp -d)
holder_pid=""
cleanup() { [[ -n "$holder_pid" ]] && kill "$holder_pid" 2>/dev/null; rm -rf "$tmp"; }
trap cleanup EXIT

os=$(dr_os)
printf '# %s %s, bash %s\n' "$(uname -s)" "$(uname -m)" "$BASH_VERSION"

# ---------------------------------------------------------------- free-port

# Hold a UDP port the way another program would (0.0.0.0 only, like the
# Nostr VPN helper). Prints nothing; sets holder_pid.
hold_udp() {
  perl -MSocket=:all -e '
    socket(my $s, PF_INET, SOCK_DGRAM, 0) or die;
    bind($s, pack_sockaddr_in($ARGV[0], INADDR_ANY)) or die "bind: $!";
    $| = 1; print "held\n"; sleep 120;
  ' "$1" > "$tmp/held" 2>&1 &
  holder_pid=$!
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    grep -q held "$tmp/held" 2>/dev/null && return 0
    sleep 0.2
  done
  return 1
}

# A base port nobody uses, so the tests never depend on what this machine runs.
base=$(fp_pick_free_udp_port 47000 200 2>/dev/null)
check "fp_pick_free_udp_port finds a free port ($base)" test -n "$base"
check "fp_udp_port_free: a free port is free" fp_udp_port_free "$base"

if command -v perl >/dev/null 2>&1 && hold_udp "$base"; then
  fp_udp_port_free "$base"; rc=$?
  check "fp_udp_port_free: a held port is taken (rc=$rc)" test "$rc" = 1
  got=$(fp_pick_free_udp_port "$base" 5 2>/dev/null)
  check "fp_pick_free_udp_port skips the held port (got $got)" test "$got" = "$((base + 1))"
  if [[ -r /proc/net/udp ]]; then
    FP_PROBE=proc fp_udp_port_free "$base"; rc=$?
    check "/proc probe: a held port is taken (rc=$rc)" test "$rc" = 1
    FP_PROBE=proc fp_udp_port_free "$((base + 1))"; rc=$?
    check "/proc probe: a free port is free (rc=$rc)" test "$rc" = 0
  else
    skp "/proc probe" "no /proc/net/udp on $os"
  fi

  # nvpn_listen_port with the default moved onto the held port: what happens
  # when the Nostr VPN app already holds 51820.
  out=$(fp_existing_box_port() { return 1; }; FP_DEFAULT_PORT=$base nvpn_listen_port "$tmp/none.env" 2> "$tmp/msg")
  check "nvpn_listen_port moves off a taken default (got $out)" test "$out" = "$((base + 1))"
  check "nvpn_listen_port says which port and why" grep -q "UDP $base is already in use" "$tmp/msg"
  sed 's/^/      | /' "$tmp/msg"
  kill "$holder_pid" 2>/dev/null; wait "$holder_pid" 2>/dev/null; holder_pid=""
else
  skp "held-port checks" "no perl to hold a port"
fi

out=$(fp_existing_box_port() { return 1; }; FP_DEFAULT_PORT=$base nvpn_listen_port "$tmp/none.env" 2> "$tmp/msg")
check "nvpn_listen_port keeps a free default (got $out)" test "$out" = "$base"

printf 'NVPN_LISTEN_PORT=51999\n' > "$tmp/keep.env"
out=$(nvpn_listen_port "$tmp/keep.env" 2>/dev/null)
check "nvpn_listen_port keeps the port saved in .env (got $out)" test "$out" = 51999

out=$(docker() { printf '%s\n' '0.0.0.0:51821->51820/udp, [::]:51821->51820/udp'; }; nvpn_listen_port "$tmp/none.env" 2>/dev/null)
check "nvpn_listen_port keeps the port a running box already holds (got $out)" test "$out" = 51821

# ------------------------------------------------------------- docker-ready

check "dr_os is known ($os)" test "$os" = macos -o "$os" = linux -o "$os" = wsl

printf 'hello\n' > "$tmp/f"
good=$(dr_sha256 "$tmp/f")
check "dr_verify_sha256 accepts the right hash" dr_verify_sha256 "$tmp/f" "$good"
! dr_verify_sha256 "$tmp/f" "0000$good" 2> "$tmp/msg"
check "dr_verify_sha256 refuses a wrong hash" grep -q "Checksum mismatch" "$tmp/msg"

# Missing docker: with docker off PATH, dry-run shows the pinned install.
nodocker=$tmp/bin
mkdir -p "$nodocker"
for c in uname grep awk sed cut id sw_vers defaults plutil sha256sum shasum mktemp rm cat mkdir tr head; do
  p=$(command -v "$c" 2>/dev/null) && ln -s "$p" "$nodocker/$c"
done
(
  PATH=$nodocker
  dr_find_docker() { return 1; }
  DR_DRY_RUN=1 DR_LAUNCH_AGENTS_DIR=$tmp/agents DR_DOCKER_SETTINGS=$tmp/none.json docker_ready
) 2> "$tmp/dry"
rc=$?
check "docker_ready dry run with no docker returns 0 (rc=$rc)" test "$rc" = 0
case "$os" in
  macos) check "dry run installs the pinned Docker Desktop DMG" grep -q "$DR_MAC_DESKTOP_BUILD/Docker.dmg" "$tmp/dry" ;;
  linux) check "dry run installs Docker via the pinned get.docker.com" grep -q "$DR_GET_DOCKER_COMMIT/install.sh" "$tmp/dry" ;;
esac
sed 's/^/      | /' "$tmp/dry"

if [[ "$os" == macos ]]; then
  DR_LAUNCH_AGENTS_DIR=$tmp/agents dr_write_launch_agent desktop 2>/dev/null
  plist=$tmp/agents/$DR_AGENT_LABEL.plist
  check "login agent is a valid plist" plutil -lint -s "$plist"
  check "login agent opens Docker at login" test "$(plutil -extract ProgramArguments.3 raw "$plist")" = Docker -a "$(plutil -extract RunAtLoad raw "$plist")" = true
  DR_LAUNCH_AGENTS_DIR=$tmp/agents dr_write_launch_agent orbstack 2>/dev/null
  check "login agent opens OrbStack when that is the engine" test "$(plutil -extract ProgramArguments.3 raw "$plist")" = OrbStack

  printf '{"AutoStart":false,"Cpus":4,"DockerAppLaunchPath":"/Applications/Docker.app"}\n' > "$tmp/settings.json"
  DR_DOCKER_SETTINGS=$tmp/settings.json dr_desktop_autostart_setting 2>/dev/null
  check "Docker Desktop AutoStart setting turned on" test "$(plutil -extract AutoStart raw "$tmp/settings.json")" = true
  check "other Docker Desktop settings kept" test "$(plutil -extract Cpus raw "$tmp/settings.json")" = 4
  check "settings file is still JSON" python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$tmp/settings.json"
  DR_DOCKER_SETTINGS=$tmp/absent.json dr_desktop_autostart_setting 2>/dev/null
  check "no settings file yet (first install) is not an error" test ! -e "$tmp/absent.json"
else
  skp "macOS login agent and Docker Desktop setting" "not macOS"
fi

# The real engine on this machine: only looks, never starts or stops it.
if dr_find_docker && dr_docker_answers; then
  check "dr_find_docker + dr_docker_answers on this machine's Docker" true
  # Full run against the live engine: it answers, so nothing is started.
  # Autostart writes go to the temp dir (macOS) or run dry (Linux).
  if [[ "$os" == macos ]]; then
    DR_LAUNCH_AGENTS_DIR=$tmp/live-agents DR_DOCKER_SETTINGS=$tmp/settings.json docker_ready 2> "$tmp/live"
  else
    DR_DRY_RUN=1 docker_ready 2> "$tmp/live"
  fi
  rc=$?
  check "docker_ready with Docker already running returns 0 (rc=$rc)" test "$rc" = 0
  check "docker_ready did not try to start a running Docker" not_in "Waiting for Docker" "$tmp/live"
  sed 's/^/      | /' "$tmp/live"
else
  skp "live engine checks" "no running Docker here"
fi

printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skip"
((fail == 0))
