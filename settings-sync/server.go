package main

// The edit page and its two API calls.
//
// Nothing here can change the list on its own: /api/publish only accepts an
// event already signed by the owner's key (in the browser, by his signer), so
// a forged request has nothing to offer it. The Host check is still enforced
// because /api/state shows this Pi-hole's current lists, and a DNS-rebinding
// page would otherwise be able to read them.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed page.html
var pageHTML []byte

type server struct {
	s          *syncer
	setupError string   // why the service is not syncing, if it isn't
	hosts      []string // extra Host names allowed besides loopback
}

func (h *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(pageHTML)
	})
	mux.HandleFunc("GET /api/state", h.state)
	mux.HandleFunc("POST /api/publish", h.publish)
	return h.guard(mux)
}

func (h *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.allowedHost(r.Host) {
			http.Error(w, "wrong host", http.StatusMisdirectedRequest)
			return
		}
		if r.Method != http.MethodGet {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			}
		}
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (h *server) allowedHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	for _, a := range h.hosts {
		if host == a {
			return true
		}
	}
	return false
}

type stateResponse struct {
	SetupError string      `json:"setup_error,omitempty"`
	Owner      string      `json:"owner"`
	OwnerNpub  string      `json:"owner_npub"`
	Box        string      `json:"box"`
	BoxNpub    string      `json:"box_npub"`
	Newest     *event      `json:"newest"`
	Applied    *appliedRef `json:"applied"`
	Status     status      `json:"status"`
	Local      *localState `json:"local"`
	Extras     *localState `json:"extras"`
	LocalError string      `json:"local_error,omitempty"`
}

type appliedRef struct {
	ID        string    `json:"id"`
	CreatedAt int64     `json:"created_at"`
	AppliedAt time.Time `json:"applied_at"`
}

func (h *server) state(w http.ResponseWriter, r *http.Request) {
	s := h.s
	resp := stateResponse{SetupError: h.setupError, Box: pubHex(s.key), BoxNpub: npubEncode(pubHex(s.key))}
	if s.owner != "" {
		resp.Owner, resp.OwnerNpub = s.owner, npubEncode(s.owner)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resp.Newest, resp.Status = s.newest, s.status
	if a := s.state.Applied; a != nil {
		resp.Applied = &appliedRef{a.ID, a.CreatedAt, s.state.AppliedAt}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.pihole.login(ctx); err != nil {
		resp.LocalError = err.Error()
	} else {
		st, err := s.pihole.read(ctx)
		s.pihole.logout(context.WithoutCancel(ctx))
		if err != nil {
			resp.LocalError = err.Error()
		} else {
			var cur *settings
			if s.state.Applied != nil {
				cur, _ = s.openList(s.state.Applied)
			}
			resp.Local, resp.Extras = st, extras(st, cur)
		}
	}
	writeJSONResponse(w, http.StatusOK, resp)
}

type publishRequest struct {
	Event *event `json:"event"`
	Base  string `json:"base"` // id of the list the page started from; "" for the first
}

func (h *server) publish(w http.ResponseWriter, r *http.Request) {
	s := h.s
	if h.setupError != "" {
		writeError(w, http.StatusServiceUnavailable, errors.New(h.setupError))
		return
	}
	var req publishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil || req.Event == nil {
		writeError(w, http.StatusBadRequest, errors.New("expected {event, base}"))
		return
	}
	ev := req.Event
	if err := s.acceptable(ev); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	got, err := s.openList(ev)
	if err == nil {
		err = got.validate()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Refuse to save over a list this page never saw: two boxes' pages open at
	// once would otherwise drop each other's edits.
	s.mu.Lock()
	cur := s.pickNewest(s.fetchAll(r.Context()))
	ok := s.status.RelaysOK
	s.mu.Unlock()
	if ok == 0 {
		writeError(w, http.StatusBadGateway, errors.New("no relay answered, so nothing was saved; the box keeps its current list"))
		return
	}
	if cur != nil && cur.ID != req.Base && cur.ID != ev.ID {
		writeError(w, http.StatusConflict, errors.New("a newer list was saved since this page loaded it; reload and make the change again"))
		return
	}
	if cur != nil && !newer(ev, cur) && cur.ID != ev.ID {
		writeError(w, http.StatusConflict, errors.New("this list is not newer than the one on the relays"))
		return
	}

	published := 0
	var errs []string
	for _, rl := range s.relays {
		if err := s.publish(r.Context(), rl, ev); err != nil {
			errs = append(errs, rl+": "+err.Error())
		} else {
			published++
		}
	}
	if published == 0 {
		writeError(w, http.StatusBadGateway, errors.New("no relay accepted the list: "+strings.Join(errs, "; ")))
		return
	}
	applyErr := s.run(r.Context(), []*event{ev})
	resp := map[string]any{"published": published, "relays": len(s.relays), "relay_errors": errs, "applied": applyErr == nil}
	if applyErr != nil {
		resp["apply_error"] = applyErr.Error()
	}
	writeJSONResponse(w, http.StatusOK, resp)
}

func writeJSONResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSONResponse(w, code, map[string]string{"error": err.Error()})
}
