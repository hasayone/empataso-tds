package cron

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/store"
)

// TestSummaryBody checks which text the daily summary sends.
// A daily template replaces the built report. An empty template and an
// interval schedule keep the report.
func TestSummaryBody(t *testing.T) {
	report := "Профит за сегодня\n1.00 $"
	cfg := Config{Conversions: Conversions{Schedule: "daily", SummaryTemplate: "  Сводка  "}}
	if got := summaryBody(cfg, report); got != "Сводка" {
		t.Fatalf("daily template = %q", got)
	}
	cfg.Conversions.SummaryTemplate = "  "
	if got := summaryBody(cfg, report); got != report {
		t.Fatalf("empty template = %q", got)
	}
	cfg.Conversions.SummaryTemplate = "Сводка"
	cfg.Conversions.Schedule = "interval"
	if got := summaryBody(cfg, report); got != report {
		t.Fatalf("interval schedule = %q", got)
	}
}

// TestDailySummarySendsStoredText checks that a daily run sends the stored
// summary and leaves the per-conversion template unused for that message.
func TestDailySummarySendsStoredText(t *testing.T) {
	e := newEnv(t)
	pb := &fakePB{}
	e.r.pb = pb
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return clock }
	pb.rows = []store.PostbackRow{{TS: clock.Add(-time.Hour), Profit: 9, Group: "Promo"}}
	cfg := Default()
	cfg.Telegram = tg
	cfg.Conversions.Schedule = "daily"
	cfg.Conversions.Template = "[PROFIT] [GROUP]"
	cfg.Conversions.SummaryTemplate = "Сводка дня"
	if _, err := e.r.announceConversions(context.Background(), cfg, convSummaryForce); err != nil {
		t.Fatal(err)
	}
	if m := e.messages(); len(m) != 1 || m[0] != "Сводка дня" {
		t.Fatalf("messages = %q", m)
	}
}

// TestFillConvSummary checks that a saved daily template is filled from the
// same sections as the built report, and that an unknown token stays.
func TestFillConvSummary(t *testing.T) {
	rows := []store.PostbackRow{{
		Group: "Promo", Stream: "Main", Country: "us", Device: "phone", Keyword: "bet", Profit: 4,
	}}
	tpl := "Профит за [PERIOD]\n[PROFIT]\n\nКампании\n[CAMPAIGNS]\n\nПотоки\n[STREAMS]\n\nСтраны\n[COUNTRIES]\n\nУстройства\n[DEVICES]\n\nКлючи\n[KEYWORDS]"
	want := profitSummaryText("today", time.Time{}, time.Time{}, 4, rows, "ru")
	got := fillConvSummary(tpl+"\n[CITY]", "today", time.Time{}, time.Time{}, 4, rows, "ru")
	if got != want+"\n[CITY]" {
		t.Fatalf("filled\n%s\nreport\n%s", got, want)
	}
}

// TestDailySummaryFillsVariables checks that a daily run substitutes the
// summary tokens and does not send them as written.
func TestDailySummaryFillsVariables(t *testing.T) {
	e := newEnv(t)
	pb := &fakePB{}
	e.r.pb = pb
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return clock }
	pb.rows = []store.PostbackRow{{TS: clock.Add(-time.Hour), Profit: 9, Group: "Promo"}}
	cfg := Default()
	cfg.Telegram = tg
	cfg.Conversions.Schedule = "daily"
	cfg.Conversions.SummaryTemplate = "Сумма [PROFIT]\n[CAMPAIGNS]"
	if _, err := e.r.announceConversions(context.Background(), cfg, convSummaryForce); err != nil {
		t.Fatal(err)
	}
	if m := e.messages(); len(m) != 1 || m[0] != "Сумма 9.00 $\nPromo — 9.00 $" {
		t.Fatalf("messages = %q", m)
	}
}

// TestDailySummaryIgnoresAmountSample checks that a stored amount placeholder
// is not the Telegram text. The job sends the report it builds.
func TestDailySummaryIgnoresAmountSample(t *testing.T) {
	e := newEnv(t)
	pb := &fakePB{}
	e.r.pb = pb
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return clock }
	pb.rows = []store.PostbackRow{{TS: clock.Add(-time.Hour), Profit: 9, Group: "Promo"}}
	cfg := Default()
	cfg.Telegram = tg
	cfg.Conversions.Schedule = "daily"
	cfg.Conversions.SummaryTemplate = convSummarySamples[1]
	if _, err := e.r.announceConversions(context.Background(), cfg, convSummaryForce); err != nil {
		t.Fatal(err)
	}
	m := e.messages()
	if len(m) != 1 || strings.Contains(m[0], "12.50") || !strings.Contains(m[0], "Promo — 9.00 $") {
		t.Fatalf("messages = %q", m)
	}
}
