package main

// A second DNS box: the same install on another machine, joined to the first
// box's network so both can answer the mesh.
//
// Every box founds its own network on its first start (nvpn/entrypoint.sh),
// so a second box has to leave that one and join the first box's. That is
// nvpn's manual join, and it takes one hand-over in each direction:
//
//   1. The first box shows its join code: its device ID and its network ID.
//      Pasted into the second box's setup, the hub hands both to the panel's
//      /api/manual_add_network. The panel switches the box to that network
//      and it waits, trusting nothing but those two IDs.
//   2. The second box then shows its own device ID. Pasted into the first
//      box's "Add your next device", the hub hands it to the panel's
//      /api/add_participant. The first box signs a roster naming it and
//      delivers it; the second box accepts it because the first box's ID is
//      the admin it was told to expect.
//
// Step 2 is the approval, the same as Add for a phone's join link: nothing
// joins until the person at the first box pastes that ID. Step 1 on its own
// changes only the second box, and only before anything depends on it: a box
// that already has devices of its own refuses, because leaving its network
// would cut them off.
//
// Measured against two throwaway boxes on the pinned nvpn (a8ffb3ca): after
// step 1 the second box lists the first as "waiting for admin" and its own
// entry has rosterAccepted false; after step 2 it reads true and the first box
// shows the second online. The first box never sees an inbound request for a
// manual join, so its side is driven by the ID, not by a request list.

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// An npub is "npub1" plus 58 bech32 characters. The panel decodes it again
// and its answer is the one that counts; this only catches a wrong paste
// before anything is sent.
var (
	npubRe      = regexp.MustCompile(`^npub1[02-9ac-hj-np-z]{58}$`)
	networkIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// joinCode is what the first box shows for a second box to paste: its device
// ID and network ID, one line, so it copies in one go.
func joinCode(npub, networkID string) string {
	if npub == "" || networkID == "" {
		return ""
	}
	return npub + ":" + networkID
}

// joinCodeQRPrefix marks the join code in the QR code the first box shows,
// so a scanner can tell it from a device ID or a phone's join link.
const joinCodeQRPrefix = "sdns://join/"

// parseJoinCode takes the code from joinCode, or read from its QR code. It
// also takes the two IDs pasted with a space or a new line between them,
// which is how they look when copied from `nvpn status` or the panel instead.
func parseJoinCode(raw string) (npub, networkID string, err error) {
	s := strings.TrimSpace(raw)
	// Match and cut on the same bytes, as in parseJoinLink.
	if n := len(joinCodeQRPrefix); len(s) > n && strings.ToLower(s[:n]) == joinCodeQRPrefix {
		s = s[n:]
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ':' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	if len(fields) != 2 {
		return "", "", errors.New("That isn't a join code. Copy it again from the first box: it is one line, npub1… then a colon and the network ID.")
	}
	npub, networkID = strings.ToLower(fields[0]), fields[1]
	if !npubRe.MatchString(npub) {
		return "", "", errors.New("The first part of that code isn't a device ID (npub1…). Copy the code again from the first box.")
	}
	if !networkIDRe.MatchString(networkID) {
		return "", "", errors.New("The part after the colon isn't a network ID. Copy the code again from the first box.")
	}
	return npub, networkID, nil
}

// parseBoxID takes a box's ID as pasted, or as read from its QR code
// (deviceQRPrefix, scan.go). A "nostr:" prefix, which some apps add to a
// copied npub, is dropped too.
func parseBoxID(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	// Match and cut on the same bytes, as in parseJoinLink.
	if n := len(deviceQRPrefix); len(s) > n && strings.ToLower(s[:n]) == deviceQRPrefix {
		s = s[n:]
		if i := strings.IndexAny(s, "?#"); i >= 0 {
			s = s[:i]
		}
	} else if n := len("nostr:"); len(s) > n && strings.ToLower(s[:n]) == "nostr:" {
		s = s[n:]
	}
	npub := strings.ToLower(strings.TrimSpace(s))
	if npub == "" {
		return "", errors.New("Paste the new box's device ID first.")
	}
	if !npubRe.MatchString(npub) {
		return "", errors.New("That isn't a device ID. On the new box's setup it is the line that starts with npub1, 63 characters long.")
	}
	return npub, nil
}

// boxRole says whether this box is the first one or joining another, from
// what the panel reports, and falls back to what the reader picked in the
// wizard only while the facts can't tell. Facts win so that a reader who
// picked wrong is not walked through steps that would break the box.
func boxRole(p page, picked string) string {
	switch {
	case p.network != nil && !p.network.LocalIsAdmin:
		return "second" // it already asked to join another box's network
	case len(p.Devices) > 0 || p.PendingJoins > 0:
		return "first" // devices depend on this box's own network
	case picked == "first" || picked == "second":
		return picked
	}
	return ""
}

// formGuard is the shared front of every route that changes something: POST
// only, a legitimate Host and origin, a small form and this process's token
// (see dns.go). It writes the refusal itself and reports whether to go on.
func (c config) formGuard(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !c.hostAllowed(r.Host) || !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(c.csrfToken)) != 1 {
		http.Error(w, "this form has expired; reload the page and try again", http.StatusForbidden)
		return false
	}
	return true
}

// boxResultPage is what a refused or failed join or add shows.
type boxResultPage struct {
	Title  string
	Error  string
	Return string
}

func renderBoxResult(w http.ResponseWriter, v boxResultPage, status int) {
	noFraming(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := boxResultTmpl.Execute(w, v); err != nil {
		log.Printf("hub: rendering box result page: %v", err)
	}
}

// serveJoinFirstBox is step 1 above, on the second box.
func (c config) serveJoinFirstBox(w http.ResponseWriter, r *http.Request) {
	if !c.formGuard(w, r) {
		return
	}
	back := "/setup?step=3"
	if err := c.joinFirstBox(r.Context(), r.PostForm.Get("code")); err != nil {
		var ae *actionError
		status := http.StatusBadGateway
		if errors.As(err, &ae) {
			status = ae.status
		}
		renderBoxResult(w, boxResultPage{Title: "This box didn't join", Error: err.Error(), Return: back}, status)
		return
	}
	http.Redirect(w, r, "/setup?step=4", http.StatusSeeOther)
}

// actionError is a refusal or failure worded for the person at the box, with
// the HTTP status the dashboard answers it with. The terminal setup
// (termsetup.go) prints the same words.
type actionError struct {
	status int
	msg    string
}

func (e *actionError) Error() string { return e.msg }

func refuse(status int, msg string) error { return &actionError{status, msg} }

// joinFirstBox is step 1 above, on the second box: the checks and the one
// panel write behind both the setup page's Join button and the terminal
// setup. It decides from fresh state, never from what a page showed.
func (c config) joinFirstBox(ctx context.Context, rawCode string) error {
	admin, networkID, err := parseJoinCode(rawCode)
	if err != nil {
		return refuse(http.StatusBadRequest, err.Error())
	}

	p := c.buildPage(ctx)
	switch {
	case p.network == nil && p.noNetwork == "":
		return refuse(http.StatusConflict, "Nothing was changed. The control panel isn't answering, so this box can't join yet.")
	case p.OwnNpub != "" && p.OwnNpub == admin:
		return refuse(http.StatusBadRequest, "Nothing was changed. That is this box's own join code. Paste the code from your first box.")
	case p.network != nil && !p.network.LocalIsAdmin:
		return refuse(http.StatusConflict, "Nothing was changed. This box has already asked to join a network. Finish that on the first box, or reinstall this box to start over.")
	case len(p.Devices) > 0 || p.PendingJoins > 0:
		return refuse(http.StatusConflict, "Nothing was changed. Devices already use this box's own network, and joining another one would cut them off. This is a first box: add other devices to it instead.")
	}

	// Not tied to the caller: if the tab closes mid-request the panel may
	// still switch, and the log must say what it answered.
	ctx = context.WithoutCancel(ctx)
	state, err := c.panelWrite(ctx, "/api/manual_add_network", map[string]any{"adminNpub": admin, "meshNetworkId": networkID})
	if err == nil {
		err = checkJoined(state, networkID)
	}
	if err != nil {
		log.Printf("hub: joining network %s of %s failed: %s (panel: %v)", networkID, shortNpub(admin), plainPanelError(err), err)
		return refuse(http.StatusBadGateway, "Couldn't join. "+plainPanelError(err))
	}
	log.Printf("hub: asked to join network %s of %s", networkID, shortNpub(admin))
	return nil
}

// checkJoined: the panel's answer has to show the box running the first
// box's network, or the join is reported as failed.
func checkJoined(state tick, networkID string) error {
	for _, n := range state.Networks {
		if n.NetworkID == networkID && n.Enabled && !n.LocalIsAdmin {
			return nil
		}
	}
	return errors.New("the control panel accepted the code but isn't running that network")
}

// serveAddBox is step 2 above, on the first box.
func (c config) serveAddBox(w http.ResponseWriter, r *http.Request) {
	if !c.formGuard(w, r) {
		return
	}
	back := joinReturn(r.PostForm.Get("from"))
	fail := func(err error) {
		var ae *actionError
		status := http.StatusBadGateway
		if errors.As(err, &ae) {
			status = ae.status
		}
		renderBoxResult(w, boxResultPage{Title: "The box wasn't added", Error: err.Error(), Return: back}, status)
	}
	p := c.buildPage(r.Context())
	npub, asBackup, already, err := checkAddBox(p, r.PostForm.Get("box"))
	if err != nil {
		fail(err)
		return
	}
	if already {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	name := cleanName(r.PostForm.Get("name"), 40)
	if name == "" {
		name = "DNS box 2"
	}

	// The review step, as for a phone's join link: a pasted or scanned ID
	// shows which box it names, and the Add button there posts it back with
	// confirm=yes. Without it, nothing is sent to the panel.
	if r.PostForm.Get("confirm") != "yes" {
		renderBoxReview(w, boxReviewPage{
			Box: npub, Name: name, NetworkName: p.NetworkName, CSRF: c.csrfToken,
			From: r.PostForm.Get("from"), Return: back, AsBackup: asBackup,
		})
		return
	}
	if err := c.addBox(r.Context(), p, npub, name, asBackup); err != nil {
		fail(err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// checkAddBox reads a pasted or scanned box ID and decides, from p, whether
// it can be added and whether as a backup DNS box. already: it is on the
// network in the role it would get, so there is nothing to do.
func checkAddBox(p page, raw string) (npub string, asBackup, already bool, err error) {
	npub, err = parseBoxID(raw)
	if err != nil {
		return "", false, false, refuse(http.StatusBadRequest, err.Error())
	}
	if blocked := addBlocked(p); blocked != "" {
		return "", false, false, refuse(http.StatusConflict, "Nothing was changed. "+blocked)
	}
	if p.OwnNpub != "" && npub == p.OwnNpub {
		return "", false, false, refuse(http.StatusBadRequest, "Nothing was changed. That is this box's own ID. Paste the ID the new box shows.")
	}
	asBackup = addsAsBackup(p.network, npub)
	if asBackup && p.network.RemovedDevices == nil {
		// The panel would take the add and drop dnsBackup unread, leaving the
		// box a plain member that answers nothing: refuse instead.
		return "", false, false, refuse(http.StatusConflict, "Nothing was changed. This box's VPN control panel is too old to add a box as a backup DNS box in one step. Update Sovereign-DNS on this box (run the install command again), then add the box.")
	}
	for _, d := range p.Devices {
		if d.Npub == npub && (d.Role != "" || !asBackup) {
			return npub, asBackup, true, nil
		}
	}
	return npub, asBackup, false, nil
}

// addBox is the approval itself, after the review: one write, so the panel
// adds the box and makes it a backup in the same signed roster and it is
// never a plain member in between.
func (c config) addBox(ctx context.Context, p page, npub, name string, asBackup bool) error {
	fields := map[string]any{"networkId": p.network.ID, "npub": npub, "alias": name}
	if asBackup {
		fields["dnsBackup"] = true
	}
	ctx = context.WithoutCancel(ctx)
	state, err := c.panelWrite(ctx, "/api/add_participant", fields)
	if err == nil {
		err = checkBoxAdded(state, npub, asBackup)
	}
	if err != nil {
		logAddFailure("box", shortNpub(npub), err)
		return refuse(http.StatusBadGateway, "Couldn't add the box. "+plainPanelError(err))
	}
	if asBackup {
		log.Printf("hub: added box %s as a backup DNS box", shortNpub(npub))
	} else {
		log.Printf("hub: added box %s", shortNpub(npub))
	}
	return nil
}

// addsAsBackup: a box joins as a backup DNS box whenever the network has a
// DNS box for it to back up, which is every network this stack sets up. With
// none (network DNS never turned on), nvpn refuses a backup, so it joins as
// a plain member.
func addsAsBackup(net *tickNetwork, npub string) bool {
	return net != nil && net.DNSNodeNpub != "" && net.DNSNodeNpub != npub
}

var errBoxNotBackup = errors.New("the control panel added the box but didn't make it a backup DNS box")

// checkBoxAdded reads the panel's answer: the box has to be listed, and when
// it was added as a backup, listed as one. An answer that says otherwise is
// a failure, not a success.
func checkBoxAdded(state tick, npub string, asBackup bool) error {
	for _, n := range state.Networks {
		for _, p := range n.Participants {
			if p.Npub != npub {
				continue
			}
			if asBackup && !p.ServesDNSBackup {
				return errBoxNotBackup
			}
			return nil
		}
	}
	return errors.New("the control panel accepted the ID but doesn't list the box")
}

// panelWrite posts one action to the control panel and decodes the state it
// answers with. A fresh request, as in setNetworkDNS: nothing from the
// browser's request is forwarded.
func (c config) panelWrite(ctx context.Context, path string, fields map[string]any) (tick, error) {
	var state tick
	if c.mock != "" {
		return state, errors.New("the hub is in mock mode (HUB_MOCK), so nothing was sent")
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return state, err
	}
	ctx, cancel := context.WithTimeout(ctx, switchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.panelAPI+path, bytes.NewReader(body))
	if err != nil {
		return state, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: switchTimeout}).Do(req)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return state, fmt.Errorf("this control panel build has no %s route", path)
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil && apiErr.Error != "" {
			return state, errors.New(apiErr.Error)
		}
		return state, fmt.Errorf("control panel returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return state, fmt.Errorf("control panel reply: %w", err)
	}
	return state, nil
}

// boxReviewPage is the review step for adding a box by its ID.
type boxReviewPage struct {
	Box         string
	Name        string
	NetworkName string
	CSRF        string
	From        string
	Return      string
	AsBackup    bool // it joins as a backup DNS box
}

func renderBoxReview(w http.ResponseWriter, v boxReviewPage) {
	noFraming(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := boxReviewTmpl.Execute(w, v); err != nil {
		log.Printf("hub: rendering box review page: %v", err)
	}
}

var boxReviewTmpl = template.Must(template.New("boxreview").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Add a box · Sovereign-DNS</title>
<style>
  body { margin: 0; background: #0f1115; color: #e6e9ef;
         font: 16px/1.6 ui-sans-serif, -apple-system, "Segoe UI", Roboto, sans-serif; }
  main { max-width: 640px; margin: 0 auto; padding: 48px 24px 72px; }
  .brand { color: #9aa4b8; font-size: 13px; text-transform: uppercase; letter-spacing: 0.09em; margin: 0 0 22px; }
  h1 { font-size: 28px; line-height: 1.2; margin: 0 0 10px; letter-spacing: -0.02em; }
  .lede { color: #9aa4b8; margin: 0 0 22px; }
  .card { background: #171a21; border: 1px solid #262b36; border-radius: 12px; padding: 18px 20px; margin: 0 0 18px; }
  .label { color: #9aa4b8; font-size: 12px; text-transform: uppercase; letter-spacing: 0.08em; font-weight: 600; display: block; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px; overflow-wrap: anywhere; }
  .fine { color: #9aa4b8; font-size: 14px; margin: 12px 0 0; }
  input[type=text] { background: #0b0d11; color: #e6e9ef; border: 1px solid #262b36; border-radius: 8px;
    padding: 8px 10px; font: inherit; font-size: 15px; width: 100%; max-width: 280px; margin: 4px 0 0; }
  .actions { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 6px; }
  .btn { font: inherit; font-size: 15px; font-weight: 600; line-height: 1.2; border-radius: 9px; padding: 11px 20px;
         cursor: pointer; text-decoration: none; display: inline-block; border: 1px solid #262b36;
         background: #1f2430; color: #e6e9ef; }
  .btn.primary { background: #6ea8fe; border-color: #6ea8fe; color: #0b1220; }
</style>
</head>
<body>
<main>
  <p class="brand">Sovereign-DNS · add a box</p>
  <h1>Add this box{{if .NetworkName}} to {{.NetworkName}}{{end}}?</h1>
  <p class="lede">{{if .AsBackup}}It joins your mesh as a backup DNS box: when this box is off, asleep or updating, it answers instead.{{else}}It joins your mesh as another device. This network has no DNS box yet, so it isn't made a backup.{{end}} Only add a box you set up yourself.</p>
  <form method="post" action="/devices/add-box">
    <div class="card">
      <span class="label">Box ID</span>
      <code>{{.Box}}</code>
      <p class="fine">Check that this matches the ID on the new box's setup page, under <em>Let the first box add it</em>.</p>
      <label class="label" for="box-name" style="margin-top:14px">Name it</label>
      <input type="text" id="box-name" name="name" value="{{.Name}}" maxlength="40">
    </div>
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <input type="hidden" name="box" value="{{.Box}}">
    <input type="hidden" name="from" value="{{.From}}">
    <input type="hidden" name="confirm" value="yes">
    <div class="actions">
      <button class="btn primary" type="submit" autofocus>Add box</button>
      <a class="btn" href="{{.Return}}">Cancel</a>
    </div>
  </form>
</main>
</body>
</html>
`))

var boxResultTmpl = template.Must(template.New("box").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Sovereign-DNS</title>
<style>
  body { margin: 0; background: #0f1115; color: #e6e9ef;
         font: 16px/1.6 ui-sans-serif, -apple-system, "Segoe UI", Roboto, sans-serif; }
  main { max-width: 640px; margin: 0 auto; padding: 48px 24px 72px; }
  .brand { color: #9aa4b8; font-size: 13px; text-transform: uppercase; letter-spacing: 0.09em; margin: 0 0 22px; }
  h1 { font-size: 28px; line-height: 1.2; margin: 0 0 16px; letter-spacing: -0.02em; }
  .err { border-left: 3px solid #e5484d; padding: 10px 14px; background: #1d1416; border-radius: 0 8px 8px 0; margin: 0 0 20px; overflow-wrap: anywhere; }
  a.btn { display: inline-block; font-weight: 600; border-radius: 9px; padding: 11px 20px; text-decoration: none;
          border: 1px solid #262b36; background: #1f2430; color: #e6e9ef; }
</style>
</head>
<body>
<main>
  <p class="brand">Sovereign-DNS</p>
  <h1>{{.Title}}</h1>
  <div class="err" role="alert">{{.Error}}</div>
  <a class="btn" href="{{.Return}}">Back</a>
</main>
</body>
</html>
`))

// boxCardHTML is the first box's half of adding another DNS box, shared by
// the dashboard and the wizard's add step like addCardHTML. It needs .CSRF,
// .JoinCode and .AddFrom.
const boxCardHTML = `{{define "boxcard"}}
<ol class="todo box-steps">
  <li>Install Sovereign-DNS on the other machine with the same one-line command, open its setup, and pick <em>I already have a box</em>.</li>
  <li>On the other box press <strong>Scan the first box's code</strong> and take a photo of this QR code, or paste the join code under it:
    <div class="qr" data-qr="sdns://join/{{.JoinCode}}" hidden aria-label="This box's join code as a QR code" role="img"></div>
    <pre class="copy" id="join-code">{{.JoinCode}}</pre>
    <button class="btn copybtn" type="button" data-copy="join-code" hidden>Copy</button>
  </li>
  <li>It then shows a QR code with its own ID. Press <strong>Scan QR code</strong> here and take a photo of it (on a computer, pick a photo or screenshot of it). Or paste the ID it shows instead. Either way you see the box's ID and press Add: that is the approval, and nothing joins until you do. {{if .HasDNSBox}}That one Add also makes it a backup DNS box, so it answers when this box is off.{{end}}
    <div class="add-actions" style="margin-bottom:10px">
      <button class="btn primary add-scan" type="button" data-scan data-csrf="{{.CSRF}}" data-from="{{.AddFrom}}" hidden>Scan QR code</button>
    </div>
    <form class="add-form" method="post" action="/devices/add-box">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <input type="hidden" name="from" value="{{.AddFrom}}">
      <textarea name="box" rows="2" spellcheck="false" autocomplete="off" placeholder="npub1…" required aria-label="The new box's ID"></textarea>
      <div class="add-actions"><button class="btn" type="submit">Review this box</button></div>
    </form>
  </li>
</ol>
<p class="add-note">The join code is not a password. It only tells the new box which network to ask for, and on its own it lets nobody in.</p>
{{template "copyjs"}}
<script src="/assets/scan.js"></script>
{{template "qrjs"}}
{{end}}{{define "qrjs"}}
<script src="/assets/qrcode.js"></script>
<script>
document.querySelectorAll('[data-qr]').forEach(function (el) {
  if (typeof qrcode !== 'function' || el.firstChild) return;
  var q = qrcode(0, 'M');
  q.addData(el.dataset.qr);
  q.make();
  el.innerHTML = q.createSvgTag({cellSize: 5, margin: 20, scalable: true});
  el.hidden = false;
});
</script>
{{end}}`

// serveReAdd puts back a device this box removed, from the dashboard's
// Removed devices list. Only a device on that list: this is not a way to add
// an ID nobody reviewed. backup=yes re-adds it as a backup DNS box, in one
// roster as for a new box.
func (c config) serveReAdd(w http.ResponseWriter, r *http.Request) {
	if !c.formGuard(w, r) {
		return
	}
	back := "/dashboard#removed"
	fail := func(status int, msg string) {
		renderBoxResult(w, boxResultPage{Title: "The device wasn't re-added", Error: msg, Return: back}, status)
	}
	npub, err := parseBoxID(r.PostForm.Get("npub"))
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	p := c.buildPage(r.Context())
	if blocked := addBlocked(p); blocked != "" {
		fail(http.StatusConflict, "Nothing was changed. "+blocked)
		return
	}
	var removed *tickRemoved
	for i := range p.Removed {
		if p.Removed[i].Npub == npub {
			removed = &p.Removed[i]
		}
	}
	if removed == nil {
		fail(http.StatusConflict, "Nothing was changed. That device isn't on this box's list of removed devices any more. Reload the dashboard.")
		return
	}
	asBackup := r.PostForm.Get("backup") == "yes"
	if asBackup && !addsAsBackup(p.network, npub) {
		fail(http.StatusConflict, "Nothing was changed. This network has no DNS box yet, so the device can't come back as its backup. Re-add it as a plain device instead.")
		return
	}
	fields := map[string]any{"networkId": p.network.ID, "npub": npub, "alias": removed.Alias}
	if asBackup {
		fields["dnsBackup"] = true
	}
	ctx := context.WithoutCancel(r.Context())
	state, err := c.panelWrite(ctx, "/api/add_participant", fields)
	if err == nil {
		err = checkBoxAdded(state, npub, asBackup)
	}
	if err != nil {
		logAddFailure("removed device", shortNpub(npub), err)
		fail(http.StatusBadGateway, "Couldn't re-add the device. "+plainPanelError(err))
		return
	}
	log.Printf("hub: re-added %s%s", shortNpub(npub), map[bool]string{true: " as a backup DNS box"}[asBackup])
	http.Redirect(w, r, back, http.StatusSeeOther)
}
