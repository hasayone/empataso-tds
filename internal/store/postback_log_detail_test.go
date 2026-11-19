package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestInsertPostbackLogDetailColumns checks that the detail columns are written
// once ensureSchema has seen them, and left out before that.
func TestInsertPostbackLogDetailColumns(t *testing.T) {
	row := PostbackLogRow{
		Ts: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), Subid: "c1", Status: "sale", Payout: 2, Outcome: "ok", Direction: "in",
		IP: "203.0.113.5", Network: "net1", Method: "GET", Path: "/postback", Params: "key=%2A%2A%2A&subid=c1",
		UserAgent: "curl/8", CampaignID: "g1", Duplicate: true,
	}
	c, conn := newBatchCH()
	if err := c.InsertPostbackLog(context.Background(), []PostbackLogRow{row}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conn.queries[0], "useragent") || len(conn.batch.rows[0]) != 6 {
		t.Fatalf("without the detail columns: %s %v", conn.queries[0], conn.batch.rows[0])
	}
	c, conn = newBatchCH()
	c.pblogDetail.Store(true)
	if err := c.InsertPostbackLog(context.Background(), []PostbackLogRow{row}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conn.queries[0], "ip, network_id, method, path, params, useragent, campaign_id, duplicate") {
		t.Fatalf("statement %s", conn.queries[0])
	}
	got := conn.batch.rows[0]
	want := []any{"203.0.113.5", "net1", "GET", "/postback", "key=%2A%2A%2A&subid=c1", "curl/8", "g1", uint8(1)}
	if !reflect.DeepEqual(got[6:], want) {
		t.Fatalf("detail values %v, want %v", got[6:], want)
	}
}

// TestPostbackLogTokenCoversDetail checks that two batches that differ only in a
// detail field get different tokens, so the second is not dropped as a repeat.
func TestPostbackLogTokenCoversDetail(t *testing.T) {
	base := PostbackLogRow{Ts: time.Unix(1700000000, 0), Subid: "c", Outcome: "ok", Direction: "in"}
	tok := postbackLogToken([]PostbackLogRow{base})
	for name, mutate := range map[string]func(*PostbackLogRow){
		"ip":        func(r *PostbackLogRow) { r.IP = "203.0.113.1" },
		"network":   func(r *PostbackLogRow) { r.Network = "n" },
		"method":    func(r *PostbackLogRow) { r.Method = "POST" },
		"path":      func(r *PostbackLogRow) { r.Path = "/postback" },
		"params":    func(r *PostbackLogRow) { r.Params = "a=1" },
		"useragent": func(r *PostbackLogRow) { r.UserAgent = "x" },
		"campaign":  func(r *PostbackLogRow) { r.CampaignID = "g" },
		"duplicate": func(r *PostbackLogRow) { r.Duplicate = true },
	} {
		row := base
		mutate(&row)
		if postbackLogToken([]PostbackLogRow{row}) == tok {
			t.Errorf("%s does not change the token", name)
		}
	}
}

// TestPostbackLogSQLFilters checks the WHERE clause and arguments of the log query.
func TestPostbackLogSQLFilters(t *testing.T) {
	from, to := time.Unix(1, 0), time.Unix(2, 0)
	q := PostbackLogQuery{From: from, To: to, Outcome: "click_not_found", Network: "n1", IP: "203.0.113.9", CID: "c1",
		Direction: "in", Offset: 50, Limit: 25, Scope: &Cond{Field: "campaign_id", Op: "in", Values: []string{"g1", "g2"}, Scope: true}}
	query, args := postbackLogSQL(q, true)
	for _, part := range []string{"outcome = ?", "subid = ?", "direction = ?", "network_id = ?", "ip = ?",
		"campaign_id != ''", "campaign_id IN (?)", "ORDER BY ts DESC LIMIT ? OFFSET ?", "useragent"} {
		if !strings.Contains(query, part) {
			t.Errorf("query lacks %q: %s", part, query)
		}
	}
	want := []any{from, to, "click_not_found", "c1", "in", "n1", "203.0.113.9", []string{"g1", "g2"}, 25, 50}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args %v, want %v", args, want)
	}
	nin, _ := postbackLogSQL(PostbackLogQuery{Scope: &Cond{Op: "nin", Values: []string{"g3"}}}, true)
	if !strings.Contains(nin, "campaign_id NOT IN (?)") {
		t.Fatalf("nin scope: %s", nin)
	}
}

// TestPostbackLogSQLOutcomes checks the accepted, duplicate and default-limit cases.
func TestPostbackLogSQLOutcomes(t *testing.T) {
	dup, args := postbackLogSQL(PostbackLogQuery{Outcome: PostbackOutcomeDuplicate, Limit: 5000}, true)
	if !strings.Contains(dup, "outcome = 'ok' AND duplicate = 1") || args[len(args)-2] != 200 {
		t.Fatalf("duplicate: %s %v", dup, args)
	}
	ok, _ := postbackLogSQL(PostbackLogQuery{Outcome: "ok"}, true)
	if !strings.Contains(ok, "outcome = 'ok' AND duplicate = 0") {
		t.Fatalf("ok: %s", ok)
	}
	old, _ := postbackLogSQL(PostbackLogQuery{Outcome: PostbackOutcomeDuplicate}, false)
	if strings.Contains(old, "duplicate") || strings.Contains(old, "useragent") || !strings.Contains(old, " AND 0") {
		t.Fatalf("without detail columns a duplicate filter matches nothing: %s", old)
	}
	scoped, _ := postbackLogSQL(PostbackLogQuery{Scope: &Cond{Op: "in", Values: []string{"g"}}}, false)
	if !strings.Contains(scoped, " AND 0") {
		t.Fatalf("a scoped read without campaign column must match nothing: %s", scoped)
	}
}

// TestHasColumns checks the readiness test of the detail wave.
func TestHasColumns(t *testing.T) {
	all := map[string]bool{}
	for _, col := range postbackLogDetailCols {
		all[col] = true
	}
	if !hasColumns(all, postbackLogDetailCols) {
		t.Fatal("every column present")
	}
	delete(all, "duplicate")
	if hasColumns(all, postbackLogDetailCols) || hasColumns(nil, postbackLogDetailCols) {
		t.Fatal("a missing column must make the wave not ready")
	}
}
