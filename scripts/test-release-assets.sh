#!/usr/bin/env bash
# The release step must refuse an installer that is not what the tag's tree
# builds, and must publish the hash a user checks it against.
#
#   bash scripts/test-release-assets.sh      (run from a committed tree)
#
# Works on a throwaway clone; starts nothing, touches nothing else.
set -u
cd "$(dirname "$0")/.." || exit 1
repo=$(pwd)

pass=0 fail=0
ok() { pass=$((pass + 1)); printf 'ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL  %s\n' "$1"; }
check() { local name=$1; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }
refuses() { ! "$@" >/dev/null 2>&1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git clone -q --local "$repo" "$work/clone" || exit 1
cd "$work/clone" || exit 1
[[ -f scripts/release-assets.sh ]] || { echo "FAIL  scripts/release-assets.sh is missing"; exit 1; }
export SOVEREIGN_DNS_RELEASE_DRY=1
# A stand-in origin whose main is the commit under test.
git init -q --bare "$work/origin.git" && git -C "$work/origin.git" config receive.shallowUpdate true \
  && git remote set-url origin "$work/origin.git" \
  && git push -q origin HEAD:refs/heads/main || { echo 'FAIL  could not set up a stand-in origin'; exit 1; }

# A tag name that is not vX.Y.Z is refused (a branch name, a typo, a prerelease).
for t in main v1 1.2.3 v1.2.3-rc1 v1.2.3.4 "v1.2.3 "; do
  check "refuses tag '$t'" refuses scripts/release-assets.sh "$t" "$work/out-bad"
done

# The good case: both files out, the pair checks, the notes carry the hash.
good_out="$work/out"
check "builds assets for v9.9.9" scripts/release-assets.sh v9.9.9 "$good_out"
check "install.sh is the tree's installer" cmp -s scripts/install.sh "$good_out/install.sh"
check "the pair verifies with shasum" bash -c "cd '$good_out' && shasum -a 256 -c install.sh.sha256"
hash=$(cut -d' ' -f1 "$good_out/install.sh.sha256")
check "release notes name the hash" grep -q "$hash" "$good_out/notes.md"
check "release notes verify line pins the tag" grep -q "releases/download/v9.9.9/install.sh" "$good_out/notes.md"

# The README's verify line is the notes' line with the tag as a placeholder.
readme_line=$(grep -m1 'releases/download/vX.Y.Z/install.sh' "$repo/README.md" || true)
notes_line=$(grep -m1 'releases/download/v9.9.9/install.sh' "$good_out/notes.md" || true)
check "README has a verify line" test -n "$readme_line"
check "README line is the notes line (tag aside)" test "${notes_line//v9.9.9/vX.Y.Z}" = "$readme_line"

# Not the tag's tree: a tampered installer, a stale hash file, a tag that is
# not at HEAD (when not a dry run).
cp scripts/install.sh "$work/install.orig"
printf '\n# tampered\n' >> scripts/install.sh
check "refuses a tampered install.sh" refuses scripts/release-assets.sh v9.9.9 "$work/out-t"
cp "$work/install.orig" scripts/install.sh
printf '%064d  install.sh\n' 0 > scripts/install.sh.sha256
check "refuses a stale hash file" refuses scripts/release-assets.sh v9.9.9 "$work/out-s"
git checkout -q scripts/install.sh.sha256
# An installer and hash that agree with each other but are older than the tree.
printf '\nstale\n' >> README.md
check "refuses an installer older than the tree" refuses scripts/release-assets.sh v9.9.9 "$work/out-o"
git checkout -q README.md
unset SOVEREIGN_DNS_RELEASE_DRY
check "refuses when the tag does not exist" refuses scripts/release-assets.sh v9.9.9 "$work/out-n"
git -c tag.gpgSign=false tag v9.9.8 HEAD || { echo 'FAIL  could not create the test tag'; exit 1; }
git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -q --allow-empty -m "after the tag" --no-verify || { echo 'FAIL  could not make a later commit'; exit 1; }
check "refuses when the tag is not HEAD" refuses scripts/release-assets.sh v9.9.8 "$work/out-h"
git -c tag.gpgSign=false tag v9.9.9 HEAD || { echo 'FAIL  could not create the test tag'; exit 1; }
check "refuses a tag that is HEAD but not on main" refuses scripts/release-assets.sh v9.9.9 "$work/out-m"
git push -q origin HEAD:refs/heads/main || { echo 'FAIL  could not move the stand-in main'; exit 1; }
check "accepts when the tag is HEAD" scripts/release-assets.sh v9.9.9 "$work/out-ok"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
