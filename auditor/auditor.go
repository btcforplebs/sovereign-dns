package main

// The background auditor (design note 6.1, step 5.1). Client lookups are never
// touched: the box keeps answering from its own Unbound. Names the box looked
// up are sampled onto a queue; a worker re-checks each one later against a
// public resolver over Oblivious DoH, mixed with decoy names, and flags any
// disagreement.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"time"
)

type Witness2 interface {
	Query(name string, qtype uint16) (Answer, Route, error)
}

type Auditor struct {
	W1 interface {
		Query(string, uint16) (Answer, error)
	}
	W2     Witness2
	ASN    ASNLookup
	Decoys []string
	Rate   float64 // share of already-seen unsigned names re-checked
	NDecoy int
	MaxQ   int
	Seen   *SeenSet
	Out    *Recorder
	rnd    func() float64

	mu    sync.Mutex
	queue []string
}

// Intake is called for every name the box resolved. It is cheap: one query
// to the box's own Unbound, which already has the answer cached.
func (a *Auditor) Intake(name string) {
	name, ok := normalize(name)
	if !ok {
		return
	}
	first := a.Seen.Add(name)
	w1, err := a.W1.Query(name, typeA)
	if err != nil {
		// Could not tell whether it is signed: treat it as unsigned, so a
		// resolver that is struggling gets checked rather than skipped.
		a.Out.Count("intake_errors")
	} else if w1.AD {
		// Signed and validated by the box itself: no witness needed (6.1 step 2).
		a.Out.Count("secure_skipped")
		return
	}
	rnd := a.rnd
	if rnd == nil {
		rnd = mrand.Float64
	}
	if !first && rnd() >= a.Rate {
		a.Out.Count("not_sampled")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queue) >= a.MaxQ {
		a.queue = a.queue[1:]
		a.Out.Count("queue_dropped") // counted, never silent
	}
	a.queue = append(a.queue, name)
	if first {
		a.Out.Count("sampled_first_seen")
	} else {
		a.Out.Count("sampled_random")
	}
}

func (a *Auditor) QueueLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.queue)
}

// Cycle checks one queued name plus NDecoy decoys, in random order. It
// returns false when the queue was empty.
func (a *Auditor) Cycle() bool {
	a.mu.Lock()
	if len(a.queue) == 0 {
		a.mu.Unlock()
		return false
	}
	name := a.queue[0]
	a.queue = a.queue[1:]
	a.mu.Unlock()

	type item struct {
		name  string
		decoy bool
	}
	items := []item{{name, false}}
	for _, d := range pickDecoys(a.Decoys, a.NDecoy, name) {
		items = append(items, item{d, true})
	}
	mrand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	for _, it := range items {
		for _, qt := range []uint16{typeA, typeAAAA} {
			a.check(it.name, qt, it.decoy)
		}
	}
	a.Out.Count("cycles")
	a.Out.Flush()
	return true
}

func (a *Auditor) check(name string, qtype uint16, decoy bool) {
	rec := CheckRecord{Time: time.Now().UTC(), Name: name, Decoy: decoy, QType: qtypeName(qtype)}
	w1, err1 := a.W1.Query(name, qtype)
	w2, route, err2 := a.W2.Query(name, qtype)
	rec.Route = route
	switch {
	case err1 != nil:
		rec.Verdict = Verdict{Inconclusive, "box resolver: " + err1.Error()}
	case err2 != nil:
		rec.Verdict = Verdict{Inconclusive, "public resolver: " + err2.Error()}
	default:
		rec.Box, rec.Public = &w1, &w2
		rec.Verdict = compare(w1, w2, a.ASN)
	}
	a.Out.Record(rec)
	a.Out.Flush() // a slow witness must not leave status.json stale for a whole cycle
}

func pickDecoys(list []string, n int, not string) []string {
	var out []string
	for _, i := range mrand.Perm(len(list)) {
		if len(out) == n {
			break
		}
		if list[i] != not {
			out = append(out, list[i])
		}
	}
	return out
}

// normalize drops names that never leave the house or the mesh: they have no
// public answer to compare with, and sending them out would leak them.
func normalize(n string) (string, bool) {
	n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
	if n == "" || !strings.Contains(n, ".") || strings.ContainsAny(n, " /\\") {
		return "", false
	}
	for _, suf := range []string{".arpa", ".local", ".lan", ".home", ".internal", ".fips", ".localhost", ".home.arpa", ".onion"} {
		if strings.HasSuffix(n, suf) {
			return "", false
		}
	}
	return n, true
}

func qtypeName(t uint16) string {
	if t == typeAAAA {
		return "AAAA"
	}
	return "A"
}

// SeenSet remembers which names the box has looked up before, as salted
// hashes, so the state directory never holds a readable browsing history.
type SeenSet struct {
	salt []byte
	mu   sync.Mutex
	m    map[string]bool
	add  func(h string) // persistence hook
}

func newSalt() []byte {
	b := make([]byte, 32)
	rand.Read(b)
	return b
}

func (s *SeenSet) hash(name string) string {
	h := sha256.New()
	h.Write(s.salt)
	h.Write([]byte(name))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Add marks name seen and reports whether this was the first time.
func (s *SeenSet) Add(name string) bool {
	h := s.hash(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]bool{}
	}
	if s.m[h] {
		return false
	}
	s.m[h] = true
	if s.add != nil {
		s.add(h)
	}
	return true
}
