#!/usr/bin/env bash
# Tests for scripts/lib/low-memory.sh. Needs no Docker and touches nothing real:
# memory figures come from a fake /proc/meminfo, and swap commands are replaced
# by stand-ins that record what would have run.
#
#   bash scripts/test-low-memory.sh
set -u
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib/low-memory.sh
source "$here/lib/low-memory.sh"

pass=0 fail=0
ok() { pass=$((pass + 1)); printf 'ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL  %s\n' "$1"; }
check() { local name=$1; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }
has() { grep -q -- "$1" "$2"; }
lacks() { ! grep -q -- "$1" "$2"; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
meminfo() { printf 'MemTotal:  %s kB\nSwapTotal: %s kB\n' "$1" "$2" > "$tmp/meminfo"; export LM_MEMINFO=$tmp/meminfo; }

# Stand-ins first on PATH: nothing runs for real.
bin=$tmp/bin; mkdir -p "$bin"
for c in dd chmod mkswap swapon; do
  printf '#!/bin/sh\necho "%s $*" >> "%s/ran"\n' "$c" "$tmp" > "$bin/$c"; chmod +x "$bin/$c"
done
printf '#!/bin/sh\nexit 1\n' > "$bin/swapon-fails"
# sudo just runs its command, so a test never asks for a password or gets root.
printf '#!/bin/sh\nexec "$@"\n' > "$bin/sudo"; chmod +x "$bin/sudo"
PATH=$bin:$PATH
export LM_SWAPFILE=$tmp/swapfile
uname() { echo Linux; }   # the function wins over the binary in this shell

run() { : > "$tmp/ran"; lm_low_memory_check > "$tmp/out" 2>&1 < "${1:-/dev/null}"; }

meminfo 480000 0
check "lm_mem_kb reads MemTotal" test "$(lm_mem_kb)" = 480000
check "lm_swap_kb reads SwapTotal" test "$(lm_swap_kb)" = 0
LM_MEMINFO=$tmp/missing
check "an unreadable meminfo is 0, not an error" test "$(lm_mem_kb)" = 0
meminfo 480000 0

# 512 MB, nobody at the terminal, no answer: warns, changes nothing.
run
check "512 MB warns" has "about 468 MB" "$tmp/out"
check "  mentions fwupd" has fwupd "$tmp/out"
check "  with no terminal and no answer, adds no swap" lacks swapon "$tmp/ran"
check "  and says how to get the offer" has SOVEREIGN_DNS_SWAP "$tmp/out"

# Answer set: yes.
SOVEREIGN_DNS_SWAP=1 run
check "SOVEREIGN_DNS_SWAP=1 creates and enables the file" has "swapon $tmp/swapfile" "$tmp/ran"
check "  as mode 600" has "chmod 600" "$tmp/ran"
check "  and reports it" has "Swap is on" "$tmp/out"
rm -f "$tmp/swapfile"

# Answer set: no.
SOVEREIGN_DNS_SWAP=0 run
check "SOVEREIGN_DNS_SWAP=0 adds nothing" lacks swapon "$tmp/ran"

# An existing file is never overwritten.
: > "$tmp/swapfile"
SOVEREIGN_DNS_SWAP=1 run
check "an existing swap file is left alone" lacks "dd " "$tmp/ran"
rm -f "$tmp/swapfile"

# Swap already on: nothing to offer.
meminfo 480000 524288
SOVEREIGN_DNS_SWAP=1 run
check "existing swap: no new swap" lacks swapon "$tmp/ran"
check "  and it says so" has "already has swap" "$tmp/out"

# Enough memory: silent.
meminfo 2000000 0
SOVEREIGN_DNS_SWAP=1 run
check "2 GB: prints nothing" test ! -s "$tmp/out"
meminfo 1000000 0
run
check "a 1 GB VPS (about 976 MB reported) is warned" has "MB of memory" "$tmp/out"
meminfo 1100000 0
run
check "just over 1 GiB is not" test ! -s "$tmp/out"

# swapon refusing (a container): carries on, leaves no file behind.
meminfo 480000 0
cp "$bin/swapon-fails" "$bin/swapon"
SOVEREIGN_DNS_SWAP=1 run
check "swapon failing is reported, not fatal" has "Carrying on without it" "$tmp/out"
check "  and removes the half-made file" test ! -e "$tmp/swapfile"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
((fail == 0))
