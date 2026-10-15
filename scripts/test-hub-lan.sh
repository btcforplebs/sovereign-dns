#!/usr/bin/env bash
# Tests for scripts/hub-lan.sh and the installer's "open the dashboard to your
# home network" step, run through the real scripts/install.sh, and for
# scripts/hub-mesh.sh, which shares the dashboard password with it.
#
#   bash scripts/test-hub-lan.sh
#
# Safe anywhere: docker, curl and nc are stand-ins that record what would have
# run, and everything happens in a temp dir. The Ctrl-D cases need `expect`
# (for a terminal to press it in) and skip without it.
# Regenerate install.sh first (scripts/make-installer.sh): this runs the
# committed one, the file people download.
set -u
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

pass=0 fail=0 skip=0
ok() { pass=$((pass + 1)); printf 'ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL  %s\n' "$1"; }
skp() { skip=$((skip + 1)); printf 'skip  %s (%s)\n' "$1" "$2"; }
check() { local name=$1; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
bin=$tmp/bin
mkdir -p "$bin"
cat >"$bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >>"$FAKE_LOG"
if [[ "$1" == info && "${2:-}" == --format ]]; then echo "${FAKE_DOCKER_OS:-Debian GNU/Linux 12 (bookworm)}"; fi
exit 0
EOF
printf '#!/bin/sh\nexit 0\n' >"$bin/curl"
printf '#!/bin/sh\nexit 1\n' >"$bin/nc"
chmod +x "$bin"/*
export FAKE_LOG=$tmp/docker.log
PATH=$bin:$PATH

# A fresh folder per case. $1 names it; the installer runs inside it.
fresh() { rm -rf "$tmp/$1"; mkdir -p "$tmp/$1"; : >"$FAKE_LOG"; }
env_has() { grep -qx -- "$2" "$1/sovereign-dns/.env"; }
ran() { grep -q -- "$1" "$FAKE_LOG"; }

# ── The installer, no terminal: the env answers ────────────────────────────
fresh a
(cd "$tmp/a" && echo pihole-pw | SOVEREIGN_DNS_HUB_LAN=1 SOVEREIGN_DNS_HUB_PASSWORD='hub pass $HOME' \
   bash "$root/scripts/install.sh" >"$tmp/a.out" 2>&1)
st=$?
check "installer with SOVEREIGN_DNS_HUB_LAN=1 exits 0" test "$st" = 0
check "  ...and still starts the stack" ran "compose up -d --build"
check "  ...writes HUB_BIND=0.0.0.0" env_has "$tmp/a" "HUB_BIND=0.0.0.0"
check "  ...writes the password single-quoted, \$ and all" env_has "$tmp/a" "HUB_PASSWORD='hub pass \$HOME'"
check "  ...keeps .env at 0600" test "$(stat -f %Lp "$tmp/a/sovereign-dns/.env" 2>/dev/null || stat -c %a "$tmp/a/sovereign-dns/.env")" = 600
check "  ...prints the home-network address" grep -q "sign in with the dashboard password" "$tmp/a.out"

fresh b
(cd "$tmp/b" && echo pihole-pw | SOVEREIGN_DNS_HUB_LAN=0 bash "$root/scripts/install.sh" >"$tmp/b.out" 2>&1)
st=$?
check "installer with SOVEREIGN_DNS_HUB_LAN=0 exits 0" test "$st" = 0
check "  ...and still starts the stack" ran "compose up -d --build"
check "  ...records HUB_BIND=127.0.0.1, so it never asks again" env_has "$tmp/b" "HUB_BIND=127.0.0.1"

fresh c
(cd "$tmp/c" && echo pihole-pw | DISPLAY= WAYLAND_DISPLAY= SSH_CONNECTION=1 bash "$root/scripts/install.sh" >"$tmp/c.out" 2>&1)
st=$?
check "installer with no terminal and no answer exits 0" test "$st" = 0
check "  ...asks nothing and leaves HUB_BIND unset" bash -c '! grep -q "^HUB_BIND=" "$1"' _ "$tmp/c/sovereign-dns/.env"

# ── hub-lan.sh on its own ──────────────────────────────────────────────────
d=$tmp/c/sovereign-dns
: >"$FAKE_LOG"
(cd "$d" && SOVEREIGN_DNS_HUB_PASSWORD=abcdefgh9 bash scripts/hub-lan.sh on --no-restart </dev/null >/dev/null 2>&1)
check "hub-lan.sh on --no-restart exits 0" test "$?" = 0
check "  ...and restarts nothing" bash -c '! grep -q "compose up" "$1"' _ "$FAKE_LOG"
(cd "$d" && bash scripts/hub-lan.sh off --no-restart </dev/null >/dev/null 2>&1)
check "hub-lan.sh off --no-restart exits 0" test "$?" = 0
check "  ...and drops the password" bash -c '! grep -q "^HUB_PASSWORD=" "$1"' _ "$d/.env"
(cd "$d" && SOVEREIGN_DNS_HUB_PASSWORD=abcdefgh9 bash scripts/hub-lan.sh on </dev/null >"$tmp/on.out" 2>&1)
check "hub-lan.sh on exits 0" test "$?" = 0
check "  ...and restarts only the hub" ran "compose up -d hub"
check "  ...says nothing about Docker Desktop on Linux" bash -c '! grep -q "Docker Desktop" "$1"' _ "$tmp/on.out"
(cd "$d" && FAKE_DOCKER_OS="Docker Desktop" SOVEREIGN_DNS_HUB_PASSWORD=abcdefgh9 bash scripts/hub-lan.sh on --no-restart </dev/null >"$tmp/dd.out" 2>&1)
check "on Docker Desktop, says the password is the only guard" grep -q "password is the only thing guarding it" "$tmp/dd.out"
cp "$d/.env" "$tmp/env.before"
(cd "$d" && SOVEREIGN_DNS_HUB_PASSWORD=short bash scripts/hub-lan.sh on </dev/null >/dev/null 2>&1)
check "a short password is refused (exit 1)" test "$?" = 1
check "  ...and .env is untouched" cmp -s "$tmp/env.before" "$d/.env"

# ── Ctrl-D at a prompt: back to this computer only, install carries on ────
if command -v expect >/dev/null 2>&1; then
  cat >"$tmp/eof.exp" <<'EOF'
set timeout 20
set match [lindex $argv 0]
spawn {*}[lrange $argv 1 end]
expect {
  "Pi-hole admin dashboard" { send "pihole-pw\r"; exp_continue }
  -exact {[Y/n] } { send "y\r"; exp_continue }
  $match { send "\004"; exp_continue }
  eof
}
catch wait result
exit [lindex $result 3]
EOF
  (cd "$d" && expect "$tmp/eof.exp" "at least 8 characters" bash scripts/hub-lan.sh on --no-restart >/dev/null 2>&1)
  check "Ctrl-D at hub-lan.sh's password prompt exits 0" test "$?" = 0
  check "  ...and leaves the dashboard on this computer only" bash -c 'grep -qx "HUB_BIND=127.0.0.1" "$1" && ! grep -q "^HUB_PASSWORD=" "$1"' _ "$d/.env"

  fresh e
  (cd "$tmp/e" && expect "$tmp/eof.exp" "at least 8 characters" \
     env DISPLAY= WAYLAND_DISPLAY= SSH_CONNECTION=1 bash "$root/scripts/install.sh" >/dev/null 2>&1)
  check "Ctrl-D at the installer's dashboard password exits 0" test "$?" = 0
  check "  ...and still starts the stack" ran "compose up -d --build"
  check "  ...on this computer only" env_has "$tmp/e" "HUB_BIND=127.0.0.1"

  # The [Y/n] question itself: answer with Ctrl-D instead of y.
  sed 's/{ send "y\\r"; exp_continue }/{ send "\\004"; exp_continue }/' "$tmp/eof.exp" >"$tmp/eof-q.exp"
  fresh g
  (cd "$tmp/g" && expect "$tmp/eof-q.exp" "NO-SUCH-PROMPT" \
     env DISPLAY= WAYLAND_DISPLAY= SSH_CONNECTION=1 bash "$root/scripts/install.sh" >/dev/null 2>&1)
  check "Ctrl-D at the installer's [Y/n] exits 0, as a no" test "$?" = 0
  check "  ...on this computer only" env_has "$tmp/g" "HUB_BIND=127.0.0.1"
else
  skp "Ctrl-D cases" "no expect"
fi

# ── hub-mesh.sh, and the one password it shares with hub-lan.sh ──────────
fresh m
(cd "$tmp/m" && echo pihole-pw | SOVEREIGN_DNS_HUB_LAN=0 bash "$root/scripts/install.sh" >/dev/null 2>&1)
m=$tmp/m/sovereign-dns
no_pw() { ! grep -q '^HUB_PASSWORD=' "$1/.env"; }
: >"$FAKE_LOG"
(cd "$m" && bash scripts/hub-mesh.sh on </dev/null >/dev/null 2>&1)
check "hub-mesh.sh on with no password and no terminal exits 1" test "$?" = 1
check "  ...and opens nothing" bash -c '! grep -q "^HUB_MESH=on" "$1"' _ "$m/.env"
(cd "$m" && SOVEREIGN_DNS_HUB_PASSWORD='mesh pass $HOME' bash scripts/hub-mesh.sh on </dev/null >"$tmp/mesh-on.out" 2>&1)
check "hub-mesh.sh on exits 0" test "$?" = 0
check "  ...writes HUB_MESH=on" env_has "$tmp/m" "HUB_MESH=on"
check "  ...writes the password single-quoted, \$ and all" env_has "$tmp/m" "HUB_PASSWORD='mesh pass \$HOME'"
check "  ...leaves the dashboard off the home network" env_has "$tmp/m" "HUB_BIND=127.0.0.1"
check "  ...keeps .env at 0600" test "$(stat -f %Lp "$m/.env" 2>/dev/null || stat -c %a "$m/.env")" = 600
check "  ...restarts the hub and its mesh gate, not the VPN" bash -c 'grep -q "compose up -d hub hub-mesh$" "$1" && ! grep -q "up -d.*nvpn" "$1"' _ "$FAKE_LOG"
check "  ...and says where Pi-hole is" grep -q "/admin" "$tmp/mesh-on.out"
(cd "$m" && bash scripts/hub-mesh.sh on --no-restart </dev/null >"$tmp/mesh-again.out" 2>&1)
check "hub-mesh.sh on again reuses the password, no terminal needed" test "$?" = 0
check "  ...and says so" grep -q "already picked" "$tmp/mesh-again.out"
check "  ...with exactly one HUB_MESH line" test "$(grep -c '^HUB_MESH=' "$m/.env")" = 1
(cd "$m" && SOVEREIGN_DNS_HUB_PASSWORD=lanpass99 bash scripts/hub-lan.sh on --no-restart </dev/null >/dev/null 2>&1)
check "hub-lan.sh on while the mesh is open keeps HUB_MESH=on" env_has "$tmp/m" "HUB_MESH=on"
(cd "$m" && bash scripts/hub-lan.sh off --no-restart </dev/null >/dev/null 2>&1)
check "hub-lan.sh off while the mesh is open keeps the password" env_has "$tmp/m" "HUB_PASSWORD='lanpass99'"
check "  ...and closes the home network" env_has "$tmp/m" "HUB_BIND=127.0.0.1"
(cd "$m" && SOVEREIGN_DNS_HUB_PASSWORD=lanpass99 bash scripts/hub-lan.sh on --no-restart </dev/null >/dev/null 2>&1)
(cd "$m" && bash scripts/hub-mesh.sh off --no-restart </dev/null >/dev/null 2>&1)
check "hub-mesh.sh off while the home network is open keeps the password" env_has "$tmp/m" "HUB_PASSWORD='lanpass99'"
check "  ...and writes HUB_MESH=off" env_has "$tmp/m" "HUB_MESH=off"
(cd "$m" && bash scripts/hub-lan.sh off --no-restart </dev/null >/dev/null 2>&1)
check "both off: no password left" no_pw "$m"
(cd "$m" && SOVEREIGN_DNS_HUB_PASSWORD=meshpass9 bash scripts/hub-mesh.sh on --no-restart </dev/null >/dev/null 2>&1)
(cd "$m" && bash scripts/hub-mesh.sh off --no-restart </dev/null >/dev/null 2>&1)
check "hub-mesh.sh off alone drops the password" no_pw "$m"
cp "$m/.env" "$tmp/env.before"
(cd "$m" && SOVEREIGN_DNS_HUB_PASSWORD=short bash scripts/hub-mesh.sh on </dev/null >/dev/null 2>&1)
check "hub-mesh.sh on refuses a short password (exit 1)" test "$?" = 1
check "  ...and .env is untouched" cmp -s "$tmp/env.before" "$m/.env"

# A hand-edited .env, quoted the way compose also accepts. The scripts must
# read it as the hub does, or off would drop a password the hub still needs.
printf '%s\n' 'HUB_MESH="on"' 'HUB_BIND=127.0.0.1' 'HUB_PASSWORD="hand typed pw"' >"$m/.env"
(cd "$m" && bash scripts/hub-lan.sh off --no-restart </dev/null >/dev/null 2>&1)
check "hub-lan.sh off with HUB_MESH=\"on\" keeps a hand-quoted password" env_has "$tmp/m" 'HUB_PASSWORD="hand typed pw"'
printf '%s\n' 'HUB_MESH=on' 'HUB_BIND="0.0.0.0"' "HUB_PASSWORD='lanpass99'" >"$m/.env"
(cd "$m" && bash scripts/hub-mesh.sh off --no-restart </dev/null >/dev/null 2>&1)
check "hub-mesh.sh off with HUB_BIND=\"0.0.0.0\" keeps the password" env_has "$tmp/m" "HUB_PASSWORD='lanpass99'"
printf '%s\n' 'HUB_BIND=127.0.0.1' "HUB_PASSWORD=''" >"$m/.env"
(cd "$m" && bash scripts/hub-mesh.sh on --no-restart </dev/null >/dev/null 2>&1)
check "hub-mesh.sh on does not reuse an empty HUB_PASSWORD (exit 1, no terminal)" test "$?" = 1

# ── dns-lan.sh: Docker Desktop cannot check senders, so it refuses there ──
fresh n
(cd "$tmp/n" && echo pihole-pw | SOVEREIGN_DNS_HUB_LAN=0 bash "$root/scripts/install.sh" >/dev/null 2>&1)
dl=$tmp/n/sovereign-dns
cp "$dl/.env" "$tmp/env.before"
: >"$FAKE_LOG"
(cd "$dl" && FAKE_DOCKER_OS="Docker Desktop" SOVEREIGN_DNS_LAN_IP=192.168.1.20 bash scripts/dns-lan.sh on </dev/null >"$tmp/dl-dd.out" 2>&1)
check "dns-lan.sh on under Docker Desktop refuses (exit 1)" test "$?" = 1
check "  ...and .env is untouched" cmp -s "$tmp/env.before" "$dl/.env"
check "  ...and starts nothing" bash -c '! grep -q "compose up" "$1"' _ "$FAKE_LOG"
check "  ...says no sender check is possible (open resolver)" grep -q "cannot tell your home network from the internet" "$tmp/dl-dd.out"
check "  ...says the house is one client to the rate limit" grep -q "5000 lookups a minute applies to every device" "$tmp/dl-dd.out"
check "  ...and how to override" grep -q "SOVEREIGN_DNS_LAN_DESKTOP=1 bash scripts/dns-lan.sh on" "$tmp/dl-dd.out"
(cd "$dl" && FAKE_DOCKER_OS="Docker Desktop" SOVEREIGN_DNS_LAN_DESKTOP=1 SOVEREIGN_DNS_LAN_IP=192.168.1.20 bash scripts/dns-lan.sh on --no-restart </dev/null >"$tmp/dl-ddo.out" 2>&1)
check "dns-lan.sh on under Docker Desktop with the override exits 0" test "$?" = 0
check "  ...turns it on" env_has "$tmp/n" "COMPOSE_PROFILES=dns-lan"
check "  ...still shows the warning" grep -q "^WARNING: this box runs Docker Desktop" "$tmp/dl-ddo.out"
(cd "$dl" && bash scripts/dns-lan.sh off --no-restart </dev/null >/dev/null 2>&1)
(cd "$dl" && SOVEREIGN_DNS_LAN_IP=192.168.1.20 bash scripts/dns-lan.sh on --no-restart </dev/null >"$tmp/dl-linux.out" 2>&1)
check "dns-lan.sh on on Linux exits 0 with no override" test "$?" = 0
check "  ...turns it on at that address" env_has "$tmp/n" "DNS_LAN_BIND=192.168.1.20"
check "  ...with no Docker Desktop warning" bash -c '! grep -q "Docker Desktop" "$1"' _ "$tmp/dl-linux.out"

printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skip"
[[ "$fail" == 0 ]]
