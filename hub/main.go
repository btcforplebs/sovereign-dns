// Command hub serves the sovereign-dns landing page: what's running, how to
// reach it, and how to add a device — the things that otherwise require
// reading `docker compose logs` and knowing which container to exec into.
//
// It is deliberately read-only. Every mutable setting in this stack already
// has an owner with a UI (devices in the control panel, blocklists in Pi-hole,
// the rest in .env), so the hub launches those rather than re-implementing
// them badly. It holds no credentials and never touches the Docker socket:
// status comes from probing the services the same way a user would.
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
	mock         string // HUB_MOCK: render a canned state instead of probing (screenshots, demos)
	checkName    string // unique name a device can look up to prove it resolves through Pi-hole
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
	DaemonRunning bool   `json:"daemonRunning"`
	OwnNpub       string `json:"ownNpub"`
	NetworkID     string `json:"networkId"`
	TunnelIP      string `json:"tunnelIp"`
	MagicDNS      string `json:"magicDnsSuffix"`
	Networks      []struct {
		Name                string `json:"name"`
		NetworkID           string `json:"networkId"`
		DNSOverride         bool   `json:"dnsOverride"`
		LocalIsDNSNode      bool   `json:"localIsDnsNode"`
		InboundJoinRequests []struct {
			Npub string `json:"npub"`
		} `json:"inboundJoinRequests"`
		Participants []struct {
			Alias     string `json:"alias"`
			Npub      string `json:"npub"`
			TunnelIP  string `json:"tunnelIp"`
			Reachable bool   `json:"reachable"`
			State     string `json:"state"`
			// Presence over the relays is not a data path. A device can be
			// "online" here and still pass no traffic, so this is what
			// separates an amber tile from a green one.
			DirectProbePending bool   `json:"fipsDirectProbePending"`
			TransportType      string `json:"fipsTransportType"`
			StatusText         string `json:"statusText"`
		} `json:"participants"`
	} `json:"networks"`
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
}

type page struct {
	Tiles         []tile
	PendingJoins  int
	PanelURL      string
	PiholeURL     string
	DocsURL       string
	OwnNpub       string
	NetworkID     string
	NetworkName   string
	InviteCommand string
	Devices       []device
	MeshSummary   string
	MeshState     string
	Settings      []setting
	OverrideState string
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
}

func main() {
	cfg := loadConfig()
	mux := newMux(cfg)

	log.Printf("hub: listening on %s", cfg.listen)
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

// newMux wires the routes. Split from main so tests can serve a request
// against a config whose probes point at nothing.
func newMux(cfg config) *http.ServeMux {
	tmpl := template.Must(template.New("page").Parse(pageHTML))

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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("hub: rendering page: %v", err)
		}
	})
	mux.HandleFunc("/setup", cfg.serveWizard)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data := cfg.buildPage(r.Context())
		if data.FirstRun {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("hub: rendering page: %v", err)
		}
	})
	return mux
}

func loadConfig() config {
	webPort := env("WEB_UI_PORT", "8082")
	docsPort := env("DOCS_PORT", "8081")
	piholePort := env("PIHOLE_WEB_PORT", "80")
	return config{
		mock:        env("HUB_MOCK", ""),
		checkName:   newCheckName(),
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
	if len(state.Networks) > 0 {
		net := state.Networks[0]
		p.NetworkName = net.Name
		p.PendingJoins = len(net.InboundJoinRequests)
		p.OverrideState = "off"
		if net.DNSOverride {
			p.OverrideState = "on"
		}
		if net.NetworkID != "" {
			p.NetworkID = net.NetworkID
		}

		carrying := 0
		for _, participant := range net.Participants {
			if participant.State == "local" {
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

		if p.OwnNpub != "" && p.NetworkID != "" {
			p.InviteCommand = fmt.Sprintf(
				"nvpn join-manual --admin-device-id %s --network-id %s", p.OwnNpub, p.NetworkID)
		}
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
	return p
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
