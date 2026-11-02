package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password hashes are argon2id in the PHC string format
// ($argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>), so the parameters travel
// with the hash and can be raised later without a migration: a login with
// an older hash simply verifies against the parameters it was made with.
const (
	argonMemory  uint32 = 64 * 1024
	argonTime    uint32 = 3
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
	// MinPasswordLen is the floor; length is the one rule, since
	// composition rules push people towards worse passwords.
	MinPasswordLen = 8
)

// HashCost is what a new hash costs: argon2id's memory (KiB) and passes. The
// zero value is the production cost, so only a caller that names another gets
// one.
type HashCost struct {
	Memory, Time uint32
}

// CheapHash is for tests, whose thousands of hashes guard nothing. Verifying
// reads the cost off the hash, so nothing else about them changes.
var CheapHash = HashCost{Memory: 8, Time: 1}

// HashPassword returns the PHC string for a password, at the production cost.
func HashPassword(password string) (string, error) {
	return HashCost{}.hash(password)
}

func (c HashCost) hash(password string) (string, error) {
	if c == (HashCost{}) {
		c = HashCost{Memory: argonMemory, Time: argonTime}
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	key := argon2.IDKey([]byte(password), salt, c.Time, c.Memory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		c.Memory, c.Time, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a PHC string in constant time.
func VerifyPassword(encoded, password string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return ErrUnauthorized
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return ErrUnauthorized
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return ErrUnauthorized
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return ErrUnauthorized
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrUnauthorized
	}
	return nil
}

// PasswordVerifier is the password method: the proof is the password, the
// stored secret its hash.
type PasswordVerifier struct{}

func (PasswordVerifier) Kind() string { return KindPassword }

func (PasswordVerifier) Verify(m Method, proof string) error {
	return VerifyPassword(string(m.Secret), proof)
}

// checkPassword applies the one rule a new password must meet.
func checkPassword(pw string) error {
	if len(pw) < MinPasswordLen {
		return invalidf("password must be at least %d characters", MinPasswordLen)
	}
	return nil
}
