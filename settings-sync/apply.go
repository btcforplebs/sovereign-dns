package main

// Making one Pi-hole match the shared list without wiping anything else.
//
// The box remembers which entries it created itself (the managed set).
// Applying a list adds or updates every entry in it, and removes only entries
// this box created for an earlier list that this one dropped. An entry that
// was already on the Pi-hole never becomes managed, even when the shared list
// names it too: a later list that drops it leaves it in place. Entries not in
// the list are reported as "only on this box", so the edit page can offer to
// add them to the shared list.

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

// tracker is the managed set being updated as apply goes, so a run that fails
// part way still records what it created.
type tracker map[string]bool

func track(keys []string) tracker {
	t := tracker{}
	for _, k := range keys {
		t[k] = true
	}
	return t
}

func (t tracker) keys() []string {
	out := []string{}
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// apply returns a line per change it made and the new managed set. On error
// the Pi-hole may be part way there and the managed set says exactly how far;
// every step is idempotent, so the next run finishes the job.
func apply(ctx context.Context, p *pihole, want *settings, prev managedSet) ([]string, managedSet, error) {
	mg, ml, md := track(prev.Groups), track(prev.Lists), track(prev.Domains)
	var changes []string
	var st *localState
	var err error
	out := func(err error) ([]string, managedSet, error) {
		return changes, managedSet{Groups: mg.keys(), Lists: ml.keys(), Domains: md.keys()}, err
	}
	if st, err = p.read(ctx); err != nil {
		return out(err)
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
			return out(err)
		}
		if !ok {
			mg[g.Name] = true
		}
		changes = append(changes, "group "+g.Name)
		groupsChanged = true
	}
	if groupsChanged {
		if st, err = p.read(ctx); err != nil {
			return out(err)
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
			return out(err)
		}
		cur, ok := haveLists[l.key()]
		if ok && cur.Comment == l.Comment && cur.Enabled == l.Enabled && sameGroups(cur.Groups, l.Groups) {
			continue
		}
		if err := p.putList(ctx, l, ids, ok); err != nil {
			return out(err)
		}
		if !ok {
			ml[l.key()] = true
		}
		changes = append(changes, "list "+l.key())
		gravity = true
	}
	for _, k := range prev.Lists {
		if wantLists[k] {
			continue
		}
		if cur, ok := haveLists[k]; ok {
			if err := p.deleteList(ctx, cur); err != nil {
				return out(err)
			}
			changes = append(changes, "removed list "+k)
			gravity = true
		}
		delete(ml, k)
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
			return out(err)
		}
		cur, ok := haveDomains[d.key()]
		if ok && cur.Comment == d.Comment && cur.Enabled == d.Enabled && sameGroups(cur.Groups, d.Groups) {
			continue
		}
		if err := p.putDomain(ctx, d, ids, ok); err != nil {
			return out(err)
		}
		if !ok {
			md[d.key()] = true
		}
		changes = append(changes, "domain "+d.key())
	}
	for _, k := range prev.Domains {
		if wantDomains[k] {
			continue
		}
		if cur, ok := haveDomains[k]; ok {
			if err := p.deleteDomain(ctx, cur); err != nil {
				return out(err)
			}
			changes = append(changes, "removed domain "+k)
		}
		delete(md, k)
	}

	// Dropped groups last, once nothing from the list points at them.
	wantGroups := map[string]bool{}
	for _, g := range want.Groups {
		wantGroups[g.Name] = true
	}
	for _, n := range prev.Groups {
		if wantGroups[n] || n == defaultGroup {
			continue
		}
		if _, ok := have[n]; ok {
			if err := p.deleteGroup(ctx, n); err != nil {
				return out(err)
			}
			changes = append(changes, "removed group "+n)
		}
		delete(mg, n)
	}

	if gravity {
		if err := p.gravity(ctx); err != nil {
			return out(err)
		}
		changes = append(changes, "gravity rebuilt")
	}
	return out(nil)
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
