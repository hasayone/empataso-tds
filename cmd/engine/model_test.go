package main

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// modelEvent serves one request to the stop group g1 and returns the logged model.
func modelEvent(t *testing.T, headers map[string]string) string {
	t.Helper()
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop",
		Streams: []config.Stream{{
			Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "ok"},
		}},
	})
	h, ins, stop := curlEnv(t, g, "0")
	do(t, h, "/g1", "8.8.8.8", headers)
	stop()
	if len(ins.events) != 1 {
		t.Fatalf("events %+v", ins.events)
	}
	return ins.events[0].DeviceModel
}

func TestEventCarriesModelFromUserAgent(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 13; SM-S918B Build/TP1A.220624.014) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36"
	if got := modelEvent(t, map[string]string{"User-Agent": ua}); got != "SM-S918B" {
		t.Fatalf("model = %q", got)
	}
}

func TestEventModelHintWinsOverHiddenUserAgent(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36"
	got := modelEvent(t, map[string]string{"User-Agent": ua, "Sec-CH-UA-Model": `"Pixel 8"`})
	if got != "Pixel 8" {
		t.Fatalf("model = %q", got)
	}
}

func TestEventModelEmptyWhenHidden(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36"
	if got := modelEvent(t, map[string]string{"User-Agent": ua, "Sec-CH-UA-Model": `""`}); got != "" {
		t.Fatalf("model = %q, want empty", got)
	}
}
