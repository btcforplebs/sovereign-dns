package main

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"
)

// runScript drives the terminal setup with typed lines and returns what it
// printed. Waits are short so a test never sits through a real one.
func runScript(t *testing.T, cfg config, script string, o setupOptions) string {
	t.Helper()
	var out strings.Builder
	o.ready, o.admitted, o.poll = 300*time.Millisecond, 300*time.Millisecond, 20*time.Millisecond
	io := setupIO{in: bufio.NewReader(strings.NewReader(script)), out: &out}
	if err := cfg.terminalSetup(context.Background(), io, o); err != nil {
		t.Fatalf("terminalSetup: %v\n%s", err, out.String())
	}
	return out.String()
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestTerminalFirstBoxAddsAPhoneAfterReview(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	url := "http://192.168.1.20:8080/setup?step=3&kind=phone"
	out := runScript(t, cfg, "1\n"+phoneLink(t)+"\ny\n\n", setupOptions{ifNew: true, url: url})

	mustContain(t, out, "Set up your devices now?", "Join Network and tap Copy Request", url,
		"npub1box:9683dbda", "This link is from Logen's iPhone, device ID "+phoneNpub, "Added Logen's iPhone", "(1 added just now)")
	if len(f.writes) != 1 || f.writes[0].URL.Path != "/api/import_join_request" || f.bodies[0]["request"] != phoneLink(t) {
		t.Fatalf("panel got %d writes, bodies %v", len(f.writes), f.bodies)
	}
	if len(f.qrTexts) != 1 || f.qrTexts[0] != url {
		t.Fatalf("QR drawn for %q, want the add-device page", f.qrTexts)
	}
	if !strings.Contains(out, "█") {
		t.Fatalf("no QR code printed:\n%s", out)
	}
}

func TestTerminalFirstBoxReviewIsTheApproval(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	out := runScript(t, cfg, "1\n"+phoneLink(t)+"\nn\n"+phoneLink(t)+"\n\n\n", setupOptions{})
	mustContain(t, out, "Not added.", "0 devices on this box's network")
	if len(f.writes) != 0 {
		t.Fatalf("answering n (or Enter) sent %d writes", len(f.writes))
	}
	if len(f.qrTexts) != 0 || strings.Contains(out, "camera") {
		t.Fatalf("a box with no home-network dashboard printed its page:\n%s", out)
	}
}

func TestTerminalFirstBoxAddsASecondBoxAsBackup(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	out := runScript(t, cfg, "1\n "+deviceQRPrefix+secondBoxNpub+"\ny\n\n", setupOptions{})
	mustContain(t, out, "a box with ID "+secondBoxNpub, "It joins as a backup DNS box", "Added the box.")
	if len(f.writes) != 1 || f.writes[0].URL.Path != "/api/add_participant" ||
		f.bodies[0]["npub"] != secondBoxNpub || f.bodies[0]["dnsBackup"] != true {
		t.Fatalf("panel got %d writes, bodies %v", len(f.writes), f.bodies)
	}
}

func TestTerminalFirstBoxRefusalsAreTheDashboards(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	out := runScript(t, cfg, "1\nnvpn://join-request/nope\nnpub1box"+strings.Repeat("q", 50)+"\nhello\n\n", setupOptions{})
	if len(f.writes) != 0 {
		t.Fatalf("a bad paste sent %d writes", len(f.writes))
	}
	if strings.Count(out, "Paste a join link") != 4 {
		t.Fatalf("did not keep asking after bad pastes:\n%s", out)
	}
	_, _, linkErr := parseJoinLink("nvpn://join-request/nope")
	mustContain(t, out, linkErr.Error())
}

func TestTerminalSecondBoxJoinsAndShowsItsID(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	out := runScript(t, cfg, "2\n"+firstBoxNpub+":a0454be9\n\n", setupOptions{ifNew: true})

	mustContain(t, out, "Join your first box", "paste this ID", "npub1box", "Waiting for your first box")
	if len(f.writes) != 1 || f.writes[0].URL.Path != "/api/manual_add_network" ||
		f.bodies[0]["adminNpub"] != firstBoxNpub || f.bodies[0]["meshNetworkId"] != "a0454be9" {
		t.Fatalf("panel got %d writes, bodies %v", len(f.writes), f.bodies)
	}
	if len(f.qrTexts) != 1 || f.qrTexts[0] != deviceQRPrefix+"npub1box" {
		t.Fatalf("QR drawn for %q, want this box's ID as the dashboard shows it", f.qrTexts)
	}

	// Once the first box adds it, the wait ends on its own.
	f.admitted = true
	out = runScript(t, cfg, "", setupOptions{ifNew: true})
	mustContain(t, out, "this box has joined your first box's network")
	if len(f.writes) != 1 {
		t.Fatalf("a rerun wrote again: %d writes", len(f.writes))
	}
}

func TestTerminalSecondBoxWaitEndsWhenAdmitted(t *testing.T) {
	f := &fakePanel{alone: true, override: true, joinedTo: "a0454be9", joinAdmin: firstBoxNpub}
	cfg := panelConfig(t, f)
	go func() { time.Sleep(60 * time.Millisecond); f.admitted = true }()
	var out strings.Builder
	in := bufio.NewReader(blockingReader{})
	err := cfg.secondBoxWait(context.Background(), setupIO{in: in, out: &out},
		setupOptions{admitted: 2 * time.Second, poll: 20 * time.Millisecond}, cfg.buildPage(context.Background()))
	if err != nil || !strings.Contains(out.String(), "You're set") {
		t.Fatalf("wait did not end on admission: %v\n%s", err, out.String())
	}
}

// blockingReader never returns: a terminal nobody types into.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) { select {} }

func TestTerminalSecondBoxRefusalsAreTheDashboards(t *testing.T) {
	f := &fakePanel{alone: true, override: true, selfNpub: secondBoxNpub}
	cfg := panelConfig(t, f)
	out := runScript(t, cfg, "2\n"+secondBoxNpub+":9683dbda\nnot a code\n\n", setupOptions{})
	mustContain(t, out, "That is this box's own join code", "Skipped.")
	if len(f.writes) != 0 {
		t.Fatalf("refused joins sent %d writes", len(f.writes))
	}
}

func TestTerminalSetupSkips(t *testing.T) {
	f := &fakePanel{alone: true, override: true}
	cfg := panelConfig(t, f)
	for name, script := range map[string]string{"Enter": "\n", "no terminal": "", "after a wrong answer": "3\n\n"} {
		out := runScript(t, cfg, script, setupOptions{ifNew: true})
		mustContain(t, out, "Skipped.", "docker compose exec hub /hub setup")
		if len(f.writes) != 0 {
			t.Fatalf("%s: skipping wrote %d times", name, len(f.writes))
		}
	}
}

func TestTerminalSetupQuietOnAnUpdate(t *testing.T) {
	f := &fakePanel{override: true} // a box with a device already
	cfg := panelConfig(t, f)
	out := runScript(t, cfg, "", setupOptions{ifNew: true})
	mustContain(t, out, "Device setup: already done (1 device")
	if strings.Contains(out, "Set up your devices now?") || strings.Contains(out, "Paste") {
		t.Fatalf("an update asked questions:\n%s", out)
	}
	// Run by hand, it goes straight to adding more.
	out = runScript(t, cfg, "\n", setupOptions{})
	mustContain(t, out, "Paste a join link")
}

func TestTerminalSetupWaitsForTheVPN(t *testing.T) {
	f := &fakePanel{noNetworks: true}
	cfg := panelConfig(t, f)
	out := runScript(t, cfg, "1\n", setupOptions{ifNew: true})
	mustContain(t, out, "The VPN isn't ready yet", "docker compose exec hub /hub setup")
	if strings.Contains(out, "Set up your devices now?") {
		t.Fatalf("asked before the VPN was ready:\n%s", out)
	}
}

func TestRenderQRIsSquareWithAQuietZone(t *testing.T) {
	// 3x3, centre dark: 7 modules with the quiet zone, so 4 character rows.
	got := renderQR(3, []bool{false, false, false, false, true, false, false, false, false})
	rows := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(rows) != 4 {
		t.Fatalf("%d rows:\n%s", len(rows), got)
	}
	// Character rows cover module rows 0-1, 2-3, 4-5 and 6, so the dark
	// centre (3,3) is the lower half of character row 1, column 3.
	if r := []rune(strings.TrimPrefix(rows[1], "    ")); r[3] != '▀' {
		t.Fatalf("centre module not dark in its lower half: %q", string(r))
	}
	for _, r := range []rune(strings.TrimPrefix(rows[0], "    ")) {
		if r != '█' {
			t.Fatalf("quiet zone not light: %q", rows[0])
		}
	}
}
