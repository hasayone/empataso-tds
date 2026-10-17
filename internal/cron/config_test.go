package cron

import (
	"strings"
	"testing"
)

func TestDefaultIPProxySources(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	var proxy []IPSource
	for _, s := range c.IPLists.Sources {
		if strings.Contains(s.URL, "firehol_proxies.netset") {
			t.Fatalf("oversized list still configured: %s", s.URL)
		}
		if s.Target == "ip_proxy" {
			proxy = append(proxy, s)
		}
	}
	if len(proxy) != 5 {
		t.Fatalf("ip_proxy sources = %d", len(proxy))
	}
	if proxy[0].Mode != "replace" {
		t.Fatalf("first ip_proxy mode = %s", proxy[0].Mode)
	}
	for i, s := range proxy[1:] {
		if s.Mode != "merge" {
			t.Fatalf("ip_proxy source %d mode = %s", i+2, s.Mode)
		}
	}
}
