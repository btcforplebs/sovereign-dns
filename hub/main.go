// Command hub serves the sovereign-dns landing page: what's running, how to
// reach it, and how to add a device — the things that otherwise require
// reading `docker compose logs` and knowing which container to exec into.
//
// It is read-only with two exceptions. Every mutable setting in this stack
// already has an owner with a UI (devices in the control panel, blocklists in
// Pi-hole, the rest in .env), so the hub launches those rather than
// re-implementing them badly. The exceptions both go through the control
// panel's own API, behind the guards described in their files: the network
// DNS override, the one setting this stack exists for (dns.go), and adding a
// device from its join link (join.go), so a phone joins without a terminal.
// The hub holds no credentials and never touches the Docker socket: status comes from probing
// the services the same way a user would.
//
// Standard library only, on purpose. This page fronts an unauthenticated
// admin surface, so it should be readable end to end by whoever is trusting
// it.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

const probeTimeout = 4 * time.Second

type config struct {
	listen       string
	panelURL     string // reachable from the browser (host loopback)
	panelAPI     string // reachable from this container (compose network)
	piholeAddr   string // host:port on dns-internal
	docsAddr     string // host:port on the compose network
	unboundAddr  string // host:port on dns-internal
	piholeWebURL string // what the browser should open, filled with the mesh IP
	docsURL      string // what the browser should open
	envSettings  []setting
	mock         string        // HUB_MOCK: render a canned state instead of probing (screenshots, demos)
	checkName    string        // unique name a device can look up to prove it resolves through Pi-hole
	csrfToken    string        // per process; every form that changes something must echo it
	auth         *auth         // login for home-network access (auth.go); nil or no password: none
	mesh         bool          // HUB_MESH=on: the hub also answers the mesh, through the gate (mesh.go)
	backups      *backupMemory // which networks have had a backup DNS box (alerts.go)
}

type setting struct {
	Name      string
	Value     string
	ChangedIn string
}

// tick is the subset of the control panel's state we render. The panel's own
// UI is driven by this same payload, so it stays in step with the daemon
// without us parsing config.toml.
type tick struct {
	DaemonRunning bool          `json:"daemonRunning"`
	OwnNpub       string        `json:"ownNpub"`
	NetworkID     string        `json:"networkId"`
	TunnelIP      string        `json:"tunnelIp"`
	MagicDNS      string        `json:"magicDnsSuffix"`
	Networks      []tickNetwork `json:"networks"`
	// VPNEnabled is false once someone presses Disconnect in the control
	// panel: the daemon keeps running, but this box stops carrying the mesh.
	VPNEnabled bool   `json:"vpnEnabled"`
	VPNStatus  string `json:"vpnStatus"`
	// The panel's own build and the daemon's, from the state file the daemon
	// writes. Both carry "+<commit>" on images built with NVPN_BUILD_COMMIT
	// (nvpn/Dockerfile), so two images of the same release still differ.
	AppVersion    string `json:"appVersion"`
	DaemonVersion string `json:"daemonBinaryVersion"`
}

type tickNetwork struct {
	ID                  string   `json:"id"` // config-local id; the panel's write routes key on this
	Name                string   `json:"name"`
	NetworkID           string   `json:"networkId"`
	Enabled             bool     `json:"enabled"`
	LocalIsAdmin        bool     `json:"localIsAdmin"`
	AdminNpubs          []string `json:"adminNpubs"`
	DNSNodeNpub         string   `json:"dnsNodeNpub"`
	DNSOverride         bool     `json:"dnsOverride"`
	LocalIsDNSNode      bool     `json:"localIsDnsNode"`
	InboundJoinRequests []struct {
		Npub string `json:"npub"`
	} `json:"inboundJoinRequests"`
	Participants []tickParticipant `json:"participants"`
	// Devices this admin removed. Nil when the panel predates the list (it
	// sends none at all), which is also how the hub knows the panel can't add
	// a box as a backup in one step (boxjoin.go): both came in one build.
	RemovedDevices *[]tickRemoved `json:"removedDevices"`
}

type tickRemoved struct {
	Npub  string `json:"npub"`
	Alias string `json:"alias"`
}

type tickParticipant struct {
	Alias     string `json:"alias"`
	Npub      string `json:"npub"`
	TunnelIP  string `json:"tunnelIp"`
	Reachable bool   `json:"reachable"`
	State     string `json:"state"`
	// The box's own entry reads false while it waits for the admin's
	// signed roster after a manual join (boxjoin.go), true once in.
	RosterAccepted bool `json:"rosterAccepted"`
	// Presence over the relays is not a data path. A device can be
	// "online" here and still pass no traffic, so this is what
	// separates an amber tile from a green one.
	DirectProbePending bool   `json:"fipsDirectProbePending"`
	TransportType      string `json:"fipsTransportType"`
	StatusText         string `json:"statusText"`
	// Its part in the network's DNS: the DNS box, or one of its backups.
	ServesDNS       bool `json:"servesNetworkDns"`
	ServesDNSBackup bool `json:"servesNetworkDnsBackup"`
}

type tile struct {
	Name   string
	State  string // "up", "down", "warn"
	Detail string
}

type device struct {
	Alias    string
	Npub     string
	TunnelIP string
	Carrying bool
	Detail   string
	Role     string // "DNS box", "backup DNS box", or ""
}

type page struct {
	Tiles         []tile
	PendingJoins  int
	PanelURL      string
	PiholeURL     string
	MeshURL       string // this dashboard from any device on the mesh (HUB_MESH=on)
	MeshPanelURL  string // the VPN control panel from the mesh (HUB_MESH=on)
	PanelOnMesh   bool   `json:"-"` // the reader came over the mesh, so PanelURL is MeshPanelURL
	DocsURL       string
	OwnNpub       string
	NetworkID     string
	NetworkName   string
	AddReady      bool // gates the Add-a-device card: this box knows its own device ID and network
	Devices       []device
	MeshSummary   string
	MeshState     string
	Settings      []setting
	OverrideState string // "on", "off", or "unknown" when the panel did not answer
	DNS           dnsControl
	Error         string
	GeneratedAt   string

	// Raw facts behind the tiles. The setup wizard (wizard.go) decides which
	// step a newcomer is on from these, so it never re-probes anything.
	DaemonUp  bool
	PanelUp   bool
	PiholeUp  bool
	UnboundUp bool
	Covered   int // devices that actually get DNS through this box
	CheckName string
	FirstRun  bool // no device besides this box has ever joined

	// A second DNS box (boxjoin.go). JoinCode is what this box shows for
	// another box to join it: only when it runs its own network. Member: this
	// box asked to join another box's network; Admitted: that box's roster
	// has taken it in. AdminNpub is that other box.
	JoinCode  string
	Member    bool
	Admitted  bool
	AdminNpub string
	OwnBackup bool // the first box's roster makes this box a backup DNS box

	// Things that need the reader now (alerts.go), worst first.
	Alerts []alert
	// Devices removed from this network, each with a Re-add button.
	Removed []tickRemoved
	// Backups: how many devices on the network are backup DNS boxes.
	Backups int
	// HasDNSBox: the network has a DNS box, so a box can join as its backup.
	HasDNSBox bool

	// Request-scoped, never part of /api/status: the form token, and which
	// confirmation (if any) the dashboard is asking for.
	CSRF string `json:"-"`
	// Remote: the reader addressed the hub by a non-loopback name, so links
	// to services published on the box's loopback (the control panel) cannot
	// open for them. Login: pages are behind the hub password (auth.go).
	Remote  bool   `json:"-"`
	Login   bool   `json:"-"`
	Confirm string `json:"-"`
	AddFrom string `json:"-"` // where the add-device form returns to ("setup" or the dashboard)

	network *tickNetwork // the network the DNS switch acts on
	ownNpub string
	// Why network is nil although the panel answered. Empty means the panel
	// itself is the problem.
	noNetwork string
}

func main() {
	// The same binary is the mesh gate (mesh.go), which runs in nvpn's
	// namespace and has no shell for a healthcheck, and the home-network DNS
	// forwarder (dnslan.go).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mesh-gate":
			runMeshGate()
			return
		case "mesh-health":
			os.Exit(meshHealth())
		case "dns-lan":
			runDNSLAN()
			return
		case "dns-lan-health":
			os.Exit(dnsLANHealth())
		default:
			log.Fatalf("hub: unknown command %q", os.Args[1])
		}
	}

	cfg := loadConfig()
	a, err := newAuth(env("HUB_BIND", "127.0.0.1"), os.Getenv("HUB_PASSWORD"))
	if err != nil {
		log.Fatalf("hub: %v", err)
	}
	cfg.auth = a
	cfg.mesh = meshOn()
	if err := meshNeedsLogin(cfg.mesh, a); err != nil {
		log.Fatalf("hub: %v", err)
	}
	if a.enabled() {
		log.Printf("hub: home-network access on (published on %s), pages need the hub password", env("HUB_BIND", "127.0.0.1"))
	}
	mux := newMux(cfg)

	if cfg.mesh {
		gate, err := netip.ParseAddr(env("HUB_MESH_GATE", "172.28.53.10"))
		if err != nil {
			log.Fatalf("hub: HUB_MESH_GATE: %v", err)
		}
		log.Printf("hub: mesh access on (port %s, from the gate at %s), pages need the hub password", meshHubListen, gate)
		meshServer := &http.Server{
			Addr:              meshHubListen,
			Handler:           meshHandler(gate, mux),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() { log.Fatal(meshServer.ListenAndServe()) }()
		log.Printf("hub: control panel on the mesh (port %s, from the gate at %s), behind the hub password", meshPanelHubListen, gate)
		panelServer := &http.Server{
			Addr:              meshPanelHubListen,
			Handler:           meshHandler(gate, cfg.auth.wrap(meshPanelProxy(cfg.panelAPI))),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() { log.Fatal(panelServer.ListenAndServe()) }()
	}

	log.Printf("hub: listening on %s", cfg.listen)
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

// newMux wires the routes, behind the login when there is one. Split from
// main so tests can serve a request against a config whose probes point at
// nothing.
func newMux(cfg config) http.Handler {
	tmpl := template.Must(template.Must(template.Must(template.New("page").Parse(pageHTML)).Parse(addCardHTML)).Parse(boxCardHTML))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		data := cfg.buildPage(r.Context())
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("hub: encoding status: %v", err)
		}
	})
	// The dashboard, always. "/" also lands here once at least one device has
	// joined; before that it sends a newcomer to the wizard, because a status
	// grid means nothing to someone who has not added a device yet.
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		data := cfg.buildPage(r.Context())
		data.CSRF = cfg.csrfToken
		cfg.viewer(&data, r)
		data.Confirm = r.URL.Query().Get("confirm")
		noFraming(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("hub: rendering page: %v", err)
		}
	})
	mux.HandleFunc("/setup", cfg.serveWizard)
	mux.HandleFunc("/dns", cfg.serveDNS(tmpl))
	mux.HandleFunc("/devices/add", cfg.serveAddDevice)
	mux.HandleFunc("/devices/add-box", cfg.serveAddBox)
	mux.HandleFunc("/devices/readd", cfg.serveReAdd)
	mux.HandleFunc("/setup/join", cfg.serveJoinFirstBox)
	mux.HandleFunc("/assets/scan.js", serveScript("scan.js", scanJS))
	mux.HandleFunc("/assets/jsqr.js", serveScript("jsqr.js", jsQRSource))
	mux.HandleFunc("/assets/qrcode.js", serveScript("qrcode.js", qrcodeGenSource))
	mux.Handle("/docs/", docsProxy(cfg.docsAddr))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data := cfg.buildPage(r.Context())
		// A second box waiting for the first box to add it is not set up
		// yet either, though its roster already lists that box.
		if data.FirstRun || (data.Member && !data.Admitted) {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		data.CSRF = cfg.csrfToken
		cfg.viewer(&data, r)
		noFraming(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("hub: rendering page: %v", err)
		}
	})
	return cfg.auth.wrap(mux)
}

// viewer fills in what depends on who is looking rather than on the box.
func (c config) viewer(p *page, r *http.Request) {
	p.Remote = !loopbackHost(r.Host)
	p.Login = c.auth.enabled()
	// Over the mesh the panel opens at its own mesh port, behind the same
	// login (mesh.go); from the home network it still opens only on the box.
	if fromMeshGate(r) && p.MeshPanelURL != "" {
		p.PanelURL = p.MeshPanelURL
		p.PanelOnMesh = true
	}
	// The docs' own port is on the box's loopback; from elsewhere, read them
	// through the hub (docsProxy).
	if p.Remote && p.DocsURL != "" {
		p.DocsURL = "/docs/"
	}
}

// hostAllowed is the DNS-rebinding guard for the routes that change things.
// Without a login, only a loopback Host is legitimate (the hub is published
// on loopback only). With one, the session cookie is the guard: a rebinding
// page arrives under its own name, so its requests carry no session and never
// get this far (auth.go).
func (c config) hostAllowed(host string) bool {
	return loopbackHost(host) || c.auth.enabled()
}

func loadConfig() config {
	webPort := env("WEB_UI_PORT", "8082")
	docsPort := env("DOCS_PORT", "8081")
	piholePort := env("PIHOLE_WEB_PORT", "80")
	return config{
		mock:        env("HUB_MOCK", ""),
		backups:     &backupMemory{},
		checkName:   newCheckName(),
		csrfToken:   newCSRFToken(),
		listen:      env("HUB_LISTEN", ":8080"),
		panelURL:    fmt.Sprintf("http://localhost:%s", webPort),
		panelAPI:    env("PANEL_API", "http://sovereign-dns-web:8081"),
		piholeAddr:  env("PIHOLE_ADDR", "172.28.53.10:"+piholePort),
		unboundAddr: env("UNBOUND_ADDR", "172.28.53.11:53"),
		docsAddr:    env("DOCS_ADDR", "sovereign-dns-docs:80"),
		docsURL:     fmt.Sprintf("http://localhost:%s", docsPort),
		envSettings: []setting{
			{"Pi-hole password", "set during install", ".env"},
			{"Timezone", env("TZ", "UTC"), ".env"},
			{"Control panel port", webPort, ".env"},
			{"Docs port", docsPort, ".env"},
			{"Pi-hole web port", piholePort, ".env"},
			{"Recursion", "no forwarders, root servers only", "built in"},
		},
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (c config) buildPage(ctx context.Context) page {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout+time.Second)
	defer cancel()

	var (
		wg                          sync.WaitGroup
		state                       tick
		tickErr                     error
		piholeUp, docsUp, unboundUp bool
		panelUp                     bool
	)

	if c.mock != "" {
		m := mockState(c.mock)
		state, tickErr = m.tick, m.tickErr
		if m.hadBackup && len(state.Networks) > 0 {
			c.backups.note(state.Networks[0].NetworkID, 1)
		}
		panelUp, piholeUp, docsUp, unboundUp = m.panelUp, m.piholeUp, m.docsUp, m.unboundUp
	} else {
		wg.Add(5)
		go func() { defer wg.Done(); state, tickErr = c.fetchTick(ctx) }()
		go func() { defer wg.Done(); panelUp = probeHTTP(ctx, c.panelAPI+"/api/health") }()
		go func() { defer wg.Done(); piholeUp = probeHTTP(ctx, "http://"+c.piholeAddr+"/admin/login") }()
		go func() { defer wg.Done(); docsUp = probeHTTP(ctx, "http://"+c.docsAddr+"/") }()
		go func() { defer wg.Done(); unboundUp = probeDNS(ctx, c.unboundAddr) }()
		wg.Wait()
	}

	p := page{
		PanelURL:    c.panelURL,
		Settings:    c.envSettings,
		GeneratedAt: time.Now().Format("15:04:05"),
		DaemonUp:    state.DaemonRunning,
		PanelUp:     panelUp,
		PiholeUp:    piholeUp,
		UnboundUp:   unboundUp,
		CheckName:   c.checkName,
		// "No device has joined" is only knowable when the panel answered.
		// If it did not, this is a box in an unknown state, and the
		// dashboard (which shows the error) is the right page for that;
		// sending the reader to the wizard would tell an established box
		// it is brand new.
		FirstRun: tickErr == nil,
	}
	if tickErr != nil {
		p.Error = "Can't reach the VPN control panel, so device and network details are unavailable: " + tickErr.Error()
	}

	daemon := tile{Name: "VPN daemon", State: "down", Detail: "unreachable"}
	if state.DaemonRunning {
		daemon = tile{Name: "VPN daemon", State: "up", Detail: "running"}
	}
	p.Tiles = append(p.Tiles,
		daemon,
		boolTile("Control panel", panelUp, "up", "down"),
		boolTile("Pi-hole", piholeUp, "filtering", "unreachable"),
		boolTile("Unbound", unboundUp, "resolving, no forwarders", "unreachable"),
		boolTile("Docs", docsUp, "up", "down"),
	)

	p.OwnNpub = state.OwnNpub
	p.NetworkID = state.NetworkID
	// Unknown until the panel says otherwise: a dead panel must not read as a
	// default-looking "off".
	p.OverrideState = "unknown"
	if tickErr == nil && len(state.Networks) == 0 {
		p.noNetwork = "No network has been joined or created yet, so there is nothing to switch. Set one up in the control panel first."
	}
	if len(state.Networks) > 0 {
		net, matched := activeNetwork(state)
		p.NetworkName = net.Name
		p.PendingJoins = len(net.InboundJoinRequests)
		if tickErr == nil && !matched {
			p.noNetwork = "The control panel lists more than one network but doesn't say which one is running, so the switch can't tell which to change."
		}
		if tickErr == nil && matched {
			p.network = &net
			p.ownNpub = state.OwnNpub
			p.OverrideState = "off"
			if net.DNSOverride {
				p.OverrideState = "on"
			}
		}
		if net.NetworkID != "" {
			p.NetworkID = net.NetworkID
		}

		if tickErr == nil && matched {
			if net.LocalIsAdmin {
				p.JoinCode = joinCode(state.OwnNpub, net.NetworkID)
			} else {
				p.Member = true
				for _, a := range net.AdminNpubs {
					if a != state.OwnNpub {
						p.AdminNpub = a
						break
					}
				}
				for _, participant := range net.Participants {
					if participant.Npub != "" && participant.Npub == state.OwnNpub {
						p.Admitted = participant.RosterAccepted
						p.OwnBackup = participant.ServesDNSBackup
					}
				}
			}
		}

		p.HasDNSBox = tickErr == nil && matched && net.DNSNodeNpub != ""
		if tickErr == nil && matched && net.LocalIsAdmin && net.RemovedDevices != nil {
			p.Removed = *net.RemovedDevices
		}
		for _, participant := range net.Participants {
			if participant.ServesDNSBackup {
				p.Backups++
			}
		}

		carrying := 0
		for _, participant := range net.Participants {
			// This box itself. Its state only reads "local" once the daemon
			// has settled; in the first seconds it can be anything, and
			// counting it then tells the wizard a device has joined.
			if participant.State == "local" || (participant.Npub != "" && participant.Npub == state.OwnNpub) {
				continue
			}
			// A device only actually gets sovereign DNS if packets reach it,
			// so this tracks the daemon's own ✓/✗ rather than roster
			// membership. Note what is deliberately *not* required: a direct
			// probe. Relayed paths carry traffic fine and stay
			// "probing direct" indefinitely, so requiring a direct path here
			// would report a working mesh as broken.
			isCarrying := participant.Reachable && participant.State == "online"
			if isCarrying {
				carrying++
			}
			detail := participant.StatusText
			if detail == "" {
				detail = participant.State
			}
			if !isCarrying {
				detail += " — not covered by this resolver"
			}
			p.Devices = append(p.Devices, device{
				Alias:    participant.Alias,
				Npub:     participant.Npub,
				TunnelIP: strings.SplitN(participant.TunnelIP, "/", 2)[0],
				Carrying: isCarrying,
				Detail:   detail,
				Role:     dnsRole(participant),
			})
		}
		total := len(p.Devices)
		p.Covered = carrying
		p.FirstRun = total == 0
		p.MeshSummary = fmt.Sprintf("%d/%d reachable", carrying, total)
		switch {
		case total == 0:
			p.MeshSummary = "no other devices yet"
			p.MeshState = "warn"
		case carrying == total:
			p.MeshState = "up"
		default:
			p.MeshState = "warn"
		}
		p.Tiles = append(p.Tiles, tile{Name: "Mesh devices", State: p.MeshState, Detail: p.MeshSummary})

		p.AddReady = p.OwnNpub != "" && p.NetworkID != ""
	}

	p.DNS = buildDNSControl(p)
	if tickErr == nil {
		p.Alerts = buildAlerts(p, state, c.backups)
	}

	// Only offer links that actually go somewhere: a launch card for a service
	// that is down is worse than no card, because the user blames themselves
	// for the dead page.
	if docsUp {
		p.DocsURL = c.docsURL
	}
	if meshIP := strings.SplitN(state.TunnelIP, "/", 2)[0]; meshIP != "" && piholeUp {
		p.PiholeURL = fmt.Sprintf("http://%s/admin", meshIP)
	}
	if meshIP := strings.SplitN(state.TunnelIP, "/", 2)[0]; meshIP != "" && c.mesh {
		p.MeshURL = "http://" + net.JoinHostPort(meshIP, strings.TrimPrefix(meshGateListen, ":"))
		p.MeshPanelURL = "http://" + net.JoinHostPort(meshIP, strings.TrimPrefix(meshPanelGateListen, ":"))
	}
	return p
}

func dnsRole(participant tickParticipant) string {
	switch {
	case participant.ServesDNS:
		return "DNS box"
	case participant.ServesDNSBackup:
		return "backup DNS box"
	}
	return ""
}

// newCheckName is a name nobody else will ever look up, so finding it in
// Pi-hole's query log proves the device asked this box and not its old
// resolver. It is fixed for the life of the process so the wizard page can
// keep showing the same one across refreshes.
func newCheckName() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "sovereign-check.test"
	}
	return fmt.Sprintf("sovereign-check-%s.test", hex.EncodeToString(b[:]))
}

// activeNetwork is the network the daemon is running, which the panel names
// at the top level of the tick. The DNS switch writes to it, so it must be
// the one on screen, not whichever happens to be listed first. With several
// networks and none matching (the top-level id blank or stale), there is no
// right answer: matched is false and the switch refuses rather than guess.
// The first network is still returned so the page has something to show.
func activeNetwork(state tick) (tickNetwork, bool) {
	for _, n := range state.Networks {
		if n.NetworkID != "" && n.NetworkID == state.NetworkID {
			return n, true
		}
	}
	return state.Networks[0], len(state.Networks) == 1
}

// noFraming stops another site from framing a page and steering a click onto
// one of its buttons. The tokens and origin checks cannot catch that: the
// click really does come from this page.
func noFraming(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
}

func boolTile(name string, ok bool, up, down string) tile {
	if ok {
		return tile{Name: name, State: "up", Detail: up}
	}
	return tile{Name: name, State: "down", Detail: down}
}

func (c config) fetchTick(ctx context.Context) (tick, error) {
	var state tick
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.panelAPI+"/api/tick", nil)
	if err != nil {
		return state, err
	}
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return state, fmt.Errorf("control panel returned %s", resp.Status)
	}
	return state, json.NewDecoder(resp.Body).Decode(&state)
}

func probeHTTP(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Timeout: probeTimeout,
		// A redirect to a login page still proves the service is serving.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// probeDNS resolves a name through Unbound rather than just opening a socket:
// a wedged resolver still accepts connections, and "port 53 is open" is not
// the promise this stack makes.
func probeDNS(ctx context.Context, addr string) bool {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: probeTimeout}
			return dialer.DialContext(ctx, "udp", addr)
		},
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	addrs, err := resolver.LookupHost(ctx, "example.com")
	if err != nil {
		var dnsErr *net.DNSError
		// A resolver that answers "no such host" is still resolving.
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return true
		}
		return false
	}
	return len(addrs) > 0
}
