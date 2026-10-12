package main

// The DNS switch: the one thing the hub changes rather than just reports.
//
// It never touches Docker. It asks the control panel to do it, through the
// same /api/set_network_dns route the panel's own UI uses, so the admin check,
// the config write and the daemon reload all stay in nvpn's code.
//
// The hub has no login, and loopback is not a boundary against the browser:
// any page open in any tab can submit a form to http://localhost:8080. So the
// route only accepts a POST that
//   - names a loopback Host (a DNS-rebinding page arrives under its own name),
//   - comes from this page's own origin when the browser says where it came from,
//   - echoes this process's CSRF token, which a cross-site page cannot read.
// Turning DNS off, or on while the resolver is down, also needs a second,
// explicit confirmation, enforced here and not just in the page.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// Turning the override on or off makes the panel rewrite the config and wait
// for `nvpn reload`, which takes longer than a status probe.
const switchTimeout = 20 * time.Second

type dnsControl struct {
	State         string // "on", "off", "unknown"
	CanSwitch     bool
	Blocked       string // why it can't be switched, when it can't
	NodeIsThisBox bool
	Node          string // shortened npub of the DNS node, when it is another device
	ResolverUp    bool   // Pi-hole and Unbound both answering
}

func buildDNSControl(p page) dnsControl {
	d := dnsControl{State: p.OverrideState, ResolverUp: p.PiholeUp && p.UnboundUp}
	net := p.network
	switch {
	case net == nil:
		d.Blocked = "The control panel isn't answering, so the current setting is unknown. It can't be changed until the panel is back."
		return d
	case !net.LocalIsAdmin:
		d.Blocked = "Only a network admin can change this, and this box isn't one."
		return d
	case net.DNSNodeNpub == "" && p.ownNpub == "":
		d.Blocked = "This box's identity isn't known yet, so it can't be made the DNS device."
		return d
	}
	d.CanSwitch = true
	d.NodeIsThisBox = net.DNSNodeNpub == "" || net.LocalIsDNSNode
	if !d.NodeIsThisBox {
		d.Node = shortNpub(net.DNSNodeNpub)
	}
	return d
}

// ConfirmingDNS is "off" or "on" while the dashboard is asking the reader to
// confirm that switch, and empty otherwise. A confirmation left in the URL
// after the state already changed is ignored.
func (p page) ConfirmingDNS() string {
	if !p.DNS.CanSwitch {
		return ""
	}
	switch {
	case p.Confirm == "dns-off" && p.DNS.State == "on":
		return "off"
	case p.Confirm == "dns-on" && p.DNS.State != "on" && !p.DNS.ResolverUp:
		return "on"
	}
	return ""
}

func shortNpub(npub string) string {
	if len(npub) <= 20 {
		return npub
	}
	return npub[:12] + "…" + npub[len(npub)-6:]
}

func newCSRFToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// No randomness means no way to tell our form from someone else's.
		log.Fatalf("hub: generating CSRF token: %v", err)
	}
	return hex.EncodeToString(b[:])
}

func (c config) serveDNS(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !loopbackHost(r.Host) || !sameOrigin(r) {
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
		want := r.PostForm.Get("override")
		if want != "on" && want != "off" {
			http.Error(w, "override must be on or off", http.StatusBadRequest)
			return
		}

		// Decide from fresh state, never from what the page showed when it
		// was rendered.
		p := c.buildPage(r.Context())
		p.CSRF = c.csrfToken
		if !p.DNS.CanSwitch {
			p.Error = "Nothing was changed. " + p.DNS.Blocked
			render(w, tmpl, p, http.StatusConflict)
			return
		}
		if want == p.DNS.State {
			http.Redirect(w, r, "/dashboard#dns", http.StatusSeeOther)
			return
		}
		if needsConfirm(want, p.DNS) && r.PostForm.Get("confirm") != "yes" {
			http.Redirect(w, r, "/dashboard?confirm=dns-"+want+"#dns", http.StatusSeeOther)
			return
		}

		// Turning on with no DNS device chosen makes this box the DNS device,
		// which is what the entrypoint does on a fresh install. Turning off
		// leaves the choice exactly as it was.
		node := p.network.DNSNodeNpub
		if node == "" && want == "on" {
			node = p.ownNpub
		}
		// Not tied to the browser: if the tab closes during the reload, the
		// panel may still apply the change, and the log must say what the
		// panel answered rather than "cancelled".
		ctx := context.WithoutCancel(r.Context())
		if err := c.setNetworkDNS(ctx, p.network.ID, node, want == "on"); err != nil {
			log.Printf("hub: turning network DNS %s: %v", want, err)
			p = c.buildPage(r.Context())
			p.CSRF = c.csrfToken
			p.Error = fmt.Sprintf("Couldn't turn network DNS %s: %v", want, err)
			render(w, tmpl, p, http.StatusBadGateway)
			return
		}
		log.Printf("hub: network DNS override turned %s", want)
		http.Redirect(w, r, "/dashboard#dns", http.StatusSeeOther)
	}
}

// needsConfirm: turning off drops every device back to its own resolver, and
// turning on while Pi-hole or Unbound is down makes every device's lookups
// fail (the override fails closed). Both deserve a second look.
func needsConfirm(want string, d dnsControl) bool {
	return want == "off" || !d.ResolverUp
}

func render(w http.ResponseWriter, tmpl *template.Template, p page, status int) {
	noFraming(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := tmpl.Execute(w, p); err != nil {
		log.Printf("hub: rendering page: %v", err)
	}
}

// loopbackHost: the hub is only published on 127.0.0.1, so a browser that
// reached it legitimately addressed it by a loopback name. Anything else is a
// DNS-rebinding page that resolved its own hostname to us.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin trusts a missing Origin (curl, scripts on this machine, which
// can reach the panel directly anyway) but rejects one that names any other
// origin, including the opaque "null" a sandboxed frame sends.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return strings.EqualFold(origin, "http://"+r.Host)
}

// setNetworkDNS asks the panel to change the override, then checks the state
// it returns, so a request the panel accepted but did not apply is reported
// as a failure instead of a success. It builds a fresh request: nothing from
// the browser's request is forwarded, because the panel runs
// --behind-trusted-proxy and believes forwarding headers.
func (c config) setNetworkDNS(ctx context.Context, networkID, node string, on bool) error {
	if c.mock != "" {
		return errors.New("the hub is in mock mode (HUB_MOCK), so nothing was sent")
	}
	body, err := json.Marshal(map[string]any{
		"networkId":   networkID,
		"npub":        node,
		"dnsOverride": on,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, switchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.panelAPI+"/api/set_network_dns", bytes.NewReader(body))
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
		return errors.New("this control panel build has no network DNS route (needs nostr-vpn with /api/set_network_dns)")
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
		if n.ID == networkID {
			if n.DNSOverride != on {
				return fmt.Errorf("the control panel accepted the change but still reports the override %s", onOff(n.DNSOverride))
			}
			return nil
		}
	}
	return errors.New("the control panel's reply no longer lists this network")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
