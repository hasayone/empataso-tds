package store

import (
	"strings"
	"testing"
	"time"
)

func TestPostbackWhereStatusAndCID(t *testing.T) {
	from := time.Unix(1, 0)
	to := time.Unix(2, 0)
	where, args := postbackWhere(PostbackQuery{
		From: from, To: to, Status: "sale", CID: "abc", Group: "promo",
	}, true)
	for _, col := range []string{"status = ?", "cid = ?", "group_name = ?"} {
		if !strings.Contains(where, col) {
			t.Fatalf("where %q missing %s", where, col)
		}
	}
	if len(args) != 5 {
		t.Fatalf("args %d, want 5 (from, to, group, status, cid)", len(args))
	}
	if args[2] != "promo" || args[3] != "sale" || args[4] != "abc" {
		t.Fatalf("args %#v", args)
	}
}

func TestPostbackWhereSkipsEntityFiltersWithoutColumns(t *testing.T) {
	where, args := postbackWhere(PostbackQuery{
		From: time.Unix(1, 0), To: time.Unix(2, 0), Offer: "o1",
	}, false)
	if strings.Contains(where, "offer_id") {
		t.Fatalf("offer filter leaked: %s", where)
	}
	if len(args) != 2 {
		t.Fatalf("args %d", len(args))
	}
}
