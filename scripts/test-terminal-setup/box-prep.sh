#!/bin/sh
# Inside a dind box: tools install.sh needs, a tun device, a home folder.
set -e
apk add --no-cache bash curl coreutils grep sed gawk iproute2 >/dev/null
[ -c /dev/net/tun ] || { mkdir -p /dev/net; mknod /dev/net/tun c 10 200; chmod 600 /dev/net/tun; }
until docker info >/dev/null 2>&1; do sleep 1; done
echo prepped
