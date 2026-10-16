package main

// Add a device: the second thing the hub changes rather than just reports.
//
// A new device makes its own join request. The Nostr VPN app shows it as a QR
// code with Copy and Share buttons, and `nvpn join-request --no-qr` prints it
// on a computer. It is a plain-text link, nvpn://join-request/<payload>. The
// person at this box pastes it here (or photographs its QR code with Scan QR
// code), sees which device it names, and adds it with one more click. The
// hub hands the link to the control panel's own /api/import_join_request, so
// the admin check, the roster change and the delivery back to the device all
// stay in nvpn's code, the same way dns.go leaves the DNS switch to the panel.
//
// Nothing here approves on its own: pasting a link shows the device ID, and
// the page's Add button is what sends it to the panel. That review is a page
// flow, not a server-side lock: a POST that already carries confirm=yes
// imports directly, so what actually stands in front of the panel is the
// route's guards, the same as /dns: POST only, a loopback Host, this page's
// own origin, and the per-process CSRF token.
//
// The link is not harmless to pass around. It carries the device's
// requestSecret in the clear, the token the device uses to accept an
// approval at a point where it can't yet check who approved it (nvpn redacts
// it everywhere it logs or stores it). So the copy here treats it like a
// one-time password: hand it over directly, not through chat or email.

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	joinLinkPrefix = "nvpn://join-request/"
	// nostr-identity refuses to encode a link longer than this
	// (NOSTR_IDENTITY_DEVICE_APPROVAL_BOOTSTRAP_MAX_URI_LENGTH), so anything
	// longer did not come from a Nostr VPN app.
	joinLinkMaxLen = 384
	// The longest device name the review page shows.
	joinLabelMaxLen = 40
)

// joinLink is what the review step shows. The payload is unpadded base64url
// JSON written by nostr-identity's encode_nostr_identity_device_approval_bootstrap.
// The hub only reads it to show the reader which device they are about to add;
// the panel parses and checks it again, and its answer is the one that counts.
type joinLink struct {
	DeviceNpub  string `json:"deviceAppKeyNpub"`
	RequestNpub string `json:"requestNpub"`
	Secret      string `json:"requestSecret"`
	Label       string `json:"label,omitempty"`
}

// parseJoinLink accepts the link with stray whitespace around it, which is
// what a paste from a chat app or a terminal usually brings.
func parseJoinLink(raw string) (string, joinLink, error) {
	link := strings.TrimSpace(raw)
	var j joinLink
	if link == "" {
		return "", j, errors.New("Paste the device's join link first.")
	}
	if len(link) > joinLinkMaxLen {
		return "", j, errors.New("That is too long to be a Nostr VPN join link.")
	}
	// Match and cut on the same bytes of the same string. Lowercasing the
	// whole link first can change its length (some runes fold to a shorter
	// encoding), which would shift where the payload starts.
	n := len(joinLinkPrefix)
	if len(link) < n || strings.ToLower(link[:n]) != joinLinkPrefix {
		return "", j, errors.New("That isn't a Nostr VPN join link. It should start with nvpn://join-request/")
	}
	rest := link[n:]
	payload, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return "", j, errors.New("That join link is damaged. Copy it again from the device.")
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil || !npubRe.MatchString(j.DeviceNpub) || j.Secret == "" {
		return "", j, errors.New("That join link is damaged. Copy it again from the device.")
	}
	// Whoever made the QR code wrote the label, and the review page shows it
	// next to the Add button, so it is only ever a plain short name.
	j.Label = cleanName(j.Label, joinLabelMaxLen)
	return joinLinkPrefix + rest, j, nil
}

// cleanName makes a name safe to show where someone decides whether to add
// a device: control and format characters (bidi overrides, zero-width
// spaces) and broken UTF-8 are dropped, runs of space become one, and it is
// cut to max runes, never inside one.
func cleanName(s string, max int) string {
	var b strings.Builder
	n, space := 0, false
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == utf8.RuneError || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs):
			continue
		case unicode.IsSpace(r):
			space = true
			continue
		}
		if space && n > 0 {
			if n+1 >= max {
				break
			}
			b.WriteByte(' ')
			n++
		}
		space = false
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// addDevicePage is the review step and the result of a failed add.
type addDevicePage struct {
	Link        string
	Device      string // the full device npub
	Label       string
	NetworkName string
	CSRF        string
	Error       string
	Joined      bool // the device is already on the mesh; nothing to add
	Return      string
}

var addDeviceTmpl = template.Must(template.New("add").Parse(addDeviceHTML))

// joinReturn keeps the reader where they started: the wizard's approval step
// or the dashboard. Only these two, so the form cannot be used to redirect
// anywhere else.
func joinReturn(v string) string {
	if v == "setup" {
		return "/setup?step=3"
	}
	return "/dashboard#add-device"
}

func (c config) serveAddDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !c.hostAllowed(r.Host) || !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(c.csrfToken)) != 1 {
		http.Error(w, "this form has expired; reload the page and try again", http.StatusForbidden)
		return
	}
	from := r.PostForm.Get("from")
	view := addDevicePage{CSRF: c.csrfToken, Return: joinReturn(from)}

	link, j, err := parseJoinLink(r.PostForm.Get("request"))
	if err != nil {
		view.Error = err.Error()
		renderAdd(w, view, http.StatusBadRequest)
		return
	}
	view.Link, view.Device, view.Label = link, j.DeviceNpub, j.Label

	// Decide from fresh state, never from what a page showed earlier.
	p := c.buildPage(r.Context())
	view.NetworkName = p.NetworkName
	already, err := checkAddDevice(p, j)
	if err != nil {
		view.Error = err.Error()
		renderAdd(w, view, http.StatusConflict)
		return
	}
	if already {
		// Say so: a silent bounce back looks the same as "nothing happened".
		view.Joined = true
		renderAdd(w, view, http.StatusOK)
		return
	}

	// The review step. The page's Add button posts the same link back with
	// confirm=yes; without it, nothing is sent to the panel.
	if r.PostForm.Get("confirm") != "yes" {
		renderAdd(w, view, http.StatusOK)
		return
	}

	// Not tied to the browser, as in dns.go: if the tab closes mid-request the
	// panel may still add the device, and the log must say what it answered.
	ctx := context.WithoutCancel(r.Context())
	if err := c.importJoinRequest(ctx, link, j.DeviceNpub); err != nil {
		logAddFailure("device", shortNpub(j.DeviceNpub), err)
		view.Error = "Couldn't add the device. " + plainPanelError(err)
		renderAdd(w, view, http.StatusBadGateway)
		return
	}
	log.Printf("hub: added device %s", shortNpub(j.DeviceNpub))
	http.Redirect(w, r, view.Return, http.StatusSeeOther)
}

// checkAddDevice decides, from p, whether the device a join link names can be
// added here. already: it is on the network, so there is nothing to do. The
// dashboard and the terminal setup (termsetup.go) both ask it.
func checkAddDevice(p page, j joinLink) (already bool, err error) {
	if blocked := addBlocked(p); blocked != "" {
		return false, errors.New("Nothing was changed. " + blocked)
	}
	for _, d := range p.Devices {
		if d.Npub == j.DeviceNpub {
			return true, nil
		}
	}
	return false, nil
}

// addBlocked says why this box can't add a device right now, or "" when it can.
func addBlocked(p page) string {
	switch {
	case p.network == nil && p.noNetwork != "":
		return p.noNetwork
	case p.network == nil:
		return "The control panel isn't answering, so no device can be added until it is back."
	case !p.network.LocalIsAdmin:
		return "Only a network admin can add devices, and this box isn't one."
	}
	return ""
}

func renderAdd(w http.ResponseWriter, view addDevicePage, status int) {
	noFraming(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := addDeviceTmpl.Execute(w, view); err != nil {
		log.Printf("hub: rendering add-device page: %v", err)
	}
}

// importJoinRequest hands the link to the panel and then checks that the
// device is really in the network it returns, so an accepted request that
// added nobody is reported as a failure. A fresh request: nothing from the
// browser is forwarded (see setNetworkDNS).
func (c config) importJoinRequest(ctx context.Context, link, deviceNpub string) error {
	if c.mock != "" {
		return errors.New("the hub is in mock mode (HUB_MOCK), so nothing was sent")
	}
	body, err := json.Marshal(map[string]string{"request": link})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, switchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.panelAPI+"/api/import_join_request", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: switchTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return errors.New("this control panel build can't import join requests (needs nostr-vpn with /api/import_join_request)")
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil && apiErr.Error != "" {
			return errors.New(apiErr.Error)
		}
		return fmt.Errorf("control panel returned %s", resp.Status)
	}
	var state tick
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return fmt.Errorf("control panel reply: %w", err)
	}
	for _, n := range state.Networks {
		for _, p := range n.Participants {
			if p.Npub == deviceNpub {
				return nil
			}
		}
	}
	return errors.New("the control panel accepted the request but doesn't list the device")
}

const addDeviceHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Add a device · Sovereign-DNS</title>
<style>
  :root {
    --bg: #0f1115; --panel: #171a21; --line: #262b36; --ink: #e6e9ef;
    --dim: #9aa4b8; --up: #46c07a; --down: #e5484d; --warn: #e8a33d;
    --accent: #6ea8fe;
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--ink);
         font: 16px/1.6 ui-sans-serif, -apple-system, "Segoe UI", Roboto, sans-serif; }
  main { max-width: 640px; margin: 0 auto; padding: 48px 24px 72px; }
  .brand { color: var(--dim); font-size: 13px; text-transform: uppercase; letter-spacing: 0.09em; margin: 0 0 22px; }
  h1 { font-size: 28px; line-height: 1.2; margin: 0 0 10px; letter-spacing: -0.02em; }
  .lede { color: var(--dim); margin: 0 0 22px; }
  .card { background: var(--panel); border: 1px solid var(--line); border-radius: 12px; padding: 18px 20px; margin: 0 0 18px; }
  .label { color: var(--dim); font-size: 12px; text-transform: uppercase; letter-spacing: 0.08em; font-weight: 600; }
  .name { font-size: 20px; font-weight: 700; margin: 2px 0 12px; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px; overflow-wrap: anywhere; }
  .err { border-left: 3px solid var(--down); padding: 10px 14px; background: #1d1416; border-radius: 0 8px 8px 0; margin: 0 0 20px; }
  .fine { color: var(--dim); font-size: 14px; }
  .actions { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 6px; }
  form { margin: 0; }
  .btn { font: inherit; font-size: 15px; font-weight: 600; line-height: 1.2; border-radius: 9px; padding: 11px 20px;
         cursor: pointer; text-decoration: none; display: inline-block; border: 1px solid var(--line);
         background: #1f2430; color: var(--ink); }
  .btn:hover { border-color: var(--accent); }
  .btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .btn.primary { background: var(--accent); border-color: var(--accent); color: #0b1220; }
</style>
</head>
<body>
<main>
  <p class="brand">Sovereign-DNS · add a device</p>
  {{if .Error}}<div class="err" role="alert">{{.Error}}</div>{{end}}
  {{if .Joined}}
  <h1>This device is already on your mesh</h1>
  <p class="lede">Nothing was changed. It's already in {{if .NetworkName}}{{.NetworkName}}{{else}}your network{{end}}, so there's nothing to add.</p>
  {{else if .Device}}
  <h1>Add this device{{if .NetworkName}} to {{.NetworkName}}{{end}}?</h1>
  <p class="lede">It joins your mesh and starts using this box for DNS. Only add a device you recognise.</p>
  {{end}}
  {{if .Device}}
  <div class="card">
    <div class="label">Device</div>
    <div class="name">{{if .Label}}{{.Label}}{{else}}Unnamed device{{end}}</div>
    <div class="label">Device ID</div>
    <code>{{.Device}}</code>
    {{if not .Joined}}<p class="fine" style="margin:12px 0 0">Check that this matches the new device's own ID. In the Nostr VPN app: <em>Join Network</em> → <em>Manual join</em>. On a computer: <code>nvpn status</code>, the <code>device_id</code> line.</p>{{end}}
  </div>
  <div class="actions">
    {{if not (or .Error .Joined)}}
    <form method="post" action="/devices/add">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <input type="hidden" name="request" value="{{.Link}}">
      <input type="hidden" name="confirm" value="yes">
      <input type="hidden" name="from" value="{{if eq .Return "/setup?step=3"}}setup{{end}}">
      <button class="btn primary" type="submit" autofocus>Add device</button>
    </form>
    {{end}}
    <a class="btn{{if .Joined}} primary{{end}}" href="{{.Return}}">{{if .Joined}}Done{{else}}Cancel{{end}}</a>
  </div>
  {{else}}
  <h1>That link didn't work</h1>
  <div class="actions"><a class="btn" href="{{.Return}}">Back</a></div>
  {{end}}
</main>
</body>
</html>
`

// addCardHTML is the "Add a device" card, shared by the dashboard and the
// wizard's step 2. It needs .CSRF and a "from" value; it is defined as a
// named template so both pages render exactly the same form.
//
// The Scan QR code button runs scanJS (scan.go): it takes a photo of the code
// (a file picker on a computer) and reads it in the page. Without JavaScript the card
// is paste-only. Pasting or scanning a join link opens the review page by
// itself, so the reader's one deliberate click is Add there.
const addCardHTML = `{{define "addcss"}}
  .add-label { display: block; font-weight: 600; margin: 0 0 6px; }
  .add-form textarea { width: 100%; resize: vertical; background: #0b0d11; color: var(--ink);
    border: 1px solid var(--line); border-radius: 8px; padding: 10px 12px;
    font: 13px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; }
  .add-form textarea:focus { outline: 2px solid var(--accent); outline-offset: 1px; border-color: var(--accent); }
  .add-actions { display: flex; gap: 10px; flex-wrap: wrap; margin: 10px 0 0; }
  .add-form .btn { font: inherit; font-size: 14px; font-weight: 600; line-height: 1.2;
    border-radius: 8px; padding: 9px 16px; cursor: pointer; border: 1px solid var(--line);
    background: #1f2430; color: var(--ink); }
  .add-form .btn:hover { border-color: var(--accent); }
  .add-form .btn.primary { background: var(--accent); border-color: var(--accent); color: #0b1220; }
  .add-note[hidden], .add-scan[hidden] { display: none; }
  .add-note { color: var(--dim); font-size: 14px; margin: 8px 0 0; }
  .add-how { border-top: 1px solid var(--line); margin-top: 16px; padding-top: 6px; font-size: 14px; }
  .add-how p { margin: 8px 0; }
  .add-how .add-fine { color: var(--dim); }
  .box-steps { padding-left: 22px; margin: 0; }
  .box-steps li { margin: 0 0 14px; }
  pre.copy { background: #0b0d11; border: 1px solid var(--line); border-radius: 8px; padding: 10px 12px; margin: 8px 0 0;
    white-space: pre-wrap; word-break: break-all; font: 13px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; }
  .copybtn { font: inherit; font-size: 13px; font-weight: 600; border-radius: 8px; padding: 6px 12px; margin: 8px 0 0;
    cursor: pointer; border: 1px solid var(--line); background: #1f2430; color: var(--ink); }
  .copybtn[hidden] { display: none; }
  .qr { width: 240px; max-width: 100%; margin: 10px 0 0; }
  .qr svg { display: block; width: 100%; height: auto; border-radius: 8px; }
  .qr[hidden] { display: none; }
{{end}}{{define "copyjs"}}
<script>
document.querySelectorAll('.copybtn').forEach(function (b) {
  if (!navigator.clipboard || b.dataset.ready) return;
  b.dataset.ready = '1';
  b.hidden = false;
  b.addEventListener('click', function () {
    navigator.clipboard.writeText(document.getElementById(b.dataset.copy).textContent.trim()).then(function () { b.textContent = 'Copied'; });
  });
});
</script>
{{end}}{{define "addcard"}}
<form class="add-form" method="post" action="/devices/add">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <input type="hidden" name="from" value="{{.AddFrom}}">
  <label class="add-label" for="join-link">Paste the new device's join link</label>
  <textarea id="join-link" name="request" rows="3" spellcheck="false" autocomplete="off"
            placeholder="nvpn://join-request/…" required></textarea>
  <div class="add-actions">
    <button class="btn primary" type="submit">Review device</button>
    <button class="btn add-scan" type="button" data-scan data-csrf="{{.CSRF}}" data-from="{{.AddFrom}}" hidden>Scan QR code</button>
  </div>
</form>
<div class="add-how">
  <p><strong>iPhone or Android:</strong> open Nostr VPN and go to <em>Join Network</em>. It shows
    a QR code: press <strong>Scan QR code</strong> here and take a photo of it
    (on a computer, pick a photo or screenshot of it). Easier to paste? Tap <em>Share</em> and AirDrop it to this computer, or <em>Copy Request</em>,
    and paste it above.</p>
  <p><strong>Mac, Windows or Linux:</strong> with Nostr VPN installed, run
    <code>nvpn join-request --no-qr</code> and paste the line it prints.</p>
  <p class="add-fine">Treat the link like a one-time password: it's good for one join. Hand it
    over directly (AirDrop, a cable, or typing it), not through chat or email. On its own it
    changes nothing on this box: you see which device it names and press Add. Adding it is the
    approval.</p>
</div>
<script>
(function () {
  var form = document.currentScript.previousElementSibling.previousElementSibling;
  var box = form.querySelector('textarea');
  var prefix = 'nvpn://join-request/';
  function looksRight(v) { return v.trim().toLowerCase().indexOf(prefix) === 0; }
  box.addEventListener('paste', function () {
    setTimeout(function () { if (looksRight(box.value)) form.submit(); }, 0);
  });
})();
</script>
<script src="/assets/scan.js"></script>
{{end}}`
