package config

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLimitBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 5, ""},
		{"zero limit", "abc", 0, ""},
		{"short", "abc", 5, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"ascii cut", "abcdefgh", 5, "abcde"},
		{"rune ends at limit", "ab\u00e9", 4, "ab\u00e9"},
		{"cut inside two-byte rune", "abc\u00e9", 4, "abc"},
		{"cut inside three-byte rune", "a\u20ac\u20ac", 3, "a"},
		{"cut inside four-byte rune", "\U0001F600\U0001F600", 6, "\U0001F600"},
		{"only a rune that does not fit", "\u20ac", 2, ""},
	}
	for _, tc := range cases {
		got := LimitBytes(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("%s: LimitBytes(%q, %d) = %q, want %q", tc.name, tc.in, tc.n, got, tc.want)
		}
		if !utf8.ValidString(got) && utf8.ValidString(tc.in) {
			t.Errorf("%s: result %q is not valid UTF-8", tc.name, got)
		}
	}
}

func TestApplySourceLimitsVisitorInput(t *testing.T) {
	long := strings.Repeat("a", MaxValueBytes+40)
	wide := strings.Repeat("\u00e9", MaxValueBytes) // two bytes each, 510 bytes
	src := &TrafficSource{Parameters: map[string]string{"keyword": "kw", "sub_id_2": "s2"}}
	q := url.Values{"kw": {wide}, "sub_id_1": {long}, "s2": {wide}, "sub_id_15": {"ok"}}
	keyword, subs := ApplySource(q, src)
	if len(keyword) > MaxValueBytes || !utf8.ValidString(keyword) || keyword == "" {
		t.Errorf("keyword %d bytes valid=%v", len(keyword), utf8.ValidString(keyword))
	}
	if len(subs[0]) != MaxValueBytes {
		t.Errorf("sub_id_1 is %d bytes, want %d", len(subs[0]), MaxValueBytes)
	}
	if len(subs[1]) > MaxValueBytes || !utf8.ValidString(subs[1]) || len(subs[1]) != MaxValueBytes-1 {
		t.Errorf("sub_id_2 %d bytes valid=%v, want a cut that keeps whole runes", len(subs[1]), utf8.ValidString(subs[1]))
	}
	if subs[14] != "ok" {
		t.Errorf("a short sub id changed: %q", subs[14])
	}
}

// A Sub ID rule value and the sub id it is compared with share one limit.
func TestSubIDLimitIsShared(t *testing.T) {
	if maxSubIDValueLen != MaxValueBytes {
		t.Fatalf("rule limit %d, visitor limit %d", maxSubIDValueLen, MaxValueBytes)
	}
}
