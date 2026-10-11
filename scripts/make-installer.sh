#!/usr/bin/env bash
# Generates scripts/install.sh: a self-extracting installer whose payload is
# the repo tree itself (everything tracked except the installer). The
# installer can no longer drift from the repo, because it is never edited —
# only regenerated. Run this after changing any file that ships.
#
#   scripts/make-installer.sh          regenerate scripts/install.sh
#   scripts/make-installer.sh --check  fail if install.sh's payload differs
#                                      from the tree (drift guard)
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="scripts/install.sh"
MARKER="__PAYLOAD_BELOW__"

payload_files() {
  # .githooks/ is for people working on the repo, not for installs.
  git ls-files | grep -v -x "$OUT" | grep -v '^\.githooks/'
}

make_payload() {
  # gzip -n: no embedded timestamp, so an unchanged tree packs identically.
  payload_files | tar -cf - -T - | gzip -n
}

b64_decode() {
  if printf 'QQo=' | base64 -d >/dev/null 2>&1; then base64 -d; else base64 -D; fi
}

if [[ "${1:-}" == "--check" ]]; then
  # The pins are part of what --check guards, so one command proves both.
  scripts/check-pins.sh >/dev/null || { scripts/check-pins.sh; exit 1; }
  [[ -f "$OUT" ]] || { echo "drift: $OUT does not exist"; exit 1; }
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  line=$(awk -v m="$MARKER" '$0 == m { print NR + 1; exit }' "$OUT")
  [[ -n "$line" ]] || { echo "drift: no payload marker in $OUT"; exit 1; }
  mkdir "$tmp/tree"
  tail -n +"$line" "$OUT" | b64_decode > "$tmp/payload.tgz"
  tar -xzf "$tmp/payload.tgz" -C "$tmp/tree"
  status=0
  while IFS= read -r f; do
    if ! diff -q "$tmp/tree/$f" "$f" >/dev/null 2>&1; then
      echo "drift: $f"
      status=1
    fi
  done < <(payload_files)
  stale=$(cd "$tmp/tree" && find . -type f | sed 's|^\./||' | sort | comm -23 - <(cd "$OLDPWD" && payload_files | sort))
  if [[ -n "$stale" ]]; then
    printf 'drift (packed but no longer in tree): %s\n' $stale
    status=1
  fi
  [[ "$status" == 0 ]] && echo "install.sh matches the tree"
  exit "$status"
fi

# Never ship an installer whose images or sources can change underneath it.
scripts/check-pins.sh >/dev/null || { scripts/check-pins.sh; exit 1; }

{
  cat << 'HEADER'
#!/usr/bin/env bash
# Sovereign-DNS one-shot installer.
#
# GENERATED FILE — do not edit by hand. The payload below IS the repo tree;
# change repo files and run scripts/make-installer.sh to regenerate.
#
# What it does: checks Docker, unpacks the whole stack into ./sovereign-dns,
# asks for one password, builds, starts, and hands you a dashboard URL.
set -euo pipefail

die() { printf '\n%s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "Docker isn't installed. Get it first: https://docs.docker.com/get-docker/"
docker info >/dev/null 2>&1 || die "Docker is installed but not running.
Open Docker Desktop (or start the docker service), wait until it says it's running, then run this script again."
docker compose version >/dev/null 2>&1 || die "This Docker is missing the 'compose' plugin.
Update Docker Desktop (or install docker-compose-plugin), then run this script again."

# The stack publishes these localhost ports. If another program already holds
# one, say so now in plain language instead of failing mid-build. A previous
# sovereign-dns install holding them is fine — compose reuses its containers.
if command -v nc >/dev/null 2>&1 && ! docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^sovereign-dns-'; then
  for port in 8080 8081 8082; do
    if nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
      die "Port $port is already in use by another program on this computer.
Close whatever is using it (or run this script once, then edit the *_PORT values in sovereign-dns/.env) and try again."
    fi
  done
fi

if [[ -d sovereign-dns ]]; then
  echo "Existing ./sovereign-dns found — refreshing its stack files. Your .env, password, and data are kept."
fi
mkdir -p sovereign-dns

if printf 'QQo=' | base64 -d >/dev/null 2>&1; then dec() { base64 -d; }; else dec() { base64 -D; }; fi
payload_line=$(awk '/^__PAYLOAD_BELOW__$/ { print NR + 1; exit }' "$0")
[[ -n "$payload_line" ]] || die "This installer file is truncated — re-download it and run again."
tail -n +"$payload_line" "$0" | dec | tar -xzf - -C sovereign-dns
cd sovereign-dns

if [[ ! -f .env ]]; then
  echo
  echo "Pick a password for your Pi-hole admin dashboard (typing is hidden):"
  read -rs PIHOLE_PASSWORD
  echo
  (umask 077; cat > .env) << ENV
PIHOLE_PASSWORD=${PIHOLE_PASSWORD}
TZ=UTC
PIHOLE_WEB_PORT=80
HUB_PORT=8080
DOCS_PORT=8081
WEB_UI_PORT=8082
NVPN_REPO=https://github.com/btcforplebs/nostr-vpn.git
NVPN_TUN_IFACE=utun100
RUST_LOG=info
ENV
fi

# Older installs wrote NVPN_REF=<branch> here. Nothing reads it any more (the
# commit is pinned in docker-compose.yml), so drop it rather than leave a
# setting that looks like it does something.
if grep -q '^NVPN_REF=' .env; then
  (umask 077; grep -v '^NVPN_REF=' .env > .env.tmp || true) && cat .env.tmp > .env && rm -f .env.tmp
  echo "Removed the old NVPN_REF setting from .env; the NostrVPN commit is now pinned by the stack."
fi

# .env holds the Pi-hole password. Installs before this line wrote it with the
# default umask (usually 644), so tighten it every run, not just on creation.
chmod 600 .env

HUB_PORT=$(grep '^HUB_PORT=' .env | cut -d= -f2 || true)
HUB_PORT=${HUB_PORT:-8080}

if [[ "${SOVEREIGN_DNS_EXTRACT_ONLY:-0}" == "1" ]]; then
  echo "Extract-only mode: files written to ./sovereign-dns, nothing started."
  exit 0
fi

echo
echo "Building and starting (the first run compiles from source and can take 10-15 minutes; later runs are fast)..."
docker compose up -d --build

echo
echo "Waiting for your dashboard to come up..."
hub_up=0
if command -v curl >/dev/null 2>&1; then
  for _ in $(seq 1 45); do
    if curl -fsS "http://127.0.0.1:${HUB_PORT}/" >/dev/null 2>&1; then
      hub_up=1
      break
    fi
    sleep 2
  done
fi

echo
echo "=========================================="
if [[ "$hub_up" == "1" ]]; then
  echo "Done! Open your dashboard:"
else
  echo "Started. The dashboard may need another minute — open:"
fi
echo
echo "    http://localhost:${HUB_PORT}"
echo
echo "Everything lives there: what's running, this device's ID, the invite"
echo "command for adding your phone or laptop, the VPN control panel, the"
echo "Pi-hole ad-block dashboard, and the docs."
echo
echo "If something shows as down, run:  bash sovereign-dns/scripts/healthcheck.sh"
echo "=========================================="
exit 0
HEADER
  echo "$MARKER"
  make_payload | base64
} > "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
chmod +x "$OUT"
echo "wrote $OUT ($(wc -c < "$OUT" | tr -d ' ') bytes, $(payload_files | wc -l | tr -d ' ') files packed)"
