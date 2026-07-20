package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestSealOpenRoundtrip(t *testing.T) {
	key, err := ParseKey(GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{"token": "glpat-abc123secret", "api_key": "sk-xyz"}
	sealed, err := Seal(key, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "glpat") || strings.Contains(string(sealed), "sk-xyz") {
		t.Fatal("plaintext visible in ciphertext")
	}
	out, err := Open(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if out["token"] != in["token"] || out["api_key"] != in["api_key"] {
		t.Errorf("roundtrip = %v", out)
	}
}

func TestWrongKeyFailsLoudly(t *testing.T) {
	k1, _ := ParseKey(GenerateKey())
	k2, _ := ParseKey(GenerateKey())
	sealed, err := Seal(k1, map[string]string{"token": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(k2, sealed); !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("err = %v, want ErrKeyMismatch (never silent garbage)", err)
	}
	if _, err := Open(k2, []byte("short")); !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("truncated ciphertext err = %v", err)
	}
}

func TestEmptySeals(t *testing.T) {
	key, _ := ParseKey(GenerateKey())
	sealed, err := Seal(key, nil)
	if err != nil || sealed != nil {
		t.Fatalf("empty map should seal to nil, got %v, %v", sealed, err)
	}
	out, err := Open(key, nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("nil ciphertext should open to empty map, got %v, %v", out, err)
	}
}

func TestParseKeyRejectsBadInput(t *testing.T) {
	if _, err := ParseKey("not base64!!"); err == nil {
		t.Error("junk accepted")
	}
	if _, err := ParseKey("c2hvcnQ="); err == nil { // "short"
		t.Error("wrong length accepted")
	}
}

func TestMask(t *testing.T) {
	if got := Mask("glpat-verylongtoken1234"); got != "••••1234" {
		t.Errorf("long mask = %q", got)
	}
	if got := Mask("short"); got != "••••" {
		t.Errorf("short mask = %q", got)
	}
	all := MaskAll(map[string]string{"a": "longenoughvalue"})
	if all["a"] != "••••alue" {
		t.Errorf("MaskAll = %v", all)
	}
}
