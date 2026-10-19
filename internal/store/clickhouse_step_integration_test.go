//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/logbuf"
)

// stepEvents is one campaign's traffic: two visitors on a landing (step 0) and the
// first of them again on /offer (step 1). offerUniq is the uniqueness flag the
// /offer event carries: 1 with the per-step key, 0 when the offer step shared the
// landing's key.
func stepEvents(group string, now time.Time, offerUniq uint8) []logbuf.Event {
	base := logbuf.Event{Ts: now, GroupID: group, GroupName: group, Stream: "s1"}
	land1, land2, offer1 := base, base, base
	land1.CID, land1.Uniq, land1.LandingID = group+"_a", 1, "L1"
	land2.CID, land2.Uniq, land2.LandingID = group+"_b", 1, "L1"
	offer1.CID, offer1.Uniq, offer1.OfferID, offer1.NetworkID, offer1.Step = group+"_a", offerUniq, "O1", "N1", 1
	return []logbuf.Event{land1, land2, offer1}
}

// pricedPair is one priced landing click and its /offer step. current logs the
// step as the engine does now: no cents, the currency label kept, its own unique.
// Otherwise the offer row copies the price and shares the landing key.
func pricedPair(group string, now time.Time, current bool) []logbuf.Event {
	land := logbuf.Event{Ts: now, GroupID: group, GroupName: group, Stream: "s1", CID: group + "_p",
		Uniq: 1, LandingID: "L2", CostCents: 100, CostCur: "USD"}
	offer := land
	offer.LandingID, offer.OfferID, offer.NetworkID = "", "O2", "N1"
	if current {
		offer.CostCents, offer.Step = 0, 1
		return []logbuf.Event{land, offer}
	}
	offer.Uniq = 0
	return []logbuf.Event{land, offer}
}

// TestCHStepUniques writes campaign traffic with an /offer step and checks that
// the campaign and stream summaries count each visitor once, that the catalog
// facts and the report keep the offer's own unique, and that the unique weights
// of a manual spend equal those of a campaign logged before the offer step had
// its own uniqueness flag.
func TestCHStepUniques(t *testing.T) {
	ch := chFromEnv(t)
	if !ch.stepEvents {
		t.Fatal("step wave is not ready")
	}
	ctx := context.Background()
	group := fmt.Sprintf("itest_step_%d", os.Getpid())
	legacy := group + "_legacy"
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	t.Cleanup(func() { cleanupGroup(t, ch, group); cleanupGroup(t, ch, legacy) })

	events := append(stepEvents(group, now, 1), stepEvents(legacy, now, 0)...)
	events = append(events, pricedPair(group, now, true)...)
	events = append(events, pricedPair(legacy, now, false)...)
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	conds := []Cond{{Field: "campaign_id", Op: "eq", Values: []string{group}}}

	sum, err := ch.SummaryFiltered(ctx, from, to, conds)
	if err != nil || sum.Total != 5 || sum.Unique != 3 {
		t.Fatalf("summary %+v err=%v, want 5 hits and 3 uniques", sum, err)
	}
	series, err := ch.TimeSeriesFiltered(ctx, from, to, 3600, conds)
	if err != nil {
		t.Fatal(err)
	}
	var seriesHits, seriesUniq int64
	for _, p := range series {
		seriesHits += p.Total
		seriesUniq += p.Unique
	}
	if seriesHits != 5 || seriesUniq != 3 {
		t.Fatalf("series hits=%d uniques=%d, want 5 and 3", seriesHits, seriesUniq)
	}
	perf, err := ch.PerformanceFiltered(ctx, from, to, conds)
	if err != nil || len(perf) != 1 || perf[0].Hits != 5 || perf[0].Unique != 3 {
		t.Fatalf("performance %+v err=%v", perf, err)
	}

	facts, err := ch.ClickFactsFiltered(ctx, from, to, conds)
	if err != nil {
		t.Fatal(err)
	}
	uniq := map[string]int64{}
	var uniqFirst int64
	for _, f := range facts.Clicks {
		uniq[f.LandingID+"/"+f.OfferID] += f.Uniq
		uniqFirst += f.UniqFirst
		if f.CostCur != "" && f.CostCents == 0 {
			t.Errorf("fact %+v: a currency key without cents", f)
		}
	}
	if uniq["L1/"] != 2 || uniq["/O1"] != 1 || uniq["L2/"] != 1 || uniq["/O2"] != 1 {
		t.Fatalf("facts unique per object %v, want every landing and offer on its own", uniq)
	}
	if uniqFirst != 3 {
		t.Fatalf("facts uniq_first sums to %d, want the 3 campaign uniques", uniqFirst)
	}
	byOffer := []Cond{conds[0], {Field: "offer_id", Op: "eq", Values: []string{"O1"}}}
	offerSum, err := ch.SummaryFiltered(ctx, from, to, byOffer)
	if err != nil || offerSum.Unique != 1 {
		t.Fatalf("offer selection %+v err=%v, want the own unique of the offer", offerSum, err)
	}
	byCur := []Cond{conds[0], {Field: "cost_cur", Op: "eq", Values: []string{"USD"}}}
	curSum, err := ch.SummaryFiltered(ctx, from, to, byCur)
	if err != nil || curSum.Total != 2 {
		t.Fatalf("cost_cur filter %+v err=%v, want the landing and the offer step", curSum, err)
	}
	slice, err := ch.SliceFiltered(ctx, from, to, conds)
	if err != nil {
		t.Fatal(err)
	}
	var sliceUniq, sliceFirst int64
	for _, r := range slice {
		sliceUniq += r.Unique
		sliceFirst += r.UniqFirst
	}
	if sliceUniq != 5 {
		t.Fatalf("report unique %d, want 5: the offer column is grouped on, every row keeps its own uniques", sliceUniq)
	}
	if sliceFirst != sum.Unique {
		t.Fatalf("report uniq_first %d, want the summary's %d", sliceFirst, sum.Unique)
	}

	got, err := ch.ClickBuckets(ctx, group, from, to, true)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ch.ClickBuckets(ctx, legacy, from, to, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("legacy campaign has no spend weights, the comparison proves nothing")
	}
	if fmt.Sprint(bucketWeights(got)) != fmt.Sprint(bucketWeights(want)) {
		t.Fatalf("spend weights %v differ from the legacy log %v", bucketWeights(got), bucketWeights(want))
	}
}

// bucketWeights lists hits and uniques per offer id, ordered by the id.
func bucketWeights(rows []BucketCount) map[string][2]int64 {
	out := map[string][2]int64{}
	for _, r := range rows {
		w := out[r.OfferID]
		out[r.OfferID] = [2]int64{w[0] + r.Hits, w[1] + r.Uniq}
	}
	return out
}
