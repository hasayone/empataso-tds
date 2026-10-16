package ktimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type denyTrip struct{}

// RoundTrip не должен вызываться, если allowlist отклонил запрос.
//
// Параметры: req — запрос.
// Результат: нет.
// Ошибки: всегда, чтобы тест увидел обход проверки.
func (denyTrip) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errCalled
}

var errCalled = errString("transport called")

type errString string

// Error возвращает текст.
//
// Параметры: нет.
// Результат: строка ошибки.
// Ошибки: нет.
func (e errString) Error() string { return string(e) }

func TestAllowRejectsPostAndLogs(t *testing.T) {
	c := &Client{Base: "https://tracker.example", Key: "secret", HTTP: &http.Client{Transport: denyTrip{}}}
	for _, path := range []string{
		"/admin_api/v1/report/build",
		"/admin_api/v1/clicks/log",
		"/admin_api/v1/conversions/log",
	} {
		_, err := c.Get(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("%s: %v", path, err)
		}
	}
	_, err := c.Get(context.Background(), "/admin_api/v1/campaigns")
	if err == nil || !strings.Contains(err.Error(), "не выполнен") {
		t.Fatalf("allowed GET did not reach transport: %v", err)
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/landing_pages/1/download"); err != nil {
		t.Fatal(err)
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/landing_pages/1/preview"); err == nil {
		t.Fatal("preview allowed")
	}
	_, err = c.Get(context.Background(), "/admin_api/v1/landing_pages/1/download")
	if err == nil || !strings.Contains(err.Error(), "не выполнен") {
		t.Fatalf("download: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://tracker.example/admin_api/v1/campaigns", nil)
	if err := Allow(req.Method, req.URL.Path); err == nil {
		t.Fatal("POST allowed")
	}
}

func TestAllowLog(t *testing.T) {
	for _, path := range []string{"/admin_api/v1/clicks/log", "/admin_api/v1/conversions/log"} {
		if err := AllowLog(http.MethodPost, path); err != nil {
			t.Fatal(path, err)
		}
	}
	if err := AllowLog(http.MethodPost, "/admin_api/v1/campaigns"); err == nil {
		t.Fatal("POST campaigns allowed")
	}
	if err := AllowLog(http.MethodPost, "/admin_api/v1/report/build"); err == nil {
		t.Fatal("POST report allowed")
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/affiliate_networks/2"); err != nil {
		t.Fatal(err)
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/traffic_sources/2"); err != nil {
		t.Fatal(err)
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/domains/2"); err != nil {
		t.Fatal(err)
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/groups"); err != nil {
		t.Fatal(err)
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/groups?type=offers"); err != nil {
		t.Fatal(err)
	}
	if err := Allow(http.MethodGet, "/admin_api/v1/groups/1"); err == nil {
		t.Fatal("group id allowed")
	}
	c := &Client{Base: "https://tracker.example", Key: "secret", HTTP: &http.Client{Transport: denyTrip{}}}
	if _, err := c.postLog(context.Background(), "/admin_api/v1/campaigns", map[string]int{}); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatal(err)
	}
	if _, err := c.postLog(context.Background(), "/admin_api/v1/report/build", map[string]int{}); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatal(err)
	}
}

func TestFetchLogsPages(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Api-Key") != "k-test" {
			t.Errorf("api key %q", r.Header.Get("Api-Key"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type %s", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Limit   int      `json:"limit"`
			Offset  int      `json:"offset"`
			Columns []string `json:"columns"`
			Sort    []struct {
				Name  string `json:"name"`
				Order string `json:"order"`
			} `json:"sort"`
			Range struct {
				Timezone string `json:"timezone"`
			} `json:"range"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.Limit != logPageLimit || req.Range.Timezone != "UTC" || !containsCol(req.Columns, "sub_id_12") {
			t.Fatalf("req %+v", req)
		}
		wantSort := "datetime"
		if r.URL.Path == "/admin_api/v1/conversions/log" {
			wantSort = "postback_datetime"
		}
		if len(req.Sort) != 1 || req.Sort[0].Name != wantSort || req.Sort[0].Order != "asc" {
			t.Fatalf("sort %+v", req.Sort)
		}
		if containsCol(req.Columns, "payout") || containsCol(req.Columns, "country") {
			t.Fatalf("forbidden column %v", req.Columns)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/conversions/log":
			if !containsCol(req.Columns, "cost") || !containsCol(req.Columns, "country_code") || !containsCol(req.Columns, "affiliate_network_id") {
				t.Fatalf("conversion columns %v", req.Columns)
			}
			if req.Offset == 0 {
				_, _ = io.WriteString(w, `{"rows":`+logRows(logPageLimit, `{"sub_id":"c0","status":"sale","revenue":"1.5","cost":"0.2","campaign_id":"7","stream_id":11,"offer_id":101,"landing_id":3,"affiliate_network_id":2,"ts_id":"5","country_code":"ZA","city":"Johannesburg","region":"Gauteng","device_type":"mobile","os":"Android","browser":"Chrome","operator":"Vodacom","ip":"1.2.3.4","user_agent":"Mozilla","postback_datetime":"2026-01-01 01:00:00","click_datetime":"2026-01-01 00:00:00","sub_id_3":"z"}`)+`,"total":"10001"}`)
				return
			}
			if req.Offset == logPageLimit {
				_, _ = io.WriteString(w, `{"rows":[{"sub_id":"c-last","revenue":"2","campaign_id":9}],"total":10001}`)
				return
			}
			t.Errorf("extra conversions offset %d", req.Offset)
		case "/admin_api/v1/clicks/log":
			if !containsCol(req.Columns, "user_agent") || !containsCol(req.Columns, "is_unique_campaign") || !containsCol(req.Columns, "destination") || !containsCol(req.Columns, "search_engine") {
				t.Fatalf("click columns %v", req.Columns)
			}
			_, _ = io.WriteString(w, `{"rows":[{"sub_id":"k1","country_code":"ZA","is_bot":false,"is_unique_campaign":true,"affiliate_network_id":"4","ts_id":8,"operator":"Vodacom","search_engine":"Google","destination":"https://land.example/","cost":"3.25","sub_id_1":"a"},{"sub_id":"k2","is_bot":false,"is_unique_campaign":false}],"total":2}`)
		default:
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	var progress []string
	c.Progress = func(stage string, done, total int) {
		progress = append(progress, fmt.Sprintf("%s %d %d", stage, done, total))
	}
	var got ConversionRow
	n, err := c.FetchConversions(context.Background(), "2026-01-01 00:00:00", "2026-01-02 00:00:00", func(row ConversionRow) error {
		if row.SubID == "c0" {
			got = row
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != logPageLimit+1 || got.CampaignID != 7 || got.Revenue != 1.5 || got.Cost != 0.2 || got.Subs[2] != "z" {
		t.Fatalf("n=%d row=%+v", n, got)
	}
	if got.CountryCode != "ZA" || got.NetworkID != 2 || got.SourceID != 5 || got.Status != "sale" || got.Operator != "Vodacom" {
		t.Fatalf("conversion fields %+v", got)
	}
	var bot ClickRow
	n, err = c.FetchClicks(context.Background(), "2026-01-01 00:00:00", "2026-01-02 00:00:00", func(row ClickRow) error {
		if row.SubID == "k1" {
			bot = row
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || bot.IsBot || !bot.IsUnique || bot.Cost != 3.25 || bot.Subs[0] != "a" || bot.CountryCode != "ZA" || bot.NetworkID != 4 || bot.SourceID != 8 || bot.Destination == "" || bot.SearchEngine != "Google" {
		t.Fatalf("clicks n=%d row=%+v", n, bot)
	}
	if calls != 3 {
		t.Fatalf("calls %d", calls)
	}
	if !strings.Contains(strings.Join(progress, "\n"), fmt.Sprintf("conversions %d 10001", logPageLimit+1)) || !strings.Contains(strings.Join(progress, "\n"), "clicks 2 2") {
		t.Fatalf("progress %v", progress)
	}
}

func TestFetchSnapshotOptional(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = io.WriteString(w, `[{"id":1,"alias":"a","name":"A"}]`)
		case "/admin_api/v1/campaigns/1/streams":
			_, _ = io.WriteString(w, `[]`)
		case "/admin_api/v1/offers", "/admin_api/v1/landing_pages", "/admin_api/v1/groups",
			"/admin_api/v1/stream_schemas", "/admin_api/v1/stream_types", "/admin_api/v1/stream_filters", "/admin_api/v1/streams_actions":
			_, _ = io.WriteString(w, `[]`)
		case "/admin_api/v1/affiliate_networks":
			_, _ = io.WriteString(w, `[{"id":"2","name":"n","state":"active"}]`)
		case "/admin_api/v1/traffic_sources":
			http.NotFound(w, r)
		case "/admin_api/v1/domains":
			http.Error(w, "no", http.StatusInternalServerError)
		default:
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	snap, err := c.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.AffiliateNetworks) != 1 || snap.AffiliateNetworks[0].ID != 2 || len(snap.TrafficSources) != 0 || len(snap.Domains) != 0 {
		t.Fatalf("%+v", snap)
	}
	n, err := c.CountCampaigns(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("count %d %v", n, err)
	}
}

func logRows(n int, first string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if i == 0 {
			b.WriteString(first)
			continue
		}
		fmt.Fprintf(&b, `{"sub_id":"r%d","revenue":"1","campaign_id":"3"}`, i)
	}
	b.WriteByte(']')
	return b.String()
}

func containsCol(cols []string, want string) bool {
	for _, c := range cols {
		if c == want {
			return true
		}
	}
	return false
}

func TestRequireLiveEnvEmpty(t *testing.T) {
	t.Setenv("KEITARO_BASE_URL", "")
	t.Setenv("KEITARO_API_KEY", "")
	if _, _, err := RequireLiveEnv(); err == nil {
		t.Fatal("empty env accepted")
	}
}

func TestFetchLandingArchive(t *testing.T) {
	var accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		if r.URL.Path != "/admin_api/v1/landing_pages/701/download" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Api-Key") != "k-test" {
			t.Errorf("api key %q", r.Header.Get("Api-Key"))
		}
		_, _ = w.Write([]byte("PK\x03\x04zip-body"))
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	body, err := c.FetchLandingArchive(context.Background(), 701)
	if err != nil || string(body) != "PK\x03\x04zip-body" {
		t.Fatalf("%v %q", err, body)
	}
	if accept != "*/*" {
		t.Fatalf("accept %s", accept)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "{not-archive}", http.StatusNotFound)
	}))
	defer bad.Close()
	c = &Client{Base: bad.URL, Key: "k-test", HTTP: bad.Client()}
	if _, err := c.FetchLandingArchive(context.Background(), 701); err == nil {
		t.Fatal("404 accepted")
	}
}

func TestMaskURL(t *testing.T) {
	got := MaskURL("https://offer.example/path?token=abc&subid=1#frag")
	if strings.Contains(got, "abc") || strings.Contains(got, "frag") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "offer.example") || !strings.Contains(got, "token=***") {
		t.Fatal(got)
	}
}
