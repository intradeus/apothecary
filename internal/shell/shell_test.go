package shell

import "testing"

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":         `''`,
		"a b":      `'a b'`,
		"$HOME`x`": "'$HOME`x`'",
		"it's":     `'it'\''s'`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestTail(t *testing.T) {
	if got := Tail("abcdef", 3); got != "def" {
		t.Errorf("Tail = %q", got)
	}
	if got := Tail("ab", 3); got != "ab" {
		t.Errorf("short Tail = %q", got)
	}
}
