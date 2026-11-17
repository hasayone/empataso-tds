package cron

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

// maxAnnounce bounds one run: a backlog after downtime becomes a summary, not
// a flood.
const maxAnnounce = 20

// convPeriodIDs are the report period ids the profit summary reuses.
var convPeriodIDs = map[string]bool{
	"today": true, "yesterday": true, "week": true, "7d": true,
	"month": true, "prevmonth": true, "30d": true, "year": true,
	"all": true, "dates": true, "times": true,
}

// runConversions announces postbacks recorded since the last run and, once
// in the same run, the payout sum for the configured report period.
// cfg is the cron configuration. The summary goes through the same Telegram
// channel as each conversion. A first run does not replay old conversions
// one by one, but it does send the period profit. Returns a short status
// and a store or Telegram error. A missing store or an unconfigured bot is
// an error and sends nothing.
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
	first := from.IsZero()
	sent := 0
	nRows := 0
	var lastOK time.Time
	if !first {
		exclude := r.keitaroExcludeHost()
		rows, _, err := r.pb.PostbacksExcluding(ctx, from, to, "", 1000, exclude)
		if err != nil {
			return "", err
		}
		nRows = len(rows)
		tpl := cfg.Conversions.Template
		if strings.TrimSpace(tpl) == "" {
			tpl = DefaultConversionTemplate
		}
		// rows come newest first; announce in the order they happened.
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
		// Every new line of this window is out. Move the mark before the
		// summary so a later Telegram failure does not send those lines again.
		r.state(func(st *Status) { st.ConvSince = to })
	}
	msg, err := r.sendProfitSummary(ctx, cfg)
	if err != nil {
		if first {
			return "", err
		}
		return fmt.Sprintf("%d new conversion(s)", nRows), err
	}
	if first {
		r.state(func(st *Status) { st.ConvSince = to })
		return msg + "; watching for new conversions", nil
	}
	return fmt.Sprintf("%d new conversion(s); %s", nRows, msg), nil
}

// sendProfitSummary sends the period payout once.
// cfg supplies the period and the Telegram chat. The amount is the payout
// sum of the conversion journal for that window, the same sum the report
// adds up, with no Keitaro-import host left out. Returns the status phrase
// (profit and the period id) and a query or Telegram error. It notifies once.
func (r *Runner) sendProfitSummary(ctx context.Context, cfg Config) (string, error) {
	from, to := convPeriod(cfg.Conversions, r.now())
	_, profit, err := r.pb.PostbacksExcluding(ctx, from, to, "", 1, "")
	if err != nil {
		return "", err
	}
	text := profitSummaryText(cfg.Conversions.Period, from, to, profit, alertLang())
	if err := r.notify(ctx, cfg.Telegram, text); err != nil {
		return "", err
	}
	return fmt.Sprintf("profit %.2f", profit), nil
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

// validate checks the profit period. An empty period is today. dates and
// times need both ends, with the end after the start. An unknown period id
// or an unknown time zone is an error. It does not change c.
func (c Conversions) validate() error {
	if c.Period != "" && !convPeriodIDs[c.Period] {
		return fmt.Errorf("conversions: unknown period %q", c.Period)
	}
	if strings.TrimSpace(c.Timezone) != "" {
		if _, err := time.LoadLocation(convZoneName(c.Timezone)); err != nil {
			return errors.New("conversions: unknown timezone")
		}
	}
	if c.Period == "dates" || c.Period == "times" {
		loc := convLocation(c.Timezone)
		from, to, ok := convCustomSpan(c.From, c.To, loc)
		if !ok || !to.After(from) {
			return errors.New("conversions: period needs both ends")
		}
	}
	return nil
}

// convPeriod is the half-open window [from, to) of the report period at now.
// c is the conversions section. An empty or unknown id is today. dates and
// times use the stored ends; a pair that does not parse falls back to today.
// It does not fail.
func convPeriod(c Conversions, now time.Time) (from, to time.Time) {
	loc := convLocation(c.Timezone)
	id := c.Period
	if id == "" || !convPeriodIDs[id] {
		id = "today"
	}
	if id == "dates" || id == "times" {
		a, b, ok := convCustomSpan(c.From, c.To, loc)
		if ok && b.After(a) {
			return a, b
		}
		id = "today"
	}
	return convNamedSpan(id, now, loc)
}

// convNamedSpan is one named report period at now in loc.
// id is a convPeriodIDs key other than dates and times. now is the clock.
// The end of today, week, month, year, 7d, 30d and all is now. It does not fail.
func convNamedSpan(id string, now time.Time, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch id {
	case "yesterday":
		return today.AddDate(0, 0, -1), today
	case "week":
		wd := int(now.Weekday())
		if wd == 0 {
			wd = 7
		}
		return today.AddDate(0, 0, -(wd - 1)), now
	case "7d":
		return now.Add(-7 * 24 * time.Hour), now
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc), now
	case "prevmonth":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return start.AddDate(0, -1, 0), start
	case "30d":
		return now.Add(-30 * 24 * time.Hour), now
	case "year":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc), now
	case "all":
		return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), now
	default:
		return today, now
	}
}

// convCustomSpan parses the two ends of a dates or times period.
// fromRaw and toRaw are RFC3339, a local timestamp, or a calendar date.
// loc is the zone of a timestamp that has no offset. A date-only end is the
// next midnight, so that day is inside the window. ok is false when either
// end is missing or not a time.
func convCustomSpan(fromRaw, toRaw string, loc *time.Location) (from, to time.Time, ok bool) {
	from, okFrom := parseConvBound(fromRaw, loc, false)
	to, okTo := parseConvBound(toRaw, loc, true)
	if !okFrom || !okTo {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// parseConvBound reads one end of a custom period.
// raw is the stored text. loc is the zone for a value without an offset.
// end is true for the right edge: a bare date then means the next midnight.
// ok is false when raw is empty or not a time.
func parseConvBound(raw string, loc *time.Location, end bool) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.UTC
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, true
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
		if end {
			return t.AddDate(0, 0, 1), true
		}
		return t, true
	}
	return time.Time{}, false
}

// convLocation loads the summary time zone.
// name is an IANA id; empty and an unknown name are UTC. Europe/Kiev is
// Europe/Kyiv when that zone loads. It does not fail.
func convLocation(name string) *time.Location {
	name = convZoneName(name)
	if name == "" || name == "UTC" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// convZoneName canonicalises a zone id the panel may store.
// Europe/Kiev becomes Europe/Kyiv. A blank name stays blank. It does not fail.
func convZoneName(name string) string {
	name = strings.TrimSpace(name)
	if name == "Europe/Kiev" {
		return "Europe/Kyiv"
	}
	return name
}

// alertLang is the Telegram language of cron alerts: ru, uk or en.
// It reads EMTDS_ALERT_LANG. Anything else, including empty, is English.
// It does not fail.
func alertLang() string {
	switch l := strings.ToLower(strings.TrimSpace(config.Env("EMTDS_ALERT_LANG"))); l {
	case "ru", "uk":
		return l
	default:
		return "en"
	}
}

// profitSummaryText is the one Telegram line for the period payout.
// period is the report period id. from and to are the window, used when the
// period is a custom range. profit is the payout sum. lang is ru, uk or en.
// It does not fail.
func profitSummaryText(period string, from, to time.Time, profit float64, lang string) string {
	amount := strconv.FormatFloat(profit, 'f', 2, 64)
	label := convPeriodLabel(period, from, to, lang)
	switch lang {
	case "ru":
		return "Профит за " + label + ": " + amount
	case "uk":
		return "Профіт за " + label + ": " + amount
	default:
		return "Profit for " + label + ": " + amount
	}
}

// convPeriodLabel is the period phrase that follows "profit for".
// period is the report period id. from and to label a custom range.
// lang is ru, uk or en; anything else is English. It does not fail.
func convPeriodLabel(period string, from, to time.Time, lang string) string {
	if period == "dates" || period == "times" {
		return from.Format("2006-01-02 15:04") + " — " + to.Format("2006-01-02 15:04")
	}
	labels := map[string][3]string{
		"today":     {"today", "сегодня", "сьогодні"},
		"yesterday": {"yesterday", "вчера", "вчора"},
		"week":      {"this week", "текущую неделю", "поточний тиждень"},
		"7d":        {"the last 7 days", "последние 7 дней", "останні 7 днів"},
		"month":     {"this month", "текущий месяц", "поточний місяць"},
		"prevmonth": {"the previous month", "предыдущий месяц", "попередній місяць"},
		"30d":       {"the last 30 days", "последние 30 дней", "останні 30 днів"},
		"year":      {"this year", "текущий год", "поточний рік"},
		"all":       {"all time", "всё время", "весь час"},
	}
	row, ok := labels[period]
	if !ok {
		row = labels["today"]
	}
	switch lang {
	case "ru":
		return row[1]
	case "uk":
		return row[2]
	default:
		return row[0]
	}
}
