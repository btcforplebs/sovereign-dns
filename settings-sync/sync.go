package main

// The sync loop: pull the newest list, check it, apply it, remember it.
//
// Rules, in order:
//  1. Only events of the settings kind and d tag, whose signature verifies and
//     whose author is the configured owner, count. Anything else is dropped
//     before it is opened.
//  2. Of those, the newest wins (NIP-01: later created_at, then lowest id).
//     An event dated more than ten minutes ahead of this box's clock is
//     dropped, or one bad clock could freeze every later edit.
//  3. The box never goes back: a list older than the one it already applied
//     is ignored, even if a relay serves nothing newer.
//  4. If the newest list cannot be opened by this box (not sealed to it,
//     corrupt) or fails validation, the box keeps what it had. It does not
//     fall back to an older readable list; that would be a rollback.
//  5. With no relay answering, the box re-applies its saved copy, so the
//     Pi-hole keeps serving the last good list.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

const maxFutureSkew = 10 * time.Minute

type savedState struct {
	Applied   *event     `json:"applied,omitempty"` // the sealed event last applied in full
	Managed   managedSet `json:"managed"`
	AppliedAt time.Time  `json:"applied_at"`
}

type status struct {
	LastRun     time.Time `json:"last_run"`
	RelaysOK    int       `json:"relays_ok"`
	RelaysTotal int       `json:"relays_total"`
	Note        string    `json:"note"`  // why the newest list was not taken, when it wasn't
	Error       string    `json:"error"` // last apply error
	Changes     []string  `json:"changes"`
}

type syncer struct {
	owner   string // hex
	key     *btcec.PrivateKey
	relays  []string
	pihole  *pihole
	dataDir string
	now     func() time.Time

	// fetch and publish are the relay calls; tests swap them for an
	// in-memory relay.
	fetch   func(ctx context.Context, url, owner string) ([]*event, error)
	publish func(ctx context.Context, url string, ev *event) error

	mu     sync.Mutex // one run at a time
	state  savedState
	status status
	newest *event // newest valid event seen, sealed
}

func (s *syncer) statePath() string { return filepath.Join(s.dataDir, "state.json") }

func (s *syncer) load() error {
	b, err := os.ReadFile(s.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.state)
}

func (s *syncer) save() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// acceptable: rule 1 and the clock half of rule 2.
func (s *syncer) acceptable(ev *event) error {
	switch {
	case ev.Kind != settingsKind || ev.tag("d") != settingsD:
		return errors.New("not a settings list")
	case ev.PubKey != s.owner:
		return errors.New("not signed by the owner")
	case time.Unix(ev.CreatedAt, 0).After(s.now().Add(maxFutureSkew)):
		return errors.New("dated in the future")
	}
	return ev.verify()
}

func (s *syncer) pickNewest(evs []*event) *event {
	var best *event
	for _, ev := range evs {
		if s.acceptable(ev) == nil && newer(ev, best) {
			best = ev
		}
	}
	return best
}

func (s *syncer) fetchAll(ctx context.Context) []*event {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var all []*event
	ok := 0
	for _, r := range s.relays {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			evs, err := s.fetch(ctx, r, s.owner)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && len(evs) == 0 {
				log.Printf("relay %s: %v", r, err)
				return
			}
			ok++
			all = append(all, evs...)
		}(r)
	}
	wg.Wait()
	s.status.RelaysOK, s.status.RelaysTotal = ok, len(s.relays)
	return all
}

// runLocked is one pass; the caller holds mu and a Pi-hole session. fetched,
// when not nil, is used instead of asking the relays (the publish path already
// has the event in hand).
func (s *syncer) runLocked(ctx context.Context, fetched []*event) error {
	if fetched == nil {
		fetched = s.fetchAll(ctx)
	}
	s.status.LastRun = s.now()
	s.status.Note = ""
	if best := s.pickNewest(fetched); best != nil && newer(best, s.newest) {
		s.newest = best
	}

	target := s.state.Applied
	var want *settings
	if cand := s.newest; cand != nil && (target == nil || newer(cand, target)) {
		got, err := open(cand, s.key, s.owner)
		if err == nil {
			err = got.validate()
		}
		if err != nil {
			s.status.Note = fmt.Sprintf("ignored the newest list (%s): %v", cand.ID[:12], err)
			log.Print(s.status.Note)
		} else {
			target, want = cand, got
		}
	}
	if target == nil {
		s.status.Error = ""
		return nil // no list yet: leave the Pi-hole exactly as it is
	}
	if want == nil {
		got, err := open(target, s.key, s.owner)
		if err != nil {
			return s.fail(fmt.Errorf("saved list no longer opens: %w", err))
		}
		want = got
	}

	changes, err := apply(ctx, s.pihole, want, s.state.Managed)
	s.status.Changes = changes
	if err != nil {
		return s.fail(err)
	}
	if len(changes) > 0 {
		log.Printf("applied list %s: %v", target.ID[:12], changes)
	}
	if s.state.Applied == nil || s.state.Applied.ID != target.ID {
		s.state.AppliedAt = s.now()
	}
	s.state.Applied = target
	s.state.Managed = managedFrom(want)
	s.status.Error = ""
	return s.save()
}

func (s *syncer) fail(err error) error {
	s.status.Error = err.Error()
	log.Printf("sync: %v", err)
	return err
}

// run is one pass inside one Pi-hole session.
func (s *syncer) run(ctx context.Context, fetched []*event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pihole.login(ctx); err != nil {
		return s.fail(fmt.Errorf("pihole login: %w", err))
	}
	defer s.pihole.logout(context.WithoutCancel(ctx))
	return s.runLocked(ctx, fetched)
}
