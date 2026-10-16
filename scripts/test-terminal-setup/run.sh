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
DC=(docker compose -p tom-termsetup -f boxes.yml)
cleanup() { [[ "${KEEP:-0}" == 1 ]] || "${DC[@]}" down -v -t 2 >/dev/null 2>&1; }
trap cleanup EXIT
echo "disk before:"; df -h / | tail -1
mkdir -p work && cp "$1" work/install.sh
"${DC[@]}" up -d >/dev/null || exit 1
for b in boxa boxb; do "${DC[@]}" exec -T "$b" sh < box-prep.sh || exit 1; done
# A phone's join link, made the way a computer makes one: a fresh nvpn identity
# and `nvpn join-request --no-qr`, using the same nvpn image the boxes run. On
# Linux join-request asks the running daemon for the link, so start one (it
# starts switched off, no tunnel) and do not wait for approval.
"${DC[@]}" exec -T boxa sh -c 'mkdir -p /tmp/x && cd /tmp/x && tail -n +$(( $(awk "/^__PAYLOAD_BELOW__\$/{print NR;exit}" /work/install.sh)+1 )) /work/install.sh | base64 -d | tar -xf - docker-compose.prebuilt.yml' || exit 1
img=$("${DC[@]}" exec -T boxa sh -c "awk '\$0 == \"  nvpn:\" { a = 1; next } a && /^    image: / { print \$2; exit }' /tmp/x/docker-compose.prebuilt.yml")
echo "nvpn image: $img"
phoneout=$("${DC[@]}" exec -T boxa sh -c "docker run --rm --cap-add NET_ADMIN --device /dev/net/tun --entrypoint sh '$img' -c 'nvpn init --force >/dev/null 2>&1; nvpn start --daemon 2>&1; for i in 1 2 3 4 5 6 7 8 9 10; do nvpn join-request --no-wait --no-qr 2>&1 && break; sleep 2; done'")
phone=$(grep -m1 -o 'nvpn://join-request/[A-Za-z0-9_-]*' <<<"$phoneout")
[[ "$phone" == nvpn://join-request/* ]] || { echo "could not make a phone join link:"; echo "$phoneout"; exit 1; }
echo "phone link made (${#phone} chars)"
expect drive.exp "$phone"; rc=$?
echo "== drive exit $rc"
# Across the mesh, the way a phone or the other box asks: each box looks up a
# name and an ad domain at the other's mesh address.
meship() { "${DC[@]}" exec -T "$1" sh -c 'cd /root/sovereign-dns && docker compose exec -T nvpn ip -4 -o addr show utun100' | awk '{print $4}' | cut -d/ -f1; }
cross() { # cross <from box> <to ip>
  local n ad
  n=$("${DC[@]}" exec -T "$1" sh -c "cd /root/sovereign-dns && docker compose exec -T nvpn dig +short +time=3 +tries=2 @$2 example.com A" | grep -E '^[0-9.]+$' | head -1)
  ad=$("${DC[@]}" exec -T "$1" sh -c "cd /root/sovereign-dns && docker compose exec -T nvpn dig +short +time=3 +tries=2 @$2 doubleclick.net A" | head -1)
  if [[ -n "$n" && "$ad" == 0.0.0.0 ]]; then echo "ok    $1 -> $2 answers and blocks ads"; else echo "FAIL  $1 -> $2: example.com '$n', doubleclick.net '$ad'"; rc=1; fi
}
if [[ "$rc" == 0 ]]; then
  ipa=$(meship boxa) ipb=$(meship boxb)
  echo "mesh: A $ipa, B $ipb"
  for _ in $(seq 1 20); do  # the two boxes' direct path can take a moment
    "${DC[@]}" exec -T boxa sh -c "cd /root/sovereign-dns && docker compose exec -T nvpn dig +short +time=2 +tries=1 @$ipb example.com A" | grep -qE '^[0-9.]+$' && break
    sleep 3
  done
  cross boxa "$ipb"
  cross boxb "$ipa"
fi
for b in boxa boxb; do
  echo "== $b"; "${DC[@]}" exec -T "$b" sh -c 'cd /root/sovereign-dns && docker compose ps --format "{{.Name}} {{.Status}}" && docker compose exec -T nvpn nvpn status 2>&1 | grep -E "network:|network_dns|peers:"'
done
echo "disk after:"; df -h / | tail -1
exit $rc
