package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The second box's one Add: member and backup DNS box in one write, so it is
// never a plain member in between.
func TestAddBoxMakesItABackupInOneWrite(t *testing.T) {
	f := &fakePanel{override: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)
	form := url.Values{"csrf": {cfg.csrfToken}, "box": {secondBoxNpub}, "name": {"Pi"}}
	review := postTo(mux, "/devices/add-box", form, nil).Body.String()
	if !strings.Contains(review, "joins your mesh as a backup DNS box") || len(f.writes) != 0 {
		t.Fatalf("review does not say it joins as a backup (%d writes):\n%s", len(f.writes), review)
	}
	form.Set("confirm", "yes")
	if rec := postTo(mux, "/devices/add-box", form, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: %d\n%s", rec.Code, rec.Body.String())
	}
	if len(f.writes) != 1 || f.bodies[0]["dnsBackup"] != true || f.bodies[0]["npub"] != secondBoxNpub {
		t.Fatalf("want one add_participant with dnsBackup, got %v", f.bodies)
	}
	// The dashboard marks it.
	if body := get(t, mux, "/dashboard").Body.String(); !strings.Contains(body, `<span class="role">backup DNS box</span>`) {
		t.Fatalf("dashboard does not mark the backup")
	}
	// Adding it again changes nothing.
	postTo(mux, "/devices/add-box", form, nil)
	if len(f.writes) != 1 {
		t.Fatalf("second add of a backup reached the panel: %d writes", len(f.writes))
	}
}

// A member that is not yet a backup is made one by the same Add, instead of
// being waved through as "already there".
func TestAddBoxMakesAnExistingMemberABackup(t *testing.T) {
	f := &fakePanel{override: true, added: []string{secondBoxNpub}}
	cfg := panelConfig(t, f)
	rec := postTo(newMux(cfg), "/devices/add-box", url.Values{"csrf": {cfg.csrfToken}, "box": {secondBoxNpub}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusSeeOther || len(f.writes) != 1 || f.bodies[0]["dnsBackup"] != true {
		t.Fatalf("existing member: %d, bodies %v", rec.Code, f.bodies)
	}
}

// A panel from before add-as-backup would drop dnsBackup unread and leave a
// plain member: refused before anything is sent.
func TestAddBoxRefusedOnAnOldPanel(t *testing.T) {
	f := &fakePanel{override: true, oldPanel: true}
	cfg := panelConfig(t, f)
	rec := postTo(newMux(cfg), "/devices/add-box", url.Values{"csrf": {cfg.csrfToken}, "box": {secondBoxNpub}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusConflict || len(f.writes) != 0 || !strings.Contains(rec.Body.String(), "too old") {
		t.Fatalf("old panel: %d, %d writes\n%s", rec.Code, len(f.writes), rec.Body.String())
	}
}

// A panel that took the add but didn't make a backup is reported, in words
// that say the box is in but won't answer.
func TestAddBoxReportsABackupThatDidNotTake(t *testing.T) {
	f := &fakePanel{override: true, dropBackup: true}
	cfg := panelConfig(t, f)
	rec := postTo(newMux(cfg), "/devices/add-box", url.Values{"csrf": {cfg.csrfToken}, "box": {secondBoxNpub}, "confirm": {"yes"}}, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "The box was added, but the control panel didn&#39;t make it a backup DNS box") {
		t.Fatalf("backup that didn't take: %d\n%s", rec.Code, rec.Body.String())
	}
}

// With no DNS box there is nothing to back up: the box joins as a member.
func TestAddBoxWithNoDNSBoxAddsAMember(t *testing.T) {
	f := &fakePanel{noNode: true}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)
	form := url.Values{"csrf": {cfg.csrfToken}, "box": {secondBoxNpub}}
	if review := postTo(mux, "/devices/add-box", form, nil).Body.String(); !strings.Contains(review, "no DNS box yet") {
		t.Fatalf("review does not say why it isn't a backup:\n%s", review)
	}
	form.Set("confirm", "yes")
	if rec := postTo(mux, "/devices/add-box", form, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: %d\n%s", rec.Code, rec.Body.String())
	}
	if _, sent := f.bodies[0]["dnsBackup"]; sent || len(f.writes) != 1 {
		t.Fatalf("member add sent %v", f.bodies)
	}
}

func TestRemovedDevicesCanBeReAdded(t *testing.T) {
	f := &fakePanel{override: true, removed: []string{secondBoxNpub}}
	cfg := panelConfig(t, f)
	mux := newMux(cfg)
	body := get(t, mux, "/dashboard").Body.String()
	for _, want := range []string{`id="removed"`, `action="/devices/readd"`, `name="npub" value="` + secondBoxNpub + `"`, "Re-add as backup DNS box"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard lacks %q", want)
		}
	}

	// Only a device on the list.
	rec := postTo(mux, "/devices/readd", url.Values{"csrf": {cfg.csrfToken}, "npub": {firstBoxNpub}}, nil)
	if rec.Code != http.StatusConflict || len(f.writes) != 0 {
		t.Fatalf("re-add of an unlisted device: %d, %d writes", rec.Code, len(f.writes))
	}
	if rec := postTo(mux, "/devices/readd", url.Values{"csrf": {"nope"}, "npub": {secondBoxNpub}}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("re-add without the token: %d", rec.Code)
	}

	rec = postTo(mux, "/devices/readd", url.Values{"csrf": {cfg.csrfToken}, "npub": {secondBoxNpub}, "backup": {"yes"}}, nil)
	if rec.Code != http.StatusSeeOther || len(f.writes) != 1 || f.bodies[0]["dnsBackup"] != true || f.bodies[0]["alias"] != "old "+secondBoxNpub[len(secondBoxNpub)-4:] {
		t.Fatalf("re-add as backup: %d, bodies %v\n%s", rec.Code, f.bodies, rec.Body.String())
	}
	if body := get(t, mux, "/dashboard").Body.String(); strings.Contains(body, `id="removed"`) {
		t.Fatalf("re-added device still listed as removed")
	}
}

func TestRemovedDevicesHiddenOnAnOldPanel(t *testing.T) {
	body := get(t, newMux(panelConfig(t, &fakePanel{oldPanel: true, removed: []string{secondBoxNpub}})), "/dashboard").Body.String()
	if strings.Contains(body, `id="removed"`) {
		t.Fatalf("an old panel's dashboard shows a removed list it never sent")
	}
}

func TestDashboardAlerts(t *testing.T) {
	// Disconnect pressed on the DNS box, with network DNS on.
	body := get(t, newMux(panelConfig(t, &fakePanel{override: true, vpnOff: true})), "/dashboard").Body.String()
	if !strings.Contains(body, `class="alert down"`) || !strings.Contains(body, "The VPN is paused on this box, and it is the network&#39;s DNS box.") {
		t.Fatalf("no paused alert:\n%s", body)
	}
	// Positive control: connected, no alert.
	body = get(t, newMux(panelConfig(t, &fakePanel{override: true})), "/dashboard").Body.String()
	if strings.Contains(body, `class="alert`) {
		t.Fatalf("an alert on a healthy box")
	}
	// Paused but network DNS off: nobody depends on it, no alert.
	body = get(t, newMux(panelConfig(t, &fakePanel{vpnOff: true})), "/dashboard").Body.String()
	if strings.Contains(body, "VPN is paused") {
		t.Fatalf("paused alert with network DNS off")
	}

	// Two builds.
	body = get(t, newMux(panelConfig(t, &fakePanel{override: true, versions: [2]string{"4.1.18+aaaaaaa", "4.1.18+bbbbbbb"}})), "/dashboard").Body.String()
	if !strings.Contains(body, "different builds") || !strings.Contains(body, "Control panel 4.1.18&#43;aaaaaaa, VPN 4.1.18&#43;bbbbbbb") {
		t.Fatalf("no build mismatch alert")
	}
	for _, same := range [][2]string{{"4.1.18+aaaaaaa", "4.1.18+aaaaaaa"}, {"4.1.18", ""}, {"", "4.1.18"}} {
		body = get(t, newMux(panelConfig(t, &fakePanel{override: true, versions: same})), "/dashboard").Body.String()
		if strings.Contains(body, "different builds") {
			t.Fatalf("build mismatch alert for %v", same)
		}
	}
}

func TestBackupsGoneAlert(t *testing.T) {
	net := &tickNetwork{NetworkID: "9683dbda", DNSNodeNpub: "npub1box", DNSOverride: true}
	state := tick{DaemonRunning: true, VPNEnabled: true}
	seen := &backupMemory{}
	gone := func(backups int) bool {
		for _, a := range buildAlerts(page{network: net, Backups: backups}, state, seen) {
			if a.Title == "No backup DNS box left." {
				return true
			}
		}
		return false
	}
	if gone(0) {
		t.Fatal("alert on a network that never had a backup")
	}
	if gone(1) {
		t.Fatal("alert while a backup is there")
	}
	if !gone(0) {
		t.Fatal("no alert once the last backup left")
	}
	if gone(2) {
		t.Fatal("alert after a backup came back")
	}
	if (&backupMemory{}).note("9683dbda", 0) {
		t.Fatal("a fresh memory says the network had a backup")
	}
	// End to end: the dashboard raises it once a backup it showed is gone.
	f := &fakePanel{override: true, added: []string{secondBoxNpub}, backups: map[string]bool{secondBoxNpub: true}}
	mux := newMux(panelConfig(t, f))
	if body := get(t, mux, "/dashboard").Body.String(); strings.Contains(body, "No backup DNS box left") {
		t.Fatal("alert while the backup is there")
	}
	f.backups = nil
	if body := get(t, mux, "/dashboard").Body.String(); !strings.Contains(body, "No backup DNS box left") {
		t.Fatal("no alert once the backup is gone")
	}
}

func TestJoinCodeQRAndScanFill(t *testing.T) {
	f := &fakePanel{override: true}
	body := get(t, newMux(panelConfig(t, f)), "/dashboard").Body.String()
	if !strings.Contains(body, `data-qr="sdns://join/npub1box:9683dbda"`) || !strings.Contains(body, `src="/assets/qrcode.js"`) {
		t.Fatalf("first box does not show its join code as a QR code")
	}
	for _, in := range []string{joinCodeQRPrefix + firstBoxNpub + ":a0454be9", "SDNS://JOIN/" + firstBoxNpub + ":a0454be9"} {
		if npub, id, err := parseJoinCode(in); err != nil || npub != firstBoxNpub || id != "a0454be9" {
			t.Errorf("parseJoinCode(%q) = %q %q %v", in, npub, id, err)
		}
	}
	if _, _, err := parseJoinCode(joinCodeQRPrefix + secondBoxNpub); err == nil {
		t.Errorf("a QR prefix with no network ID was accepted")
	}
	if !strings.Contains(scanJS, "btn.dataset.fill") {
		t.Fatalf("scan.js does not fill a join code into the field")
	}
}

func TestPlainPanelErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()
	cases := []struct {
		err  error
		want string
	}{
		{ctx.Err(), "took too long"},
		{errors.New(`Post "http://sovereign-dns-web:8081/api/add_participant": dial tcp 172.28.53.3:8081: connect: connection refused`), "isn't answering"},
		{errors.New("this control panel build has no /api/add_participant route"), "too old"},
		{errors.New("only a network admin can change the network DNS settings"), "isn't the network's admin"},
		{errors.New("this network has no DNS box yet, so there is nothing to back up; add the device, then make it the DNS box"), "Turn network DNS on first"},
		{errors.New("invalid npub: checksum failed"), "didn't accept that device ID"},
		{fmt.Errorf("wrapped: %w", errBoxNotBackup), "The box was added"},
		// Positive control: an unknown message is passed on, not swallowed.
		{errors.New("roster is locked"), "The VPN control panel refused: roster is locked"},
	}
	for _, c := range cases {
		if got := plainPanelError(c.err); !strings.Contains(got, c.want) {
			t.Errorf("plainPanelError(%v) = %q, want it to contain %q", c.err, got, c.want)
		}
	}
}

// A write to a panel that isn't there reads as "isn't answering", the
// error a stopped container really produces.
func TestDeadPanelReadsAsNotAnswering(t *testing.T) {
	cfg := deadConfig(t)
	cfg.panelAPI = "http://127.0.0.1:1" // nothing listens there
	_, err := cfg.panelWrite(context.Background(), "/api/add_participant", map[string]any{"networkId": "network-1"})
	if err == nil {
		t.Fatal("a write to nothing succeeded")
	}
	if got := plainPanelError(err); !strings.Contains(got, "isn't answering") {
		t.Fatalf("%v reads as %q", err, got)
	}
}
