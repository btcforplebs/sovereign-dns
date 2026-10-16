package main

// The setup wizard: what a newcomer sees first, at http://localhost:8080,
// after install.sh finishes. Each step is a question with a plain answer and
// the single thing to do next. Step 2 asks which box this is, and the rest
// follow from the answer:
//
//   First box                          Another box (boxjoin.go)
//   1. Is everything running?          1. Is everything running?
//   2. First box or another one?       2. First box or another one?
//   3. Add your next device            3. Join your first box
//      (a phone, or another DNS box)
//   4. Approve it                      4. Let the first box add it
//   5. Does it really use the Pi-hole?
//
// It is a view over the same page struct the dashboard renders from, so it
// never probes anything itself and cannot disagree with the dashboard. Like
// the rest of the hub it changes nothing itself: every change goes through
// the control panel, which owns it.

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
)

type wizardStep struct {
	N       int
	Key     string
	Title   string
	Done    bool
	Current bool
}

type wizardPage struct {
	page
	Role      string // "first", "second", or "" until the reader picks
	Kind      string // on the first box's add step: "phone" or "box"
	Steps     []wizardStep
	Step      int    // the step being shown
	Key       string // which step that is
	Total     int
	StepDone  bool   // whether the shown step is satisfied
	AllDone   bool   // every step satisfied
	PrevURL   string // "" on the first step
	PrevStep  int
	NextURL   string // "" on the last step
	StateKey  string // changes when anything the wizard shows changes; the page polls for it
	PiholeLog string // Pi-hole query log, or "" when the mesh IP is not known yet
}

type stepDef struct {
	key, title string
	done       func(p page) bool
}

var (
	stepRunning = stepDef{"running", "Is everything running?", func(p page) bool {
		return p.DaemonUp && p.PanelUp && p.PiholeUp && p.UnboundUp
	}}
	firstBoxSteps = []stepDef{
		stepRunning,
		{"role", "First box or another one?", nil},
		{"add", "Add your next device", func(p page) bool { return p.PendingJoins > 0 || len(p.Devices) > 0 }},
		{"approve", "Approve it", func(p page) bool { return len(p.Devices) > 0 }},
		{"check", "Does it really use the Pi-hole?", func(p page) bool { return p.Covered > 0 }},
	}
	secondBoxSteps = []stepDef{
		stepRunning,
		{"role", "First box or another one?", nil},
		{"join", "Join your first box", func(p page) bool { return p.Member }},
		{"admitted", "Let the first box add it", func(p page) bool { return p.Admitted }},
	}
)

// roleCookie remembers the reader's answer to step 2 until the facts take
// over (boxRole). It only picks which steps are shown; nothing is changed by
// it, so a page that sets it from another tab can do no harm.
const roleCookie = "sdns_setup_role"

func buildWizard(p page, want int, picked, kind string) wizardPage {
	w := wizardPage{page: p, AllDone: true, Role: boxRole(p, picked)}
	defs := firstBoxSteps
	if w.Role == "second" {
		defs = secondBoxSteps
	}
	w.Total = len(defs)
	first := 0
	for i, d := range defs {
		done := w.Role != ""
		if d.done != nil {
			done = d.done(p)
		}
		if !done {
			w.AllDone = false
			if first == 0 {
				first = i + 1
			}
		}
		w.Steps = append(w.Steps, wizardStep{N: i + 1, Key: d.key, Title: d.title, Done: done})
	}
	if first == 0 {
		first = w.Total
	}
	// Land on the first thing that still needs doing, unless the reader asked
	// for a specific step (Back, or re-reading an earlier one).
	w.Step = want
	if w.Step < 1 || w.Step > w.Total {
		w.Step = first
	}
	cur := &w.Steps[w.Step-1]
	cur.Current = true
	w.Key = cur.Key
	w.StepDone = cur.Done
	if w.Step > 1 {
		w.PrevStep = w.Step - 1
		w.PrevURL = fmt.Sprintf("/setup?step=%d", w.PrevStep)
	}
	if w.Step < w.Total {
		w.NextURL = fmt.Sprintf("/setup?step=%d", w.Step+1)
	}
	w.Kind = "phone"
	if kind == "box" {
		w.Kind = "box"
	}
	if p.PiholeURL != "" {
		w.PiholeLog = p.PiholeURL + "/queries"
	}
	w.StateKey = stateKey(p) + "-" + w.Role
	return w
}

// stateKey is what the page compares to decide whether to reload. It covers
// exactly the facts the wizard renders, so a reload happens when the reader
// would see something different and not on every clock tick.
func stateKey(p page) string {
	return fmt.Sprintf("%t%t%t%t-%d-%d-%d-%t%t-%t%t",
		p.DaemonUp, p.PanelUp, p.PiholeUp, p.UnboundUp,
		p.PendingJoins, len(p.Devices), p.Covered, p.AddReady, p.PiholeURL != "",
		p.Member, p.Admitted)
}

func (c config) serveWizard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if role := q.Get("role"); role == "first" || role == "second" {
		http.SetCookie(w, &http.Cookie{Name: roleCookie, Value: role, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/setup?step=3", http.StatusSeeOther)
		return
	}
	picked := ""
	if ck, err := r.Cookie(roleCookie); err == nil {
		picked = ck.Value
	}
	data := c.buildPage(r.Context())
	want, _ := strconv.Atoi(q.Get("step"))
	data.CSRF = c.csrfToken
	c.viewer(&data, r)
	data.AddFrom = "setup"
	wz := buildWizard(data, want, picked, q.Get("kind"))
	if q.Get("format") == "key" {
		// The page polls this. Plain text, one line, cheap.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, wz.StateKey)
		return
	}
	noFraming(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := wizardTmpl.Execute(w, wz); err != nil {
		log.Printf("hub: rendering wizard: %v", err)
	}
}

var wizardTmpl = template.Must(template.Must(template.Must(template.New("wizard").Parse(wizardHTML)).Parse(addCardHTML)).Parse(boxCardHTML))

const wizardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Set up Sovereign-DNS · step {{.Step}} of {{.Total}}</title>
<style>
  :root {
    --bg: #0f1115; --panel: #171a21; --line: #262b36; --ink: #e6e9ef;
    --dim: #9aa4b8; --up: #46c07a; --down: #e5484d; --warn: #e8a33d;
    --accent: #6ea8fe; --accent-ink: #0b1220;
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--ink);
         font: 16px/1.6 ui-sans-serif, -apple-system, "Segoe UI", Roboto, sans-serif; }
  main { max-width: 720px; margin: 0 auto; padding: 40px 24px 72px; }
  .brand { color: var(--dim); font-size: 13px; text-transform: uppercase; letter-spacing: 0.09em; margin: 0 0 22px; }
  .brand a { color: var(--dim); text-decoration: none; float: right; text-transform: none; letter-spacing: 0; }
  .brand a:hover { color: var(--accent); }

  ol.rail { list-style: none; display: flex; gap: 6px; padding: 0; margin: 0 0 34px; }
  ol.rail li { flex: 1; font-size: 12px; color: var(--dim); border-top: 3px solid var(--line); padding-top: 8px; }
  ol.rail li.done { border-color: var(--up); }
  ol.rail li.current { border-color: var(--accent); color: var(--ink); font-weight: 600; }
  ol.rail li .n { display: block; font-size: 11px; letter-spacing: 0.06em; text-transform: uppercase; }
  ol.rail li.done .n::after { content: " ✓"; color: var(--up); }

  h1 { font-size: 30px; line-height: 1.2; margin: 0 0 10px; letter-spacing: -0.02em; }
  .lede { color: var(--dim); margin: 0 0 24px; font-size: 17px; }
  .card { background: var(--panel); border: 1px solid var(--line); border-radius: 12px; padding: 18px 20px; margin: 0 0 16px; }
  .row { display: flex; align-items: flex-start; gap: 12px; padding: 12px 0; border-top: 1px solid var(--line); }
  .row:first-child { border-top: 0; padding-top: 0; }
  .row:last-child { padding-bottom: 0; }
  .dot { width: 11px; height: 11px; border-radius: 50%; flex: none; margin-top: 8px; background: var(--line); }
  .up .dot { background: var(--up); } .down .dot { background: var(--down); } .warn .dot { background: var(--warn); }
  .row .name { font-weight: 600; }
  .row .what { color: var(--dim); font-size: 14px; }
  .row .state { margin-left: auto; flex: none; font-size: 14px; font-weight: 600; padding-top: 2px; }
  .up .state { color: var(--up); } .down .state { color: var(--down); } .warn .state { color: var(--warn); }

  .verdict { border-left: 3px solid var(--accent); padding: 4px 0 4px 16px; margin: 20px 0; }
  .verdict.ok { border-color: var(--up); }
  .verdict.wait { border-color: var(--warn); }
  .verdict strong { display: block; margin-bottom: 2px; }
  .verdict p { margin: 4px 0; }
  .verdict .fine { color: var(--dim); font-size: 14px; }
  .verdict.down { border-color: var(--down); }
  .verdict code { white-space: nowrap; }
  /* The dial error is one long token; the commands next to it are not. */
  .verdict .cause, .err { overflow-wrap: anywhere; }
  .err { border-left: 3px solid var(--down); padding: 10px 14px; margin: 20px 0; color: var(--dim); }

  .kind { margin: 0 0 10px; font-size: 13px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--dim); font-weight: 600; }
  .placeholder { border: 1px dashed var(--line); border-radius: 10px; padding: 14px 16px; color: var(--dim); font-size: 15px; }
  .placeholder strong { color: var(--ink); }

  code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 14px; }
  pre { background: #0b0d11; border: 1px solid var(--line); border-radius: 8px; padding: 12px 14px; overflow-x: auto; margin: 8px 0 0; white-space: pre-wrap; word-break: break-all; }
  ol.todo { padding-left: 22px; margin: 10px 0 0; }
  ol.todo li { margin: 8px 0; }
  .big { display: inline-block; background: var(--accent); color: var(--accent-ink); font-weight: 700; text-decoration: none; padding: 10px 18px; border-radius: 9px; }
  .big:hover { filter: brightness(1.08); }
  .badge { display: inline-block; background: var(--warn); color: #1a1205; border-radius: 999px; padding: 3px 11px; font-weight: 600; font-size: 13px; }
  a { color: var(--accent); }

  nav.steps { display: flex; justify-content: space-between; align-items: center; margin-top: 34px; padding-top: 18px; border-top: 1px solid var(--line); }
  nav.steps a { text-decoration: none; font-weight: 600; }
  nav.steps a.next { background: var(--accent); color: var(--accent-ink); padding: 10px 18px; border-radius: 9px; }
  nav.steps a.next.soft { background: transparent; color: var(--accent); border: 1px solid var(--line); }
  nav.steps a.prev { color: var(--dim); }
  nav.steps a.prev:hover { color: var(--ink); }
  nav.steps .spacer { flex: 1; }
  footer { color: var(--dim); font-size: 12px; margin-top: 30px; }
{{template "addcss"}}
  .choose { display: grid; gap: 12px; }
  .pick { display: block; border: 1px solid var(--line); border-radius: 10px; padding: 14px 16px; text-decoration: none; color: var(--ink); }
  .pick:hover, .pick.picked { border-color: var(--accent); }
  .pick span { display: block; color: var(--dim); font-size: 14px; }
  .tabs { display: flex; gap: 8px; margin: 0 0 14px; }
  .tab { border: 1px solid var(--line); border-radius: 999px; padding: 6px 14px; text-decoration: none; color: var(--dim); font-size: 14px; font-weight: 600; }
  .tab.on { border-color: var(--accent); color: var(--ink); }
</style>
</head>
<body>
<main data-key="{{.StateKey}}">
  <p class="brand">Sovereign-DNS · setup <a href="/dashboard">Skip to the dashboard →</a></p>

  <ol class="rail">
    {{range .Steps}}
    <li class="{{if .Current}}current{{else if .Done}}done{{end}}"><span class="n">Step {{.N}}</span>{{.Title}}</li>
    {{end}}
  </ol>

  {{/* The control panel is the source of device facts. Step 1 says what
       went wrong in full; every other step gets the one-line version so a
       reader who typed ?step=3 still learns why nothing is moving. */}}
  {{if and .Error (ne .Key "running")}}<div class="err">{{.Error}}</div>{{end}}

  {{/* ───────────────────────── STEP 1 ───────────────────────── */}}
  {{if eq .Key "running"}}
  <h1>Is everything running?</h1>
  <p class="lede">Your box has four parts. Each one has to be up before a device can use it.</p>

  <div class="card">
    <div class="row {{if .UnboundUp}}up{{else}}down{{end}}">
      <span class="dot"></span>
      <span><span class="name">Unbound</span> — the resolver<br>
        <span class="what">Finds answers itself, straight from the internet's root servers. No Google, no Cloudflare, nobody in between.</span></span>
      <span class="state">{{if .UnboundUp}}Running{{else}}Not yet{{end}}</span>
    </div>
    <div class="row {{if .PiholeUp}}up{{else}}down{{end}}">
      <span class="dot"></span>
      <span><span class="name">Pi-hole</span> — the filter<br>
        <span class="what">Every lookup from your devices passes through it. Ads and trackers get a dead end instead of an address.</span></span>
      <span class="state">{{if .PiholeUp}}Running{{else}}Not yet{{end}}</span>
    </div>
    <div class="row {{if .DaemonUp}}up{{else}}down{{end}}">
      <span class="dot"></span>
      <span><span class="name">VPN</span> — the private road<br>
        <span class="what">Carries your devices' lookups to this box wherever they are, at home or on cellular, with nothing opened on your router.</span></span>
      <span class="state">{{if .DaemonUp}}Running{{else}}Not yet{{end}}</span>
    </div>
    <div class="row {{if .PanelUp}}up{{else}}down{{end}}">
      <span class="dot"></span>
      <span><span class="name">Admin panel</span> — where you say yes<br>
        <span class="what">The page where devices get approved: <code>localhost:8082</code> on the box, or from another computer with the dashboard password.</span></span>
      <span class="state">{{if .PanelUp}}Running{{else}}Not yet{{end}}</span>
    </div>
  </div>

  {{if .StepDone}}
  <div class="verdict ok">
    <strong>All four are running.</strong>
    <p>Nothing to fix here. Next, add the first device that will use it.</p>
  </div>
  {{else if .Error}}
  <div class="verdict down">
    <strong>The admin panel isn't answering, so the VPN can't be checked.</strong>
    <p class="cause">{{.Error}}</p>
    <p>Give it a few seconds; the panel starts right after this page does, and this page checks again by itself.</p>
    <p>If it stays red, the panel container has stopped. Open a terminal in the folder you installed into and run <code>docker compose up -d</code>.</p>
  </div>
  {{else}}
  <div class="verdict wait">
    <strong>Still starting. You don't need to do anything.</strong>
    <p>Unbound and Pi-hole normally come up within about a minute. This page checks again every few seconds and moves on by itself.</p>
    <p class="fine">Still showing "Not yet" after a few minutes? Open a terminal in the folder you installed into, run <code>docker compose up -d</code>, and come back. If Docker itself is not running, start Docker first.</p>
  </div>
  {{end}}
  {{end}}

  {{/* ───────────────────────── STEP 2 ───────────────────────── */}}
  {{if eq .Key "role"}}
  <h1>First box or another one?</h1>
  <p class="lede">You can run Sovereign-DNS on more than one machine. The second one keeps DNS working when the first is off, asleep or being updated.</p>

  {{if and .StepDone (eq .Role "second") .Member}}
  <div class="verdict ok">
    <strong>This is another box.</strong>
    <p>It has asked to join your first box's network.</p>
  </div>
  {{else if and .StepDone (eq .Role "first") (or .Devices .PendingJoins)}}
  <div class="verdict ok">
    <strong>This is your first box.</strong>
    <p>Devices already use its network, so it stays the one they join.</p>
  </div>
  {{else}}
  <div class="card choose">
    <a class="pick{{if eq .Role "first"}} picked{{end}}" href="/setup?role=first">
      <strong>This is my first box</strong>
      <span>It runs your network. Phones, laptops and any other DNS box join it.</span>
    </a>
    <a class="pick{{if eq .Role "second"}} picked{{end}}" href="/setup?role=second">
      <strong>I already have a box</strong>
      <span>This one joins your first box's network, as a second DNS box. Have the first box's setup or dashboard open.</span>
    </a>
  </div>
  {{end}}
  {{end}}

  {{/* ─────────────────── STEP 3, FIRST BOX ─────────────────── */}}
  {{if eq .Key "add"}}
  <h1>Add your next device</h1>
  <p class="lede">What are you adding?</p>

  <div class="tabs" role="tablist">
    <a role="tab" class="tab{{if eq .Kind "phone"}} on{{end}}" href="/setup?step={{.Step}}&amp;kind=phone">A phone or computer</a>
    <a role="tab" class="tab{{if eq .Kind "box"}} on{{end}}" href="/setup?step={{.Step}}&amp;kind=box">Another DNS box</a>
  </div>

  {{if eq .Kind "box"}}
  <div class="card" id="add-box">
    {{if and .AddReady .JoinCode}}
    {{template "boxcard" .}}
    {{else}}
    <p>Waiting for the VPN to start before a box can be added. Go back to step 1 if this takes more than a minute.</p>
    {{end}}
  </div>
  {{else}}
  <div class="card" id="add-device">
    {{if .AddReady}}
    {{template "addcard" .}}
    {{else}}
    <p>Waiting for the VPN to start before a device can be added. Go back to step 1 if this takes more than a minute.</p>
    {{end}}
  </div>
  {{end}}

  {{if .StepDone}}
  <div class="verdict ok">
    <strong>{{if .PendingJoins}}A device is asking to join.{{else}}A device has already joined.{{end}}</strong>
    <p>Next, approve it. A box you just added is already approved.</p>
  </div>
  {{end}}
  {{end}}

  {{/* ────────────────── STEP 3, ANOTHER BOX ────────────────── */}}
  {{if eq .Key "join"}}
  <h1>Join your first box</h1>
  <p class="lede">This box leaves the network it made for itself and joins your first box's, so both answer the same devices.</p>

  {{if .Member}}
  <div class="verdict ok">
    <strong>Done. This box has asked to join.</strong>
    <p>Next, let the first box add it.</p>
  </div>
  {{else}}
  <div class="card">
    <ol class="todo">
      <li>On your <strong>first</strong> box, open setup (or its dashboard), go to <em>Add your next device</em> and pick <em>Another DNS box</em>. It shows a QR code and a join code.</li>
      <li>Press <strong>Scan the first box's code</strong> and take a photo of that QR code (on a computer, pick a photo or screenshot of it). Or paste the join code here. Then press Join.</li>
    </ol>
    <div class="add-actions" style="margin-bottom:10px">
      <button class="btn add-scan" type="button" data-scan data-fill="join-code-in" hidden>Scan the first box's code</button>
    </div>
    <form class="add-form" method="post" action="/setup/join">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <textarea id="join-code-in" name="code" rows="2" spellcheck="false" autocomplete="off" placeholder="npub1…:a0454be9" required aria-label="The first box's join code"></textarea>
      <div class="add-actions"><button class="btn primary" type="submit">Join</button></div>
    </form>
    <script src="/assets/scan.js"></script>
    <p class="fine" style="color:var(--dim);font-size:14px;margin:14px 0 0">
      The code is not a password: on its own it lets nobody in. It only tells this box which network to ask for. The first box still has to add this one.
    </p>
  </div>
  {{end}}
  {{end}}

  {{/* ────────────────── STEP 4, ANOTHER BOX ────────────────── */}}
  {{if eq .Key "admitted"}}
  <h1>Let the first box add it</h1>
  {{if .Admitted}}
  <div class="verdict ok">
    <strong>You're set. This box is on {{if .NetworkName}}{{.NetworkName}}{{else}}your first box's network{{end}}.</strong>
    <p>The first box added it, and both now see each other.</p>
    {{if .OwnBackup}}
    <p class="fine">It is the network's backup DNS box: when the first box is off, asleep or updating, devices ask this one instead.</p>
    {{else}}
    <p class="fine">It isn't a backup DNS box yet, so it doesn't answer when the first box is off. On the first box, under <em>Add another DNS box</em>, scan or paste this box's ID again and press Add: that makes it the backup.</p>
    {{end}}
    <p><a class="big" href="/dashboard">Go to the dashboard →</a></p>
  </div>
  {{else if .Member}}
  <p class="lede">Now the other way round. This is how the first box approves this one.</p>
  <div class="card">
    <ol class="todo">
      <li>On the first box, still under <em>Add your next device</em> → <em>Another DNS box</em>, press <strong>Scan QR code</strong> and take a photo of this code:
        <div class="qr" data-qr="nvpn://device/{{.OwnNpub}}" hidden aria-label="This box's ID as a QR code" role="img"></div>
        <p class="fine" style="margin:10px 0 0">Or paste this box's ID there instead:</p>
        <pre class="copy" id="own-id">{{.OwnNpub}}</pre>
        <button class="btn copybtn" type="button" data-copy="own-id" hidden>Copy</button>
      </li>
      <li>Check the ID it shows matches the one above, and press <strong>Add box</strong> there. This page notices on its own, usually within a few seconds.</li>
    </ol>
  </div>
  <script src="/assets/qrcode.js"></script>
  <script>
  document.querySelectorAll('[data-qr]').forEach(function (el) {
    if (typeof qrcode !== 'function') return;
    var q = qrcode(0, 'M');
    q.addData(el.dataset.qr);
    q.make();
    el.innerHTML = q.createSvgTag({cellSize: 5, margin: 20, scalable: true});
    el.hidden = false;
  });
  </script>
  <div class="verdict wait">
    <strong>Waiting for the first box{{if .AdminNpub}} ({{.AdminNpub | printf "%.18s"}}…){{end}}.</strong>
    <p class="fine">Still waiting after a minute with the ID added? Both boxes need to be on and online.</p>
  </div>
  {{else}}
  <div class="verdict wait">
    <strong>This box hasn't asked to join yet.</strong>
    <p>Go back to step 3 and paste the first box's join code.</p>
  </div>
  {{end}}
  {{end}}

  {{/* ─────────────────── STEP 4, FIRST BOX ──────────────────── */}}
  {{if eq .Key "approve"}}
  <h1>Approve it</h1>
  <p class="lede">Nothing reaches your box until you say yes. That is the whole point of this step.</p>

  <div class="card">
    {{if .PendingJoins}}
    <p><span class="badge">{{.PendingJoins}} device{{if gt .PendingJoins 1}}s{{end}} waiting</span></p>
    <ol class="todo">
      {{if .PanelOffHere}}
      <li>Easiest from here: go back to step 3 and paste the device's join link. That adds it in one click.</li>
      <li>Or, on the box itself, open the admin panel at <code>localhost:8082</code>.</li>
      {{else}}
      <li>Open the admin panel: <a class="big" href="{{.PanelURL}}" target="_blank" rel="noopener">Open the control panel →</a></li>
      {{end}}
      <li>Find <strong>Join requests</strong>. You should see one request: the device from step 2.</li>
      <li>Click <strong>Approve</strong>. Come back here. This page notices on its own.</li>
    </ol>
    <p class="fine" style="color:var(--dim);font-size:14px;margin:14px 0 0">
      Don't recognise it? Reject it. A request can come only from someone who has your box's identity and network name, but approval is yours alone.
    </p>
    {{else if .Devices}}
    <div class="row up">
      <span class="dot"></span>
      <span><span class="name">Approved</span><br>
        <span class="what">{{range $i, $d := .Devices}}{{if $i}}, {{end}}{{if $d.Alias}}{{$d.Alias}}{{else}}(unnamed){{end}}{{end}} {{if gt (len .Devices) 1}}are{{else}}is{{end}} on your network.</span></span>
    </div>
    {{else}}
    <div class="row warn">
      <span class="dot"></span>
      <span><span class="name">No request yet</span><br>
        <span class="what">Paste the device's join link in step 3. You'll see which device it is and approve it with one click.</span></span>
    </div>
    {{end}}
  </div>

  {{if .StepDone}}
  <div class="verdict ok">
    <strong>Approved.</strong>
    <p>Approved is not the same as working. The last step checks that lookups really arrive.</p>
  </div>
  {{end}}
  {{end}}

  {{/* ─────────────────── STEP 5, FIRST BOX ──────────────────── */}}
  {{if eq .Key "check"}}
  <h1>Does it really use the Pi-hole?</h1>
  <p class="lede">Two checks: this box's own view, then one you can see with your own eyes.</p>

  <div class="card">
    <p style="margin:0 0 6px;font-weight:600">1. Is the device connected?</p>
    {{if .Devices}}
      {{range .Devices}}
      <div class="row {{if .Carrying}}up{{else}}warn{{end}}">
        <span class="dot"></span>
        <span><span class="name">{{if .Alias}}{{.Alias}}{{else}}(unnamed){{end}}</span> <span class="what">{{.TunnelIP}}</span><br>
          <span class="what">{{if .Carrying}}Connected. Its lookups can reach this box.{{else}}Approved, but not connected. Open NostrVPN on that device and press Connect. This row turns green on its own.{{end}}</span></span>
        <span class="state">{{if .Carrying}}Connected{{else}}Not yet{{end}}</span>
      </div>
      {{end}}
    {{else}}
      <div class="row warn"><span class="dot"></span><span class="what">No device has been approved yet. Go back to step 4.</span></div>
    {{end}}
  </div>

  <div class="card">
    <p style="margin:0 0 6px;font-weight:600">2. See it for yourself</p>
    <p class="what" style="color:var(--dim);font-size:14px;margin:0 0 6px">
      Connected means packets can reach this box. It does not prove the device sends its lookups here. This does.
    </p>
    <ol class="todo">
      <li>On the device, open a browser and go to
        <pre>http://{{.CheckName}}</pre>
        The page will not load. That is expected: it is a made-up name nobody owns, so only your own box could ever have been asked for it.
      </li>
      <li>Open Pi-hole's query log{{if .PiholeLog}}: <a href="{{.PiholeLog}}" target="_blank" rel="noopener">{{.PiholeLog}}</a>{{else}} at <code>http://&lt;this box's mesh IP&gt;/admin/queries</code> (the IP appears here once the VPN is up){{end}}, sign in with the Pi-hole password you chose at install, and search for <code>{{.CheckName}}</code>.
        <br><strong>If it is in the log, the device asked your box.</strong> If it is not, the device still asks its old resolver.
      </li>
      <li>One more, for the ads: on the device, go to <code>http://doubleclick.net</code>. It should go nowhere, and the log shows it as <em>blocked</em>.</li>
    </ol>
    <p class="fine" style="color:var(--dim);font-size:14px;margin:14px 0 0">
      This page cannot look in the log for you. It holds no passwords on purpose, so a page anyone on this computer can open never gets to see your traffic.
    </p>
  </div>

  {{if .AllDone}}
  <div class="verdict ok">
    <strong>You're set.</strong>
    <p>Every lookup from that device now goes through your own resolver, filtered by your own Pi-hole. Add the rest of your devices the same way, from step 3.</p>
    <p><a class="big" href="/dashboard">Go to the dashboard →</a></p>
  </div>
  {{end}}
  {{end}}

  <nav class="steps">
    {{if .PrevURL}}<a class="prev" href="{{.PrevURL}}">← Back to step {{.PrevStep}}</a>{{else}}<span class="prev" style="color:var(--dim)">Step 1 of {{.Total}}</span>{{end}}
    <span class="spacer"></span>
    {{if .NextURL}}<a class="next {{if not .StepDone}}soft{{end}}" href="{{.NextURL}}">{{if .StepDone}}Next →{{else}}Skip ahead →{{end}}</a>
    {{else if .AllDone}}<a class="next" href="/dashboard">Dashboard →</a>
    {{else if eq .Role "first"}}<a class="next soft" href="/setup?step=3">Add another device</a>{{end}}
  </nav>

  <footer>Checked at {{.GeneratedAt}} · updates on its own · {{if .Login}}open to your home network, behind the dashboard password{{else}}served only to this computer{{end}}.</footer>
</main>
{{template "copyjs"}}
<script>
  // Reload when the facts on this page change, and only then. Polling a
  // one-line key keeps a stuck stack from making the page flicker.
  (function () {
    var key = document.querySelector('main').dataset.key;
    var step = {{.Step}};
    setInterval(function () {
      fetch('/setup?format=key&step=' + step, {cache: 'no-store'})
        .then(function (r) { return r.text(); })
        .then(function (k) { if (k && k !== key) location.reload(); })
        .catch(function () {});
    }, 5000);
  })();
</script>
</body>
</html>
`
