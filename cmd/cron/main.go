// Command cron — the EmTDS background service: list and geo database
// updates, VirusTotal checks, disk monitoring, keyword cleanup, conversion
// notifications, the Redis memory alert. See internal/cron.
//
// It shares the engine's environment: the same EMTDS_DATA_DIR,
// EMTDS_GROUPS_FILE, EMTDS_KEYS_DIR, EMTDS_GEO_DB, EMTDS_ASN_DB and
// EMTDS_DBIP_ASN_DB, plus EMTDS_CRON_FILE for its own config (edited in the
// admin panel).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/store"
)

func main() {
	// Parse -probe before any initialisation: it must work in a read-only
	// container without ClickHouse or Redis access.
	var probePath string
	flag.StringVar(&probePath, "probe", "", "print cron status probe and exit")
	flag.Parse()
	if probePath != "" {
		os.Exit(runProbe(probePath, time.Now(), os.Stdout, os.Stderr))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	gf := config.GeoFilePaths()
	paths := cron.Paths{
		Config:  getenv("EMTDS_CRON_FILE", "cron.json"),
		DataDir: getenv("EMTDS_DATA_DIR", "./data"),
		KeysDir: getenv("EMTDS_KEYS_DIR", "keys"),
		Groups:  config.Env("EMTDS_GROUPS_FILE"),
		CityDB:  gf.City,
		ASNDB:   gf.ASN,
		DBIPASN: gf.DBIPASN,
	}

	// ClickHouse is only needed to announce conversions.
	var pb cron.PostbackSource
	if a := config.Env("EMTDS_CLICKHOUSE_ADDR"); a != "" {
		ch, err := store.OpenCHWaiting(a, getenv("EMTDS_CLICKHOUSE_DB", config.DefaultClickHouseDB),
			getenv("EMTDS_CLICKHOUSE_USER", config.DefaultClickHouseUser), config.Env("EMTDS_CLICKHOUSE_PASSWORD"))
		if err != nil {
			log.Warn("clickhouse not connected, conversion notifications unavailable", "err", err)
		} else {
			defer ch.Close()
			pb = ch
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := cron.New(paths, pb, log)
	// Redis is only needed for the memory alert; this is the one process that sends it.
	if a := config.Env("EMTDS_REDIS_ADDR"); a != "" {
		probe := store.OpenRedisProbe(a, config.Env("EMTDS_REDIS_PASSWORD"))
		defer probe.Close()
		alert := cron.RedisAlertFromEnv()
		runner.WatchRedis(probe, alert)
		log.Info("redis memory alert on", "addr", a, "percent", alert.Percent, "recover", alert.Recover)
	}

	// The engines write their metrics files; this is the one process that reads them and alerts.
	loss := cron.LossAlertFromEnv()
	runner.WatchLoss(loss)
	log.Info("event loss alert on", "dir", loss.Dir, "recover_after", loss.RecoverAfter.String())

	bi := config.Build()
	log.Info("cron started", "config", paths.Config, "data", paths.DataDir, "version", bi.Version, "commit", bi.Commit)
	runner.Run(ctx, 5*time.Second)
	log.Info("cron stopped")
}

// runProbe reads the cron status at path and writes the probe output to w.
// Returns 0 on success or 1 when the status cannot be read.
// werr receives the error message on failure.
func runProbe(path string, now time.Time, w io.Writer, werr io.Writer) int {
	p, err := cron.ProbeStatus(path, now)
	if err != nil {
		fmt.Fprintln(werr, err.Error())
		return 1
	}
	fmt.Fprint(w, p.Format())
	return 0
}

func getenv(k, def string) string {
	if v := config.Env(k); v != "" {
		return v
	}
	return def
}
