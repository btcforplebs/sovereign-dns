# Native Unbound for every architecture the stack runs on.
#
# This replaces mvance/unbound, which is amd64-only (last release 2024-10). On
# Apple Silicon it ran under Docker Desktop's amd64 emulation, and when that
# emulation went away the already-running resolver kept serving intermittent
# SERVFAILs while its healthcheck could not even exec /bin/sh -- so nothing
# reported it. Alpine ships arm64 and amd64 builds of unbound, and drill for
# the healthcheck.
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8

RUN apk add --no-cache unbound=1.25.2-r1 drill=1.8.4-r0 \
 && mkdir -p /var/lib/unbound \
 && chown unbound:unbound /var/lib/unbound

COPY unbound.conf /etc/unbound/unbound.conf
COPY entrypoint.sh healthcheck.sh /usr/local/bin/

EXPOSE 53/udp 53/tcp
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
