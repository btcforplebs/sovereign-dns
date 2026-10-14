package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

var relays = []string{"wss://relay-a.example", "wss://relay-b.example"}

func mustKey(t *testing.T) *btcec.PrivateKey {
	t.Helper()
	sk, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return sk
}

// signed is what Logen's signer produces: the list sealed and signed by sk.
func signed(t *testing.T, sk *btcec.PrivateKey, s *settings, at int64) *event {
	t.Helper()
	content, err := seal(s, sk)
	if err != nil {
		t.Fatal(err)
	}
	ev := &event{CreatedAt: at, Kind: settingsKind, Tags: [][]string{{"d", settingsD}}, Content: content}
	if err := ev.sign(sk); err != nil {
		t.Fatal(err)
	}
	return ev
}

type testBox struct {
	key    *btcec.PrivateKey
	pihole *fakePihole
	sync   *syncer
}

func newBox(t *testing.T, owner string, rl *fakeRelays, clock *time.Time) *testBox {
	t.Helper()
	b := &testBox{key: mustKey(t), pihole: newFakePihole(t)}
	b.sync = b.syncer(t, owner, rl, clock, t.TempDir())
	return b
}

func (b *testBox) syncer(t *testing.T, owner string, rl *fakeRelays, clock *time.Time, dir string) *syncer {
	s := &syncer{owner: owner, key: b.key, relays: relays, pihole: b.pihole.client(), dataDir: dir,
		now: func() time.Time { return *clock }, fetch: rl.fetch, publish: rl.publish}
	if err := s.load(); err != nil {
		t.Fatal(err)
	}
	return s
}

func (b *testBox) run(t *testing.T) {
	t.Helper()
	if err := b.sync.run(context.Background(), nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func (b *testBox) asBox(name string) box { return box{PubKey: pubHex(b.key), Name: name} }

func blocklist(addrs ...string) []list {
	var out []list
	for _, a := range addrs {
		out = append(out, list{Address: a, Type: "block", Enabled: true, Groups: []string{defaultGroup}})
	}
	return out
}

func (f *fakePihole) addresses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.lists {
		out = append(out, l.Address)
	}
	return out
}

func TestNIP44SpecVector(t *testing.T) {
	// First vector of the NIP-44 spec (nip44.vectors.json, v2.valid.encrypt_decrypt[0]).
	raw1, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	raw2, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000002")
	sk1, _ := btcec.PrivKeyFromBytes(raw1)
	sk2, _ := btcec.PrivKeyFromBytes(raw2)
	ck, err := nip44ConversationKey(sk1, pubHex(sk2))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(ck[:]); got != "c41c775356fd92eadc63ff5a0dc1da211b268cbea22316767095b2871ea1412d" {
		t.Fatalf("conversation key %s", got)
	}
	nonce, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	want := "AgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABee0G5VSK0/9YypIObAtDKfYEAjD35uVkHyB0F4DwrcNaCXlCWZKaArsGrY6M9wnuTMxWfp1RTN9Xga8no+kF5Vsb"
	got, err := nip44Encrypt("a", ck, nonce)
	if err != nil || got != want {
		t.Fatalf("encrypt: %v %s", err, got)
	}
	if pt, err := nip44Decrypt(want, ck); err != nil || pt != "a" {
		t.Fatalf("decrypt: %v %q", err, pt)
	}
	// The other side derives the same key.
	ck2, _ := nip44ConversationKey(sk2, pubHex(sk1))
	if ck2 != ck {
		t.Fatal("conversation key is not symmetric")
	}
	for _, n := range []int{1, 32, 33, 64, 65, 100, 255, 256, 257, 383, 384, 385, 1000, 65535} {
		pt := strings.Repeat("x", n)
		ct, err := nip44Encrypt(pt, ck, nil)
		if err != nil {
			t.Fatal(err)
		}
		if back, err := nip44Decrypt(ct, ck); err != nil || back != pt {
			t.Fatalf("round trip at %d: %v", n, err)
		}
	}
	tampered := []byte(want)
	tampered[40] ^= 1
	if _, err := nip44Decrypt(string(tampered), ck); err == nil {
		t.Fatal("tampered payload decrypted")
	}
}

func TestNpubRoundTrip(t *testing.T) {
	// fiatjaf's well-known npub, as NIP-19 documents it.
	const npub = "npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6"
	const hx = "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"
	if got, err := parsePubkey(npub); err != nil || got != hx {
		t.Fatalf("decode: %v %s", err, got)
	}
	if got := npubEncode(hx); got != npub {
		t.Fatalf("encode: %s", got)
	}
}

// A newer list signed by Logen replaces the older one; an older one never
// comes back, even when a relay serves it.
func TestNewerSignedListReplacesOlder(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)

	v1 := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/one.txt")}, clock.Unix()-100)
	rl.put(v1, relays...)
	b.run(t)
	if got := b.pihole.addresses(); len(got) != 1 || got[0] != "https://lists.example/one.txt" {
		t.Fatalf("after v1: %v", got)
	}

	v2 := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/two.txt")}, clock.Unix()-50)
	rl.put(v2, relays[1]) // only one relay has it yet
	b.run(t)
	if got := b.pihole.addresses(); len(got) != 1 || got[0] != "https://lists.example/two.txt" {
		t.Fatalf("after v2: %v", got)
	}
	if b.sync.state.Applied.ID != v2.ID {
		t.Fatal("v2 not recorded as applied")
	}

	// A relay that only has v1 (stale, or replaying it) cannot roll the box back.
	rl.events = map[string][]*event{relays[0]: {v1}}
	b.run(t)
	if got := b.pihole.addresses(); len(got) != 1 || got[0] != "https://lists.example/two.txt" {
		t.Fatalf("rolled back to %v", got)
	}
	// Nor can it after a restart, from the saved state alone.
	b.sync = b.syncer(t, pubHex(logen), rl, &clock, b.sync.dataDir)
	b.run(t)
	if b.sync.state.Applied.ID != v2.ID || b.pihole.addresses()[0] != "https://lists.example/two.txt" {
		t.Fatal("rolled back after restart")
	}
}

// Lists signed by anyone else, tampered with, dated in the future, or not
// readable by this box are ignored; the box keeps what it had.
func TestIgnoresForeignAndUnreadableLists(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen, mallory := mustKey(t), mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	good := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/good.txt")}, clock.Unix()-100)
	rl.put(good, relays...)
	b.run(t)

	evil := &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://evil.example/allow-all.txt")}
	cases := map[string]*event{}

	// Signed by someone else, correctly sealed to this box.
	cases["other signer"] = signed(t, mallory, evil, clock.Unix())

	// An old list of Logen's, re-signed by someone else with a newer date: it
	// opens fine (Logen sealed it), so only the author check stops a rollback.
	old := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/old.txt")}, clock.Unix()-500)
	resigned := &event{CreatedAt: clock.Unix(), Kind: settingsKind, Tags: old.Tags, Content: old.Content}
	_ = resigned.sign(mallory)
	cases["Logen's old list re-signed by someone else"] = resigned

	// The same old list with only its date moved forward: the id is recomputed,
	// so only the signature check stops it.
	redated := *old
	redated.CreatedAt = clock.Unix()
	redated.ID = redated.computeID()
	cases["Logen's old list re-dated"] = &redated

	// Mallory's event relabelled with Logen's pubkey: the signature no longer verifies.
	relabel := signed(t, mallory, evil, clock.Unix())
	relabel.PubKey = pubHex(logen)
	relabel.ID = relabel.computeID()
	cases["claims Logen, wrong signature"] = relabel

	// Logen's real event with the content swapped.
	swapped := *signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}}, clock.Unix())
	other := signed(t, mallory, evil, clock.Unix())
	swapped.Content = other.Content
	cases["content swapped after signing"] = &swapped

	// Logen's, but sealed only to another box.
	cases["not sealed to this box"] = signed(t, logen, &settings{V: 1, Boxes: []box{{PubKey: pubHex(mustKey(t)), Name: "other"}}, Lists: blocklist("https://lists.example/x.txt")}, clock.Unix())

	// Logen's, but the content is not a sealed list at all.
	junk := &event{CreatedAt: clock.Unix(), Kind: settingsKind, Tags: [][]string{{"d", settingsD}}, Content: "not json"}
	_ = junk.sign(logen)
	cases["unreadable content"] = junk

	// Logen's, readable, but carries a LAN address.
	cases["carries a LAN address"] = signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("http://192.168.1.20/list.txt")}, clock.Unix())

	// Logen's and valid, but dated an hour ahead.
	cases["dated in the future"] = signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/future.txt")}, clock.Unix()+3600)

	// Same kind, Logen's, different d tag.
	otherD := &event{CreatedAt: clock.Unix(), Kind: settingsKind, Tags: [][]string{{"d", "something-else"}}, Content: signed(t, logen, evil, 0).Content}
	_ = otherD.sign(logen)
	cases["other d tag"] = otherD

	want := b.pihole.snapshot(t)
	for name, ev := range cases {
		t.Run(name, func(t *testing.T) {
			rl.events = map[string][]*event{relays[0]: {good}, relays[1]: {good, ev}}
			b.sync.newest = good
			_ = b.sync.run(context.Background(), nil)
			if got := b.pihole.snapshot(t); got != want {
				t.Fatalf("Pi-hole changed:\n%s", got)
			}
			if b.sync.state.Applied.ID != good.ID {
				t.Fatal("applied list changed")
			}
		})
	}
}

// Two boxes, each with its own Pi-hole and its own history, end up the same.
// Nothing either had is removed until the list itself drops it.
func TestTwoBoxesConverge(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	rl := newFakeRelays()
	mac, pi := newBox(t, pubHex(logen), rl, &clock), newBox(t, pubHex(logen), rl, &clock)

	// Before any list: the Mac box has Logen's existing blocklist, the Pi has
	// a domain someone added in its own Pi-hole page.
	ctx := context.Background()
	mp := mac.pihole.client()
	_ = mp.login(ctx)
	_ = mp.putList(ctx, list{Address: "https://lists.example/existing.txt", Type: "block", Enabled: true}, []int{0}, false)
	mp.logout(ctx)
	pp := pi.pihole.client()
	_ = pp.login(ctx)
	_ = pp.putDomain(ctx, domain{Domain: "ads.example", Type: "deny", Kind: "exact", Enabled: true}, []int{0}, false)
	pp.logout(ctx)
	piOnly := pi.pihole.snapshot(t)

	// No list on the relays yet: neither box touches its Pi-hole.
	mac.run(t)
	pi.run(t)
	if pi.pihole.snapshot(t) != piOnly {
		t.Fatal("a box changed its Pi-hole with no list published")
	}

	// 1. Logen saves the first list from the Mac box's page (seeded from its Pi-hole).
	first := &settings{V: 1, Boxes: []box{mac.asBox("mac"), pi.asBox("pi")},
		Groups: []group{{Name: "kids", Comment: "kids' tablets", Enabled: true}},
		Lists:  []list{{Address: "https://lists.example/existing.txt", Type: "block", Enabled: true, Groups: []string{defaultGroup, "kids"}}},
	}
	rl.put(signed(t, logen, first, clock.Unix()), relays...)
	mac.run(t)
	pi.run(t)
	// The Pi kept its own domain (never wiped), and has the shared list.
	if !strings.Contains(pi.pihole.snapshot(t), "ads.example") {
		t.Fatal("the Pi's own domain was wiped")
	}
	if !strings.Contains(pi.pihole.snapshot(t), "existing.txt") || !strings.Contains(pi.pihole.snapshot(t), "kids") {
		t.Fatalf("the Pi did not get the list: %s", pi.pihole.snapshot(t))
	}

	// 2. Logen opens the Pi's page, which shows ads.example as "only on this
	// box"; he adds it to the shared list and saves from there.
	pp = pi.pihole.client()
	_ = pp.login(ctx)
	st, _ := pp.read(ctx)
	pp.logout(ctx)
	x := extras(st, first)
	if len(x.Domains) != 1 || x.Domains[0].Domain != "ads.example" || len(x.Lists) != 0 {
		t.Fatalf("extras: %+v", x)
	}
	second := *first
	second.Domains = append(second.Domains, x.Domains...)
	clock = clock.Add(time.Minute)
	rl.put(signed(t, logen, &second, clock.Unix()), relays...)
	pi.run(t)
	mac.run(t)
	if a, b := mac.pihole.snapshot(t), pi.pihole.snapshot(t); a != b {
		t.Fatalf("boxes differ:\nmac %s\npi  %s", a, b)
	}

	// 3. Logen drops the blocklist and the group. The Pi created them, so it
	// removes them. The Mac had the blocklist before any list existed, so it
	// keeps it (and its page lists it under "only on this box"); the group the
	// Mac created goes.
	third := second
	third.Groups = nil
	third.Lists = blocklist("https://lists.example/replacement.txt")
	clock = clock.Add(time.Minute)
	rl.put(signed(t, logen, &third, clock.Unix()), relays...)
	mac.run(t)
	pi.run(t)
	a, b := mac.pihole.snapshot(t), pi.pihole.snapshot(t)
	if strings.Contains(b, "existing.txt") || strings.Contains(b, "kids") || !strings.Contains(b, "replacement.txt") || !strings.Contains(b, "ads.example") {
		t.Fatalf("unexpected Pi state: %s", b)
	}
	if !strings.Contains(a, "existing.txt") || strings.Contains(a, "kids") || !strings.Contains(a, "replacement.txt") || !strings.Contains(a, "ads.example") {
		t.Fatalf("unexpected Mac state: %s", a)
	}
	// The Pi had ads.example before the list named it; the Mac created it.
	// Dropping it removes it from the Mac only.
	fourth := third
	fourth.Domains = nil
	clock = clock.Add(time.Minute)
	rl.put(signed(t, logen, &fourth, clock.Unix()), relays...)
	mac.run(t)
	pi.run(t)
	if strings.Contains(mac.pihole.snapshot(t), "ads.example") || !strings.Contains(pi.pihole.snapshot(t), "ads.example") {
		t.Fatalf("ads.example: mac %s / pi %s", mac.pihole.snapshot(t), pi.pihole.snapshot(t))
	}
	if mac.pihole.gravity == 0 || pi.pihole.gravity == 0 {
		t.Fatal("gravity was not rebuilt after list changes")
	}
}

// Tron's case: an entry already on the Pi-hole, which a shared list later
// names and then drops, is never removed. That holds for a blocklist, a deny
// domain (removing it would silently unblock the domain) and a group, and
// across a restart.
func TestPreexistingEntryNeverRemoved(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	ctx := context.Background()
	p := b.pihole.client()
	_ = p.login(ctx)
	_ = p.putGroup(ctx, group{Name: "kids", Enabled: true}, false)
	_ = p.putList(ctx, list{Address: "https://lists.example/mine.txt", Type: "block", Enabled: true}, []int{0}, false)
	_ = p.putDomain(ctx, domain{Domain: "tracker.example", Type: "deny", Kind: "exact", Enabled: true}, []int{0}, false)
	p.logout(ctx)
	before := b.pihole.snapshot(t)

	v1 := &settings{V: 1, Boxes: []box{b.asBox("pi")}, Groups: []group{{Name: "kids", Enabled: true}},
		Lists:   []list{{Address: "https://lists.example/mine.txt", Type: "block", Enabled: true, Groups: []string{defaultGroup}}, {Address: "https://lists.example/new.txt", Type: "block", Enabled: true, Groups: []string{"kids"}}},
		Domains: []domain{{Domain: "tracker.example", Type: "deny", Kind: "exact", Enabled: true, Groups: []string{defaultGroup}}}}
	rl.put(signed(t, logen, v1, clock.Unix()), relays...)
	b.run(t)
	if m := b.sync.state.Managed; len(m.Groups) != 0 || len(m.Domains) != 0 || len(m.Lists) != 1 || m.Lists[0] != "block https://lists.example/new.txt" {
		t.Fatalf("managed set adopted pre-existing entries: %+v", m)
	}

	b.sync = b.syncer(t, pubHex(logen), rl, &clock, b.sync.dataDir) // restart in between
	clock = clock.Add(time.Minute)
	rl.put(signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}}, clock.Unix()), relays...)
	b.run(t)
	if got := b.pihole.snapshot(t); got != before {
		t.Fatalf("pre-existing entries changed or removed:\nbefore %s\nafter  %s", before, got)
	}
}

// A state.json edited on disk gets the same checks as a list from a relay;
// a far-future saved list would otherwise freeze the box on it for good.
func TestSavedStateIsReverified(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen, mallory := mustKey(t), mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	for name, ev := range map[string]*event{
		"far future": signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}}, clock.Unix()+365*24*3600),
		"not Logen":  signed(t, mallory, &settings{V: 1, Boxes: []box{b.asBox("pi")}}, clock.Unix()),
	} {
		b.sync.state = savedState{Applied: ev, Managed: managedSet{Lists: []string{"block https://lists.example/victim.txt"}}}
		if err := b.sync.save(); err != nil {
			t.Fatal(err)
		}
		s := b.syncer(t, pubHex(logen), rl, &clock, b.sync.dataDir)
		if s.state.Applied != nil || len(s.state.Managed.Lists) != 0 {
			t.Fatalf("%s: saved state trusted: %+v", name, s.state)
		}
	}
}

// With every relay down, a box keeps its list, and puts back a shared entry
// someone removed in its Pi-hole page.
func TestRelaysDownKeepsLastList(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	rl.put(signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/one.txt")}, clock.Unix()), relays...)
	b.run(t)
	want := b.pihole.snapshot(t)

	for _, r := range relays {
		rl.down[r] = true
	}
	b.pihole.mu.Lock()
	b.pihole.lists = nil
	b.pihole.mu.Unlock()
	b.sync = b.syncer(t, pubHex(logen), rl, &clock, b.sync.dataDir) // and restarted
	b.run(t)
	if got := b.pihole.snapshot(t); got != want {
		t.Fatalf("not restored with relays down:\n%s", got)
	}
	if b.sync.status.RelaysOK != 0 {
		t.Fatal("relays reported up")
	}
}

// guardCases: every list here must be refused (ok=false) or accepted (ok=true)
// by both settings.go and the page. Addresses are private ranges, never real.
var guardCases = []struct {
	name string
	s    settings
	ok   bool
}{
	{"list host is an IP", settings{V: 1, Lists: blocklist("http://10.0.0.5/hosts")}, false},
	{"list host is IPv6", settings{V: 1, Lists: blocklist("http://[fd00::1]/hosts")}, false},
	{"list host .local", settings{V: 1, Lists: blocklist("http://nas.local/hosts")}, false},
	{"list host .local.", settings{V: 1, Lists: blocklist("http://nas.local./hosts")}, false},
	{"list host single label", settings{V: 1, Lists: blocklist("http://pihole/hosts")}, false},
	{"list URL with credentials", settings{V: 1, Lists: blocklist("https://user:pw@lists.example/x")}, false},
	{"IP in list path", settings{V: 1, Lists: blocklist("https://cdn.example.com/10.0.0.5.txt")}, false},
	{"IP in list query", settings{V: 1, Lists: blocklist("https://example.com/l.txt?from=192.168.1.5")}, false},
	{"nip.io host", settings{V: 1, Lists: blocklist("https://10.0.0.5.nip.io/l.txt")}, false},
	{"dashed sslip.io host", settings{V: 1, Lists: blocklist("https://10-0-0-5.sslip.io/l.txt")}, false},
	{"IP in list comment", settings{V: 1, Lists: []list{{Address: "https://lists.example/x", Type: "block", Comment: "from 192.0.2.9"}}}, false},
	{"exact domain is an IP", settings{V: 1, Domains: []domain{{Domain: "192.168.1.1", Type: "deny", Kind: "exact"}}}, false},
	{"exact domain IP with trailing dot", settings{V: 1, Domains: []domain{{Domain: "10.0.0.5.", Type: "deny", Kind: "exact"}}}, false},
	{"comment IP with trailing dot", settings{V: 1, Domains: []domain{{Domain: "example.com", Type: "deny", Kind: "exact", Comment: "seen at 10.0.0.5."}}}, false},
	{"regex domain with escaped IP", settings{V: 1, Domains: []domain{{Domain: `^10\.0\.0\.5$`, Type: "deny", Kind: "regex"}}}, false},
	{"regex reverse-lookup of a LAN IP", settings{V: 1, Domains: []domain{{Domain: `5\.1\.168\.192\.in-addr\.arpa`, Type: "deny", Kind: "regex"}}}, false},
	{"regex reverse-lookup of a LAN range", settings{V: 1, Domains: []domain{{Domain: `(\.|^)168\.192\.in-addr\.arpa$`, Type: "allow", Kind: "regex"}}}, false},
	{"regex IPv6", settings{V: 1, Domains: []domain{{Domain: `^fd00::5$`, Type: "deny", Kind: "regex"}}}, false},
	{"regex .local", settings{V: 1, Domains: []domain{{Domain: `(\.|^)nas\.local$`, Type: "deny", Kind: "regex"}}}, false},
	{"exact .local", settings{V: 1, Domains: []domain{{Domain: "nas.local", Type: "allow", Kind: "exact"}}}, false},
	{"exact .lan", settings{V: 1, Domains: []domain{{Domain: "logen-mbp.lan", Type: "allow", Kind: "exact"}}}, false},
	{"exact single label", settings{V: 1, Domains: []domain{{Domain: "nas", Type: "allow", Kind: "exact"}}}, false},
	{"exact localhost.", settings{V: 1, Domains: []domain{{Domain: "localhost.", Type: "allow", Kind: "exact"}}}, false},
	{"box name .local", settings{V: 1, Boxes: []box{{PubKey: strings.Repeat("ab", 32), Name: "pihole.local"}}}, false},
	{"box name with IP", settings{V: 1, Boxes: []box{{PubKey: strings.Repeat("ab", 32), Name: "pi at 10.0.0.7"}}}, false},
	{"group named for an IPv6 address", settings{V: 1, Groups: []group{{Name: "home fe80::1 devices"}}}, false},
	{"bad box pubkey", settings{V: 1, Boxes: []box{{PubKey: "nothex", Name: "x"}}}, false},
	{"unknown group", settings{V: 1, Lists: []list{{Address: "https://lists.example/x", Type: "block", Groups: []string{"missing"}}}}, false},
	{"unknown version", settings{V: 2}, false},
	{"ordinary list", settings{V: 1,
		Boxes:  []box{{PubKey: strings.Repeat("ab", 32), Name: "This box"}, {PubKey: strings.Repeat("cd", 32), Name: "Pi 4"}},
		Groups: []group{{Name: "kids", Comment: "added 2026-10-14", Enabled: true}},
		Lists: []list{
			{Address: "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts", Type: "block", Enabled: true, Groups: []string{"kids"}},
			{Address: "https://v.firebog.net/hosts/AdguardDNS.txt", Type: "block", Enabled: true},
			{Address: "https://big.oisd.nl", Type: "block", Enabled: true, Comment: "v2 list, 1.5M domains"},
		},
		Domains: []domain{
			{Domain: `(\.|^)tiktok\.com$`, Type: "deny", Kind: "regex"},
			{Domain: `^ad[0-9]+\.example\.com$`, Type: "deny", Kind: "regex"},
			{Domain: "example.com", Type: "allow", Kind: "exact"},
			{Domain: "my.home-depot.example.", Type: "allow", Kind: "exact"},
		}}, true},
}

func TestValidateRefusesAddresses(t *testing.T) {
	for _, c := range guardCases {
		err := c.s.validate()
		if c.ok && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// The page refuses the same lists as the box, before the signer is asked:
// its copy of the guard (between the markers in page.html) runs under node
// on the same cases.
func TestPageGuardMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; the page guard is not checked")
	}
	page := string(pageHTML)
	const begin, end = "// --- address guard", "// --- end address guard ---"
	i, j := strings.Index(page, begin), strings.Index(page, end)
	if i < 0 || j < i {
		t.Fatal("page.html has no address guard")
	}
	type jsCase struct {
		Name string   `json:"name"`
		S    settings `json:"s"`
	}
	var cases []jsCase
	for _, c := range guardCases {
		cases = append(cases, jsCase{c.name, c.s})
	}
	in, _ := json.Marshal(cases)
	script := page[i:j] + "\nconst cases = " + string(in) + ";\nconsole.log(JSON.stringify(cases.map((c) => validateList(c.s).length === 0)));\n"
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(guardCases) {
		t.Fatalf("node output: %s", out)
	}
	for k, c := range guardCases {
		if got[k] != c.ok {
			t.Errorf("%s: page accepts=%v, want %v", c.name, got[k], c.ok)
		}
	}
}

// The page's save path: only Logen's signature gets a list published, and a
// save based on a list someone else has since replaced is refused.
func TestPublishEndpoint(t *testing.T) {
	clock := time.Now()
	logen, mallory := mustKey(t), mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	h := (&server{s: b.sync}).routes()
	post := func(ev *event, base string) (int, string) {
		body, _ := json.Marshal(publishRequest{Event: ev, Base: base})
		req := httptest.NewRequest(http.MethodPost, "http://localhost:8083/api/publish", bytes.NewReader(body))
		req.Header.Set("Origin", "http://localhost:8083")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	mine := func(addr string, at int64) *event {
		return signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist(addr)}, at)
	}

	if code, body := post(signed(t, mallory, &settings{V: 1, Boxes: []box{b.asBox("pi")}}, clock.Unix()), ""); code != 400 {
		t.Fatalf("someone else's list: %d %s", code, body)
	}
	if code, _ := post(signed(t, logen, &settings{V: 1}, clock.Unix()), ""); code != 400 {
		t.Fatal("a list this box can't read was published")
	}
	v1 := mine("https://lists.example/one.txt", clock.Unix())
	if code, body := post(v1, ""); code != 200 {
		t.Fatalf("first save: %d %s", code, body)
	}
	if n := len(rl.events[relays[0]]) + len(rl.events[relays[1]]); n != 2 {
		t.Fatalf("published to %d relays", n)
	}
	if b.pihole.addresses()[0] != "https://lists.example/one.txt" {
		t.Fatal("not applied on save")
	}
	// Two pages loaded v1. One saves v2; the other, still based on v1, is refused.
	if code, body := post(mine("https://lists.example/two.txt", clock.Unix()+1), v1.ID); code != 200 {
		t.Fatalf("second save: %d %s", code, body)
	}
	if code, _ := post(mine("https://lists.example/three.txt", clock.Unix()+2), v1.ID); code != 409 {
		t.Fatalf("stale save was not refused: %d", code)
	}
	// Wrong Host (DNS rebinding) and cross-origin posts are refused outright.
	req := httptest.NewRequest(http.MethodGet, "http://evil.example:8083/api/state", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding host got %d", w.Code)
	}
	body, _ := json.Marshal(publishRequest{Event: v1})
	req = httptest.NewRequest(http.MethodPost, "http://localhost:8083/api/publish", bytes.NewReader(body))
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin post got %d", w.Code)
	}
}

// The state the page reads carries neither the box's key nor the Pi-hole
// password. (It does show this Pi-hole's lists: that is what the page edits.)
func TestStateCarriesNoSecrets(t *testing.T) {
	clock := time.Now()
	logen := mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	b.pihole.password = "correct-horse-battery-staple"
	b.sync.pihole = b.pihole.client()
	rl.put(signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/x")}, clock.Unix()), relays...)
	b.run(t)
	w := httptest.NewRecorder()
	(&server{s: b.sync}).routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8083/api/state", nil))
	out := w.Body.String()
	if w.Code != 200 || !strings.Contains(out, b.sync.state.Applied.ID) {
		t.Fatalf("state: %d %s", w.Code, out)
	}
	if strings.Contains(out, hex.EncodeToString(b.key.Serialize())) || strings.Contains(out, b.pihole.password) {
		t.Fatalf("state leaks a secret: %s", out)
	}
}
