package main

// NIP-44 version 2, as the spec defines it. Used for one thing: wrapping the
// list's content key for each reader. Checked against the spec's test vectors
// in nip44_test.go.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"

	"github.com/btcsuite/btcd/btcec/v2"
	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/hkdf"
)

func nip44ConversationKey(sk *btcec.PrivateKey, peerHex string) ([32]byte, error) {
	var key [32]byte
	raw, err := hex.DecodeString(peerHex)
	if err != nil || len(raw) != 32 {
		return key, errors.New("bad peer pubkey")
	}
	pub, err := btcec.ParsePubKey(append([]byte{0x02}, raw...))
	if err != nil {
		return key, err
	}
	shared := btcec.GenerateSharedSecret(sk, pub) // the x coordinate, unhashed
	copy(key[:], hkdf.Extract(sha256.New, shared, []byte("nip44-v2")))
	return key, nil
}

func nip44MessageKeys(ck [32]byte, nonce []byte) (chachaKey, chachaNonce, hmacKey []byte) {
	r := hkdf.Expand(sha256.New, ck[:], nonce)
	k := make([]byte, 76)
	_, _ = io.ReadFull(r, k)
	return k[0:32], k[32:44], k[44:76]
}

func nip44PaddedLen(n int) int {
	if n <= 32 {
		return 32
	}
	next := 1 << bitLen(n-1) // the spec's 1 << (floor(log2(n-1)) + 1)
	chunk := 32
	if next > 256 {
		chunk = next / 8
	}
	return chunk * ((n-1)/chunk + 1)
}

func bitLen(x int) int {
	n := 0
	for x > 0 {
		n++
		x >>= 1
	}
	return n
}

func nip44Encrypt(plaintext string, ck [32]byte, nonce []byte) (string, error) {
	if nonce == nil {
		nonce = make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return "", err
		}
	}
	n := len(plaintext)
	if n < 1 || n > 65535 {
		return "", errors.New("plaintext length out of range")
	}
	ckey, cnonce, hkey := nip44MessageKeys(ck, nonce)
	padded := make([]byte, 2+nip44PaddedLen(n))
	binary.BigEndian.PutUint16(padded, uint16(n))
	copy(padded[2:], plaintext)
	c, err := chacha20.NewUnauthenticatedCipher(ckey, cnonce)
	if err != nil {
		return "", err
	}
	ct := make([]byte, len(padded))
	c.XORKeyStream(ct, padded)
	mac := hmac.New(sha256.New, hkey)
	mac.Write(nonce)
	mac.Write(ct)
	out := append([]byte{2}, nonce...)
	out = append(out, ct...)
	out = mac.Sum(out)
	return base64.StdEncoding.EncodeToString(out), nil
}

func nip44Decrypt(payload string, ck [32]byte) (string, error) {
	if payload == "" || payload[0] == '#' {
		return "", errors.New("unknown encryption version")
	}
	if len(payload) < 132 || len(payload) > 87472 {
		return "", errors.New("invalid payload size")
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", errors.New("invalid base64")
	}
	if len(raw) < 99 || len(raw) > 65603 || raw[0] != 2 {
		return "", errors.New("unknown version or bad length")
	}
	nonce, ct, tag := raw[1:33], raw[33:len(raw)-32], raw[len(raw)-32:]
	ckey, cnonce, hkey := nip44MessageKeys(ck, nonce)
	mac := hmac.New(sha256.New, hkey)
	mac.Write(nonce)
	mac.Write(ct)
	if !hmac.Equal(mac.Sum(nil), tag) {
		return "", errors.New("invalid MAC")
	}
	c, err := chacha20.NewUnauthenticatedCipher(ckey, cnonce)
	if err != nil {
		return "", err
	}
	padded := make([]byte, len(ct))
	c.XORKeyStream(padded, ct)
	n := int(binary.BigEndian.Uint16(padded))
	if n < 1 || n > 65535 || len(padded) != 2+nip44PaddedLen(n) {
		return "", errors.New("invalid padding")
	}
	return string(padded[2 : 2+n]), nil
}
