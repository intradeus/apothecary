// Package secrets encrypts integration secrets at rest (DESIGN2.md §3):
// NaCl secretbox, ciphertext = nonce(24) || box(JSON object), key from
// FACTORY_MASTER_KEY (base64 std of exactly 32 bytes). The raw values never
// leave the engine: API responses carry masks only.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/nacl/secretbox"
)

const keyLen = 32

type Key [keyLen]byte

// ErrKeyMismatch is returned when a ciphertext does not open under the
// configured master key — never silently return garbage (§3).
var ErrKeyMismatch = errors.New("secrets: decryption failed (master key mismatch?)")

// ParseKey decodes a FACTORY_MASTER_KEY value.
func ParseKey(s string) (Key, error) {
	var k Key
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return k, fmt.Errorf("secrets: master key is not valid base64: %w", err)
	}
	if len(raw) != keyLen {
		return k, fmt.Errorf("secrets: master key must decode to %d bytes, got %d", keyLen, len(raw))
	}
	copy(k[:], raw)
	return k, nil
}

// GenerateKey returns a fresh base64 master key (factoryd keygen).
func GenerateKey() string {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return base64.StdEncoding.EncodeToString(k[:])
}

// Seal encrypts a secret field map. An empty (or nil) map seals to nil —
// "no secrets" is stored as an empty bytea (§3).
func Seal(key Key, values map[string]string) ([]byte, error) {
	if len(values) == 0 {
		return nil, nil
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	boxed := secretbox.Seal(nonce[:], plain, &nonce, (*[keyLen]byte)(&key))
	return boxed, nil
}

// Open decrypts a sealed secret map; nil/empty ciphertext yields an empty map.
func Open(key Key, ciphertext []byte) (map[string]string, error) {
	if len(ciphertext) == 0 {
		return map[string]string{}, nil
	}
	if len(ciphertext) < 24 {
		return nil, ErrKeyMismatch
	}
	var nonce [24]byte
	copy(nonce[:], ciphertext[:24])
	plain, ok := secretbox.Open(nil, ciphertext[24:], &nonce, (*[keyLen]byte)(&key))
	if !ok {
		return nil, ErrKeyMismatch
	}
	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("secrets: sealed payload is not a JSON object: %w", err)
	}
	return out, nil
}

// Mask renders a secret for API responses (§3): "••••" + last 4 chars for
// values longer than 8 chars, plain "••••" otherwise. Never the value.
func Mask(v string) string {
	if len(v) > 8 {
		return "••••" + v[len(v)-4:]
	}
	return "••••"
}

// MaskAll masks every field of a secret map (nil-safe, never nil result).
func MaskAll(values map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range values {
		out[k] = Mask(v)
	}
	return out
}
