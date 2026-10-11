#!/usr/bin/env bash
# Fails if anything the stack builds or runs is referenced by a name that can
# change underneath us: an image without a digest, or nvpn by anything but a
# full commit SHA. Two installs of the same installer must get the same bits.
#
#   scripts/check-pins.sh
set -euo pipefail
cd "$(dirname "$0")/.."

status=0
fail() { echo "unpinned: $*"; status=1; }
digest='@sha256:[0-9a-f]{64}'

# Compose services that pull an image.
while IFS= read -r line; do
  ref=$(sed -E 's/^[[:space:]]*image:[[:space:]]*//' <<<"$line")
  grep -Eq "$digest\$" <<<"$ref" || fail "docker-compose.yml image $ref"
done < <(grep -E '^[[:space:]]*image:' docker-compose.yml)

for df in $(git ls-files '*Dockerfile'); do
  # Base images given as ARG defaults (FROM ${X}).
  while IFS= read -r line; do
    ref=${line#*=}
    grep -Eq "$digest\$" <<<"$ref" || fail "$df $line"
  done < <(grep -E '^ARG [A-Z_]*IMAGE=' "$df")

  # Literal FROM lines. Skip ${ARG} (checked above), scratch, and earlier stages.
  stages=$(grep -Eio '^FROM .* AS [a-z0-9_-]+' "$df" | awk '{print tolower($NF)}')
  while IFS= read -r ref; do
    case "$ref" in
      '${'*|scratch) continue ;;
    esac
    grep -qx "$(tr 'A-Z' 'a-z' <<<"$ref")" <<<"$stages" && continue
    grep -Eq "$digest\$" <<<"$ref" || fail "$df FROM $ref"
  done < <(grep -E '^FROM ' "$df" | awk '{print $2}')

  # cargo install must name one exact version.
  while IFS= read -r line; do
    grep -Eq -- '--version =[0-9]+\.[0-9]+\.[0-9]+' <<<"$line" || fail "$df $line"
  done < <(grep -E 'cargo install' "$df" || true)
done

# nvpn source: a full SHA, and the same one everywhere it is defaulted.
shas=$( { grep -Eho 'NVPN_COMMIT:-[^}]*' docker-compose.yml || true
          grep -Eho '^ARG NVPN_COMMIT=.*' nvpn/Dockerfile || true; } | sed -E 's/.*(:-|=)//' | sort -u)
[[ -n "$shas" ]] || fail "no NVPN_COMMIT default found"
for s in $shas; do
  grep -Eqx '[0-9a-f]{40}' <<<"$s" || fail "NVPN_COMMIT default '$s' is not a full commit SHA"
done
[[ $(wc -l <<<"$shas") -le 1 ]] || fail "NVPN_COMMIT defaults disagree: $(echo $shas)"

[[ "$status" == 0 ]] && echo "all images and sources pinned"
exit "$status"
