package main

// The sealed envelope inside the event's content.
//
// One random 32-byte content key K encrypts the list (AES-256-GCM). K is then
// wrapped once per reader with NIP-44 v2, from Logen's key to the reader's: to
// Logen himself, and to every box the list names. A box unwraps its copy with
// its own key and Logen's pubkey. The page in a browser does the same through
// Logen's signer (NIP-07 nip44.encrypt/decrypt), so his key never reaches a box.
//
// Readers are not listed by pubkey: each wrap is filed under
// sha256(prefix | salt | pubkey), with a fresh salt per save, so the public
// event shows how many readers there are but not which boxes they are.
//
// The envelope needs no authentication of its own: the event signature covers
// the whole content, and only an event signed by Logen is ever opened.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
)

type envelope struct {
	V     int               `json:"v"`
	Salt  string            `json:"salt"`  // base64, 16 bytes
	Keys  map[string]string `json:"keys"`  // reader id -> NIP-44 payload of hex(K)
	Nonce string            `json:"nonce"` // base64, 12 bytes
	Data  string            `json:"data"`  // base64 AES-256-GCM of the settings JSON
}

func readerID(salt, pubkeyHex string) string {
	h := sha256.Sum256([]byte("sovereign-dns/settings|" + salt + "|" + pubkeyHex))
	return hex.EncodeToString(h[:16])
}

var errNotForThisBox = errors.New("the newest list is not sealed to this box")

// open decrypts a list sealed by owner (hex) for the holder of sk.
func open(ev *event, sk *btcec.PrivateKey, ownerHex string) (*settings, error) {
	var env envelope
	if err := json.Unmarshal([]byte(ev.Content), &env); err != nil {
		return nil, fmt.Errorf("content is not a sealed list: %w", err)
	}
	if env.V != 1 {
		return nil, fmt.Errorf("unknown envelope version %d", env.V)
	}
	wrapped, ok := env.Keys[readerID(env.Salt, pubHex(sk))]
	if !ok {
		return nil, errNotForThisBox
	}
	ck, err := nip44ConversationKey(sk, ownerHex)
	if err != nil {
		return nil, err
	}
	khex, err := nip44Decrypt(wrapped, ck)
	if err != nil {
		return nil, fmt.Errorf("unwrap content key: %w", err)
	}
	k, err := hex.DecodeString(khex)
	if err != nil || len(k) != 32 {
		return nil, errors.New("content key is not 32 bytes of hex")
	}
	nonce, err1 := base64.StdEncoding.DecodeString(env.Nonce)
	data, err2 := base64.StdEncoding.DecodeString(env.Data)
	if err1 != nil || err2 != nil || len(nonce) != 12 {
		return nil, errors.New("bad nonce or data encoding")
	}
	block, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, errors.New("list does not decrypt")
	}
	var s settings
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, fmt.Errorf("list is not valid JSON: %w", err)
	}
	return &s, nil
}

// seal is what the page does in the browser, in Go for the tests: Logen's
// key wraps K for himself and every box the list names.
func seal(s *settings, owner *btcec.PrivateKey) (string, error) {
	plain, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	k := make([]byte, 32)
	saltRaw := make([]byte, 16)
	nonce := make([]byte, 12)
	for _, b := range [][]byte{k, saltRaw, nonce} {
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
	}
	salt := base64.StdEncoding.EncodeToString(saltRaw)
	readers := []string{pubHex(owner)}
	for _, b := range s.Boxes {
		readers = append(readers, b.PubKey)
	}
	env := envelope{V: 1, Salt: salt, Keys: map[string]string{}, Nonce: base64.StdEncoding.EncodeToString(nonce)}
	for _, r := range readers {
		ck, err := nip44ConversationKey(owner, r)
		if err != nil {
			return "", err
		}
		w, err := nip44Encrypt(hex.EncodeToString(k), ck, nil)
		if err != nil {
			return "", err
		}
		env.Keys[readerID(salt, r)] = w
	}
	block, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(block)
	env.Data = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, nil))
	out, err := json.Marshal(env)
	return string(out), err
}
