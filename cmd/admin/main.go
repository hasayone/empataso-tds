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
		h, err := security.HashPassword(*hashPw)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(h)
		return
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
		Limiter:      allowAll{},
		Log:          log,
		KeitaroBase:  strings.TrimSpace(os.Getenv("KEITARO_BASE_URL")),
		KeitaroKey:   os.Getenv("KEITARO_API_KEY"),
	}

	// Redis: sessions (survive restart) + login rate-limit.
	if a := os.Getenv("KUZTDS_REDIS_ADDR"); a != "" {
		if rdb, err := store.OpenRedis(a, os.Getenv("KUZTDS_REDIS_PASSWORD"), 0); err != nil {
			log.Warn("redis not connected, using in-memory sessions", "err", err)
		} else {
			cfg.Sessions = store.NewRedisSessions(rdb)
			cfg.Limiter = limiterAdapter{c: store.NewCounters(rdb)}
			log.Info("redis connected", "addr", a)
		}
	}

	// Groups config (same JSON as the engine) — read and save.
	if f := os.Getenv("KUZTDS_GROUPS_FILE"); f != "" {
		cfg.Groups = admin.NewFileGroups(f)
		log.Info("groups file", "path", f)
		dir := filepath.Dir(f)
		cfg.Offers = admin.NewJSONStore[config.Offer](getenv("KUZTDS_OFFERS_FILE", filepath.Join(dir, "offers.json")))
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
		pagesDir := getenv("KUZTDS_PAGES_DIR", filepath.Join(dir, "pages"))
		cfg.Pages = admin.NewFilePages(pagesDir)
		log.Info("pages dir", "path", pagesDir)
		cfg.Manifest = admin.NewFileManifest(filepath.Join(dir, "keitaro-import.json"))
		landingsDir := getenv("KUZTDS_LANDINGS_DIR", filepath.Join(dir, "landings"))
		cfg.LandingFiles = admin.NewFileLandings(landingsDir)
		log.Info("landings dir", "path", landingsDir)
		cfg.UsersFile = getenv("KUZTDS_USERS_FILE", filepath.Join(dir, "users.json"))
		log.Info("users file", "path", cfg.UsersFile)
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
	cfg.GeoCityPath, cfg.GeoASNPath = os.Getenv("KUZTDS_GEO_DB"), os.Getenv("KUZTDS_ASN_DB")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cfg.GeoCityPath != "" || cfg.GeoASNPath != "" {
		db, err := geo.Open(cfg.GeoCityPath, cfg.GeoASNPath, log)
		if err != nil {
			log.Warn("geo db not loaded yet, will keep watching", "err", err)
		}
		cfg.Geo = db
		go db.Watch(ctx, time.Minute)
	}

	// Keywords.
	cfg.Keys = admin.NewFileKeys(getenv("KUZTDS_KEYS_DIR", "keys"))

	// ClickHouse: statistics (store.CH implements admin.StatsProvider directly).
	if a := os.Getenv("KUZTDS_CLICKHOUSE_ADDR"); a != "" {
		ch, err := store.OpenCH(a, getenv("KUZTDS_CLICKHOUSE_DB", "kuztds"),
			getenv("KUZTDS_CLICKHOUSE_USER", "kuztds"), os.Getenv("KUZTDS_CLICKHOUSE_PASSWORD"))
		if err != nil {
			log.Warn("clickhouse not connected, stats disabled", "err", err)
		} else {
			cfg.Stats = ch
			cfg.Warehouse = ch
			log.Info("clickhouse connected", "addr", a)
		}
	}

	// A hash we cannot use is a reason not to start. Booting anyway means every
	// login answers "invalid credentials" and the operator hunts a password
	// that is not the problem.
	app, err := admin.New(cfg)
	if err != nil {
		log.Error("admin api not started", "err", err)
		os.Exit(1)
	}

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

// limiterAdapter limits login to 10 attempts per minute per key (IP).
type limiterAdapter struct{ c *store.Counters }

func (a limiterAdapter) Allow(ctx context.Context, key string) (bool, error) {
	return a.c.LoginAllow(ctx, key, 10, time.Minute)
}

// allowAll is the limiter used when Redis is not configured. It never fails,
// so the handler's refuse-on-error branch cannot fire in a deployment that has
// no counters at all.
type allowAll struct{}

func (allowAll) Allow(context.Context, string) (bool, error) { return true, nil }

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
