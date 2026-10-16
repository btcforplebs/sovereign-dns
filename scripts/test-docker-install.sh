#!/usr/bin/env bash
# The installer's "Docker isn't installed" path on Linux, in a throwaway
# container of one distro (privileged, so the Docker it installs can start):
#
#   bash scripts/test-docker-install.sh ubuntu:24.04 scripts/install.sh
#
# As an ordinary user with sudo, as on a fresh desktop:
#   - answering n installs nothing and prints the download link;
#   - pressing Enter installs Docker from Docker's repository, adds the user
#     to the docker group, and carries on in this same run to the password
#     prompt, where the test stops (no answer).
# Needs Docker on the machine running the test, and the internet.
set -uo pipefail
img=${1:?usage: test-docker-install.sh <image> <install.sh>}
inst=$(cd "$(dirname "${2:?usage}")" && pwd)/$(basename "$2")
name="sdns-dockertest-$$"
trap 'docker rm -f "$name" >/dev/null 2>&1' EXIT
fail() { echo "FAIL  $img: $*"; exit 1; }

docker run -d --privileged --name "$name" -v "$inst:/opt/install.sh:ro" "$img" sleep infinity >/dev/null || fail "could not start $img"
# What a fresh desktop has and a container image lacks: sudo and a user.
docker exec "$name" sh -c 'apt-get update -q >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -q sudo passwd >/dev/null &&
  useradd -m -s /bin/bash tess && echo "tess ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/tess' || fail "could not prepare a user"
as_user() { docker exec -i -u tess -w /home/tess "$name" "$@"; }
as_user sh -c 'command -v docker' >/dev/null && fail "the image already has docker"

out=$(printf 'n\n' | as_user bash /opt/install.sh 2>&1); rc=$?
echo "--- answered n (exit $rc)"; echo "$out" | tail -4
[[ $rc -ne 0 ]] || fail "answering n did not stop the installer"
grep -q 'Install it now? \[Y/n\]' <<<"$out" || fail "no question was asked"
grep -q 'https://docs.docker.com/get-docker/' <<<"$out" || fail "answering n gave no download link"
as_user sh -c 'command -v docker' >/dev/null && fail "answering n installed docker anyway"

out=$(printf '\n' | as_user bash /opt/install.sh 2>&1); rc=$?
echo "--- pressed Enter (exit $rc)"; echo "$out" | grep -vE '^(Get|Hit|Ign|Reading|Building|Selecting|Preparing|Unpacking|Setting up|Processing|Fetched|\(Reading)' | tail -12
grep -q 'Docker is installed\.' <<<"$out" || fail "Docker was not installed"
as_user sh -c 'docker --version && sudo docker compose version' || fail "docker or its compose plugin is missing"
as_user sh -c 'id -nG tess' | tr ' ' '\n' | grep -qx docker || fail "the user was not added to the docker group"
grep -q 'not running' <<<"$out" && fail "the installer stopped at 'not running' instead of carrying on"
grep -q 'Pick a password' <<<"$out" || fail "the installer did not carry on to the password prompt"
echo "ok    $img: asks first; n installs nothing; Enter installs Docker and carries on"
