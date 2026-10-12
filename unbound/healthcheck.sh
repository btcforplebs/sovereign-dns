#!/bin/sh
# Fails unless Unbound can resolve a name nobody has ever asked for.
#
# A fixed name (the old check used cloudflare.com) is answered from cache, so
# it stays green while recursion is broken. A random label under .com forces a
# real round trip to the .com servers on every run, and .com is NSEC3-signed,
# so aggressive NSEC caching cannot synthesize the answer locally. The answer
# must also validate: NXDOMAIN or NOERROR passes, SERVFAIL or a timeout fails.
label=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')
out=$(drill @127.0.0.1 "hc-$label.com" A 2>&1) || { echo "$out"; exit 1; }
case "$out" in
  *"rcode: NXDOMAIN"*|*"rcode: NOERROR"*) exit 0 ;;
  *) echo "hc-$label.com: $(echo "$out" | grep -m1 rcode)"; exit 1 ;;
esac
