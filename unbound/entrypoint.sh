#!/bin/sh
set -e
# Refresh the DNSSEC root trust anchor (RFC 5011). unbound-anchor exits 1 when
# it updated the file, which is success here; it falls back to its built-in
# anchor if the network is not up yet.
unbound-anchor -a /var/lib/unbound/root.key || true
chown unbound:unbound /var/lib/unbound/root.key
unbound-checkconf /etc/unbound/unbound.conf
exec unbound -d -c /etc/unbound/unbound.conf
