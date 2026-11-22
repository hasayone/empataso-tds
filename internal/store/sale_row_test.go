package store

import "testing"

// TestSaleRow checks which status a sale is stored under and whether it is a new sale.
func TestSaleRow(t *testing.T) {
	cases := []struct {
		name                 string
		status               string
		repeat, upsell, seen bool
		tid                  string
		stored               string
		fresh                bool
	}{
		{name: "first sale", status: "sale", stored: "sale", fresh: true},
		{name: "repeat without upsell", status: "sale", repeat: true, tid: "t2", stored: "sale"},
		{name: "repeat without tid", status: "sale", repeat: true, upsell: true, stored: "sale"},
		{name: "new tid", status: "sale", repeat: true, upsell: true, tid: " t2 ", stored: "sale:t2", fresh: true},
		{name: "same tid again", status: "sale", repeat: true, upsell: true, tid: "t2", seen: true, stored: "sale:t2"},
		{name: "lead is not an upsell", status: "lead", repeat: true, upsell: true, tid: "t2", stored: "lead"},
	}
	for _, c := range cases {
		stored, fresh := saleRow(c.status, c.repeat, c.upsell, c.tid, c.seen)
		if stored != c.stored || fresh != c.fresh {
			t.Errorf("%s: got %q fresh %v, want %q fresh %v", c.name, stored, fresh, c.stored, c.fresh)
		}
	}
}
