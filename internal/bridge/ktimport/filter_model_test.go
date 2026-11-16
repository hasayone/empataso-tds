package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelFilterExact(t *testing.T) {
	cases := []struct {
		name    string
		f       KFilter
		flag    int
		values  string
		skipped bool
	}{
		{"accept list", KFilter{Name: "device_model", Mode: "accept", Payload: json.RawMessage(`["iPhone","Pixel 8"]`)}, 2, "iPhone|Pixel 8", false},
		{"reject list", KFilter{Name: "device_model", Mode: "reject", Payload: json.RawMessage(`["SM-S918B"]`)}, 1, "SM-S918B", false},
		{"substring mode", KFilter{Name: "device_model", Mode: "contains", Payload: json.RawMessage(`["Pixel"]`)}, 0, "", true},
		{"regex mode", KFilter{Name: "device_model", Mode: "regex", Payload: json.RawMessage(`["Pixel.*"]`)}, 0, "", true},
		{"regex value", KFilter{Name: "device_model", Mode: "accept", Payload: json.RawMessage(`["/^Pixel/"]`)}, 0, "", true},
		{"empty payload", KFilter{Name: "device_model", Mode: "accept", Payload: json.RawMessage(`[]`)}, 0, "", true},
		{"null payload", KFilter{Name: "device_model", Mode: "accept", Payload: json.RawMessage(`null`)}, 0, "", true},
		{"blank string payload", KFilter{Name: "device_model", Mode: "accept", Payload: json.RawMessage(`""`)}, 0, "", true},
	}
	for _, c := range cases {
		rules, ok, approx, why := oneFilter(c.f, nil)
		if c.skipped {
			if ok || !strings.Contains(why, "device_model") {
				t.Errorf("%s: ok=%v why=%q, want a skipped filter", c.name, ok, why)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: filter not carried: %s", c.name, why)
			continue
		}
		if !approx || !strings.Contains(why, "словарь моделей Keitaro отличается от токена UA") {
			t.Errorf("%s: a carried model filter must say it is approximate: approx=%v why=%q", c.name, approx, why)
		}
		if rules.Model.Flag != c.flag || strings.Join(rules.Model.Values, "|") != c.values {
			t.Errorf("%s: model = %+v", c.name, rules.Model)
		}
		if rules.Brand.Flag != 0 {
			t.Errorf("%s: brand must stay empty: %+v", c.name, rules.Brand)
		}
	}
}

func TestModelFilterApproximatedLoss(t *testing.T) {
	rules, ok, _, losses, _ := mapFilters([]KFilter{
		{Name: "device_model", Mode: "accept", Payload: json.RawMessage(`["SM-S918B"]`)},
	}, nil, false)
	if !ok || rules.Model.Flag != 2 {
		t.Fatalf("model filter not carried: ok=%v %+v", ok, rules.Model)
	}
	if len(losses) != 1 || losses[0].Kind != "approximated" || !strings.Contains(losses[0].Detail, "словарь моделей Keitaro отличается от токена UA") {
		t.Fatalf("losses = %+v", losses)
	}
}

func TestEmptyExactFilterIsReported(t *testing.T) {
	for _, name := range []string{"device_model", "sub_id_4"} {
		_, ok, _, losses, _ := mapFilters([]KFilter{
			{Name: name, Mode: "accept", Payload: json.RawMessage(`[]`)},
			{Name: "country", Mode: "accept", Payload: json.RawMessage(`["pl"]`)},
		}, nil, false)
		if !ok {
			t.Fatalf("%s: the country filter keeps the stream on", name)
		}
		if len(losses) != 1 || losses[0].Kind != "skipped" || !strings.Contains(losses[0].Detail, name) {
			t.Errorf("%s: losses = %+v, want one skipped line", name, losses)
		}
	}
}

func TestModelFilterLoss(t *testing.T) {
	_, _, _, losses, _ := mapFilters([]KFilter{
		{Name: "device_model", Mode: "accept", Payload: json.RawMessage(`["/^Pixel/"]`)},
		{Name: "country", Mode: "accept", Payload: json.RawMessage(`["pl"]`)},
	}, nil, false)
	if len(losses) != 1 || losses[0].Kind != "skipped" || !strings.Contains(losses[0].Detail, "device_model") {
		t.Fatalf("losses = %+v", losses)
	}
}
