package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct horse"

// lanConfig is a hub set up the way `scripts/hub-lan.sh on` leaves it.
func lanConfig(t *testing.T) config {
	t.Helper()
	cfg := deadConfig(t)
	a, err := newAuth("0.0.0.0", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cfg.auth = a
	cfg.lanPanelPort = lanPanelPort(a)
	return cfg
}

// req builds a request from a home-network address to the hub's LAN name,
// which is what a laptop on the same Wi-Fi sends.
func req(method, path, from string, body url.Values) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr = net.JoinHostPort(from, "51000")
	r.Host = "192.168.4.20:8080"
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func signIn(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := serve(h, req(http.MethodPost, "/login", "192.168.4.30", url.Values{"password": {testPassword}, "next": {"/dashboard"}}))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard" {
		t.Fatalf("sign in: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie is not HttpOnly + SameSite=Strict: %+v", c)
			}
			return c
		}
	}
	t.Fatal("sign in set no session cookie")
	return nil
}

// On the network with no password is the one setup the hub must never serve.
func TestNetworkBindNeedsPassword(t *testing.T) {
	for _, bind := range []string{"0.0.0.0", "192.168.4.20", "::"} {
		if _, err := newAuth(bind, ""); err == nil {
			t.Errorf("HUB_BIND=%s with no password started", bind)
		}
	}
	if _, err := newAuth("0.0.0.0", "short"); err == nil {
		t.Error("a 5-character password was accepted")
	}
	a, err := newAuth("127.0.0.1", "")
	if err != nil || a.enabled() {
		t.Fatalf("default loopback install: err %v, login %v; want no login, as before", err, a.enabled())
	}
}

// Every route, the JSON one and the ones that change things included, is
// behind the login.
func TestEverythingNeedsSignIn(t *testing.T) {
	h := newMux(lanConfig(t))
	rec := serve(h, req(http.MethodGet, "/", "192.168.4.30", nil))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("GET /: status %d, Location %q; want the login page", rec.Code, rec.Header().Get("Location"))
	}
	for _, path := range []string{"/api/status", "/docs/"} {
		if rec := serve(h, req(http.MethodGet, path, "192.168.4.30", nil)); rec.Code == http.StatusOK {
			t.Errorf("GET %s signed out: 200", path)
		}
	}
	cfg := lanConfig(t)
	h = newMux(cfg)
	for _, path := range []string{"/dns", "/devices/add"} {
		rec := serve(h, req(http.MethodPost, path, "192.168.4.30", url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}, "confirm": {"yes"}}))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s signed out, with a valid CSRF token: status %d, want 401", path, rec.Code)
		}
	}
}

func TestSignInThenDashboard(t *testing.T) {
	h := newMux(lanConfig(t))
	c := signIn(t, h)
	r := req(http.MethodGet, "/dashboard", "192.168.4.30", nil)
	r.AddCookie(c)
	rec := serve(h, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard after sign in: status %d", rec.Code)
	}
	body := rec.Body.String()
	// Seen from another computer, the control panel's loopback link would open
	// the reader's own computer. It goes to the hub's panel port instead, under
	// the name the reader used (lanpanel.go).
	if strings.Contains(body, `href="http://localhost:8082"`) {
		t.Error("remote dashboard links to localhost:8082, which opens the reader's own computer")
	}
	if !strings.Contains(body, `href="http://192.168.4.20:8083"`) {
		t.Error("remote dashboard does not link the control panel at the box's panel port")
	}
	if strings.Contains(body, "Opens only on the box itself") {
		t.Error("remote dashboard still calls the control panel box-only")
	}
	if !strings.Contains(body, `action="/logout"`) {
		t.Error("no sign-out button")
	}

	// Sign out ends the session on the server, not just in the browser.
	out := req(http.MethodPost, "/logout", "192.168.4.30", url.Values{})
	out.AddCookie(c)
	serve(h, out)
	again := req(http.MethodGet, "/dashboard", "192.168.4.30", nil)
	again.AddCookie(c)
	if rec := serve(h, again); rec.Code == http.StatusOK {
		t.Fatal("the old session still works after signing out")
	}
}

// With a session, the routes that change things accept the hub's LAN name:
// the cookie, not the loopback Host, is what keeps a rebinding page out.
func TestSignedInChangeFromLAN(t *testing.T) {
	cfg := lanConfig(t)
	h := newMux(cfg)
	c := signIn(t, h)
	r := req(http.MethodPost, "/dns", "192.168.4.30", url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}, "confirm": {"yes"}})
	r.AddCookie(c)
	rec := serve(h, r)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("signed-in POST /dns from the LAN: status %d (%s)", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}

// The default install keeps its old guard: no login, so a non-loopback Host is
// a rebinding page and is refused.
func TestNoLoginStillRefusesLANHost(t *testing.T) {
	cfg := deadConfig(t)
	r := req(http.MethodPost, "/dns", "127.0.0.1", url.Values{"csrf": {cfg.csrfToken}, "override": {"off"}, "confirm": {"yes"}})
	if rec := serve(newMux(cfg), r); rec.Code != http.StatusForbidden {
		t.Fatalf("no login, LAN Host: status %d, want 403", rec.Code)
	}
}

func TestOnlyHomeNetworkServed(t *testing.T) {
	h := newMux(lanConfig(t))
	for _, from := range []string{"8.8.8.8", "100.100.7.7", "2001:db8::1"} {
		if rec := serve(h, req(http.MethodGet, "/login", from, nil)); rec.Code != http.StatusForbidden {
			t.Errorf("GET /login from %s: status %d, want 403", from, rec.Code)
		}
	}
	for _, from := range []string{"192.168.4.30", "10.0.0.8", "172.28.53.1", "127.0.0.1", "fe80::1", "::ffff:192.168.4.30"} {
		if rec := serve(h, req(http.MethodGet, "/login", from, nil)); rec.Code != http.StatusOK {
			t.Errorf("GET /login from %s: status %d, want 200", from, rec.Code)
		}
	}
}

func TestWrongPasswordsLockOut(t *testing.T) {
	h := newMux(lanConfig(t))
	for i := 0; i < maxFailures; i++ {
		if rec := serve(h, req(http.MethodPost, "/login", "192.168.4.31", url.Values{"password": {"nope nope"}})); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: status %d", i+1, rec.Code)
		}
	}
	rec := serve(h, req(http.MethodPost, "/login", "192.168.4.31", url.Values{"password": {testPassword}}))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("right password after %d wrong ones: status %d, want 429", maxFailures, rec.Code)
	}
	// Another computer is not locked out by the first one's guesses.
	signIn(t, h)
}

func TestLoginRedirectStaysOnSite(t *testing.T) {
	for in, want := range map[string]string{
		"/dashboard": "/dashboard", "//evil.example/": "/", "https://evil.example/": "/",
		"/\\evil.example": "/", "": "/", "setup": "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoginRejectsCrossSitePost(t *testing.T) {
	h := newMux(lanConfig(t))
	r := req(http.MethodPost, "/login", "192.168.4.30", url.Values{"password": {testPassword}})
	r.Header.Set("Origin", "http://evil.example")
	if rec := serve(h, r); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site login POST: status %d, want 403", rec.Code)
	}
}

// The lockout table is bounded: wrong passwords from ever more addresses
// cannot grow it past maxTracked, and a locked-out address is kept over
// addresses that are only partway there, and over expired ones.
func TestFailureTableIsBounded(t *testing.T) {
	a, err := newAuth("0.0.0.0", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailures; i++ {
		a.recordFailure("192.168.4.99")
	}
	for i := 0; i < maxTracked+500; i++ {
		a.recordFailure(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if n := len(a.failures); n > maxTracked {
		t.Fatalf("failure table holds %d addresses, cap is %d", n, maxTracked)
	}
	if !a.lockedOut("192.168.4.99") {
		t.Fatal("a flood of one-guess addresses freed a locked-out one")
	}
	// Expired entries go first.
	a.mu.Lock()
	for _, f := range a.failures {
		f.until = time.Now().Add(-time.Second)
	}
	a.failures["192.168.4.99"].until = time.Now().Add(lockout)
	a.mu.Unlock()
	a.recordFailure("172.16.0.1")
	if !a.lockedOut("192.168.4.99") {
		t.Fatal("a locked-out address was forgotten while expired ones were kept")
	}
	if n := len(a.failures); n != 2 {
		t.Fatalf("after pruning, %d addresses tracked; want 2 (the live lockout and the new one)", n)
	}
}

// An unlocked hub answers only to a loopback name, reads included: a
// DNS-rebinding page arrives under its own name and must not read the status
// or the dashboard (which carries the form token).
func TestUnlockedHubReadsNeedLoopbackHost(t *testing.T) {
	cfg := deadConfig(t)
	h := newMux(cfg)
	for _, path := range []string{"/api/status", "/dashboard", "/", "/setup"} {
		for host, want := range map[string]bool{"localhost:8080": true, "127.0.0.1:8080": true, "evil.example:8080": false, "192.168.4.20:8080": false} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Host = host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if got := rec.Code != http.StatusForbidden; got != want {
				t.Errorf("GET %s as Host %q: status %d, served=%v want %v", path, host, rec.Code, got, want)
			}
		}
	}
}
