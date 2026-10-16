#!/usr/bin/env bash
# Generates scripts/install.sh: a self-extracting installer whose payload is
# the repo tree itself (everything tracked except the installer). The
# installer can no longer drift from the repo, because it is never edited —
# only regenerated. Run this after changing any file that ships.
#
#   scripts/make-installer.sh          regenerate scripts/install.sh
#   scripts/make-installer.sh --check  fail if install.sh is not, byte for
#                                      byte, what this tree builds (drift
#                                      guard), or if install.sh.sha256 is
#                                      not its hash
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="scripts/install.sh"
# The published fingerprint, served next to install.sh. It cannot live inside
# anything that ships in the payload (the docs included): writing the hash
# would change the payload, and so the hash. Same format as `shasum -a 256`,
# with a bare file name, so `shasum -a 256 -c install.sh.sha256` works in
# whatever folder the two files were downloaded to.
SUM="$OUT.sha256"
MARKER="__PAYLOAD_BELOW__"

payload_files() {
  # .githooks/ is for people working on the repo, not for installs.
  git ls-files | grep -v -x -e "$OUT" -e "$SUM" | grep -v '^\.githooks/'
}

sha256_line() {
  local h
  if command -v sha256sum >/dev/null 2>&1; then
    h=$(sha256sum < "$1")
  else
    h=$(shasum -a 256 < "$1")
  fi
  printf '%s  %s\n' "${h%% *}" "$(basename "$1")"
}

# The payload is a plain tar written by git, never by the host's tar or gzip,
# so the same tree gives the same install.sh bytes on any machine. Measured:
# bsdtar and GNU tar write different headers, Apple and GNU gzip compress the
# same tar differently, and both tars record each file's mtime and owner, so a
# bare `touch` used to change the hash. git archive records only what git
# tracks: the path, the content, and the mode git stores (644 or 755; tar.umask
# is pinned so a user's git config can't change it), with a fixed time and
# owner root. Content comes from the working tree, as before, through a
# throwaway index; modes come from the real index, so a checkout with
# core.fileMode=false still packs the scripts as executable. The tar is left
# uncompressed because compressed bytes depend on the zlib build.
PAYLOAD_MTIME="2000-01-01 00:00:00 +0000"

make_payload() {
  [[ "$(git archive -h 2>&1)" == *--mtime* ]] || {
    echo "make-installer.sh needs git 2.42 or later (git archive --mtime)." >&2
    exit 1
  }
  local idx tree
  idx=$(mktemp)
  rm -f "$idx"
  payload_files | while IFS= read -r f; do
    printf '%s %s\t%s\n' "$(git ls-files -s -- "$f" | cut -d' ' -f1)" "$(git hash-object -w -- "$f")" "$f"
  done | GIT_INDEX_FILE="$idx" git update-index --add --index-info
  tree=$(GIT_INDEX_FILE="$idx" git write-tree)
  rm -f "$idx"
  git -c tar.umask=022 -c core.autocrlf=false -c core.eol=lf \
    archive --format=tar --mtime="$PAYLOAD_MTIME" "$tree"
}

# GNU base64 wraps at 76 columns and macOS base64 doesn't wrap at all, so
# strip whatever the host did and wrap at 76 ourselves.
b64_encode() {
  base64 | tr -d '\n' | fold -w 76
  echo
}

b64_decode() {
  if printf 'QQo=' | base64 -d >/dev/null 2>&1; then base64 -d; else base64 -D; fi
}

render_installer() {
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

# Linux only: offer to install Docker Engine when it is missing, the way
# Docker documents it for the Debian family (Debian, Ubuntu, Linux Mint,
# Raspberry Pi OS and their relatives): Docker's own apt repository, signed
# by Docker's key. Never without asking: Enter or y installs, anything else
# (or no answer at all, as in a script) stops with the download link. macOS
# and Windows get Docker Desktop's download link instead.
docker_link="https://docs.docker.com/get-docker/"
install_docker_linux() {
  [[ -r /etc/os-release ]] || return 1
  local ID="" ID_LIKE="" VERSION_CODENAME="" UBUNTU_CODENAME="" DEBIAN_CODENAME=""
  # shellcheck disable=SC1091
  . /etc/os-release
  local repo codename
  case " $ID $ID_LIKE " in
    *" raspbian "*) repo=raspbian codename=$VERSION_CODENAME ;;   # Raspberry Pi OS, 32-bit
    *" ubuntu "*)   repo=ubuntu codename=${UBUNTU_CODENAME:-$VERSION_CODENAME} ;;  # Ubuntu, Mint, Pop!_OS
    *" debian "*)   repo=debian codename=${DEBIAN_CODENAME:-$VERSION_CODENAME} ;;  # Debian, Pi OS 64-bit, LMDE
    *) echo "This installer can set up Docker on Debian, Ubuntu, Linux Mint and Raspberry Pi OS; this looks like '${ID:-unknown}'."; return 1 ;;
  esac
  [[ -n "$codename" ]] || { echo "Couldn't tell which release of ${ID} this is."; return 1; }
  local sudo=""
  if [[ $(id -u) -ne 0 ]]; then
    command -v sudo >/dev/null 2>&1 || { echo "Installing Docker needs sudo, which isn't here."; return 1; }
    sudo=sudo
    echo "Installing needs your computer password (sudo)."
  fi
  echo "Installing Docker from Docker's own repository (download.docker.com/linux/$repo, $codename)..."
  $sudo apt-get update -q &&
  $sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y -q ca-certificates curl &&
  $sudo install -m 0755 -d /etc/apt/keyrings &&
  $sudo curl -fsSL "https://download.docker.com/linux/$repo/gpg" -o /etc/apt/keyrings/docker.asc &&
  $sudo chmod a+r /etc/apt/keyrings/docker.asc &&
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$repo $codename stable" |
    $sudo tee /etc/apt/sources.list.d/docker.list >/dev/null &&
  $sudo apt-get update -q &&
  $sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin ||
    { echo "Installing Docker failed (see above)."; return 1; }
  if [[ -d /run/systemd/system ]]; then
    $sudo systemctl enable --now docker >/dev/null 2>&1 || true
  else
    $sudo service docker start >/dev/null 2>&1 || true
  fi
  # So this user can run docker without sudo, from the next login on. This
  # run carries on in the group straight away (below).
  if [[ -n "$sudo" ]]; then
    $sudo usermod -aG docker "$(id -un)" && echo "Added $(id -un) to the docker group."
  fi
  echo "Docker is installed."
}

if ! command -v docker >/dev/null 2>&1; then
  if [[ "$(uname -s)" != Linux ]]; then
    die "Docker isn't installed. Get Docker Desktop first: $docker_link
Then run this script again."
  fi
  printf '\nDocker isn'"'"'t installed, and this box needs it. Install it now? [Y/n] '
  answer=""
  read -r answer || answer=no
  case "$answer" in
    ""|[Yy]|[Yy][Ee][Ss]) install_docker_linux || die "Docker isn't installed. Install it yourself: $docker_link
Then run this script again." ;;
    *) die "Docker isn't installed. Install it, then run this script again: $docker_link" ;;
  esac
fi
# Just installed, this user is in the docker group but this shell is not yet.
# Carry on inside the group rather than asking for a log out and back in.
# (Not `docker info | grep`: under pipefail that pipeline has docker's status.)
docker_err=$(docker info 2>&1 >/dev/null || true)
if [[ "$(uname -s)" == Linux && $(id -u) -ne 0 ]] && grep -qi 'permission denied' <<<"$docker_err" &&
   id -nG "$(id -un)" 2>/dev/null | tr ' ' '\n' | grep -qx docker &&
   ! id -nG | tr ' ' '\n' | grep -qx docker && [[ -z "${SOVEREIGN_DNS_IN_DOCKER_GROUP:-}" ]]; then
  self="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
  exec sg docker -c "SOVEREIGN_DNS_IN_DOCKER_GROUP=1 $(printf '%q ' bash "$self" "$@")"
fi
docker info >/dev/null 2>&1 || die "Docker is installed but not running.
Open Docker Desktop (or start the docker service), wait until it says it's running, then run this script again."
docker compose version >/dev/null 2>&1 || die "This Docker is missing the 'compose' plugin.
Update Docker Desktop (or install docker-compose-plugin), then run this script again."

# The stack publishes these localhost ports. If another program already holds
# one, say so now in plain language instead of failing mid-build. A previous
# sovereign-dns install holding them is fine — compose reuses its containers.
if command -v nc >/dev/null 2>&1 && ! docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^sovereign-dns-'; then
  for port in 8080 8082; do  # 8081 too only with the opt-in docs profile
    if nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
      die "Port $port is already in use by another program on this computer.
Close whatever is using it (or run this script once, then edit the *_PORT values in sovereign-dns/.env) and try again."
    fi
  done
fi

if printf 'QQo=' | base64 -d >/dev/null 2>&1; then dec() { base64 -d; }; else dec() { base64 -D; }; fi
payload_line=$(awk '/^__PAYLOAD_BELOW__$/ { print NR + 1; exit }' "$0")
[[ -n "$payload_line" ]] || die "This installer file is truncated — re-download it and run again."

if [[ -d sovereign-dns ]]; then
  echo "Existing ./sovereign-dns found — refreshing its stack files. Your .env, password, and data are kept."
  # An earlier run with sudo leaves files this user cannot overwrite, and tar
  # would stop halfway through the update. Say so before touching anything.
  blocked=$( { echo .; tail -n +"$payload_line" "$0" | dec | tar -tf -; } | while IFS= read -r f; do
    f="sovereign-dns/${f#./}"; f="${f%/}"
    if [[ -e "$f" && ! -w "$f" ]]; then echo "  $f"; fi
  done | awk 'NR <= 5')
  [[ -z "$blocked" ]] || die "Some files in ./sovereign-dns belong to another user (probably an earlier install
run with sudo), so they can't be updated:
$blocked
Make them yours again, then paste the install line again:
  sudo chown -R \"$(id -un)\" sovereign-dns"
fi
mkdir -p sovereign-dns
# tar -x adds and overwrites files but never deletes one. A prebuilt override
# left by an older installer would pin an older nvpn than this payload ships,
# so it goes before unpacking; the payload brings it back if it has one.
rm -f sovereign-dns/docker-compose.prebuilt.yml

tail -n +"$payload_line" "$0" | dec | tar -xf - -C sovereign-dns
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
NVPN_TUN_IFACE=utun100
SOVEREIGN_DNS_LOG=info
ENV
fi

# Older installs wrote RUST_LOG here. The stack now reads SOVEREIGN_DNS_LOG,
# because compose lets a RUST_LOG in the caller's shell override .env. Carry
# the value over so a deliberate setting survives, unless one is already set.
if grep -q '^RUST_LOG=' .env; then
  if grep -q '^SOVEREIGN_DNS_LOG=' .env; then
    (umask 077; grep -v '^RUST_LOG=' .env > .env.tmp || true) && cat .env.tmp > .env && rm -f .env.tmp
  else
    (umask 077; sed 's/^RUST_LOG=/SOVEREIGN_DNS_LOG=/' .env > .env.tmp) && cat .env.tmp > .env && rm -f .env.tmp
  fi
  echo "Replaced RUST_LOG in .env with SOVEREIGN_DNS_LOG, so your shell's RUST_LOG cannot change the stack's logging."
fi

# Older installs wrote NVPN_REF, NVPN_REPO or NVPN_COMMIT here. Nothing reads
# them any more: the source and commit are literals in docker-compose.yml, so
# .env cannot move what gets built. Drop them rather than leave settings that
# look like they do something.
if grep -Eq '^(NVPN_REF|NVPN_REPO|NVPN_COMMIT)=' .env; then
  (umask 077; grep -Ev '^(NVPN_REF|NVPN_REPO|NVPN_COMMIT)=' .env > .env.tmp || true) && cat .env.tmp > .env && rm -f .env.tmp
  echo "Removed old NostrVPN source settings from .env; the source and commit are pinned by the stack."
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

# The dashboard opens at localhost, on this computer only. A box with no
# screen (a Raspberry Pi set up over SSH) has nobody at that computer, so offer
# to open it to the home network instead, behind a password
# (scripts/hub-lan.sh, hub/auth.go). Asked once: the answer lands in .env as
# HUB_BIND, and runs after that keep it. SOVEREIGN_DNS_HUB_LAN=1 or 0 answers
# without asking, on any computer.
if ! grep -q '^HUB_BIND=' .env; then
  hub_lan=${SOVEREIGN_DNS_HUB_LAN:-}
  no_desktop=0
  case "$(uname -s)" in
    Linux) [[ -z "${DISPLAY:-}" && -z "${WAYLAND_DISPLAY:-}" ]] && no_desktop=1 ;;
    Darwin) [[ -n "${SSH_CONNECTION:-}" ]] && no_desktop=1 ;;
  esac
  if [[ -z "$hub_lan" && "$no_desktop" == "1" && -t 0 ]]; then
    echo
    echo "This computer has no screen to open the dashboard on. Open it from another"
    echo "computer on your home network instead? It will ask for a password you pick now."
    printf 'Open the dashboard to your home network? [Y/n] '
    read -r answer || answer=n   # Ctrl-D: no
    case "$answer" in [Nn]*) hub_lan=0 ;; *) hub_lan=1 ;; esac
  fi
  case "$hub_lan" in
    1) bash scripts/hub-lan.sh on --no-restart ;;
    0) bash scripts/hub-lan.sh off --no-restart >/dev/null ;;
  esac
fi

# Fast path: download ready-made images instead of building every service
# here (nvpn alone takes about 35 minutes on a Raspberry Pi 4, the docs site
# another 10 or more). docker-compose.prebuilt.yml pins each one it has by
# digest (an image left out builds from source), so
# Docker checks every byte it downloads against the pin. No such file, an
# image with no build for this device, a compose too old for the override, or
# a failed download all fall back to the source build, and so does
# SOVEREIGN_DNS_BUILD_FROM_SOURCE=1.
# COMPOSE_FILE in .env makes every later plain `docker compose` command in this
# folder, including the ones the docs and the dashboard tell people to run,
# load the same files. Without it, `docker compose up -d` would quietly rebuild
# everything from source. Removed first, so a source install never keeps an old one.
(umask 077; grep -v '^COMPOSE_FILE=' .env > .env.tmp || true) && cat .env.tmp > .env && rm -f .env.tmp
prebuilt=0
if [[ "${SOVEREIGN_DNS_BUILD_FROM_SOURCE:-0}" == "1" ]]; then
  echo
  echo "SOVEREIGN_DNS_BUILD_FROM_SOURCE=1: every service will be built from source."
elif [[ -f docker-compose.prebuilt.yml ]]; then
  echo
  echo "Downloading the ready-made images (Docker checks each against its pinned fingerprint)..."
  pulled=0
  if docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml config -q >/dev/null 2>&1; then
    pulled=1
    # All or nothing: half the stack prebuilt and half from source is a mix
    # nobody tested.
    for image in $(sed -nE 's/^[[:space:]]*image:[[:space:]]*//p' docker-compose.prebuilt.yml | sort -u); do
      docker pull "$image" || { pulled=0; break; }
    done
  fi
  if [[ "$pulled" == "1" ]]; then
    prebuilt=1
    echo "COMPOSE_FILE=docker-compose.yml:docker-compose.prebuilt.yml" >> .env
  else
    echo "There are no ready-made images for this device, so everything will be built from source instead."
  fi
fi

echo
from_source_msg="Building and starting (the first run compiles from source and can take 10-15 minutes, 20-40 on a Raspberry Pi; later runs are fast)..."
if [[ "$prebuilt" == "1" ]]; then
  echo "Starting (a few minutes on a Raspberry Pi; anything without a ready-made image builds first; later runs are fast)..."
  # If the ready-made images downloaded but the stack then fails to start,
  # leaving COMPOSE_FILE in .env would keep every recovery (`docker compose
  # up -d`, or running this installer again) on the same broken image. Drop
  # it and build from source instead, which is what an install did before.
  if ! docker compose up -d --build; then
    (umask 077; grep -v '^COMPOSE_FILE=' .env > .env.tmp || true) && cat .env.tmp > .env && rm -f .env.tmp
    echo
    echo "The ready-made images did not start, so everything will be built from source instead."
    echo "(To skip the ready-made images next time: SOVEREIGN_DNS_BUILD_FROM_SOURCE=1 bash install.sh)"
    echo "$from_source_msg"
    docker compose up -d --build
  fi
else
  echo "$from_source_msg"
  docker compose up -d --build
fi

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

# Device setup right here, so a box with no screen is ready without opening
# anything (hub/termsetup.go). The same checks and steps as the dashboard's
# setup, which stays the other way to do it. Only with a person at the
# terminal: a scripted install (no terminal, or SOVEREIGN_DNS_SKIP_SETUP=1)
# skips it, and on a box already set up (an update) it says so and asks
# nothing. It never fails the install.
if [[ "$hub_up" == "1" && -t 0 && -t 1 && "${SOVEREIGN_DNS_SKIP_SETUP:-0}" != "1" ]]; then
  setup_url=$(bash scripts/hub-lan.sh setup-url 2>/dev/null || true)
  docker compose exec -e SETUP_URL="$setup_url" hub /hub setup --if-new || true
fi

# Last, after any joining above (joining gives the box a new mesh address):
# ask the box for a lookup the way a device does, and say plainly whether it
# answers. A box once installed with no errors and answered nothing.
echo
echo "Checking that this box answers DNS..."
dns_ok=0
bash scripts/dns-check.sh && dns_ok=1

echo
echo "=========================================="
if [[ "$hub_up" == "1" && "$dns_ok" == "1" ]]; then
  echo "Done! Open your dashboard:"
elif [[ "$dns_ok" != "1" ]]; then
  echo "Installed, but this box is NOT answering DNS yet (see above). Your dashboard:"
else
  echo "Started. The dashboard may need another minute — open:"
fi
echo
echo "    http://localhost:${HUB_PORT}"
echo
if grep -q '^HUB_BIND=0.0.0.0$' .env; then
  bash scripts/hub-lan.sh url
  echo
fi
echo "Everything lives there: what's running, this device's ID, the invite"
echo "command for adding your phone or laptop, the VPN control panel, the"
echo "Pi-hole ad-block dashboard, and a link to the guide."
echo
echo "If something shows as down, run:  bash sovereign-dns/scripts/healthcheck.sh"
echo "=========================================="
exit 0
HEADER
  echo "$MARKER"
  make_payload | b64_encode
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
  tail -n +"$line" "$OUT" | b64_decode > "$tmp/payload.tar"
  tar -xf "$tmp/payload.tar" -C "$tmp/tree"
  status=0
  # Keep this per-file loop even though the byte compare below exists: a file
  # dropped by a .gitattributes export-ignore is missing from both the shipped
  # and the rebuilt payload, so only this loop, which reads the tree, sees it.
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
  # Same files is not enough for a published hash: rebuild and compare bytes.
  # Nothing about the checkout (mtimes, owner, umask, tar or gzip build) feeds
  # the payload, so any difference here is a real one.
  render_installer > "$tmp/rebuilt.sh"
  if ! cmp -s "$tmp/rebuilt.sh" "$OUT"; then
    echo "drift: $OUT is not what this tree builds (run scripts/make-installer.sh)"
    status=1
  fi
  if [[ ! -f "$SUM" ]]; then
    echo "drift: $SUM does not exist"
    status=1
  elif [[ "$(cat "$SUM")" != "$(sha256_line "$OUT")" ]]; then
    echo "drift: $SUM is not the hash of $OUT"
    status=1
  fi
  [[ "$status" == 0 ]] && echo "install.sh matches the tree and $SUM"
  exit "$status"
fi

# Never ship an installer whose images or sources can change underneath it.
scripts/check-pins.sh >/dev/null || { scripts/check-pins.sh; exit 1; }

render_installer > "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
chmod +x "$OUT"
sha256_line "$OUT" > "$SUM"
# Count what the archive holds, not what payload_files lists: they differ if
# git archive leaves a file out (export-ignore), and --check reports that.
packed=$(tail -n +"$(awk -v m="$MARKER" '$0 == m { print NR + 1; exit }' "$OUT")" "$OUT" | b64_decode | tar -tf - | grep -vc '/$')
echo "wrote $OUT ($(wc -c < "$OUT" | tr -d ' ') bytes, $packed files packed)"
echo "wrote $SUM: $(cut -d' ' -f1 "$SUM")"
