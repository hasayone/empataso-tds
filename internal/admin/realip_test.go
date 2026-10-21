package admin

import "testing"

func TestParseAdminTrustedProxiesInvalid(t *testing.T) {
	if _, err := parseAdminTrustedProxies([]string{"not-a-cidr"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseAdminTrustedProxiesBareAddress(t *testing.T) {
	p, err := parseAdminTrustedProxies([]string{"203.0.113.1"})
	if err != nil || len(p) != 1 || p[0].String() != "203.0.113.1/32" {
		t.Fatalf("prefixes=%v err=%v", p, err)
	}
}
