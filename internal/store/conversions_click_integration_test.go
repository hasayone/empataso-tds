//go:build integration

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// clickQuery is one finished readClick SELECT as system.query_log records it.
type clickQuery struct {
	text     string
	readRows uint64
}

// taggedCtx returns ctx whose queries carry tag as log_comment, so their
// query_log rows can be told apart from every other query on the server.
func taggedCtx(ctx context.Context, tag string) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"log_comment": tag}))
}

// clickQueries flushes the server logs and returns, oldest first, the finished
// events lookups that ran with log_comment tag.
// A failed flush or read fails t.
func clickQueries(t *testing.T, ch *CH, tag string) []clickQuery {
	t.Helper()
	ctx := context.Background()
	if err := ch.conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatalf("SYSTEM FLUSH LOGS: %v", err)
	}
	rows, err := ch.conn.Query(ctx, `SELECT query, read_rows FROM system.query_log
		WHERE type = 'QueryFinish' AND log_comment = ? AND query LIKE '%FROM events WHERE cid%'
		ORDER BY event_time_microseconds`, tag)
	if err != nil {
		t.Fatalf("query_log: %v", err)
	}
	defer rows.Close()
	var out []clickQuery
	for rows.Next() {
		var q clickQuery
		if err := rows.Scan(&q.text, &q.readRows); err != nil {
			t.Fatalf("scan query_log: %v", err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCHReadClickAge writes clicks 0, 3, 20 and 300 days old into a private
// test database and checks that readClick finds each of them, returns the
// newest row of a cid that has several (also when all are older than 7
// days), and reports sql.ErrNoRows for an unknown cid.
func TestCHReadClickAge(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	ages := []int{0, 3, 20, 300}
	var events []logbuf.Event
	for _, age := range ages {
		events = append(events, logbuf.Event{
			Ts: now.Add(-time.Duration(age) * day), GroupID: fmt.Sprintf("g%d", age),
			GroupName: fmt.Sprintf("group%d", age), Stream: "s1", CID: fmt.Sprintf("age_%d", age),
		})
	}
	events = append(events,
		logbuf.Event{Ts: now.Add(-20 * day), GroupID: "old", GroupName: "old", Stream: "s1", CID: "twice"},
		logbuf.Event{Ts: now.Add(-3 * day), GroupID: "new", GroupName: "new", Stream: "s1", CID: "twice"},
		logbuf.Event{Ts: now.Add(-30 * day), GroupID: "older", GroupName: "older", Stream: "s1", CID: "old_twice"},
		logbuf.Event{Ts: now.Add(-10 * day), GroupID: "newer", GroupName: "newer", Stream: "s1", CID: "old_twice"},
	)
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	for _, age := range ages {
		snap, err := ch.readClick(ctx, fmt.Sprintf("age_%d", age))
		if err != nil {
			t.Fatalf("click aged %d days: %v", age, err)
		}
		if want := fmt.Sprintf("g%d", age); snap.gid != want {
			t.Fatalf("click aged %d days: group %q, want %q", age, snap.gid, want)
		}
	}

	snap, err := ch.readClick(ctx, "twice")
	if err != nil || snap.gid != "new" {
		t.Fatalf("newest of two rows: group %q err=%v, want new", snap.gid, err)
	}
	snap, err = ch.readClick(ctx, "old_twice")
	if err != nil || snap.gid != "newer" {
		t.Fatalf("newest of two rows older than 7 days: group %q err=%v, want newer", snap.gid, err)
	}

	if _, err := ch.readClick(ctx, "no_such_cid"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown cid: err=%v, want sql.ErrNoRows", err)
	}
}

// TestCHReadClickWindow checks the 7-day bound itself. A fresh click must be
// served by one bounded SELECT that reads only the recent partitions; a click
// older than 7 days takes the bounded SELECT first and then the unbounded
// one. Dropping the bound makes the fresh lookup read every partition and the
// test fail.
func TestCHReadClickWindow(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	var events []logbuf.Event
	freshRows := 0
	for _, age := range []int{0, 1, 2, 3, 20, 40, 60, 80, 100, 200, 300} {
		events = append(events, logbuf.Event{
			Ts: now.Add(-time.Duration(age) * day), GroupID: "g", GroupName: "g", Stream: "s1",
			CID: fmt.Sprintf("w_%d", age),
		})
		if age < 7 {
			freshRows++
		}
	}
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	freshTag := fmt.Sprintf("itest_readclick_fresh_%d", now.UnixNano())
	if _, err := ch.readClick(taggedCtx(ctx, freshTag), "w_0"); err != nil {
		t.Fatalf("fresh click: %v", err)
	}
	fresh := clickQueries(t, ch, freshTag)
	if len(fresh) != 1 {
		t.Fatalf("fresh click ran %d lookups, want exactly 1", len(fresh))
	}
	if !strings.Contains(fresh[0].text, "INTERVAL 7 DAY") {
		t.Fatalf("fresh lookup has no 7-day bound: %s", fresh[0].text)
	}
	if fresh[0].readRows > uint64(freshRows) {
		t.Fatalf("fresh lookup read %d rows, want at most the %d rows of the last 7 days (the table holds %d)",
			fresh[0].readRows, freshRows, len(events))
	}

	oldTag := fmt.Sprintf("itest_readclick_old_%d", now.UnixNano())
	if _, err := ch.readClick(taggedCtx(ctx, oldTag), "w_300"); err != nil {
		t.Fatalf("old click: %v", err)
	}
	old := clickQueries(t, ch, oldTag)
	if len(old) != 2 {
		t.Fatalf("old click ran %d lookups, want 2", len(old))
	}
	if !strings.Contains(old[0].text, "INTERVAL 7 DAY") || strings.Contains(old[1].text, "INTERVAL 7 DAY") {
		t.Fatalf("old click: first lookup must be bounded and the second not:\n%s\n%s", old[0].text, old[1].text)
	}
}
