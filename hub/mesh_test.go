package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

var testGate = netip.MustParseAddr("172.28.53.10")

// meshReq is what the gate hands the hub's mesh port: from the gate's own
// address, naming the phone in X-Forwarded-For, with the Host the phone used.
func meshReq(method, path, device string, body url.Values) *http.Request {
	r := req(method, path, testGate.String(), body)
	r.Host = "10.44.12.7:8080"
	if device != "" {
		r.Header.Set("X-Forwarded-For", device)
	}
	return r
}

func meshMux(t *testing.T) http.Handler {
	t.Helper()
	cfg := deadConfig(t)
	a, err := newAuth("127.0.0.1", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cfg.auth = a
	cfg.mesh = true
	return meshHandler(testGate, newMux(cfg))
}

func TestMeshNeedsPassword(t *testing.T) {
	none, _ := newAuth("127.0.0.1", "")
	if meshNeedsLogin(true, none) == nil {
		t.Error("HUB_MESH=on with no password would start")
	}
	if err := meshNeedsLogin(false, none); err != nil {
		t.Errorf("HUB_MESH off, no password: %v; want the default install to start", err)
	}
	pw, _ := newAuth("127.0.0.1", testPassword)
	if err := meshNeedsLogin(true, pw); err != nil {
		t.Errorf("HUB_MESH=on with a password: %v", err)
	}
}

func TestMeshOnOnlyForOn(t *testing.T) {
	for v, want := range map[string]bool{"on": true, "ON": true, " on ": true, "": false, "off": false, "yes": false, "1": false, "true": false} {
		t.Setenv("HUB_MESH", v)
		if got := meshOn(); got != want {
			t.Errorf("HUB_MESH=%q: on=%v, want %v", v, got, want)
		}
	}
}

// Only the gate may use the mesh port: anyone else could name any device.
func TestMeshPortOnlyServesTheGate(t *testing.T) {
	h := meshMux(t)
	for _, from := range []string{"172.28.53.11", "172.18.0.1", "10.44.12.9", "127.0.0.1"} {
		r := meshReq(http.MethodGet, "/login", "10.44.12.9", nil)
		r.RemoteAddr = net.JoinHostPort(from, "4000")
		if rec := serve(h, r); rec.Code != http.StatusForbidden {
			t.Errorf("mesh port from %s: status %d, want 403", from, rec.Code)
		}
	}
	if rec := serve(h, meshReq(http.MethodGet, "/login", "", nil)); rec.Code != http.StatusBadRequest {
		t.Errorf("gate with no device: status %d, want 400", rec.Code)
	}
	if rec := serve(h, meshReq(http.MethodGet, "/login", "10.44.1.1, 10.44.1.2", nil)); rec.Code != http.StatusBadRequest {
		t.Errorf("gate naming two devices: status %d, want 400", rec.Code)
	}
}

// Every page needs the login over the mesh, and signing in works.
func TestMeshNeedsLoginThenServes(t *testing.T) {
	h := meshMux(t)
	for _, path := range []string{"/", "/dashboard", "/setup", "/docs/"} {
		rec := serve(h, meshReq(http.MethodGet, path, "10.44.12.9", nil))
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("GET %s over the mesh signed out: status %d, Location %q; want the login page", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := serve(h, meshReq(http.MethodGet, "/api/status", "10.44.12.9", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/status over the mesh signed out: status %d, want 401", rec.Code)
	}
	rec := serve(h, meshReq(http.MethodPost, "/login", "10.44.12.9", url.Values{"password": {testPassword}, "next": {"/dashboard"}}))
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			cookie = c
		}
	}
	if rec.Code != http.StatusSeeOther || cookie == nil {
		t.Fatalf("sign in over the mesh: status %d, cookie %v", rec.Code, cookie)
	}
	r := meshReq(http.MethodGet, "/dashboard", "10.44.12.9", nil)
	r.AddCookie(cookie)
	if rec := serve(h, r); rec.Code != http.StatusOK {
		t.Errorf("dashboard over the mesh signed in: status %d, want 200", rec.Code)
	}
}

// Wrong passwords lock out the device that typed them, not the whole mesh:
// every mesh request reaches the hub from the gate's one address.
func TestMeshLockoutIsPerDevice(t *testing.T) {
	h := meshMux(t)
	wrong := url.Values{"password": {"not it at all"}}
	for i := 0; i < maxFailures; i++ {
		serve(h, meshReq(http.MethodPost, "/login", "10.44.12.66", wrong))
	}
	if rec := serve(h, meshReq(http.MethodPost, "/login", "10.44.12.66", url.Values{"password": {testPassword}})); rec.Code != http.StatusTooManyRequests {
		t.Errorf("guessing device after %d misses: status %d, want 429", maxFailures, rec.Code)
	}
	if rec := serve(h, meshReq(http.MethodPost, "/login", "10.44.12.7", url.Values{"password": {testPassword}})); rec.Code != http.StatusSeeOther {
		t.Errorf("another phone on the mesh: status %d, want it to sign in (303)", rec.Code)
	}
}

// The gate: mesh callers only, forwarded with their own address and Host.
func TestMeshGate(t *testing.T) {
	var gotXFF, gotHost string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF, gotHost = r.Header.Get("X-Forwarded-For"), r.Host
		_, _ = io.WriteString(w, "hub")
	}))
	defer hub.Close()
	g := meshGate(strings.TrimPrefix(hub.URL, "http://"), netip.MustParsePrefix("10.44.0.0/16"))

	for _, from := range []string{"172.28.53.12", "172.28.53.11", "192.168.1.5", "127.0.0.1", "10.45.0.1"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = net.JoinHostPort(from, "5000")
		if rec := serve(g, r); rec.Code != http.StatusForbidden {
			t.Errorf("gate from %s: status %d, want 403", from, rec.Code)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	r.RemoteAddr = "10.44.12.7:5000"
	r.Host = "10.44.200.1:8080"
	r.Header.Set("X-Forwarded-For", "10.44.99.99") // a device naming another
	rec := serve(g, r)
	if rec.Code != http.StatusOK || rec.Body.String() != "hub" {
		t.Fatalf("gate from a mesh device: status %d, body %q", rec.Code, rec.Body.String())
	}
	if gotXFF != "10.44.12.7" {
		t.Errorf("hub was told the device is %q, want the caller's own address 10.44.12.7", gotXFF)
	}
	if gotHost != "10.44.200.1:8080" {
		t.Errorf("hub saw Host %q, want the one the phone used", gotHost)
	}

	hub.Close()
	rec = serve(g, r)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "hub-mesh.sh on") {
		t.Errorf("gate with the hub's mesh port closed: status %d, body %q; want 502 saying how to open it", rec.Code, rec.Body.String())
	}
}

// Gate and mesh port together, the way compose wires them.
func TestMeshGateToHub(t *testing.T) {
	hub := httptest.NewServer(meshHandler(netip.MustParseAddr("127.0.0.1"), func() http.Handler {
		cfg := deadConfig(t)
		a, _ := newAuth("127.0.0.1", testPassword)
		cfg.auth, cfg.mesh = a, true
		return newMux(cfg)
	}()))
	defer hub.Close()
	g := meshGate(strings.TrimPrefix(hub.URL, "http://"), netip.MustParsePrefix("10.44.0.0/16"))

	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"password": {testPassword}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://10.44.200.1:8080")
	r.RemoteAddr = "10.44.12.7:5000"
	r.Host = "10.44.200.1:8080"
	rec := serve(g, r)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("signing in through the gate: status %d, body %q", rec.Code, rec.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	r.RemoteAddr = "10.44.12.7:5000"
	r.Host = "10.44.200.1:8080"
	if rec := serve(g, r); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("dashboard through the gate with no session: status %d, want the login page", rec.Code)
	}
}
