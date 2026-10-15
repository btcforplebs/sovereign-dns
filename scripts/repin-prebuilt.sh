#!/usr/bin/env bash
# Pins the ready-made images that installs download instead of building every
# service on the device (nvpn alone takes about 35 minutes on a Raspberry Pi 4,
# the docs site's mdbook another 10+). Writes docker-compose.prebuilt.yml and
# regenerates install.sh.
#
#   scripts/repin-prebuilt.sh
#
# For every image in scripts/lib/prebuilt-images.sh, the tag this tree builds
# (its folder's hash, plus the nvpn commit for nvpn) names exactly one build.
# The "prebuilt images" workflow publishes it on every push to main. An image
# whose tag is not published yet is left out, and installs build that service
# from source until the next repin: run this in a PR that edits a folder, and
# the gate stays green. For every published one, this asks ghcr.io,
# anonymously, the way an install will, and refuses to write anything unless:
#   - the image index stored under the tag's digest really hashes to it,
#   - it has a linux/amd64 and a linux/arm64 image,
#   - GitHub holds a signed build-provenance attestation for that digest,
#     made by this repo's prebuilt images workflow running on main (needs the
#     gh CLI, logged in),
#   - the commit that attestation names as its source builds this same tag:
#     its folder hashes the same (and its NVPN_COMMIT is the same).
# The last check is what ties the bits to this tree. A tag is only a name
# anyone with write access to the package can move; moved to another build of
# the same image, everything else above would still pass.
#
# To go back to building from source everywhere: delete
# docker-compose.prebuilt.yml and run scripts/make-installer.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=lib/prebuilt-images.sh
source scripts/lib/prebuilt-images.sh

die() { echo "repin-prebuilt: $*" >&2; exit 1; }
[[ $# -eq 0 ]] || die "usage: $0 (no arguments: every tag comes from the tree)"
command -v python3 >/dev/null 2>&1 || die "needs python3 (to read the registry's JSON)"
command -v gh >/dev/null 2>&1 || die "needs the GitHub CLI, gh (to check the build attestations)"

accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json'
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# pin <image>: prints repo:tag@digest after every check above, or nothing
# when the tag is not published.
pin() {
  local img=$1 repo tag name token digest got platforms want src code
  repo=$(prebuilt_repo "$img"); tag=$(prebuilt_tag "$img")
  name=${repo#ghcr.io/}
  # No anonymous token (401/403/404): the package does not exist yet, or is
  # private. Either way an install could not pull it, so it is left out like
  # an unpublished tag. Anything else, a dropped connection included, stops
  # the repin: an outage must not quietly un-pin images that work.
  code=$(curl -sS -o "$tmp/$img.token" -w '%{http_code}' \
           "https://ghcr.io/token?service=ghcr.io&scope=repository:$name:pull" 2>/dev/null) || code=000
  case "$code" in
    200) token=$(sed -E 's/.*"token":"([^"]+)".*/\1/' "$tmp/$img.token") ;;
    401|403|404)
      echo "skipped: $repo is not published or not public; $(prebuilt_services "$img") will build from source" >&2
      return 0 ;;
    *) die "ghcr.io token request for $name answered HTTP $code; not changing any pin" ;;
  esac
  [[ -n "$token" && "$token" != *'{'* ]] || die "ghcr.io gave an unreadable token for $name"
  code=$(curl -sS -I -o "$tmp/$img.head" -w '%{http_code}' -H "Authorization: Bearer $token" -H "Accept: $accept" \
           "https://ghcr.io/v2/$name/manifests/$tag" 2>/dev/null) || code=000
  case "$code" in
    200) digest=$(tr -d '\r' <"$tmp/$img.head" | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: //p') ;;
    404)
      echo "skipped: $repo:$tag is not published; $(prebuilt_services "$img") will build from source" >&2
      return 0 ;;
    *) die "ghcr.io answered HTTP $code for $repo:$tag; not changing any pin" ;;
  esac
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "ghcr.io answered $repo:$tag with digest '$digest'"
  curl -fsS -H "Authorization: Bearer $token" -H "Accept: $accept" \
    "https://ghcr.io/v2/$name/manifests/$digest" -o "$tmp/$img.json" \
    || die "ghcr.io has no image index $digest in $name"
  # Content addressing: what came back must hash to what was asked for.
  if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum <"$tmp/$img.json")
  else got=$(shasum -a 256 <"$tmp/$img.json"); fi
  [[ "sha256:${got%% *}" == "$digest" ]] || die "what ghcr.io returned for $repo@$digest hashes to sha256:${got%% *}"
  platforms=$(python3 -I -c '
import json, sys
idx = json.load(open(sys.argv[1]))
for m in idx.get("manifests", []):
    p = m.get("platform") or {}
    print("%s/%s" % (p.get("os"), p.get("architecture")))
' "$tmp/$img.json") || die "$repo@$digest is not an image index"
  for want in linux/amd64 linux/arm64; do
    grep -Fqx "$want" <<<"$platforms" || die "$repo@$digest has no $want image (it has: $(echo $platforms))"
  done
  # Who built it: only a run of this repo's own workflow, from main, can have
  # signed this. A digest pushed by hand, or by a run on another branch, fails.
  gh attestation verify "oci://$repo@$digest" \
      --repo btcforplebs/sovereign-dns \
      --signer-workflow btcforplebs/sovereign-dns/.github/workflows/nvpn-image.yml \
      --source-ref refs/heads/main --format json >"$tmp/$img.att.json" \
    || die "no build attestation from the prebuilt images workflow on main for $repo@$digest"
  # Which commit the verified provenance says it was built from. Every
  # verified attestation must name the same one.
  src=$(python3 -I -c '
import json, sys
commits = set()
for r in json.load(open(sys.argv[1])):
    pred = r["verificationResult"]["statement"]["predicate"]
    for d in pred["buildDefinition"]["resolvedDependencies"]:
        c = (d.get("digest") or {}).get("gitCommit")
        if c:
            commits.add(c)
if len(commits) != 1:
    sys.exit("attestations name %d source commits" % len(commits))
print(commits.pop())
' "$tmp/$img.att.json") || die "could not read one source commit from the attestation for $repo@$digest"
  [[ "$src" =~ ^[0-9a-f]{40}$ ]] || die "attestation for $repo@$digest names source commit '$src'"
  git cat-file -e "$src^{commit}" 2>/dev/null || git fetch -q origin "$src" \
    || die "cannot fetch $src, the commit $repo@$digest was built from"
  want=$(prebuilt_tag "$img" "$src") || die "cannot work out the $img tag at $src"
  [[ "$want" == "$tag" ]] \
    || die "$repo:$tag points at $digest, but that image was built from $src, whose tree builds $want: the tag was moved"
  echo "$repo:$tag@$digest"
}

# Every image first, file second: a failure leaves the old pins untouched.
any=0
for img in $(prebuilt_images); do
  pin "$img" >"$tmp/$img.ref"
  if [[ -s "$tmp/$img.ref" ]]; then any=1; echo "ok: $(cat "$tmp/$img.ref")"; fi
done
if [[ "$any" == 0 ]]; then
  # Staged, not just removed: the gates read the tracked file list.
  if git ls-files --error-unmatch docker-compose.prebuilt.yml >/dev/null 2>&1; then
    git rm -q -f docker-compose.prebuilt.yml
  else
    rm -f docker-compose.prebuilt.yml
  fi
  echo "no image is published for this tree: removed docker-compose.prebuilt.yml, installs build from source"
  scripts/make-installer.sh
  exit 0
fi

{
  cat <<'EOF2'
# GENERATED by scripts/repin-prebuilt.sh; do not edit by hand.
#
# The ready-made images the installer downloads instead of building each
# service on the device. CI built each one (.github/workflows/nvpn-image.yml)
# from one folder of this repo (nvpn/, unbound/, hub/, docs/; nvpn/ at the
# NVPN_COMMIT in docker-compose.yml), for linux/amd64 and linux/arm64. Each tag
# names a hash of its folder, so scripts/check-pins.sh fails if a folder moves
# without a repin. The digest is what Docker checks every downloaded byte
# against.
#
# An image missing here (its folder changed since it was published) is built
# from source by the install, like everything is without this file.
#
# The installer loads this file on top of docker-compose.yml, by writing
# COMPOSE_FILE into .env, when this device can pull every image in it.
# Otherwise, and when SOVEREIGN_DNS_BUILD_FROM_SOURCE=1, it builds them from
# source as before. Delete this file to make every install build from source.
services:
EOF2
  for img in $(prebuilt_images); do
    [[ -s "$tmp/$img.ref" ]] || continue
    for s in $(prebuilt_services "$img"); do
      printf '  %s:\n    build: !reset null\n    image: %s\n' "$s" "$(cat "$tmp/$img.ref")"
    done
  done
} >docker-compose.prebuilt.yml
echo "wrote docker-compose.prebuilt.yml"
scripts/make-installer.sh
