package base62

import (
	"strings"
	"testing"
)

func TestEncode(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0"}, {61, "Z"}, {62, "10"}, {3843, "ZZ"}, {3844, "100"},
	}

	for _, c := range cases {
		if got := Encode(c.in); got != c.want {
			t.Errorf("Encode(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDecode(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"0", 0}, {"Z", 61}, {"10", 62}, {"ZZ", 3843}, {"100", 3844},
	}

	for _, c := range cases {
		got, err := Decode(c.in)
		if err != nil {
			t.Fatalf("Decode(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Decode(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	for _, n := range []uint64{0, 1, 61, 62, 63, 3843, 3844, 123456789} {
		got, err := Decode(Encode(n))
		if err != nil {
			t.Fatalf("round trip %d: %v", n, err)
		}
		if got != n {
			t.Fatalf("round trip %d = %d", n, got)
		}
	}
}

func TestDecodeInvalid(t *testing.T) {
	cases := []string{
		"",
		"!",
		strings.Repeat("Z", 12), // 超过 uint64 能表示的范围
	}

	for _, s := range cases {
		if _, err := Decode(s); err == nil {
			t.Fatalf("Decode(%q) should fail", s)
		}
	}
}
