package store

import (
	"strings"
	"testing"
	"time"
)

// TestPostbackLogTokenIsStablePerBatch checks that the same rows give the same
// token, that any change of any field gives another, and that a row without a
// time gives no token.
func TestPostbackLogTokenIsStablePerBatch(t *testing.T) {
	ts := time.Date(2026, 11, 16, 20, 0, 0, 5, time.UTC)
	base := []PostbackLogRow{{Ts: ts, Subid: "abc", Status: "lead", Payout: 1.5, Outcome: "ok", Direction: "in"}}
	tok := postbackLogToken(base)
	if !strings.HasPrefix(tok, "pl-") || tok != postbackLogToken(append([]PostbackLogRow(nil), base...)) {
		t.Fatalf("token %q is not stable for equal rows", tok)
	}
	for name, mutate := range map[string]func(*PostbackLogRow){
		"ts":        func(r *PostbackLogRow) { r.Ts = r.Ts.Add(time.Nanosecond) },
		"subid":     func(r *PostbackLogRow) { r.Subid = "abd" },
		"status":    func(r *PostbackLogRow) { r.Status = "sale" },
		"payout":    func(r *PostbackLogRow) { r.Payout = 2 },
		"outcome":   func(r *PostbackLogRow) { r.Outcome = "queued" },
		"direction": func(r *PostbackLogRow) { r.Direction = "out" },
	} {
		row := base[0]
		mutate(&row)
		if postbackLogToken([]PostbackLogRow{row}) == tok {
			t.Fatalf("a change of %s kept the token", name)
		}
	}
	shifted := []PostbackLogRow{{Ts: ts, Subid: "ab", Status: "clead", Payout: 1.5, Outcome: "ok", Direction: "in"}}
	if postbackLogToken(shifted) == tok {
		t.Fatal("moving a character between fields kept the token")
	}
	if got := postbackLogToken([]PostbackLogRow{{Subid: "abc"}}); got != "" {
		t.Fatalf("a row without a time got the token %q, want none", got)
	}
}
