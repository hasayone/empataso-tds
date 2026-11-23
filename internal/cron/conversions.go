package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// How one conversions run treats the profit summary. The new-conversion
// messages are sent either way.
const (
	// convSummaryFollow sends the summary on every interval run. In daily mode
	// it sends only when the stored next run is already due.
	convSummaryFollow = iota
	// convSummaryForce sends the summary on this run. Run now uses it, and so
	// does the daily clock when that clock has been reached.
	convSummaryForce
	// convSummarySkip announces new conversions and does not send the summary.
	convSummarySkip
)

// runConversions runs the conversions job as an interval does: new conversions
// and one profit summary in the same run.
// cfg is the cron configuration. Returns a short status and a store or
// Telegram error. A missing store or an unconfigured bot is an error and
// sends nothing.
func (r *Runner) runConversions(ctx context.Context, cfg Config) (string, error) {
	return r.announceConversions(ctx, cfg, convSummaryFollow)
}

// announceConversions announces postbacks recorded since the last run and,
// when the summary mode asks for it, the payout sum for the configured period.
// cfg is the cron configuration. summary is convSummaryFollow, convSummaryForce
// or convSummarySkip. Skip still announces each new conversion and does not
// send the profit line. A first run does not replay old conversions one by
// one. Returns a short status and a store or Telegram error. A missing store
// or an unconfigured bot is an error and sends nothing.
func (r *Runner) announceConversions(ctx context.Context, cfg Config, summary int) (string, error) {
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
	if !convSendSummary(r, cfg, summary, to) {
		if first {
			r.state(func(st *Status) { st.ConvSince = to })
			return "watching for new conversions", nil
		}
		return fmt.Sprintf("%d new conversion(s)", nRows), nil
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

// convSendSummary reports whether this run should send the profit summary.
// summary is the mode chosen by the scheduler. to is the run clock, truncated
// to a second. Follow sends on an interval, and in daily mode only when the
// stored next run is already due. Force sends. Skip does not. It does not fail.
func convSendSummary(r *Runner, cfg Config, summary int, to time.Time) bool {
	switch summary {
	case convSummarySkip:
		return false
	case convSummaryForce:
		return true
	default:
		if !cfg.Conversions.Daily() {
			return true
		}
		var next time.Time
		r.state(func(st *Status) { next = st.Jobs[JobConversions].NextRun })
		return !next.IsZero() && !to.Before(next)
	}
}

// sendProfitSummary sends the period payout once.
// cfg supplies the period, the optional daily template and the Telegram chat.
// The amount is the payout sum of the conversion journal for that window, the
// same sum the report adds up, with no Keitaro-import host left out. A daily
// schedule with a summary template sends that text with its summary tokens
// filled, instead of the built report. An empty template and an amount sample
// keep the built report.
// Returns the status phrase (profit and the period id) and a query or Telegram
// error. It notifies once.
func (r *Runner) sendProfitSummary(ctx context.Context, cfg Config) (string, error) {
	from, to := convPeriod(cfg.Conversions, r.now())
	// The store caps a page at 1000 rows. profit is the sum of the whole
	// period, including rows past that page. The lines below use the page.
	rows, profit, err := r.pb.PostbacksExcluding(ctx, from, to, "", 1000, "")
	if err != nil {
		return "", err
	}
	lang := summaryLang(r.paths.Groups)
	text := profitSummaryText(cfg.Conversions.Period, from, to, profit, rows, lang)
	if tpl, ok := storedSummaryTemplate(cfg); ok {
		text = fillConvSummary(tpl, cfg.Conversions.Period, from, to, profit, rows, lang)
	}
	if err := r.notify(ctx, cfg.Telegram, text); err != nil {
		return "", err
	}
	return fmt.Sprintf("profit %.2f", profit), nil
}

// summaryBody picks the Telegram text of the profit summary before tokens are filled.
// cfg is the cron configuration. report is the text profitSummaryText built.
// In daily mode a non-empty summary template that is not an amount sample
// replaces that report. Any other schedule, an empty template and a stored
// amount sample keep report, so the per-conversion template is not used here.
// It does not fail.
func summaryBody(cfg Config, report string) string {
	if text, ok := storedSummaryTemplate(cfg); ok {
		return text
	}
	return report
}

// storedSummaryTemplate is the daily text the operator saved.
// cfg is the cron configuration. The text is trimmed. ok is false when the
// schedule is not daily, the text is empty, or it is an amount sample.
// It does not fail.
func storedSummaryTemplate(cfg Config) (string, bool) {
	if !cfg.Conversions.Daily() {
		return "", false
	}
	text := strings.TrimSpace(cfg.Conversions.SummaryTemplate)
	if text == "" || convSummarySample(text) {
		return "", false
	}
	return text, true
}

// convSummarySamples are amount placeholders, not a summary template.
// A stored copy keeps the report the job builds.
var convSummarySamples = []string{
	"Earned today\n12.50 $\n\nCampaigns\n— 12.50 $\n\nStreams\n— 12.50 $\n\nCountries\n— 12.50 $\n\nDevices\n— 12.50 $\n\nKeywords\n— 12.50 $",
	"Профит за сегодня\n12.50 $\n\nКампании\n— 12.50 $\n\nПотоки\n— 12.50 $\n\nСтраны\n— 12.50 $\n\nУстройства\n— 12.50 $\n\nКлючи\n— 12.50 $",
	"Профіт за сьогодні\n12.50 $\n\nКампанії\n— 12.50 $\n\nПотоки\n— 12.50 $\n\nКраїни\n— 12.50 $\n\nПристрої\n— 12.50 $\n\nКлючі\n— 12.50 $",
}

// convSummarySample reports whether text is an amount placeholder.
// Surrounding space and CR LF are ignored. An empty text is not a sample.
// It does not fail.
func convSummarySample(text string) bool {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return false
	}
	for _, sample := range convSummarySamples {
		if text == sample {
			return true
		}
	}
	return false
}

// fillConvSummary substitutes the daily summary tokens in tpl.
// period is the report period id. from and to label a custom range.
// profit is the payout sum of the whole period. rows are the conversion page.
// lang is ru, uk or en.
// [PERIOD] is the period phrase. [PROFIT] is that sum written like report
// revenue. [CAMPAIGNS], [STREAMS], [COUNTRIES], [DEVICES] and [KEYWORDS] are
// the lines of those sections, without the section title. A token this report
// does not fill is left as written. It does not fail.
func fillConvSummary(tpl, period string, from, to time.Time, profit float64, rows []store.PostbackRow, lang string) string {
	return strings.NewReplacer(
		"[PERIOD]", convPeriodLabel(period, from, to, lang),
		"[PROFIT]", convMoney(profit),
		"[CAMPAIGNS]", convSectionLines(func(p store.PostbackRow) string { return strings.TrimSpace(p.Group) }, rows, lang),
		"[STREAMS]", convSectionLines(func(p store.PostbackRow) string { return strings.TrimSpace(p.Stream) }, rows, lang),
		"[COUNTRIES]", convSectionLines(func(p store.PostbackRow) string {
			return strings.ToUpper(strings.TrimSpace(p.Country))
		}, rows, lang),
		"[DEVICES]", convSectionLines(func(p store.PostbackRow) string {
			return convDeviceLabel(p.Device, langSlot(lang))
		}, rows, lang),
		"[KEYWORDS]", convSectionLines(func(p store.PostbackRow) string { return strings.TrimSpace(p.Keyword) }, rows, lang),
	).Replace(tpl)
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

// conversionText fills one conversion into the Telegram template.
//
// tpl is the template. p is the postback row. The string is the message text.
// [PROFIT] is this conversion's payout. [CAMPAIGN] and [GROUP] are the campaign
// name stored as the group name. [KEY] is the keyword. [SUBID] and [CID] are
// the click id. A token with no field is left as written. There is no error.
func conversionText(tpl string, p store.PostbackRow) string {
	campaign := p.Group
	return strings.NewReplacer(
		"[PROFIT]", strconv.FormatFloat(p.Profit, 'f', 2, 64),
		"[CAMPAIGN]", campaign, "[GROUP]", campaign,
		"[STREAM]", p.Stream,
		"[COUNTRY]", strings.ToUpper(p.Country), "[CITY]", p.City,
		"[DEVICE]", p.Device, "[DOMAIN]", p.Domain,
		"[KEY]", p.Keyword,
		"[SUBID]", p.CID, "[CID]", p.CID,
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
	switch c.Schedule {
	case "", "interval", "daily":
	default:
		return fmt.Errorf("conversions: unknown schedule %q", c.Schedule)
	}
	if c.AtHour < 0 || c.AtHour > 23 || c.AtMinute < 0 || c.AtMinute > 59 {
		return errors.New("conversions: time of day is not a clock time")
	}
	return nil
}

// Daily reports whether the profit summary runs at a clock time.
// An empty schedule and "interval" are the interval. It does not fail.
func (c Conversions) Daily() bool { return c.Schedule == "daily" }

// NextSummary is the next moment the profit summary should be sent.
// now is the clock. interval is the job period, used when the schedule is
// not daily. In daily mode the result is the next AtHour:AtMinute strictly
// after now in Timezone; a time equal to now belongs to the next day. An
// empty or unknown zone is UTC. It does not fail.
func (c Conversions) NextSummary(now time.Time, interval time.Duration) time.Time {
	if c.Daily() {
		return c.nextDaily(now)
	}
	if interval < 0 {
		interval = 0
	}
	return now.Add(interval)
}

// nextDaily is the next AtHour:AtMinute strictly after now in the period zone.
// Seconds are zero. It does not fail.
func (c Conversions) nextDaily(now time.Time) time.Time {
	loc := convLocation(c.Timezone)
	local := now.In(loc)
	slot := time.Date(local.Year(), local.Month(), local.Day(), c.AtHour, c.AtMinute, 0, 0, loc)
	if !slot.After(local) {
		slot = slot.AddDate(0, 0, 1)
	}
	return slot
}

// onClock reports whether t is exactly AtHour:AtMinute:00 in the period zone.
// A zero t is not. It does not fail.
func (c Conversions) onClock(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	local := t.In(convLocation(c.Timezone))
	return local.Hour() == c.AtHour && local.Minute() == c.AtMinute && local.Second() == 0 && local.Nanosecond() == 0
}

// placeConversions points the conversions job at the schedule that is switched on.
// now is the tick clock. js is the stored status. The returned status has NextRun
// rewritten when the stored instant belongs to the other mode. changed is false
// when NextRun was already right. A daily clock that is already due stays, so a
// missed time still runs. An interval next run that lies further ahead than one
// interval is pulled back to now plus that interval. It does not fail.
func (c Config) placeConversions(now time.Time, js JobStatus) (JobStatus, bool) {
	if c.Conversions.Daily() {
		if js.NextRun.IsZero() || !c.Conversions.onClock(js.NextRun) {
			next := c.Conversions.nextDaily(now)
			if js.NextRun.Equal(next) {
				return js, false
			}
			js.NextRun = next
			return js, true
		}
		return js, false
	}
	iv := c.Interval(JobConversions)
	if !js.NextRun.IsZero() && js.NextRun.After(now.Add(iv)) {
		js.NextRun = now.Add(iv)
		return js, true
	}
	return js, false
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

// summaryLang is the language of the conversion summary: ru, uk or en.
// groupsPath is the groups file; the profile is read beside it. An empty path,
// a missing profile and a profile without a language use alertLang.
// It does not fail.
func summaryLang(groupsPath string) string {
	if lang := profileLang(groupsPath); lang != "" {
		return lang
	}
	return alertLang()
}

// profileLang is the platform language of the account the summary is sent for.
// groupsPath is the groups file. The environment administrator's language is
// admin.json in the same directory (the password file lives there too).
// When that file has no language, enabled users in users.json are used: one
// shared language, or the language shared by administrators. Anything else,
// including a path in the working directory, is empty. It does not fail.
func profileLang(groupsPath string) string {
	groupsPath = strings.TrimSpace(groupsPath)
	if groupsPath == "" {
		return ""
	}
	dir := filepath.Dir(groupsPath)
	if dir == "" || dir == "." {
		return ""
	}
	if lang := jsonLang(filepath.Join(dir, "admin.json")); lang != "" {
		return lang
	}
	return usersLang(filepath.Join(dir, "users.json"))
}

// jsonLang reads the lang field of one JSON object.
// path is the file. A missing file, broken JSON and a language other than
// ru, en or uk are empty. It does not fail.
func jsonLang(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var rec struct {
		Lang string `json:"lang"`
	}
	if json.Unmarshal(b, &rec) != nil {
		return ""
	}
	return normLang(rec.Lang)
}

// usersLang is the shared profile language of enabled accounts.
// path is users.json. Accounts without ru, en or uk are skipped. When the
// remaining languages disagree, the administrators' language is used if they
// agree. Otherwise the result is empty. A missing or broken file is empty.
// It does not fail.
func usersLang(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var users []struct {
		Type    string `json:"type"`
		Enabled bool   `json:"enabled"`
		Prefs   struct {
			Lang string `json:"lang"`
		} `json:"prefs"`
	}
	if json.Unmarshal(b, &users) != nil {
		return ""
	}
	var all, admins []string
	for _, u := range users {
		if !u.Enabled {
			continue
		}
		lang := normLang(u.Prefs.Lang)
		if lang == "" {
			continue
		}
		all = append(all, lang)
		if strings.EqualFold(u.Type, "ADMIN") {
			admins = append(admins, lang)
		}
	}
	if lang, ok := oneLang(all); ok {
		return lang
	}
	if lang, ok := oneLang(admins); ok {
		return lang
	}
	return ""
}

// oneLang reports whether every item is the same language.
// langs may be empty. ok is false when the slice is empty or the items differ.
// It does not fail.
func oneLang(langs []string) (string, bool) {
	if len(langs) == 0 {
		return "", false
	}
	for _, lang := range langs[1:] {
		if lang != langs[0] {
			return "", false
		}
	}
	return langs[0], true
}

// normLang accepts ru, en and uk, in any letter case.
// Anything else, including empty, is empty. It does not fail.
func normLang(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ru", "en", "uk":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

// convMoney is a payout written the way the reports write revenue.
// profit is the amount. A conversion has no currency column. An empty code
// is USD, and the reports put `$` after the number. The result is two digits
// and that sign. It does not fail.
func convMoney(profit float64) string {
	return strconv.FormatFloat(profit, 'f', 2, 64) + " $"
}

// convSummaryLines is how many values one section of the summary lists.
// The rest of that section becomes one remainder line.
const convSummaryLines = 5

// profitSummaryText is the Telegram report of the period payout.
// period is the report period id. from and to label a custom range.
// profit is the payout sum of the whole period, not the sum of rows.
// rows are the conversions of the period page; a section is omitted when
// every row lacks that field. lang is ru, uk or en. It does not fail.
func profitSummaryText(period string, from, to time.Time, profit float64, rows []store.PostbackRow, lang string) string {
	amount := convMoney(profit)
	label := convPeriodLabel(period, from, to, lang)
	var b strings.Builder
	switch lang {
	case "ru":
		b.WriteString("Профит за " + label)
	case "uk":
		b.WriteString("Профіт за " + label)
	default:
		b.WriteString("Earned " + label)
	}
	b.WriteByte('\n')
	b.WriteString(amount)
	for _, block := range convSummaryBlocks(rows, lang) {
		b.WriteString("\n\n")
		b.WriteString(block)
	}
	return b.String()
}

// convSummaryBlocks lists the non-empty parts of a period: campaign, stream,
// country, device and keyword. rows are the conversion page. lang is ru, uk
// or en. A field that is empty on every row is left out. There is no separate
// group: the campaign name is the group name stored on the conversion.
// It does not fail.
func convSummaryBlocks(rows []store.PostbackRow, lang string) []string {
	n := langSlot(lang)
	type dim struct {
		title string
		pick  func(store.PostbackRow) string
	}
	dims := []dim{
		{[...]string{"Campaigns", "Кампании", "Кампанії"}[n], func(p store.PostbackRow) string { return strings.TrimSpace(p.Group) }},
		{[...]string{"Streams", "Потоки", "Потоки"}[n], func(p store.PostbackRow) string { return strings.TrimSpace(p.Stream) }},
		{[...]string{"Countries", "Страны", "Країни"}[n], func(p store.PostbackRow) string {
			return strings.ToUpper(strings.TrimSpace(p.Country))
		}},
		{[...]string{"Devices", "Устройства", "Пристрої"}[n], func(p store.PostbackRow) string {
			return convDeviceLabel(p.Device, n)
		}},
		{[...]string{"Keywords", "Ключи", "Ключі"}[n], func(p store.PostbackRow) string { return strings.TrimSpace(p.Keyword) }},
	}
	var out []string
	for _, d := range dims {
		if block := convDimBlock(d.title, d.pick, rows, lang); block != "" {
			out = append(out, block)
		}
	}
	return out
}

// convDimBlock is one section: a title and the largest amounts.
// title is already in lang. pick reads one field; an empty value is skipped.
// rows are the conversion page. At most convSummaryLines names are listed,
// largest amount first, and the rest is one line. An empty section is empty.
// It does not fail.
func convDimBlock(title string, pick func(store.PostbackRow) string, rows []store.PostbackRow, lang string) string {
	body := convSectionLines(pick, rows, lang)
	if body == "" {
		return ""
	}
	return title + "\n" + body
}

// convSectionLines lists one summary section without its title.
// pick reads one field; an empty value is skipped. rows are the conversion
// page. lang chooses the remainder line. At most convSummaryLines names are
// listed, largest amount first, and the rest is one line. An empty section
// is an empty string. It does not fail.
func convSectionLines(pick func(store.PostbackRow) string, rows []store.PostbackRow, lang string) string {
	sums := map[string]float64{}
	for _, row := range rows {
		name := pick(row)
		if name == "" {
			continue
		}
		sums[name] += row.Profit
	}
	if len(sums) == 0 {
		return ""
	}
	type line struct {
		name string
		sum  float64
	}
	lines := make([]line, 0, len(sums))
	for name, sum := range sums {
		lines = append(lines, line{name, sum})
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].sum != lines[j].sum {
			return lines[i].sum > lines[j].sum
		}
		return lines[i].name < lines[j].name
	})
	var b strings.Builder
	shown := lines
	if len(shown) > convSummaryLines {
		shown = lines[:convSummaryLines]
	}
	for i, ln := range shown {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(ln.name)
		b.WriteString(" — ")
		b.WriteString(convMoney(ln.sum))
	}
	if rest := lines[len(shown):]; len(rest) > 0 {
		sum := 0.0
		for _, ln := range rest {
			sum += ln.sum
		}
		b.WriteByte('\n')
		b.WriteString(convMoreLine(len(rest), sum, lang))
	}
	return b.String()
}

// convMoreLine is the remainder of a section that does not fit.
// n is how many names were left out. sum is their payout. lang is ru, uk or en.
// It does not fail.
func convMoreLine(n int, sum float64, lang string) string {
	amount := convMoney(sum)
	switch lang {
	case "ru":
		return fmt.Sprintf("ещё %d — %s", n, amount)
	case "uk":
		return fmt.Sprintf("ще %d — %s", n, amount)
	default:
		return fmt.Sprintf("and %d more — %s", n, amount)
	}
}

// langSlot is the index of lang in a three-string label: en, ru, uk.
// Anything other than ru and uk is English. It does not fail.
func langSlot(lang string) int {
	switch lang {
	case "ru":
		return 1
	case "uk":
		return 2
	default:
		return 0
	}
}

// convDeviceLabel is the device name in the summary language.
// device is the stored value. slot is langSlot. phone, computer and tablet
// are translated; any other non-empty value is returned trimmed. An empty
// device stays empty. It does not fail.
func convDeviceLabel(device string, slot int) string {
	switch strings.ToLower(strings.TrimSpace(device)) {
	case "phone":
		return [...]string{"phone", "телефон", "телефон"}[slot]
	case "computer", "desktop":
		return [...]string{"computer", "компьютер", "комп'ютер"}[slot]
	case "tablet":
		return [...]string{"tablet", "планшет", "планшет"}[slot]
	default:
		return strings.TrimSpace(device)
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
