package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The panel port opens only once the hub itself is on the home network.
func TestLANPanelPort(t *testing.T) {
	local, err := newAuth("127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if p := lanPanelPort(local); p != "" {
		t.Errorf("hub on this computer only: panel port %q, want none", p)
	}
	// A password on a loopback hub (the mesh needs one) opens no LAN port.
	meshOnly, err := newAuth("127.0.0.1", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if p := lanPanelPort(meshOnly); p != "" {
		t.Errorf("loopback hub with a password: panel port %q, want none", p)
	}
	lan, err := newAuth("0.0.0.0", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if p := lanPanelPort(lan); p != "8083" {
		t.Errorf("hub on the home network: panel port %q, want 8083", p)
	}
	t.Setenv("HUB_PANEL_PORT", "9183")
	if p := lanPanelPort(lan); p != "9183" {
		t.Errorf("HUB_PANEL_PORT=9183: panel port %q", p)
	}
}

// An old-shaped hub on the network with no panel port still says where the
// panel is rather than linking the reader's own localhost.
func TestRemoteDashboardWithoutPanelPort(t *testing.T) {
	cfg := lanConfig(t)
	cfg.lanPanelPort = ""
	h := newMux(cfg)
	r := req(http.MethodGet, "/dashboard", "192.168.4.30", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cfg.auth.newSession()})
	body := serve(h, r).Body.String()
	if strings.Contains(body, `href="http://localhost:8082"`) || !strings.Contains(body, "Opens only on the box itself") {
		t.Error("no panel port: the dashboard must name the box, not link localhost")
	}
}

// The panel from the home network: private addresses only, then the hub's
// login, then the panel with the browser's Host and none of the hub's cookie.
func TestLANPanelProxy(t *testing.T) {
	var got []*http.Request
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Clone(r.Context()))
		fmt.Fprint(w, "panel "+r.URL.Path)
	}))
	t.Cleanup(panel.Close)
	cfg := lanConfig(t)
	h := lanPanel(cfg.auth, panel.URL)
	panelReq := func(method, path, from string) *http.Request {
		r := req(method, path, from, nil)
		r.Host = "192.168.4.20:8083"
		return r
	}

	if rec := serve(h, panelReq(http.MethodGet, "/", "192.168.4.30")); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("signed out, GET /: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := serve(h, panelReq(http.MethodPost, "/api/add_participant", "192.168.4.30")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed out, POST to the panel API: %d, want 401", rec.Code)
	}
	session := cfg.auth.newSession()
	public := panelReq(http.MethodGet, "/", "8.8.8.8")
	public.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	if rec := serve(h, public); rec.Code != http.StatusForbidden {
		t.Fatalf("public address, signed in: %d, want 403", rec.Code)
	}
	script := panelReq(http.MethodGet, "/api/tick", "192.168.4.30")
	script.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	script.Header.Set("Sec-Fetch-Site", "same-site")
	script.Header.Set("Sec-Fetch-Mode", "cors")
	if rec := serve(h, script); rec.Code != http.StatusForbidden {
		t.Fatalf("fetch from another port's page: %d, want 403", rec.Code)
	}
	// A form on another port of the same host (Pi-hole at :80) is a same-site
	// navigation, so it passes crossPageFetch and carries the cookie.
	form := panelReq(http.MethodPost, "/api/add_participant", "192.168.4.30")
	form.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	form.Header.Set("Origin", "http://192.168.4.20")
	form.Header.Set("Sec-Fetch-Site", "same-site")
	form.Header.Set("Sec-Fetch-Mode", "navigate")
	if rec := serve(h, form); rec.Code != http.StatusForbidden {
		t.Fatalf("form post from another port's page: %d, want 403", rec.Code)
	}
	if len(got) != 0 {
		t.Fatalf("the panel was reached %d times before a signed-in request", len(got))
	}

	r := panelReq(http.MethodPost, "/api/tick", "192.168.4.30")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	r.Header.Set("Origin", "http://192.168.4.20:8083")
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	rec := serve(h, r)
	if rec.Code != http.StatusOK || rec.Body.String() != "panel /api/tick" || len(got) != 1 {
		t.Fatalf("signed in: %d %q, panel saw %d", rec.Code, rec.Body.String(), len(got))
	}
	p := got[0]
	if p.Host != "192.168.4.20:8083" || p.Header.Get("Origin") != "http://192.168.4.20:8083" {
		t.Errorf("panel saw Host %q Origin %q, want the browser's", p.Host, p.Header.Get("Origin"))
	}
	for _, hd := range []string{"Cookie", "X-Forwarded-For"} {
		if v := p.Header.Get(hd); v != "" {
			t.Errorf("panel got %s: %q", hd, v)
		}
	}
}

func TestLANPanelURL(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.4.20:8080":      "http://192.168.4.20:8083",
		"raspberrypi.local:8080": "http://raspberrypi.local:8083",
		"[fd00::5]:8080":         "http://[fd00::5]:8083",
		"192.168.4.20":           "http://192.168.4.20:8083",
	} {
		if got := lanPanelURL(in, "8083"); got != want {
			t.Errorf("lanPanelURL(%q) = %q, want %q", in, got, want)
		}
	}
}
