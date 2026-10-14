package main

// A small client for Pi-hole v6's REST API (/api), limited to the settings
// the shared list carries: groups, subscribed lists and domains.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type pihole struct {
	base     string // e.g. http://172.28.53.10:80
	password string
	http     *http.Client
	sid      string
}

func newPihole(base, password string) *pihole {
	return &pihole{base: strings.TrimSuffix(base, "/"), password: password, http: &http.Client{Timeout: 30 * time.Second}}
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("pihole: HTTP %d: %s", e.Status, e.Body) }

func (p *pihole) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+"/api"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.sid != "" {
		req.Header.Set("X-FTL-SID", p.sid)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		return &apiError{resp.StatusCode, strings.TrimSpace(string(raw))}
	}
	// Batch endpoints answer 201 even when an item failed, and list the
	// failures under processed.errors.
	var proc struct {
		Processed *struct {
			Errors []struct {
				Item  any    `json:"item"`
				Error string `json:"error"`
			} `json:"errors"`
		} `json:"processed"`
	}
	if json.Unmarshal(raw, &proc) == nil && proc.Processed != nil && len(proc.Processed.Errors) > 0 {
		e := proc.Processed.Errors[0]
		return fmt.Errorf("pihole: %s %s: %v: %s", method, path, e.Item, e.Error)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// login opens a session. Pi-hole has a small number of session seats, so
// every login is paired with logout.
func (p *pihole) login(ctx context.Context) error {
	var r struct {
		Session struct {
			Valid   bool   `json:"valid"`
			SID     string `json:"sid"`
			Message string `json:"message"`
		} `json:"session"`
	}
	if err := p.do(ctx, http.MethodPost, "/auth", map[string]string{"password": p.password}, &r); err != nil {
		return err
	}
	if !r.Session.Valid {
		return fmt.Errorf("pihole login refused: %s", r.Session.Message)
	}
	p.sid = r.Session.SID
	return nil
}

func (p *pihole) logout(ctx context.Context) {
	if p.sid != "" {
		_ = p.do(ctx, http.MethodDelete, "/auth", nil, nil)
		p.sid = ""
	}
}

type localGroup struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Enabled bool   `json:"enabled"`
}

// localState is one box's Pi-hole, in the shared list's terms (group names,
// not ids), plus the name<->id map needed to write it back.
type localState struct {
	Groups   []group  `json:"groups"`
	Lists    []list   `json:"lists"`
	Domains  []domain `json:"domains"`
	groupIDs map[string]int
}

func (p *pihole) read(ctx context.Context) (*localState, error) {
	var gr struct {
		Groups []localGroup `json:"groups"`
	}
	if err := p.do(ctx, http.MethodGet, "/groups", nil, &gr); err != nil {
		return nil, err
	}
	st := &localState{groupIDs: map[string]int{}}
	names := map[int]string{}
	for _, g := range gr.Groups {
		st.groupIDs[g.Name] = g.ID
		names[g.ID] = g.Name
		if g.ID != 0 {
			st.Groups = append(st.Groups, group{Name: g.Name, Comment: g.Comment, Enabled: g.Enabled})
		}
	}
	if _, ok := names[0]; !ok {
		names[0] = defaultGroup
	}
	toNames := func(ids []int) []string {
		out := []string{}
		for _, id := range ids {
			if n, ok := names[id]; ok {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	var lr struct {
		Lists []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Comment string `json:"comment"`
			Enabled bool   `json:"enabled"`
			Groups  []int  `json:"groups"`
		} `json:"lists"`
	}
	if err := p.do(ctx, http.MethodGet, "/lists", nil, &lr); err != nil {
		return nil, err
	}
	for _, l := range lr.Lists {
		st.Lists = append(st.Lists, list{Address: l.Address, Type: l.Type, Comment: l.Comment, Enabled: l.Enabled, Groups: toNames(l.Groups)})
	}
	var dr struct {
		Domains []struct {
			Domain  string `json:"domain"`
			Type    string `json:"type"`
			Kind    string `json:"kind"`
			Comment string `json:"comment"`
			Enabled bool   `json:"enabled"`
			Groups  []int  `json:"groups"`
		} `json:"domains"`
	}
	if err := p.do(ctx, http.MethodGet, "/domains", nil, &dr); err != nil {
		return nil, err
	}
	for _, d := range dr.Domains {
		st.Domains = append(st.Domains, domain{Domain: d.Domain, Type: d.Type, Kind: d.Kind, Comment: d.Comment, Enabled: d.Enabled, Groups: toNames(d.Groups)})
	}
	return st, nil
}

func (st *localState) ids(names []string) ([]int, error) {
	out := []int{}
	for _, n := range names {
		if n == defaultGroup {
			out = append(out, 0)
			continue
		}
		id, ok := st.groupIDs[n]
		if !ok {
			return nil, fmt.Errorf("group %q does not exist on this Pi-hole", n)
		}
		out = append(out, id)
	}
	if len(names) == 0 {
		out = append(out, 0) // Pi-hole puts an entry with no groups in Default
	}
	sort.Ints(out)
	return out, nil
}

func (p *pihole) putGroup(ctx context.Context, g group, exists bool) error {
	body := map[string]any{"name": g.Name, "comment": g.Comment, "enabled": g.Enabled}
	if exists {
		return p.do(ctx, http.MethodPut, "/groups/"+url.PathEscape(g.Name), body, nil)
	}
	return p.do(ctx, http.MethodPost, "/groups", body, nil)
}

func (p *pihole) deleteGroup(ctx context.Context, name string) error {
	return p.do(ctx, http.MethodDelete, "/groups/"+url.PathEscape(name), nil, nil)
}

func (p *pihole) putList(ctx context.Context, l list, groups []int, exists bool) error {
	q := "?type=" + url.QueryEscape(l.Type)
	if exists {
		body := map[string]any{"comment": l.Comment, "type": l.Type, "groups": groups, "enabled": l.Enabled}
		return p.do(ctx, http.MethodPut, "/lists/"+url.PathEscape(l.Address)+q, body, nil)
	}
	body := map[string]any{"address": l.Address, "comment": l.Comment, "groups": groups, "enabled": l.Enabled}
	return p.do(ctx, http.MethodPost, "/lists"+q, body, nil)
}

func (p *pihole) deleteList(ctx context.Context, l list) error {
	return p.do(ctx, http.MethodDelete, "/lists/"+url.PathEscape(l.Address)+"?type="+url.QueryEscape(l.Type), nil, nil)
}

func (p *pihole) putDomain(ctx context.Context, d domain, groups []int, exists bool) error {
	if exists {
		body := map[string]any{"type": d.Type, "kind": d.Kind, "comment": d.Comment, "groups": groups, "enabled": d.Enabled}
		return p.do(ctx, http.MethodPut, "/domains/"+d.Type+"/"+d.Kind+"/"+url.PathEscape(d.Domain), body, nil)
	}
	body := map[string]any{"domain": d.Domain, "comment": d.Comment, "groups": groups, "enabled": d.Enabled}
	return p.do(ctx, http.MethodPost, "/domains/"+d.Type+"/"+d.Kind, body, nil)
}

func (p *pihole) deleteDomain(ctx context.Context, d domain) error {
	return p.do(ctx, http.MethodDelete, "/domains/"+d.Type+"/"+d.Kind+"/"+url.PathEscape(d.Domain), nil, nil)
}

// gravity rebuilds the blocking database after the subscribed lists change.
// Pi-hole streams its progress; reading it to the end waits for the run.
func (p *pihole) gravity(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/api/action/gravity", nil)
	if err != nil {
		return err
	}
	if p.sid != "" {
		req.Header.Set("X-FTL-SID", p.sid)
	}
	c := *p.http
	c.Timeout = 0
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return &apiError{resp.StatusCode, "gravity"}
	}
	return nil
}
