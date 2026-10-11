#!/usr/bin/env bash
# Fails if anything the stack builds or runs is referenced by a name that can
# change underneath us: an image without a digest, nvpn by anything but a full
# commit SHA, an ADD or git fetch of something not pinned to a checksum or
# commit, a package manager allowed to re-resolve its lockfile, or any RUN
# command the gate has no rule for. Two installs of the same installer must get
# the same bits.
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

  # RUN commands are default-deny: a digest pins one image, a lockfile pins the
  # whole dependency graph, and any tool not listed here could fetch something
  # neither covers. Join continuation lines, keep RUN instructions, split shell
  # command lists into single commands, and squeeze whitespace so reformatting
  # cannot hide a command from the patterns below.
  if grep -Eiq '^[[:space:]]*run[[:space:]].*<<' "$df"; then
    fail "$df uses a heredoc RUN, which this gate cannot read"
  fi
  joined=$(awk '/\\[[:space:]]*$/ { sub(/\\[[:space:]]*$/, " "); buf = buf $0; next } { print buf $0; buf = "" }' "$df")

  # ADD fetches a URL or git repo itself, with no RUN involved. BuildKit has
  # pinned forms for both: --checksum=sha256:... and repo.git#<commit>.
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    read -ra words <<<"$line"
    for src in "${words[@]}"; do
      case "$src" in
        git@*|*.git|*.git#*|*.git/*)
          grep -Eq -- '#[0-9a-f]{40}$' <<<"$src" || fail "$df ADD git source not pinned to a commit: $src" ;;
        http://*|https://*)
          grep -Eq -- "--checksum=sha256:[0-9a-f]{64}( |\$)" <<<"$line" || fail "$df ADD without --checksum: $src" ;;
      esac
    done
  done <<<"$(grep -Ei '^[[:space:]]*add[[:space:]]' <<<"$joined" | tr -s '[:blank:]' ' ' || true)"

  # Go's own escape hatches: these make an allowed `go build` resolve or skip
  # checksum verification, wherever they are set (ENV, ARG, or a RUN prefix).
  if grep -Eq '(^|[^A-Za-z0-9_])(GOFLAGS|GOPROXY|GOSUMDB|GONOSUMDB|GONOSUMCHECK|GOINSECURE|GOPRIVATE|GOWORK)[[:space:]]*[= ]' <<<"$joined"; then
    fail "$df sets a Go module setting that can bypass go.sum: $(grep -Eo '(GOFLAGS|GOPROXY|GOSUMDB|GONOSUMDB|GONOSUMCHECK|GOINSECURE|GOPRIVATE|GOWORK)[^[:space:]]*' <<<"$joined" | head -1)"
  fi

  cmds=$(grep -Ei '^[[:space:]]*run[[:space:]]' <<<"$joined" \
           | sed -E 's/(&&|\|\||[;|])/\n/g' \
           | sed -E 's/^[[:space:]]*(RUN([[:space:]]+--[^[:space:]]+)*[[:space:]]+)?//I' \
           | tr -s '[:blank:]' ' ' \
           | sed -E 's/^ //; s/ $//; s/^((if|then|else|!) )+//; s/^([A-Za-z_][A-Za-z0-9_]*=[^ ]* )*//; s/^cargo \+[^ ]+ /cargo /' \
           | grep -v '^$' || true)
  # A gate that read nothing must not report "pinned".
  if grep -Eiq '^[[:space:]]*run[[:space:]]' "$df" && [[ -z "$cmds" ]]; then
    fail "$df has RUN instructions but no command could be read from them"
  fi
  while IFS= read -r cmd; do
    [[ -n "$cmd" ]] || continue
    case "$cmd" in
      cargo\ build*|cargo\ install*|cargo\ run*|cargo\ test*|cargo\ fetch*)
        grep -Eq -- '(^| )--(locked|frozen)( |$)' <<<"$cmd" || fail "$df no --locked: $cmd"
        case "$cmd" in cargo\ install*)
          grep -Eq -- '--version[= ]=[0-9]+\.[0-9]+\.[0-9]+' <<<"$cmd" || fail "$df no exact --version: $cmd" ;;
        esac ;;
      pnpm\ install*|pnpm\ i\ *|pnpm\ i)
        grep -Eq -- '--frozen-lockfile' <<<"$cmd" || fail "$df no --frozen-lockfile: $cmd" ;;
      pnpm\ run\ *|npm\ ci|npm\ ci\ *|npm\ run\ *) ;;
      corepack\ enable|corepack\ enable\ *) ;;
      corepack\ prepare\ *)
        grep -Eq -- ' [a-z]+@[0-9]+\.[0-9]+\.[0-9]+( |$)' <<<"$cmd" || fail "$df no exact version: $cmd" ;;
      # go build verifies go.sum read-only by default; go install/get resolve.
      go\ build*) ;;
      # Each line: tools that only build, copy or test what is already pinned.
      # apt-get is allowed while the unpinned apt stages are an open item.
      # git fetch must name its commit: a 40-hex literal, or ${VAR} whose ARG
      # default in this Dockerfile is one (and is declared exactly once).
      git\ fetch\ *)
        ok=
        read -ra words <<<"$cmd"
        for a in "${words[@]}"; do
          a=${a//\"/}; a=${a//\'/}
          if grep -Eqx '[0-9a-f]{40}' <<<"$a"; then ok=1
          elif [[ "$a" =~ ^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$ ]]; then
            defs=$(grep -Ei "^[[:space:]]*arg[[:space:]]+${BASH_REMATCH[1]}=" "$df" | cut -d= -f2- | sed -E 's/[[:space:]]+$//' || true)
            [[ -n "$defs" && $(wc -l <<<"$defs") -eq 1 ]] && grep -Eqx '[0-9a-f]{40}' <<<"$defs" && ok=1
          fi
        done
        [[ -n "$ok" ]] || fail "$df git fetch without a pinned commit: $cmd" ;;
      mdbook\ build*|git\ init*|git\ checkout*|git\ rev-parse*|\
      cd\ *|cp\ *|mv\ *|rm\ *|mkdir\ *|ln\ *|chmod\ *|chown\ *|test\ *|\
      echo\ *|printf\ *|grep\ *|set\ *|exit|exit\ *|fi|true|false|\
      apt-get\ update*|apt-get\ install\ *) ;;
      *) fail "$df command not allowed by the gate (add it with a rule if it is safe): $cmd" ;;
    esac
  done <<<"$cmds"
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
          grep -Eih '^[[:space:]]*arg[[:space:]]+NVPN_COMMIT=' nvpn/Dockerfile | sed -E 's/^[[:space:]]*[Aa][Rr][Gg][[:space:]]+NVPN_COMMIT=//; s/[[:space:]]+$//' || true; } | sort -u)
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
  refs=$(git ls-files '*[Dd]ockerfile*' '*compose*.yml' '*compose*.yaml' | xargs grep -Eoh '[a-z0-9./_:-]+@sha256:[0-9a-f]{64}' | sort -u)
  for ref in $refs; do
    # name@digest with no tag is valid Docker, but there is no tag to check it against.
    [[ "${ref%%@*}" =~ :[A-Za-z0-9._-]+$ && "${ref%%@*}" != *:*/* ]] || { mismatch "$ref: no tag beside the digest, not checked"; continue; }
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
