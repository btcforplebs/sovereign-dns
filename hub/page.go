package main

// Copy follows PLANS/HUB_LANDING_IA_COPY.md (Honey) and the scope in
// PLANS/HUB_LANDING_SCOPE.md (Bumble): six sections, every tile with both an
// up and a down state written, nothing implying coverage the stack doesn't
// have. No build step and no external assets — one file, served as-is.
const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sovereign-DNS</title>
<style>
  :root {
    --bg: #0f1115; --panel: #171a21; --line: #262b36; --ink: #e6e9ef;
    --dim: #9aa4b8; --up: #46c07a; --down: #e5484d; --warn: #e8a33d;
    --accent: #6ea8fe;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: var(--bg); color: var(--ink);
    font: 15px/1.55 ui-sans-serif, -apple-system, "Segoe UI", Roboto, sans-serif;
  }
  main { max-width: 900px; margin: 0 auto; padding: 40px 24px 72px; }
  h1 { font-size: 30px; margin: 0 0 6px; letter-spacing: -0.02em; }
  h2 { font-size: 13px; text-transform: uppercase; letter-spacing: 0.09em;
       color: var(--dim); margin: 40px 0 14px; font-weight: 600; }
  .lede { color: var(--dim); margin: 0 0 4px; }
  .grid { display: grid; gap: 12px; grid-template-columns: repeat(auto-fit, minmax(215px, 1fr)); }
  .card { background: var(--panel); border: 1px solid var(--line);
          border-radius: 10px; padding: 14px 16px; }
  .tile { display: flex; align-items: baseline; gap: 9px; }
  .dot { width: 9px; height: 9px; border-radius: 50%; flex: none; }
  .up .dot { background: var(--up); } .down .dot { background: var(--down); }
  .warn .dot { background: var(--warn); }
  .tile .name { font-weight: 600; }
  .tile .detail { color: var(--dim); font-size: 13px; }
  a.card { display: block; text-decoration: none; color: inherit; }
  a.card:hover { border-color: var(--accent); }
  a.card .name { color: var(--accent); font-weight: 600; }
  .badge { display: inline-block; background: var(--warn); color: #1a1205;
           border-radius: 999px; padding: 3px 11px; font-weight: 600; font-size: 13px; }
  code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px; }
  pre { background: #0b0d11; border: 1px solid var(--line); border-radius: 8px;
        padding: 12px 14px; overflow-x: auto; margin: 8px 0 0; }
  table { width: 100%; border-collapse: collapse; font-size: 14px; }
  th { text-align: left; color: var(--dim); font-weight: 600; font-size: 12px;
       text-transform: uppercase; letter-spacing: 0.06em; padding: 0 10px 8px 0; }
  td { padding: 8px 10px 8px 0; border-top: 1px solid var(--line); vertical-align: top; }
  td.owner { color: var(--dim); }
  .caveat { border-left: 3px solid var(--warn); padding: 2px 0 2px 14px;
            color: var(--dim); margin: 10px 0; }
  .err { border-left: 3px solid var(--down); padding: 10px 14px; background: #1d1416;
         border-radius: 0 8px 8px 0; margin-bottom: 20px; }
  .dev { display: flex; justify-content: space-between; gap: 12px; padding: 8px 0;
         border-top: 1px solid var(--line); font-size: 14px; }
  .dev:first-child { border-top: 0; }
  .dev .meta { color: var(--dim); font-size: 13px; }
  footer { color: var(--dim); font-size: 12px; margin-top: 44px; }

  .dns-row { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .dns-row .what { flex: 1 1 260px; }
  .dns-row .what .detail { color: var(--dim); font-size: 13px; }
  .pill { flex: none; border-radius: 999px; padding: 3px 12px; font-weight: 700;
          font-size: 13px; letter-spacing: 0.04em; text-transform: uppercase;
          border: 1px solid currentColor; }
  .pill.on { color: var(--up); } .pill.off { color: var(--warn); }
  .pill.unknown { color: var(--dim); }
  form.inline { margin: 0; flex: none; }
  .btn { font: inherit; font-size: 14px; font-weight: 600; line-height: 1.2;
         border-radius: 8px; padding: 9px 16px; cursor: pointer; text-decoration: none;
         display: inline-block; border: 1px solid var(--line); background: #1f2430; color: var(--ink); }
  .btn:hover { border-color: var(--accent); }
  .btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .btn.primary { background: var(--accent); border-color: var(--accent); color: #0b1220; }
  .btn.danger { background: transparent; border-color: var(--down); color: #ff8f92; }
  .btn.danger:hover { background: #2a1517; }
  .confirm { margin-top: 16px; padding: 14px 16px; border-radius: 8px;
             border: 1px solid var(--down); background: #1d1416; }
  .confirm.warn { border-color: var(--warn); background: #1f1a10; }
  .confirm h3 { margin: 0 0 6px; font-size: 16px; }
  .confirm p { margin: 0 0 12px; color: var(--dim); font-size: 14px; }
  .confirm .actions { display: flex; gap: 10px; flex-wrap: wrap; }
{{template "addcss"}}
</style>
</head>
<body>
<main>
  <h1>Sovereign-DNS</h1>
  <p class="lede">Your own DNS, filtered and private, everywhere your devices go.</p>
  <p class="lede">No third-party resolver, no exposed ports, fails closed if this box goes down.</p>

  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}

  <h2>Status</h2>
  <div class="grid">
    {{range .Tiles}}
    <div class="card tile {{.State}}">
      <span class="dot"></span>
      <span><span class="name">{{.Name}}</span><br><span class="detail">{{.Detail}}</span></span>
    </div>
    {{end}}
  </div>
  {{if .PendingJoins}}
  <p style="margin-top:14px">
    {{if .Remote}}
    <span class="badge">{{.PendingJoins}} device(s) waiting to join</span>
    Approve in the control panel on the box itself, or add the device here with its join link (below).
    {{else}}
    <a href="{{.PanelURL}}" class="badge">{{.PendingJoins}} device(s) waiting to join — approve</a>
    {{end}}
  </p>
  {{end}}

  <h2 id="dns">Network DNS</h2>
  <div class="card">
    <div class="dns-row">
      <span class="pill {{.DNS.State}}">{{.DNS.State}}</span>
      <div class="what">
        {{if eq .DNS.State "on"}}
          <strong>Every device on the mesh resolves through this box.</strong>
          <div class="detail">{{if .DNS.NodeIsThisBox}}Filtered by Pi-hole, resolved by Unbound.{{else}}DNS device: <code>{{.DNS.Node}}</code>, not this box.{{end}}
            If the resolver goes down, lookups fail rather than leak to another resolver.</div>
        {{else if eq .DNS.State "off"}}
          <strong>Devices are using their own DNS.</strong>
          <div class="detail">Usually the router's or the ISP's, with no filtering.</div>
        {{else}}
          <strong>Current setting unknown.</strong>
          <div class="detail">{{.DNS.Blocked}}</div>
        {{end}}
        {{if and .DNS.Blocked (ne .DNS.State "unknown")}}<div class="detail">{{.DNS.Blocked}}</div>{{end}}
      </div>
      {{if and .DNS.CanSwitch (not .ConfirmingDNS)}}
      <form class="inline" method="post" action="/dns">
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        {{if eq .DNS.State "on"}}
        <input type="hidden" name="override" value="off">
        <button class="btn danger" type="submit">Turn off…</button>
        {{else}}
        <input type="hidden" name="override" value="on">
        <button class="btn primary" type="submit">Turn on{{if not .DNS.ResolverUp}}…{{end}}</button>
        {{end}}
      </form>
      {{end}}
    </div>

    {{if eq .ConfirmingDNS "off"}}
    <section class="confirm" role="alert" aria-labelledby="confirm-title">
      <h3 id="confirm-title">Turn off DNS for the whole mesh?</h3>
      <p>Every device on the mesh goes back to its own resolver, usually the
        router's or the ISP's, including this computer if it's a mesh member.
        Blocklists stop applying and lookups are no longer private. You can turn
        it back on here.</p>
      <div class="actions">
        <a class="btn" href="/dashboard#dns" autofocus>Keep it on</a>
        <form class="inline" method="post" action="/dns">
          <input type="hidden" name="csrf" value="{{.CSRF}}">
          <input type="hidden" name="override" value="off">
          <input type="hidden" name="confirm" value="yes">
          <button class="btn danger" type="submit">Turn off DNS</button>
        </form>
      </div>
    </section>
    {{end}}
    {{if eq .ConfirmingDNS "on"}}
    <section class="confirm warn" role="alert" aria-labelledby="confirm-title">
      <h3 id="confirm-title">Pi-hole or Unbound isn't answering</h3>
      <p>With the override on, mesh devices refuse every other resolver. If this
        box can't answer, every lookup on every mesh device fails, including this
        computer if it's a mesh member. Fix the resolver first unless you mean to
        do this.</p>
      <div class="actions">
        <a class="btn" href="/dashboard#dns" autofocus>Leave it off</a>
        <form class="inline" method="post" action="/dns">
          <input type="hidden" name="csrf" value="{{.CSRF}}">
          <input type="hidden" name="override" value="on">
          <input type="hidden" name="confirm" value="yes">
          <button class="btn danger" type="submit">Turn on anyway</button>
        </form>
      </div>
    </section>
    {{end}}
  </div>

  <h2>Open</h2>
  <div class="grid">
    <a class="card" href="/setup">
      <span class="name">Setup guide</span><br>
      <span class="detail">Add a device, approve it, check it really uses this box.</span>
    </a>
    {{if .Remote}}
    <div class="card">
      <span class="name">Control Panel</span><br>
      <span class="detail">Rename or remove devices, network settings. Opens only on the box itself, at localhost:8082: it has no password of its own.</span>
    </div>
    {{else}}
    <a class="card" href="{{.PanelURL}}">
      <span class="name">Control Panel</span><br>
      <span class="detail">Rename or remove devices, network settings.</span>
    </a>
    {{end}}
    {{if .MeshURL}}
    <a class="card" href="{{.MeshURL}}">
      <span class="name">From your phone</span><br>
      <span class="detail">{{.MeshURL}} opens this dashboard on any device on the network, with the dashboard password.</span>
    </a>
    {{end}}
    {{if .PiholeURL}}
    <a class="card" href="{{.PiholeURL}}">
      <span class="name">Pi-hole</span><br>
      <span class="detail">Blocklists, per-device rules, query stats.</span>
    </a>
    {{end}}
    {{if .DocsURL}}
    <a class="card" href="{{.DocsURL}}">
      <span class="name">Docs</span><br>
      <span class="detail">How this stack works, layer by layer.</span>
    </a>
    {{end}}
  </div>

  <h2 id="add-device">Add a device</h2>
  <div class="card">
    {{if .AddReady}}
      {{template "addcard" .}}
    {{else}}
      <div class="detail">Waiting for the VPN to start before a device can be added.</div>
    {{end}}
  </div>

  {{if .Devices}}
  <h2>Devices on this network</h2>
  <div class="card">
    {{range .Devices}}
    <div class="dev">
      <span>{{if .Alias}}{{.Alias}}{{else}}(unnamed){{end}} <span class="meta">{{.TunnelIP}}</span></span>
      <span class="meta">{{.Detail}}</span>
    </div>
    {{end}}
  </div>
  <p class="detail" style="color:var(--dim);font-size:13px">
    Approved is not the same as covered: a device only resolves through this
    stack once it has a working path to it.
  </p>
  {{end}}

  <h2>Current config</h2>
  <div class="card">
    <table>
      <tr><th>Setting</th><th>Value</th><th>Changed in</th></tr>
      <tr>
        <td>Network DNS override</td><td>{{.OverrideState}}</td>
        <td class="owner"><a href="#dns" style="color:var(--accent)">this page</a></td>
      </tr>
      <tr><td>Blocklists</td><td>managed in Pi-hole</td><td class="owner">Pi-hole</td></tr>
      {{range .Settings}}
      <tr><td>{{.Name}}</td><td>{{.Value}}</td><td class="owner">{{.ChangedIn}}</td></tr>
      {{end}}
    </table>
  </div>

  <h2>Worth knowing</h2>
  <p class="caveat">
    Phones and tablets use the box only with a Nostr VPN build that has
    network DNS (the same code this box runs). The stock app-store app
    doesn't have it yet: it joins the network but resolves on its own.
  </p>
  <p class="caveat">
    If this stack goes down, DNS fails closed rather than silently falling back
    to your ISP. Restart it on this computer with <code>docker compose up -d</code>.
    If the computer itself is down, each device's owner can opt out locally in
    their own NostrVPN config.
  </p>

  <footer>Checked at {{.GeneratedAt}} · {{if .Login}}this page is open to your home network, behind the dashboard password.
    <form method="post" action="/logout" style="display:inline"><button type="submit">Sign out</button></form>{{else}}this page is served only to this computer.{{end}}</footer>
</main>
</body>
</html>
`
