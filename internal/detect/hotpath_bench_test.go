package detect

import (
	"fmt"
	"net/netip"
	"testing"
)

// benchUAs are typical visitor User-Agents for the parse benchmark.
var benchUAs = []string{
	"Mozilla/5.0 (Linux; Android 13; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Linux; Android 12; Redmi Note 11) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0 Mobile Safari/537.36",
}

// benchSigs returns n mixed-case signatures that never match benchUAs.
func benchSigs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("SomeCrawler%dBot", i)
	}
	return out
}

// benchBotUA is a crawler UA whose signature sits late in the string.
const benchBotUA = "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)"

// BenchmarkBotByUA measures a human UA that matches nothing, the worst case: the
// whole string is scanned against the built-in set plus 200 user signatures.
func BenchmarkBotByUA(b *testing.B) {
	m := buildUAMatcher(benchSigs(200), nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if BotByUA(benchUAs[0], m) != BotNone {
			b.Fatal("unexpected bot")
		}
	}
}

// BenchmarkBotByUA20kSigs measures the same miss against 20000 user signatures.
func BenchmarkBotByUA20kSigs(b *testing.B) {
	m := buildUAMatcher(benchSigs(maxUALines), nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if BotByUA(benchUAs[0], m) != BotNone {
			b.Fatal("unexpected bot")
		}
	}
}

// BenchmarkBotByUABot measures a crawler UA found by the built-in set.
func BenchmarkBotByUABot(b *testing.B) {
	m := DefaultUAMatcher()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if BotByUA(benchBotUA, m) != BotCrawler {
			b.Fatal("bot not found")
		}
	}
}
// benchOrgMiss is an organization name that no built-in pattern matches.
const benchOrgMiss = "Some Residential ISP LLC"

// BenchmarkProxyByOrg measures an organization scan against the built-in list.
func BenchmarkProxyByOrg(b *testing.B) {
	patterns := DefaultProxyOrg()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ProxyByOrg(benchOrgMiss, patterns)
	}
}

// BenchmarkIsProxy measures the verdict for a residential address: two organization
// names are scanned and the lists are consulted once.
func BenchmarkIsProxy(b *testing.B) {
	patterns := DefaultProxyOrg()
	lists := &fakeLists{lists: map[string]map[netip.Addr]bool{}}
	ip := netip.MustParseAddr("203.0.113.7")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		IsProxy(lists, ip, patterns, benchOrgMiss, "Some Residential ISP")
	}
}
// BenchmarkParse measures User-Agent parsing for a small set of repeating visitors.
func BenchmarkParse(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Parse(benchUAs[i%len(benchUAs)])
	}
}
