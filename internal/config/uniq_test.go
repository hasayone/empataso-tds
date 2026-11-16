package config

import "testing"

// TestUniqPlanOf checks the legacy switch and the two new fields.
func TestUniqPlanOf(t *testing.T) {
	if got := UniqPlanOf(nil); got != (UniqPlan{}) {
		t.Fatalf("nil plan = %+v", got)
	}
	oldIP := UniqPlanOf(&Group{UniqMethod: "ip", UniqSeconds: 8760 * 3600})
	if oldIP != (UniqPlan{}) {
		t.Fatalf("legacy ip = %+v", oldIP)
	}
	oldCookie := UniqPlanOf(&Group{UniqMethod: "cookie"})
	if oldCookie != (UniqPlan{LegacyCookie: true}) {
		t.Fatalf("legacy cookie = %+v", oldCookie)
	}
	empty := UniqPlanOf(&Group{})
	if empty != (UniqPlan{}) {
		t.Fatalf("empty = %+v", empty)
	}
	both := UniqPlanOf(&Group{UniqMethod: "cookie", UniqBy: "ip_ua", UniqCookie: true})
	if both != (UniqPlan{Cookie: true, ByUA: true}) {
		t.Fatalf("new fields must ignore the legacy method, got %+v", both)
	}
	ipOnly := UniqPlanOf(&Group{UniqBy: "ip", UniqCookie: true})
	if ipOnly != (UniqPlan{Cookie: true}) {
		t.Fatalf("ip + cookie = %+v", ipOnly)
	}
	ua := UniqPlanOf(&Group{UniqBy: "ip_ua"})
	if ua != (UniqPlan{ByUA: true}) {
		t.Fatalf("ip_ua = %+v", ua)
	}
}

// TestUniqCookieSeconds caps the signed cookie at 8760 hours and keeps a short window.
func TestUniqCookieSeconds(t *testing.T) {
	if got := UniqCookieSeconds(0); got != 86400 {
		t.Errorf("empty window = %d", got)
	}
	if got := UniqCookieSeconds(-5); got != 86400 {
		t.Errorf("negative window = %d", got)
	}
	if got := UniqCookieSeconds(3600); got != 3600 {
		t.Errorf("short window = %d", got)
	}
	if got := UniqCookieSeconds(UniqCookieMaxSeconds); got != UniqCookieMaxSeconds {
		t.Errorf("8760 hours = %d", got)
	}
	if got := UniqCookieSeconds(UniqCookieMaxSeconds + 1); got != UniqCookieMaxSeconds {
		t.Errorf("above 8760 hours = %d", got)
	}
}
