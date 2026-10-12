package main

// Output for the hub: status.json (rewritten atomically after every cycle)
// and checks.jsonl (one line per check, append-only). A disagreement is the
// "flag": it goes into status.json's flags list with both answers in full.
// Agreements are logged by salted hash only, so the log is not a browsing
// history.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type CheckRecord struct {
	Time     time.Time `json:"time"`
	Name     string    `json:"name,omitempty"`
	NameHash string    `json:"name_hash,omitempty"`
	Decoy    bool      `json:"decoy"`
	QType    string    `json:"qtype"`
	Verdict  Verdict   `json:"verdict"`
	Box      *Answer   `json:"box,omitempty"`
	Public   *Answer   `json:"public,omitempty"`
	Route    Route     `json:"route"`
}

type Status struct {
	Schema       int              `json:"schema"`
	Started      time.Time        `json:"started"`
	Updated      time.Time        `json:"updated"`
	Counters     map[string]int64 `json:"counters"`
	QueueLen     int              `json:"queue_len"`
	LastPublicOK *time.Time       `json:"last_public_ok"` // null = witness 2 has never answered
	LastFlag     *time.Time       `json:"last_flag"`
	Flags        []CheckRecord    `json:"flags"` // newest last, capped
}

type Recorder struct {
	Dir     string
	Hash    func(string) string
	MaxFlag int
	Queue   func() int

	mu     sync.Mutex
	st     Status
	log    *os.File
	loaded bool
}

func (r *Recorder) init() {
	if r.loaded {
		return
	}
	r.loaded = true
	r.st = Status{Schema: 1, Started: time.Now().UTC(), Counters: map[string]int64{}, Flags: []CheckRecord{}}
	// Keep flags across restarts: a flag must stay visible until someone looks.
	if b, err := os.ReadFile(filepath.Join(r.Dir, "status.json")); err == nil {
		var old Status
		if json.Unmarshal(b, &old) == nil && old.Schema == 1 {
			if old.Flags != nil {
				r.st.Flags = old.Flags
			}
			r.st.LastFlag = old.LastFlag
		}
	}
	f, err := os.OpenFile(filepath.Join(r.Dir, "checks.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		r.log = f
	}
}

func (r *Recorder) Count(k string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	r.st.Counters[k]++
}

func (r *Recorder) Record(c CheckRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	r.st.Counters["checks_"+c.Verdict.Result]++
	if c.Public != nil {
		t := c.Time
		r.st.LastPublicOK = &t
	}
	if c.Verdict.Result == Disagree {
		t := c.Time
		r.st.LastFlag = &t
		r.st.Flags = append(r.st.Flags, c)
		if len(r.st.Flags) > r.MaxFlag {
			r.st.Flags = r.st.Flags[len(r.st.Flags)-r.MaxFlag:]
		}
	} else {
		c.NameHash, c.Name = r.Hash(c.Name), ""
		c.Box, c.Public = nil, nil
	}
	if r.log != nil {
		b, _ := json.Marshal(c)
		r.log.Write(append(b, '\n'))
	}
}

// Flush writes status.json via a temp file and rename, so a reader never sees
// half a file.
func (r *Recorder) Flush() error {
	r.mu.Lock()
	r.init()
	r.st.Updated = time.Now().UTC()
	if r.Queue != nil {
		r.mu.Unlock()
		q := r.Queue()
		r.mu.Lock()
		r.st.QueueLen = q
	}
	b, err := json.MarshalIndent(r.st, "", "  ")
	r.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.Dir, ".status.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.Dir, "status.json"))
}
