# shellcheck shell=bash
# The names of the ready-made images this tree would build: one definition,
# read by CI (which builds and tags them), scripts/repin-prebuilt.sh (which
# pins them) and scripts/check-pins.sh (which fails when a pin no longer
# matches the tree). Two copies of these strings that disagreed would let an
# install download an image built from different code than the tree it ships.
#
# Sourced; defines functions and does nothing on its own. Run from the repo
# root (it reads tracked files with git).
#
# One image per folder the stack builds from. The folder is the image's whole
# build context (compose's build.context), built with the Dockerfile's own ARG
# defaults, which check-pins.sh holds equal to compose's. So every tracked file
# under the folder (its path, content and mode, since COPY keeps the mode),
# plus the nvpn commit for nvpn, is the whole build input. Change any of them
# and the tag changes, and the pinned image no longer matches the tree.
#
#   prebuilt_images           nvpn unbound hub docs
#   prebuilt_services <img>   the compose services that run it
#   prebuilt_repo <img>       ghcr.io/btcforplebs/sovereign-dns-<img>
#   prebuilt_tag <img> [commit]
#                             nvpn: nvpn-<NVPN_COMMIT, 12>-ctx-<hash of nvpn/, 12>
#                             others: <img>-ctx-<hash of <img>/, 12>
#                             With a commit, the tag that commit's tree names:
#                             repin-prebuilt.sh holds an image's attested source
#                             commit to the tag it pins.

prebuilt_images() { echo nvpn unbound hub docs; }

prebuilt_services() {
  case "$1" in
    nvpn) echo nvpn web ;;
    unbound) echo unbound ;;
    hub) echo hub hub-mesh dns-lan ;;
    docs) echo docs ;;
    *) echo "no prebuilt image named '$1'" >&2; return 1 ;;
  esac
}

prebuilt_repo() { prebuilt_services "$1" >/dev/null && echo "ghcr.io/btcforplebs/sovereign-dns-$1"; }

nvpn_image_commit() {
  local c
  c=$(if [[ -n "${1:-}" ]]; then git show "$1:nvpn/Dockerfile"; else cat nvpn/Dockerfile; fi \
        | grep -Ei '^[[:space:]]*arg[[:space:]]+NVPN_COMMIT=' | cut -d= -f2- | sed -E 's/[[:space:]]+$//')
  [[ "$c" =~ ^[0-9a-f]{40}$ ]] || { echo "nvpn/Dockerfile has no single 40-char NVPN_COMMIT default" >&2; return 1; }
  echo "$c"
}

# Hash of a build context as the installer packs it (make_payload in
# make-installer.sh): content from the working tree, mode from the index.
# One line per tracked file, "<mode> <git blob id> <path>", sorted, then
# sha256. A symlink is hashed as the link, the way git stores it, not as its
# target. With a commit, the same lines come from that commit's tree, which is
# what a clean checkout of it would give.
prebuilt_context() {
  local lines f h
  if [[ -n "${2:-}" ]]; then
    lines=$(git ls-tree -r --full-tree "$2" -- "$1/" | awk -F'\t' '{ split($1, m, " "); print m[1] " " m[3] " " $2 }' | LC_ALL=C sort -k3)
  else
    lines=$(git ls-files -- "$1" | LC_ALL=C sort | while IFS= read -r f; do
      if [[ -L "$f" ]]; then h=$(printf '%s' "$(readlink "$f")" | git hash-object --stdin)
      else h=$(git hash-object -- "$f"); fi
      printf '%s %s %s\n' "$(git ls-files -s -- "$f" | cut -d' ' -f1)" "$h" "$f"
    done)
  fi
  [[ -n "$lines" ]] || { echo "no tracked files under $1/" >&2; return 1; }
  h=$(printf '%s\n' "$lines" | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; })
  echo "${h%% *}"
}

prebuilt_tag() {
  local c x
  prebuilt_services "$1" >/dev/null || return 1
  x=$(prebuilt_context "$1" "${2:-}") || return 1
  if [[ "$1" == nvpn ]]; then
    c=$(nvpn_image_commit "${2:-}") || return 1
    echo "nvpn-${c:0:12}-ctx-${x:0:12}"
  else
    echo "$1-ctx-${x:0:12}"
  fi
}
