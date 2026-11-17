// Command engine — the emtds hot path (visitor traffic processing).
//
// Full request pipeline:
//
//	realip → blacklist (ipindex) → antiflood (Redis) → geo (mmdb/CF) →
//	detect (device+bots) → uniqueness (Redis) → router (stream selection) →
//	render (macros + redirect type) → async log (ClickHouse).
//
// All state (IP lists, signatures, group config) is kept in memory and
// refreshed in the background — without reading files on every request. Stores
// (Redis/ClickHouse) and the JSON group config are connected optionally via EMTDS_*.
// See docs/ARCHITECTURE.md.
package main

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/fetch"
	"github.com/hasayone/empataso-tds/internal/geo"
	"github.com/hasayone/empataso-tds/internal/ipindex"
	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/render"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/seplist"
	"github.com/hasayone/empataso-tds/internal/server"
	"github.com/hasayone/empataso-tds/internal/store"
)

// IP lists the engine keeps in memory (names = <name>.dat files).
var ipLists = []string{
	"ip_blacklist",
	"ip_google", "ip_bing", "ip_yandex", "ip_yahoo", "ip_mail", "ip_baidu",
	"ip_others",
	"wap",              // carriers (with labels like #beeline etc.)
	"ip_proxy",         // datacenter, VPN and proxy ranges; filled by cron
	"ip_private_relay", // iCloud Private Relay egress ranges, never counted as proxy; filled by cron
}

// shutdownPhase bounds each of the two sequential shutdown phases: draining
// in-flight requests, then draining the log buffer.
const shutdownPhase = 10 * time.Second

// Engine HTTP server limits. The read limit covers headers and body of one
// request; nginx buffers bodies before proxying, so 30s only stops a stalled
// direct client. net/http also cancels the request context once it passes,
// so it stays well above the longest handler wait (landing PHP, 10s). The
// idle limit is above nginx's 60s upstream keepalive, so
// nginx closes a pooled connection before the engine does. There is no write
// limit: landing and [REMOTE] responses may stream for longer.
const (
	readHeaderTimeout  = 5 * time.Second
	defaultReadTimeout = 30 * time.Second
	defaultIdleTimeout = 120 * time.Second
)

// engineServer builds the engine's HTTP server on addr serving h.
// read bounds reading one request, idle a keep-alive connection between
// requests; a non-positive value falls back to the default above. The
// header limit is fixed. It does not fail.
func engineServer(addr string, h http.Handler, read, idle time.Duration) *http.Server {
	if read <= 0 {
		read = defaultReadTimeout
	}
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       read,
		IdleTimeout:       idle,
	}
}

func main() {
	notices := newNoticeRing(200)
	log := slog.New(&noticeHandler{Handler: slog.NewJSONHandler(os.Stdout, nil), ring: notices})

	dataDir := getenv("EMTDS_DATA_DIR", "./data")
	addr := getenv("EMTDS_LISTEN", ":8080")
	reload := getdur("EMTDS_RELOAD_INTERVAL", time.Minute)

	// Load all IP lists into memory (phase 1).
	lists := ipindex.NewSet(dataDir, log)
	lists.Load(ipLists...)

	// Bot signatures (phase 2): UA/referer/ua_blacklist with hot-reload.
	sigs := detect.NewSignatures(dataDir, log)

	// Background hot-reload of changed files.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Geo (phase 2): MaxMind-format databases — City or Country in
	// EMTDS_GEO_DB, MaxMind ASN in EMTDS_ASN_DB, DB-IP ASN Lite beside them
	// (config.GeoFilePaths). None set → Nop (geo filters never fire). A
	// configured file that is not there yet is not fatal: the cron service
	// may still be downloading it, and Watch picks it up when it lands.
	var geores geo.Resolver = geo.Nop{}
	if gf := config.GeoFilePaths(); gf != (config.GeoFiles{}) {
		log.Info("geo paths", "city", gf.City, "asn", gf.ASN, "dbip_asn", gf.DBIPASN)
		db, err := geo.OpenWith(geo.Paths(gf), log)
		if err != nil {
			log.Warn("geo db not loaded yet, will keep watching", "err", err)
		}
		for _, in := range db.Databases() {
			log.Info("geo db", "path", in.Path, "type", in.Type, "built", in.Built, "loaded", in.Loaded)
		}
		geores = db
		go db.Watch(ctx, reload)
	}
	go lists.Watch(ctx, reload)
	go sigs.Watch(ctx, reload)

	// Redis counters (phase 4): uniq/firewall/limit. Optional. Short timeouts:
	// every counter fails open, so a slow Redis costs a click milliseconds.
	var counters *store.Counters
	if a := config.Env("EMTDS_REDIS_ADDR"); a != "" {
		rt := store.EngineRedisTimeouts()
		password := config.Env("EMTDS_REDIS_PASSWORD")
		rdb, err := store.OpenRedisWaiting(a, password, 0, rt)
		late := err != nil
		if late {
			// The client connects by itself once Redis is there; the engine
			// serves clicks meanwhile, every counter failing open.
			log.Warn("redis not connected, will keep trying", "err", err)
			rdb = store.NewRedisClient(a, password, 0, rt)
		}
		counters = store.NewCounters(rdb)
		uniqMax, uerr := store.UniqMaxFromEnv()
		if uerr != nil {
			log.Warn("uniqueness window ceiling", "err", uerr)
		}
		counters.SetUniqMax(uniqMax)
		redisUp := func() {
			startRedisTrim(ctx, rdb, uniqMax, log)
			log.Info("redis connected", "addr", a, "dial", rt.Dial.String(), "read", rt.Read.String(),
				"write", rt.Write.String(), "pool", rt.Pool.String())
		}
		if late {
			go awaitRedis(ctx, func(c context.Context) error { return rdb.Ping(c).Err() }, store.ConnectPause, redisUp)
		} else {
			redisUp()
		}
	}

	// trash modes for unknown/disabled groups (like trash()).
	trashMode := getenv("EMTDS_TRASH_MODE", "0") // 0=200,1=redirect,2=403,3=404
	trashURL := config.Env("EMTDS_TRASH_URL")

	// Postback and keyword collection.
	postbackKey := config.Env("EMTDS_POSTBACK_KEY")
	pbAuth := loadPostbackAuth(log)
	keyFiles := newKeyWriter(getenv("EMTDS_KEYS_DIR", "keys"), keysMaxBytes(log), log)

	// HTTP fetcher for CURL redirect and [REMOTE] (with in-memory cache).
	fetcher := fetch.New(getenv("EMTDS_CURL_UA", "Mozilla/5.0 (Windows NT 6.1; Win64; x64; rv:71.0) Gecko/20100101 Firefox/71.0"), log)
	curlCacheMin := 60
	if v := config.Env("EMTDS_CURL_CACHE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			curlCacheMin = n
		}
	}

	// Group config (phase 6): JSON file. Without it — the built-in demo group.
	var groups *config.Groups
	groupsFile := config.Env("EMTDS_GROUPS_FILE")
	if groupsFile != "" {
		if gg, err := config.LoadGroups(groupsFile); err != nil {
			log.Warn("groups file not loaded, using demo", "err", err)
		} else {
			groups = gg
			log.Info("groups loaded", "count", len(gg.List()))
		}
	}

	// Separation lists, held in memory for the same reason as the ip lists: the
	// previous implementation re-read and re-scanned the whole file on every
	// request that carried a keyword.
	seps := seplist.NewSet(dataDir, log)
	seps.Load(separationFiles(groups)...)
	go seps.Watch(ctx, reload)

	// Load the custom ip_list files referenced by group streams
	// (besides the standard ipLists), so the per-stream IP filter works.
	known := append([]string(nil), ipLists...)
	if extra := ipListFiles(groups); len(extra) > 0 {
		lists.Load(extra...)
		known = append(known, extra...)
		log.Info("extra ip lists loaded", "count", len(extra))
	}

	// Hot-reload of the groups config, like the .dat lists and signatures:
	// edits saved in the admin panel go live within the reload interval
	// instead of waiting for a restart.
	if groups != nil {
		go newGroupsWatcher(groupsFile, groups, lists, seps, log, known).watch(ctx, reload)
	}
	// EMTDS_API_KEY stays in the process environment. An empty key file means
	// only that env key can open ?api=; both empty rejects ?api=, as an empty
	// EMTDS_API_KEY did. The env value is not written to the key file and
	// is not logged.
	apiEnv := config.Env("EMTDS_API_KEY")
	keysFile := config.Env("EMTDS_API_KEYS_FILE")
	keySet := loadAPIKeys(keysFile, log)
	if keysFile != "" {
		go keysWatcher(ctx, keysFile, keySet, reload, log)
	}
	offers, landings, sources, networks, domains := openCatalogs(groupsFile, log)
	s2s := openS2S(groupsFile, log)
	botPath := config.Env("EMTDS_BOT_FILE")
	if botPath == "" && groupsFile != "" {
		botPath = filepath.Join(filepath.Dir(groupsFile), "bot_protection.json")
	}
	pages := newPageCache(pagesDir(config.Env("EMTDS_PAGES_DIR"), groupsFile), log)
	lands := newLandingServer(landingsDir(config.Env("EMTDS_LANDINGS_DIR"), groupsFile), config.Env("EMTDS_PHP_URL"), func(id string) (config.Landing, bool) {
		return landings.Get(id)
	}, log)
	go reloadCatalogs(ctx, reload, offers, landings, sources, networks, s2s, domains)

	// realip: trust XFF/CF only from configured proxies (spoofing protection).
	realIP, err := server.NewRealIP(trustedProxies())
	if err != nil {
		log.Error("realip init", "err", err)
		os.Exit(1)
	}

	d := &engineDeps{
		log: log, lists: lists, sigs: sigs, seps: seps, geores: geores, counters: counters,
		groups: groups, domains: domains, offers: offers, landings: landings, sources: sources, networks: networks, s2s: s2s,
		outbound: make(chan outboundJob, 64),
		fetcher:  fetcher,
		dataDir:  dataDir, keyFiles: keyFiles, postbackKey: postbackKey, pbAuth: pbAuth, keys: apiKeys{env: apiEnv, set: keySet},
		trashMode: trashMode, trashURL: trashURL, curlCacheMin: curlCacheMin,
		botPath:   botPath,
		cids:      newCIDSigner(config.Env("EMTDS_CID_KEY"), cidKeyPath(config.Env("EMTDS_CID_KEY_FILE"), groupsFile), log),
		pages:     pages,
		landServe: lands,
	}
	lands.originOn = d.withOrigin

	// ClickHouse logs (phase 4): asynchronous batch writes. Optional.
	var logs *logbuf.Buffer
	var spool *logbuf.Spool
	if a := config.Env("EMTDS_CLICKHOUSE_ADDR"); a != "" {
		sp, queue, serr := openEventSpool(log)
		if serr != nil {
			log.Error("event spool", "err", serr)
			os.Exit(1)
		}
		spool = sp
		logs = d.startEventLog(ctx, chLink{
			addr: a,
			open: func(octx context.Context) (*store.CH, error) {
				return store.OpenCHWaitingCtx(octx, a, getenv("EMTDS_CLICKHOUSE_DB", config.DefaultClickHouseDB),
					getenv("EMTDS_CLICKHOUSE_USER", config.DefaultClickHouseUser), config.Env("EMTDS_CLICKHOUSE_PASSWORD"))
			},
			pause:     store.ConnectPause,
			onConnect: func(ch *store.CH) { go ch.WatchDedup(ctx, dedupWatchEvery) },
		}, queue)
		d.startPostbackQueues(ctx, spool, log)
	}

	d.goBG(func() { d.serveOutbound(ctx) })
	// The deferred Close runs after the log buffer is closed, so the last
	// snapshot carries the final loss counters.
	collectMetrics, metricsFile := d.startMetrics(log, time.Now())
	defer metricsFile.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/postback", d.postback)
	mux.HandleFunc("/internal/notices", serveNotices(notices))
	mux.HandleFunc("/internal/metrics", serveMetrics(collectMetrics))
	mux.HandleFunc("/internal/version", versionHandler())
	mux.HandleFunc("/l/", d.serveLandingAsset)
	mux.HandleFunc("/healthz", healthzHandler(logs))
	mux.HandleFunc("/", d.root)

	srv := engineServer(addr, realIP.Middleware(mux),
		getdur("EMTDS_HTTP_READ_TIMEOUT", defaultReadTimeout), getdur("EMTDS_HTTP_IDLE_TIMEOUT", defaultIdleTimeout))

	go func() {
		bi := config.Build()
		log.Info("engine listening", "addr", addr, "version", bi.Version, "commit", bi.Short(), "lists", len(ipLists), "reload", reload.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	// stopBy is the end of the whole stop. The phases below keep their own
	// budgets, and the last two (the background workers and the postback
	// journal) get what is left, so the whole stop fits in stop_grace_period.
	stopBy := time.Now().Add(shutdownTotal)
	// Two budgets, not one. The phases are sequential, so a single shared
	// deadline means the log drain gets whatever the HTTP drain left over —
	// and a server that used all of it handed logbuf a dead context, i.e. no
	// chance to write the last batch at all.
	//
	// Worst case is therefore 2 * shutdownPhase, and the supervisor has to
	// allow for it: Docker's default is a SIGKILL ten seconds after SIGTERM,
	// which would land in the middle of the log drain. See README, "Shutdown
	// budget".
	sctx, scancel := context.WithTimeout(context.Background(), shutdownPhase)
	defer scancel()
	// Stop serving before stopping the log buffer, otherwise in-flight requests
	// keep pushing events into a buffer nobody drains any more.
	_ = srv.Shutdown(sctx)
	if logs != nil {
		// Close owns the shutdown: it shuts the door, drains and waits for the
		// writer, all inside the same budget. cancel() below stays as a backstop
		// for the case where Close gave up with work still in flight.
		lctx, lcancel := context.WithTimeout(context.Background(), shutdownPhase)
		defer lcancel()
		if err := logs.Close(lctx); err != nil {
			log.Warn("logbuf: final flush did not finish within the shutdown deadline", "err", err)
		}
		d.stopBackground(cancel, stopBy, log)
		l := logs.Losses()
		st := logs.Stats()
		if spool != nil {
			spool.Close()
		}
		log.Info("engine stopped", "events_lost", l.Total(),
			"lost_full", l.Full, "lost_queue", l.Queue,
			"lost_insert", l.Insert, "lost_late", l.Late, "lost_spool_full", l.SpoolFull,
			"spool_segments", st.Spool.Segments, "spool_events", st.Spool.Events)
		return
	}
	d.stopBackground(cancel, stopBy, log)
	log.Info("engine stopped")
}

// demoGroup — built-in demo configuration used when EMTDS_GROUPS_FILE is not set.
var demoGroup = &config.Group{
	ID: "demo", Name: "demo", Status: true,
	Redirect:    "stop", // group default when no stream is selected
	UniqSeconds: 86400,
	Firewall:    config.FirewallRule{Enabled: false, Queries: 100, Seconds: 60},
	Streams: []config.Stream{
		// phones from RU only
		{Name: "mobile_ru", Status: true,
			Rules: config.Rules{Computer: config.FlagA, Tablet: config.FlagA,
				Country: config.ListFilter{Flag: config.FlagB, Raw: "ru", Values: []string{"ru"}}},
			Out:  config.Output{Redirect: "http_redirect", Out: "https://m.example.com/?k=[KEY]&r=[RANDNUM-1000-9999]"},
			Bots: config.Bots{CheckUA: true, EmptyUA: true, IPGoogle: true, IPYandex: true, IPOthers: true, Redirect: "404_not_found"}},
		// desktop (phones/tablets are blocked)
		{Name: "desktop", Status: true,
			Rules: config.Rules{Phone: config.FlagA, Tablet: config.FlagA},
			Out:   config.Output{Redirect: "http_redirect", Out: "https://example.com/?k=[KEY]"},
			Bots:  config.Bots{CheckUA: true, IPGoogle: true, IPYandex: true, IPOthers: true, Redirect: "404_not_found"}},
		// catch-all
		{Name: "default", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "no offer"}},
	},
}

// cookieUnique implements "cookie-based" uniqueness: a dedicated cookie used
// only for uniqueness (not mixed with the rotator counter), with HttpOnly/SameSite
// flags and a correct TTL = UniqSeconds.
// First visit (no cookie) → unique and the cookie is set; repeat → not unique.
func cookieUnique(w http.ResponseWriter, r *http.Request, grp *config.Group) bool {
	return cookieUniqueNamed(w, r, grp, "ztu_"+grp.ID)
}

// cookieUniqueNamed is the cookie check behind cookieUnique for any cookie name.
// w receives the cookie on a first visit, r is the request, grp gives the TTL
// (UniqSeconds, a day when unset) and name is the cookie. It returns true when
// the cookie was absent.
func cookieUniqueNamed(w http.ResponseWriter, r *http.Request, grp *config.Group, name string) bool {
	if _, err := r.Cookie(name); err == nil {
		return false
	}
	maxAge := grp.UniqSeconds
	if maxAge <= 0 {
		maxAge = 86400
	}
	setEngineCookie(w, r, &http.Cookie{
		Name: name, Value: "1", Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return true
}

// extraParams returns GET parameter values except q and origin, in URL order, up to 5.
func extraParams(r *http.Request) []string {
	var out []string
	for _, pair := range strings.Split(r.URL.RawQuery, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		if k == "q" || k == "origin" {
			continue
		}
		dv, err := url.QueryUnescape(v)
		if err != nil {
			dv = v
		}
		out = append(out, dv)
		if len(out) == 5 {
			break
		}
	}
	return out
}

// pickOut selects an output variant from out, separated by |||, according to the
// distribution type: rotator (cookie), evenly (Redis counter), random (default).
func pickOut(w http.ResponseWriter, r *http.Request, counters *store.Counters, ctx context.Context, groupID, stream, raw, dist string, shares []int) string {
	if next, ok := router.ApplyOutShares(raw, shares, nil); ok {
		return next
	}
	if !strings.Contains(raw, "|||") {
		return raw
	}
	var parts []string
	for _, p := range strings.Split(raw, "|||") {
		parts = append(parts, strings.TrimSpace(p))
	}
	switch dist {
	case "rotator":
		name := "ztrot_" + groupID + "_" + stream
		idx := 0
		if c, err := r.Cookie(name); err == nil {
			if n, e := strconv.Atoi(c.Value); e == nil {
				idx = variantIndex(n+1, len(parts))
			}
		}
		setEngineCookie(w, r, &http.Cookie{Name: name, Value: strconv.Itoa(idx), Path: "/", HttpOnly: true, MaxAge: 86400})
		return parts[idx]
	case "evenly":
		if counters != nil {
			return parts[variantIndex(counters.Rotate(ctx, groupID, stream, len(parts)), len(parts))]
		}
		fallthrough
	default:
		return parts[rand.Intn(len(parts))]
	}
}

// variantIndex folds i into [0,n), restarting the cycle on anything out of
// range. Neither rotator nor evenly produces its index locally: the rotator
// reads it back from a cookie the visitor may rewrite, and evenly gets it from
// a Redis counter. A cookie of "-5" used to index the variant slice at -4 and
// take the whole request down with a panic.
func variantIndex(i, n int) int {
	if n <= 0 || i < 0 || i >= n {
		return 0
	}
	return i
}

// trashResult builds the response for an unknown/disabled group (trash modes).
func trashResult(mode, url string) render.Result {
	switch mode {
	case "1":
		if url != "" {
			return render.Result{Status: http.StatusFound, Location: url}
		}
	case "2":
		return render.Do("403_forbidden", "", render.Options{})
	case "3":
		return render.Do("404_not_found", "", render.Options{})
	}
	return render.Result{Status: http.StatusOK}
}

// limiter adapts store.Counters to the router.Limiter interface (per-request).
type limiter struct {
	c   *store.Counters
	ctx context.Context
	id  string
}

// Allowed both checks and consumes the stream's limit, in one atomic step.
//
// It is safe to have the side effect here because the limit is the last filter
// in router.matches and Select returns the first matching stream: a true from
// this call is the serve. There is no second call to record it with, and that is
// the point — the pair it replaced could not be made race-free.
func (l limiter) Allowed(stream string, rule config.LimitRule) bool {
	ok, err := l.c.TakeLimit(l.ctx, l.id, stream, rule)
	if err != nil {
		return true // fail-open
	}
	return ok
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func b2yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func langOf(r *http.Request) string {
	if al := r.Header.Get("Accept-Language"); len(al) >= 2 {
		return isoCode(al[:2])
	}
	return router.Empty
}

// isoCode lower-cases a two-character code and returns router.Empty for
// anything else. Country and language codes arrive in headers any visitor can
// set, and both end up in file names via [RANDLINE-([COUNTRY].dat)-1]: two
// ASCII alphanumerics cannot name a file the operator did not intend. Digits
// are allowed for Cloudflare's own markers, XX (unknown) and T1 (Tor).
func isoCode(s string) string {
	if len(s) != 2 {
		return router.Empty
	}
	for i := 0; i < 2; i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z') {
			return router.Empty
		}
	}
	return strings.ToLower(s)
}

func refOrEmpty(ref string) string {
	if ref == "" {
		return router.Empty
	}
	return ref
}

func domainOf(ref string) string {
	if ref == "" {
		return "unknown"
	}
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		return u.Host
	}
	return "unknown"
}

// trustedProxies returns the CIDRs of trusted proxies from EMTDS_TRUSTED_PROXIES
// (comma-separated). Defaults to localhost only (XFF from outside is not accepted).
func trustedProxies() []string {
	if v := config.Env("EMTDS_TRUSTED_PROXIES"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"127.0.0.1/32", "::1/128"}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func getenv(k, def string) string {
	if v := config.Env(k); v != "" {
		return v
	}
	return def
}

func getdur(k string, def time.Duration) time.Duration {
	if v := config.Env(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
