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

newenv() { printf 'PIHOLE_PASSWORD=x\nTZ=UTC\n' > "$1"; }
# docker stand-ins for `docker inspect sovereign-dns-nvpn`.
no_box() { echo "Error response from daemon: No such object: sovereign-dns-nvpn" >&2; return 1; }
box_on() { printf '{"51820/udp":[{"HostIp":"","HostPort":"%s"}]}\n' "$1"; }

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

  # The Nostr VPN app holds 51820 (here: $base) and there is no box yet.
  newenv "$tmp/a.env"
  out=$(docker() { no_box "$@"; }; FP_DEFAULT_PORT=$base nvpn_listen_port "$tmp/a.env" 2> "$tmp/msg")
  check "fresh install moves off a taken default (got $out)" test "$out" = "$((base + 1))"
  check "  and says which port and why" grep -q "UDP $base is already in use" "$tmp/msg"
  check "  and saves it in .env" grep -qx "NVPN_LISTEN_PORT=$((base + 1))" "$tmp/a.env"
  sed 's/^/      | /' "$tmp/msg"

  # The box already exists on $base (its own port is held, here by our
  # holder) but Docker can't be read: probing would move it. Must refuse.
  newenv "$tmp/b.env"
  out=$(docker() { echo "permission denied while trying to connect to the docker API" >&2; return 1; }
    FP_DEFAULT_PORT=$base nvpn_listen_port "$tmp/b.env" 2> "$tmp/msg"); rc=$?
  check "box exists, Docker unreadable: returns 1 (rc=$rc)" test "$rc" = 1
  check "  and prints no port (got '$out')" test -z "$out"
  check "  and writes nothing to .env" not_in NVPN_LISTEN_PORT "$tmp/b.env"
  sed 's/^/      | /' "$tmp/msg"
  kill "$holder_pid" 2>/dev/null; wait "$holder_pid" 2>/dev/null; holder_pid=""
else
  skp "held-port checks" "no perl to hold a port"
fi

newenv "$tmp/c.env"
out=$(docker() { no_box "$@"; }; FP_DEFAULT_PORT=$base nvpn_listen_port "$tmp/c.env" 2>/dev/null)
check "fresh install keeps a free default (got $out)" test "$out" = "$base"
check "  and saves it in .env" grep -qx "NVPN_LISTEN_PORT=$base" "$tmp/c.env"
out=$(docker() { box_on 51821; }; FP_DEFAULT_PORT=$base nvpn_listen_port "$tmp/c.env" 2>/dev/null)
check "  a later run keeps the saved port, whatever Docker says (got $out)" test "$out" = "$base"
check "  and doesn't add a second line" test "$(grep -c NVPN_LISTEN_PORT "$tmp/c.env")" = 1

newenv "$tmp/d.env"; printf 'NVPN_LISTEN_PORT=51999\n' >> "$tmp/d.env"
out=$(nvpn_listen_port "$tmp/d.env" 2>/dev/null)
check "keeps the port saved in .env (got $out)" test "$out" = 51999

# An existing box (running or stopped: docker inspect sees both) without the
# port in .env, i.e. every install from before this file existed.
newenv "$tmp/e.env"
out=$(docker() { box_on 51821; }; nvpn_listen_port "$tmp/e.env" 2>/dev/null)
check "existing box keeps its port (got $out)" test "$out" = 51821
check "  and saves it in .env" grep -qx "NVPN_LISTEN_PORT=51821" "$tmp/e.env"

newenv "$tmp/f.env"
out=$(docker() { echo "Cannot connect to the Docker daemon" >&2; return 1; }; nvpn_listen_port "$tmp/f.env" 2>/dev/null); rc=$?
check "Docker not answering: returns 1 and prints no port (rc=$rc)" test "$rc" = 1 -a -z "$out"

nvpn_listen_port "$tmp/missing.env" > /dev/null 2>&1; rc=$?
check "no .env yet: returns 1 and doesn't create it (rc=$rc)" test "$rc" = 1 -a ! -e "$tmp/missing.env"

printf 'TZ=UTC' > "$tmp/g.env" # no trailing newline
docker() { box_on 51821; }; nvpn_listen_port "$tmp/g.env" > /dev/null 2>&1; unset -f docker
check ".env without a final newline gets a separate line" test "$(sed -n 2p "$tmp/g.env")" = NVPN_LISTEN_PORT=51821

# ------------------------------------------------------------- docker-ready

check "dr_os is known ($os)" test "$os" = macos -o "$os" = linux -o "$os" = wsl

printf 'hello\n' > "$tmp/f"
good=$(dr_sha256 "$tmp/f")
check "dr_verify_sha256 accepts the right hash" dr_verify_sha256 "$tmp/f" "$good"
dr_verify_sha256 "$tmp/f" "0000$good" 2> "$tmp/msg"; rc=$?
check "dr_verify_sha256 refuses a wrong hash (rc=$rc)" test "$rc" = 1
check "  and says so" grep -q "Checksum mismatch" "$tmp/msg"

# The real install functions, with the download and every privileged step
# replaced by a recorder. A download with the wrong hash must stop before
# anything runs; the same download with its hash pinned must go through
# (that second run is what proves the first one stops at the hash check).
# install_gate FUNC [VAR=HASH]: prints the steps that ran, returns FUNC's rc.
install_gate() {
  local fn=$1 pin=${2:-}
  (
    calls=$tmp/calls; : > "$calls"
    dr_download() { printf 'not the real file\n' > "$2"; }
    hdiutil() { echo "hdiutil $1" >> "$calls"; }
    sudo() { echo "sudo $*" >> "$calls"; }
    sh() { echo "sh $*" >> "$calls"; }
    dr_sudo() { echo "dr_sudo $*" >> "$calls"; }
    sw_vers() { echo 15.0; }
    uname() { if [[ "${1:-}" == -m ]]; then echo arm64; else command uname "$@"; fi; }
    id() { if [[ "${1:-}" == -u ]]; then echo 501; else command id "$@"; fi; }
    [[ -n "$pin" ]] && eval "$pin"
    "$fn" 2>/dev/null; rc=$?
    cat "$calls"
    exit "$rc"
  )
}
fake=$(printf 'not the real file\n' > "$tmp/fake"; dr_sha256 "$tmp/fake")

out=$(install_gate dr_install_docker_mac); rc=$?
check "macOS install with a wrong hash returns 1 (rc=$rc)" test "$rc" = 1
check "  and runs nothing (ran: '${out//$'\n'/; }')" test -z "$out"
out=$(install_gate dr_install_docker_mac "DR_MAC_DESKTOP_SHA256_ARM64=$fake"); rc=$?
check "control: the same download with its hash pinned mounts and installs" grep -q "sudo .*/Docker.app/Contents/MacOS/install --accept-license" <<< "$out"

out=$(install_gate dr_install_docker_linux); rc=$?
check "Linux install with a wrong hash returns 1 (rc=$rc)" test "$rc" = 1
check "  and runs nothing (ran: '${out//$'\n'/; }')" test -z "$out"
out=$(install_gate dr_install_docker_linux "DR_GET_DOCKER_SHA256=$fake"); rc=$?
check "control: the same download with its hash pinned runs get-docker" grep -q "dr_sudo sh .*get-docker.sh --version $DR_LINUX_ENGINE_VERSION" <<< "$out"
check "  pinned to an exact engine release ($DR_LINUX_ENGINE_VERSION)" grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$' <<< "$DR_LINUX_ENGINE_VERSION"
check "  and adds the user to the docker group" grep -q "dr_sudo usermod -aG docker" <<< "$out"
out=$(DR_LINUX_ENGINE_VERSION=1.0 bash -c 'source "$1"; echo "$DR_LINUX_ENGINE_VERSION"' _ "$here/docker-ready.sh")
check "the engine pin can't be overridden from the environment (got $out)" test "$out" = "$DR_LINUX_ENGINE_VERSION"

# docker_ready itself, not dry: Docker already answering must not be started,
# and Docker not answering must be. Steps are stubs; nothing real runs.
# ready_run ANSWERS(0|1): prints the steps docker_ready took.
ready_run() {
  (
    steps=$tmp/steps; : > "$steps"; answers=$1
    dr_find_docker() { return 0; }
    dr_docker_answers() { return "$answers"; }
    dr_start_docker() { echo start >> "$steps"; }
    dr_wait_docker() { echo wait >> "$steps"; }
    dr_enable_autostart() { echo autostart >> "$steps"; }
    docker() { return 0; }
    docker_ready 2>/dev/null || echo "rc=$?" >> "$steps"
    tr '\n' ' ' < "$steps"
  )
}
out=$(ready_run 0)
check "docker_ready, Docker already running: no start, autostart on (did: $out)" test "$out" = "autostart "
out=$(ready_run 1)
check "control: docker_ready, Docker not running: starts and waits (did: $out)" test "$out" = "start wait autostart "

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

(PATH=$nodocker; DR_LAUNCH_AGENTS_DIR=$tmp/colima-agents dr_write_launch_agent colima 2> "$tmp/msg"); rc=$?
check "colima not on PATH: no login agent, returns 1 (rc=$rc)" test "$rc" = 1 -a ! -e "$tmp/colima-agents/$DR_AGENT_LABEL.plist"

# The real engine on this machine: only looks, never starts or stops it.
if dr_find_docker && dr_docker_answers; then
  check "dr_find_docker + dr_docker_answers on this machine's Docker" true
  # Full run against the live engine: it answers, so nothing is started.
  # dr_start_docker is replaced so a mistake can't touch the real engine.
  # Autostart writes go to the temp dir (macOS) or are skipped (Linux).
  (
    dr_start_docker() { echo "STARTED" >&2; }
    [[ "$os" == macos ]] || dr_enable_autostart() { return 0; }
    DR_LAUNCH_AGENTS_DIR=$tmp/live-agents DR_DOCKER_SETTINGS=$tmp/settings.json docker_ready
  ) 2> "$tmp/live"
  rc=$?
  check "docker_ready with Docker already running returns 0 (rc=$rc)" test "$rc" = 0
  check "  and did not try to start it" not_in STARTED "$tmp/live"
  sed 's/^/      | /' "$tmp/live"
  # Read-only: what this machine's box would keep.
  if docker inspect sovereign-dns-nvpn > /dev/null 2>&1; then
    want=$(docker inspect sovereign-dns-nvpn --format '{{(index (index .HostConfig.PortBindings "51820/udp") 0).HostPort}}')
    newenv "$tmp/live.env"
    out=$(nvpn_listen_port "$tmp/live.env" 2> "$tmp/msg")
    check "this machine's box keeps its port $want (got $out)" test "$out" = "$want"
    sed 's/^/      | /' "$tmp/msg"
  fi
else
  skp "live engine checks" "no running Docker here"
fi

printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skip"
((fail == 0))
