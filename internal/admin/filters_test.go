package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/store"
)

func TestParseCondsGroups(t *testing.T) {
	q := url.Values{}
	q.Add("f", "os:eq:Android")
	q.Add("f", "country:eq:PL")
	q.Add("f", "country:eq:CZ")
	q.Add("f", "device:ne:bot")
	q.Add("f", "device:ne:tv")
	conds, err := parseConds(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(conds) != 3 || conds[0].Field != "os" || conds[0].Op != "eq" || len(conds[0].Values) != 1 {
		t.Fatalf("order %+v", conds)
	}
	if conds[1].Field != "country" || conds[1].Op != "in" || strings.Join(conds[1].Values, ",") != "PL,CZ" {
		t.Fatalf("in %+v", conds[1])
	}
	if conds[2].Field != "device" || conds[2].Op != "nin" || strings.Join(conds[2].Values, ",") != "bot,tv" {
		t.Fatalf("nin %+v", conds[2])
	}

	empty := url.Values{}
	empty.Add("f", "country:eq:")
	conds, err = parseConds(empty)
	if err != nil || len(conds) != 1 || conds[0].Op != "eq" || len(conds[0].Values) != 1 || conds[0].Values[0] != "" {
		t.Fatalf("empty %+v %v", conds, err)
	}
	if _, err := parseConds(url.Values{"f": {"country:eq"}}); err == nil {
		t.Fatal("short filter accepted")
	}
	if _, err := parseConds(url.Values{"f": {"country:gt:1"}}); err == nil {
		t.Fatal("bad operator accepted")
	}
}

// filterStats records the filters the journal handlers pass to the store.
type filterStats struct {
	fakeStats
	lastLog   store.LogFilter
	logs      int
	pb        store.PostbackQuery
	sliceN    int
	filtered  []store.Cond
	filteredN int
	valLimit  int
}

func (f *filterStats) Logs(_ context.Context, lf store.LogFilter) ([]store.LogRow, int64, error) {
	f.lastLog = lf
	f.logs++
	return []store.LogRow{{IP: "1.2.3.4"}}, 1, nil
}

func (f *filterStats) QueryPostbacks(_ context.Context, q store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error) {
	f.pb = q
	return []store.PostbackRow{{Profit: 1}}, store.PostbackTotals{Profit: 1, Count: 1}, nil
}

func (f *filterStats) Slice(_ context.Context, _, _ time.Time) ([]store.SliceRow, error) {
	f.sliceN++
	return []store.SliceRow{}, nil
}

func (f *filterStats) SliceFiltered(_ context.Context, _, _ time.Time, conds []store.Cond) ([]store.SliceRow, error) {
	f.filteredN++
	f.filtered = append([]store.Cond(nil), conds...)
	return []store.SliceRow{}, nil
}

func (f *filterStats) Values(ctx context.Context, q store.ValuesQuery) ([]store.KV, error) {
	f.valLimit = q.Limit
	return f.fakeStats.Values(ctx, q)
}

func (f *filterStats) Breakdown(ctx context.Context, from, to time.Time, dim string, limit int) ([]store.KV, error) {
	switch dim {
	case "late":
		return nil, store.ErrFieldNotReady
	case "nope":
		return nil, store.ErrUnknownField
	default:
		return f.fakeStats.Breakdown(ctx, from, to, dim, limit)
	}
}

func TestLogFieldsAndValues(t *testing.T) {
	st := &filterStats{}
	base, c := serverWithStats(t, st)

	resp, err := c.Get(base + "/api/log-fields")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log-fields %d", resp.StatusCode)
	}
	var info store.FieldsInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if len(info.Fields) == 0 || info.Ready == nil {
		t.Fatalf("fields %+v", info)
	}

	resp, err = c.Get(base + "/api/log-values?table=events&field=ip")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "bad filter") {
		t.Fatalf("ip %d %s", resp.StatusCode, body)
	}

	resp, err = c.Get(base + "/api/log-values?table=events&field=country&limit=9999")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("country %d", resp.StatusCode)
	}
	var vals []store.KV
	if err := json.NewDecoder(resp.Body).Decode(&vals); err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 || vals[0].Key != "PL" || st.valLimit != 500 {
		t.Fatalf("values %+v limit %d", vals, st.valLimit)
	}
}

func TestLogFilterPassesConds(t *testing.T) {
	st := &filterStats{}
	base, c := serverWithStats(t, st)

	resp, err := c.Get(base + "/api/logs?group=demo&f=country:in:PL&f=country:in:CZ")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || st.logs != 1 {
		t.Fatalf("logs %d calls %d", resp.StatusCode, st.logs)
	}
	if len(st.lastLog.Conds) != 1 || st.lastLog.Conds[0].Field != "country" || st.lastLog.Conds[0].Op != "in" || strings.Join(st.lastLog.Conds[0].Values, ",") != "PL,CZ" {
		t.Fatalf("conds %+v", st.lastLog.Conds)
	}
	if len(st.lastLog.Group) != 1 || st.lastLog.Group[0] != "demo" {
		t.Fatalf("group %+v", st.lastLog.Group)
	}

	for _, path := range []string{"/api/logs?f=nope:eq:1", "/api/logs?f=country:gt:1", "/api/logs?f=country:eq"} {
		resp, err = c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "bad filter") {
			t.Fatalf("%s → %d %s", path, resp.StatusCode, b)
		}
	}
	if st.logs != 1 {
		t.Fatalf("bad filters reached the store %d", st.logs)
	}

	resp, err = c.Get(base + "/api/postbacks?offset=50&status=sale")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || st.pb.Offset != 50 || st.pb.Status != "sale" {
		t.Fatalf("postbacks %d %+v", resp.StatusCode, st.pb)
	}

	resp, err = c.Get(base + "/api/report")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || st.sliceN != 1 || st.filteredN != 0 {
		t.Fatalf("plain report slice %d filtered %d status %d", st.sliceN, st.filteredN, resp.StatusCode)
	}

	resp, err = c.Get(base + "/api/report?f=country:eq:PL")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || st.filteredN != 1 || st.sliceN != 1 {
		t.Fatalf("filtered report slice %d filtered %d status %d", st.sliceN, st.filteredN, resp.StatusCode)
	}
	if len(st.filtered) != 1 || st.filtered[0].Field != "country" || st.filtered[0].Op != "eq" || strings.Join(st.filtered[0].Values, ",") != "PL" {
		t.Fatalf("report conds %+v", st.filtered)
	}

	resp, err = c.Get(base + "/api/report?f=nope:eq:1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || st.filteredN != 1 {
		t.Fatalf("bad report %d filtered %d", resp.StatusCode, st.filteredN)
	}

	resp, err = c.Get(base + "/api/stats/breakdown?dim=late")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("not ready %d %s", resp.StatusCode, b)
	}
	resp, err = c.Get(base + "/api/stats/breakdown?dim=nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown dim %d", resp.StatusCode)
	}
}

// dashFilterStats records filtered vs plain calls on dashboard stats endpoints.
type dashFilterStats struct {
	fakeStats
	sumN, sumFN     int
	tsN, tsFN       int
	perfN, perfFN   int
	factsN, factsFN int
	lastConds       []store.Cond
}

func (d *dashFilterStats) Summary(ctx context.Context, from, to time.Time) (store.StatsSummary, error) {
	d.sumN++
	d.lastConds = nil
	return d.fakeStats.Summary(ctx, from, to)
}

func (d *dashFilterStats) SummaryFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) (store.StatsSummary, error) {
	d.sumFN++
	d.lastConds = append([]store.Cond(nil), conds...)
	return d.fakeStats.SummaryFiltered(ctx, from, to, conds)
}

func (d *dashFilterStats) TimeSeries(ctx context.Context, from, to time.Time, step int) ([]store.TSPoint, error) {
	d.tsN++
	d.lastConds = nil
	return d.fakeStats.TimeSeries(ctx, from, to, step)
}

func (d *dashFilterStats) TimeSeriesFiltered(ctx context.Context, from, to time.Time, step int, conds []store.Cond) ([]store.TSPoint, error) {
	d.tsFN++
	d.lastConds = append([]store.Cond(nil), conds...)
	return d.fakeStats.TimeSeriesFiltered(ctx, from, to, step, conds)
}

func (d *dashFilterStats) Performance(ctx context.Context, from, to time.Time) ([]store.PerfRow, error) {
	d.perfN++
	d.lastConds = nil
	return d.fakeStats.Performance(ctx, from, to)
}

func (d *dashFilterStats) PerformanceFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) ([]store.PerfRow, error) {
	d.perfFN++
	d.lastConds = append([]store.Cond(nil), conds...)
	return d.fakeStats.PerformanceFiltered(ctx, from, to, conds)
}

func (d *dashFilterStats) ClickFacts(ctx context.Context, from, to time.Time) (store.Facts, error) {
	d.factsN++
	d.lastConds = nil
	return d.fakeStats.ClickFacts(ctx, from, to)
}

func (d *dashFilterStats) ClickFactsFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) (store.Facts, error) {
	d.factsFN++
	d.lastConds = append([]store.Cond(nil), conds...)
	return d.fakeStats.ClickFactsFiltered(ctx, from, to, conds)
}

func assertMobileConds(t *testing.T, conds []store.Cond) {
	t.Helper()
	if len(conds) != 1 || conds[0].Field != "device" || conds[0].Op != "eq" || strings.Join(conds[0].Values, ",") != "mobile" {
		t.Fatalf("conds %+v", conds)
	}
}

func TestDashboardStatsFilter(t *testing.T) {
	st := &dashFilterStats{}
	base, c := serverWithStats(t, st)
	filter := "f=device:eq:mobile"

	paths := []struct {
		path  string
		check func(t *testing.T)
	}{
		{"/api/stats/summary?" + filter, func(t *testing.T) {
			if st.sumFN != 1 || st.sumN != 0 {
				t.Fatalf("summary filtered %d plain %d", st.sumFN, st.sumN)
			}
			assertMobileConds(t, st.lastConds)
		}},
		{"/api/stats/timeseries?" + filter, func(t *testing.T) {
			if st.tsFN != 1 || st.tsN != 0 {
				t.Fatalf("timeseries filtered %d plain %d", st.tsFN, st.tsN)
			}
			assertMobileConds(t, st.lastConds)
		}},
		{"/api/stats/performance?" + filter, func(t *testing.T) {
			if st.perfFN != 1 || st.perfN != 0 {
				t.Fatalf("performance filtered %d plain %d", st.perfFN, st.perfN)
			}
			assertMobileConds(t, st.lastConds)
		}},
		{"/api/stats/facts?" + filter, func(t *testing.T) {
			if st.factsFN != 1 || st.factsN != 0 {
				t.Fatalf("facts filtered %d plain %d", st.factsFN, st.factsN)
			}
			assertMobileConds(t, st.lastConds)
		}},
	}

	for _, p := range paths {
		resp, err := c.Get(base + p.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s filtered %d %s", p.path, resp.StatusCode, body)
		}
		p.check(t)
	}

	badPaths := []string{
		"/api/stats/summary?f=nope:eq:1",
		"/api/stats/timeseries?f=nope:eq:1",
		"/api/stats/performance?f=nope:eq:1",
		"/api/stats/facts?f=nope:eq:1",
	}
	for _, path := range badPaths {
		resp, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s bad f → %d %s", path, resp.StatusCode, body)
		}
	}

	plainPaths := []struct {
		path  string
		check func(t *testing.T)
	}{
		{"/api/stats/summary", func(t *testing.T) {
			if st.sumN != 1 || st.sumFN != 1 {
				t.Fatalf("summary plain %d filtered %d", st.sumN, st.sumFN)
			}
		}},
		{"/api/stats/timeseries", func(t *testing.T) {
			if st.tsN != 1 || st.tsFN != 1 {
				t.Fatalf("timeseries plain %d filtered %d", st.tsN, st.tsFN)
			}
		}},
		{"/api/stats/performance", func(t *testing.T) {
			if st.perfN != 1 || st.perfFN != 1 {
				t.Fatalf("performance plain %d filtered %d", st.perfN, st.perfFN)
			}
		}},
		{"/api/stats/facts", func(t *testing.T) {
			if st.factsN != 1 || st.factsFN != 1 {
				t.Fatalf("facts plain %d filtered %d", st.factsN, st.factsFN)
			}
		}},
	}
	for _, p := range plainPaths {
		resp, err := c.Get(base + p.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s plain %d", p.path, resp.StatusCode)
		}
		p.check(t)
	}
}

func TestBreakdownFilter(t *testing.T) {
	st := &recordingStats{}
	base, c := serverWithStats(t, st)

	resp, err := c.Get(base + "/api/stats/breakdown?dim=country&f=device:eq:mobile")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered %d %s", resp.StatusCode, body)
	}
	if st.bdFilteredN != 1 || st.bdN != 0 || st.bdDim != "country" {
		t.Fatalf("filtered calls %d plain %d dim %s", st.bdFilteredN, st.bdN, st.bdDim)
	}
	if len(st.bdConds) != 1 || st.bdConds[0].Field != "device" || st.bdConds[0].Op != "eq" || strings.Join(st.bdConds[0].Values, ",") != "mobile" {
		t.Fatalf("conds %+v", st.bdConds)
	}

	resp, err = c.Get(base + "/api/stats/breakdown?dim=country&f=nope:eq:1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || st.bdFilteredN != 1 || st.bdN != 0 {
		t.Fatalf("bad f %d filtered %d plain %d %s", resp.StatusCode, st.bdFilteredN, st.bdN, body)
	}

	resp, err = c.Get(base + "/api/stats/breakdown?dim=country")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || st.bdN != 1 || st.bdFilteredN != 1 {
		t.Fatalf("plain %d calls %d filtered %d", resp.StatusCode, st.bdN, st.bdFilteredN)
	}
}
