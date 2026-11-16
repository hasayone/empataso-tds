package cron

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/store"
)

// maxAnnounce bounds one run: a backlog after downtime becomes a summary, not
// a flood.
const maxAnnounce = 20

// runConversions announces the postbacks recorded since the last run.
func (r *Runner) runConversions(ctx context.Context, cfg Config) (string, error) {
	if r.pb == nil {
		return "", errors.New("no ClickHouse connection: set EMTDS_CLICKHOUSE_ADDR for the cron service")
	}
	if !cfg.Telegram.Configured() {
		return "", errors.New("telegram is not configured")
	}
	// Whole seconds: a postback's timestamp has second precision, so a window
	// that ends mid-second would skip the ones recorded later in that second.
	to := r.now().Truncate(time.Second)
	var from time.Time
	r.state(func(st *Status) { from = st.ConvSince })
	if from.IsZero() {
		// First run: start from now rather than announce history.
		r.state(func(st *Status) { st.ConvSince = to })
		return "watching for new conversions", nil
	}
	exclude := r.keitaroExcludeHost()
	rows, _, err := r.pb.PostbacksExcluding(ctx, from, to, "", 1000, exclude)
	if err != nil {
		return "", err
	}
	tpl := cfg.Conversions.Template
	if strings.TrimSpace(tpl) == "" {
		tpl = DefaultConversionTemplate
	}
	// rows come newest first; announce in the order they happened.
	sent := 0
	var lastOK time.Time
	for i := len(rows) - 1; i >= 0 && sent < maxAnnounce; i-- {
		row := rows[i]
		if err := r.notify(ctx, cfg.Telegram, conversionText(tpl, row)); err != nil {
			r.advanceConvSince(lastOK)
			return fmt.Sprintf("%d of %d announced", sent, len(rows)), err
		}
		sent++
		lastOK = row.TS
	}
	if rest := len(rows) - sent; rest > 0 {
		sum := 0.0
		for _, p := range rows[:rest] {
			sum += p.Profit
		}
		if err := r.notify(ctx, cfg.Telegram, fmt.Sprintf("… and %d more conversions, %.2f in total.", rest, sum)); err != nil {
			r.advanceConvSince(lastOK)
			return fmt.Sprintf("%d of %d announced", sent, len(rows)), err
		}
	}
	r.state(func(st *Status) { st.ConvSince = to })
	return fmt.Sprintf("%d new conversion(s)", len(rows)), nil
}

// advanceConvSince moves the conversion window past rows already announced.
// lastOK is the timestamp of the last successful message; zero leaves ConvSince.
func (r *Runner) advanceConvSince(lastOK time.Time) {
	if lastOK.IsZero() {
		return
	}
	next := lastOK.Add(time.Second).Truncate(time.Second)
	r.state(func(st *Status) { st.ConvSince = next })
}

func conversionText(tpl string, p store.PostbackRow) string {
	return strings.NewReplacer(
		"[PROFIT]", strconv.FormatFloat(p.Profit, 'f', 2, 64),
		"[GROUP]", p.Group, "[STREAM]", p.Stream,
		"[COUNTRY]", strings.ToUpper(p.Country), "[CITY]", p.City,
		"[DEVICE]", p.Device, "[DOMAIN]", p.Domain, "[CID]", p.CID,
	).Replace(tpl)
}
