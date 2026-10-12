package main

import (
	"net"
	"net/http"
	"net/http/httptest"
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
