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
	"net"
	"net/url"
	"regexp"
	"strings"
)

type settings struct {
	V       int      `json:"v"`
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

var (
	ipv4Literal = regexp.MustCompile(`(^|[^0-9.])(\d{1,3}\.){3}\d{1,3}([^0-9.]|$)`)
	// Two or more hex groups joined by colons with a :: or 7 colons: an IPv6
	// literal. Loose on purpose; a false hit only refuses a save.
	ipv6Literal = regexp.MustCompile(`(?i)([0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}`)
	lanSuffixes = []string{".local", ".lan", ".home", ".internal", ".home.arpa", ".localdomain", ".arpa"}
)

func hasIPLiteral(s string) bool {
	if ipv4Literal.MatchString(s) {
		return true
	}
	for _, m := range ipv6Literal.FindAllString(s, -1) {
		if strings.Contains(m, "::") || strings.Count(m, ":") == 7 {
			if net.ParseIP(strings.Trim(m, "[]")) != nil {
				return true
			}
		}
	}
	return false
}

func lanName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, s := range lanSuffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
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
		if hasIPLiteral(v) {
			bad("%s %q contains an IP address", what, v)
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
		case net.ParseIP(strings.Trim(u.Hostname(), "[]")) != nil:
			bad("list %q points at an IP address; only public names may be shared", l.Address)
		case lanName(u.Hostname()):
			bad("list %q points at a local name; only public names may be shared", l.Address)
		}
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
		if d.Kind == "exact" && hasIPLiteral(d.Domain) {
			bad("domain %q is an IP address", d.Domain)
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
