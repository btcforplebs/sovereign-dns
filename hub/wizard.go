package main

// The setup wizard: what a newcomer sees first, at http://localhost:8080,
// after install.sh finishes. Four steps, each one a question with a plain
// answer and the single thing to do next:
//
//   1. Is everything running?        (Unbound, Pi-hole, VPN, control panel)
//   2. Add your first device.        (invite command; phones are a placeholder)
//   3. Approve it.                   (join request -> admin panel)
//   4. Does it really use the Pi-hole? (reachability + a name only it would ask)
//
// It is a view over the same page struct the dashboard renders from, so it
// never probes anything itself and cannot disagree with the dashboard. Like
// the rest of the hub it is read-only: every click that changes something
// goes to the control panel or Pi-hole, which already own that change.

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
)

const wizardSteps = 4

type wizardStep struct {
	N       int
	Title   string
	Done    bool
	Current bool
}

type wizardPage struct {
	page
	Steps     []wizardStep
	Step      int    // the step being shown
	StepDone  bool   // whether the shown step is satisfied
	AllDone   bool   // every step satisfied
	PrevURL   string // "" on the first step
	PrevStep  int
	NextURL   string // "" on the last step
	StateKey  string // changes when anything the wizard shows changes; the page polls for it
	PiholeLog string // Pi-hole query log, or "" when the mesh IP is not known yet
}

var wizardTitles = [wizardSteps]string{
	"Is everything running?",
	"Add your first device",
	"Approve it",
	"Does it really use the Pi-hole?",
}

// stepDone is the one place that decides what "done" means for each step.
func stepDone(p page, n int) bool {
	switch n {
	case 1:
		return p.DaemonUp && p.PanelUp && p.PiholeUp && p.UnboundUp
	case 2:
		return p.PendingJoins > 0 || len(p.Devices) > 0
	case 3:
		return len(p.Devices) > 0
	case 4:
		return p.Covered > 0
	}
	return false
}

func buildWizard(p page, want int) wizardPage {
	w := wizardPage{page: p, AllDone: true}
	first := 0
	for n := 1; n <= wizardSteps; n++ {
		done := stepDone(p, n)
		if !done {
			w.AllDone = false
			if first == 0 {
				first = n
			}
		}
		w.Steps = append(w.Steps, wizardStep{N: n, Title: wizardTitles[n-1], Done: done})
	}
	if first == 0 {
		first = wizardSteps
	}
	// Land on the first thing that still needs doing, unless the reader asked
	// for a specific step (Back, or re-reading an earlier one).
	w.Step = want
	if w.Step < 1 || w.Step > wizardSteps {
		w.Step = first
	}
	w.Steps[w.Step-1].Current = true
	w.StepDone = w.Steps[w.Step-1].Done
	if w.Step > 1 {
		w.PrevStep = w.Step - 1
		w.PrevURL = fmt.Sprintf("/setup?step=%d", w.PrevStep)
	}
	if w.Step < wizardSteps {
		w.NextURL = fmt.Sprintf("/setup?step=%d", w.Step+1)
	}
	if p.PiholeURL != "" {
		w.PiholeLog = p.PiholeURL + "/queries"
	}
	w.StateKey = stateKey(p)
	return w
}

// stateKey is what the page compares to decide whether to reload. It covers
// exactly the facts the wizard renders, so a reload happens when the reader
// would see something different and not on every clock tick.
func stateKey(p page) string {
	return fmt.Sprintf("%t%t%t%t-%d-%d-%d-%t%t",
		p.DaemonUp, p.PanelUp, p.PiholeUp, p.UnboundUp,
		p.PendingJoins, len(p.Devices), p.Covered, p.InviteCommand != "", p.PiholeURL != "")
}

func (c config) serveWizard(w http.ResponseWriter, r *http.Request) {
	data := c.buildPage(r.Context())
	want, _ := strconv.Atoi(r.URL.Query().Get("step"))
	wz := buildWizard(data, want)
	if r.URL.Query().Get("format") == "key" {
		// The page polls this. Plain text, one line, cheap.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, wz.StateKey)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := wizardTmpl.Execute(w, wz); err != nil {
		log.Printf("hub: rendering wizard: %v", err)
	}
}

var wizardTmpl = template.Must(template.New("wizard").Parse(wizardHTML))

const wizardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Set up Sovereign-DNS · step {{.Step}} of 4</title>
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
  .verdict code, .err code { word-break: break-all; }
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
  {{if and .Error (ne .Step 1)}}<div class="err">{{.Error}}</div>{{end}}

  {{/* ───────────────────────── STEP 1 ───────────────────────── */}}
  {{if eq .Step 1}}
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
        <span class="what">The page at <code>localhost:8082</code> where devices get approved. Only this computer can open it.</span></span>
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
    <p>{{.Error}}</p>
    <p>If you installed a few minutes ago, that's the first build still compiling the VPN; it takes 10 to 15 minutes and this page moves on by itself. If this box was already working, the VPN container has stopped: open a terminal in the folder you installed into and run <code>docker compose up -d</code>.</p>
  </div>
  {{else}}
  <div class="verdict wait">
    <strong>Still starting. You don't need to do anything.</strong>
    <p>The very first start builds the VPN from source, which takes 10 to 15 minutes. Every start after that takes seconds. This page checks again every few seconds and moves on by itself.</p>
    <p class="fine">Still showing "Not yet" after 20 minutes? Open a terminal in the folder you installed into, run <code>docker compose up -d</code>, and come back. If Docker itself is not running, start Docker first.</p>
  </div>
  {{end}}
  {{end}}

  {{/* ───────────────────────── STEP 2 ───────────────────────── */}}
  {{if eq .Step 2}}
  <h1>Add your first device</h1>
  <p class="lede">A device joins by asking. It gets nothing until you approve it in the next step.</p>

  <div class="card">
    <p class="kind">On a Mac, Windows or Linux computer</p>
    {{if .InviteCommand}}
    <ol class="todo">
      <li>On that computer, install NostrVPN (the same app this box runs inside).</li>
      <li>Open a terminal there and paste this, exactly:
        <pre>{{.InviteCommand}}</pre>
      </li>
      <li>Come back here. The request shows up in the next step within a few seconds.</li>
    </ol>
    <p class="fine" style="color:var(--dim);font-size:14px;margin:14px 0 0">
      The long code is this box's public identity and the short one is your network's name. Together they tell the device whom to ask. Neither is a secret; what matters is your approval.
    </p>
    {{else}}
    <p>Waiting for the VPN to start before an invite can be written. Go back to step 1 if this takes more than a minute.</p>
    {{end}}
  </div>

  <div class="card">
    <p class="kind">On an iPhone or Android phone</p>
    <div class="placeholder">
      <strong>Phones: coming, not here yet.</strong><br>
      How a phone joins is still being decided. The method that lands here will be the one that passed a real test: a name looked up on the phone shows up in this box's log. Until then, start with a computer.
    </div>
  </div>

  {{if .StepDone}}
  <div class="verdict ok">
    <strong>{{if .PendingJoins}}A device is asking to join.{{else}}A device has already joined.{{end}}</strong>
    <p>Next, approve it.</p>
  </div>
  {{end}}
  {{end}}

  {{/* ───────────────────────── STEP 3 ───────────────────────── */}}
  {{if eq .Step 3}}
  <h1>Approve it</h1>
  <p class="lede">Nothing reaches your box until you say yes. That is the whole point of this step.</p>

  <div class="card">
    {{if .PendingJoins}}
    <p><span class="badge">{{.PendingJoins}} device{{if gt .PendingJoins 1}}s{{end}} waiting</span></p>
    <ol class="todo">
      <li>Open the admin panel: <a class="big" href="{{.PanelURL}}" target="_blank" rel="noopener">Open localhost:8082 →</a></li>
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
        <span class="what">Once the device runs the command from step 2, its request appears here within a few seconds. This page is watching.</span></span>
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

  {{/* ───────────────────────── STEP 4 ───────────────────────── */}}
  {{if eq .Step 4}}
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
      <div class="row warn"><span class="dot"></span><span class="what">No device has been approved yet. Go back to step 3.</span></div>
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
    <p>Every lookup from that device now goes through your own resolver, filtered by your own Pi-hole. Add the rest of your devices the same way, from step 2.</p>
    <p><a class="big" href="/dashboard">Go to the dashboard →</a></p>
  </div>
  {{end}}
  {{end}}

  <nav class="steps">
    {{if .PrevURL}}<a class="prev" href="{{.PrevURL}}">← Back to step {{.PrevStep}}</a>{{else}}<span class="prev" style="color:var(--dim)">Step 1 of 4</span>{{end}}
    <span class="spacer"></span>
    {{if .NextURL}}<a class="next {{if not .StepDone}}soft{{end}}" href="{{.NextURL}}">{{if .StepDone}}Next →{{else}}Skip ahead →{{end}}</a>
    {{else if .AllDone}}<a class="next" href="/dashboard">Dashboard →</a>
    {{else}}<a class="next soft" href="/setup?step=2">Add another device</a>{{end}}
  </nav>

  <footer>Checked at {{.GeneratedAt}} · updates on its own · served only to this computer.</footer>
</main>
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
