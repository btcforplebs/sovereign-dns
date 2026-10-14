package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

// runPage runs the page's own envelope code (page.html from "use strict" to
// the address guard) under node, as the owner's browser would, and prints
// what body logs. The fake signer opens exactly the NIP-44 payloads that open
// with the owner's key (worked out here in Go with the real NIP-44 code), and
// throws on anything else, as a real signer does on a bad MAC. Asked to
// encrypt, it records the request and returns a placeholder, SEAL<n>, which
// sealedByPage swaps for the real payload.
func runPage(t *testing.T, owner *btcec.PrivateKey, evs []*event, body string) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; the page is not checked")
	}
	page := string(pageHTML)
	i, j := strings.Index(page, `"use strict";`), strings.Index(page, "// --- address guard")
	if i < 0 || j < i {
		t.Fatal("page.html has no envelope code")
	}
	ck, err := nip44ConversationKey(owner, pubHex(owner))
	if err != nil {
		t.Fatal(err)
	}
	opens := map[string]string{}
	for _, ev := range evs {
		var env envelope
		if json.Unmarshal([]byte(ev.Content), &env) != nil {
			continue
		}
		for _, w := range env.Keys {
			if plain, err := nip44Decrypt(w, ck); err == nil {
				opens[w] = plain
			}
		}
	}
	opensJS, _ := json.Marshal(opens)
	evsJS, _ := json.Marshal(evs)
	script := page[i:j] + fmt.Sprintf(`
const OPENS = %s, EVENTS = %s, SEALS = [];
globalThis.window = { nostr: { nip44: {
  decrypt: async (peer, p) => { if (peer === st.owner && p in OPENS) return OPENS[p]; throw new Error("invalid MAC"); },
  encrypt: async (peer, plain) => { SEALS.push([peer, plain]); return "SEAL" + (SEALS.length - 1); },
} } };
st = { owner: %q };
(async () => { %s })().catch((e) => { console.error(e); process.exit(1); });
`, opensJS, evsJS, pubHex(owner), body)
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return out
}

type pageOpen struct {
	OK    bool     `json:"ok"`
	Err   string   `json:"err"`
	Lists []string `json:"lists"`
}

// pageOpens is unlock() on each event: checkEvent, then openList with the
// owner's key.
func pageOpens(t *testing.T, owner *btcec.PrivateKey, evs ...*event) []pageOpen {
	t.Helper()
	out := runPage(t, owner, evs, `
const res = [];
for (const ev of EVENTS) {
  try { await checkEvent(ev); const s = await openList(ev, st.owner); res.push({ ok: true, lists: (s.lists || []).map((l) => l.address) }); }
  catch (e) { res.push({ ok: false, err: e.message }); }
}
console.log(JSON.stringify(res));`)
	var res []pageOpen
	if err := json.Unmarshal(out, &res); err != nil || len(res) != len(evs) {
		t.Fatalf("page output: %s", out)
	}
	return res
}

// forge is what a box can do with a genuine list: it opens K from its own
// wrap, re-encrypts a list of its choosing under K with a fresh nonce, leaves
// the salt and every wrap (the owner's included) as they are, re-dates the
// event and recomputes the id. It can't sign as the owner, so the signature
// is left stale; only boxes check it. A sealed "at" field, if the format has
// one, is set to the new date.
func forge(t *testing.T, genuine *event, boxKey *btcec.PrivateKey, edit func(map[string]any), createdAt int64) *event {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(genuine.Content), &env); err != nil {
		t.Fatal(err)
	}
	ck, _ := nip44ConversationKey(boxKey, genuine.PubKey)
	var w string
	for _, p := range env["keys"].(map[string]any) {
		if plain, err := nip44Decrypt(p.(string), ck); err == nil {
			w = plain
		}
	}
	if len(w) < 64 {
		t.Fatal("box could not open its own wrap")
	}
	k, _ := hex.DecodeString(w[:64])
	block, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(block)
	nonce, _ := base64.StdEncoding.DecodeString(env["nonce"].(string))
	data, _ := base64.StdEncoding.DecodeString(env["data"].(string))
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.Unmarshal(plain, &s)
	edit(s)
	if _, ok := s["at"]; ok {
		s["at"] = createdAt
	}
	plain, _ = json.Marshal(s)
	nonce = make([]byte, 12)
	_, _ = rand.Read(nonce)
	env["nonce"] = base64.StdEncoding.EncodeToString(nonce)
	env["data"] = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, nil))
	content, _ := json.Marshal(env)
	ev := *genuine
	ev.Content, ev.CreatedAt = string(content), createdAt
	ev.ID = ev.computeID()
	return &ev
}

// Must-fix 1 (Tron, round 2): a box knows K, so it can show the owner's page
// any list under any date. The page must refuse what the owner didn't seal.
func TestPageRefusesListForgedByBox(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	genuine := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/real.txt")}, clock.Unix()-100)
	allowAll := func(s map[string]any) {
		s["lists"] = []map[string]any{{"address": "https://evil.example/allow-all.txt", "type": "allow", "enabled": true, "groups": []string{defaultGroup}}}
	}
	same := func(map[string]any) {}
	yearAhead := clock.Unix() + 365*24*3600
	forged := map[string]*event{
		"allow-all list dated a year ahead": forge(t, genuine, b.key, allowAll, yearAhead),
		"allow-all list, same date":         forge(t, genuine, b.key, allowAll, genuine.CreatedAt),
		"same list re-dated a year ahead":   forge(t, genuine, b.key, same, yearAhead),
	}

	// The gate has to be able to pass: the genuine list opens on the page.
	if got := pageOpens(t, logen, genuine)[0]; !got.OK || len(got.Lists) != 1 || got.Lists[0] != "https://lists.example/real.txt" {
		t.Fatalf("page refused the genuine list: %+v", got)
	}
	for name, ev := range forged {
		if got := pageOpens(t, logen, ev)[0]; got.OK {
			t.Errorf("%s: page opened it, lists %v", name, got.Lists)
		}
		// Go's open with the owner's key is the page's check in Go.
		if _, err := open(ev, logen, pubHex(logen)); err == nil {
			t.Errorf("%s: open with the owner's key accepted it", name)
		}
	}
}

// v1Sealed is a list in the format before this envelope version: K wrapped
// alone, nothing tying it to the event.
func v1Sealed(t *testing.T, owner *btcec.PrivateKey, s *settings, at int64) *event {
	t.Helper()
	plain, _ := json.Marshal(s)
	k, nonce := make([]byte, 32), make([]byte, 12)
	_, _ = rand.Read(k)
	_, _ = rand.Read(nonce)
	salt := base64.StdEncoding.EncodeToString(k[:16])
	block, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(block)
	env := map[string]any{"v": 1, "salt": salt, "nonce": base64.StdEncoding.EncodeToString(nonce),
		"data": base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, nil))}
	keys := map[string]string{}
	for _, r := range append([]string{pubHex(owner)}, s.Boxes[0].PubKey) {
		ck, _ := nip44ConversationKey(owner, r)
		keys[readerID(salt, r)], _ = nip44Encrypt(hex.EncodeToString(k), ck, nil)
	}
	env["keys"] = keys
	content, _ := json.Marshal(env)
	ev := &event{CreatedAt: at, Kind: settingsKind, Tags: [][]string{{"d", settingsD}}, Content: string(content)}
	if err := ev.sign(owner); err != nil {
		t.Fatal(err)
	}
	return ev
}

// Must-fix 2 (Tron, round 2): the sealed format changed without a version
// bump, so an older list was misread as re-dated. Now the format carries its
// own version, and box and page both name it when they meet an older one.
// (No v1 list was ever published to Logen's relays, so there is no legacy
// path to keep.)
func TestOlderFormatIsNamedByVersion(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	old := v1Sealed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/old.txt")}, clock.Unix()-100)

	if _, err := b.sync.openList(old); err == nil || !strings.Contains(err.Error(), "version 1") {
		t.Errorf("box: %v, want an unknown-version error", err)
	}
	if got := pageOpens(t, logen, old)[0]; got.OK || !strings.Contains(got.Err, "(v1)") {
		t.Errorf("page: %+v, want an unknown-format error", got)
	}
}

// The page seals, the box opens: both sides compute the same binding.
func TestPageSealOpensInBoxAndPage(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	rl := newFakeRelays()
	b := newBox(t, pubHex(logen), rl, &clock)
	at := clock.Unix() - 100
	out := runPage(t, logen, nil, fmt.Sprintf(`
const s = { v: 1, boxes: [{ pubkey: %q, name: "pi" }], groups: [], domains: [],
  lists: [{ address: "https://lists.example/from-page.txt", type: "block", comment: "", enabled: true, groups: ["Default"] }] };
console.log(JSON.stringify({ content: await sealList(s, %d), seals: SEALS }));`, pubHex(b.key), at))
	var res struct {
		Content string      `json:"content"`
		Seals   [][2]string `json:"seals"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.Seals) != 2 {
		t.Fatalf("page output: %s", out)
	}
	content := res.Content
	for n, sl := range res.Seals {
		ck, _ := nip44ConversationKey(logen, sl[0])
		w, err := nip44Encrypt(sl[1], ck, nil)
		if err != nil {
			t.Fatal(err)
		}
		content = strings.Replace(content, fmt.Sprintf("%q", fmt.Sprintf("SEAL%d", n)), fmt.Sprintf("%q", w), 1)
	}
	ev := &event{CreatedAt: at, Kind: settingsKind, Tags: [][]string{{"d", settingsD}}, Content: content}
	if err := ev.sign(logen); err != nil {
		t.Fatal(err)
	}
	rl.put(ev, relays...)
	b.run(t)
	if got := b.pihole.addresses(); len(got) != 1 || got[0] != "https://lists.example/from-page.txt" {
		t.Fatalf("box applied %v (note %q)", got, b.sync.status.Note)
	}
	if got := pageOpens(t, logen, ev)[0]; !got.OK {
		t.Fatalf("page refused its own list: %s", got.Err)
	}
}
