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
    <a href="{{.PanelURL}}" class="badge">{{.PendingJoins}} device(s) waiting to join — approve</a>
  </p>
  {{end}}

  <h2>Open</h2>
  <div class="grid">
    <a class="card" href="{{.PanelURL}}">
      <span class="name">Control Panel</span><br>
      <span class="detail">Manage devices, approve joins, pair with QR.</span>
    </a>
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

  <h2>Bring another device onto the mesh</h2>
  <div class="card">
    {{if .InviteCommand}}
      <div class="detail">Run this on the new device:</div>
      <pre>{{.InviteCommand}}</pre>
      <p class="detail" style="margin:12px 0 0">
        It only requests to join. Approve it in the
        <a href="{{.PanelURL}}" style="color:var(--accent)">Control Panel</a>.
      </p>
    {{else}}
      <div class="detail">Waiting for the daemon before an invite can be generated.</div>
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
        <td class="owner">terminal — <code>docker exec sovereign-dns-nvpn nvpn set --network-dns-override true|false</code></td>
      </tr>
      <tr><td>Blocklists</td><td>managed in Pi-hole</td><td class="owner">Pi-hole</td></tr>
      {{range .Settings}}
      <tr><td>{{.Name}}</td><td>{{.Value}}</td><td class="owner">{{.ChangedIn}}</td></tr>
      {{end}}
    </table>
  </div>

  <h2>Worth knowing</h2>
  <p class="caveat">
    iPhones and iPads don't get the DNS override yet — released builds only
    publish the policy, they don't apply it. Coverage today is desktop and
    CLI-mesh devices.
  </p>
  <p class="caveat">
    If this stack goes down, DNS fails closed rather than silently falling back
    to your ISP. Restart it on this computer with <code>docker compose up -d</code>.
    If the computer itself is down, each device's owner can opt out locally in
    their own NostrVPN config.
  </p>

  <footer>Checked at {{.GeneratedAt}} · this page is served only to this computer.</footer>
</main>
</body>
</html>
`
