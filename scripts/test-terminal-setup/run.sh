#!/bin/bash
# Fresh-install test for PR 30 (terminal device setup). Self-contained:
#   bash run.sh /path/to/install.sh
# Brings up two throwaway docker-in-docker "computers" (project
# tom-termsetup), installs Sovereign-DNS on each from scratch, and drives both
# terminal setups with expect (drive.exp): box A is a first box and adds a
# phone and box B; box B joins A with A's join code and waits until added.
# Touches no other container. Needs Docker, expect, and the internet (images,
# public FIPS seeds). Tears everything down at the end, KEEP=1 leaves it up.
set -uo pipefail
cd "$(dirname "$0")"
[[ -f "${1:-}" ]] || { echo "usage: bash run.sh /path/to/install.sh"; exit 2; }
command -v expect >/dev/null || { echo "needs expect"; exit 2; }
DC=(docker compose -p tom-termsetup -f compose.yml)
cleanup() { [[ "${KEEP:-0}" == 1 ]] || "${DC[@]}" down -v -t 2 >/dev/null 2>&1; }
trap cleanup EXIT
echo "disk before:"; df -h / | tail -1
mkdir -p work && cp "$1" work/install.sh
"${DC[@]}" up -d >/dev/null || exit 1
for b in boxa boxb; do "${DC[@]}" exec -T "$b" sh < box-prep.sh || exit 1; done
# A phone's join link, made the way a computer makes one: a fresh nvpn identity
# and `nvpn join-request --no-qr`, using the same nvpn image the boxes run.
"${DC[@]}" exec -T boxa sh -c 'mkdir -p /tmp/x && cd /tmp/x && tail -n +$(( $(awk "/^__PAYLOAD_BELOW__\$/{print NR;exit}" /work/install.sh)+1 )) /work/install.sh | base64 -d | tar -xf - docker-compose.prebuilt.yml' || exit 1
img=$("${DC[@]}" exec -T boxa sh -c "awk '\$0 == \"  nvpn:\" { a = 1; next } a && /^    image: / { print \$2; exit }' /tmp/x/docker-compose.prebuilt.yml")
echo "nvpn image: $img"
phone=$("${DC[@]}" exec -T boxa sh -c "docker run --rm --entrypoint sh '$img' -c 'nvpn init --force >/dev/null 2>&1; nvpn join-request --no-qr 2>/dev/null' | grep -m1 -o 'nvpn://join-request/[A-Za-z0-9_-]*'")
[[ "$phone" == nvpn://join-request/* ]] || { echo "could not make a phone join link"; exit 1; }
echo "phone link made (${#phone} chars)"
expect drive.exp "$phone"; rc=$?
echo "== drive exit $rc"
for b in boxa boxb; do
  echo "== $b"; "${DC[@]}" exec -T "$b" sh -c 'cd /root/sovereign-dns && docker compose ps --format "{{.Name}} {{.Status}}" && docker compose exec -T nvpn nvpn status 2>&1 | grep -E "network:|network_dns|peers:"'
done
echo "disk after:"; df -h / | tail -1
exit $rc
