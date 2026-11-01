package envport

import "testing"

func TestProjectHostKey(t *testing.T) {
	for in, want := range map[string]string{
		"01JABCDEFGHJKMNPQRSTVWXYZ0": "01jabcdefghjkmnpqrstvwxyz0",
		"":                           "default",
		"---":                        "default",
		"é":                          "default",
		"-a b-":                      "a-b",
		"a/b_c":                      "a-b-c",
	} {
		if got := ProjectHostKey(in); got != want {
			t.Errorf("ProjectHostKey(%q) = %q, want %q", in, got, want)
		}
	}
}
