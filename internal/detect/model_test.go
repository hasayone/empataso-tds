package detect

import (
	"strings"
	"testing"
)

func TestModelOf(t *testing.T) {
	cases := []struct {
		name, ua, want string
	}{
		{"samsung with build", "Mozilla/5.0 (Linux; Android 13; SM-S918B Build/TP1A.220624.014) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36", "SM-S918B"},
		{"samsung without build", "Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36", "SM-G991B"},
		{"pixel with space", "Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 Chrome/121.0 Mobile Safari/537.36", "Pixel 8 Pro"},
		{"redmi", "Mozilla/5.0 (Linux; Android 12; Redmi Note 10) AppleWebKit/537.36 Chrome/119.0 Mobile Safari/537.36", "Redmi Note 10"},
		{"reduced chrome K", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36", ""},
		{"old android with locale", "Mozilla/5.0 (Linux; U; Android 4.0.3; ru-ru; GT-N7100 Build/IML74K) AppleWebKit/534.30 Mobile Safari/534.30", "GT-N7100"},
		{"webview marker after model", "Mozilla/5.0 (Linux; Android 11; ONEPLUS A6003 Build/RKQ1; wv) AppleWebKit/537.36 Version/4.0 Chrome/99 Mobile Safari/537.36", "ONEPLUS A6003"},
		{"android without model", "Mozilla/5.0 (Linux; Android 9) AppleWebKit/537.36 Chrome/99 Mobile Safari/537.36", ""},
		{"iphone", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 Version/16.0 Mobile/15E148 Safari/604.1", ""},
		{"windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0", ""},
		{"empty", "", ""},
		{"no parenthesis", "curl/8.0", ""},
		{"too long is cut", "Mozilla/5.0 (Linux; Android 13; " + strings.Repeat("A", 80) + ") Chrome/1", strings.Repeat("A", 64)},
		{"control characters dropped", "Mozilla/5.0 (Linux; Android 13; SM-\x01S9\x7f18B) Chrome/1", "SM-S918B"},
		{"only control characters", "Mozilla/5.0 (Linux; Android 13; \x01\x02) Chrome/1", ""},
		{"invalid utf8 dropped", "Mozilla/5.0 (Linux; Android 13; Pix\xffel 8) Chrome/1", "Pixel 8"},
	}
	for _, c := range cases {
		if got := modelOf(c.ua); got != c.want {
			t.Errorf("%s: modelOf = %q, want %q", c.name, got, c.want)
		}
		if got := Parse(c.ua).Model; got != c.want {
			t.Errorf("%s: Parse().Model = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHintModel(t *testing.T) {
	cases := map[string]string{
		`"Pixel 8"`:                         "Pixel 8",
		`""`:                                "",
		``:                                  "",
		`"K"`:                               "",
		`  "SM-S918B"  `:                    "SM-S918B",
		"Pixel\x00 8":                       "Pixel 8",
		`"` + strings.Repeat("x", 65) + `"`: strings.Repeat("x", 64),
		strings.Repeat("я", 70):             strings.Repeat("я", 64),
	}
	for in, want := range cases {
		if got := HintModel(in); got != want {
			t.Errorf("HintModel(%q) = %q, want %q", in, got, want)
		}
	}
}
