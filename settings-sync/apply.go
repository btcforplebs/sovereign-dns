package main

// Making one Pi-hole match the shared list without wiping anything else.
//
// The box remembers which entries came from the shared list (the managed
// set). Applying a list adds or updates every entry in it, and removes only
// entries that an earlier list put there and this one dropped. Anything that
// was on the Pi-hole before it ever took a list, or that someone added in
// Pi-hole's own page, is left alone and reported as "only on this box", so
// the edit page can offer to add it to the shared list.

import (
	"context"
	"slices"
	"sort"
)

type managedSet struct {
	Groups  []string `json:"groups"`
	Lists   []string `json:"lists"`   // list.key()
	Domains []string `json:"domains"` // domain.key()
}

func managedFrom(s *settings) managedSet {
	m := managedSet{Groups: []string{}, Lists: []string{}, Domains: []string{}}
	for _, g := range s.Groups {
		m.Groups = append(m.Groups, g.Name)
	}
	for _, l := range s.Lists {
		m.Lists = append(m.Lists, l.key())
	}
	for _, d := range s.Domains {
		m.Domains = append(m.Domains, d.key())
	}
	return m
}

func sameGroups(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	if len(a) == 0 {
		a = []string{defaultGroup}
	}
	if len(b) == 0 {
		b = []string{defaultGroup}
	}
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
}

// apply returns a line per change it made. On error the Pi-hole may be part
// way there; every step is idempotent, so the next run finishes the job.
func apply(ctx context.Context, p *pihole, want *settings, prev managedSet) ([]string, error) {
	var changes []string
	st, err := p.read(ctx)
	if err != nil {
		return nil, err
	}

	// Groups first: lists and domains refer to them.
	have := map[string]group{}
	for _, g := range st.Groups {
		have[g.Name] = g
	}
	groupsChanged := false
	for _, g := range want.Groups {
		if g.Name == defaultGroup {
			continue
		}
		cur, ok := have[g.Name]
		if ok && cur == g {
			continue
		}
		if err := p.putGroup(ctx, g, ok); err != nil {
			return changes, err
		}
		changes = append(changes, "group "+g.Name)
		groupsChanged = true
	}
	if groupsChanged {
		if st, err = p.read(ctx); err != nil {
			return changes, err
		}
	}

	gravity := false
	wantLists := map[string]bool{}
	haveLists := map[string]list{}
	for _, l := range st.Lists {
		haveLists[l.key()] = l
	}
	for _, l := range want.Lists {
		wantLists[l.key()] = true
		ids, err := st.ids(l.Groups)
		if err != nil {
			return changes, err
		}
		cur, ok := haveLists[l.key()]
		if ok && cur.Comment == l.Comment && cur.Enabled == l.Enabled && sameGroups(cur.Groups, l.Groups) {
			continue
		}
		if err := p.putList(ctx, l, ids, ok); err != nil {
			return changes, err
		}
		changes = append(changes, "list "+l.key())
		gravity = true
	}
	for _, k := range prev.Lists {
		if cur, ok := haveLists[k]; ok && !wantLists[k] {
			if err := p.deleteList(ctx, cur); err != nil {
				return changes, err
			}
			changes = append(changes, "removed list "+k)
			gravity = true
		}
	}

	wantDomains := map[string]bool{}
	haveDomains := map[string]domain{}
	for _, d := range st.Domains {
		haveDomains[d.key()] = d
	}
	for _, d := range want.Domains {
		wantDomains[d.key()] = true
		ids, err := st.ids(d.Groups)
		if err != nil {
			return changes, err
		}
		cur, ok := haveDomains[d.key()]
		if ok && cur.Comment == d.Comment && cur.Enabled == d.Enabled && sameGroups(cur.Groups, d.Groups) {
			continue
		}
		if err := p.putDomain(ctx, d, ids, ok); err != nil {
			return changes, err
		}
		changes = append(changes, "domain "+d.key())
	}
	for _, k := range prev.Domains {
		if cur, ok := haveDomains[k]; ok && !wantDomains[k] {
			if err := p.deleteDomain(ctx, cur); err != nil {
				return changes, err
			}
			changes = append(changes, "removed domain "+k)
		}
	}

	// Dropped groups last, once nothing from the list points at them.
	wantGroups := map[string]bool{}
	for _, g := range want.Groups {
		wantGroups[g.Name] = true
	}
	for _, n := range prev.Groups {
		if _, ok := have[n]; ok && !wantGroups[n] && n != defaultGroup {
			if err := p.deleteGroup(ctx, n); err != nil {
				return changes, err
			}
			changes = append(changes, "removed group "+n)
		}
	}

	if gravity {
		if err := p.gravity(ctx); err != nil {
			return changes, err
		}
		changes = append(changes, "gravity rebuilt")
	}
	return changes, nil
}

// extras: what this Pi-hole has that the shared list does not. Never removed
// by a sync; shown on the page so Logen can add them to the list.
func extras(st *localState, s *settings) *localState {
	out := &localState{Groups: []group{}, Lists: []list{}, Domains: []domain{}}
	in := map[string]bool{}
	if s != nil {
		for _, g := range s.Groups {
			in["g "+g.Name] = true
		}
		for _, l := range s.Lists {
			in[l.key()] = true
		}
		for _, d := range s.Domains {
			in[d.key()] = true
		}
	}
	for _, g := range st.Groups {
		if !in["g "+g.Name] {
			out.Groups = append(out.Groups, g)
		}
	}
	for _, l := range st.Lists {
		if !in[l.key()] {
			out.Lists = append(out.Lists, l)
		}
	}
	for _, d := range st.Domains {
		if !in[d.key()] {
			out.Domains = append(out.Domains, d)
		}
	}
	return out
}
