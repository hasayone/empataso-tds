// Command admin — KuzTDS management REST API (auth, statistics, groups).
//
// Run:   KUZTDS_ADMIN_PASSWORD_HASH=... go run ./cmd/admin
// Password hash:   go run ./cmd/admin -hash 'my-password'
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/egerkuzma/kuztds/internal/admin"
	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

func main() {
	hashPw := flag.String("hash", "", "print the argon2id hash for a password and exit")
	flag.Parse()
	if *hashPw != "" {
		os.Exit(printHash(*hashPw))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg := admin.Config{
		AdminUser:    getenv("KUZTDS_ADMIN_USER", "admin"),
		PasswordHash: os.Getenv("KUZTDS_ADMIN_PASSWORD_HASH"),
		SessionTTL:   12 * time.Hour,
		CookieSecure: getenv("KUZTDS_ADMIN_COOKIE_SECURE", "true") == "true",
		EngineURL:    getenv("KUZTDS_ENGINE_URL", "http://localhost:8080"),
		PublicURL:    os.Getenv("KUZTDS_PUBLIC_URL"),
		PasswordFile: os.Getenv("KUZTDS_ADMIN_PASSWORD_FILE"),
		APIKeysFile:  os.Getenv("KUZTDS_API_KEYS_FILE"),
		Sessions:     security.NewMemoryStore(), // overridden by Redis below
		Limiter:      nil,
		Log:          log,
		KeitaroBase:  strings.TrimSpace(os.Getenv("KEITARO_BASE_URL")),
		KeitaroKey:   os.Getenv("KEITARO_API_KEY"),
		PublicIP:     strings.TrimSpace(os.Getenv("KUZTDS_PUBLIC_IP")),
	}

	connectRedis(&cfg, log)

	// Groups config (same JSON as the engine) — read and save.
	if f := os.Getenv("KUZTDS_GROUPS_FILE"); f != "" {
		applyDataFiles(&cfg, f, log)
	}

	// .dat lists (IP, WAP operators, signatures) — editor.
	if d := os.Getenv("KUZTDS_DATA_DIR"); d != "" {
		cfg.Lists = admin.NewFileLists(d)
		log.Info("lists dir", "path", d)
	}

	// Tools: IP lookup and the visitor simulator read the same lists and geo
	// databases the engine uses. Automation: the cron service's config file.
	cfg.DataDir = os.Getenv("KUZTDS_DATA_DIR")
	cfg.CronFile = os.Getenv("KUZTDS_CRON_FILE")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startGeo(ctx, &cfg, log)

	// Keywords.
	cfg.Keys = admin.NewFileKeys(getenv("KUZTDS_KEYS_DIR", "keys"))

	connectClickHouse(&cfg, log)

	// A hash we cannot use is a reason not to start. Booting anyway means every
	// login answers "invalid credentials" and the operator hunts a password
	// that is not the problem.
	if v := strings.TrimSpace(os.Getenv("KUZTDS_ADMIN_TRUSTED_PROXIES")); v != "" {
		cfg.AdminTrustedProxies = strings.Split(v, ",")
	}
	app, err := admin.New(cfg)
	if err != nil {
		log.Error("admin api not started", "err", err)
		os.Exit(1)
	}

	go app.WatchDomains(ctx, domainWatchInterval(os.Getenv("KUZTDS_DOMAIN_WATCH"), log))
	go app.WatchKeitaroSync(ctx)

	srv := &http.Server{
		Addr:              getenv("KUZTDS_ADMIN_LISTEN", ":8090"),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("admin api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	log.Info("admin api stopped")
}

// printHash writes the argon2id hash of password to stdout and returns the process exit code:
// 0 on success, 1 when hashing fails (the error goes to stderr).
func printHash(password string) int {
	h, err := security.HashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println(h)
	return 0
}

// applyDataFiles points cfg at the catalogs, counters, settings files and directories that sit
// next to the groups file. groupsFile is that file; each path can be overridden by its
// KUZTDS_* variable. log receives one line per directory in use.
func applyDataFiles(cfg *admin.Config, groupsFile string, log *slog.Logger) {
	f := groupsFile
	cfg.Groups = admin.NewFileGroups(f)
	log.Info("groups file", "path", f)
	dir := filepath.Dir(f)
	cfg.Offers = admin.NewJSONStore[config.Offer](getenv("KUZTDS_OFFERS_FILE", filepath.Join(dir, "offers.json")))
	cfg.Boards = admin.NewJSONStore[config.Board](getenv("KUZTDS_BOARDS_FILE", filepath.Join(dir, "boards.json")))
	cfg.S2S = admin.NewJSONStore[config.OutboundPostback](getenv("KUZTDS_S2S_FILE", filepath.Join(dir, "s2s.json")))
	cfg.Landings = admin.NewJSONStore[config.Landing](getenv("KUZTDS_LANDINGS_FILE", filepath.Join(dir, "landings.json")))
	cfg.Sources = admin.NewJSONStore[config.TrafficSource](getenv("KUZTDS_SOURCES_FILE", filepath.Join(dir, "sources.json")))
	cfg.Networks = admin.NewJSONStore[config.AffiliateNetwork](getenv("KUZTDS_NETWORKS_FILE", filepath.Join(dir, "networks.json")))
	cfg.Domains = admin.NewJSONStore[config.Domain](getenv("KUZTDS_DOMAINS_FILE", filepath.Join(dir, "domains.json")))
	cfg.SeqOffers = admin.NewIDSeq(getenv("KUZTDS_OFFER_SEQ_FILE", filepath.Join(dir, "offer_seq.json")))
	cfg.SeqLandings = admin.NewIDSeq(getenv("KUZTDS_LANDING_SEQ_FILE", filepath.Join(dir, "landing_seq.json")))
	cfg.SeqSources = admin.NewIDSeq(getenv("KUZTDS_SOURCE_SEQ_FILE", filepath.Join(dir, "source_seq.json")))
	cfg.SeqNetworks = admin.NewIDSeq(getenv("KUZTDS_NETWORK_SEQ_FILE", filepath.Join(dir, "network_seq.json")))
	cfg.SeqDomains = admin.NewIDSeq(getenv("KUZTDS_DOMAIN_SEQ_FILE", filepath.Join(dir, "domain_seq.json")))
	cfg.SpendPath = getenv("KUZTDS_SPEND_FILE", filepath.Join(dir, "spend.json"))
	cfg.BotPath = getenv("KUZTDS_BOT_FILE", filepath.Join(dir, "bot_protection.json"))
	cfg.UIPath = getenv("KUZTDS_UI_FILE", filepath.Join(dir, "ui.json"))
	cfg.ViewsPath = getenv("KUZTDS_VIEWS_FILE", filepath.Join(dir, "views.json"))
	pagesDir := getenv("KUZTDS_PAGES_DIR", filepath.Join(dir, "pages"))
	cfg.Pages = admin.NewFilePages(pagesDir)
	log.Info("pages dir", "path", pagesDir)
	cfg.Manifest = admin.NewFileManifest(filepath.Join(dir, "keitaro-import.json"))
	cfg.KeitaroSyncFile = keitaroSyncFileForGroups(f)
	cfg.KeitaroFile = getenv("KUZTDS_KEITARO_FILE", filepath.Join(dir, "keitaro.json"))
	cfg.NamecheapFile = getenv("KUZTDS_NAMECHEAP_FILE", filepath.Join(dir, "namecheap.json"))
	cfg.NamecheapURL = os.Getenv("KUZTDS_NAMECHEAP_URL")
	cfg.NamecheapSandboxURL = os.Getenv("KUZTDS_NAMECHEAP_SANDBOX_URL")
	cfg.CloudflareFile = getenv("KUZTDS_CLOUDFLARE_FILE", filepath.Join(dir, "cloudflare.json"))
	landingsDir := getenv("KUZTDS_LANDINGS_DIR", filepath.Join(dir, "landings"))
	landings := admin.NewFileLandings(landingsDir)
	cfg.LandingFiles = landings
	log.Info("landings dir", "path", landingsDir)
	restored, removed, err := landings.RecoverUnzip(time.Hour)
	if err != nil {
		log.Warn("landings recovery", "err", err)
	}
	if restored+removed > 0 {
		log.Info("landings recovery", "restored", restored, "removed", removed)
	}
	cfg.UsersFile = getenv("KUZTDS_USERS_FILE", filepath.Join(dir, "users.json"))
	log.Info("users file", "path", cfg.UsersFile)
	cfg.RolesFile = getenv("KUZTDS_ROLES_FILE", filepath.Join(dir, "roles.json"))
	log.Info("roles file", "path", cfg.RolesFile)
}

// Bounds of the domain watch interval, set by KUZTDS_DOMAIN_WATCH.
const (
	domainWatchDefault = 5 * time.Minute
	domainWatchMin     = time.Minute
)

// domainWatchInterval reads the interval of the domain watch.
// raw is the value of KUZTDS_DOMAIN_WATCH, a Go duration such as 5m. An empty or invalid value
// gives five minutes and a value under a minute gives a minute; log receives a warning for a
// value that is invalid or raised.
func domainWatchInterval(raw string, log *slog.Logger) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return domainWatchDefault
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	switch {
	case err != nil:
		log.Warn("KUZTDS_DOMAIN_WATCH is not a duration, using the default", "default", domainWatchDefault)
		return domainWatchDefault
	case d < domainWatchMin:
		log.Warn("KUZTDS_DOMAIN_WATCH is below the minimum, using it", "minimum", domainWatchMin)
		return domainWatchMin
	}
	return d
}

// connectRedis moves sessions and the login rate limit to Redis when KUZTDS_REDIS_ADDR is set.
// cfg is updated in place. A connection failure is logged and the in-memory store stays.
func connectRedis(cfg *admin.Config, log *slog.Logger) {
	a := os.Getenv("KUZTDS_REDIS_ADDR")
	if a == "" {
		return
	}
	rdb, err := store.OpenRedis(a, os.Getenv("KUZTDS_REDIS_PASSWORD"), 0)
	if err != nil {
		log.Warn("redis not connected, using in-memory sessions", "err", err)
		return
	}
	cfg.Sessions = store.NewRedisSessions(rdb)
	cfg.Limiter = limiterAdapter{c: store.NewCounters(rdb)}
	cfg.LoginRateLimitShared = true
	log.Info("redis connected", "addr", a)
}

// connectClickHouse wires statistics and the warehouse when KUZTDS_CLICKHOUSE_ADDR is set.
// cfg is updated in place. A connection failure is logged and statistics stay disabled.
func connectClickHouse(cfg *admin.Config, log *slog.Logger) {
	a := os.Getenv("KUZTDS_CLICKHOUSE_ADDR")
	if a == "" {
		return
	}
	ch, err := store.OpenCH(a, getenv("KUZTDS_CLICKHOUSE_DB", "kuztds"),
		getenv("KUZTDS_CLICKHOUSE_USER", "kuztds"), os.Getenv("KUZTDS_CLICKHOUSE_PASSWORD"))
	if err != nil {
		log.Warn("clickhouse not connected, stats disabled", "err", err)
		return
	}
	cfg.Stats = ch
	cfg.Warehouse = ch
	log.Info("clickhouse connected", "addr", a)
}

// startGeo opens the geo databases named by KUZTDS_GEO_DB and KUZTDS_ASN_DB and keeps them
// reloaded until ctx ends. cfg is updated in place; nothing happens when no path is set. A database that
// cannot be loaded yet is logged and watched, so a later file replacement is picked up.
func startGeo(ctx context.Context, cfg *admin.Config, log *slog.Logger) {
	cfg.GeoCityPath, cfg.GeoASNPath = os.Getenv("KUZTDS_GEO_DB"), os.Getenv("KUZTDS_ASN_DB")
	if cfg.GeoCityPath == "" && cfg.GeoASNPath == "" {
		return
	}
	db, err := geo.Open(cfg.GeoCityPath, cfg.GeoASNPath, log)
	if err != nil {
		log.Warn("geo db not loaded yet, will keep watching", "err", err)
	}
	cfg.Geo = db
	go db.Watch(ctx, time.Minute)
}

// limiterAdapter limits login to 10 attempts per minute per key (IP).
type limiterAdapter struct{ c *store.Counters }

func (a limiterAdapter) Allow(ctx context.Context, key string) (bool, error) {
	return a.c.LoginAllow(ctx, key, 10, time.Minute)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// keitaroSyncFileForGroups returns the catch-up cursor path for a groups file directory.
func keitaroSyncFileForGroups(groupsFile string) string {
	return getenv("KUZTDS_KEITARO_SYNC_FILE", filepath.Join(filepath.Dir(groupsFile), "keitaro-sync.json"))
}
