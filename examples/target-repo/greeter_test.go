package greeter

import "testing"

func TestGreeting(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"named", "Lu", "Hello, Lu!"},
		{"anonymous", "", "Hello there!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Greeting(tc.in); got != tc.want {
				t.Errorf("Greeting(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
