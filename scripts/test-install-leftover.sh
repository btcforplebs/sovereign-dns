#!/usr/bin/env bash
# A leftover from an earlier install must not block the copy-paste install.
#
#   bash scripts/test-install-leftover.sh
#
# Two leftovers, each run the old way first (it must fail, so the test can
# tell) and then the current way:
#   - an install.sh this user cannot overwrite (what an earlier `sudo curl`
#     leaves): the install line from README.md, with its URL pointed at a local
#     copy of scripts/install.sh and the final `bash install.sh` left off;
#   - files in ./sovereign-dns this user cannot overwrite (what an earlier
#     `sudo bash install.sh` leaves): scripts/install.sh itself, with a stand-in
#     docker and SOVEREIGN_DNS_EXTRACT_ONLY=1, so nothing is started.
# A read-only file stands in for a root-owned one: neither can be opened for
# writing, and both can be deleted from a folder this user owns. Needs a
# non-root user, because root can write anything.
#
# Starts nothing and touches nothing outside a temp dir. Safe next to a live box.

set -u
cd "$(dirname "$0")/.." || exit 1
repo=$(pwd)

pass=0 fail=0
ok() { pass=$((pass + 1)); printf 'ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL  %s\n' "$1"; }
check() { local name=$1; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }

if [[ "$(id -u)" == "0" ]]; then
  echo "test-install-leftover: run as a normal user; root can overwrite anything" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
url=https://raw.githubusercontent.com/btcforplebs/sovereign-dns/main/scripts/install.sh
mkdir -p "$tmp/src"
cp scripts/install.sh "$tmp/src/install.sh"

# --- 1. The install line ----------------------------------------------------
line=$(grep -m1 -E "^[^#]*curl [^ ]+ $url && bash install\.sh$" README.md)
[[ -n "$line" ]] || { bad "README.md has the install line"; line=; }
download=${line% && bash install.sh}
download=${download//$url/file://$tmp/src/install.sh}
old_download="curl -fsSLO file://$tmp/src/install.sh"

leftover() {
  rm -rf "${tmp:?}/home"; mkdir -p "$tmp/home"
  printf 'old installer\n' >"$tmp/home/install.sh"
  chmod a-w "$tmp/home/install.sh"
}
run_line() { (cd "$tmp/home" && bash -c "$1") >/dev/null 2>&1; }
fresh() { cmp -s "$tmp/home/install.sh" "$tmp/src/install.sh"; }
not() { ! "$@"; }

leftover
check "old line: a leftover install.sh blocks the download" not run_line "$old_download"
check "old line: the leftover is still there" not fresh
leftover
check "README line: downloads past a leftover install.sh" run_line "$download"
check "README line: the fresh installer is what ran next" fresh
rm -rf "${tmp:?}/home"; mkdir -p "$tmp/home"
check "README line: works with no leftover" run_line "$download"

# The quickstart shows the same line with the release host as a placeholder.
quick=$(grep -m1 -E '^[^#]*curl [^ ]+ https://INSTALLER-URL/install\.sh && bash install\.sh$' docs/src/quickstart.md)
check "quickstart line matches README" test "${quick//https:\/\/INSTALLER-URL\/install.sh/$url}" == "$line"

# --- 2. The installer over a tree it cannot overwrite --------------------------
mkdir -p "$tmp/bin"
cat >"$tmp/bin/docker" <<'EOF'
#!/bin/sh
# Stand-in: says yes to the installer's checks, runs nothing. `docker ps`
# lists a box container, as on an update, so the port check stands aside.
[ "$1" = ps ] && echo sovereign-dns-hub
exit 0
EOF
chmod +x "$tmp/bin/docker"

box() {
  rm -rf "${tmp:?}/box"; mkdir -p "$tmp/box/sovereign-dns"
  printf 'PIHOLE_PASSWORD=x\n' >"$tmp/box/sovereign-dns/.env"
  printf 'old\n' >"$tmp/box/sovereign-dns/README.md"
  chmod a-w "$tmp/box/sovereign-dns/README.md"
}
install_into_box() {
  (cd "$tmp/box" && PATH="$tmp/bin:$PATH" SOVEREIGN_DNS_EXTRACT_ONLY=1 bash "$1" </dev/null) >"$tmp/out" 2>&1
}
says_how_to_fix() { grep -q 'belong to another user' "$tmp/out" && grep -q 'sovereign-dns/README.md' "$tmp/out" && grep -q "sudo chown -R \"$(id -un)\" sovereign-dns" "$tmp/out"; }
untouched() { [[ ! -e "$tmp/box/sovereign-dns/docker-compose.yml" ]]; }
updated() { cmp -s "$tmp/box/sovereign-dns/docker-compose.yml" docker-compose.yml; }

if git cat-file -e 0f958fdaf9093c7d11475d635e253995f6cf880a 2>/dev/null; then
  git show 0f958fdaf9093c7d11475d635e253995f6cf880a:scripts/install.sh >"$tmp/old-install.sh"
  box
  install_into_box "$tmp/old-install.sh"
  check "old installer: does not say what to do about the files" not says_how_to_fix
else
  bad "old installer (0f958fda) not in this clone: git fetch origin"
fi
box
check "installer: refuses a tree it cannot overwrite" not install_into_box "$repo/scripts/install.sh"
check "installer: says which files and how to fix them" says_how_to_fix
check "installer: refuses before unpacking anything" untouched
chmod u+w "$tmp/box/sovereign-dns/README.md"
check "installer: after the fix, updates the tree" install_into_box "$repo/scripts/install.sh"
check "installer: the tree now matches this payload" updated

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" == 0 ]]
