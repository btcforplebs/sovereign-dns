#!/usr/bin/env bash
# Fails if anything the stack builds is referenced by a name that can change
# underneath us: an image without a digest, nvpn by anything but a full commit
# SHA, an ADD or git fetch of something not pinned to a checksum or commit, a
# package manager allowed to re-resolve its lockfile, any RUN command the gate
# has no rule for, or a compose build section that bypasses all of that. Two
# installs of the same installer must build the same bits.
#
# Scope: build inputs only. What a container runs (CMD, ENTRYPOINT, compose
# command:/entrypoint:, entrypoint.sh) is tracked code in the payload and is
# reviewed, not gated. Allowed tools that read in-repo files (pnpm run ->
# package.json, cargo -> build.rs) are pinned by the commit they come from;
# mdbook's book.toml is the one such file this repo tracks, so it is read below.
#
#   scripts/check-pins.sh                  offline: is everything pinned?
#   scripts/check-pins.sh --verify-remote  also ask the registry that each pin's
#                                          tag still points at its digest
set -euo pipefail
cd "$(dirname "$0")/.."

status=0
fail() { echo "unpinned: $*"; status=1; }
digest='@sha256:[0-9a-f]{64}'
dockerfiles=$(git ls-files '*[Dd]ockerfile*')
[[ -n "$dockerfiles" ]] || fail "no tracked Dockerfile found"

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

# A compose build: section decides what is actually built: it can override the
# Dockerfile's ARG defaults, replace a whole stage with a named context, import
# layers from a remote build cache, or carry an inline Dockerfile with no file
# at all. Its keys are default-deny -- compose keeps growing new build inputs,
# and an input this gate cannot read must fail, not be ignored.
compose_build_keys() {
  awk '
    # Emits one record per compose build: input.
    #   bkey <key>            a first-level key inside a build: block
    #   ctx  <value>          a build context (long or short form)
    #   arg  <name> <value>   a build arg name and its value
    #   dpath <path>          the Dockerfile this build: block builds
    #   raw  <value>          a value in a form this parser does not read
    function ind(s) { match(s, /[^ \t]/); return RSTART - 1 }
    function val(s) { sub(/^[^:]*:[ \t]*/, "", s); sub(/[ \t]+#.*$/, "", s); sub(/[ \t]+$/, "", s); gsub(/^["\047]|["\047]$/, "", s); return s }
    # A context is a plain path or a remote URL. Anything else (an anchor, an
    # alias, a flow mapping) can hide a whole build: block from this parser.
    function ctxv(v) { if (v !~ /^[A-Za-z0-9_.\/:@#+=-]+$/) print "raw " v; else { print "ctx " v; c = v } }
    function endb() { if (inb) print "dpath " (c == "" ? "." : c) "/" (d == "" ? "Dockerfile" : d); inb = 0; ina = 0 }
    /^[ \t]*#/ { next }
    /^[ \t]*$/ { next }
    {
      i = ind($0)
      if (inb && i <= bi) endb()
      if (!inb && $0 ~ /^[ \t]*build:[ \t]*$/) { bi = i; inb = 1; ki = -1; ina = 0; c = ""; d = ""; next }
      if (!inb && $0 ~ /^[ \t]*build:[ \t]*[^ \t]/) { c = ""; ctxv(val($0)); print "dpath " c "/Dockerfile"; next }
      if (!inb) next
      if (ki < 0) ki = i
      if (i == ki) {
        ina = 0
        k = $0; sub(/^[ \t]*/, "", k); sub(/[ \t]*:.*$/, "", k)
        print "bkey " k
        v = val($0)
        if (k == "args") { if (v != "") print "raw " v; ina = 1; ai = -1; next }
        if (k == "context") ctxv(v)
        if (k == "dockerfile") d = v
        next
      }
      if (ina && i > ki) {
        if (ai < 0) ai = i
        if (i != ai) next
        a = $0; sub(/^[ \t]*/, "", a); sub(/^-[ \t]*/, "", a)
        n = a; sub(/[ \t]*[:=].*$/, "", n)
        v = a; if (v ~ /[:=]/) sub(/^[^:=]*[:=][ \t]*/, "", v); else v = ""
        print "arg " n " " v
      }
    }
    END { endb() }
  ' "$1"
}
# ARGs whose value this gate proves from a Dockerfile default: used in a FROM,
# or named as a ref by git fetch/checkout. Compose must not be able to move one.
dockerfile_args=$(printf '%s\n' $dockerfiles \
  | xargs grep -Eih '^[[:space:]]*from[[:space:]]+\$' 2>/dev/null \
  | sed -E 's/.*\$\{?([A-Za-z0-9_]+).*/\1/' | sort -u || true)
# `git fetch`'s first positional is the repository, not a ref: any repo serves
# the same commit or the SHA check fails, so only the refs after it are pinned.
gitref_args=$(printf '%s\n' $dockerfiles \
  | xargs grep -Eoh 'git[[:space:]]+(fetch|checkout)[^&;|]*' 2>/dev/null \
  | sed -E 's/git[[:space:]]+fetch([[:space:]]+(-[^[:space:]]+|[0-9]+))*[[:space:]]+[^[:space:]]+/git fetch/' \
  | grep -Eo '\$\{?[A-Za-z_][A-Za-z0-9_]*' | tr -d '${' | sort -u || true)
for cf in $compose_files; do
  grep -Eq '^include[[:space:]]*:' "$cf" && fail "$cf uses include:, which this gate does not follow"
  grep -Eq '^[[:space:]]+extends[[:space:]]*:' "$cf" && fail "$cf uses extends:, which this gate does not follow"
  while read -r kind a b; do
    case "$kind" in
      bkey)
        case "$a" in
          context|dockerfile|args) ;;
          *) fail "$cf build key '$a' is a build input this gate does not check" ;;
        esac ;;
      ctx)
        case "$a" in
          http://*|https://*|git@*|git://*|ssh://*|*.git|*.git#*) fail "$cf remote build context: $a" ;;
        esac ;;
      raw) fail "$cf build value '$a${b:+ $b}' is in a form this gate does not read" ;;
      dpath)
        # The Dockerfile compose will build, resolved against its context, must
        # be one the loop below reads: tracked, and matched by its glob.
        p=$(sed -E 's|(^\|/)\./|\1|g; s|//+|/|g' <<<"$a")
        case "$p" in http://*|https://*|git@*|git://*|ssh://*|*.git*) continue ;; esac
        grep -Fqx -- "$p" <<<"$dockerfiles" || fail "$cf builds $p, which is not a tracked Dockerfile this gate reads" ;;
      arg)
        v=$(sed -E 's/[[:space:]]+#.*$//; s/[[:space:]]+$//; s/^["'"'"']//; s/["'"'"']$//' <<<"$b")
        [[ "$v" =~ ^\$\{[A-Za-z0-9_]+:-(.*)\}$ ]] && v=${BASH_REMATCH[1]}
        if grep -Fqx -- "$a" <<<"$dockerfile_args"; then
          grep -Eq -- "$digest\$" <<<"$v" || fail "$cf build arg $a=$v is not pinned by digest"
        elif [[ "$a" == NVPN_COMMIT ]] || grep -Fqx -- "$a" <<<"$gitref_args"; then
          grep -Eqx '[0-9a-f]{40}' <<<"$v" || fail "$cf build arg $a=$v is not a full commit SHA"
        elif [[ "$a" == NVPN_REPO ]]; then
          : # any repo is fine: the commit SHA proves the content, whatever served it
        else
          fail "$cf build arg '$a' has no rule in this gate (add one if it is safe)"
        fi ;;
    esac
  done < <(compose_build_keys "$cf")
done

# Every tracked Dockerfile ships in the installer payload, whatever its suffix.
for df in $dockerfiles; do
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
  # SHELL replaces the command line every later RUN is appended to, so it runs
  # whatever it likes while the RUN the gate reads stays in the allowlist.
  if grep -Eiq '^[[:space:]]*shell[[:space:]]' "$df"; then
    fail "$df uses SHELL, which this gate does not read"
  fi
  if grep -Eiq '^[[:space:]]*onbuild[[:space:]]' "$df"; then
    fail "$df uses ONBUILD, whose instructions this gate does not read"
  fi
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
        git@*|git://*|ssh://*|*.git|*.git#*|*.git/*)
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

  runs=$(grep -Ei '^[[:space:]]*run[[:space:]]' <<<"$joined" || true)
  # A command substitution runs a command of its own, so each innermost $(...)
  # is checked as a command too. Backticks are refused rather than parsed.
  grep -q '`' <<<"$runs" && fail "$df uses a backtick command substitution, which this gate does not read"
  # $( (cmd) ) and $(a $(b) c) are substitutions the innermost-only pattern
  # below cannot read. Count them: one extraction per $( or the gate is blind.
  opens=$( { grep -o '\$(' <<<"$runs" || true; } | wc -l | tr -d ' ')
  reads=$( { grep -o '\$([^()]*)' <<<"$runs" || true; } | wc -l | tr -d ' ')
  [[ "$opens" == "$reads" ]] || fail "$df has a command substitution this gate cannot read ($opens found, $reads readable)"
  cmds=$( { echo "$runs"; grep -o '\$([^()]*)' <<<"$runs" | sed -E 's/^\$\(//; s/\)$//' || true; } \
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
      git\ fetch\ *|git\ checkout\ *|git\ checkout)
        # Every ref named here must be pinned: a 40-hex literal, FETCH_HEAD, or
        # ${VAR}/$VAR whose ARG default in this Dockerfile is declared exactly
        # once and is 40-hex. One pinned ref is not enough -- `git fetch R <sha>
        # main` then `git checkout main` would otherwise ride through.
        pinned_ref() {
          local a=$1
          a=${a//\"/}; a=${a//\'/}
          [[ "$a" == FETCH_HEAD ]] && return 0
          grep -Eqx '[0-9a-f]{40}' <<<"$a" && return 0
          if [[ "$a" =~ ^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$ ]]; then
            local defs
            defs=$(grep -Ei "^[[:space:]]*arg[[:space:]]+${BASH_REMATCH[1]}=" "$df" | cut -d= -f2- | sed -E 's/[[:space:]]+$//' || true)
            [[ -n "$defs" && $(wc -l <<<"$defs") -eq 1 ]] && grep -Eqx '[0-9a-f]{40}' <<<"$defs" && return 0
          fi
          return 1
        }
        read -ra words <<<"$cmd"
        skip=0; pos=0; refs=0
        for a in "${words[@]:2}"; do
          if [[ "$skip" == 1 ]]; then skip=0; continue; fi
          case "$a" in
            --depth|--deepen|-j|--jobs|--upload-pack|--negotiation-tip|--refmap|-o|--server-option) skip=1; continue ;;
            -*) continue ;;
          esac
          pos=$((pos+1))
          # `git fetch`'s first positional is the repository; `git checkout`'s is a ref.
          [[ "${words[1]}" == fetch && "$pos" == 1 ]] && continue
          refs=$((refs+1))
          pinned_ref "$a" || fail "$df unpinned ref in: $cmd"
        done
        [[ "${words[1]}" == fetch && "$refs" == 0 ]] && fail "$df git fetch names no commit: $cmd" ;;
      mdbook\ build*|git\ init*|git\ rev-parse*|\
      cd\ *|cp\ *|mv\ *|rm\ *|mkdir\ *|ln\ *|chmod\ *|chown\ *|test\ *|\
      echo\ *|printf\ *|grep\ *|set\ *|exit|exit\ *|fi|true|false|\
      apt-get\ update*|apt-get\ install\ *) ;;
      *) fail "$df command not allowed by the gate (add it with a rule if it is safe): $cmd" ;;
    esac
  done <<<"$cmds"
done

# mdbook runs any preprocessor or renderer `command` in book.toml, so an
# allowed `mdbook build` is only as pinned as that file.
for bt in $(git ls-files '*book.toml'); do
  grep -Eq '^[[:space:]]*command[[:space:]]*=' "$bt" && fail "$bt sets a command for mdbook to run: $(grep -Em1 '^[[:space:]]*command[[:space:]]*=' "$bt")"
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
