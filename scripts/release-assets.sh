#!/usr/bin/env bash
# Builds the files a release publishes, or refuses. Run by the release
# workflow on a tag, and by hand to see what it would publish.
#
#   scripts/release-assets.sh vX.Y.Z <out-dir>
#
# Refuses unless:
#   - the tag is exactly vX.Y.Z (no branch names, no prerelease suffix);
#   - the tag exists, points at the commit checked out, and that commit is on
#     origin's main (needs full history, not a depth-1 checkout);
#   - scripts/install.sh is byte for byte what this tree builds, its .sha256
#     is its hash, and the pins pass (make-installer.sh --check).
# Writes install.sh, install.sh.sha256 and notes.md (the hash and the check a
# user runs) to <out-dir>, then verifies the pair the way a user will.
# SOVEREIGN_DNS_RELEASE_DRY=1 skips the tag and main tests, for the
# test and for a look before tagging.
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "release-assets: $*" >&2; exit 1; }

tag=${1:-}
out=${2:-}
[[ -n "$out" ]] || die "usage: release-assets.sh vX.Y.Z <out-dir>"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "'$tag' is not a release tag (vX.Y.Z)"

if [[ "${SOVEREIGN_DNS_RELEASE_DRY:-}" != "1" ]]; then
  git rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null \
    || die "tag $tag does not exist"
  [[ "$(git rev-parse "refs/tags/$tag^{commit}")" == "$(git rev-parse HEAD)" ]] \
    || die "tag $tag is not the commit checked out; release from the tag itself"
  # Self-consistency (--check below) is not review: a tag on an unmerged
  # branch with a doctored file would pass it. Only commits on main release.
  git fetch -q origin main || die "cannot fetch main to check the tag is on it"
  git merge-base --is-ancestor HEAD refs/remotes/origin/main \
    || die "tag $tag is not on main; release only from main"
fi

scripts/make-installer.sh --check >&2 || die "install.sh is not what this tree builds, or its pins fail"

mkdir -p "$out"
cp scripts/install.sh scripts/install.sh.sha256 "$out/"
(cd "$out" && if command -v sha256sum >/dev/null 2>&1; then sha256sum -c install.sh.sha256; else shasum -a 256 -c install.sh.sha256; fi) >&2 \
  || die "the published pair does not verify"

hash=$(cut -d' ' -f1 "$out/install.sh.sha256")
base="https://github.com/btcforplebs/sovereign-dns/releases/download/$tag"
# The same line README.md carries with vX.Y.Z in place of the tag
# (scripts/test-release-assets.sh holds the two together).
verify="rm -f install.sh install.sh.sha256 && curl -fsSLO $base/install.sh && curl -fsSLO $base/install.sh.sha256 && shasum -a 256 -c install.sh.sha256 && bash install.sh"
cat > "$out/notes.md" <<N
Sovereign DNS $tag

SHA-256 of install.sh:

\`$hash\`

Check it before you run it (on Linux, \`sha256sum -c\` does the same as \`shasum -a 256 -c\`):

\`\`\`bash
$verify
\`\`\`

The two files here are signed by this repository's release workflow:
\`gh attestation verify install.sh --repo btcforplebs/sovereign-dns\`
N
echo "release-assets: $tag ready in $out (sha256 $hash)" >&2
