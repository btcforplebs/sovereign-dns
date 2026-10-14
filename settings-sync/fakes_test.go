package main

// Test doubles: a Pi-hole that speaks the slice of the v6 API this service
// uses, and relays that keep every event they are given (older ones too, so
// the box's own newest-wins logic is what gets tested).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeEntry struct {
	Address string `json:"address,omitempty"`
	Domain  string `json:"domain,omitempty"`
	Type    string `json:"type"`
	Kind    string `json:"kind,omitempty"`
	Comment string `json:"comment"`
	Enabled bool   `json:"enabled"`
	Groups  []int  `json:"groups"`
	ID      int    `json:"id"`
}

type fakePihole struct {
	mu       sync.Mutex
	password string
	sessions map[string]bool
	nextID   int
	groups   []localGroup
	lists    []fakeEntry
	domains  []fakeEntry
	gravity  int
	srv      *httptest.Server
}

func newFakePihole(t *testing.T) *fakePihole {
	f := &fakePihole{password: "pw", sessions: map[string]bool{}, nextID: 1,
		groups: []localGroup{{ID: 0, Name: defaultGroup, Enabled: true}}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePihole) client() *pihole { return newPihole(f.srv.URL, f.password) }

func (f *fakePihole) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/")
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i], _ = url.PathUnescape(parts[i])
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
	if parts[0] == "auth" {
		switch {
		case r.Method == http.MethodPost && body["password"] == f.password:
			sid := "sid" + strings.Repeat("x", len(f.sessions)+1)
			f.sessions[sid] = true
			reply(200, map[string]any{"session": map[string]any{"valid": true, "sid": sid}})
		case r.Method == http.MethodPost:
			reply(401, map[string]any{"session": map[string]any{"valid": false, "message": "password incorrect"}})
		case r.Method == http.MethodDelete:
			delete(f.sessions, r.Header.Get("X-FTL-SID"))
			w.WriteHeader(204)
		}
		return
	}
	if !f.sessions[r.Header.Get("X-FTL-SID")] {
		reply(401, map[string]any{"error": "unauthorized"})
		return
	}
	ints := func(v any) []int {
		var out []int
		xs, _ := v.([]any)
		for _, x := range xs {
			out = append(out, int(x.(float64)))
		}
		sort.Ints(out)
		return out
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	bln := func(k string) bool { b, _ := body[k].(bool); return b }
	switch {
	case parts[0] == "groups" && r.Method == http.MethodGet:
		reply(200, map[string]any{"groups": f.groups})
	case parts[0] == "groups" && r.Method == http.MethodPost:
		f.groups = append(f.groups, localGroup{ID: f.nextID, Name: str("name"), Comment: str("comment"), Enabled: bln("enabled")})
		f.nextID++
		reply(201, map[string]any{})
	case parts[0] == "groups" && r.Method == http.MethodPut:
		for i, g := range f.groups {
			if g.Name == parts[1] {
				f.groups[i].Name, f.groups[i].Comment, f.groups[i].Enabled = str("name"), str("comment"), bln("enabled")
			}
		}
		reply(200, map[string]any{})
	case parts[0] == "groups" && r.Method == http.MethodDelete:
		for i, g := range f.groups {
			if g.Name == parts[1] && g.ID != 0 {
				f.groups = append(f.groups[:i], f.groups[i+1:]...)
				break
			}
		}
		w.WriteHeader(204)
	case parts[0] == "lists" && r.Method == http.MethodGet:
		reply(200, map[string]any{"lists": f.lists})
	case parts[0] == "lists" && r.Method == http.MethodPost:
		typ := r.URL.Query().Get("type")
		for _, l := range f.lists {
			if l.Address == str("address") && l.Type == typ {
				reply(201, map[string]any{"processed": map[string]any{"errors": []any{map[string]any{"item": l.Address, "error": "UNIQUE constraint failed"}}}})
				return
			}
		}
		f.lists = append(f.lists, fakeEntry{Address: str("address"), Type: typ, Comment: str("comment"), Enabled: bln("enabled"), Groups: ints(body["groups"]), ID: f.nextID})
		f.nextID++
		reply(201, map[string]any{"processed": map[string]any{"errors": []any{}}})
	case parts[0] == "lists" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		typ := r.URL.Query().Get("type")
		for i, l := range f.lists {
			if l.Address == parts[1] && l.Type == typ {
				if r.Method == http.MethodDelete {
					f.lists = append(f.lists[:i], f.lists[i+1:]...)
				} else {
					f.lists[i].Comment, f.lists[i].Enabled, f.lists[i].Groups = str("comment"), bln("enabled"), ints(body["groups"])
				}
				reply(200, map[string]any{})
				return
			}
		}
		reply(404, map[string]any{"error": "not found"})
	case parts[0] == "domains" && r.Method == http.MethodGet:
		reply(200, map[string]any{"domains": f.domains})
	case parts[0] == "domains" && r.Method == http.MethodPost:
		f.domains = append(f.domains, fakeEntry{Domain: str("domain"), Type: parts[1], Kind: parts[2], Comment: str("comment"), Enabled: bln("enabled"), Groups: ints(body["groups"]), ID: f.nextID})
		f.nextID++
		reply(201, map[string]any{"processed": map[string]any{"errors": []any{}}})
	case parts[0] == "domains" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		for i, d := range f.domains {
			if d.Type == parts[1] && d.Kind == parts[2] && d.Domain == parts[3] {
				if r.Method == http.MethodDelete {
					f.domains = append(f.domains[:i], f.domains[i+1:]...)
				} else {
					f.domains[i].Comment, f.domains[i].Enabled, f.domains[i].Groups = str("comment"), bln("enabled"), ints(body["groups"])
				}
				reply(200, map[string]any{})
				return
			}
		}
		reply(404, map[string]any{"error": "not found"})
	case path == "action/gravity" && r.Method == http.MethodPost:
		f.gravity++
		_, _ = w.Write([]byte("  [✓] Done.\n"))
	default:
		reply(404, map[string]any{"error": "no route " + r.Method + " " + path})
	}
}

// snapshot: the Pi-hole's filtering state in comparable form.
func (f *fakePihole) snapshot(t *testing.T) string {
	t.Helper()
	p := f.client()
	ctx := context.Background()
	if err := p.login(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.logout(ctx)
	st, err := p.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(st.Groups, func(i, j int) bool { return st.Groups[i].Name < st.Groups[j].Name })
	sort.Slice(st.Lists, func(i, j int) bool { return st.Lists[i].key() < st.Lists[j].key() })
	sort.Slice(st.Domains, func(i, j int) bool { return st.Domains[i].key() < st.Domains[j].key() })
	b, _ := json.Marshal(st)
	return string(b)
}

// fakeRelays: relay URL -> every event it was sent. A relay in down fails.
type fakeRelays struct {
	mu     sync.Mutex
	events map[string][]*event
	down   map[string]bool
}

func newFakeRelays() *fakeRelays {
	return &fakeRelays{events: map[string][]*event{}, down: map[string]bool{}}
}

func (r *fakeRelays) fetch(_ context.Context, url, owner string) ([]*event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down[url] {
		return nil, errors.New("connection refused")
	}
	var out []*event
	for _, ev := range r.events[url] {
		cp := *ev
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRelays) publish(_ context.Context, url string, ev *event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down[url] {
		return errors.New("connection refused")
	}
	cp := *ev
	r.events[url] = append(r.events[url], &cp)
	return nil
}

// put stores ev on every relay, as if someone published it there directly.
func (r *fakeRelays) put(ev *event, urls ...string) {
	for _, u := range urls {
		_ = r.publish(context.Background(), u, ev)
	}
}
