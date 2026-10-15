package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The vendored libraries are the upstream files byte for byte.
func TestVendoredQRLibrariesAreUpstream(t *testing.T) {
	for name, c := range map[string][2]string{
		"jsQR":             {jsQRSource, jsQRSHA256},
		"qrcode-generator": {qrcodeGenSource, qrcodeGenSHA256},
	} {
		sum := sha256.Sum256([]byte(c[0]))
		if got := hex.EncodeToString(sum[:]); got != c[1] {
			t.Errorf("%s hashes to %s, want the upstream %s", name, got, c[1])
		}
	}
}

func TestScriptsAreServed(t *testing.T) {
	mux := newMux(deadConfig(t))
	for path, src := range map[string]string{
		"/assets/scan.js":   scanJS,
		"/assets/jsqr.js":   jsQRSource,
		"/assets/qrcode.js": qrcodeGenSource,
	} {
		rec := get(t, mux, path)
		h := rec.Header()
		if rec.Code != http.StatusOK || rec.Body.String() != src ||
			h.Get("Content-Type") != "text/javascript; charset=utf-8" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d %v (%d bytes)", path, rec.Code, h, rec.Body.Len())
		}
		r := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d", path, w.Code)
		}
	}
}

// With a password, the scripts are behind the login like everything else.
func TestScriptsNeedTheLogin(t *testing.T) {
	mux := newMux(lanConfig(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req(http.MethodGet, "/assets/scan.js", "192.168.4.30", nil))
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "sdnsScan") {
		t.Fatalf("scan.js served without signing in")
	}
}

// Every scan button carries this page's token and where to come back to, and
// the page loads the shared scanner rather than a camera-only one of its own.
func TestScanButtonsOnTheDashboardAndWizard(t *testing.T) {
	f := &fakePanel{override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)

	dash := get(t, mux, "/dashboard").Body.String()
	if n := strings.Count(dash, `data-scan data-csrf="`+cfg.csrfToken+`" data-from=""`); n != 2 {
		t.Fatalf("dashboard has %d scan buttons with the token, want 2 (phone and box)", n)
	}
	if !strings.Contains(dash, `src="/assets/scan.js"`) || strings.Contains(dash, "BarcodeDetector") {
		t.Fatalf("dashboard does not load the shared scanner")
	}
	for _, path := range []string{"/setup?step=3", "/setup?step=3&kind=box"} {
		page := getWithRole(t, mux, path, "first")
		if !strings.Contains(page, `data-scan data-csrf="`+cfg.csrfToken+`" data-from="setup"`) {
			t.Fatalf("%s has no scan button returning to setup", path)
		}
	}
	if len(f.writes) != 0 {
		t.Fatalf("rendering pages reached the panel")
	}
}

// The scanner posts to the review routes, never with confirm, and knows the
// same two forms the server does.
func TestScannerOnlyEverReviews(t *testing.T) {
	for _, want := range []string{"'/devices/add'", "'/devices/add-box'", "'nvpn://join-request/'", "'nvpn://device/'", "setAttribute('capture', 'environment')"} {
		if !strings.Contains(scanJS, want) {
			t.Errorf("scan.js lacks %s", want)
		}
	}
	for _, gone := range []string{"getUserMedia", "isSecureContext", "<video"} {
		if strings.Contains(scanJS, gone) {
			t.Errorf("scan.js still has %s: the button takes a photo, it never runs a live camera", gone)
		}
	}
	if strings.Contains(scanJS, "confirm") {
		t.Errorf("scan.js sends confirm: a scan must land on the review page")
	}
	if !strings.HasPrefix(deviceQRPrefix, "nvpn://") || !strings.Contains(scanJS, "var DEVICE = '"+deviceQRPrefix+"'") {
		t.Errorf("scan.js and parseBoxID disagree on the box QR form")
	}
}
