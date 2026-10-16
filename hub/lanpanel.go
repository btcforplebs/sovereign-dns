package main

// Opening the VPN control panel (nvpn-web) from other computers on the home
// network, for a box with no screen, like a Raspberry Pi.
//
// The panel has no login of its own, so compose only ever publishes it on the
// box's loopback (localhost:8082). Once the hub itself is on the home network
// (scripts/hub-lan.sh on, so HUB_BIND is not loopback and HUB_PASSWORD is
// set), the hub opens a second port, HUB_PANEL_PORT (8083), published where
// the hub is. Every request there gets the same rules as the hub's own pages
// (auth.go): private addresses only, then a signed-in session, then the
// panel, through the same proxy the mesh uses (meshPanelProxy). The session
// cookie is the hub's: a browser sends it to any port of the same host, so
// signing in to the dashboard is enough.
//
// With the hub on loopback only, the port is published on loopback too and
// nothing listens on it: the panel on the box is already at localhost:8082.

import (
	"net"
	"net/http"
)

// lanPanelListen is the hub's panel port inside its container. Compose
// publishes it as ${HUB_BIND}:${HUB_PANEL_PORT:-8083}.
const lanPanelListen = ":8092"

// lanPanelPort is the published panel port, or "" while the hub is on this
// computer only (or has no password, which newAuth already refuses off it).
func lanPanelPort(a *auth) string {
	if !a.enabled() || !a.remote {
		return ""
	}
	return env("HUB_PANEL_PORT", "8083")
}

// lanPanel is the hub's home-network panel port: the login, then the panel.
// Like the mesh panel port, a page on another port of the same host (the
// dashboard is one, but so is anything else the box serves) may link here,
// not read a signed-in page with a script.
func lanPanel(a *auth, panelAPI string) http.Handler {
	next := meshPanel(a, panelAPI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if crossPageFetch(r) {
			http.Error(w, "this page can't be fetched from another site's page", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// lanPanelURL is the panel's address under the name the reader used for the
// dashboard (192.168.1.50, raspberrypi.local), at the panel port.
func lanPanelURL(hubHost, panelPort string) string {
	host := hubHost
	if h, _, err := net.SplitHostPort(hubHost); err == nil {
		host = h
	}
	return "http://" + net.JoinHostPort(host, panelPort)
}
