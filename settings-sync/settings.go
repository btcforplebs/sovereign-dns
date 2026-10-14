package main

// What the shared list holds, and what it must never hold.
//
// Only Pi-hole's filtering settings travel: groups, subscribed lists and
// allow/deny domains. Clients, local DNS records, DHCP and each box's own
// wiring (interface, upstream, NTP, password) stay per box: clients and local
// records are LAN addresses, and the wiring is set by the compose file.

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type settings struct {
	V       int      `json:"v"`
	At      int64    `json:"at"` // the event's created_at, sealed so it can't be changed
	Boxes   []box    `json:"boxes"`
	Groups  []group  `json:"groups"`
	Lists   []list   `json:"lists"`
	Domains []domain `json:"domains"`
}

// box is a DNS box that may read the list. Logen's own key always may.
type box struct {
	PubKey string `json:"pubkey"` // hex
	Name   string `json:"name"`
}

type group struct {
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Enabled bool   `json:"enabled"`
}

type list struct {
	Address string   `json:"address"`
	Type    string   `json:"type"` // block | allow
	Comment string   `json:"comment"`
	Enabled bool     `json:"enabled"`
	Groups  []string `json:"groups"` // group names; "Default" is Pi-hole's group 0
}

type domain struct {
	Domain  string   `json:"domain"`
	Type    string   `json:"type"` // allow | deny
	Kind    string   `json:"kind"` // exact | regex
	Comment string   `json:"comment"`
	Enabled bool     `json:"enabled"`
	Groups  []string `json:"groups"`
}

const defaultGroup = "Default"

func (l list) key() string   { return l.Type + " " + l.Address }
func (d domain) key() string { return d.Type + "/" + d.Kind + " " + d.Domain }

// The address guard. page.html carries a line-for-line copy (between the
// "address guard" markers) so the page refuses the same lists before the
// signer is asked; TestPageGuardMatchesGo runs both on the same cases.
//
// Every string is scanned after lowercasing and dropping backslashes, so an
// escaped regex (`192\.168\.`) reads like the address it matches.
var (
	// Four 1-3 digit groups joined by dots or dashes (the dashed form is how
	// nip.io/sslip.io hosts spell an IP). A trailing dot still counts, and so
	// do longer runs like a.b.c.d.e: loose on purpose, a false hit only
	// refuses a save.
	ipv4Literal = regexp.MustCompile(`(^|[^0-9])([0-9]{1,3}[.-]){3}[0-9]{1,3}([^0-9]|$)`)
	// Up to eight hex groups joined by colons; counted as IPv6 when it has a
	// :: or seven colons, and at least one hex digit.
	ipv6Literal = regexp.MustCompile(`([0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}`)
	hexDigit    = regexp.MustCompile(`[0-9a-f]`)
	// Tokens: what is left between these. Splits URLs, regexes and prose alike.
	tokenSplit = regexp.MustCompile(`[\s,;"'()|^\[\]{}<>=&?/:#@!*+$]+`)
	// A name ending in one of these never resolves on the public internet,
	// or resolves straight back to the address written in it.
	lanSuffixes = []string{".local", ".lan", ".home", ".internal", ".localdomain", ".arpa",
		".nip.io", ".sslip.io", ".xip.io", ".traefik.me", ".localtest.me", ".lvh.me"}
)

func scanForm(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), `\`, "")
}

// addressIn says why s carries a home or LAN address, or "" if it doesn't.
func addressIn(s string) string {
	t := scanForm(s)
	if ipv4Literal.MatchString(t) {
		return "an IPv4 address"
	}
	for _, m := range ipv6Literal.FindAllString(t, -1) {
		if (strings.Contains(m, "::") || strings.Count(m, ":") == 7) && hexDigit.MatchString(m) {
			return "an IPv6 address"
		}
	}
	if strings.Contains(t, "in-addr.arpa") || strings.Contains(t, "ip6.arpa") {
		return "a reverse-lookup name"
	}
	for _, tok := range tokenSplit.Split(t, -1) {
		tok = strings.TrimRight(tok, ".")
		if tok == "localhost" {
			return "localhost"
		}
		for _, suf := range lanSuffixes {
			if strings.HasSuffix(tok, suf) {
				return "a local name (" + suf + ")"
			}
		}
	}
	return ""
}

// singleLabel: a host or exact domain with no dot is a LAN machine name.
func singleLabel(name string) bool {
	return !strings.Contains(strings.Trim(scanForm(name), "."), ".")
}

// validate refuses anything that could carry a home or LAN address, and
// anything Pi-hole would not accept, so a bad list never half-applies.
func (s *settings) validate() error {
	if s.V != 1 {
		return fmt.Errorf("unknown list version %d", s.V)
	}
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	text := func(what, v string) {
		if why := addressIn(v); why != "" {
			bad("%s %q contains %s", what, v, why)
		}
	}
	groups := map[string]bool{defaultGroup: true}
	for _, b := range s.Boxes {
		if _, err := parsePubkey(b.PubKey); err != nil || len(b.PubKey) != 64 {
			bad("box %q: pubkey must be 64 hex characters", b.Name)
		}
		text("box name", b.Name)
	}
	for _, g := range s.Groups {
		if strings.TrimSpace(g.Name) == "" {
			bad("a group has no name")
		}
		if groups[g.Name] {
			bad("group %q is listed twice", g.Name)
		}
		groups[g.Name] = true
		text("group", g.Name)
		text("group comment", g.Comment)
	}
	refs := func(what string, gs []string) {
		for _, g := range gs {
			if !groups[g] {
				bad("%s names group %q, which the list does not define", what, g)
			}
		}
	}
	seen := map[string]bool{}
	for _, l := range s.Lists {
		if l.Type != "block" && l.Type != "allow" {
			bad("list %q: type must be block or allow", l.Address)
		}
		u, err := url.Parse(l.Address)
		switch {
		case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
			bad("list %q is not an http(s) URL", l.Address)
		case singleLabel(u.Hostname()):
			bad("list %q points at a local name; only public names may be shared", l.Address)
		}
		text("list", l.Address) // host, path and query
		if u != nil && u.User != nil {
			bad("list %q carries a username or password", l.Address)
		}
		if seen[l.key()] {
			bad("list %q is listed twice", l.Address)
		}
		seen[l.key()] = true
		text("list comment", l.Comment)
		refs("list "+l.Address, l.Groups)
	}
	for _, d := range s.Domains {
		if (d.Type != "allow" && d.Type != "deny") || (d.Kind != "exact" && d.Kind != "regex") {
			bad("domain %q: type must be allow/deny and kind exact/regex", d.Domain)
		}
		if strings.TrimSpace(d.Domain) == "" {
			bad("a domain entry is empty")
		}
		text("domain", d.Domain) // exact and regex alike
		if d.Kind == "exact" && singleLabel(d.Domain) {
			bad("domain %q is a single-label (local) name", d.Domain)
		}
		if seen[d.key()] {
			bad("domain %q is listed twice", d.Domain)
		}
		seen[d.key()] = true
		text("domain comment", d.Comment)
		refs("domain "+d.Domain, d.Groups)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
