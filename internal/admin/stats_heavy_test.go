package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/hasayone/empataso-tds/internal/store"
)

// heavyStats fails the dashboard and report reads with err.
type heavyStats struct {
	fakeStats
	err error
}

func (h heavyStats) Summary(context.Context, time.Time, time.Time) (store.StatsSummary, error) {
	return store.StatsSummary{}, h.err
}

func (h heavyStats) Performance(context.Context, time.Time, time.Time) ([]store.PerfRow, error) {
	return nil, h.err
}

func (h heavyStats) Slice(context.Context, time.Time, time.Time) ([]store.SliceRow, error) {
	return nil, h.err
}

func (h heavyStats) KeywordStats(context.Context, time.Time, time.Time, []store.Cond, store.KeywordMode) (store.KeywordStats, error) {
	return store.KeywordStats{}, h.err
}

func (h heavyStats) KeywordTrend(context.Context, time.Time, time.Time, []store.Cond, store.KeywordTrendParams) (store.KeywordTrend, error) {
	return store.KeywordTrend{}, h.err
}

func TestWriteStatsErr(t *testing.T) {
	cases := []struct {
		err      error
		status   int
		msg      string
		wantCode string
	}{
		{&clickhouse.Exception{Code: 159}, http.StatusGatewayTimeout, msgQueryTooHeavy, "query_too_heavy"},
		{&clickhouse.Exception{Code: 241}, http.StatusGatewayTimeout, msgQueryTooHeavy, "query_too_heavy"},
		{errors.New("boom"), http.StatusInternalServerError, "stats error", "internal"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeStatsErr(rec, c.err, "stats error")
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != c.status || body["error"] != c.msg || body["code"] != c.wantCode {
			t.Errorf("%v: %d %v", c.err, rec.Code, body)
		}
	}
}

func TestStatsRoutesAnswerQueryTooHeavy(t *testing.T) {
	heavy := heavyStats{err: &clickhouse.Exception{Code: 159, Message: "Timeout exceeded"}}
	base, c := serverWithStats(t, heavy)
	for _, path := range []string{"/api/stats/summary", "/api/stats/performance", "/api/report", "/api/keywords/stats", "/api/keywords/trend"} {
		resp, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var body map[string]string
		_ = json.Unmarshal(b, &body)
		if resp.StatusCode != http.StatusGatewayTimeout || body["code"] != "query_too_heavy" {
			t.Errorf("%s: %d %s", path, resp.StatusCode, b)
		}
	}
	base, c = serverWithStats(t, heavyStats{err: errors.New("connection reset")})
	resp, err := c.Get(base + "/api/stats/summary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("other errors stay 500, got %d", resp.StatusCode)
	}
	for _, path := range []string{"/api/keywords/stats", "/api/keywords/trend"} {
		resp, err = c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: another failure is 500 and never 200 with zeros, got %d", path, resp.StatusCode)
		}
	}
}
