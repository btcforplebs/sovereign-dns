# shellcheck shell=bash
# The name of the ready-made nvpn image this tree would build: one definition,
# read by CI (which builds and tags it), scripts/repin-prebuilt.sh (which pins
# it) and scripts/check-pins.sh (which fails when the pin no longer matches the
# tree). Two copies of this string that disagreed would let an install
# download an image built from different code than the tree it ships.
#
# Sourced; defines functions and does nothing on its own. Run from the repo
# root (it reads tracked files with git).
#
#   nvpn_image_repo   ghcr.io/btcforplebs/sovereign-dns-nvpn
#   nvpn_image_tag    nvpn-<NVPN_COMMIT, 12 chars>-ctx-<hash of nvpn/, 12 chars>
#
# The image is built from the nvpn/ folder alone (compose's build context),
# with the Dockerfile's own ARG defaults, which check-pins.sh holds equal to
# compose's. So the nvpn commit plus every tracked file under nvpn/ (its path,
# content and mode, since COPY keeps the mode) are the whole build input.
# Change any of them and the tag changes, and the pinned image no longer
# matches the tree.

nvpn_image_repo() { echo ghcr.io/btcforplebs/sovereign-dns-nvpn; }

nvpn_image_commit() {
  local c
  c=$(grep -Ei '^[[:space:]]*arg[[:space:]]+NVPN_COMMIT=' nvpn/Dockerfile | cut -d= -f2- | sed -E 's/[[:space:]]+$//')
  [[ "$c" =~ ^[0-9a-f]{40}$ ]] || { echo "nvpn/Dockerfile has no single 40-char NVPN_COMMIT default" >&2; return 1; }
  echo "$c"
}

# Hash of the build context as the installer packs it (make_payload in
# make-installer.sh): content from the working tree, mode from the index.
# One line per tracked file, "<mode> <git blob id> <path>", sorted, then sha256.
nvpn_image_context() {
  local files f h
  files=$(git ls-files nvpn | LC_ALL=C sort)
  [[ -n "$files" ]] || { echo "no tracked files under nvpn/" >&2; return 1; }
  h=$(while IFS= read -r f; do printf '%s %s %s\n' "$(git ls-files -s -- "$f" | cut -d' ' -f1)" "$(git hash-object -- "$f")" "$f"; done <<<"$files" \
        | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; })
  echo "${h%% *}"
}

nvpn_image_tag() {
  local c x
  c=$(nvpn_image_commit) || return 1
  x=$(nvpn_image_context) || return 1
  echo "nvpn-${c:0:12}-ctx-${x:0:12}"
}
