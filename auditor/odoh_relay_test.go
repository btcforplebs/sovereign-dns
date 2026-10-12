package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeTarget is an ODoH target answering every A query with 192.0.2.7.
func fakeTarget(t *testing.T) (config []byte, handle func([]byte) []byte) {
	sk, _ := ecdh.X25519().GenerateKey(rand.Reader)
	contents := append([]byte{0, 0x20, 0, 1, 0, 1}, lp(sk.PublicKey().Bytes())...)
	priv, _ := hpke.NewDHKEMPrivateKey(sk)
	cfg, _ := parseConfigs(lp(append([]byte{0, 1}, lp(contents)...)))
	return lp(append([]byte{0, 1}, lp(contents)...)), func(msg []byte) []byte {
		kl := u16(msg[1:])
		em := msg[3+kl+2:]
		rcp, err := hpke.NewRecipient(em[:32], priv, hpke.HKDFSHA256(), hpke.AES128GCM(), []byte("odoh query"))
		if err != nil {
			t.Fatal(err)
		}
		qplain, err := rcp.Open(append([]byte{1}, lp(cfg.keyID)...), em[32:])
		if err != nil {
			t.Fatal(err)
		}
		resp := []byte{0, 0, 0x81, 0x80, 0, 0, 0, 1, 0, 0, 0, 0, 0xC0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7}
		secret, _ := rcp.Export("odoh response", 16)
		nonce := make([]byte, 16)
		rand.Read(nonce)
		prk, _ := hkdf.Extract(sha256.New, secret, append(append([]byte{}, qplain...), lp(nonce)...))
		key, _ := hkdf.Expand(sha256.New, prk, "odoh key", 16)
		iv, _ := hkdf.Expand(sha256.New, prk, "odoh nonce", 12)
		blk, _ := aes.NewCipher(key)
		g, _ := cipher.NewGCM(blk)
		ct := g.Seal(nil, iv, append(lp(resp), lp(nil)...), append([]byte{2}, lp(nonce)...))
		return append(append([]byte{2}, lp(nonce)...), lp(ct)...)
	}
}

// One relay is dead: every query must still be answered, via the other one.
func TestODoHFallsBackToTheOtherRelay(t *testing.T) {
	config, handle := fakeTarget(t)
	var good, dead atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/odohconfigs":
			w.Write(config)
		case r.URL.Path == "/dead":
			dead.Add(1)
			http.Error(w, "down", http.StatusBadGateway)
		case r.URL.Path == "/relay" && r.URL.Query().Get("targetpath") == "/dns-query":
			good.Add(1)
			b, _ := io.ReadAll(r.Body)
			w.Write(handle(b))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	o := &ODoH{
		Relays:  []Relay{{URL: srv.URL + "/dead", Operator: "a"}, {URL: srv.URL + "/relay", Operator: "b"}},
		Targets: []Target{{Host: host, Path: "/dns-query", Operator: "c"}},
		HTTP:    srv.Client(),
	}
	for i := 0; i < 20; i++ {
		a, route, err := o.Query("example.com", typeA)
		if err != nil || len(a.Addrs) != 1 || a.Addrs[0].String() != "192.0.2.7" {
			t.Fatalf("query %d: %+v %v", i, a, err)
		}
		if !strings.HasSuffix(route.Relay, "/relay") {
			t.Fatalf("route names the dead relay: %s", route.Relay)
		}
	}
	if dead.Load() == 0 || good.Load() != 20 {
		t.Fatalf("expected the dead relay to be tried sometimes (got %d) and every answer from the good one (got %d)", dead.Load(), good.Load())
	}
	// Both dead: the error must surface, never an empty answer.
	o.Relays[1].URL = srv.URL + "/dead"
	if _, _, err := o.Query("example.com", typeA); err == nil {
		t.Fatal("two dead relays returned no error")
	}
}
