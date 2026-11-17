//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

func TestInitialWatermarkIntegration(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	host := "it_wm_" + newITDatabaseName()
	now := time.Now().UTC().Truncate(time.Second)
	evTS := now.Add(-30 * time.Minute)
	convTS := now.Add(-10 * time.Minute)
	if err := ch.InsertEvents(ctx, []logbuf.Event{{
		Ts: evTS, Domain: host, CID: "wm1", GroupName: "g", Stream: "s", Uniq: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ch.InsertConversions(ctx, []ConversionInsert{{
		TS: convTS, Domain: host, CID: "wm1", Status: "sale", GroupName: "g", StreamName: "s", Profit: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	clicks, conv, err := ch.InitialWatermark(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if clicks.Before(evTS) || clicks.After(evTS.Add(time.Second)) {
		t.Fatalf("clicks watermark = %v want ~%v", clicks, evTS)
	}
	if conv.Before(convTS) || conv.After(convTS.Add(time.Second)) {
		t.Fatalf("conversions watermark = %v want ~%v", conv, convTS)
	}
}

func TestInitialWatermarkNoRowsIntegration(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	host := "it_wm_empty_" + newITDatabaseName()
	clicks, conv, err := ch.InitialWatermark(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if !clicks.IsZero() || !conv.IsZero() {
		t.Fatalf("empty host data must yield zero watermarks, got clicks=%v conv=%v", clicks, conv)
	}
}

func TestExistingCIDsIntegration(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	host := "it_cid_" + newITDatabaseName()
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	if err := ch.InsertEvents(ctx, []logbuf.Event{
		{Ts: now, Domain: host, CID: "a", GroupName: "g", Stream: "s", Uniq: 1},
		{Ts: now, Domain: host, CID: "b", GroupName: "g", Stream: "s", Uniq: 1},
		{Ts: now, Domain: "other", CID: "c", GroupName: "g", Stream: "s", Uniq: 1},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	got, err := ch.ExistingCIDs(ctx, host, from, to, []string{"a", "c", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["a"]; !ok {
		t.Fatalf("missing a in %v", got)
	}
	if _, ok := got["c"]; ok {
		t.Fatal("c must not match other domain")
	}
	if _, ok := got["missing"]; ok {
		t.Fatal("missing must not appear")
	}
}

func TestPostbacksExcludeKeitaroDomainIntegration(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	group := "it_ex_" + newITDatabaseName()
	keitaroHost := "keitaro-" + group + ".example"
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	if err := ch.InsertConversions(ctx, []ConversionInsert{
		{TS: now, Domain: keitaroHost, CID: "k1", Status: "sale", GroupName: group, StreamName: "s", Profit: 5},
		{TS: now, Domain: "traffic.example", CID: "k2", Status: "sale", GroupName: group, StreamName: "s", Profit: 3},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	all, _, err := ch.PostbacksExcluding(ctx, from, to, group, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all rows = %d", len(all))
	}
	filtered, profit, err := ch.PostbacksExcluding(ctx, from, to, group, 100, keitaroHost)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].CID != "k2" || profit != 3 {
		t.Fatalf("filtered = %+v profit=%v", filtered, profit)
	}
}

func TestPostbacksFinalDeduplicatesIntegration(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	group := "it_fin_" + newITDatabaseName()
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	cid := "dup_" + group
	if err := ch.InsertConversions(ctx, []ConversionInsert{
		{TS: now, Domain: "d.example", CID: cid, Status: "sale", GroupName: group, StreamName: "s", Profit: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ch.InsertConversions(ctx, []ConversionInsert{
		{TS: now.Add(time.Second), Domain: "d.example", CID: cid, Status: "sale", GroupName: group, StreamName: "s", Profit: 9},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	pb, profit, err := ch.PostbacksExcluding(ctx, from, to, group, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var matches int
	for _, row := range pb {
		if row.CID == cid && row.Status == "sale" {
			matches++
			if row.Profit != 9 {
				t.Fatalf("profit = %v want 9 (newer row)", row.Profit)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("duplicate cid/status rows = %d", matches)
	}
	if profit != 9 {
		t.Fatalf("total profit = %v want 9", profit)
	}
}

func TestPerformanceFinalDeduplicatesIntegration(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	group := "it_perf_" + newITDatabaseName()
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	cid := "perf_dup_" + group
	if err := ch.InsertConversions(ctx, []ConversionInsert{
		{TS: now, Domain: "d.example", CID: cid, Status: "sale", GroupName: group, StreamName: "s1", Profit: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ch.InsertConversions(ctx, []ConversionInsert{
		{TS: now.Add(time.Second), Domain: "d.example", CID: cid, Status: "sale", GroupName: group, StreamName: "s1", Profit: 9},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	perf, err := ch.Performance(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var row *PerfRow
	for i := range perf {
		if perf[i].Group == group && perf[i].Stream == "s1" {
			row = &perf[i]
			break
		}
	}
	if row == nil {
		t.Fatal("performance row missing")
	}
	if row.Conv != 1 || row.Profit != 9 {
		t.Fatalf("performance = %+v want 1 conv and 9 profit", *row)
	}
}
