package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevice(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Firefox/120.0", Computer},
		{"", Computer},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) Mobile/15E148", Phone},
		{"Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit Mobile Safari", Phone},
		{"Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X) AppleWebKit", Tablet},
		{"Mozilla/5.0 (Linux; Android 12; SM-T870) AppleWebKit Safari", Tablet}, // Samsung Tab tablet
		{"Mozilla/5.0 (Linux; Android 13) AppleWebKit Safari", Tablet},          // Android without mobile
		{"Mozilla/5.0 (Windows Phone 10.0; Lumia 950)", Phone},
	}
	for _, c := range cases {
		if got := Device(c.ua); got != c.want {
			t.Errorf("Device(%q) = %q; want %q", c.ua, got, c.want)
		}
	}
}

func TestBotByUA(t *testing.T) {
	m := buildUAMatcher([]string{"AhrefsBot", "SemrushBot"}, nil)
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", BotGoogle},
		{"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)", BotBing},
		{"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)", BotYandex},
		{"Mozilla/5.0 (compatible; Baiduspider/2.0)", BotBaidu},
		{"Mozilla/5.0 (compatible; AhrefsBot/7.0)", BotSignUA},
		{"Mozilla/5.0 (Windows NT 10.0) Firefox/120.0", BotNone},
		{"", BotNone},
	}
	for _, c := range cases {
		if got := BotByUA(c.ua, m); got != c.want {
			t.Errorf("BotByUA(%q) = %q; want %q", c.ua, got, c.want)
		}
	}
}

func TestCrawlerByHost(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"crawl-66-249-66-1.googlebot.com.", BotGoogle},
		{"CRAWL-66-249-66-1.GOOGLEBOT.COM", BotGoogle},
		{"google.evil.com", BotNone},
		{"evilgooglebot.com", BotNone},
		{"rate-limited-proxy-66-249-90-1.google.com", BotGoogle},
		{"abc.googleusercontent.com", BotNone},
		{"msnbot-1.search.msn.com", BotBing},
		{"spider-1.yandex.ru.", BotYandex},
		{"baiduspider-1.crawl.baidu.com", BotBaidu},
		{"baiduspider-1.crawl.baidu.jp", BotBaidu},
		{"h1.crawl.yahoo.net", BotYahoo},
		{"spider-1.go.mail.ru.", BotMail},
		{"go.mail.ru", BotMail},
		{"fetcher1.mail.ru", BotNone},
		{"cloud-1.hb.vkcs.cloud.mail.ru", BotNone},
		{"evilgo.mail.ru", BotNone},
		{"17-58-98-1.applebot.apple.com", BotApple},
		{"apple.com", BotNone},
		{"mail.ru.evil.com", BotNone},
		{"", BotNone},
		{".", BotNone},
	}
	for _, c := range cases {
		if got := CrawlerByHost(c.host); got != c.want {
			t.Errorf("CrawlerByHost(%q) = %q; want %q", c.host, got, c.want)
		}
	}
}

func TestFakeAndVerifiable(t *testing.T) {
	if got := FakeLabel(BotGoogle); got != "fake_google" {
		t.Errorf("FakeLabel = %q", got)
	}
	for _, l := range []string{BotGoogle, BotBing, BotYandex, BotBaidu, BotYahoo, BotMail, BotApple} {
		if !Verifiable(l) {
			t.Errorf("%q must be verifiable", l)
		}
	}
	for _, l := range []string{BotNone, BotCrawler, BotHeadless, BotHTTPClient, BotScanner, BotSignUA, FakeLabel(BotGoogle)} {
		if Verifiable(l) {
			t.Errorf("%q must not be verifiable", l)
		}
	}
}

func TestBotByReferer(t *testing.T) {
	if got := BotByReferer("https://spam-site.example/", []string{"spam-site.example"}); got != BotSignRef {
		t.Errorf("referer sign = %q", got)
	}
	if got := BotByReferer("https://google.com/", []string{"spam-site.example"}); got != BotNone {
		t.Errorf("clean referer = %q", got)
	}
}
func TestInUABlacklist(t *testing.T) {
	list := []string{"BadBot/1.0", "Evil/2.0"}
	if !InUABlacklist("BadBot/1.0", list) {
		t.Error("an exact match should fire")
	}
	if InUABlacklist("BadBot/1.0 extra", list) {
		t.Error("ua_blacklist — exact match only")
	}
}

func TestSignaturesLoadAndReload(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name+".dat")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(fileUA, "# bots\nAhrefsBot\nSemrushBot\n")

	s := NewSignatures(dir, nil)
	for _, ua := range []string{"x AhrefsBot y", "x SEMRUSHBOT y"} {
		if got := BotByUA(ua, s.UAMatcher()); got != BotSignUA {
			t.Errorf("BotByUA(%q) = %q; want sign_ua", ua, got)
		}
	}
}
