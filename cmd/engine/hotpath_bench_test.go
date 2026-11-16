package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/detect"
	"github.com/egerkuzma/emtds/internal/geo"
	"github.com/egerkuzma/emtds/internal/ipindex"
	"github.com/egerkuzma/emtds/internal/server"
)

// BenchmarkBotProfile measures reading the installation bot profile on the request path.
func BenchmarkBotProfile(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "bot_protection.json")
	if err := os.WriteFile(path, []byte(`{"ch_ua":true,"ch_empty_ua":true}`), 0o644); err != nil {
		b.Fatal(err)
	}
	d := &engineDeps{botPath: path, lists: ipindex.NewSet(dir, discardLog())}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !d.botProfile().CheckUA {
				b.Fatal("profile not read")
			}
		}
	})
}

// BenchmarkDetectBotSignatures measures UA and referer signature matching for a human visitor.
func BenchmarkDetectBotSignatures(b *testing.B) {
	dir := b.TempDir()
	var ua, ref strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&ua, "SomeCrawler%dBot\n", i)
		fmt.Fprintf(&ref, "Spam-Site-%d.Example\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "signature_ua.dat"), []byte(ua.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signature_ref.dat"), []byte(ref.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	lists := ipindex.NewSet(dir, discardLog())
	sigs := detect.NewSignatures(dir, discardLog())
	in := botInput{
		IP:     netip.MustParseAddr("8.8.8.8"),
		UA:     "Mozilla/5.0 (Linux; Android 13; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36",
		Ref:    "https://news.example.org/article?id=1",
		Lang:   "en",
		Device: detect.Phone,
	}
	cfg := config.Bots{CheckUA: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if detectBot(lists, sigs, dir, in, cfg) != detect.BotNone {
			b.Fatal("human classified as bot")
		}
	}
}

// benchWriter is a response writer that keeps only the status code, so the
// benchmark does not pay for a recorder body per request.
type benchWriter struct {
	hdr  http.Header
	code int
}

// Header returns the header map the handler writes into.
func (w *benchWriter) Header() http.Header { return w.hdr }

// Write discards the body and counts an implicit 200.
func (w *benchWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return len(p), nil
}

// WriteHeader records the status code.
func (w *benchWriter) WriteHeader(code int) { w.code = code }

// BenchmarkRootBotPath measures the whole (*engineDeps).root handler on the bot path:
// a bots-only first stream with toggles that differ from the installation profile (so
// detectBot runs twice) and a regular redirect stream, over a human, a headless
// browser, curl and Googlebot from a few public IPs, without Redis, ClickHouse or
// network. It reports p50-ns and p99-ns of one request next to the mean.
func BenchmarkRootBotPath(b *testing.B) {
	dir := b.TempDir()
	botPath := filepath.Join(dir, "bot_protection.json")
	if err := os.WriteFile(botPath, []byte(`{"ch_ua":true,"ch_empty_ua":true,"ch_proxy":true}`), 0o644); err != nil {
		b.Fatal(err)
	}
	stubPTR(b, ptrNamed(), fwdTo())

	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	groups := group(
		config.Stream{Name: "Bots", Status: true,
			Rules: config.Rules{Bot: config.FlagA},
			Out:   config.Output{Redirect: "show_text", Out: "BOT"},
			Bots:  config.Bots{CheckUA: true, EmptyUA: true, EmptyLang: true, Redirect: "404_not_found"}},
		config.Stream{Name: "Flow", Status: true,
			Out: config.Output{Redirect: "http_redirect", Out: "https://offer.example/?ua=[USERAGENT]"}},
	)
	d := &engineDeps{
		log: log, lists: lists, sigs: detect.NewSignatures(dir, log), geores: geo.Nop{},
		groups: groups, dataDir: dir, keyFiles: newKeyWriter(dir, 0, nil), trashMode: "0", botPath: botPath,
	}
	realIP, err := server.NewRealIP([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		b.Fatal(err)
	}
	h := realIP.Middleware(http.HandlerFunc(d.root))

	uas := []string{
		uaIPhone,
		"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/120.0.0.0 Mobile Safari/537.36",
		"curl/8.4.0",
		uaGoogleBot,
	}
	ips := []string{"8.8.8.8", "1.1.1.1", "9.9.9.9", "208.67.222.222", "66.249.66.1"}
	reqs := make([]*http.Request, len(uas)*len(ips))
	for i := range reqs {
		req, err := http.NewRequest(http.MethodGet, "/g1", nil)
		if err != nil {
			b.Fatal(err)
		}
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("User-Agent", uas[i%len(uas)])
		req.Header.Set("Accept-Language", "en")
		req.Header.Set("X-Forwarded-For", ips[i%len(ips)])
		reqs[i] = req
	}
	w := &benchWriter{hdr: http.Header{}}
	serve := func(req *http.Request) {
		clear(w.hdr)
		w.code = 0
		h.ServeHTTP(w, req)
	}
	for _, req := range reqs {
		serve(req)
		if w.code == 0 {
			b.Fatalf("no response for %q", req.UserAgent())
		}
	}

	samples := make([]time.Duration, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := reqs[i%len(reqs)]
		start := time.Now()
		serve(req)
		samples[i] = time.Since(start)
	}
	b.StopTimer()
	reportPercentiles(b, samples)
}

// reportPercentiles sorts samples and reports their p50-ns and p99-ns.
// samples holds one duration per request; an empty slice reports nothing.
func reportPercentiles(b *testing.B, samples []time.Duration) {
	if len(samples) == 0 {
		return
	}
	slices.Sort(samples)
	b.ReportMetric(float64(samples[len(samples)/2]), "p50-ns")
	b.ReportMetric(float64(samples[min(len(samples)-1, len(samples)*99/100)]), "p99-ns")
}

// offerBook writes n active offers to a catalog file in dir and opens it.
// Offer i has id "o<i>" and URL https://offer<i>.example/. It fails b on a
// write or parse error.
func offerBook(b *testing.B, dir string, n int) *config.Book[config.Offer] {
	b.Helper()
	list := make([]config.Offer, n)
	for i := range list {
		list[i] = config.Offer{ID: fmt.Sprintf("o%d", i), Name: fmt.Sprintf("Offer %d", i), URL: fmt.Sprintf("https://offer%d.example/?c=[CID]", i), Status: true}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "offers.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		b.Fatal(err)
	}
	book, err := config.OpenBook(path, func(o config.Offer) string { return o.ID })
	if err != nil {
		b.Fatal(err)
	}
	return book
}

// BenchmarkCatalogs measures one catalogs call, which root makes once per
// click. The cost must not grow with the size of the offer catalog. Sizes
// are offers in the catalog; 1130 is the local catalog after the Keitaro
// import.
func BenchmarkCatalogs(b *testing.B) {
	for _, n := range []int{10, 1130, 5000} {
		b.Run(fmt.Sprintf("offers=%d", n), func(b *testing.B) {
			d := &engineDeps{offers: offerBook(b, b.TempDir(), n)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(d.catalogs().Offers) != n {
					b.Fatal("catalog incomplete")
				}
			}
		})
	}
}

// BenchmarkRootOfferStream measures the whole root handler for a click on a
// stream that rotates two catalog offers, among 10000 campaigns, with the
// offer catalog at several sizes. Requests run in parallel from many
// addresses, without Redis, ClickHouse or network. It reports p50-ns and
// p99-ns of one request next to the mean.
func BenchmarkRootOfferStream(b *testing.B) {
	for _, n := range []int{10, 1130, 5000} {
		b.Run(fmt.Sprintf("offers=%d", n), func(b *testing.B) {
			benchRootOffers(b, n)
		})
	}
}

// benchRootOffers runs BenchmarkRootOfferStream for a catalog of n offers.
func benchRootOffers(b *testing.B, n int) {
	dir := b.TempDir()
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	stream := config.Stream{Name: "Offers", Status: true,
		Out: config.Output{Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o1", Share: 50}, {ID: "o2", Share: 50}}}}
	all := make([]*config.Group, 0, 10000)
	for i := 0; i < 10000; i++ {
		all = append(all, &config.Group{ID: fmt.Sprintf("g%d", i), Name: fmt.Sprintf("g%d", i), Status: true, Redirect: "stop",
			UniqSeconds: 86400, Streams: []config.Stream{stream}})
	}
	d := &engineDeps{
		log: log, lists: lists, sigs: detect.NewSignatures(dir, log), geores: geo.Nop{},
		groups: config.NewGroups(all...), offers: offerBook(b, dir, n), dataDir: dir,
		keyFiles: newKeyWriter(dir, 0, nil), trashMode: "0",
	}
	realIP, err := server.NewRealIP([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		b.Fatal(err)
	}
	h := realIP.Middleware(http.HandlerFunc(d.root))
	reqs := make([]*http.Request, 256)
	for i := range reqs {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/g%d?sub_id_1=s%d&keyword=slots", i*37%10000, i), nil)
		if err != nil {
			b.Fatal(err)
		}
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("User-Agent", uaIPhone)
		req.Header.Set("Accept-Language", "en")
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("41.%d.%d.%d", i%200+1, i*7%250, i*13%250+1))
		reqs[i] = req
	}
	probe := &benchWriter{hdr: http.Header{}}
	h.ServeHTTP(probe, reqs[0])
	if probe.code != http.StatusFound {
		b.Fatalf("offer stream answered %d, want 302", probe.code)
	}

	var mu sync.Mutex
	var samples []time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		w := &benchWriter{hdr: http.Header{}}
		local := make([]time.Duration, 0, 1024)
		for i := 0; pb.Next(); i++ {
			clear(w.hdr)
			w.code = 0
			start := time.Now()
			h.ServeHTTP(w, reqs[i%len(reqs)])
			local = append(local, time.Since(start))
		}
		mu.Lock()
		samples = append(samples, local...)
		mu.Unlock()
	})
	b.StopTimer()
	reportPercentiles(b, samples)
}
