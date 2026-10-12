package main

// Origin-ASN lookups through Team Cymru's DNS interface, asked via the box's
// own Unbound. Only addresses in a disagreement are looked up, so Cymru learns
// a site's address only when the two witnesses already returned different
// ones. A local IP-to-ASN table would remove that leak (design note 6.3).

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cymru struct {
	R   Resolver
	TTL time.Duration

	mu    sync.Mutex
	cache map[netip.Addr]asnEntry
}

type asnEntry struct {
	asns []uint32
	at   time.Time
}

func (c *cymru) Lookup(a netip.Addr) ([]uint32, error) {
	// Never send private, loopback or documentation addresses anywhere:
	// nobody announces them, which is itself the answer.
	if !a.IsGlobalUnicast() || a.IsPrivate() || isDocumentation(a) {
		return nil, nil
	}
	c.mu.Lock()
	if e, ok := c.cache[a]; ok && time.Since(e.at) < c.TTL {
		c.mu.Unlock()
		return e.asns, nil
	}
	c.mu.Unlock()
	ans, err := c.R.Query(cymruName(a), typeTXT)
	if err != nil {
		return nil, err
	}
	var asns []uint32
	switch ans.Rcode {
	case rcodeNXDomain:
		// unannounced: known-empty
	case rcodeNoError:
		for _, t := range ans.TXT {
			// "15169 | 8.8.8.0/24 | US | arin | 2023-12-28"; several origins
			// are space-separated in the first field.
			for _, f := range strings.Fields(strings.SplitN(t, "|", 2)[0]) {
				if n, err := strconv.ParseUint(f, 10, 32); err == nil {
					asns = append(asns, uint32(n))
				}
			}
		}
	default:
		return nil, fmt.Errorf("cymru %s: %s", a, rcodeName(ans.Rcode))
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[netip.Addr]asnEntry{}
	}
	c.cache[a] = asnEntry{asns, time.Now()}
	c.mu.Unlock()
	return asns, nil
}

func cymruName(a netip.Addr) string {
	if a.Is4() {
		b := a.As4()
		return fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", b[3], b[2], b[1], b[0])
	}
	b := a.As16()
	var s strings.Builder
	for i := 15; i >= 0; i-- {
		fmt.Fprintf(&s, "%x.%x.", b[i]&0xF, b[i]>>4)
	}
	return s.String() + "origin6.asn.cymru.com"
}

var docPrefixes = []netip.Prefix{
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("100.64.0.0/10"),
}

func isDocumentation(a netip.Addr) bool {
	for _, p := range docPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
