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
// Nothing is published on the host and no firewall rule changes: the mesh is
// the only way in, and it only admits devices the network's admin added.
// With HUB_MESH off (the default) the hub has no mesh port, and the gate
// answers that the dashboard is not open to the mesh.

import (
	"bytes"
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
)

var errMeshNoPassword = authError("HUB_MESH=on opens the hub to the mesh but HUB_PASSWORD is empty; refusing to serve an admin page with no login. Run scripts/hub-mesh.sh on (or off) to fix .env")

// meshOn reads HUB_MESH. Only "on" opens anything; any other value is off.
func meshOn() bool { return strings.EqualFold(env("HUB_MESH", "off"), "on") }

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
		device, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Forwarded-For")))
		if err != nil {
			http.Error(w, "the mesh gate did not say which device this is", http.StatusBadRequest)
			return
		}
		// From here on the device is the client: the lockout counts its wrong
		// passwords, not every mesh device's together.
		r2 := r.Clone(r.Context())
		r2.RemoteAddr = net.JoinHostPort(device.Unmap().String(), "0")
		r2.Header.Del("X-Forwarded-For")
		next.ServeHTTP(w, r2)
	})
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
func meshGate(target string, mesh netip.Prefix) http.Handler {
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
			_, _ = w.Write([]byte("The dashboard is not open to the mesh.\n\nOn the box, in the sovereign-dns folder, run:\n    bash scripts/hub-mesh.sh on\n"))
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

func runMeshGate() {
	target := env("HUB_MESH_TARGET", "172.28.53.12"+meshHubListen)
	mesh, err := netip.ParsePrefix(env("HUB_MESH_CIDR", "10.44.0.0/16"))
	if err != nil {
		log.Fatalf("hub mesh gate: HUB_MESH_CIDR: %v", err)
	}
	log.Printf("hub mesh gate: listening on %s for %s, passing to %s", meshGateListen, mesh, target)
	server := &http.Server{
		Addr:              meshGateListen,
		Handler:           meshGate(target, mesh),
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
