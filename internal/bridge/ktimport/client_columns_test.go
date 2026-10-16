package ktimport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFlexString(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`42`, "42"},
		{`"abc"`, "abc"},
		{`null`, ""},
		{`{"a":1}`, ""},
		{`[1]`, ""},
		{`true`, ""},
	}
	for _, tc := range cases {
		var got FlexString
		if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if string(got) != tc.want {
			t.Fatalf("%s -> %q", tc.in, got)
		}
	}
}

func TestCanonParamExtras(t *testing.T) {
	for _, key := range []string{"site", "ad_campaign_id", "creative_id", "external_id", "keyword"} {
		got, ok := canonParam(key)
		if !ok || got != key {
			t.Fatalf("canonParam(%q) = %q %v", key, got, ok)
		}
	}
	if _, ok := canonParam("cost"); ok {
		t.Fatal("cost accepted")
	}
}

func TestFetchExtendedColumnsFallback(t *testing.T) {
	var convSets [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Columns []string `json:"columns"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("json %v", err)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		wide := false
		switch r.URL.Path {
		case "/admin_api/v1/conversions/log":
			convSets = append(convSets, append([]string(nil), req.Columns...))
			wide = containsCol(req.Columns, "previous_status")
		case "/admin_api/v1/clicks/log":
			wide = containsCol(req.Columns, "ad_campaign_id")
		default:
			http.NotFound(w, r)
			return
		}
		if wide {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/admin_api/v1/conversions/log" {
			_, _ = io.WriteString(w, `{"rows":[{"sub_id":"c1","tid":42,"ad_campaign_id":"77"}],"total":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"rows":[{"sub_id":"k1","external_id":9}],"total":1}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	var conv ConversionRow
	n, err := c.FetchConversions(context.Background(), "2026-01-01 00:00:00", "2026-01-02 00:00:00", func(row ConversionRow) error {
		conv = row
		return nil
	})
	if err != nil || n != 1 || !c.ExtendedColumnsRejected() || string(conv.TID) != "42" || string(conv.AdCampaignID) != "77" {
		t.Fatalf("n=%d rejected=%v row=%+v err=%v", n, c.ExtendedColumnsRejected(), conv, err)
	}
	if len(convSets) != 2 || containsCol(convSets[1], "previous_status") || !containsCol(convSets[0], "previous_status") {
		t.Fatalf("column sets %v", convSets)
	}
	var click ClickRow
	n, err = c.FetchClicks(context.Background(), "2026-01-01 00:00:00", "2026-01-02 00:00:00", func(row ClickRow) error {
		click = row
		return nil
	})
	if err != nil || n != 1 || !c.ExtendedColumnsRejected() || string(click.ExternalID) != "9" {
		t.Fatalf("clicks n=%d rejected=%v row=%+v err=%v", n, c.ExtendedColumnsRejected(), click, err)
	}
}

func TestFetchExtendedColumnsAccepted(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"rows":[],"total":0}`)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	n, err := c.FetchConversions(context.Background(), "2026-01-01 00:00:00", "2026-01-02 00:00:00", func(ConversionRow) error {
		t.Fatal("row")
		return nil
	})
	if err != nil || n != 0 || calls != 1 || c.ExtendedColumnsRejected() {
		t.Fatalf("n=%d calls=%d rejected=%v err=%v", n, calls, c.ExtendedColumnsRejected(), err)
	}
}
