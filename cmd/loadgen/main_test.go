package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serverCounts records what the fake engine received.
type serverCounts struct {
	hits     atomic.Int64
	followed atomic.Int64
	missing  atomic.Int64
}

// newCampaignServer answers 302 for redirectCampaign and 200 for every other slug.
// A request to /land means the client followed Location. Counts are updated atomically.
// It returns the server and the counters. Close the server when the test finishes.
func newCampaignServer(redirectCampaign string) (*httptest.Server, *serverCounts) {
	counts := &serverCounts{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/land" {
			counts.followed.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("followed"))
			return
		}
		counts.hits.Add(1)
		q := r.URL.Query()
		if !contains(keywords, q.Get("q")) || !contains(models, q.Get("sub_id_3")) {
			counts.missing.Add(1)
		}
		if strings.TrimPrefix(r.URL.Path, "/") == redirectCampaign {
			w.Header().Set("Location", "http://"+r.Host+"/land")
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte("redirect"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	return srv, counts
}

// contains reports whether v is an element of items.
// It returns false when v is missing or items is empty.
func contains(items []string, v string) bool {
	for _, item := range items {
		if item == v {
			return true
		}
	}
	return false
}

// TestRunPaceStatusAndNoRedirect checks a paced two-campaign run.
// The server must see 200 clicks, split 302/200, with no request to Location.
// Elapsed must cover the pace window. It fails the test on any mismatch.
func TestRunPaceStatusAndNoRedirect(t *testing.T) {
	srv, counts := newCampaignServer("alpha")
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rep, err := run(ctx, Options{
		URL:         srv.URL,
		Campaigns:   []string{"alpha", "beta"},
		N:           200,
		Duration:    200 * time.Millisecond,
		Concurrency: 8,
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts.hits.Load() != 200 || counts.missing.Load() != 0 {
		t.Fatalf("hits=%d missing=%d", counts.hits.Load(), counts.missing.Load())
	}
	if counts.followed.Load() != 0 {
		t.Fatalf("followed redirect %d", counts.followed.Load())
	}
	if rep.Sent != 200 || rep.Errors != 0 || rep.Status[302] != 100 || rep.Status[200] != 100 {
		t.Fatalf("%+v", rep)
	}
	if rep.Campaigns["alpha"] != 100 || rep.Campaigns["beta"] != 100 {
		t.Fatalf("campaigns %+v", rep.Campaigns)
	}
	if rep.Elapsed < 150*time.Millisecond {
		t.Fatalf("elapsed %s", rep.Elapsed)
	}
	started, err := time.Parse(time.RFC3339, rep.Started)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := time.Parse(time.RFC3339, rep.Finished)
	if err != nil {
		t.Fatal(err)
	}
	if started.Location().String() != "UTC" || finished.Before(started) {
		t.Fatalf("started %s finished %s", rep.Started, rep.Finished)
	}
}

// TestEveryRequestHasQuery checks that each click carries q and sub_id_3.
// Duration is zero, so the run does not pace. It fails when any request omits a parameter.
func TestEveryRequestHasQuery(t *testing.T) {
	srv, counts := newCampaignServer("alpha")
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const n = 40
	rep, err := run(ctx, Options{
		URL:         srv.URL,
		Campaigns:   []string{"alpha", "beta"},
		N:           n,
		Duration:    0,
		Concurrency: 4,
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts.hits.Load() != n || counts.missing.Load() != 0 || rep.Sent != n || rep.Errors != 0 {
		t.Fatalf("hits=%d missing=%d sent=%d errors=%d", counts.hits.Load(), counts.missing.Load(), rep.Sent, rep.Errors)
	}
}

// TestValidateRejectsEmptyCampaigns checks the required-flag rules.
// Empty campaigns, n <= 0, and concurrency <= 0 must all fail. It does not start a run.
func TestValidateRejectsEmptyCampaigns(t *testing.T) {
	if err := validate(Options{N: 1, Concurrency: 1}); err == nil {
		t.Fatal("empty campaigns")
	}
	if err := validate(Options{Campaigns: []string{"a"}, N: 0, Concurrency: 1}); err == nil {
		t.Fatal("n")
	}
	if err := validate(Options{Campaigns: []string{"a"}, N: 1, Concurrency: 0}); err == nil {
		t.Fatal("concurrency")
	}
	if _, _, err := parseFlags([]string{"-campaigns", " , "}); err == nil {
		t.Fatal("blank campaigns flag")
	}
}

// TestReportTextHasSentP95AndCodes checks the plain-text summary.
// The text must name sent, p95, and the status codes stored on the report.
func TestReportTextHasSentP95AndCodes(t *testing.T) {
	text := Report{
		Sent:   4,
		Status: map[int]int{200: 3, 302: 1},
		P95:    12 * time.Millisecond,
	}.Text()
	for _, part := range []string{"sent", "p95", "200", "302"} {
		if !strings.Contains(text, part) {
			t.Fatalf("Text()=%q missing %q", text, part)
		}
	}
}

// TestPercentilesKnownSlice checks nearest-rank percentiles on a fixed sample.
// The slice is 10,20,...,100 ms. p50 is 50ms, p95 and p99 are 100ms, max is 100ms.
// A reversed copy must produce the same numbers. An empty slice is all zeros.
func TestPercentilesKnownSlice(t *testing.T) {
	samples := []time.Duration{
		10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond, 50 * time.Millisecond,
		60 * time.Millisecond, 70 * time.Millisecond, 80 * time.Millisecond, 90 * time.Millisecond, 100 * time.Millisecond,
	}
	assertPercentiles(t, samples)
	reversed := make([]time.Duration, len(samples))
	for i := range samples {
		reversed[i] = samples[len(samples)-1-i]
	}
	assertPercentiles(t, reversed)
	p50, p95, p99, slowest := percentiles(nil)
	if p50 != 0 || p95 != 0 || p99 != 0 || slowest != 0 {
		t.Fatalf("empty %s %s %s %s", p50, p95, p99, slowest)
	}
}

// assertPercentiles checks the known 10-element latency slice.
// samples must be a permutation of 10ms..100ms. It fails the test on a mismatch.
func assertPercentiles(t *testing.T, samples []time.Duration) {
	t.Helper()
	p50, p95, p99, slowest := percentiles(samples)
	if p50 != 50*time.Millisecond || p95 != 100*time.Millisecond || p99 != 100*time.Millisecond || slowest != 100*time.Millisecond {
		t.Fatalf("p50=%s p95=%s p99=%s max=%s", p50, p95, p99, slowest)
	}
}
