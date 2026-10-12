package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ips(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

var fakeASN = map[string][]uint32{
	"1.1.1.1": {13335}, "1.0.0.1": {13335}, "8.8.8.8": {15169}, "8.8.4.4": {15169},
}

func lookupFake(a netip.Addr) ([]uint32, error) { return fakeASN[a.String()], nil }

func TestCompare(t *testing.T) {
	cases := []struct {
		name   string
		w1, w2 Answer
		want   string
	}{
		{"same ip", Answer{Addrs: ips("1.1.1.1")}, Answer{Addrs: ips("1.1.1.1")}, Agree},
		{"cdn: different ip, same owner", Answer{Addrs: ips("1.1.1.1")}, Answer{Addrs: ips("1.0.0.1")}, Agree},
		{"different owner", Answer{Addrs: ips("1.1.1.1")}, Answer{Addrs: ips("8.8.8.8")}, Disagree},
		{"plant in TEST-NET (no owner)", Answer{Addrs: ips("192.0.2.1")}, Answer{Addrs: ips("1.1.1.1")}, Disagree},
		{"both unowned but different", Answer{Addrs: ips("192.0.2.1")}, Answer{Addrs: ips("192.0.2.2")}, Disagree},
		{"injected nxdomain", Answer{Rcode: rcodeNXDomain}, Answer{Addrs: ips("1.1.1.1")}, Disagree},
		{"both nxdomain", Answer{Rcode: rcodeNXDomain}, Answer{Rcode: rcodeNXDomain}, Agree},
		{"servfail is not a lie", Answer{Rcode: rcodeServFail}, Answer{Addrs: ips("1.1.1.1")}, Inconclusive},
		{"nodata both", Answer{}, Answer{}, Agree},
		{"data on one side only", Answer{}, Answer{Addrs: ips("1.1.1.1")}, Disagree},
	}
	for _, c := range cases {
		if got := compare(c.w1, c.w2, lookupFake); got.Result != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, got.Result, got.Reason, c.want)
		}
	}
	// A failed owner lookup must never read as agreement.
	failing := func(netip.Addr) ([]uint32, error) { return nil, os.ErrDeadlineExceeded }
	if got := compare(Answer{Addrs: ips("1.1.1.1")}, Answer{Addrs: ips("8.8.8.8")}, failing); got.Result != Inconclusive {
		t.Errorf("lookup failure: got %s", got.Result)
	}
}

func TestPairsNeverShareOperator(t *testing.T) {
	ps := pairs(defaultRelays, defaultTargets)
	if len(ps) < 2 {
		t.Fatalf("want at least 2 usable pairs, got %d", len(ps))
	}
	for _, p := range ps {
		if strings.EqualFold(defaultRelays[p[0]].Operator, defaultTargets[p[1]].Operator) {
			t.Errorf("pair %v puts one operator in both roles", p)
		}
	}
	// Positive control: the excluded pairing exists in the inputs.
	if len(ps) == len(defaultRelays)*len(defaultTargets) {
		t.Errorf("expected the same-operator pair to be excluded")
	}
}

// fakes for the auditor loop
type fakeW1 map[string]Answer

func (f fakeW1) Query(n string, qt uint16) (Answer, error) {
	if qt == typeAAAA {
		return Answer{}, nil
	}
	return f[n], nil
}

type fakeW2 map[string]Answer

func (f fakeW2) Query(n string, qt uint16) (Answer, Route, error) {
	if qt == typeAAAA {
		return Answer{}, Route{"r", "t"}, nil
	}
	return f[n], Route{"r", "t"}, nil
}

func newTestAuditor(t *testing.T, w1 fakeW1, w2 fakeW2, rnd func() float64) (*Auditor, string) {
	dir := t.TempDir()
	seen := &SeenSet{salt: []byte("s")}
	a := &Auditor{W1: w1, W2: w2, ASN: lookupFake, Decoys: []string{"d1.test", "d2.test", "d3.test", "d4.test"},
		Rate: 0.05, NDecoy: 2, MaxQ: 3, Seen: seen, rnd: rnd}
	a.Out = &Recorder{Dir: dir, Hash: seen.hash, MaxFlag: 100, Queue: a.QueueLen}
	return a, dir
}

func TestSampling(t *testing.T) {
	ok := Answer{Addrs: ips("1.1.1.1")}
	w1 := fakeW1{"a.test": ok, "signed.test": {AD: true, Addrs: ips("1.1.1.1")}}
	r := 0.5
	a, _ := newTestAuditor(t, w1, nil, func() float64 { return r })
	a.Intake("a.test")
	if a.QueueLen() != 1 {
		t.Fatal("first-seen name must always be queued")
	}
	a.Intake("A.TEST.") // same name, normalised; rnd 0.5 >= 5%
	if a.QueueLen() != 1 {
		t.Fatal("seen name above the rate must not be queued")
	}
	r = 0.01
	a.Intake("a.test")
	if a.QueueLen() != 2 {
		t.Fatal("seen name below the rate must be queued")
	}
	a.Intake("signed.test")
	if a.QueueLen() != 2 {
		t.Fatal("DNSSEC-secure name must be skipped")
	}
	for _, n := range []string{"printer.local", "router.lan", "1.0.168.192.in-addr.arpa", "box.fips", "localhost"} {
		a.Intake(n)
	}
	if a.QueueLen() != 2 {
		t.Fatal("house-only names must never be queued (they would leak)")
	}
	a.Intake("b.test")
	a.Intake("c.test")
	if a.QueueLen() != 3 || a.Out.st.Counters["queue_dropped"] != 1 {
		t.Fatalf("queue cap: len %d dropped %d", a.QueueLen(), a.Out.st.Counters["queue_dropped"])
	}
}

func TestCycleFlagsAndDecoys(t *testing.T) {
	good := Answer{Addrs: ips("1.1.1.1")}
	w1 := fakeW1{"victim.test": {Addrs: ips("8.8.8.8")}, "d1.test": good, "d2.test": good, "d3.test": good, "d4.test": good}
	w2 := fakeW2{"victim.test": good, "d1.test": good, "d2.test": good, "d3.test": good, "d4.test": good}
	a, dir := newTestAuditor(t, w1, w2, nil)
	a.Intake("victim.test")
	if !a.Cycle() {
		t.Fatal("cycle found no work")
	}
	b, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st Status
	json.Unmarshal(b, &st)
	if len(st.Flags) != 1 || st.Flags[0].Name != "victim.test" || st.Flags[0].Decoy || st.LastFlag == nil {
		t.Fatalf("want exactly one flag for victim.test, got %+v", st.Flags)
	}
	// 1 real + 2 decoys, A and AAAA each = 6 checks.
	f, _ := os.Open(filepath.Join(dir, "checks.jsonl"))
	defer f.Close()
	sc := bufio.NewScanner(f)
	n, decoys := 0, map[string]bool{}
	for sc.Scan() {
		n++
		var c CheckRecord
		json.Unmarshal(sc.Bytes(), &c)
		if c.Verdict.Result == Agree && (c.Name != "" || c.NameHash == "") {
			t.Errorf("agreement must be logged by hash only: %s", sc.Text())
		}
		if c.Decoy {
			decoys[c.NameHash] = true
		}
	}
	if n != 6 || len(decoys) != 2 {
		t.Fatalf("want 6 checks with 2 distinct decoys, got %d checks, %d decoys", n, len(decoys))
	}
	// A clean name stays quiet.
	w1["victim.test"] = good
	a.Intake("victim.test")
	a.Rate = 1
	a.Intake("victim.test")
	a.Cycle()
	b, _ = os.ReadFile(filepath.Join(dir, "status.json"))
	json.Unmarshal(b, &st)
	if len(st.Flags) != 1 {
		t.Fatalf("a clean re-check must not add a flag, have %d", len(st.Flags))
	}
}

func TestNameFromLine(t *testing.T) {
	cases := map[string]string{
		"Oct 11 23:00:01 dnsmasq[52]: query[A] www.example.com from 10.44.0.2": "www.example.com",
		"Oct 11 23:00:01 dnsmasq[52]: query[AAAA] v6.example.com from fd00::1": "v6.example.com",
		"Oct 11 23:00:01 dnsmasq[52]: query[PTR] 1.0.0.10.in-addr.arpa from x": "",
		"Oct 11 23:00:01 dnsmasq[52]: reply www.example.com is 1.2.3.4":        "",
		"bare.example.org": "bare.example.org",
		"# comment":        "",
	}
	for in, want := range cases {
		got, ok := nameFromLine(in)
		if (want == "") == ok || got != want && want != "" {
			t.Errorf("%q: got %q %v, want %q", in, got, ok, want)
		}
	}
}

// TestODoHRoundTrip plays the target side with crypto/hpke, so the client's
// sealing and the response key schedule (RFC 9230 6.2) are checked offline,
// and a tampered response must fail to open.
func TestODoHRoundTrip(t *testing.T) {
	sk, _ := ecdh.X25519().GenerateKey(rand.Reader)
	contents := append([]byte{0, 0x20, 0, 1, 0, 1}, lp(sk.PublicKey().Bytes())...)
	wire := lp(append([]byte{0, 1}, lp(contents)...))
	cfg, err := parseConfigs(wire)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := buildQuery("example.com", typeA, 0)
	msg, sender, qplain, err := sealQuery(cfg, q)
	if err != nil {
		t.Fatal(err)
	}
	// target: open the query
	kidLen := u16(msg[1:])
	if !bytes.Equal(msg[3:3+kidLen], cfg.keyID) {
		t.Fatal("key id mismatch")
	}
	em := msg[3+kidLen+2:]
	priv, _ := hpke.NewDHKEMPrivateKey(sk)
	rcp, err := hpke.NewRecipient(em[:32], priv, hpke.HKDFSHA256(), hpke.AES128GCM(), []byte("odoh query"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := rcp.Open(append([]byte{1}, lp(cfg.keyID)...), em[32:])
	if err != nil || !bytes.Equal(pt, qplain) {
		t.Fatalf("target could not open query: %v", err)
	}
	// target: answer with one A record
	resp := append([]byte{0, 0, 0x81, 0x80, 0, 0, 0, 1, 0, 0, 0, 0}, []byte{0xC0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7}...)
	secret, _ := rcp.Export("odoh response", 16)
	nonce := make([]byte, 16)
	rand.Read(nonce)
	prk, _ := hkdf.Extract(sha256.New, secret, append(append([]byte{}, qplain...), lp(nonce)...))
	key, _ := hkdf.Expand(sha256.New, prk, "odoh key", 16)
	iv, _ := hkdf.Expand(sha256.New, prk, "odoh nonce", 12)
	blk, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(blk)
	ct := g.Seal(nil, iv, append(lp(resp), lp(nil)...), append([]byte{2}, lp(nonce)...))
	body := append(append([]byte{2}, lp(nonce)...), lp(ct)...)

	got, err := openResponse(sender, qplain, body)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := parseAnswer(got, typeA)
	if err != nil || len(a.Addrs) != 1 || a.Addrs[0] != netip.MustParseAddr("192.0.2.7") {
		t.Fatalf("decoded %+v %v", a, err)
	}
	body[len(body)-1] ^= 1
	if _, err := openResponse(sender, qplain, body); err == nil {
		t.Fatal("tampered response opened")
	}
}

func TestCymruName(t *testing.T) {
	if got := cymruName(netip.MustParseAddr("8.8.4.4")); got != "4.4.8.8.origin.asn.cymru.com" {
		t.Error(got)
	}
	if got := cymruName(netip.MustParseAddr("2001:4860::8888")); !strings.HasPrefix(got, "8.8.8.8.0.0.0.0") || !strings.HasSuffix(got, "0.6.8.4.1.0.0.2.origin6.asn.cymru.com") {
		t.Error(got)
	}
}

type errW1 struct{}

func (errW1) Query(string, uint16) (Answer, error) { return Answer{}, os.ErrDeadlineExceeded }

// A resolver that cannot answer the signed/unsigned question must not make a
// name disappear from the audit.
func TestIntakeErrorStillQueues(t *testing.T) {
	a, _ := newTestAuditor(t, nil, nil, func() float64 { return 0.99 })
	a.W1 = errW1{}
	a.Intake("slow.test")
	if a.QueueLen() != 1 || a.Out.st.Counters["intake_errors"] != 1 {
		t.Fatalf("queue %d, intake_errors %d", a.QueueLen(), a.Out.st.Counters["intake_errors"])
	}
}
