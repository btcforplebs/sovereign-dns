package main

// Opening the hub from the other devices on the mesh (a phone on 5G, a laptop
// away from home), at this box's mesh address.
//
// Mesh traffic arrives inside the nvpn container's network namespace, on its
// tunnel interface. Nothing the host publishes is there, which is why the
// hub's loopback port, and even scripts/hub-lan.sh, cannot be reached from the
// mesh. So two pieces, both this one binary:
//
//   - The gate (`/hub mesh-gate`, the hub-mesh service) runs inside nvpn's
//     namespace, like Pi-hole. It listens on port 8080 there and passes each
//     request to the hub's mesh port over dns-internal, naming the device it
//     came from in X-Forwarded-For. It decides nothing about access except
//     that the caller must be a mesh address: a container on dns-internal
//     gets 403. It holds no password and no session.
//   - The hub's mesh port (HUB_MESH=on) is a second listener, only ever
//     reached by the gate. It is served only with HUB_PASSWORD set (the hub
//     refuses to start otherwise), and every page needs the same login as on
//     the home network (auth.go). Any caller but the gate gets 403, so the
//     device address in X-Forwarded-For is only ever believed from the gate.
//
// The VPN control panel (nvpn-web) comes the same way, on its own port, so
// the network's admin can approve devices, make admins and pick DNS boxes
// from a phone: the gate listens on 8082 too and passes it to the hub's
// panel port, which needs the same login and then hands the request to the
// panel (meshPanelProxy). The panel has no login and no client-address check
// of its own, so the hub's login is the only thing in front of it, exactly as
// on the box's loopback today. (Its --behind-trusted-proxy flag only lets it
// bind a non-loopback address; it reads no forwarding header.) With no hub
// password the panel port refuses everything (meshPanel).
//
// Nothing is published on the host and no firewall rule changes: the mesh is
// the only way in, and it only admits devices the network's admin added.
// With HUB_MESH off (the default) the hub has no mesh port, and the gate
// answers that the dashboard is not open to the mesh.

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// The gate's port inside nvpn's namespace, so the address a device opens
	// is http://<box mesh IP>:8080. Pi-hole's admin page keeps port 80 there.
	meshGateListen = ":8080"
	// The hub's mesh port on dns-internal. Not published anywhere.
	meshHubListen = ":8090"
	// The control panel from the mesh: http://<box mesh IP>:8082, the same
	// port it has on the box's loopback, and the hub's port for it.
	meshPanelGateListen = ":8082"
	meshPanelHubListen  = ":8091"
)

var errMeshNoPassword = authError("HUB_MESH=on opens the hub to the mesh but HUB_PASSWORD is empty; refusing to serve an admin page with no login. Run scripts/hub-mesh.sh on (or off) to fix .env")

// meshOn reads HUB_MESH. Only "on" opens anything; any other value is off.
// Exactly "on", as scripts/lib/hub-password.sh reads it: if the two read
// .env differently, hub-mesh.sh off could drop a password the hub still needs.
func meshOn() bool { return env("HUB_MESH", "off") == "on" }

// meshNeedsLogin: the mesh port is never served without the password.
func meshNeedsLogin(mesh bool, a *auth) error {
	if mesh && !a.enabled() {
		return errMeshNoPassword
	}
	return nil
}

// meshHandler is the hub's mesh port: the gate only, behind the login.
// next must already be wrapped by auth (newMux does that).
func meshHandler(gate netip.Addr, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, ok := remoteIP(r.RemoteAddr)
		if !ok || from != gate {
			http.Error(w, "this port only serves the mesh gate", http.StatusForbidden)
			return
		}
		// Pi-hole's page is on the same mesh IP, another port: the same site,
		// so the browser sends it the hub's SameSite=Strict cookie. Writes
		// already fail the origin checks; this stops its scripts reading a
		// signed-in page. A link followed from it (a navigation) still works.
		if crossPageFetch(r) {
			http.Error(w, "this page can't be fetched from another site's page", http.StatusForbidden)
			return
		}
		device, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Forwarded-For")))
		if err != nil {
			http.Error(w, "the mesh gate did not say which device this is", http.StatusBadRequest)
			return
		}
		// From here on the device is the client: the lockout counts its wrong
		// passwords, not every mesh device's together.
		r2 := r.Clone(context.WithValue(r.Context(), meshGateKey{}, true))
		r2.RemoteAddr = net.JoinHostPort(device.Unmap().String(), "0")
		r2.Header.Del("X-Forwarded-For")
		next.ServeHTTP(w, r2)
	})
}

// crossPageFetch: a request another origin's page made that is not the reader
// opening a page. Browsers that send no Sec-Fetch-* headers pass, as with
// sameOrigin; every current one sends them.
func crossPageFetch(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-site", "cross-site":
		return r.Header.Get("Sec-Fetch-Mode") != "navigate"
	}
	return false
}

type meshGateKey struct{}

// fromMeshGate: the request came through the mesh port, so its client
// address is the one the gate named (auth.go caps those failures together).
func fromMeshGate(r *http.Request) bool {
	v, _ := r.Context().Value(meshGateKey{}).(bool)
	return v
}

func remoteIP(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// meshGate is the gate's handler: mesh callers only, passed to the hub's mesh
// port with the Host they asked for (so the hub's same-origin checks see the
// address in the browser) and the device's own address.
func meshGate(target string, mesh netip.Prefix, closed string) http.Handler {
	to := &url.URL{Scheme: "http", Host: target}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(to)
			pr.Out.Host = pr.In.Host
			// Rewrite has already dropped any X-Forwarded-For the caller
			// sent; this sets it to the caller's own address alone.
			pr.SetXForwarded()
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("hub mesh gate: %v", err)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(closed))
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, ok := remoteIP(r.RemoteAddr)
		if !ok || !mesh.Contains(ip) {
			http.Error(w, "this page is only served to devices on the mesh", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// closedText is what the gate answers while the hub's mesh port is closed.
func closedText(what string) string {
	return what + " is not open to the mesh.\n\nOn the box, in the sovereign-dns folder, run:\n    bash scripts/hub-mesh.sh on\n"
}

func runMeshGate() {
	target := env("HUB_MESH_TARGET", "172.28.53.12"+meshHubListen)
	mesh, err := netip.ParsePrefix(env("HUB_MESH_CIDR", "10.44.0.0/16"))
	if err != nil {
		log.Fatalf("hub mesh gate: HUB_MESH_CIDR: %v", err)
	}
	panelTarget := env("HUB_MESH_PANEL_TARGET", "172.28.53.12"+meshPanelHubListen)
	log.Printf("hub mesh gate: listening on %s for %s, passing to %s", meshGateListen, mesh, target)
	log.Printf("hub mesh gate: listening on %s for %s, passing to %s", meshPanelGateListen, mesh, panelTarget)
	panel := &http.Server{
		Addr:              meshPanelGateListen,
		Handler:           meshGate(panelTarget, mesh, closedText("The VPN control panel")),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { log.Fatal(panel.ListenAndServe()) }()
	server := &http.Server{
		Addr:              meshGateListen,
		Handler:           meshGate(target, mesh, closedText("The dashboard")),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

// meshHealth is the gate's healthcheck (the image has no shell). The gate
// shares nvpn's namespace the way Pi-hole does, and like Pi-hole it is left
// in a namespace with no tunnel when nvpn is recreated, so it checks for the
// tunnel itself rather than for its own socket.
func meshHealth() int {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 1
	}
	if !bytes.Contains(data, []byte(env("NVPN_TUN_IFACE", "utun100")+":")) {
		return 1
	}
	return 0
}

// meshPanel is the hub's panel port: the login, then the panel. With no hub
// password, auth.wrap would pass every request straight through, so this
// refuses outright instead of relying on meshNeedsLogin having stopped the
// hub from starting.
func meshPanel(a *auth, panelAPI string) http.Handler {
	if !a.enabled() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "the control panel is only opened to the mesh behind the hub password, and none is set", http.StatusForbidden)
		})
	}
	return a.wrap(meshPanelProxy(panelAPI))
}

// meshPanelProxy hands a signed-in mesh request to the control panel. A fresh
// request in the ways that matter: none of the caller's forwarding headers
// are passed (the panel reads none today; this keeps it so if one ever does),
// and the hub's session cookie stays with the hub. The browser's Host and Origin go through
// unchanged, which is what the panel's own same-origin check compares.
func meshPanelProxy(panelAPI string) http.Handler {
	to, err := url.Parse(panelAPI)
	if err != nil {
		log.Fatalf("hub: PANEL_API: %v", err)
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(to)
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("X-Real-Ip")
			// Rewrite has already dropped Forwarded and X-Forwarded-*.
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("hub: mesh panel: %v", err)
			http.Error(w, "The VPN control panel isn't answering. On the box, run: docker compose up -d web", http.StatusBadGateway)
		},
	}
}
