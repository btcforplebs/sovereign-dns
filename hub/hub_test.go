package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// deadConfig is a hub whose every probe points at a port nothing listens on,
// which is what the hub sees when the control panel container is down or
// still building. The tick fails with a real dial error, not a canned one.
func deadConfig(t *testing.T) config {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	cfg := loadConfig()
	cfg.panelAPI = "http://" + addr
	cfg.piholeAddr = addr
	cfg.docsAddr = addr
	cfg.unboundAddr = addr
	return cfg
}

func get(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// A panel that cannot be reached says nothing about whether devices have
// joined, so "/" must keep serving the dashboard rather than deciding the box
// is brand new and bouncing an established install into the wizard.
func TestPanelDownKeepsDashboard(t *testing.T) {
	rec := get(t, newMux(deadConfig(t)), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / with the panel down: status %d, want 200 (Location %q)", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Body.String(), "reach the VPN control panel") {
		t.Fatalf("dashboard rendered without the panel error:\n%s", rec.Body.String())
	}
}

// With the panel down, step 1 must show the real cause and must not claim the
// stack is merely still starting: on a box that was working, that advice is
// wrong and tells the reader to wait for something that will never happen.
func TestWizardShowsPanelError(t *testing.T) {
	body := get(t, newMux(deadConfig(t)), "/setup").Body.String()
	if !strings.Contains(body, "connection refused") {
		t.Fatalf("step 1 does not show the dial error:\n%s", body)
	}
	if strings.Contains(body, "Still starting") {
		t.Fatalf("step 1 still gives the \"still starting\" advice with the panel down:\n%s", body)
	}
}

// Steps 2 to 4 are reachable by URL with the panel down. Each must carry the
// error as a one-line banner, or a reader who typed ?step=3 sees an empty
// join list with no reason given.
func TestWizardLaterStepsCarryPanelError(t *testing.T) {
	mux := newMux(deadConfig(t))
	for step := 2; step <= wizardSteps; step++ {
		body := get(t, mux, fmt.Sprintf("/setup?step=%d", step)).Body.String()
		if !strings.Contains(body, `<div class="err">`) || !strings.Contains(body, "connection refused") {
			t.Errorf("step %d does not show the panel error banner", step)
		}
	}
}

// Step 4 only reloads when the key changes, and the Pi-hole link is built from
// the mesh IP, which can arrive after Pi-hole itself is already up.
func TestStateKeyTracksPiholeURL(t *testing.T) {
	before := page{PiholeUp: true}
	after := before
	after.PiholeURL = "http://10.44.5.1/admin"
	if stateKey(before) == stateKey(after) {
		t.Fatalf("state key %q ignores PiholeURL", stateKey(before))
	}
}

// fakePanel stands in for nvpn-web: it serves a tick with one network and
// applies /api/set_network_dns the way the real route does, recording every
// write so a test can prove one did or did not happen.
type fakePanel struct {
	override bool
	ignore   bool // accept writes without applying them
	writes   []*http.Request
	bodies   []map[string]any
}

func (f *fakePanel) handler() http.Handler {
	mux := http.NewServeMux()
	state := func() map[string]any {
		return map[string]any{
			"daemonRunning": true,
			"ownNpub":       "npub1box",
			"networkId":     "9683dbda",
			"networks": []map[string]any{{
				"id": "network-1", "name": "home", "networkId": "9683dbda",
				"localIsAdmin": true, "dnsNodeNpub": "npub1box",
				"dnsOverride": f.override, "localIsDnsNode": true,
				"participants": []map[string]any{{"alias": "laptop", "state": "online", "reachable": true}},
			}},
		}
	}
	mux.HandleFunc("/api/tick", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(state())
	})
	mux.HandleFunc("/api/set_network_dns", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.writes = append(f.writes, r)
		f.bodies = append(f.bodies, body)
		if !f.ignore {
			f.override = body["dnsOverride"] == true
		}
		json.NewEncoder(w).Encode(state())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {}) // health, Pi-hole login
	return mux
}

func panelConfig(t *testing.T, f *fakePanel) config {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	cfg := deadConfig(t)
	cfg.panelAPI = srv.URL
	cfg.piholeAddr = strings.TrimPrefix(srv.URL, "http://")
	return cfg
}

func post(mux http.Handler, form url.Values, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/dns", strings.NewReader(form.Encode()))
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

func TestDNSSwitchTurnsOffOnlyAfterConfirmation(t *testing.T) {
	f := &fakePanel{override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)

	if body := get(t, mux, "/dashboard").Body.String(); !strings.Contains(body, `<span class="pill on">on</span>`) {
		t.Fatalf("dashboard does not show the override as on:\n%s", body)
	}

	rec := post(mux, url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard?confirm=dns-off#dns" {
		t.Fatalf("unconfirmed off: %d %q, want 303 to the confirmation", rec.Code, rec.Header().Get("Location"))
	}
	if len(f.writes) != 0 {
		t.Fatalf("unconfirmed off reached the panel")
	}
	if body := get(t, mux, "/dashboard?confirm=dns-off").Body.String(); !strings.Contains(body, "Turn off DNS for the whole mesh?") {
		t.Fatalf("confirmation not rendered:\n%s", body)
	}

	rec = post(mux, url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}, "confirm": {"yes"}}, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("Cookie", "session=abc")
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard#dns" {
		t.Fatalf("confirmed off: %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(f.writes) != 1 || f.override {
		t.Fatalf("panel writes %d, override %t; want 1 write, override off", len(f.writes), f.override)
	}
	want := map[string]any{"networkId": "network-1", "npub": "npub1box", "dnsOverride": false}
	for k, v := range want {
		if f.bodies[0][k] != v {
			t.Errorf("panel body %s = %v, want %v", k, f.bodies[0][k], v)
		}
	}
	for _, h := range []string{"X-Forwarded-For", "Cookie", "Origin"} {
		if v := f.writes[0].Header.Get(h); v != "" {
			t.Errorf("browser header %s forwarded to the panel: %q", h, v)
		}
	}
	if body := get(t, mux, "/dashboard").Body.String(); !strings.Contains(body, `<span class="pill off">off</span>`) {
		t.Fatalf("dashboard does not show the override as off after the switch")
	}

	// Back on. Unbound is unreachable in this config, so on also needs a
	// confirmation: the override fails closed with no resolver behind it.
	rec = post(mux, url.Values{"csrf": {cfg.csrfToken}, "override": {"on"}}, nil)
	if rec.Header().Get("Location") != "/dashboard?confirm=dns-on#dns" || len(f.writes) != 1 {
		t.Fatalf("on with the resolver down did not ask first: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = post(mux, url.Values{"csrf": {cfg.csrfToken}, "override": {"on"}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusSeeOther || !f.override {
		t.Fatalf("confirmed on: %d, override %t", rec.Code, f.override)
	}
}

func TestDNSSwitchRejectsForeignRequests(t *testing.T) {
	f := &fakePanel{override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)
	ok := url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}, "confirm": {"yes"}}

	if rec := get(t, mux, "/dns?override=off&confirm=yes&csrf="+cfg.csrfToken); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /dns: %d, want 405", rec.Code)
	}
	cases := map[string]struct {
		form   url.Values
		mutate func(*http.Request)
	}{
		"no token":       {url.Values{"override": {"off"}, "confirm": {"yes"}}, nil},
		"wrong token":    {url.Values{"csrf": {"00"}, "override": {"off"}, "confirm": {"yes"}}, nil},
		"cross-site":     {ok, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }},
		"null origin":    {ok, func(r *http.Request) { r.Header.Set("Origin", "null") }},
		"same-site port": {ok, func(r *http.Request) { r.Header.Set("Origin", "http://localhost:8082") }},
		"fetch metadata": {ok, func(r *http.Request) { r.Header.Del("Origin"); r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		"rebinding host": {ok, func(r *http.Request) {
			r.Host = "evil.example:8080"
			r.Header.Set("Origin", "http://evil.example:8080")
		}},
	}
	for name, c := range cases {
		if rec := post(mux, c.form, c.mutate); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}
	if len(f.writes) != 0 || !f.override {
		t.Fatalf("a rejected request reached the panel (%d writes)", len(f.writes))
	}
	// Positive control: the same form from this page's own origin goes through.
	if rec := post(mux, ok, nil); rec.Code != http.StatusSeeOther || len(f.writes) != 1 {
		t.Fatalf("legitimate request: %d, %d writes", rec.Code, len(f.writes))
	}
}

// With the panel down the setting is unknown. It must not read as "off", and
// the switch must refuse rather than guess.
func TestDNSSwitchUnknownWhenPanelDown(t *testing.T) {
	cfg := deadConfig(t)
	mux := newMux(cfg)
	body := get(t, mux, "/dashboard").Body.String()
	if !strings.Contains(body, `<span class="pill unknown">unknown</span>`) || strings.Contains(body, `<span class="pill off">`) {
		t.Fatalf("panel down: override not shown as unknown:\n%s", body)
	}
	if strings.Contains(body, `action="/dns"`) {
		t.Fatalf("panel down: the switch is still offered")
	}
	rec := post(mux, url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Nothing was changed") {
		t.Fatalf("panel down: POST %d, want 409 with nothing changed", rec.Code)
	}
}

// A panel that accepts the write but does not apply it must surface as a
// failure, not a silent success.
func TestDNSSwitchReportsUnappliedChange(t *testing.T) {
	f := &fakePanel{override: true, ignore: true}
	cfg := panelConfig(t, f)
	rec := post(newMux(cfg), url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "still reports the override on") {
		t.Fatalf("unapplied change: %d\n%s", rec.Code, rec.Body.String())
	}
}

// The token authorises changes, so it must not leak through the JSON status.
func TestStatusJSONOmitsCSRFToken(t *testing.T) {
	cfg := panelConfig(t, &fakePanel{override: true})
	if body := get(t, newMux(cfg), "/api/status").Body.String(); strings.Contains(body, cfg.csrfToken) {
		t.Fatalf("/api/status leaks the CSRF token")
	}
}
