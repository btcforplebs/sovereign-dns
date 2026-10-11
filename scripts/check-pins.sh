#!/usr/bin/env bash
# Fails if anything the stack builds is referenced by a name that can change
# underneath us: an image without a digest, nvpn by anything but a full commit
# SHA, an ADD or git fetch of something not pinned to a checksum or commit, a
# package manager allowed to re-resolve its lockfile, any RUN command the gate
# has no rule for, or a compose build section that bypasses all of that. Two
# installs of the same installer must build the same bits.
#
# Scope: build inputs, plus the compose settings that decide what the box
# exposes (pihole's FTLCONF keys, mounts, ports, network_mode, networks; see
# the end of this file). What a container runs (CMD, ENTRYPOINT, compose
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

# Fails on any flag in $1 that is not named in the rest of the arguments. A
# rule that only requires --locked still allows --index: for a command the gate
# trusts, the flags are part of what it has to trust.
flags_allowed() {
  local cmd orig f ok
  orig=$1
  # Collapse quoted values first: -ldflags="-s -w" is one flag, not two.
  cmd=$(sed -E 's/"[^"]*"/""/g; s/'"'"'[^'"'"']*'"'"'/'"'"''"'"'/g' <<<"$1"); shift
  read -ra _words <<<"$cmd"
  for f in "${_words[@]}"; do
    case "$f" in -*) ;; *) continue ;; esac
    f=${f%%=*}; ok=
    for a in "$@"; do [[ "$f" == "$a" ]] && ok=1; done
    [[ -n "$ok" ]] || fail "$df flag $f is not one this gate's rule for '${_words[0]} ${_words[1]:-}' allows: $orig"
  done
}
dockerfiles=$(git ls-files '*[Dd]ockerfile*')
[[ -n "$dockerfiles" ]] || fail "no tracked Dockerfile found"

# Every tracked compose file: compose auto-loads docker-compose.override.yml,
# and any of them can be passed with -f.
compose_files=$(git ls-files '*compose*.yml' '*compose*.yaml')
[[ -n "$compose_files" ]] || fail "no tracked compose file found"

# Read each compose file the way compose reads it. `docker compose config
# --no-interpolate` resolves anchors, aliases, merge keys, flow style, include:
# and extends: into canonical block YAML, and leaves ${X:-default} literal, so
# the verdict does not depend on .env or the caller's environment (and a failing
# gate cannot print PIHOLE_PASSWORD). One file per render: compose only renders
# the files it is given, so a tracked docker-compose.prod.yml would otherwise
# never be read. The render is client-side and needs no daemon. Without the
# compose CLI, the raw files are read instead and must be block style (below).
root=$(pwd -P)
rendered=$(mktemp -d)
trap 'rm -rf "$rendered"' EXIT
yaml() { echo "$rendered/$(tr / _ <<<"$1")"; }
if docker compose version >/dev/null 2>&1; then mode=rendered; else
  mode=raw; echo "note: docker compose not found; reading compose files as written"
fi
for cf in $compose_files; do
  if [[ "$mode" == raw ]]; then cp "$cf" "$(yaml "$cf")"; continue; fi
  # Nonzero means "could not read it", which must not fall through as "read it
  # and it is fine". --profile '*' keeps services in any profile in the render.
  if ! env -i PATH="$PATH" HOME="$HOME" docker compose -f "$cf" --env-file /dev/null --profile '*' \
         config --no-interpolate >"$(yaml "$cf")" 2>"$rendered/err"; then
    fail "$cf: docker compose could not render it: $(head -c 300 "$rendered/err")"
  elif ! grep -q '^services:' "$(yaml "$cf")"; then
    fail "$cf: docker compose rendered no services from it"
  fi
done

# Compose services that pull an image.
for cf in $compose_files; do
  while IFS= read -r line; do
    ref=$(sed -E 's/^[[:space:]]*image:[[:space:]]*//; s/[[:space:]]+#.*$//; s/[[:space:]]+$//' <<<"$line")
    grep -Eq -- "$digest\$" <<<"$ref" || fail "$cf image $ref"
  done < <(grep -E '^[[:space:]]*image:' "$(yaml "$cf")" || true)
done

# Raw files only (no compose CLI): everything below reads compose line by line,
# so a flow mapping or a whole JSON document would hide keys from it rather than
# fail. YAML is a superset of JSON: `{"services":{"docs":{"build":{"cache_from":
# [...]}}}}` is a valid compose file that no line-oriented check can see. Require
# block style, with ${...} interpolation (and flow sequences, which cannot carry
# keys) exempt. include: and extends: pull in files this reading does not follow.
if [[ "$mode" == raw ]]; then for cf in $compose_files; do
  grep -Eq '^include[[:space:]]*:' "$cf" && fail "$cf uses include:, which this gate does not follow without docker compose"
  grep -Eq '^[[:space:]]+extends[[:space:]]*:' "$cf" && fail "$cf uses extends:, which this gate does not follow without docker compose"
  if sed -E 's/\$\{[^}]*\}//g; s/[[:space:]]#.*$//' "$cf" | grep -q '[{}]'; then
    fail "$cf uses a flow mapping, which this gate reads line by line and cannot see into"
  fi
  # `? key` puts a key where a line-oriented read does not look for one, and a
  # double-quoted scalar can be folded over two lines, so `? "FTLCONF_misc_\`
  # hides a setting from every key scan below. Both are block YAML this gate
  # still cannot read.
  grep -Eq '^[[:space:]]*\?[[:space:]]' "$cf" && fail "$cf uses an explicit '? key', which this gate reads line by line and cannot see"
  awk '{ n = gsub(/"/, "\""); if (n % 2) exit 1 }' "$cf" \
    || fail "$cf has a double-quoted value continued onto another line, which this gate reads line by line and cannot see"
done; fi

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
        # A rendered context is absolute; the tracked list is repo-relative.
        p=${a#"$root"/}
        p=$(sed -E 's|(^\|/)\./|\1|g; s|//+|/|g' <<<"$p")
        case "$p" in http://*|https://*|git@*|git://*|ssh://*|*.git*) continue ;; esac
        grep -Fqx -- "$p" <<<"$dockerfiles" || fail "$cf builds $p, which is not a tracked Dockerfile this gate reads" ;;
      arg)
        v=$(sed -E 's/[[:space:]]+#.*$//; s/[[:space:]]+$//; s/^["'"'"']//; s/["'"'"']$//' <<<"$b")
        # A build arg must be a literal. ${X:-pinned} is not a pin: .env or the
        # caller's environment supplies X, the gate reads the tree, and the two
        # disagree with no warning (install.sh itself writes NVPN_REPO to .env).
        case "$v" in *'${'*|*'$'[A-Za-z_]*) fail "$cf build arg $a=$v is interpolated, so .env decides it; write the pinned value literally"; continue ;; esac
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
  done < <(compose_build_keys "$(yaml "$cf")")
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
  # Every toolchain here reads its own config from the environment, and those
  # settings decide where code comes from: GOPROXY/GOFLAGS for go, CARGO_* and
  # RUSTUP_DIST_SERVER for cargo (RUSTUP_TOOLCHAIN alone makes any cargo call
  # download a whole toolchain), COREPACK_NPM_REGISTRY for corepack, npm_config_*
  # for pnpm. Default-deny the namespaces and allow the two this stack sets.
  while IFS= read -r v; do
    case "$v" in
      RUST_LOG|CGO_ENABLED) continue ;;
      CARGO*|RUSTUP*|RUSTC*|RUSTFLAGS|COREPACK*|PNPM*|YARN*|NPM_CONFIG*|npm_config*|GOFLAGS|GOPROXY|GOSUMDB|GONOSUM*|GOINSECURE|GOPRIVATE|GOWORK|GOPATH|GOMODCACHE|GOTOOLCHAIN)
        fail "$df sets $v, a toolchain setting that decides where code comes from" ;;
    esac
  done < <(grep -Eio '(^[[:space:]]*(env|arg)[[:space:]]+|[[:space:]])[A-Za-z_][A-Za-z0-9_]*=' <<<"$joined" \
             | sed -E 's/^[[:space:]]*(env|arg)[[:space:]]+//I; s/^[[:space:]]+//; s/=$//' | sort -u || true)

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
        esac
        # --index, --registry and --config each move where the crate comes from
        # while leaving --locked and --version in place, so flags are an
        # allowlist too, not just a list of ones that must be present.
        flags_allowed "$cmd" --locked --frozen --release --version -p --package --bin --bins --features --no-default-features --root ;;
      pnpm\ install*|pnpm\ i\ *|pnpm\ i)
        grep -Eq -- '--frozen-lockfile' <<<"$cmd" || fail "$df no --frozen-lockfile: $cmd"
        flags_allowed "$cmd" --frozen-lockfile --store-dir --prod --ignore-scripts ;;
      pnpm\ run\ *|npm\ ci|npm\ ci\ *|npm\ run\ *) ;;
      corepack\ enable|corepack\ enable\ *) ;;
      corepack\ prepare\ *)
        grep -Eq -- ' [a-z]+@[0-9]+\.[0-9]+\.[0-9]+( |$)' <<<"$cmd" || fail "$df no exact version: $cmd"
        flags_allowed "$cmd" --activate ;;
      # go build verifies go.sum read-only by default; go install/get resolve.
      # -mod=mod is GOFLAGS=-mod=mod spelled as a flag.
      go\ build*) flags_allowed "$cmd" -trimpath -ldflags -o -tags -buildvcs ;;
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
  done < <(for cf in $compose_files; do cat "$(yaml "$cf")"; done \
             | grep -Eh '^[[:space:]]*(-[[:space:]]*)?NVPN_COMMIT[[:space:]]*[:=]' \
             | sed -E 's/^[[:space:]]*(-[[:space:]]*)?NVPN_COMMIT[[:space:]]*[:=][[:space:]]*//' || true)
}
shas=$( { compose_shas
          grep -Eih '^[[:space:]]*arg[[:space:]]+NVPN_COMMIT=' nvpn/Dockerfile | sed -E 's/^[[:space:]]*[Aa][Rr][Gg][[:space:]]+NVPN_COMMIT=//; s/[[:space:]]+$//' || true; } | sort -u)
[[ -n "$shas" ]] || fail "no NVPN_COMMIT default found"
for s in $shas; do
  grep -Eqx '[0-9a-f]{40}' <<<"$s" || fail "NVPN_COMMIT default '$s' is not a full commit SHA"
done
[[ $(wc -l <<<"$shas") -le 1 ]] || fail "NVPN_COMMIT defaults disagree: $(echo $shas)"

# Pi-hole settings that decide what listens on the mesh. They are pinned like
# a digest: a literal value in the pihole service, never ${...}, because .env
# beats a compose default. Losing BIND or an NTP line opens a listener to every
# mesh device with the same image digest, so no digest check would notice.
# `docker compose up` does not load docker-compose.yml by name: it loads the
# first of compose.yaml/compose.yml/docker-compose.yaml/docker-compose.yml it
# finds, plus the matching override file. Read that set -- the one the installer
# builds -- not a fixed filename, or a tracked compose.yaml or
# docker-compose.override.yml can set these keys to anything it likes.
loaded=$(yaml __loaded__)
if [[ "$mode" == rendered ]]; then
  env -i PATH="$PATH" HOME="$HOME" docker compose --env-file /dev/null --profile '*' \
    config --no-interpolate >"$loaded" 2>"$rendered/err" \
    || fail "docker compose could not render the files it auto-loads: $(head -c 300 "$rendered/err")"
else
  cat $compose_files > "$loaded"
fi
pihole_env=$(awk '/^  pihole:[[:space:]]*$/ { inp = 1; next }
                  inp && /^  [^[:space:]#]/ { inp = 0 }
                  inp' "$loaded")
for want in 'FTLCONF_dns_listeningMode BIND' 'FTLCONF_ntp_ipv4_active false' 'FTLCONF_ntp_ipv6_active false'; do
  read -r key val <<<"$want"
  grep -Eq "^[[:space:]]*(-[[:space:]]*)?${key}[[:space:]]*[:=][[:space:]]*[\"']?${val}[\"']?[[:space:]]*(#.*)?$" <<<"$pihole_env" \
    || fail "pihole must set $key to a literal $val in the compose files the installer loads"
  # Raw mode cannot tell which file wins, so no tracked file may disagree.
  if [[ "$mode" == raw ]]; then
    while IFS= read -r line; do
      grep -Eq "[:=][[:space:]]*[\"']?${val}[\"']?[[:space:]]*(#.*)?$" <<<"$line" \
        || fail "a tracked compose file sets $key to something other than $val: $line"
    done < <(grep -Eh "^[[:space:]]*(-[[:space:]]*)?${key}[[:space:]]*[:=]" $compose_files || true)
  fi
done

# Raw mode cannot tell which of several auto-loadable names compose would load,
# and an omitted key is as good as a flipped one: a tracked compose.yaml that
# wins precedence and simply leaves these keys out hands FTL its own defaults
# (listen on everything, NTP server on). Refuse to judge such a tree.
if [[ "$mode" == raw ]]; then
  n=$(printf '%s\n' $compose_files | grep -Ecx '(docker-)?compose\.(yml|yaml)' || true)
  [[ "$n" -le 1 ]] || fail "more than one auto-loaded compose file is tracked and this gate cannot tell which one compose would load without the compose CLI"
fi

# The three values above are pinned; the rest of the FTLCONF namespace is not,
# and FTL has settings that open a listener or move upstreams just as well.
# FTLCONF_misc_dnsmasq_lines passes dnsmasq config straight through. Allow the
# keys this stack sets and fail the rest, the way RUN commands are handled.
# Read keys only, at the start of a line (map or list form, quoted or not): a
# comment that names another key is not a setting.
while IFS= read -r k; do
  case "$k" in
    FTLCONF_webserver_api_password|FTLCONF_dns_upstreams|FTLCONF_dns_interface|\
    FTLCONF_dns_listeningMode|FTLCONF_dns_dnssec|FTLCONF_ntp_ipv4_active|\
    FTLCONF_ntp_ipv6_active|FTLCONF_webserver_port) ;;
    *) fail "pihole sets $k, an FTL setting this gate has no rule for (add one if it is safe)" ;;
  esac
done < <(sed -nE "s/^[[:space:]]*(-[[:space:]]*)?[\"']?(FTLCONF_[A-Za-z0-9_]+).*/\\2/p" "$loaded" | sort -u)

# env_file contents are not in the render -- `config` prints the path only --
# so an FTLCONF setting there is invisible to every check above.
grep -Eq '^[[:space:]]+env_file[[:space:]]*:' "$loaded" && fail "a compose service uses env_file:, whose contents this gate does not read"

# What a service can reach and be reached by is as much a part of the box's
# exposure as the three FTLCONF values: a bind mount can replace
# forward-records.conf (the file that stops Unbound forwarding to a third
# party) or drop config into /etc/dnsmasq.d, a published port lands in the
# host netns where the listener inventory never looks, and network_mode or a
# new network moves a service out of, or adds an interface to, nvpn's netns.
# Default-deny, like RUN commands: every service, every service key, every
# top-level network key, and every item of the keys below must be one this
# gate has a rule for. Each value is listed in both the canonical form compose
# renders and the short form the tree is written in (raw mode).
surface() {
  awk '
    function flush() { if (it != "") print svc "\t" key "\t" it; it = "" }
    BEGIN { split("ports volumes network_mode devices cap_add networks", w, " "); for (i in w) W[w[i]] = 1 }
    {
      line = $0
      sub(/[[:space:]]+#.*$/, "", line); sub(/[[:space:]]+$/, "", line)
      if (line ~ /^[[:space:]]*(#|$)/) next
      match(line, /^ */); n = RLENGTH; body = substr(line, n + 1)
      if (n == 0) { flush(); top = body; sub(/:.*/, "", top); si = ki = ii = mi = -1; next }
      if (top != "services" && top != "networks") next
      if (si < 0) si = n
      if (n == si) { flush(); svc = body; sub(/:.*/, "", svc); if (top == "networks") svc = "@network:" svc; key = ""; ki = -1; next }
      if (ki < 0) ki = n
      if (n == ki) {
        flush(); key = body; v = body; sub(/:.*/, "", key); ii = mi = -1
        print svc "\t" key "\t"
        if (sub(/^[^:]*:[[:space:]]*/, "", v) && v != "" && (key in W)) { gsub(/["\047]/, "", v); print svc "\t" key "\t" v }
        next
      }
      if (!(key in W)) next
      if (body ~ /^- /) {
        flush(); ii = n + 2; v = substr(body, 3)
        if (v ~ /^[A-Za-z_]+:( |$)/) { body = v; n = ii } else { gsub(/["\047]/, "", v); it = v; next }
      } else if (ii < 0) {
        # Map form (networks:): the names at the first level are the items.
        if (mi < 0) mi = n
        if (n == mi) { v = body; sub(/:.*/, "", v); print svc "\t" key "\t" v }
        next
      }
      if (n == ii && body ~ /^[A-Za-z_]+:/) {
        f = body; sub(/:.*/, "", f); v = body; sub(/^[^:]*:[[:space:]]*/, "", v); gsub(/["\047]/, "", v)
        if (v != "") it = it (it == "" ? "" : ";") f "=" v
      }
    }
    END { flush() }' "$1"
}
surface_allowed=$(cat <<'ALLOW'
docs	ports	127.0.0.1:${DOCS_PORT:-8081}:80
docs	networks	default
hub	ports	127.0.0.1:${HUB_PORT:-8080}:8080
hub	networks	default
hub	networks	dns-internal
web	ports	127.0.0.1:${WEB_UI_PORT:-8082}:8081
web	networks	default
web	volumes	source=nvpn-config;target=/root/.config/nvpn;type=volume;volume={}
web	volumes	nvpn-config:/root/.config/nvpn
nvpn	ports	${NVPN_LISTEN_PORT:-51820}:51820/udp
nvpn	networks	dns-internal
nvpn	cap_add	NET_ADMIN
nvpn	devices	permissions=rwm;source=/dev/net/tun;target=/dev/net/tun
nvpn	devices	/dev/net/tun:/dev/net/tun
nvpn	volumes	source=nvpn-config;target=/root/.config/nvpn;type=volume;volume={}
nvpn	volumes	nvpn-config:/root/.config/nvpn
pihole	network_mode	service:nvpn
pihole	cap_add	NET_BIND_SERVICE
pihole	volumes	source=pihole-config;target=/etc/pihole;type=volume;volume={}
pihole	volumes	pihole-config:/etc/pihole
unbound	networks	dns-internal
unbound	volumes	read_only=true;source=./unbound/forward-records.conf;target=/opt/unbound/etc/unbound/forward-records.conf;type=bind
unbound	volumes	./unbound/forward-records.conf:/opt/unbound/etc/unbound/forward-records.conf:ro
ALLOW
)
surface_files="$loaded"
for cf in $compose_files; do surface_files+=" $(yaml "$cf")"; done
for f in $surface_files; do
  while IFS=$'\t' read -r s k v; do
    case "$s" in
      docs|hub|web|nvpn|pihole|unbound) ;;
      @network:dns-internal|@network:default)
        case "$k" in driver|internal|ipam|name) ;; *) fail "network ${s#@network:} sets $k, which this gate has no rule for (enable_ipv6 would give the bridge an address [::] listeners answer on)" ;; esac
        continue ;;
      @network:*) fail "a compose file defines network ${s#@network:}, which this gate has no rule for"; continue ;;
      *) fail "a compose file defines service $s, which this gate has no rule for"; continue ;;
    esac
    case "$k" in
      build|cap_add|command|container_name|depends_on|devices|entrypoint|environment|healthcheck|image|network_mode|networks|ports|restart|volumes) ;;
      *) fail "$s sets $k, a service key this gate has no rule for" ;;
    esac
    [[ -z "$v" ]] && continue
    grep -Fqx -- "$s	$k	$v" <<<"$surface_allowed" \
      || fail "$s $k '$v' is not one this gate allows (bind mounts, published ports, network_mode, networks, devices and capabilities are default-deny)"
  done < <(sed "s|$root/|./|g" "$f" | surface /dev/stdin)
done
# Required, not just allowed: pihole serves the mesh only from inside nvpn's netns.
surface /dev/stdin <"$loaded" | grep -Fqx "pihole	network_mode	service:nvpn" \
  || fail "pihole must set network_mode: service:nvpn in the compose files the installer loads"

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
