package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A well-formed npub: "npub1" and 58 bech32 characters. The hub only checks
// the shape; the panel decodes it.
const (
	firstBoxNpub  = "npub162ve8d27trvv44cgxrmzeusa2ejdtwjc7wqdkmx3q9hn5g2slmrsae4kdp"
	secondBoxNpub = "npub16wtf93pgldp4hru0fkqayuydm7ge36tvxc2f3z6y7tet5nwfcq4s3rhveh"
)

func postTo(mux http.Handler, path string, form url.Values, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
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

func getWithRole(t *testing.T, mux http.Handler, path, role string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if role != "" {
		req.AddCookie(&http.Cookie{Name: roleCookie, Value: role})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestParseJoinCode(t *testing.T) {
	for _, in := range []string{
		firstBoxNpub + ":a0454be9",
		"  " + firstBoxNpub + ":a0454be9\n",
		firstBoxNpub + " a0454be9",
		firstBoxNpub + "\na0454be9",
		strings.ToUpper(firstBoxNpub[:5]) + firstBoxNpub[5:] + ":a0454be9",
	} {
		npub, id, err := parseJoinCode(in)
		if err != nil || npub != firstBoxNpub || id != "a0454be9" {
			t.Errorf("parseJoinCode(%q) = %q %q %v", in, npub, id, err)
		}
	}
	for _, in := range []string{
		"",
		firstBoxNpub,                     // no network ID
		"a0454be9:" + firstBoxNpub,       // swapped
		firstBoxNpub + ":a0454be9:extra", // three parts
		firstBoxNpub[:40] + ":a0454be9",  // truncated npub
		firstBoxNpub + ":a04<script>",    // not an ID
		"nvpn://join-request/abc",        // a phone's join link pasted in the wrong place
	} {
		if _, _, err := parseJoinCode(in); err == nil {
			t.Errorf("parseJoinCode(%q) accepted", in)
		}
	}
}

// The facts decide the role whenever they can, whatever the reader picked: a
// box with devices on its own network must never be walked into joining
// another one, and a box that already asked to join is a second box.
func TestBoxRoleFactsBeatThePick(t *testing.T) {
	admin := &tickNetwork{LocalIsAdmin: true}
	member := &tickNetwork{LocalIsAdmin: false}
	cases := []struct {
		name   string
		p      page
		picked string
		want   string
	}{
		{"fresh box, nothing picked", page{network: admin}, "", ""},
		{"fresh box, picked first", page{network: admin}, "first", "first"},
		{"fresh box, picked second", page{network: admin}, "second", "second"},
		{"fresh box, junk cookie", page{network: admin}, "admin", ""},
		{"devices on it, picked second", page{network: admin, Devices: []device{{Npub: "npub1x"}}}, "second", "first"},
		{"a request waiting, picked second", page{network: admin, PendingJoins: 1}, "second", "first"},
		{"asked to join, picked first", page{network: member, Devices: []device{{Npub: firstBoxNpub}}}, "first", "second"},
		{"panel down, picked second", page{}, "second", "second"},
	}
	for _, c := range cases {
		if got := boxRole(c.p, c.picked); got != c.want {
			t.Errorf("%s: boxRole = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWizardAsksWhichBox(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)

	body := get(t, mux, "/setup?step=2").Body.String()
	if !strings.Contains(body, `href="/setup?role=first"`) || !strings.Contains(body, `href="/setup?role=second"`) {
		t.Fatalf("step 2 on a fresh box does not ask which box it is:\n%s", body)
	}
	if strings.Contains(body, `class="done"><span class="n">Step 2`) {
		t.Fatalf("step 2 shows as done before anything was picked")
	}

	rec := get(t, mux, "/setup?role=second")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?step=3" {
		t.Fatalf("picking: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	ck := rec.Result().Cookies()
	if len(ck) != 1 || ck[0].Name != roleCookie || ck[0].Value != "second" || !ck[0].HttpOnly {
		t.Fatalf("picking set cookies %+v", ck)
	}
	if len(f.writes) != 0 {
		t.Fatalf("picking a role reached the panel")
	}

	second := getWithRole(t, mux, "/setup?step=3", "second")
	if !strings.Contains(second, `action="/setup/join"`) || !strings.Contains(second, "Join your first box") || strings.Contains(second, `action="/devices/add"`) {
		t.Fatalf("a second box's step 3 is not the join form:\n%s", second)
	}
	if !strings.Contains(second, "of 4</title>") {
		t.Fatalf("a second box's wizard is not 4 steps")
	}

	first := getWithRole(t, mux, "/setup?step=3&kind=box", "first")
	if !strings.Contains(first, `action="/devices/add-box"`) || !strings.Contains(first, "npub1box:9683dbda") || !strings.Contains(first, "of 5</title>") {
		t.Fatalf("a first box's step 3 (DNS box) has no join code and add-box form:\n%s", first)
	}
	if phone := getWithRole(t, mux, "/setup?step=3", "first"); !strings.Contains(phone, `action="/devices/add"`) {
		t.Fatalf("a first box's step 3 defaults to the phone join link")
	}
}

func TestSecondBoxJoins(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)

	code := firstBoxNpub + ":a0454be9"
	rec := postTo(mux, "/setup/join", url.Values{"csrf": {cfg.csrfToken}, "code": {code}}, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?step=4" {
		t.Fatalf("join: %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(f.writes) != 1 || f.writes[0].URL.Path != "/api/manual_add_network" ||
		f.bodies[0]["adminNpub"] != firstBoxNpub || f.bodies[0]["meshNetworkId"] != "a0454be9" || len(f.bodies[0]) != 2 {
		t.Fatalf("panel got %d writes, bodies %v", len(f.writes), f.bodies)
	}
	if v := f.writes[0].Header.Get("X-Forwarded-For"); v != "" {
		t.Errorf("browser header forwarded to the panel: %q", v)
	}

	// Waiting for the first box: no cookie needed any more, the facts say
	// second box. "/" sends it back to setup, which shows this box's ID.
	if rec := get(t, mux, "/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("/ while waiting: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// (Step 1 never reads done here: the fake has no Unbound.)
	wait := get(t, mux, "/setup?step=4").Body.String()
	if !strings.Contains(wait, "Let the first box add it") || !strings.Contains(wait, "npub1box") || strings.Contains(wait, "You're set") {
		t.Fatalf("waiting step does not show this box's ID:\n%s", wait)
	}
	if !strings.Contains(wait, `data-qr="nvpn://device/npub1box"`) || !strings.Contains(wait, `src="/assets/qrcode.js"`) {
		t.Fatalf("waiting step does not show this box's ID as a QR code:\n%s", wait)
	}

	// The first box adds it.
	f.admitted = true
	done := get(t, mux, "/setup?step=4").Body.String()
	if !strings.Contains(done, "You're set") {
		t.Fatalf("admitted box not reported done:\n%s", done)
	}
	if rec := get(t, mux, "/"); rec.Code != http.StatusOK {
		t.Fatalf("/ once admitted: %d, want the dashboard", rec.Code)
	}

	// Pasting a code again changes nothing.
	rec = postTo(mux, "/setup/join", url.Values{"csrf": {cfg.csrfToken}, "code": {code}}, nil)
	if rec.Code != http.StatusConflict || len(f.writes) != 1 {
		t.Fatalf("second join: %d, %d writes", rec.Code, len(f.writes))
	}
}

func TestSecondBoxJoinRefusals(t *testing.T) {
	code := firstBoxNpub + ":a0454be9"
	cases := []struct {
		name   string
		f      *fakePanel
		form   url.Values
		mutate func(*http.Request)
		status int
		says   string
	}{
		{"devices already on this box", &fakePanel{}, url.Values{"code": {code}}, nil, http.StatusConflict, "cut them off"},
		{"its own code", &fakePanel{alone: true, selfNpub: firstBoxNpub}, url.Values{"code": {firstBoxNpub + ":9683dbda"}}, nil, http.StatusBadRequest, "own join code"},
		{"its own code, two networks and none marked running", &fakePanel{alone: true, selfNpub: firstBoxNpub, decoyFirst: true, noActive: true}, url.Values{"code": {firstBoxNpub + ":9683dbda"}}, nil, http.StatusBadRequest, "own join code"},
		{"a bad code", &fakePanel{alone: true}, url.Values{"code": {"hello"}}, nil, http.StatusBadRequest, "a join code"},
		{"no token", &fakePanel{alone: true}, url.Values{"csrf": {"nope"}, "code": {code}}, nil, http.StatusForbidden, ""},
		{"another site", &fakePanel{alone: true}, url.Values{"code": {code}}, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, http.StatusForbidden, ""},
		{"a rebinding name", &fakePanel{alone: true}, url.Values{"code": {code}}, func(r *http.Request) { r.Host = "evil.example:8080"; r.Header.Del("Origin") }, http.StatusForbidden, ""},
	}
	for _, c := range cases {
		cfg := panelConfig(t, c.f)
		mux := newMux(cfg)
		if c.form.Get("csrf") == "" {
			c.form.Set("csrf", cfg.csrfToken)
		}
		rec := postTo(mux, "/setup/join", c.form, c.mutate)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("%s: %d, want %d with %q\n%s", c.name, rec.Code, c.status, c.says, rec.Body.String())
		}
		if len(c.f.writes) != 0 {
			t.Errorf("%s: reached the panel", c.name)
		}
	}
	if rec := get(t, newMux(panelConfig(t, &fakePanel{alone: true})), "/setup/join"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /setup/join: %d", rec.Code)
	}
}

// A panel that takes the code but doesn't run the network has not joined
// anything, and the page must not say it has.
func TestSecondBoxJoinReportsUnappliedJoin(t *testing.T) {
	f := &fakePanel{alone: true, dropManual: true}
	cfg := panelConfig(t, f)
	rec := postTo(newMux(cfg), "/setup/join", url.Values{"csrf": {cfg.csrfToken}, "code": {firstBoxNpub + ":a0454be9"}}, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "isn&#39;t running that network") {
		t.Fatalf("unapplied join: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestFirstBoxAddsSecondBox(t *testing.T) {
	f := &fakePanel{override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)

	if body := get(t, mux, "/dashboard").Body.String(); !strings.Contains(body, `action="/devices/add-box"`) || !strings.Contains(body, "npub1box:9683dbda") {
		t.Fatalf("dashboard has no add-box card with the join code")
	}
	// A pasted or scanned ID only reviews: it names the box and sends nothing.
	form := url.Values{"csrf": {cfg.csrfToken}, "box": {" " + deviceQRPrefix + secondBoxNpub + "\n"}, "name": {"Pi"}, "from": {"setup"}}
	rec := postTo(mux, "/devices/add-box", form, nil)
	review := rec.Body.String()
	if rec.Code != http.StatusOK || len(f.writes) != 0 {
		t.Fatalf("review: %d, %d writes\n%s", rec.Code, len(f.writes), review)
	}
	for _, want := range []string{secondBoxNpub, `name="box" value="` + secondBoxNpub + `"`, `name="confirm" value="yes"`,
		`name="name" value="Pi"`, `name="from" value="setup"`, cfg.csrfToken, `href="/setup?step=3"`} {
		if !strings.Contains(review, want) {
			t.Fatalf("review page lacks %q:\n%s", want, review)
		}
	}
	form.Set("confirm", "yes")
	rec = postTo(mux, "/devices/add-box", form, func(r *http.Request) {
		r.Header.Set("Cookie", "session=abc")
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?step=3" {
		t.Fatalf("add box: %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(f.writes) != 1 || f.writes[0].URL.Path != "/api/add_participant" ||
		f.bodies[0]["networkId"] != "network-1" || f.bodies[0]["npub"] != secondBoxNpub || f.bodies[0]["alias"] != "Pi" {
		t.Fatalf("panel got %d writes, bodies %v", len(f.writes), f.bodies)
	}
	if v := f.writes[0].Header.Get("Cookie"); v != "" {
		t.Errorf("browser cookie forwarded to the panel: %q", v)
	}
	// Again: already there, nothing sent.
	rec = postTo(mux, "/devices/add-box", url.Values{"csrf": {cfg.csrfToken}, "box": {secondBoxNpub}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusSeeOther || len(f.writes) != 1 {
		t.Fatalf("adding the same box twice: %d, %d writes", rec.Code, len(f.writes))
	}
}

func TestAddBoxRefusals(t *testing.T) {
	cases := []struct {
		name   string
		f      *fakePanel
		box    string
		status int
	}{
		{"not an admin", &fakePanel{notAdmin: true}, secondBoxNpub, http.StatusConflict},
		{"its own ID", &fakePanel{selfNpub: secondBoxNpub}, secondBoxNpub, http.StatusBadRequest},
		{"not an ID", &fakePanel{}, "npub1box", http.StatusBadRequest},
		{"a join code instead of an ID", &fakePanel{}, firstBoxNpub + ":a0454be9", http.StatusBadRequest},
		{"a phone link", &fakePanel{}, "nvpn://join-request/abc", http.StatusBadRequest},
		{"its own ID from a QR code", &fakePanel{selfNpub: secondBoxNpub}, deviceQRPrefix + secondBoxNpub, http.StatusBadRequest},
	}
	for _, c := range cases {
		cfg := panelConfig(t, c.f)
		rec := postTo(newMux(cfg), "/devices/add-box", url.Values{"csrf": {cfg.csrfToken}, "box": {c.box}, "confirm": {"yes"}}, nil)
		if rec.Code != c.status || len(c.f.writes) != 0 {
			t.Errorf("%s: %d (want %d), %d writes", c.name, rec.Code, c.status, len(c.f.writes))
		}
	}

	f := &fakePanel{dropBox: true}
	cfg := panelConfig(t, f)
	rec := postTo(newMux(cfg), "/devices/add-box", url.Values{"csrf": {cfg.csrfToken}, "box": {secondBoxNpub}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "doesn&#39;t list the box") {
		t.Fatalf("unapplied add: %d\n%s", rec.Code, rec.Body.String())
	}
}

// Only a box that runs its own network has a join code to hand out. A second
// box showing one would send a third box to ask a network it can't approve.
func TestJoinCodeOnlyOnAdmin(t *testing.T) {
	ctx := httptest.NewRequest(http.MethodGet, "/", nil).Context()
	if p := panelConfig(t, &fakePanel{}).buildPage(ctx); p.JoinCode != "npub1box:9683dbda" {
		t.Fatalf("admin box join code %q", p.JoinCode)
	}
	if p := panelConfig(t, &fakePanel{notAdmin: true}).buildPage(ctx); p.JoinCode != "" {
		t.Fatalf("member box shows a join code %q", p.JoinCode)
	}
	p := panelConfig(t, &fakePanel{alone: true, joinedTo: "a0454be9", joinAdmin: firstBoxNpub}).buildPage(ctx)
	if p.JoinCode != "" || !p.Member || p.Admitted || p.AdminNpub != firstBoxNpub {
		t.Fatalf("waiting second box: code %q member %t admitted %t admin %q", p.JoinCode, p.Member, p.Admitted, p.AdminNpub)
	}
}

func TestParseBoxIDForms(t *testing.T) {
	for _, in := range []string{
		secondBoxNpub,
		" " + secondBoxNpub + "\n",
		strings.ToUpper(secondBoxNpub),
		deviceQRPrefix + secondBoxNpub,
		"NVPN://Device/" + secondBoxNpub,
		deviceQRPrefix + secondBoxNpub + "?name=Pi",
		deviceQRPrefix + secondBoxNpub + "#x",
		"nostr:" + secondBoxNpub,
	} {
		if got, err := parseBoxID(in); err != nil || got != secondBoxNpub {
			t.Errorf("parseBoxID(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{
		"",
		deviceQRPrefix,
		"nvpn://join-request/" + secondBoxNpub,
		"nvpn://devices/" + secondBoxNpub,
		"https://example.com/device/" + secondBoxNpub,
		deviceQRPrefix + "x/" + secondBoxNpub,
		firstBoxNpub + ":a0454be9",
		"nostr:" + deviceQRPrefix + secondBoxNpub,
	} {
		if got, err := parseBoxID(in); err == nil {
			t.Errorf("parseBoxID(%q) accepted %q", in, got)
		}
	}
}
