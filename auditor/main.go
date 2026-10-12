// Command auditor is sovereign-dns Phase 5 step 5.1: a background check that
// the box's own Unbound (witness 1) is not being fed poisoned answers, by
// re-checking a sample of looked-up names against a public resolver reached
// over Oblivious DoH (witness 2). Design: PLANS/SOVEREIGN_DNS_PHASE_5_DESIGN.md
// in the Buzz workspace; see auditor/README.md.
package main

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed decoys.txt
var decoyFile string

func loadDecoys(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if n, ok := normalize(l); ok && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, n)
		}
	}
	return out
}

func main() {
	w1 := flag.String("w1", "127.0.0.1:53", "the box's own Unbound (witness 1), host:port")
	state := flag.String("state", "./auditor-state", "directory for status.json, checks.jsonl and the seen-set")
	source := flag.String("source", "", "query log to follow (dnsmasq/FTL lines or bare names); '-' reads stdin")
	once := flag.Bool("once", false, "read the source to EOF, check everything queued, write status, exit (gates)")
	interval := flag.Duration("interval", 15*time.Second, "mean time between checks; each wait is uniform in [0.5x, 1.5x]")
	rate := flag.Float64("rate", 0.05, "share of already-seen unsigned names re-checked (first-seen names always are)")
	ndecoy := flag.Int("decoys", 2, "decoy names per check")
	decoyPath := flag.String("decoy-file", "", "decoy list, one name per line (default: built in)")
	listen := flag.String("listen", "", "optional address serving GET /status (e.g. 127.0.0.1:8099)")
	flag.Parse()

	if *source == "" {
		log.Fatal("-source is required")
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		log.Fatal(err)
	}
	decoys := loadDecoys(decoyFile)
	if *decoyPath != "" {
		b, err := os.ReadFile(*decoyPath)
		if err != nil {
			log.Fatal(err)
		}
		decoys = loadDecoys(string(b))
	}
	if len(decoys) < *ndecoy+1 {
		log.Fatalf("need at least %d decoys, have %d", *ndecoy+1, len(decoys))
	}

	seen, err := openSeen(*state)
	if err != nil {
		log.Fatal(err)
	}
	box := Resolver{Addr: *w1, Timeout: 5 * time.Second}
	asn := &cymru{R: box, TTL: time.Hour}
	odoh := &ODoH{Relays: defaultRelays, Targets: defaultTargets, HTTP: newHTTPClient(*w1, 15*time.Second)}
	a := &Auditor{W1: box, W2: odoh, ASN: asn.Lookup, Decoys: decoys, Rate: *rate, NDecoy: *ndecoy, MaxQ: 500, Seen: seen}
	a.Out = &Recorder{Dir: *state, Hash: seen.hash, MaxFlag: 100, Queue: a.QueueLen}
	a.Out.Flush()

	if *listen != "" {
		go serveStatus(*listen, *state)
	}

	if *once {
		in := os.Stdin
		if *source != "-" {
			if in, err = os.Open(*source); err != nil {
				log.Fatal(err)
			}
		}
		readAll(in, a.Intake)
		for a.Cycle() {
		}
		if err := a.Out.Flush(); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *source == "-" {
		go readAll(os.Stdin, a.Intake)
	} else {
		go follow(*source, a.Intake)
	}
	for {
		time.Sleep(time.Duration(float64(*interval) * (0.5 + mrand.Float64())))
		if !a.Cycle() {
			a.Out.Flush()
		}
	}
}

// openSeen loads the salted seen-set. The salt is created once per install
// and never leaves the state directory.
func openSeen(dir string) (*SeenSet, error) {
	saltPath := filepath.Join(dir, "seen.salt")
	salt, err := os.ReadFile(saltPath)
	if os.IsNotExist(err) {
		salt = []byte(hex.EncodeToString(newSalt()))
		err = os.WriteFile(saltPath, salt, 0o600)
	}
	if err != nil {
		return nil, err
	}
	s := &SeenSet{salt: salt, m: map[string]bool{}}
	listPath := filepath.Join(dir, "seen.txt")
	if b, err := os.ReadFile(listPath); err == nil {
		for _, h := range strings.Fields(string(b)) {
			s.m[h] = true
		}
	}
	f, err := os.OpenFile(listPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s.add = func(h string) { fmt.Fprintln(f, h) }
	return s, nil
}

func serveStatus(addr, dir string) {
	http.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		b, err := os.ReadFile(filepath.Join(dir, "status.json"))
		if err != nil || !json.Valid(b) {
			http.Error(w, "no status yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.Write(b)
	})
	log.Fatal(http.ListenAndServe(addr, nil))
}
