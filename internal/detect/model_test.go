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

func TestTradeNames(t *testing.T) {
	ua := func(model string) string {
		return "Mozilla/5.0 (Linux; Android 14; " + model + ") AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36"
	}
	a05 := "Samsung Galaxy A05"
	note := "Xiaomi Redmi Note 12"
	cases := []struct{ name, in, want string }{
		{"a05 code", ua("SM-A055F Build/UP1A.231005.007"), a05},
		{"a05 words", ua("Galaxy A05"), a05},
		{"a05 samsung internet", ua("SAMSUNG SM-A055F"), a05},
		{"a05 latin america", ua("SM-A055M"), a05},
		{"a05 hint", `"SM-A055F"`, a05},
		{"a13", ua("SM-A135F"), "Samsung Galaxy A13"},
		{"a13 revision", ua("SM-A137F"), "Samsung Galaxy A13"},
		{"a14", ua("SM-A145R"), "Samsung Galaxy A14"},
		{"a14 5g stays a code", ua("SM-A146B"), "SM-A146B"},
		{"a16", ua("SM-A165F"), "Samsung Galaxy A16"},
		{"a16 words", ua("Galaxy A16"), "Samsung Galaxy A16"},
		{"a16 5g", ua("SM-A166U"), "Samsung Galaxy A16 5G"},
		{"a16 5g words", ua("Galaxy A16 5G"), "Samsung Galaxy A16 5G"},
		{"a075 is not a05", ua("SM-A075F"), "Samsung SM-A075F"},
		{"a175 stays the code", ua("SM-A175F"), "Samsung SM-A175F"},
		{"redmi 14c code", ua("2409BRN2CY"), "Xiaomi Redmi 14C"},
		{"redmi 14c words", ua("Redmi 14C"), "Xiaomi Redmi 14C"},
		{"note 12 code", ua("23021RAAEG"), note},
		{"note 12 words", ua("Redmi Note 12"), note},
		{"note 12 4g words", ua("Redmi Note 12 4G"), note},
		{"note 12 5g is another phone", ua("Redmi Note 12 5G"), "Redmi Note 12 5G"},
		{"note 12 5g code", ua("22111317G"), "22111317G"},
		{"honor x8c code", ua("ABR-LX1"), "Huawei Honor X8c"},
		{"honor x8c words", ua("Honor X8c"), "Huawei Honor X8c"},
		{"honor x8b", ua("LLY-LX1"), "Huawei Honor X8b"},
		{"honor 400", ua("DNY-NX9"), "Huawei Honor 400"},
		{"huawei only", ua("HUAWEI"), "Huawei"},
		{"revvl", ua("REVVL V+ 5G Build/SP1A.210812.016"), "T-Mobile REVVL V+ 5G"},
		{"infinix code", ua("Infinix X6880"), "Infinix Hot 50 Pro+"},
		{"infinix words", ua("Infinix Hot 50 Pro+"), "Infinix Hot 50 Pro+"},
		{"camon code", ua("TECNO CG7"), "Tecno Mobile Camon 17P"},
		{"camon n", ua("TECNO CG7n"), "Tecno Mobile Camon 17P"},
		{"camon words", ua("TECNO Camon 17P"), "Tecno Mobile Camon 17P"},
		{"realme code", ua("RMX3842"), "Realme 12 Pro 5G"},
		{"realme plus stays", ua("RMX3840"), "RMX3840"},
		{"sharp code", ua("SHV48"), "Sharp Aquos S3 Basic"},
		{"sharp words", ua("AQUOS sense3 basic"), "Sharp Aquos S3 Basic"},
		{"instagram app tuple", "Instagram 295.0.0.32.109 Android (34/14; 480dpi; 720x1600; samsung; SM-A055F; a05; mt6768; en_US; 1)", a05},
		{"instagram unknown code", "Instagram 295.0.0.32.109 Android (33/13; 420dpi; 1080x2220; samsung; SM-G991B; o1s; exynos2100; en_US; 486631846)", "SM-G991B"},
		{"empty", "", ""},
		{"reduced chrome", ua("K"), ""},
	}
	for _, c := range cases {
		got := Parse(c.in).Model
		if strings.HasPrefix(c.in, `"`) {
			got = HintModel(c.in)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if Parse(ua("SM-A075F")).Model == a05 {
		t.Error("SM-A075F must not be reported as the Galaxy A05")
	}
	if Parse(ua("SM-A165F")).Model == Parse(ua("SM-A166B")).Model {
		t.Error("Galaxy A16 and Galaxy A16 5G must stay different")
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
