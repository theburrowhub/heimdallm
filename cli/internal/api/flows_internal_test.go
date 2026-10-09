package api

import "testing"

func TestKeyLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"9", "10", true}, {"10", "9", false}, {"10", "a", true}, {"a", "10", false},
		{"010", "10", true}, {"10", "010", false}, {"a", "b", true}, {"b", "b", false},
	} {
		if got := keyLess(c.a, c.b); got != c.want {
			t.Errorf("keyLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
