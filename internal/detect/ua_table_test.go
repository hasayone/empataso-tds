package detect

import "testing"

// uaCase is a full User-Agent with the label the built-in set must give it.
type uaCase struct {
	name string
	ua   string
	want string
}

// humanUAs are real visitor User-Agents: browsers, in-app browsers and TVs. None may be a bot.
var humanUAs = []uaCase{
	{"iOS Safari", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1", BotNone},
	{"iPad Safari", "Mozilla/5.0 (iPad; CPU OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1", BotNone},
	{"Android Chrome", "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36", BotNone},
	{"Android WebView", "Mozilla/5.0 (Linux; Android 12; SM-A525F Build/SP1A.210812.016; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/119.0.6045.163 Mobile Safari/537.36", BotNone},
	{"Facebook iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 [FBAN/FBIOS;FBAV/420.0.0.30.113;FBBV/512345678;FBDV/iPhone14,5;FBMD/iPhone;FBSN/iOS;FBSV/16.5;FBSS/3;FBID/phone;FBLC/en_US;FBOP/5]", BotNone},
	{"Facebook Android", "Mozilla/5.0 (Linux; Android 13; SM-G991B Build/TP1A.220624.014; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/119.0.6045.163 Mobile Safari/537.36 [FB_IAB/FB4A;FBAV/430.0.0.30.115;]", BotNone},
	{"Instagram Android", "Instagram 295.0.0.32.109 Android (33/13; 420dpi; 1080x2220; samsung; SM-G991B; o1s; exynos2100; en_US; 486631846)", BotNone},
	{"Instagram iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Instagram 300.0.0.29.110 (iPhone14,5; iOS 16_5; en_US; en-US; scale=3.00; 1170x2532; 493694670)", BotNone},
	{"TikTok Android", "Mozilla/5.0 (Linux; Android 12; SM-A525F Build/SP1A.210812.016; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/114.0.5735.131 Mobile Safari/537.36 musical_ly_2023207030 JsSdk/1.0 NetType/WIFI Channel/googleplay AppName/musical_ly app_version/27.3.0 ByteLocale/en ByteFullLocale/en Region/US BytedanceWebview/d8a21c6", BotNone},
	{"TikTok iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 musical_ly_29.1.0 JsSdk/2.0 NetType/WIFI Channel/App Store ByteLocale/en Region/US RevealType/Dialog isDarkMode/0 WKWebView/1 BytedanceWebview/d8a21c6", BotNone},
	{"Telegram Android", "Mozilla/5.0 (Linux; Android 13; SM-A515F Build/TP1A.220624.014; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/118.0.0.0 Mobile Safari/537.36 Telegram-Android/10.2.9 (Samsung SM-A515F; Android 13; SDK 33; HIGH)", BotNone},
	{"Telegram iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Safari/604.1", BotNone},
	{"Bing app iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.5 Mobile/15E148 Safari/604.1 BingSapphire/1.0.410503002", BotNone},
	{"Bing app Android", "Mozilla/5.0 (Linux; Android 13; SM-S908B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Mobile Safari/537.36 BingWeb/1.0 EdgA/119.0.0.0", BotNone},
	{"Baidu app iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 baiduboxapp/12.1.0.10 (Baidu; P2 16.5)", BotNone},
	{"Baidu app Android", "Mozilla/5.0 (Linux; Android 11; M2007J3SC Build/RKQ1.200826.002; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/99.0.4844.88 Mobile Safari/537.36 T7/13.32 baiduboxapp/13.32.0.10 (Baidu; P1 11)", BotNone},
	{"Baidu Browser", "Mozilla/5.0 (Linux; Android 8.1.0; Nexus 5X Build/OPM4.171019.021.D1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/66.0.3359.126 Mobile Safari/537.36 baidubrowser/7.19.13.0 (Baidu; P1 8.1.0)", BotNone},
	{"Google app iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) GSA/306.0.600115987 Mobile/15E148 Safari/604.1", BotNone},
	{"Yandex Browser", "Mozilla/5.0 (Linux; arm_64; Android 12; SM-G998B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.5993.80 YaBrowser/23.11.0.123 Mobile Safari/537.36", BotNone},
	{"Samsung Internet", "Mozilla/5.0 (Linux; Android 13; SAMSUNG SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/23.0 Chrome/115.0.0.0 Mobile Safari/537.36", BotNone},
	{"Opera Mini", "Opera/9.80 (Android; Opera Mini/36.2.2254/119.132; U; en) Presto/2.12.423 Version/12.16", BotNone},
	{"UC Browser", "Mozilla/5.0 (Linux; U; Android 10; en-US; RMX2001 Build/QP1A.190711.020) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/78.0.3904.108 UCBrowser/13.4.0.1306 Mobile Safari/537.36", BotNone},
	{"Cubot phone", "Mozilla/5.0 (Linux; Android 11; CUBOT_X30 Build/RP1A.200720.011) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/108.0.5359.128 Mobile Safari/537.36", BotNone},
	{"Cubot Kingkong", "Mozilla/5.0 (Linux; Android 9; CUBOT KINGKONG MINI) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/74.0.3729.136 Mobile Safari/537.36", BotNone},
	{"MIUI Browser", "Mozilla/5.0 (Linux; U; Android 11; en-us; Redmi Note 9 Build/RP1A.200720.011) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/89.0.4389.116 Mobile Safari/537.36 XiaoMi/MiuiBrowser/14.4.0-g", BotNone},
	{"Edge", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0", BotNone},
	{"Firefox desktop", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0", BotNone},
	{"Firefox Android", "Mozilla/5.0 (Android 13; Mobile; rv:121.0) Gecko/121.0 Firefox/121.0", BotNone},
	{"Chrome Windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36", BotNone},
	{"Sogou Mobile Browser", "Mozilla/5.0 (Linux; U; Android 9; zh-cn; MI 8 Build/PKQ1.180729.001) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/57.0.2987.132 MQQBrowser/7.5 Mobile Safari/537.36 SogouMobileBrowser/5.22.8", BotNone},
	{"Samsung Tizen TV", "Mozilla/5.0 (SMART-TV; LINUX; Tizen 6.0) AppleWebKit/537.36 (KHTML, like Gecko) 76.0.3809.146/6.0 TV Safari/537.36", BotNone},
	{"LG webOS TV", "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.79 Safari/537.36 WebAppManager", BotNone},
	{"Yahoo Japan app Android", "Mozilla/5.0 (Linux; Android 13; SO-51C Build/64.2.C.2.140; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/119.0.6045.163 Mobile Safari/537.36 YJApp-ANDROID jp.co.yahoo.android.yjtop/3.80.0", BotNone},
	{"Yahoo Japan app iOS", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 YJApp-IOS jp.co.yahoo.ipn.appli/4.5.1", BotNone},
	{"Bingo game WebView", "Mozilla/5.0 (Linux; Android 11; SM-A125F Build/RP1A.200720.012; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/119.0.6045.163 Mobile Safari/537.36 BingoBlitz/3.2.1", BotNone},
	{"okhttp inside a browser UA", "Mozilla/5.0 (Linux; Android 11; SM-A515F) AppleWebKit/537.36 okhttp/4.9.3", BotNone},
}

// botUAs are real crawler, tool and scanner User-Agents with the label the built-in set gives them.
var botUAs = []uaCase{
	{"Googlebot", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", BotGoogle},
	{"Googlebot smartphone", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", BotGoogle},
	{"AdsBot-Google", "AdsBot-Google (+http://www.google.com/adsbot.html)", BotGoogle},
	{"Google-Read-Aloud", "Mozilla/5.0 (Linux; Android 7.0; SM-G930V Build/NRD90M) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/59.0.3071.125 Mobile Safari/537.36 (compatible; Google-Read-Aloud; +https://support.google.com/webmasters/answer/1061943)", BotGoogle},
	{"Mediapartners-Google", "Mediapartners-Google", BotGoogle},
	{"bingbot", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)", BotBing},
	{"okhttp without slash is not a client", "okhttp", BotNone},
	{"BingPreview", "Mozilla/5.0 (Windows NT 6.1; WOW64) AppleWebKit/534+ (KHTML, like Gecko) BingPreview/1.0b", BotBing},
	{"msnbot", "msnbot/2.0b (+http://search.msn.com/msnbot.htm)", BotBing},
	{"YandexBot", "Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)", BotYandex},
	{"YandexMobileBot", "Mozilla/5.0 (iPhone; CPU iPhone OS 8_1 like Mac OS X) AppleWebKit/600.1.4 (KHTML, like Gecko) Version/8.0 Mobile/12B411 Safari/600.1.4 (compatible; YandexMobileBot/3.0; +http://yandex.com/bots)", BotYandex},
	{"Baiduspider", "Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)", BotBaidu},
	{"Yahoo Slurp", "Mozilla/5.0 (compatible; Yahoo! Slurp; http://help.yahoo.com/help/us/ysearch/slurp)", BotYahoo},
	{"Mail.ru bot", "Mozilla/5.0 (compatible; Mail.RU_Bot/2.0; +http://go.mail.ru/help/robots)", BotMail},
	{"Applebot", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.1.1 Safari/605.1.15 (Applebot/0.1; +http://www.apple.com/go/applebot)", BotApple},
	{"DuckDuckBot", "DuckDuckBot/1.1; (+http://duckduckgo.com/duckduckbot.html)", BotCrawler},
	{"HeadlessChrome mobile", "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/120.0.0.0 Mobile Safari/537.36", BotHeadless},
	{"PhantomJS", "Mozilla/5.0 (Unknown; Linux x86_64) AppleWebKit/538.1 (KHTML, like Gecko) PhantomJS/2.1.1 Safari/538.1", BotHeadless},
	{"Chrome-Lighthouse", "Mozilla/5.0 (Linux; Android 11; moto g power (2022)) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/109.0.5414.83 Mobile Safari/537.36 Chrome-Lighthouse", BotHeadless},
	{"curl", "curl/7.68.0", BotHTTPClient},
	{"wget", "Wget/1.21.2", BotHTTPClient},
	{"python-requests", "python-requests/2.28.1", BotHTTPClient},
	{"aiohttp", "Python/3.10 aiohttp/3.8.4", BotHTTPClient},
	{"Go-http-client", "Go-http-client/1.1", BotHTTPClient},
	{"okhttp bare", "okhttp/4.9.3", BotHTTPClient},
	{"Java bare", "Java/1.8.0_191", BotHTTPClient},
	{"libwww-perl", "libwww-perl/6.15", BotHTTPClient},
	{"Scrapy", "Scrapy/2.8.0 (+https://scrapy.org)", BotHTTPClient},
	{"node-fetch", "node-fetch/1.0 (+https://github.com/bitinn/node-fetch)", BotHTTPClient},
	{"axios", "axios/1.4.0", BotHTTPClient},
	{"curl behind Mozilla", "Mozilla/5.0 (compatible) curl/8.0.1", BotHTTPClient},
	{"sqlmap", "sqlmap/1.7.2#stable (https://sqlmap.org)", BotScanner},
	{"Nmap", "Mozilla/5.0 (compatible; Nmap Scripting Engine; https://nmap.org/book/nse.html)", BotScanner},
	{"zgrab", "Mozilla/5.0 zgrab/0.x", BotScanner},
	{"Nikto", "Mozilla/5.00 (Nikto/2.1.6) (Evasions:None) (Test:Port Check)", BotScanner},
	{"CensysInspect", "Mozilla/5.0 (compatible; CensysInspect/1.1; +https://about.censys.io/)", BotScanner},
	{"AhrefsBot", "Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)", BotCrawler},
	{"SemrushBot", "Mozilla/5.0 (compatible; SemrushBot/7~bl; +http://www.semrush.com/bot.html)", BotCrawler},
	{"MJ12bot", "Mozilla/5.0 (compatible; MJ12bot/v1.4.8; http://mj12bot.com/)", BotCrawler},
	{"DotBot", "Mozilla/5.0 (compatible; DotBot/1.2; +https://opensiteexplorer.org/dotbot; help@moz.com)", BotCrawler},
	{"PetalBot", "Mozilla/5.0 (Linux; Android 7.0;) AppleWebKit/537.36 (KHTML, like Gecko) Mobile Safari/537.36 (compatible; PetalBot;+https://webmaster.petalsearch.com/site/petalbot)", BotCrawler},
	{"Bytespider", "Mozilla/5.0 (Linux; Android 5.0) AppleWebKit/537.36 (KHTML, like Gecko) Mobile Safari/537.36 (compatible; Bytespider; spider-feedback@bytedance.com)", BotCrawler},
	{"GPTBot", "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.0; +https://openai.com/gptbot", BotCrawler},
	{"ClaudeBot", "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)", BotCrawler},
	{"CCBot", "CCBot/2.0 (https://commoncrawl.org/faq/)", BotCrawler},
	{"Amazonbot", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_10_5) AppleWebKit/600.2.5 (KHTML, like Gecko) Version/8.0.2 Safari/600.2.5 (Amazonbot/0.1; +https://developer.amazon.com/support/amazonbot)", BotCrawler},
	{"facebookexternalhit", "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)", BotCrawler},
	{"Sogou web spider", "Sogou web spider/4.0(+http://www.sogou.com/docs/help/webmasters.htm#07)", BotCrawler},
}

// TestUATable checks every real User-Agent against the built-in matcher.
func TestUATable(t *testing.T) {
	if len(humanUAs) < 22 || len(botUAs) < 33 || len(humanUAs)+len(botUAs) < 55 {
		t.Fatalf("table too small: %d human, %d bot", len(humanUAs), len(botUAs))
	}
	for _, c := range append(append([]uaCase{}, humanUAs...), botUAs...) {
		if got := BotByUA(c.ua, nil); got != c.want {
			t.Errorf("%s: BotByUA(%q) = %q; want %q", c.name, c.ua, got, c.want)
		}
	}
}

// TestUATableLowerCase checks that the verdict does not depend on the case of the UA.
func TestUATableLowerCase(t *testing.T) {
	m := DefaultUAMatcher()
	for _, c := range append(append([]uaCase{}, humanUAs...), botUAs...) {
		if got := m.Match(upperASCII(c.ua)); got != c.want {
			t.Errorf("%s: upper-case UA = %q; want %q", c.name, got, c.want)
		}
	}
}

// upperASCII folds ASCII letters of s to capitals.
func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}
