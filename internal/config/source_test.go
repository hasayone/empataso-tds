package config

import (
	"net/url"
	"testing"
)

func TestApplySourceMapsPartnerName(t *testing.T) {
	src := &TrafficSource{Parameters: map[string]string{"sub_id_1": "click_id"}}
	_, subs := ApplySource(url.Values{"click_id": {"ZZ"}}, src)
	if subs[0] != "ZZ" {
		t.Fatalf("sub_id_1 = %q", subs[0])
	}
}
