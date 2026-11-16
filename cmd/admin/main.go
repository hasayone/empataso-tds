// Command admin — EmTDS management REST API (auth, statistics, groups).
//
// Run:   EMTDS_ADMIN_PASSWORD_HASH=... go run ./cmd/admin
// Password hash:   go run ./cmd/admin -hash 'my-password'
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/egerkuzma/emtds/internal/admin"
	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/cron"
	"github.com/egerkuzma/emtds/internal/geo"
	"github.com/egerkuzma/emtds/internal/security"
	"github.com/egerkuzma/emtds/internal/store"
)

func main() {
	if code, stop := startupFlags(); stop {
		os.Exit(code)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	uniqMax, uerr := store.UniqMaxFromEnv()
	if uerr != nil {
		log.Warn("uniqueness window ceiling", "err", uerr)
	}

	cfg := admin.Config{
		AdminUser:    getenv("EMTDS_ADMIN_USER", "admin"),
		PasswordHash: config.Env("EMTDS_ADMIN_PASSWORD_HASH"),
		SessionTTL:   12 * time.Hour,
		UniqMax:      uniqMax,
		CookieSecure: getenv("EMTDS_ADMIN_COOKIE_SECURE", "true") == "true",
		EngineURL:    getenv("EMTDS_ENGINE_URL", "http://localhost:8080"),
		PublicURL:    config.Env("EMTDS_PUBLIC_URL"),
		PasswordFile: config.Env("EMTDS_ADMIN_PASSWORD_FILE"),
		APIKeysFile:  config.Env("EMTDS_API_KEYS_FILE"),
		Sessions:     security.NewMemoryStore(), // overridden by Redis below
		Limiter:      nil,
		Log:          log,
		KeitaroBase:  strings.TrimSpace(os.Getenv("KEITARO_BASE_URL")),
		KeitaroKey:   os.Getenv("KEITARO_API_KEY"),
		PublicIP:     strings.TrimSpace(config.Env("EMTDS_PUBLIC_IP")),

		CIDKeyFile:    cidKeyFile(config.Env("EMTDS_CID_KEY_FILE"), config.Env("EMTDS_GROUPS_FILE")),
		SecretValues:  secretValues(),
		AITestBaseURL: strings.TrimSpace(config.Env("EMTDS_AI_TEST_BASE_URL")),
	}

	connectRedis(&cfg, log)

	// Groups config (same JSON as the engine) — read and save.
	if f := config.Env("EMTDS_GROUPS_FILE"); f != "" {
		applyDataFiles(&cfg, f, log)
	}

	// .dat lists (IP, WAP operators, signatures) — editor.
	if d := config.Env("EMTDS_DATA_DIR"); d != "" {
		cfg.Lists = admin.NewFileLists(d)
		log.Info("lists dir", "path", d)
	}

	// Tools: IP lookup and the visitor simulator read the same lists and geo
	// databases the engine uses. Automation: the cron service's config file.
	cfg.DataDir = config.Env("EMTDS_DATA_DIR")
	cfg.CronFile = config.Env("EMTDS_CRON_FILE")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startGeo(ctx, &cfg, log)

	// Keywords.
	cfg.Keys = admin.NewFileKeys(getenv("EMTDS_KEYS_DIR", "keys"))

	connectClickHouse(&cfg, log)

	// A hash we cannot use is a reason not to start. Booting anyway means every
	// login answers "invalid credentials" and the operator hunts a password
	// that is not the problem.
	if v := strings.TrimSpace(config.Env("EMTDS_ADMIN_TRUSTED_PROXIES")); v != "" {
		cfg.AdminTrustedProxies = strings.Split(v, ",")
	}
	app, err := admin.New(cfg)
	if err != nil {
		log.Error("admin api not started", "err", err)
		os.Exit(1)
	}

	go app.WatchDomains(ctx, domainWatchInterval(config.Env("EMTDS_DOMAIN_WATCH"), log))
	go app.WatchKeitaroSync(ctx)
	go app.WatchChecker(ctx)
	go app.WatchAudit(ctx)
	go app.WatchAssistant(ctx)
	go app.WatchUpdatePending(ctx)
	go app.WatchKeyUse(ctx)

	srv := &http.Server{
		Addr:              getenv("EMTDS_ADMIN_LISTEN", ":8090"),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	srv.RegisterOnShutdown(app.CancelTurns)
	go func() {
		b := config.Build()
		log.Info("admin api listening", "addr", srv.Addr, "version", b.Version, "commit", b.Short())
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
	app.FlushKeyUse()
	log.Info("admin api stopped")
}

// secretEnvNames are the environment variables whose values are secrets the assistant masks.
var secretEnvNames = []string{
	"EMTDS_CLICKHOUSE_PASSWORD", "EMTDS_REDIS_PASSWORD", "EMTDS_POSTBACK_KEY", "EMTDS_CID_KEY",
	"EMTDS_API_KEY", "EMTDS_ADMIN_PASSWORD", "EMTDS_ADMIN_PASSWORD_HASH", "KEITARO_API_KEY",
}

// secretValues returns the non-empty values of secretEnvNames. A secret moved to a file
// or a volume later changes only the environment, not the admin package.
func secretValues() []string {
	var out []string
	for _, name := range secretEnvNames {
		if v := config.Env(name); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// cidKeyFile returns the click id signing key file of the engine: override when it is not
// empty, otherwise cid.key next to groupsFile, or "" when neither is known. The rule is the
// engine's own, so both processes read the same file.
func cidKeyFile(override, groupsFile string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	if groupsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(groupsFile), "cid.key")
}

// startupFlags parses -hash and -recovery. stop true means the process should
// exit with code and not open the API. -hash prints an argon2id hash. -recovery
// prints a one-time code. A password is not accepted as an argument.
func startupFlags() (code int, stop bool) {
	hashPw := flag.String("hash", "", "print the argon2id hash for a password and exit")
	recovery := flag.Bool("recovery", false, "print a one-time password recovery code and exit")
	flag.Parse()
	if *hashPw != "" {
		return printHash(*hashPw), true
	}
	if *recovery {
		return runRecovery(os.Stdout, flag.Args()), true
	}
	return 0, false
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
// EMTDS_* variable. log receives one line per directory in use.
func applyDataFiles(cfg *admin.Config, groupsFile string, log *slog.Logger) {
	f := groupsFile
	cfg.Groups = admin.NewFileGroups(f)
	log.Info("groups file", "path", f)
	dir := filepath.Dir(f)
	cfg.Offers = admin.NewJSONStore[config.Offer](getenv("EMTDS_OFFERS_FILE", filepath.Join(dir, "offers.json")))
	cfg.Boards = admin.NewJSONStore[config.Board](getenv("EMTDS_BOARDS_FILE", filepath.Join(dir, "boards.json")))
	cfg.S2S = admin.NewJSONStore[config.OutboundPostback](getenv("EMTDS_S2S_FILE", filepath.Join(dir, "s2s.json")))
	cfg.Landings = admin.NewJSONStore[config.Landing](getenv("EMTDS_LANDINGS_FILE", filepath.Join(dir, "landings.json")))
	cfg.Sources = admin.NewJSONStore[config.TrafficSource](getenv("EMTDS_SOURCES_FILE", filepath.Join(dir, "sources.json")))
	cfg.Networks = admin.NewJSONStore[config.AffiliateNetwork](getenv("EMTDS_NETWORKS_FILE", filepath.Join(dir, "networks.json")))
	cfg.Domains = admin.NewJSONStore[config.Domain](getenv("EMTDS_DOMAINS_FILE", filepath.Join(dir, "domains.json")))
	cfg.SeqOffers = admin.NewIDSeq(getenv("EMTDS_OFFER_SEQ_FILE", filepath.Join(dir, "offer_seq.json")))
	cfg.SeqLandings = admin.NewIDSeq(getenv("EMTDS_LANDING_SEQ_FILE", filepath.Join(dir, "landing_seq.json")))
	cfg.SeqSources = admin.NewIDSeq(getenv("EMTDS_SOURCE_SEQ_FILE", filepath.Join(dir, "source_seq.json")))
	cfg.SeqNetworks = admin.NewIDSeq(getenv("EMTDS_NETWORK_SEQ_FILE", filepath.Join(dir, "network_seq.json")))
	cfg.SeqDomains = admin.NewIDSeq(getenv("EMTDS_DOMAIN_SEQ_FILE", filepath.Join(dir, "domain_seq.json")))
	cfg.SpendPath = getenv("EMTDS_SPEND_FILE", filepath.Join(dir, "spend.json"))
	cfg.BotPath = getenv("EMTDS_BOT_FILE", filepath.Join(dir, "bot_protection.json"))
	cfg.UIPath = getenv("EMTDS_UI_FILE", filepath.Join(dir, "ui.json"))
	cfg.ViewsPath = getenv("EMTDS_VIEWS_FILE", filepath.Join(dir, "views.json"))
	pagesDir := getenv("EMTDS_PAGES_DIR", filepath.Join(dir, "pages"))
	cfg.Pages = admin.NewFilePages(pagesDir)
	log.Info("pages dir", "path", pagesDir)
	cfg.Manifest = admin.NewFileManifest(filepath.Join(dir, "keitaro-import.json"))
	cfg.KeitaroSyncFile = keitaroSyncFileForGroups(f)
	cfg.KeitaroFile = getenv("EMTDS_KEITARO_FILE", filepath.Join(dir, "keitaro.json"))
	cfg.NamecheapFile = getenv("EMTDS_NAMECHEAP_FILE", filepath.Join(dir, "namecheap.json"))
	cfg.NamecheapURL = config.Env("EMTDS_NAMECHEAP_URL")
	cfg.NamecheapSandboxURL = config.Env("EMTDS_NAMECHEAP_SANDBOX_URL")
	cfg.CloudflareFile = getenv("EMTDS_CLOUDFLARE_FILE", filepath.Join(dir, "cloudflare.json"))
	cfg.CheckerFile = getenv("EMTDS_CHECKER_FILE", filepath.Join(dir, "checker.json"))
	cfg.CheckerAppsFile = getenv("EMTDS_CHECKER_APPS_FILE", filepath.Join(dir, "checker-apps.json"))
	cfg.DocsFile = getenv("EMTDS_DOCS_FILE", filepath.Join(dir, "docs.json"))
	cfg.AIFile = getenv("EMTDS_AI_FILE", filepath.Join(dir, "ai.json"))
	cfg.AIDir = getenv("EMTDS_AI_DIR", filepath.Join(dir, "assistant"))
	cfg.UpdateDir = getenv("EMTDS_UPDATE_DIR", cron.UpdateDir(f))
	cfg.UpdateInboxDir = config.Env("EMTDS_UPDATE_INBOX_DIR")
	landingsDir := getenv("EMTDS_LANDINGS_DIR", filepath.Join(dir, "landings"))
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
	cfg.UsersFile = getenv("EMTDS_USERS_FILE", filepath.Join(dir, "users.json"))
	log.Info("users file", "path", cfg.UsersFile)
	cfg.RolesFile = getenv("EMTDS_ROLES_FILE", filepath.Join(dir, "roles.json"))
	log.Info("roles file", "path", cfg.RolesFile)
}

// Bounds of the domain watch interval, set by EMTDS_DOMAIN_WATCH.
const (
	domainWatchDefault = 5 * time.Minute
	domainWatchMin     = time.Minute
)

// domainWatchInterval reads the interval of the domain watch.
// raw is the value of EMTDS_DOMAIN_WATCH, a Go duration such as 5m. An empty or invalid value
// gives five minutes and a value under a minute gives a minute; log receives a warning for a
// value that is invalid or raised.
func domainWatchInterval(raw string, log *slog.Logger) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return domainWatchDefault
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	switch {
	case err != nil:
		log.Warn("EMTDS_DOMAIN_WATCH is not a duration, using the default", "default", domainWatchDefault)
		return domainWatchDefault
	case d < domainWatchMin:
		log.Warn("EMTDS_DOMAIN_WATCH is below the minimum, using it", "minimum", domainWatchMin)
		return domainWatchMin
	}
	return d
}

// connectRedis moves sessions and the login rate limit to Redis when EMTDS_REDIS_ADDR is set.
// cfg is updated in place. A connection failure is logged and the in-memory store stays.
func connectRedis(cfg *admin.Config, log *slog.Logger) {
	a := config.Env("EMTDS_REDIS_ADDR")
	if a == "" {
		return
	}
	rdb, err := store.OpenRedisWaiting(a, config.Env("EMTDS_REDIS_PASSWORD"), 0, store.RedisTimeouts{})
	if err != nil {
		log.Warn("redis not connected, using in-memory sessions", "err", err)
		return
	}
	cfg.Sessions = store.NewRedisSessions(rdb)
	cfg.Limiter = limiterAdapter{c: store.NewCounters(rdb)}
	cfg.LoginRateLimitShared = true
	log.Info("redis connected", "addr", a)
}

// connectClickHouse wires statistics and the warehouse when EMTDS_CLICKHOUSE_ADDR is set.
// The statistics queries get the limits of store.AnalyticsLimitsFromEnv.
// cfg is updated in place. A connection failure is logged and statistics stay disabled.
func connectClickHouse(cfg *admin.Config, log *slog.Logger) {
	a := config.Env("EMTDS_CLICKHOUSE_ADDR")
	if a == "" {
		return
	}
	ch, err := store.OpenCHWaiting(a, getenv("EMTDS_CLICKHOUSE_DB", config.DefaultClickHouseDB),
		getenv("EMTDS_CLICKHOUSE_USER", config.DefaultClickHouseUser), config.Env("EMTDS_CLICKHOUSE_PASSWORD"))
	if err != nil {
		log.Warn("clickhouse not connected, stats disabled", "err", err)
		return
	}
	lim := store.AnalyticsLimitsFromEnv()
	ch.SetAnalyticsLimits(lim)
	cfg.Stats = ch
	cfg.Warehouse = ch
	log.Info("clickhouse connected", "addr", a)
	log.Info("clickhouse analytics limits", "max_execution", lim.MaxExecution.String(), "priority", lim.Priority,
		"max_threads", lim.MaxThreads, "max_memory_mb", lim.MaxMemory>>20)
}

// startGeo opens the geo databases at config.GeoFilePaths() and keeps them
// reloaded until ctx ends. cfg is updated in place; nothing happens when no
// path is set. A database that cannot be loaded yet is logged and watched, so
// a later file replacement is picked up.
func startGeo(ctx context.Context, cfg *admin.Config, log *slog.Logger) {
	gf := config.GeoFilePaths()
	cfg.GeoCityPath, cfg.GeoASNPath, cfg.GeoDBIPPath = gf.City, gf.ASN, gf.DBIPASN
	if gf == (config.GeoFiles{}) {
		return
	}
	log.Info("geo paths", "city", gf.City, "asn", gf.ASN, "dbip_asn", gf.DBIPASN)
	db, err := geo.OpenWith(geo.Paths(gf), log)
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

// runRecovery prints one recovery code and stores only its hash.
// w receives the code. args must be empty: the command does not take a new
// password. The login is EMTDS_RECOVERY_LOGIN, or EMTDS_ADMIN_USER when that
// is empty. The code file is written beside EMTDS_ADMIN_PASSWORD_FILE.
// It returns 0 after the code is printed, or 1 when the file was not written.
func runRecovery(w io.Writer, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "recovery does not take a password or arguments")
		return 1
	}
	login := getenv("EMTDS_RECOVERY_LOGIN", "")
	if login == "" {
		login = getenv("EMTDS_ADMIN_USER", "admin")
	}
	if err := admin.IssueRecoveryCode(w, login, config.Env("EMTDS_ADMIN_PASSWORD_FILE"), time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "recovery code was not created: %v\n", err)
		return 1
	}
	return 0
}

func getenv(k, def string) string {
	if v := config.Env(k); v != "" {
		return v
	}
	return def
}

// keitaroSyncFileForGroups returns the catch-up cursor path for a groups file directory.
func keitaroSyncFileForGroups(groupsFile string) string {
	return getenv("EMTDS_KEITARO_SYNC_FILE", filepath.Join(filepath.Dir(groupsFile), "keitaro-sync.json"))
}
