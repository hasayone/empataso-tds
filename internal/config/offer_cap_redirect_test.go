package config

import "testing"

// TestCapLimitRedirect checks that a used-up limit, daily or total, hands the click
// to the one alternative offer, and that an empty alternative skips the offer.
// A limit that is off reports no number. cpc writes the amount on the click;
// cpa does not.
func TestCapLimitRedirect(t *testing.T) {
	o := Offer{FallbackID: " alt "}
	if g := o.CapRedirect(CapDaily); g != "alt" {
		t.Fatalf("daily limit alternative = %q", g)
	}
	if g := o.CapRedirect(CapTotal); g != "alt" {
		t.Fatalf("total limit alternative = %q", g)
	}
	if g := o.CapRedirect(CapDaily | CapTotal); g != "alt" {
		t.Fatalf("both limits alternative = %q", g)
	}
	o.FallbackID = "  "
	if g := o.CapRedirect(CapDaily); g != "" {
		t.Fatalf("empty alternative did not skip: %q", g)
	}
	o.FallbackID = "alt"
	if g := o.CapRedirect(0); g != "" {
		t.Fatalf("no limit used up still redirected: %q", g)
	}
	if n, ok := (Offer{ConvTotal: true, ConvTotalLimit: 4}).TotalLimit(); n != 4 || !ok {
		t.Fatalf("total limit = %d %v", n, ok)
	}
	if n, ok := (Offer{ConvTotal: false, ConvTotalLimit: 4}).TotalLimit(); n != 0 || ok {
		t.Fatalf("a total limit that is off = %d %v", n, ok)
	}
	cpc := Offer{PayoutModel: "cpc", Payout: 2.5, PayoutCurrency: "USD", PayoutParam: true}
	if rev, ok := cpc.ClickRevenue(); !ok || rev != 2.5 {
		t.Fatalf("cpc click revenue = %v %v", rev, ok)
	}
	if _, ok := cpc.ReservePayout(); ok {
		t.Fatal("cpc also wrote the amount on the conversion")
	}
	cpa := Offer{PayoutModel: "cpa", Payout: 2.5, PayoutCurrency: "USD", PayoutParam: true}
	if _, ok := cpa.ClickRevenue(); ok {
		t.Fatal("cpa wrote revenue on the click")
	}
	if rev, ok := cpa.ReservePayout(); !ok || rev != 2.5 {
		t.Fatalf("cpa conversion reserve = %v %v", rev, ok)
	}
}
