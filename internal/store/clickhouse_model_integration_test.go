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

// TestCHDeviceModel writes a click with a device model, records a conversion
// from it and checks that both journals return the model and that a field
// filter on device_model works for clicks and conversions.
func TestCHDeviceModel(t *testing.T) {
	ch := chFromEnv(t)
	if !ch.modelEvents || !ch.modelConv {
		t.Fatal("device_model wave is not ready")
	}
	ctx := context.Background()
	group := fmt.Sprintf("itest_model_%d", os.Getpid())
	cid := fmt.Sprintf("model_%d", os.Getpid())
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	t.Cleanup(func() { cleanupGroup(t, ch, group) })

	err := ch.InsertEvents(ctx, []logbuf.Event{
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", CID: cid, DeviceModel: "SM-S918B"},
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", CID: cid + "x", DeviceModel: "Pixel 8"},
	})
	if err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := ch.RecordConversion(ctx, PostbackIn{CID: cid, Profit: 2, Status: "sale"}); err != nil {
		t.Fatalf("RecordConversion: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	rows, total, err := ch.Logs(ctx, LogFilter{
		From: from, To: to, Group: []string{group}, Limit: 10,
		Conds: []Cond{{Field: "device_model", Op: "eq", Values: []string{"SM-S918B"}}},
	})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].DeviceModel != "SM-S918B" {
		t.Fatalf("Logs by model: total=%d err=%v rows=%+v", total, err, rows)
	}
	pb, _, err := ch.QueryPostbacks(ctx, PostbackQuery{
		From: from, To: to, Group: group, Limit: 10,
		Conds: []Cond{{Field: "device_model", Op: "eq", Values: []string{"SM-S918B"}}},
	})
	if err != nil || len(pb) != 1 || pb[0].DeviceModel != "SM-S918B" {
		t.Fatalf("Postbacks by model: err=%v rows=%+v", err, pb)
	}
	other, _, err := ch.QueryPostbacks(ctx, PostbackQuery{
		From: from, To: to, Group: group, Limit: 10,
		Conds: []Cond{{Field: "device_model", Op: "eq", Values: []string{"Pixel 8"}}},
	})
	if err != nil || len(other) != 0 {
		t.Fatalf("Postbacks by another model: err=%v rows=%+v", err, other)
	}
	kv, err := ch.Values(ctx, ValuesQuery{Table: "events", Field: "device_model", From: from, To: to, Limit: 50})
	if err != nil || !containsKey(kv, "Pixel 8") {
		t.Fatalf("Values device_model: err=%v kv=%v", err, kv)
	}
}
