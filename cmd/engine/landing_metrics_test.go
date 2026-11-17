package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

// The latency buckets have inclusive upper edges, and the failure classes do
// not overlap: a visitor who left is no failure of PHP.
func TestLandingStatsObserve(t *testing.T) {
	cases := []struct {
		name   string
		took   time.Duration
		status int
		err    error
		want   config.LandingMetrics
	}{
		{"fast ok", 100 * time.Millisecond, 200, nil, config.LandingMetrics{Requests: 1, LatencyLe100: 1, LatencySumMs: 100}},
		{"under a second", 101 * time.Millisecond, 200, nil, config.LandingMetrics{Requests: 1, LatencyLe1s: 1, LatencySumMs: 101}},
		{"five seconds", 5 * time.Second, 404, nil, config.LandingMetrics{Requests: 1, LatencyLe5s: 1, LatencySumMs: 5000}},
		{"slow 5xx", 6 * time.Second, 503, nil, config.LandingMetrics{Requests: 1, Status5xx: 1, LatencyGt5s: 1, LatencySumMs: 6000}},
		{"4xx is no failure", time.Millisecond, 499, nil, config.LandingMetrics{Requests: 1, LatencyLe100: 1, LatencySumMs: 1}},
		{"deadline", 10 * time.Second, 0, context.DeadlineExceeded, config.LandingMetrics{Requests: 1, Timeouts: 1, LatencyGt5s: 1, LatencySumMs: 10000}},
		{"refused", time.Millisecond, 0, errors.New("connection refused"), config.LandingMetrics{Requests: 1, ConnErrors: 1, LatencyLe100: 1, LatencySumMs: 1}},
		{"visitor left", 50 * time.Millisecond, 0, context.Canceled, config.LandingMetrics{Requests: 1, LatencyLe100: 1, LatencySumMs: 50}},
		{"status is ignored with an error", time.Millisecond, 502, errors.New("reset"), config.LandingMetrics{Requests: 1, ConnErrors: 1, LatencyLe100: 1, LatencySumMs: 1}},
	}
	for _, c := range cases {
		var s landingStats
		s.observe(c.took, c.status, c.err)
		if got := s.snapshot(); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// proxyPHP counts every request it sends: an answer, a 5xx, a timeout and a
// connection that nobody listens on. A request to a disabled PHP is not sent
// and not counted.
func TestProxyPHPCountsRequests(t *testing.T) {
	release := make(chan struct{})
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ok.php"):
			_, _ = w.Write([]byte("ok"))
		case strings.HasSuffix(r.URL.Path, "/boom.php"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/hang.php"):
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}))
	defer php.Close()
	defer close(release)

	srv := newLandingServer(t.TempDir(), php.URL, nil, discardLog())
	srv.client.Timeout = 100 * time.Millisecond
	for _, name := range []string{"ok.php", "boom.php", "hang.php"} {
		_, _ = srv.proxyPHP(nil, "7", name, "203.0.113.8", "cid", "")
	}
	got := srv.stats.snapshot()
	if got.Requests != 3 || got.Status5xx != 1 || got.Timeouts != 1 || got.ConnErrors != 0 {
		t.Fatalf("after ok, 5xx and a hang: %+v", got)
	}
	if got.LatencyGt5s != 0 || got.LatencyLe100+got.LatencyLe1s != 3 {
		t.Fatalf("latency buckets %+v, want all three requests under 5 s", got)
	}

	php.Close()
	if _, err := srv.proxyPHP(nil, "7", "ok.php", "", "", ""); err == nil {
		t.Fatal("a closed origin must fail")
	}
	if got = srv.stats.snapshot(); got.Requests != 4 || got.ConnErrors != 1 {
		t.Fatalf("after a closed origin: %+v", got)
	}

	off := newLandingServer(t.TempDir(), "", nil, discardLog())
	if _, err := off.proxyPHP(nil, "7", "ok.php", "", "", ""); !errors.Is(err, errPHPOff) {
		t.Fatalf("disabled php: %v", err)
	}
	if got := off.stats.snapshot(); got != (config.LandingMetrics{}) {
		t.Fatalf("a request that was not sent is counted: %+v", got)
	}
}

// The snapshot of the engine carries the landing counters, and its JSON uses the
// names other processes read.
func TestMetricsSnapshotHasLanding(t *testing.T) {
	d := &engineDeps{landServe: newLandingServer(t.TempDir(), "http://php", nil, discardLog())}
	d.landServe.stats.observe(2*time.Second, 502, nil)
	now := time.Now()
	m := d.metricsSnapshot("engine", now, now)
	if m.Landing.Requests != 1 || m.Landing.Status5xx != 1 || m.Landing.LatencyLe5s != 1 {
		t.Fatalf("landing part %+v", m.Landing)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"landing":{`, `"requests":1`, `"status_5xx":1`, `"latency_le_5s":1`, `"latency_sum_ms":2000`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("%s missing in %s", key, raw)
		}
	}
	if m := (*engineDeps)(nil).metricsSnapshot("engine", now, now); m.Landing != (config.LandingMetrics{}) {
		t.Fatalf("a nil engine reports %+v", m.Landing)
	}
}
