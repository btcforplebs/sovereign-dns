#!/usr/bin/env bash
# Fails if anything the stack builds or runs is referenced by a name that can
# change underneath us: an image without a digest, nvpn by anything but a full
# commit SHA, or a package manager allowed to re-resolve its lockfile. Two
# installs of the same installer must get the same bits.
#
#   scripts/check-pins.sh                  offline: is everything pinned?
#   scripts/check-pins.sh --verify-remote  also ask the registry that each pin's
#                                          tag still points at its digest
set -euo pipefail
cd "$(dirname "$0")/.."

status=0
fail() { echo "unpinned: $*"; status=1; }
digest='@sha256:[0-9a-f]{64}'

# Compose services that pull an image, in every tracked compose file: compose
# auto-loads docker-compose.override.yml, and any of them can be passed with -f.
compose_files=$(git ls-files '*compose*.yml' '*compose*.yaml')
[[ -n "$compose_files" ]] || fail "no tracked compose file found"
for cf in $compose_files; do
  while IFS= read -r line; do
    ref=$(sed -E 's/^[[:space:]]*image:[[:space:]]*//; s/[[:space:]]+#.*$//; s/[[:space:]]+$//' <<<"$line")
    grep -Eq -- "$digest\$" <<<"$ref" || fail "$cf image $ref"
  done < <(grep -E '^[[:space:]]*image:' "$cf" || true)
done

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
  done < <(grep -Eio -- '(--from=|[=,[:space:]]from=)[^[:space:],]+' "$df" | sed -E 's/^.*from=//I' || true)

  # Package managers: a digest pins one image, the lockfile pins the whole
  # dependency graph, so each command must refuse to re-resolve it. Join
  # continuation lines, then split shell command lists into single commands.
  while IFS= read -r cmd; do
    # (bash 3.2 on macOS has no ;;& so cargo gets its own case.)
    case "$cmd" in
      cargo\ build*|cargo\ install*|cargo\ run*|cargo\ test*)
        grep -Eq -- '(^|[[:space:]])--(locked|frozen)([[:space:]]|$)' <<<"$cmd" || fail "$df no --locked: $cmd" ;;
    esac
    case "$cmd" in
      cargo\ install*)
        grep -Eq -- '--version[= ]=[0-9]+\.[0-9]+\.[0-9]+' <<<"$cmd" || fail "$df no exact --version: $cmd" ;;
      pnpm\ install*|pnpm\ i\ *|pnpm\ i)
        grep -Eq -- '--frozen-lockfile' <<<"$cmd" || fail "$df no --frozen-lockfile: $cmd" ;;
      npm\ install*|npm\ i\ *|npm\ i)
        fail "$df use npm ci, not: $cmd" ;;
    esac
  done < <(awk '/\\[[:space:]]*$/ { sub(/\\[[:space:]]*$/, " "); buf = buf $0; next } { print buf $0; buf = "" }' "$df" \
             | sed -E 's/(&&|\|\||[;|])/\n/g' \
             | sed -E 's/^[[:space:]]*(RUN([[:space:]]+--[^[:space:]]+)*[[:space:]]+)?//I; s/^([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*//')
done

# nvpn source: a full SHA, and the same one everywhere it is defaulted.
# Every NVPN_COMMIT a compose file sets, in map (`NVPN_COMMIT: x`) or list
# (`- NVPN_COMMIT=x`) form. ${NVPN_COMMIT:-x} contributes x; a literal value
# counts as itself, so `NVPN_COMMIT: main` cannot slip past.
compose_shas() {
  local v
  while IFS= read -r v; do
    v=$(sed -E 's/[[:space:]]+#.*$//; s/[[:space:]]+$//; s/^["'"'"']//; s/["'"'"']$//' <<<"$v")
    if [[ "$v" =~ ^\$\{NVPN_COMMIT:-(.*)\}$ ]]; then echo "${BASH_REMATCH[1]}"
    else echo "$v"; fi
  done < <(grep -Eh '^[[:space:]]*(-[[:space:]]*)?NVPN_COMMIT[[:space:]]*[:=]' $compose_files \
             | sed -E 's/^[[:space:]]*(-[[:space:]]*)?NVPN_COMMIT[[:space:]]*[:=][[:space:]]*//' || true)
}
shas=$( { compose_shas
          grep -Eho '^ARG NVPN_COMMIT=.*' nvpn/Dockerfile | sed -E 's/^ARG NVPN_COMMIT=//' || true; } | sort -u)
[[ -n "$shas" ]] || fail "no NVPN_COMMIT default found"
for s in $shas; do
  grep -Eqx '[0-9a-f]{40}' <<<"$s" || fail "NVPN_COMMIT default '$s' is not a full commit SHA"
done
[[ $(wc -l <<<"$shas") -le 1 ]] || fail "NVPN_COMMIT defaults disagree: $(echo $shas)"

[[ "$status" == 0 ]] && echo "all images, sources and lockfiles pinned"

# The offline gate proves a digest is present, not that it belongs to the tag
# written beside it, and the tag is what a human reads when bumping. Ask Docker
# Hub. A tag that has moved is reported, not "fixed": re-pinning is a decision.
if [[ "${1:-}" == "--verify-remote" ]]; then
  mismatch() { echo "remote: $*"; status=1; }
  accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'
  refs=$(git ls-files '*[Dd]ockerfile*' '*compose*.yml' '*compose*.yaml' | xargs grep -Eoh '[a-z0-9./_-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}' | sort -u)
  for ref in $refs; do
    name=${ref%%:*}; rest=${ref#*:}; tag=${rest%%@*}; pin=${rest#*@}
    [[ "$name" == */* ]] || name="library/$name"
    [[ "${name%%/*}" == *.* ]] && { mismatch "$ref: registry other than Docker Hub, not checked"; continue; }
    token=$(curl -fsS "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$name:pull" \
              | sed -E 's/.*"token":"([^"]+)".*/\1/') || { mismatch "$ref: no registry token"; continue; }
    manifest_head() { curl -fsSI -H "Authorization: Bearer $token" -H "Accept: $accept" "https://registry-1.docker.io/v2/$name/manifests/$1"; }
    live=$(manifest_head "$tag" 2>/dev/null | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: //p') || true
    if [[ -z "$live" ]]; then mismatch "$ref: tag $tag not found on Docker Hub"
    elif [[ "$live" != "$pin" ]]; then
      # Either the tag moved on since pinning, or the tag text is wrong for this digest.
      if manifest_head "$pin" >/dev/null 2>&1; then where="the pinned digest exists in $name"
      else where="the pinned digest is not in $name at all"; fi
      mismatch "$name:$tag points at $live, not the pinned $pin; $where"
    else echo "ok: $name:$tag"; fi
  done
  [[ "$status" == 0 ]] && echo "every tag still points at its pinned digest"
fi
exit "$status"
