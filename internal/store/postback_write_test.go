package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// fakeBatch is a driver.Batch that records what is appended and how it ends.
type fakeBatch struct {
	driver.Batch
	rows      [][]any
	appendErr error
	sent      int
	aborted   int
}

func (b *fakeBatch) Append(v ...any) error {
	if b.appendErr != nil {
		return b.appendErr
	}
	b.rows = append(b.rows, v)
	return nil
}

func (b *fakeBatch) Send() error {
	b.sent++
	return nil
}

func (b *fakeBatch) Abort() error {
	b.aborted++
	return nil
}

// batchRecConn is a driver.Conn that hands out one fakeBatch and remembers the
// statement and the context of the last PrepareBatch.
type batchRecConn struct {
	driver.Conn
	batch   *fakeBatch
	queries []string
	ctxs    []context.Context
}

func (c *batchRecConn) PrepareBatch(ctx context.Context, query string, _ ...driver.PrepareBatchOption) (driver.Batch, error) {
	c.queries = append(c.queries, query)
	c.ctxs = append(c.ctxs, ctx)
	return c.batch, nil
}

func newBatchCH() (*CH, *batchRecConn) {
	conn := &batchRecConn{batch: &fakeBatch{}}
	return &CH{conn: conn, limits: testLimits()}, conn
}

func TestInsertPostbackLogOneSyncBatch(t *testing.T) {
	c, conn := newBatchCH()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	rows := []PostbackLogRow{
		{Ts: at, Subid: "c1", Status: "lead", Payout: 1.5, Outcome: "ok", Direction: "in"},
		{Subid: "c2", Status: "sale", Payout: 2, Outcome: "404", Direction: "out"},
	}
	if err := c.InsertPostbackLog(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	if len(conn.queries) != 1 || !strings.Contains(conn.queries[0], "INSERT INTO postback_log (ts, subid,") {
		t.Fatalf("statements %v", conn.queries)
	}
	b := conn.batch
	if b.sent != 1 || len(b.rows) != 2 || b.aborted != 0 {
		t.Fatalf("sent %d rows %d aborted %d, want one batch of two", b.sent, len(b.rows), b.aborted)
	}
	if got := b.rows[0][0].(time.Time); !got.Equal(at) || b.rows[0][1] != "c1" || b.rows[0][4] != "ok" {
		t.Fatalf("first row keeps its own time and fields: %v", b.rows[0])
	}
	if got := b.rows[1][0].(time.Time); got.IsZero() {
		t.Fatal("a zero Ts must become the current time")
	}
	set, ok := conn.ctxs[0].Value(insertSettingsKey{}).(clickhouse.Settings)
	if !ok || set["insert_deduplicate"] != 0 {
		t.Fatalf("a batch with a row without Ts has no stable token, so deduplication must be off: %v", set)
	}
}

func TestInsertPostbackLogEmptyAndNoConn(t *testing.T) {
	c, conn := newBatchCH()
	if err := c.InsertPostbackLog(context.Background(), nil); err != nil || len(conn.queries) != 0 {
		t.Fatalf("an empty slice must not touch the connection: err=%v queries=%v", err, conn.queries)
	}
	var none *CH
	if err := none.InsertPostbackLog(context.Background(), []PostbackLogRow{{Subid: "c"}}); !errors.Is(err, ErrNoStoreConn) {
		t.Fatalf("nil client: %v, want ErrNoStoreConn", err)
	}
	if err := (&CH{}).InsertPostbackLog(context.Background(), []PostbackLogRow{{Subid: "c"}}); !errors.Is(err, ErrNoStoreConn) {
		t.Fatalf("client without a connection: %v, want ErrNoStoreConn", err)
	}
}

func TestInsertPostbackLogAppendErrorAborts(t *testing.T) {
	c, conn := newBatchCH()
	conn.batch.appendErr = errors.New("bad column")
	err := c.InsertPostbackLog(context.Background(), []PostbackLogRow{{Subid: "c"}})
	if err == nil || conn.batch.aborted != 1 || conn.batch.sent != 0 {
		t.Fatalf("err=%v aborted=%d sent=%d, want an error and an aborted batch", err, conn.batch.aborted, conn.batch.sent)
	}
}

// writeConversion names the ts column and passes the arrival time, never the
// server default.
func TestWriteConversionWritesArrivalTime(t *testing.T) {
	c, conn := newBatchCH()
	at := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	in := PostbackIn{CID: "c1", Status: " Lead ", Profit: 3, At: at}
	if err := c.writeConversion(context.Background(), clickSnap{gname: "g", sname: "s"}, in, ""); err != nil {
		t.Fatal(err)
	}
	if len(conn.queries) != 1 || !strings.HasPrefix(conn.queries[0], "INSERT INTO conversions (ts, domain,") {
		t.Fatalf("statement %v", conn.queries)
	}
	row := conn.batch.rows[0]
	if got, ok := row[0].(time.Time); !ok || !got.Equal(at) {
		t.Fatalf("row time %v, want the arrival %v", row[0], at)
	}
	if !slices.Contains(row, any("lead")) {
		t.Fatalf("status must be stored folded: %v", row)
	}
}

func TestNormalStatus(t *testing.T) {
	for in, want := range map[string]string{" Lead ": "lead", "SALE": "sale", "": "", "rejected": "rejected"} {
		if got := normalStatus(in); got != want {
			t.Errorf("normalStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCountersTryLock(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	key := "outb:c1:lead"
	if ok, err := c.TryLock(ctx, key, time.Hour); !ok || err != nil {
		t.Fatalf("first take: %v %v", ok, err)
	}
	if ok, err := c.TryLock(ctx, key, time.Hour); ok || err != nil {
		t.Fatalf("second take must lose: %v %v", ok, err)
	}
	if ttl := mr.TTL(key); ttl <= 0 || ttl > time.Hour {
		t.Fatalf("the key lives %v, want up to an hour", ttl)
	}
	if err := c.Unlock(ctx, key); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.TryLock(ctx, key, time.Hour); !ok {
		t.Fatal("a released key can be taken again")
	}
	mr.Close()
	if ok, err := c.TryLock(ctx, "outb:c2:sale", time.Hour); !ok || err == nil {
		t.Fatalf("a dead Redis must fail open with an error: %v %v", ok, err)
	}
}
