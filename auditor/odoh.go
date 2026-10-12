package main

// Oblivious DoH client (RFC 9230): witness 2. The query is HPKE-sealed to the
// target's public key and POSTed to a relay, so the relay sees the box's IP but
// not the name, and the target sees the name but only the relay's IP.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Relay and Target carry an Operator so the two roles of one query are never
// held by the same party: RFC 9230's privacy holds only while they do not
// collude (sections 3 and 11). The Fastly relay and odoh.crypto.sx are both
// run by Frank Denis, so that pairing must never be chosen.
type Relay struct {
	URL      string `json:"url"`
	Operator string `json:"operator"`
}

type Target struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	Operator string `json:"operator"`
}

// Measured working on 2026-10-11 (PLANS/SOVEREIGN_DNS_PHASE_5_DESIGN.md 2).
var defaultRelays = []Relay{
	{URL: "https://odoh-relay.edgecompute.app/", Operator: "crypto.sx"},
	{URL: "https://odoh-relay.numa.rs/relay", Operator: "numa"},
}

var defaultTargets = []Target{
	{Host: "odoh.cloudflare-dns.com", Path: "/dns-query", Operator: "cloudflare"},
	{Host: "odoh.crypto.sx", Path: "/dns-query", Operator: "crypto.sx"},
}

// pairs lists every (relay, target) combination with distinct operators.
func pairs(relays []Relay, targets []Target) [][2]int {
	var out [][2]int
	for i, r := range relays {
		for j, t := range targets {
			if !strings.EqualFold(r.Operator, t.Operator) {
				out = append(out, [2]int{i, j})
			}
		}
	}
	return out
}

type odohConfig struct {
	contents []byte // serialized ObliviousDoHConfigContents, input to key_id
	pk       hpke.PublicKey
	keyID    []byte
	fetched  time.Time
}

type ODoH struct {
	Relays  []Relay
	Targets []Target
	HTTP    *http.Client

	mu      sync.Mutex
	configs map[string]*odohConfig
}

// newHTTPClient resolves relay and target hostnames through the box's own
// Unbound (witness 1), so witness 2 does not depend on the host's resolver.
func newHTTPClient(w1 string, timeout time.Duration) *http.Client {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", w1)
	}}
	d := &net.Dialer{Resolver: r, Timeout: timeout}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: d.DialContext, ForceAttemptHTTP2: true}}
}

func u16(b []byte) int { return int(binary.BigEndian.Uint16(b)) }

func lp(b []byte) []byte {
	o := make([]byte, 2, 2+len(b))
	binary.BigEndian.PutUint16(o, uint16(len(b)))
	return append(o, b...)
}

// parseConfigs picks the first config this client supports:
// X25519 / HKDF-SHA256 / AES-128-GCM, the only suite the measured targets use.
func parseConfigs(b []byte) (*odohConfig, error) {
	if len(b) < 2 || u16(b)+2 > len(b) {
		return nil, errors.New("odohconfigs: bad length")
	}
	b = b[2 : 2+u16(b)]
	for len(b) >= 4 {
		ver, l := u16(b), u16(b[2:])
		if 4+l > len(b) {
			break
		}
		c := b[4 : 4+l]
		b = b[4+l:]
		if ver != 1 || len(c) < 8 || u16(c) != 0x20 || u16(c[2:]) != 1 || u16(c[4:]) != 1 {
			continue
		}
		pkl := u16(c[6:])
		if 8+pkl > len(c) {
			continue
		}
		ecpk, err := ecdh.X25519().NewPublicKey(c[8 : 8+pkl])
		if err != nil {
			continue
		}
		pk, err := hpke.NewDHKEMPublicKey(ecpk)
		if err != nil {
			continue
		}
		prk, err := hkdf.Extract(sha256.New, c, nil)
		if err != nil {
			return nil, err
		}
		kid, err := hkdf.Expand(sha256.New, prk, "odoh key id", 32)
		if err != nil {
			return nil, err
		}
		return &odohConfig{contents: c, pk: pk, keyID: kid, fetched: time.Now()}, nil
	}
	return nil, errors.New("odohconfigs: no supported suite")
}

func (o *ODoH) config(t Target, refresh bool) (*odohConfig, error) {
	o.mu.Lock()
	c := o.configs[t.Host]
	o.mu.Unlock()
	if c != nil && !refresh && time.Since(c.fetched) < 12*time.Hour {
		return c, nil
	}
	resp, err := o.HTTP.Get("https://" + t.Host + "/.well-known/odohconfigs")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("odohconfigs: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return nil, err
	}
	if c, err = parseConfigs(body); err != nil {
		return nil, err
	}
	o.mu.Lock()
	if o.configs == nil {
		o.configs = map[string]*odohConfig{}
	}
	o.configs[t.Host] = c
	o.mu.Unlock()
	return c, nil
}

// Route names the relay and target one query used, for the log.
type Route struct {
	Relay  string `json:"relay"`
	Target string `json:"target"`
}

// Query picks a random operator-distinct (relay, target) pair. If that fails
// it tries once more through a different relay, so one dead relay does not
// turn half of all checks inconclusive.
func (o *ODoH) Query(name string, qtype uint16) (Answer, Route, error) {
	ps := pairs(o.Relays, o.Targets)
	if len(ps) == 0 {
		return Answer{}, Route{}, errors.New("no relay/target pair with distinct operators")
	}
	rand.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
	var errs []error
	tried := -1
	for _, p := range ps {
		if tried >= 0 && p[0] == tried {
			continue
		}
		r, t := o.Relays[p[0]], o.Targets[p[1]]
		route := Route{Relay: r.URL, Target: t.Host}
		a, err := o.exchange(r, t, name, qtype, false)
		if errors.Is(err, errStaleConfig) {
			a, err = o.exchange(r, t, name, qtype, true)
		}
		if err == nil {
			return a, route, nil
		}
		errs = append(errs, err)
		if tried >= 0 {
			break
		}
		tried = p[0]
	}
	return Answer{}, Route{}, errors.Join(errs...)
}

var errStaleConfig = errors.New("target rejected key")

func (o *ODoH) exchange(r Relay, t Target, name string, qtype uint16, refresh bool) (Answer, error) {
	cfg, err := o.config(t, refresh)
	if err != nil {
		return Answer{}, err
	}
	q, err := buildQuery(name, qtype, 0)
	if err != nil {
		return Answer{}, err
	}
	msg, sender, qplain, err := sealQuery(cfg, q)
	if err != nil {
		return Answer{}, err
	}
	url := r.URL + "?targethost=" + t.Host + "&targetpath=" + t.Path
	req, _ := http.NewRequest("POST", url, bytes.NewReader(msg))
	req.Header.Set("content-type", "application/oblivious-dns-message")
	req.Header.Set("accept", "application/oblivious-dns-message")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return Answer{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return Answer{}, err
	}
	// RFC 9230 4.3: a target that cannot decrypt answers 401 so the client
	// re-fetches its config. Relays pass it through.
	if resp.StatusCode == 401 && !refresh {
		return Answer{}, errStaleConfig
	}
	if resp.StatusCode != 200 {
		return Answer{}, fmt.Errorf("relay %s: HTTP %d", r.URL, resp.StatusCode)
	}
	pt, err := openResponse(sender, qplain, body)
	if err != nil {
		return Answer{}, err
	}
	a, _, err := parseAnswer(pt, qtype)
	return a, err
}

// sealQuery builds an ObliviousDoHMessage of type query (RFC 9230 6.1-6.2).
func sealQuery(cfg *odohConfig, dnsMsg []byte) (msg []byte, s *hpke.Sender, qplain []byte, err error) {
	qplain = append(lp(dnsMsg), lp(nil)...) // no padding
	enc, s, err := hpke.NewSender(cfg.pk, hpke.HKDFSHA256(), hpke.AES128GCM(), []byte("odoh query"))
	if err != nil {
		return nil, nil, nil, err
	}
	aad := append([]byte{1}, lp(cfg.keyID)...)
	ct, err := s.Seal(aad, qplain)
	if err != nil {
		return nil, nil, nil, err
	}
	msg = append(append([]byte{1}, lp(cfg.keyID)...), lp(append(enc, ct...))...)
	return msg, s, qplain, nil
}

// openResponse decrypts an ObliviousDoHMessage of type response (RFC 9230 6.2)
// and returns the DNS message inside it.
func openResponse(s *hpke.Sender, qplain, body []byte) ([]byte, error) {
	if len(body) < 5 || body[0] != 2 {
		return nil, errors.New("odoh: not a response message")
	}
	nl := u16(body[1:])
	if 3+nl+2 > len(body) {
		return nil, errShort
	}
	nonce := body[3 : 3+nl]
	el := u16(body[3+nl:])
	if 3+nl+2+el > len(body) {
		return nil, errShort
	}
	ect := body[3+nl+2 : 3+nl+2+el]
	secret, err := s.Export("odoh response", 16)
	if err != nil {
		return nil, err
	}
	salt := append(append([]byte{}, qplain...), lp(nonce)...)
	prk, err := hkdf.Extract(sha256.New, secret, salt)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Expand(sha256.New, prk, "odoh key", 16)
	if err != nil {
		return nil, err
	}
	iv, err := hkdf.Expand(sha256.New, prk, "odoh nonce", 12)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	rplain, err := g.Open(nil, iv, ect, append([]byte{2}, lp(nonce)...))
	if err != nil {
		return nil, fmt.Errorf("odoh: response did not authenticate: %w", err)
	}
	if len(rplain) < 2 || 2+u16(rplain) > len(rplain) {
		return nil, errShort
	}
	return rplain[2 : 2+u16(rplain)], nil
}
