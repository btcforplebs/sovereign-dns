package main

// Deciding whether two witnesses agree. Design note section 6.3: different
// IPs are normal (CDNs, geo-DNS), so agreement is judged by owner network
// (origin ASN), and only a different owner, a different rcode, or data on one
// side only counts as a disagreement.

import (
	"fmt"
	"net/netip"
	"slices"
)

const (
	Agree        = "agree"
	Disagree     = "disagree"
	Inconclusive = "inconclusive"
)

type Verdict struct {
	Result string `json:"result"`
	Reason string `json:"reason"`
}

// ASNLookup returns the origin ASNs announcing addr. An empty slice with a
// nil error means "known: nobody announces it" (unrouted, private, TEST-NET);
// an error means the lookup itself failed and nothing can be concluded.
type ASNLookup func(netip.Addr) ([]uint32, error)

func compare(w1, w2 Answer, asn ASNLookup) Verdict {
	if w1.Rcode == rcodeServFail || w2.Rcode == rcodeServFail {
		return Verdict{Inconclusive, "a witness returned SERVFAIL"}
	}
	if w1.Rcode != w2.Rcode {
		return Verdict{Disagree, fmt.Sprintf("rcode differs: box %s, public %s", rcodeName(w1.Rcode), rcodeName(w2.Rcode))}
	}
	if w1.Rcode != rcodeNoError {
		return Verdict{Agree, "same rcode " + rcodeName(w1.Rcode)}
	}
	switch {
	case len(w1.Addrs) == 0 && len(w2.Addrs) == 0:
		return Verdict{Agree, "both have no addresses"}
	case len(w1.Addrs) == 0:
		return Verdict{Disagree, "box has no addresses, public resolver does"}
	case len(w2.Addrs) == 0:
		return Verdict{Disagree, "box has addresses, public resolver has none"}
	}
	for _, a := range w1.Addrs {
		if slices.Contains(w2.Addrs, a) {
			return Verdict{Agree, "shared address " + a.String()}
		}
	}
	s1, err := asnSet(w1.Addrs, asn)
	if err != nil {
		return Verdict{Inconclusive, "owner lookup failed: " + err.Error()}
	}
	s2, err := asnSet(w2.Addrs, asn)
	if err != nil {
		return Verdict{Inconclusive, "owner lookup failed: " + err.Error()}
	}
	for _, n := range s1 {
		if slices.Contains(s2, n) {
			return Verdict{Agree, fmt.Sprintf("different addresses, same owner network AS%d", n)}
		}
	}
	return Verdict{Disagree, fmt.Sprintf("different owner networks: box AS%v, public AS%v", s1, s2)}
}

func asnSet(addrs []netip.Addr, asn ASNLookup) ([]uint32, error) {
	var out []uint32
	for _, a := range addrs {
		ns, err := asn(a)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func rcodeName(r int) string {
	switch r {
	case rcodeNoError:
		return "NOERROR"
	case rcodeServFail:
		return "SERVFAIL"
	case rcodeNXDomain:
		return "NXDOMAIN"
	case 5:
		return "REFUSED"
	}
	return fmt.Sprintf("RCODE%d", r)
}
