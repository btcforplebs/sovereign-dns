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
  ref=$(sed -E 's/^[[:space:]]*image:[[:space:]]*//; s/[[:space:]]+#.*$//; s/[[:space:]]+$//' <<<"$line")
  grep -Eq -- "$digest\$" <<<"$ref" || fail "docker-compose.yml image $ref"
done < <(grep -E '^[[:space:]]*image:' docker-compose.yml)

# Every tracked Dockerfile ships in the installer payload, whatever its suffix.
for df in $(git ls-files '*[Dd]ockerfile*'); do
  # Instructions are case-insensitive and may be indented; match them as Docker does.
  stages=$(grep -Eio '^[[:space:]]*from[[:space:]].*[[:space:]]as[[:space:]]+[a-z0-9_.-]+' "$df" | awk '{print tolower($NF)}' || true)
  # The image a FROM names, with --platform and other flags dropped.
  while IFS= read -r ref; do
    case "$ref" in
      scratch) continue ;;
      '$'*)
        # FROM ${X} / $X: X's ARG default is what gets pulled, whatever X is called.
        name=$(sed -E 's/^\$\{?([A-Za-z0-9_]+).*/\1/' <<<"$ref")
        defs=$(grep -Ei "^[[:space:]]*arg[[:space:]]+$name=" "$df" | cut -d= -f2- || true)
        [[ -n "$defs" ]] || { fail "$df FROM $ref has no ARG $name default"; continue; }
        # Docker uses the last declaration; any redeclaration is too easy to misread.
        [[ $(wc -l <<<"$defs") -eq 1 ]] || { fail "$df ARG $name is declared more than once"; continue; }
        def=$defs
        grep -Eq -- "$digest\$" <<<"$def" || fail "$df ARG $name=$def"
        continue ;;
    esac
    grep -qx -- "$(tr 'A-Z' 'a-z' <<<"$ref")" <<<"$stages" && continue
    grep -Eq -- "$digest\$" <<<"$ref" || fail "$df FROM $ref"
  done < <(grep -Ei '^[[:space:]]*from[[:space:]]' "$df" | awk '{for (i = 2; i <= NF; i++) if ($i !~ /^--/) { print $i; break }}')

  # COPY --from= and RUN --mount=...,from= pull an image too unless they name a stage
  # (or a stage index).
  while IFS= read -r ref; do
    [[ "$ref" =~ ^[0-9]+$ ]] && continue
    grep -qx -- "$(tr 'A-Z' 'a-z' <<<"$ref")" <<<"$stages" && continue
    grep -Eq -- "$digest\$" <<<"$ref" || fail "$df --from $ref"
  done < <(grep -Eio -- '(--from=|[,[:space:]]from=)[^[:space:],]+' "$df" | sed -E 's/^.*from=//I' || true)

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
