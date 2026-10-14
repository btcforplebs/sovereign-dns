package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

// needNode finds node. The page's half of the checks runs under it, so a
// missing node fails the suite; SKIP_PAGE_TESTS=1 skips them on purpose.
func needNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err == nil {
		return node
	}
	if os.Getenv("SKIP_PAGE_TESTS") == "1" {
		t.Skip("SKIP_PAGE_TESTS=1: page.html is NOT checked")
	}
	t.Fatal("node not found: page.html can't be checked. Install node, or set SKIP_PAGE_TESTS=1 to skip the page tests on purpose")
	return ""
}

// ownerOpens is what the owner's signer can decrypt among evs' wraps: the
// fake signers below answer exactly these, as a real one would.
func ownerOpens(t *testing.T, owner *btcec.PrivateKey, evs []*event) map[string]string {
	t.Helper()
	ck, err := nip44ConversationKey(owner, pubHex(owner))
	if err != nil {
		t.Fatal(err)
	}
	opens := map[string]string{}
	for _, ev := range evs {
		var env envelope
		if ev == nil || json.Unmarshal([]byte(ev.Content), &env) != nil {
			continue
		}
		for _, w := range env.Keys {
			if plain, err := nip44Decrypt(w, ck); err == nil {
				opens[w] = plain
			}
		}
	}
	return opens
}

// runPage runs the page's own envelope code (page.html from "use strict" to
// the address guard) under node, as the owner's browser would, and prints
// what body logs. The fake signer opens exactly the NIP-44 payloads that open
// with the owner's key (worked out here in Go with the real NIP-44 code), and
// throws on anything else, as a real signer does on a bad MAC. Asked to
// encrypt, it records the request and returns a placeholder, SEAL<n>, which
// sealedByPage swaps for the real payload.
func runPage(t *testing.T, owner *btcec.PrivateKey, evs []*event, body string) []byte {
	t.Helper()
	node := needNode(t)
	page := string(pageHTML)
	i, j := strings.Index(page, `"use strict";`), strings.Index(page, "// --- address guard")
	if i < 0 || j < i {
		t.Fatal("page.html has no envelope code")
	}
	opensJS, _ := json.Marshal(ownerOpens(t, owner, evs))
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

// pageApp is what the owner sees after pressing Unlock.
type pageApp struct {
	Editor bool              `json:"editor"` // #editor shown
	Msg    string            `json:"msg"`
	Kind   string            `json:"kind"`
	List   settings          `json:"list"`
	Base   string            `json:"base"`
	Store  map[string]string `json:"store"` // localStorage after
	// With save: what reached the box's /api/publish, and what the signer
	// was asked to encrypt for it.
	Published *struct {
		Event event `json:"event"`
	} `json:"published"`
	Seals [][2]string `json:"seals"`
}

// pageVisit is one visit to a box's page.
type pageVisit struct {
	st    stateResponse
	store map[string]string // localStorage at the start
	now   time.Time         // the browser's clock
	save  bool              // press Save after Unlock, changing nothing
}

// unlockPage runs the whole page script under node against a fake DOM: the
// box's /api/state answers st, localStorage starts as store, and the fake
// signer is the owner's (as in runPage). It loads, presses Unlock, and
// reports what the owner sees; with v.save it then presses Save.
func unlockPage(t *testing.T, owner *btcec.PrivateKey, v pageVisit) pageApp {
	t.Helper()
	st, store := v.st, v.store
	node := needNode(t)
	page := string(pageHTML)
	i, j := strings.Index(page, "<script>"), strings.Index(page, "</script>")
	if i < 0 || j < i {
		t.Fatal("page.html has no script")
	}
	st.Owner = pubHex(owner)
	opensJS, _ := json.Marshal(ownerOpens(t, owner, []*event{st.Newest}))
	stJS, _ := json.Marshal(st)
	if store == nil {
		store = map[string]string{}
	}
	storeJS, _ := json.Marshal(store)
	script := fmt.Sprintf(`"use strict";
const OPENS = %s, STATE = %s, STORE = %s, NOW = %d, SAVE = %t, SEALS = [];
let PUBLISHED = null;
Date.now = () => NOW * 1000;
class FakeEl {
  constructor(tag) { this.tag = tag; this.children = []; this.attrs = {}; this.dataset = {}; this.hidden = false; this.textContent = ""; this.className = ""; this.value = ""; }
  setAttribute(k, v) { this.attrs[k] = v; } removeAttribute(k) { delete this.attrs[k]; } addEventListener() {}
  append(...c) { this.children.push(...c); } replaceChildren(...c) { this.children = c; }
  querySelectorAll() { return []; } querySelector() { return null; } focus() {}
}
const ELS = {};
const byId = (id) => ELS[id] || (ELS[id] = new FakeEl("#" + id));
for (const id of ["locked", "editor", "extras-wrap"]) byId(id).hidden = true; // hidden in the HTML
globalThis.document = { getElementById: byId, createElement: (t) => new FakeEl(t), querySelectorAll: () => [] };
globalThis.fetch = async (url, opts) => {
  if (url !== "api/publish") return { ok: true, json: async () => JSON.parse(JSON.stringify(STATE)) };
  PUBLISHED = JSON.parse(opts.body);
  return { ok: true, json: async () => ({ published: 1, relays: 1, applied: true }) };
};
Object.defineProperty(globalThis, "localStorage", { configurable: true, value: {
  getItem: (k) => (k in STORE ? STORE[k] : null), setItem: (k, v) => { STORE[k] = String(v); } } });
globalThis.window = { nostr: { getPublicKey: async () => STATE.owner,
  signEvent: async (tmpl) => ({ ...tmpl, pubkey: STATE.owner, id: "00".repeat(32), sig: "" }), // the box checks the signature, not this test
  nip44: {
    decrypt: async (peer, p) => { if (peer === STATE.owner && p in OPENS) return OPENS[p]; throw new Error("invalid MAC"); },
    encrypt: async (peer, plain) => { SEALS.push([peer, plain]); return "SEAL" + (SEALS.length - 1); } } } };
%s
(async () => {
  await load(); await unlock();
  const seen = { editor: !ELS.editor.hidden, msg: ELS.msg.textContent, kind: ELS.msg.className, list: JSON.parse(JSON.stringify(list)), base };
  if (SAVE) await save({ preventDefault() {} });
  console.log(JSON.stringify({ ...seen, store: STORE, published: PUBLISHED, seals: SEALS }));
})().catch((e) => { console.error(e); process.exit(1); });
`, opensJS, stJS, storeJS, v.now.Unix(), v.save, page[i+len("<script>"):j])
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got pageApp
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("page output: %s", out)
	}
	return got
}

// savedList is what the page's save sealed, opened with the owner's key: the
// signer's placeholders swapped for real NIP-44 payloads first.
func savedList(t *testing.T, owner *btcec.PrivateKey, got pageApp) *settings {
	t.Helper()
	if got.Published == nil {
		t.Fatalf("nothing was published; message %q", got.Msg)
	}
	ev := got.Published.Event
	for n, sl := range got.Seals {
		ck, _ := nip44ConversationKey(owner, sl[0])
		w, err := nip44Encrypt(sl[1], ck, nil)
		if err != nil {
			t.Fatal(err)
		}
		ev.Content = strings.Replace(ev.Content, fmt.Sprintf("%q", fmt.Sprintf("SEAL%d", n)), fmt.Sprintf("%q", w), 1)
	}
	s, err := open(&ev, owner, pubHex(owner))
	if err != nil {
		t.Fatalf("the saved list does not open: %v", err)
	}
	return s
}

func addresses(ls []list) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Address)
	}
	return out
}

// boxState is what a box's /api/state says: its newest list, and a Pi-hole
// holding one blocklist of its own.
func boxState(b *testBox, newest *event) stateResponse {
	return stateResponse{Box: pubHex(b.key), Newest: newest, Local: &localState{Lists: blocklist("https://lists.example/on-this-box.txt")}}
}

// hostileState is a compromised box: its Pi-hole, as it reports it, allows
// everything and lets a tracker through, and it offers the same as extras.
func hostileState(b *testBox, newest *event) stateResponse {
	bad := &localState{
		Lists:   []list{{Address: "https://evil.example/allow-all.txt", Type: "allow", Enabled: true, Groups: []string{defaultGroup}}},
		Domains: []domain{{Domain: "tracker.example.com", Type: "allow", Kind: "exact", Enabled: true, Groups: []string{defaultGroup}}},
	}
	return stateResponse{Box: pubHex(b.key), Newest: newest, Local: bad, Extras: bad}
}

// stripOwnerWrap is what a box can do for free: drop the owner's wrap (keys
// is the part the binding doesn't cover) and recompute the id.
func stripOwnerWrap(genuine *event, owner string) *event {
	var env envelope
	_ = json.Unmarshal([]byte(genuine.Content), &env)
	delete(env.Keys, readerID(env.Salt, owner))
	content, _ := json.Marshal(env)
	ev := *genuine
	ev.Content = string(content)
	ev.ID = ev.computeID()
	return &ev
}

type pageFloor struct {
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
	Boxes     []box  `json:"boxes"`
}

func floorIn(t *testing.T, store map[string]string, owner *btcec.PrivateKey) *pageFloor {
	t.Helper()
	v, ok := store[settingsD+" floor "+pubHex(owner)]
	if !ok {
		return nil
	}
	var f pageFloor
	if err := json.Unmarshal([]byte(v), &f); err != nil {
		t.Fatalf("floor %q: %v", v, err)
	}
	return &f
}

// Must-fix 1 (Tron, round 3): a list the page can't open must not keep the
// owner out of the editor, or a compromised box locks him out of the one
// page that can remove it. He gets an editor, loudly, with no rules from the
// box that failed the check (round 4) and the boxes this browser last
// trusted.
func TestPageEditorOpensWhenListWontOpen(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	b := newBox(t, pubHex(logen), newFakeRelays(), &clock)
	other := box{PubKey: pubHex(mustKey(t)), Name: "Pi 4"}
	genuine := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi"), other}, Lists: blocklist("https://lists.example/real.txt")}, clock.Unix()-100)
	junk := &event{CreatedAt: clock.Unix(), Kind: settingsKind, Tags: [][]string{{"d", settingsD}}, Content: "not json"}
	_ = junk.sign(logen)

	// Control: the genuine list opens in the editor, with no warning, and
	// this browser keeps its boxes.
	got := unlockPage(t, logen, pageVisit{st: boxState(b, genuine), now: clock})
	if !got.Editor || strings.Join(addresses(got.List.Lists), " ") != "https://lists.example/real.txt" || strings.Contains(got.Kind, "err") {
		t.Fatalf("genuine list: %+v", got)
	}
	seen := got.Store
	for name, ev := range map[string]*event{
		"owner's wrap removed by the box": stripOwnerWrap(genuine, pubHex(logen)),
		"stray v1 list":                   v1Sealed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/old.txt")}, clock.Unix()-50),
		"content not a sealed list":       junk,
	} {
		for _, known := range []bool{true, false} {
			store := map[string]string{}
			if known {
				store = seen
			}
			got := unlockPage(t, logen, pageVisit{st: boxState(b, ev), store: store, now: clock})
			name := fmt.Sprintf("%s, boxes known=%v", name, known)
			if !got.Editor {
				t.Errorf("%s: editor not shown; message %q", name, got.Msg)
				continue
			}
			if len(got.List.Lists)+len(got.List.Domains)+len(got.List.Groups) != 0 || got.Base != ev.ID {
				t.Errorf("%s: editor holds rules %+v on base %s, want none on %s", name, got.List, got.Base, ev.ID)
			}
			wantBoxes := 0
			if known {
				wantBoxes = 2
			}
			if len(got.List.Boxes) != wantBoxes {
				t.Errorf("%s: editor names boxes %+v, want the %d this browser last trusted", name, got.List.Boxes, wantBoxes)
			}
			if !strings.Contains(got.Kind, "err") || !strings.Contains(got.Msg, "failed this page's checks") || !strings.Contains(got.Msg, "come from the box that failed the check") {
				t.Errorf("%s: no loud warning: %q (%s)", name, got.Msg, got.Kind)
			}
		}
	}

	// The genuine first run still starts from this box's Pi-hole.
	got = unlockPage(t, logen, pageVisit{st: boxState(b, nil), now: clock})
	if a := addresses(got.List.Lists); len(a) != 1 || a[0] != "https://lists.example/on-this-box.txt" || strings.Contains(got.Kind, "err") {
		t.Fatalf("first run: %+v", got)
	}
}

// Must-fix (Tron, round 4): a box that fails the check must not choose what
// the owner signs. Saving from the fallback lets no rule from that box's
// Pi-hole in, and keeps every box the last trusted list named.
func TestPageSaveFromFallbackKeepsBoxesAndNoHostileRule(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	b := newBox(t, pubHex(logen), newFakeRelays(), &clock)
	other := box{PubKey: pubHex(mustKey(t)), Name: "Pi 4"}
	genuine := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi"), other},
		Lists:   blocklist("https://lists.example/real.txt"),
		Domains: []domain{{Domain: "tracker.example.com", Type: "deny", Kind: "exact", Enabled: true, Groups: []string{defaultGroup}}}}, clock.Unix()-100)

	// The owner has used this box's page before, while it was honest.
	store := unlockPage(t, logen, pageVisit{st: boxState(b, genuine), now: clock}).Store

	// Now the box strips the owner's wrap and reports a hostile Pi-hole. The
	// owner, told the list can't be trusted, saves.
	got := unlockPage(t, logen, pageVisit{st: hostileState(b, stripOwnerWrap(genuine, pubHex(logen))), store: store, now: clock, save: true})
	saved := savedList(t, logen, got)
	for _, l := range saved.Lists {
		if l.Type == "allow" || strings.Contains(l.Address, "evil.example") {
			t.Errorf("hostile list signed: %+v", l)
		}
	}
	for _, d := range saved.Domains {
		if d.Type == "allow" {
			t.Errorf("hostile domain signed: %+v", d)
		}
	}
	keys := map[string]bool{}
	for _, x := range saved.Boxes {
		keys[x.PubKey] = true
	}
	if !keys[other.PubKey] || !keys[pubHex(b.key)] || len(keys) != 2 {
		t.Errorf("saved for boxes %+v, want this box and %s", saved.Boxes, other.Name)
	}
}

// Must-fix 2 (Tron, round 3): a box can offer an older genuine list, and the
// owner's next save would make it the newest everywhere, re-admitting a box
// he removed. The page keeps a floor and won't edit anything older.
func TestPageRefusesListOlderThanFloor(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	b := newBox(t, pubHex(logen), newFakeRelays(), &clock)
	removed := box{PubKey: pubHex(mustKey(t)), Name: "removed box"}
	older := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi"), removed}, Lists: blocklist("https://lists.example/old.txt")}, clock.Unix()-1000)
	newer := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/new.txt")}, clock.Unix())

	// Opening the newer list sets the floor.
	got := unlockPage(t, logen, pageVisit{st: boxState(b, newer), now: clock})
	if !got.Editor || strings.Join(addresses(got.List.Lists), " ") != "https://lists.example/new.txt" {
		t.Fatalf("newer list: %+v", got)
	}
	store := got.Store
	if f := floorIn(t, store, logen); f == nil || f.CreatedAt != newer.CreatedAt || f.ID != newer.ID || len(f.Boxes) != 1 {
		t.Fatalf("floor after opening the newer list: %+v", f)
	}

	// The box then offers the older one.
	got = unlockPage(t, logen, pageVisit{st: boxState(b, older), store: store, now: clock})
	if a := addresses(got.List.Lists); len(a) == 1 && a[0] == "https://lists.example/old.txt" {
		t.Fatalf("page opened the older list for editing: %q", got.Msg)
	}
	for _, x := range got.List.Boxes {
		if x.PubKey == removed.PubKey {
			t.Fatal("the removed box is back in the editor")
		}
	}
	if !strings.Contains(got.Kind, "err") || !strings.Contains(got.Msg, "older than") {
		t.Fatalf("no loud warning: %q (%s)", got.Msg, got.Kind)
	}
	for k, v := range got.Store {
		if v != store[k] {
			t.Fatal("the older list lowered the floor")
		}
	}

	// Older than nothing: a floor below the offered list lets it open.
	got = unlockPage(t, logen, pageVisit{st: boxState(b, newer), now: clock})
	if !got.Editor || strings.Contains(got.Kind, "err") {
		t.Fatalf("newer list with no floor: %+v", got)
	}
}

// An owner-signed list dated far ahead (boxes refuse it) is flagged, does not
// move the floor, and does not push the next save's date past what boxes take.
func TestPageFlagsListDatedAhead(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	logen := mustKey(t)
	b := newBox(t, pubHex(logen), newFakeRelays(), &clock)
	ahead := signed(t, logen, &settings{V: 1, Boxes: []box{b.asBox("pi")}, Lists: blocklist("https://lists.example/ahead.txt")}, clock.Unix()+365*24*3600)

	got := unlockPage(t, logen, pageVisit{st: boxState(b, ahead), now: clock, save: true})
	if !strings.Contains(got.Msg, "ahead of this computer's clock") {
		t.Errorf("no warning: %q", got.Msg)
	}
	if a := addresses(got.List.Lists); len(a) != 0 {
		t.Errorf("editor opened the list dated ahead: %v", a)
	}
	if f := floorIn(t, got.Store, logen); f != nil && f.CreatedAt > clock.Unix()+600 {
		t.Errorf("floor moved to %d", f.CreatedAt)
	}
	if got.Published == nil || got.Published.Event.CreatedAt > clock.Unix()+600 {
		t.Errorf("save dated %+v, want within the boxes' skew", got.Published)
	}
}
