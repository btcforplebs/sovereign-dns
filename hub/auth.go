package main

// Opening the hub from other computers on the home network.
//
// By default compose publishes the hub on the host's loopback only, and the
// hub has no login: the only people who can reach it are already sitting at
// the box. A headless box (a Raspberry Pi with no screen) has nobody sitting
// at it, so `scripts/hub-lan.sh on` (or the installer, which offers it when it
// finds no desktop) publishes the hub on the box's other addresses too and sets
// HUB_PASSWORD. The hub can add devices to the network, so from then on every
// page asks for that password first.
//
// Three rules, each enforced here rather than trusted to the compose file:
//   - HUB_BIND names where compose publishes the hub. Anything but loopback
//     with no HUB_PASSWORD set, and the hub refuses to start: an open admin
//     page on the network is never the result of a half-done setup.
//   - With a password set, every route needs a signed-in session, from
//     loopback too. The session cookie is the boundary that the loopback-Host
//     check stands for in the default setup (dns.go), so a DNS-rebinding page,
//     which arrives under its own name and so without the cookie, gets the
//     login page and nothing else.
//   - Only private addresses are served (home networks, loopback, Docker's own
//     bridge). A box with a public address answers the internet with 403,
//     login or not. Tailscale-style 100.64.0.0/10 addresses are refused too:
//     "the home network" is what was asked for, not every network the box
//     happens to be on.
//
// The password travels as plain HTTP on the home network; there is no TLS.
// That matches the Pi-hole admin page this stack already serves.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "sdns_hub_session"
	sessionTTL    = 30 * 24 * time.Hour
	// Wrong passwords from one address before it has to wait, and how long.
	maxFailures  = 5
	lockout      = 5 * time.Minute
	minPassword  = 8
	failureDelay = 500 * time.Millisecond
)

type auth struct {
	password string // empty: no login (loopback-only install)
	remote   bool   // HUB_BIND is not loopback

	mu       sync.Mutex
	sessions map[string]time.Time  // session id -> expiry
	failures map[string]*failCount // remote IP -> recent wrong passwords
}

type failCount struct {
	n     int
	until time.Time
}

// newAuth reads HUB_BIND and HUB_PASSWORD. It returns an error, and the hub
// does not start, when the hub would be on the network with no password.
func newAuth(bind, password string) (*auth, error) {
	a := &auth{
		password: password,
		remote:   !loopbackHost(bind),
		sessions: map[string]time.Time{},
		failures: map[string]*failCount{},
	}
	if a.remote && password == "" {
		return nil, errNoPassword
	}
	if password != "" && len(password) < minPassword {
		return nil, errShortPassword
	}
	return a, nil
}

type authError string

func (e authError) Error() string { return string(e) }

const (
	errNoPassword    = authError("HUB_BIND publishes the hub beyond this computer but HUB_PASSWORD is empty; refusing to serve an admin page with no login. Run scripts/hub-lan.sh on (or off) to fix .env")
	errShortPassword = authError("HUB_PASSWORD is shorter than 8 characters")
)

// enabled reports whether pages need a login.
func (a *auth) enabled() bool { return a != nil && a.password != "" }

// privateClient: who may be served at all once the hub is on the network.
func privateClient(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// wrap puts every route behind the rules above. With no password and a
// loopback bind it changes nothing: that is today's hub, byte for byte.
func (a *auth) wrap(next http.Handler) http.Handler {
	if !a.enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !privateClient(r.RemoteAddr) {
			http.Error(w, "this page is only served to your home network", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/login":
			a.serveLogin(w, r)
			return
		case "/logout":
			a.serveLogout(w, r)
			return
		}
		if !a.signedIn(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
				http.Error(w, "sign in first", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *auth) signedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.sessions, c.Value)
		return false
	}
	return true
}

func (a *auth) checkPassword(got string) bool {
	// Hash both so the comparison takes the same time whatever the lengths.
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(a.password))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// lockedOut is true while an address has used up its wrong guesses.
func (a *auth) lockedOut(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.failures[ip]
	return f != nil && f.n >= maxFailures && time.Now().Before(f.until)
}

func (a *auth) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.failures[ip]
	if f == nil || time.Now().After(f.until) {
		f = &failCount{}
		a.failures[ip] = f
	}
	f.n++
	f.until = time.Now().Add(lockout)
}

func (a *auth) newSession() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("hub: generating session: %v", err)
	}
	id := hex.EncodeToString(b[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.sessions[id] = now.Add(sessionTTL)
	return id
}

// safeNext keeps the after-login redirect on this site: a path, never
// "//elsewhere" or a full URL.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func (a *auth) serveLogin(w http.ResponseWriter, r *http.Request) {
	noFraming(w)
	view := loginView{Next: safeNext(r.URL.Query().Get("next"))}
	switch r.Method {
	case http.MethodGet:
		renderLogin(w, view, http.StatusOK)
	case http.MethodPost:
		if !sameOrigin(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		view.Next = safeNext(r.PostForm.Get("next"))
		ip := clientIP(r)
		if a.lockedOut(ip) {
			view.Error = "Too many wrong passwords. Wait five minutes and try again."
			renderLogin(w, view, http.StatusTooManyRequests)
			return
		}
		if !a.checkPassword(r.PostForm.Get("password")) {
			a.recordFailure(ip)
			time.Sleep(failureDelay)
			log.Printf("hub: wrong hub password from %s", ip)
			view.Error = "That password is not right."
			renderLogin(w, view, http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    a.newSession(),
			Path:     "/",
			MaxAge:   int(sessionTTL / time.Second),
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, view.Next, http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *auth) serveLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type loginView struct {
	Next  string
	Error string
}

func renderLogin(w http.ResponseWriter, v loginView, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := loginTmpl.Execute(w, v); err != nil {
		log.Printf("hub: rendering login: %v", err)
	}
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in · Sovereign DNS</title>
<style>
  body { font: 17px/1.5 system-ui, sans-serif; background: #0f1115; color: #e8e8ea; display: grid; place-items: center; min-height: 100vh; margin: 0; }
  form { background: #181b22; padding: 28px; border-radius: 12px; width: min(360px, 90vw); }
  h1 { font-size: 22px; margin: 0 0 6px; }
  p { color: #a0a4ad; margin: 0 0 18px; font-size: 15px; }
  input[type=password] { width: 100%; box-sizing: border-box; padding: 10px; font-size: 17px; border-radius: 8px; border: 1px solid #333; background: #0f1115; color: inherit; }
  button { margin-top: 14px; width: 100%; padding: 10px; font-size: 17px; border: 0; border-radius: 8px; background: #3b82f6; color: white; }
  .err { color: #f87171; }
</style>
</head>
<body>
<form method="post" action="/login">
  <h1>Sovereign DNS</h1>
  <p>Enter the dashboard password you picked when you turned on home-network access.</p>
  {{if .Error}}<p class="err">{{.Error}}</p>{{end}}
  <input type="hidden" name="next" value="{{.Next}}">
  <input type="password" name="password" autocomplete="current-password" autofocus required aria-label="Dashboard password">
  <button type="submit">Sign in</button>
</form>
</body>
</html>
`))

// docsProxy serves the read-only docs site under /docs/, so a reader on
// another computer can open them: the docs container, like the control panel,
// is published on the box's loopback only. GET and HEAD only, and nothing
// from the reader's request but the path travels on (no cookie, no session).
func docsProxy(docsAddr string) http.Handler {
	target := &url.URL{Scheme: "http", Host: docsAddr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = "/" + strings.TrimPrefix(pr.In.URL.Path, "/docs/")
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = ""
			pr.Out.Host = target.Host
			pr.Out.Header = http.Header{}
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
