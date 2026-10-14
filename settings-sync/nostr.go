package main

// The few Nostr pieces this service needs: an event, its id, BIP-340
// signature checks, and npub encoding. Signing is here only so the tests can
// play Logen; the box itself never signs anything.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// The shared list is one addressable event: kind 30078 (application data,
// NIP-78) under this d tag. A relay keeps only the newest one per author.
const (
	settingsKind = 30078
	settingsD    = "sovereign-dns/settings"
)

type event struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// serialize is the NIP-01 form the id is the SHA-256 of. encoding/json escapes
// <, > and & by default, which NIP-01 does not, so the encoder turns that off.
func (e *event) serialize() []byte {
	tags := e.Tags
	if tags == nil {
		tags = [][]string{}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode([]any{0, e.PubKey, e.CreatedAt, e.Kind, tags, e.Content})
	return []byte(strings.TrimSuffix(b.String(), "\n"))
}

func (e *event) computeID() string {
	h := sha256.Sum256(e.serialize())
	return hex.EncodeToString(h[:])
}

func (e *event) tag(name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

// verify checks that the id matches the content and the signature is the
// author's. It says nothing about who the author is; callers check that.
func (e *event) verify() error {
	if e.computeID() != e.ID {
		return errors.New("event id does not match its content")
	}
	pk, err := hex.DecodeString(e.PubKey)
	if err != nil || len(pk) != 32 {
		return errors.New("bad pubkey")
	}
	pub, err := schnorr.ParsePubKey(pk)
	if err != nil {
		return fmt.Errorf("bad pubkey: %w", err)
	}
	sb, err := hex.DecodeString(e.Sig)
	if err != nil {
		return errors.New("bad signature encoding")
	}
	sig, err := schnorr.ParseSignature(sb)
	if err != nil {
		return fmt.Errorf("bad signature: %w", err)
	}
	id, _ := hex.DecodeString(e.ID)
	if !sig.Verify(id, pub) {
		return errors.New("signature does not verify")
	}
	return nil
}

func (e *event) sign(sk *btcec.PrivateKey) error {
	e.PubKey = hex.EncodeToString(schnorr.SerializePubKey(sk.PubKey()))
	e.ID = e.computeID()
	id, _ := hex.DecodeString(e.ID)
	sig, err := schnorr.Sign(sk, id)
	if err != nil {
		return err
	}
	e.Sig = hex.EncodeToString(sig.Serialize())
	return nil
}

// newer: NIP-01's rule for replaceable events. Later created_at wins; on a
// tie, the lowest id.
func newer(a, b *event) bool {
	if b == nil {
		return true
	}
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID < b.ID
}

func pubHex(sk *btcec.PrivateKey) string {
	return hex.EncodeToString(schnorr.SerializePubKey(sk.PubKey()))
}

// --- bech32 (BIP-173), just enough for npub ---

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

func bech32Polymod(v []byte) uint32 {
	gen := []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, b := range v {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(b)
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32HRPExpand(hrp string) []byte {
	out := make([]byte, 0, len(hrp)*2+1)
	for _, c := range hrp {
		out = append(out, byte(c>>5))
	}
	out = append(out, 0)
	for _, c := range hrp {
		out = append(out, byte(c&31))
	}
	return out
}

func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	acc, bits := uint(0), uint(0)
	maxv := uint(1)<<to - 1
	var out []byte
	for _, b := range data {
		if uint(b)>>from != 0 {
			return nil, errors.New("invalid data")
		}
		acc = acc<<from | uint(b)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, errors.New("invalid padding")
	}
	return out, nil
}

func npubEncode(hexKey string) string {
	raw, _ := hex.DecodeString(hexKey)
	data, _ := convertBits(raw, 8, 5, true)
	v := append(bech32HRPExpand("npub"), data...)
	pm := bech32Polymod(append(v, 0, 0, 0, 0, 0, 0)) ^ 1
	var b strings.Builder
	b.WriteString("npub1")
	for _, d := range data {
		b.WriteByte(bech32Charset[d])
	}
	for i := 0; i < 6; i++ {
		b.WriteByte(bech32Charset[(pm>>(5*(5-i)))&31])
	}
	return b.String()
}

// parsePubkey accepts an npub or 64 hex characters and returns lowercase hex.
func parsePubkey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) == 64 {
		if _, err := hex.DecodeString(s); err == nil {
			return s, nil
		}
	}
	if !strings.HasPrefix(s, "npub1") {
		return "", errors.New("not an npub or hex pubkey")
	}
	var data []byte
	for _, c := range s[5:] {
		i := strings.IndexRune(bech32Charset, c)
		if i < 0 {
			return "", errors.New("bad npub character")
		}
		data = append(data, byte(i))
	}
	if len(data) < 6 || bech32Polymod(append(bech32HRPExpand("npub"), data...)) != 1 {
		return "", errors.New("bad npub checksum")
	}
	raw, err := convertBits(data[:len(data)-6], 5, 8, false)
	if err != nil || len(raw) != 32 {
		return "", errors.New("bad npub length")
	}
	return hex.EncodeToString(raw), nil
}
