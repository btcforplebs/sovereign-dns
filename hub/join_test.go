package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const phoneNpub = "npub1uyp9vdtya03pk6qqywx0h76tlcaf38t3ju9hls3f4rufj26skzus6gkucu"

// makeLink encodes a join link the way nostr-identity does: unpadded
// base64url JSON after the prefix.
func makeLink(t *testing.T, fields map[string]string) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return joinLinkPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func phoneLink(t *testing.T) string {
	return makeLink(t, map[string]string{
		"deviceAppKeyNpub": phoneNpub, "requestNpub": "npub1request", "requestSecret": "s3cret", "label": "Logen's iPhone",
	})
}

func postAdd(mux http.Handler, form url.Values, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/devices/add", strings.NewReader(form.Encode()))
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://localhost:8080")
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestParseJoinLink(t *testing.T) {
	good := phoneLink(t)
	link, j, err := parseJoinLink("  \n" + strings.Replace(good, "nvpn://join-request/", "NVPN://Join-Request/", 1) + "\n")
	if err != nil || j.DeviceNpub != phoneNpub || j.Label != "Logen's iPhone" || link != good {
		t.Fatalf("good link: %q %+v %v", link, j, err)
	}
	bad := map[string]string{
		"empty":         "",
		"other scheme":  "https://example.com/join-request/abc",
		"other nvpn":    "nvpn://join/abc",
		"not base64":    joinLinkPrefix + "%%%",
		"not json":      joinLinkPrefix + base64.RawURLEncoding.EncodeToString([]byte("hello")),
		"unknown field": makeLink(t, map[string]string{"deviceAppKeyNpub": phoneNpub, "requestSecret": "s", "admin": "x"}),
		"no secret":     makeLink(t, map[string]string{"deviceAppKeyNpub": phoneNpub, "requestNpub": "npub1r"}),
		"no npub":       makeLink(t, map[string]string{"deviceAppKeyNpub": "abc", "requestSecret": "s"}),
		// The review page tells the reader to compare this line with the
		// device, so it must be exactly an npub and nothing a QR code adds.
		"npub plus text": makeLink(t, map[string]string{"deviceAppKeyNpub": phoneNpub + " \u202e(verified)", "requestSecret": "s"}),
		"short npub":     makeLink(t, map[string]string{"deviceAppKeyNpub": "npub1qqqq", "requestSecret": "s"}),
		"upper npub":     makeLink(t, map[string]string{"deviceAppKeyNpub": strings.ToUpper(phoneNpub), "requestSecret": "s"}),
		"too long":       joinLinkPrefix + strings.Repeat("A", joinLinkMaxLen),
		// A look-alike rune in the prefix (U+017F, long s) is refused.
		"long s prefix": strings.Replace(phoneLink(t), "request", "reque\u017ft", 1),
	}
	for name, v := range bad {
		if _, _, err := parseJoinLink(v); err == nil {
			t.Errorf("%s: accepted %q", name, v)
		}
	}

	// U+0130 (dotted capital I) lowercases to a plain "i" one byte shorter,
	// so lowercasing the whole link would match the prefix and then cut the
	// payload one byte early. It has to be refused as not a join link, at the
	// prefix, rather than fail later as a damaged payload.
	dotted := strings.Replace(phoneLink(t), "join", "jo\u0130n", 1)
	if _, _, err := parseJoinLink(dotted); err == nil || !strings.Contains(err.Error(), "isn't a Nostr VPN join link") {
		t.Errorf("dotted capital I prefix: got %v, want the not-a-join-link error", err)
	}
}

// Pasting only shows the device. The panel hears nothing until Add is
// pressed on the review page, and then gets exactly the link and nothing
// from the browser's request.
func TestAddDeviceOnlyAfterReview(t *testing.T) {
	f := &fakePanel{override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)
	link := phoneLink(t)

	if body := get(t, mux, "/dashboard").Body.String(); !strings.Contains(body, `action="/devices/add"`) || !strings.Contains(body, cfg.csrfToken) {
		t.Fatalf("dashboard has no add-device form with the token")
	}

	rec := postAdd(mux, url.Values{"csrf": {cfg.csrfToken}, "request": {link}}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), phoneNpub) || !strings.Contains(rec.Body.String(), "Logen&#39;s iPhone") {
		t.Fatalf("review: %d\n%s", rec.Code, rec.Body.String())
	}
	if len(f.writes) != 0 {
		t.Fatalf("the review step reached the panel")
	}

	rec = postAdd(mux, url.Values{"csrf": {cfg.csrfToken}, "request": {link}, "confirm": {"yes"}}, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("Cookie", "session=abc")
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard#add-device" {
		t.Fatalf("add: %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(f.writes) != 1 || f.bodies[0]["request"] != link || len(f.bodies[0]) != 1 {
		t.Fatalf("panel got %d writes, body %v", len(f.writes), f.bodies)
	}
	for _, h := range []string{"X-Forwarded-For", "Cookie", "Origin"} {
		if v := f.writes[0].Header.Get(h); v != "" {
			t.Errorf("browser header %s forwarded to the panel: %q", h, v)
		}
	}

	// Pasted again from the wizard: the page says it's already on the mesh,
	// offers no Add, and Done leads back to the wizard's approval step.
	rec = postAdd(mux, url.Values{"csrf": {cfg.csrfToken}, "request": {link}, "from": {"setup"}}, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "already on your mesh") ||
		strings.Contains(body, `name="confirm"`) || !strings.Contains(body, `href="/setup?step=3"`) {
		t.Fatalf("already-added device from the wizard: %d\n%s", rec.Code, body)
	}
	if len(f.writes) != 1 {
		t.Fatalf("a device already on the network was imported again")
	}
}

func TestAddDeviceRejectsForeignRequests(t *testing.T) {
	f := &fakePanel{}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)
	ok := url.Values{"csrf": {cfg.csrfToken}, "request": {phoneLink(t)}, "confirm": {"yes"}}

	if rec := get(t, mux, "/devices/add"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
	cases := map[string]func(*http.Request){
		"rebinding host": func(r *http.Request) { r.Host = "evil.example:8080" },
		"other origin":   func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"null origin":    func(r *http.Request) { r.Header.Set("Origin", "null") },
		"cross-site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	}
	for name, mutate := range cases {
		if rec := postAdd(mux, ok, mutate); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}
	bad := url.Values{"csrf": {"wrong"}, "request": ok["request"], "confirm": {"yes"}}
	if rec := postAdd(mux, bad, nil); rec.Code != http.StatusForbidden {
		t.Errorf("bad token: %d", rec.Code)
	}
	if len(f.writes) != 0 {
		t.Fatalf("a rejected request reached the panel")
	}
}

func TestAddDeviceRefusedWhenNotAdmin(t *testing.T) {
	f := &fakePanel{notAdmin: true}
	cfg := panelConfig(t, f)
	rec := postAdd(newMux(cfg), url.Values{"csrf": {cfg.csrfToken}, "request": {phoneLink(t)}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "isn&#39;t one") || len(f.writes) != 0 {
		t.Fatalf("non-admin box: %d, %d writes\n%s", rec.Code, len(f.writes), rec.Body.String())
	}
}

func TestAddDeviceReportsUnappliedImport(t *testing.T) {
	f := &fakePanel{dropJoin: true}
	cfg := panelConfig(t, f)
	rec := postAdd(newMux(cfg), url.Values{"csrf": {cfg.csrfToken}, "request": {phoneLink(t)}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "doesn&#39;t list the device") {
		t.Fatalf("unapplied import: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestAddDeviceBadLinkChangesNothing(t *testing.T) {
	f := &fakePanel{}
	cfg := panelConfig(t, f)
	rec := postAdd(newMux(cfg), url.Values{"csrf": {cfg.csrfToken}, "request": {"nvpn://join/abc"}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "isn&#39;t a Nostr VPN join link") || len(f.writes) != 0 {
		t.Fatalf("bad link: %d, %d writes\n%s", rec.Code, len(f.writes), rec.Body.String())
	}
}

func TestWizardStepTwoHasAddForm(t *testing.T) {
	f := &fakePanel{}
	cfg := panelConfig(t, f)
	body := get(t, newMux(cfg), "/setup?step=3").Body.String()
	if !strings.Contains(body, `action="/devices/add"`) || !strings.Contains(body, cfg.csrfToken) || !strings.Contains(body, `name="from" value="setup"`) {
		t.Fatalf("step 3 has no add-device form returning to the wizard:\n%s", body)
	}
}

// In the daemon's first seconds the box's own roster entry is not yet marked
// "local". It must still not count as a joined device, or a fresh box's wizard
// skips straight past "Add your first device".
func TestOwnEntryIsNotADevice(t *testing.T) {
	f := &fakePanel{selfEarly: true}
	cfg := panelConfig(t, f)
	p := cfg.buildPage(httptest.NewRequest(http.MethodGet, "/", nil).Context())
	for _, d := range p.Devices {
		if d.Npub == "npub1box" {
			t.Fatalf("the box's own entry is listed as a device: %+v", p.Devices)
		}
	}
}

// The label is the QR code's author's words on the approval page: no bidi
// overrides, zero-width or control characters, and short.
func TestJoinLinkLabelIsCleaned(t *testing.T) {
	for in, want := range map[string]string{
		"Logen's iPhone":        "Logen's iPhone",
		"  Moto \n\t g  ":       "Moto g",
		"evil\u202e enohPi":     "evil enohPi",
		"zero\u200bwidth\ufeff": "zerowidth",
		"bell\x07":              "bell",
		strings.Repeat("é", 60): strings.Repeat("é", joinLabelMaxLen),
		"Logen's iPhone — already approved by the admin, just press Add": "Logen's iPhone — already approved by the",
	} {
		_, j, err := parseJoinLink(makeLink(t, map[string]string{"deviceAppKeyNpub": phoneNpub, "requestSecret": "s", "label": in}))
		if err != nil || j.Label != want {
			t.Errorf("label %q: got %q %v, want %q", in, j.Label, err, want)
		}
	}
}
