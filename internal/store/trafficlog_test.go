package store

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTrafficLog(t *testing.T, limits TrafficLogLimits) (*TrafficLog, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewTrafficLog(rdb, limits), mr
}

func TestTrafficLogKeepsNewestFirstAndCutsToTheLimit(t *testing.T) {
	lim := DefaultTrafficLogLimits()
	lim.PerCampaign = 3
	l, _ := newTrafficLog(t, lim)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if err := l.Push(ctx, []string{"g"}, "c"+strconv.Itoa(i), []byte(`{"n":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.Recent(ctx, "g", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || string(got[0]) != `{"n":5}` || string(got[2]) != `{"n":3}` {
		t.Fatalf("recent = %q, want the latest three newest first", got)
	}
	if two, _ := l.Recent(ctx, "g", 2); len(two) != 2 {
		t.Errorf("asking for 2 returned %d", len(two))
	}
}

func TestTrafficLogClickKeepsStepsInOrder(t *testing.T) {
	l, _ := newTrafficLog(t, DefaultTrafficLogLimits())
	ctx := context.Background()
	_ = l.Push(ctx, []string{"g"}, "cid1", []byte(`{"step":0}`))
	_ = l.Push(ctx, []string{"g"}, "cid1", []byte(`{"step":1}`))
	got, err := l.Click(ctx, "cid1")
	if err != nil || len(got) != 2 || string(got[0]) != `{"step":0}` || string(got[1]) != `{"step":1}` {
		t.Fatalf("click = %q err %v", got, err)
	}
	if none, err := l.Click(ctx, "unknown"); err != nil || len(none) != 0 {
		t.Errorf("an unknown click returned %q, err %v", none, err)
	}
}

func TestTrafficLogStoresUnderEveryNamedCampaign(t *testing.T) {
	l, _ := newTrafficLog(t, DefaultTrafficLogLimits())
	ctx := context.Background()
	if err := l.Push(ctx, []string{"a", "", "b"}, "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"a", "b"} {
		if got, _ := l.Recent(ctx, g, 10); len(got) != 1 {
			t.Errorf("campaign %s holds %d entries", g, len(got))
		}
	}
	if err := l.Push(ctx, []string{""}, "", []byte(`{}`)); err == nil {
		t.Error("an entry for no campaign must fail")
	}
}

func TestTrafficLogEntriesExpire(t *testing.T) {
	lim := TrafficLogLimits{PerCampaign: 10, CampaignTTL: time.Hour, ClickTTL: time.Minute}
	l, mr := newTrafficLog(t, lim)
	ctx := context.Background()
	_ = l.Push(ctx, []string{"g"}, "cid", []byte(`{}`))
	mr.FastForward(2 * time.Minute)
	if got, _ := l.Click(ctx, "cid"); len(got) != 0 {
		t.Error("the click path must expire after the click lifetime")
	}
	if got, _ := l.Recent(ctx, "g", 1); len(got) != 1 {
		t.Error("the campaign log lives longer than a click path")
	}
	mr.FastForward(2 * time.Hour)
	if got, _ := l.Recent(ctx, "g", 1); len(got) != 0 {
		t.Error("an idle campaign log must expire")
	}
}

func TestTrafficLogOffAndNil(t *testing.T) {
	var nilLog *TrafficLog
	if nilLog.Enabled() || nilLog.Limits().PerCampaign != 0 {
		t.Error("a nil log keeps nothing")
	}
	if got, err := nilLog.Click(context.Background(), "x"); got != nil || err != nil {
		t.Errorf("nil log click = %v %v", got, err)
	}
	off, _ := newTrafficLog(t, TrafficLogLimits{})
	if off.Enabled() {
		t.Error("zero entries per campaign is off")
	}
	if err := off.Push(context.Background(), []string{"g"}, "c", []byte(`{}`)); err == nil {
		t.Error("an off log must refuse a push")
	}
	if got, _ := off.Recent(context.Background(), "g", 5); got != nil {
		t.Errorf("an off log returned %v", got)
	}
}

func TestTrafficLogLimitsFromEnv(t *testing.T) {
	t.Setenv("EMTDS_TRAFFIC_LOG_MAX", "20000")
	t.Setenv("EMTDS_TRAFFIC_LOG_TTL", "36h")
	t.Setenv("EMTDS_TRAFFIC_LOG_CLICK_TTL", "nonsense")
	l := TrafficLogLimitsFromEnv()
	if l.PerCampaign != MaxTrafficLogMax || l.CampaignTTL != 36*time.Hour || l.ClickTTL != DefaultTrafficLogClickTTL {
		t.Errorf("limits = %+v", l)
	}
	t.Setenv("EMTDS_TRAFFIC_LOG_MAX", "0")
	if TrafficLogLimitsFromEnv().PerCampaign != 0 {
		t.Error("0 must switch the log off")
	}
}

func TestPackEntryRoundTripAndBound(t *testing.T) {
	in := []byte(strings.Repeat(`{"k":"value"}`, 100))
	packed, err := packEntry(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) >= len(in) {
		t.Errorf("packed %d of %d bytes", len(packed), len(in))
	}
	out, err := unpackEntry(packed)
	if err != nil || !bytes.Equal(out, in) {
		t.Fatalf("round trip failed: %v", err)
	}
	big, _ := packEntry(bytes.Repeat([]byte("a"), trafficLogMaxEntry+10))
	if _, err := unpackEntry(big); err == nil {
		t.Error("an entry that unpacks beyond the ceiling must be refused")
	}
	if _, err := unpackEntry([]byte("not deflate")); err == nil {
		t.Error("garbage must be refused")
	}
}
