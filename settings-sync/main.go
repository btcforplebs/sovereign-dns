// settings-sync keeps every DNS box's Pi-hole on one shared list of
// blocklists, allow/deny domains and groups. The list is a Nostr event signed
// by the owner's own key and sealed to his devices and the boxes it names;
// each box pulls the newest one from his relays and applies it to its own
// Pi-hole. No box is the main one, and no box ever holds the owner's key.
// See README.md.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// loadOrCreateKey: the box's own Nostr key, made on first start and kept in
// the service's volume. It only ever decrypts; it is not the owner's key and
// not nvpn's.
func loadOrCreateKey(path string) (*btcec.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("%s is not a 32-byte hex key", path)
		}
		sk, _ := btcec.PrivKeyFromBytes(raw)
		return sk, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sk, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(sk.Serialize())+"\n"), 0o600); err != nil {
		return nil, err
	}
	return sk, nil
}

func main() {
	dataDir := env("DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	key, err := loadOrCreateKey(filepath.Join(dataDir, "box.key"))
	if err != nil {
		log.Fatal(err)
	}
	interval, err := time.ParseDuration(env("SETTINGS_SYNC_INTERVAL", "2m"))
	if err != nil || interval < 10*time.Second {
		log.Fatal("SETTINGS_SYNC_INTERVAL must be a duration of at least 10s")
	}

	s := &syncer{
		key:     key,
		pihole:  newPihole(env("PIHOLE_URL", "http://172.28.53.10:80"), os.Getenv("PIHOLE_PASSWORD")),
		dataDir: dataDir,
		now:     time.Now,
		fetch:   fetchLists,
		publish: publishEvent,
	}
	srv := &server{s: s}
	for _, h := range strings.Split(env("SETTINGS_SYNC_HOSTS", ""), ",") {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			srv.hosts = append(srv.hosts, h)
		}
	}

	// Not set up yet is a normal state on a fresh box: the page still shows
	// this box's npub, which is what the owner needs to add it to the list.
	owner, err := parsePubkey(os.Getenv("SETTINGS_OWNER"))
	if err != nil {
		srv.setupError = "SETTINGS_OWNER is not set to the owner's npub"
	}
	s.owner = owner
	for _, r := range strings.Split(os.Getenv("SETTINGS_RELAYS"), ",") {
		if r = strings.TrimSpace(r); strings.HasPrefix(r, "wss://") || strings.HasPrefix(r, "ws://") {
			s.relays = append(s.relays, r)
		}
	}
	if srv.setupError == "" && len(s.relays) == 0 {
		srv.setupError = "SETTINGS_RELAYS lists no ws:// or wss:// relay"
	}
	if err := s.load(); err != nil {
		log.Fatalf("read saved state: %v", err)
	}
	log.Printf("box key %s", npubEncode(pubHex(key)))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if srv.setupError != "" {
		log.Printf("not syncing: %s", srv.setupError)
	} else {
		go func() {
			for {
				_ = s.run(ctx, nil)
				select {
				case <-ctx.Done():
					return
				case <-time.After(interval):
				}
			}
		}()
	}

	hs := &http.Server{Addr: env("LISTEN", ":8083"), Handler: srv.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sh)
	}()
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
