package ktimport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestKeitaroVersionKeys проверяет имена ключей версий и выбор ключей по строке без версии.
func TestKeitaroVersionKeys(t *testing.T) {
	if k := KeysFor(Keitaro10); k.Click != "click_id" || k.Conversion != "conversion_id" || k.Type != "" {
		t.Errorf("keitaro 10: %+v", k)
	}
	if k := KeysFor(Keitaro11); k.Click != "event_id" || k.Conversion != "event_id" || k.Type != "event_type" {
		t.Errorf("keitaro 11: %+v", k)
	}
	if k := KeysFor(""); k != (IDKeys{}) {
		t.Errorf("неизвестная версия: %+v", k)
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"десятая", `{"click_id":"a","conversion_id":"b"}`, Keitaro10},
		{"одиннадцатая", `{"event_id":"a","event_type":"b"}`, Keitaro11},
		{"только event_id", `{"event_id":"a"}`, Keitaro11},
		{"только conversion_id", `{"conversion_id":"a"}`, Keitaro10},
		{"пустой ключ всё равно ключ", `{"event_id":null}`, Keitaro11},
		{"оба набора", `{"click_id":"a","event_id":"b"}`, ""},
		{"без ключей", `{"sub_id":"a"}`, ""},
	}
	for _, tc := range cases {
		raw, err := rawFields([]byte(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := KeysOfRow(raw).Version; got != tc.want {
			t.Errorf("%s: %q, ждали %q", tc.name, got, tc.want)
		}
	}
}

// TestKeitaroVersionRows проверяет на одном входе оба формата: 10 даёт click_id и conversion_id,
// 11 даёт event_id и event_type; оба попадают в одни поля строки клика и конверсии.
func TestKeitaroVersionRows(t *testing.T) {
	clicks := []struct{ name, raw, id string }{
		{"10", `{"sub_id":"1vepsc2qv8h","click_id":"c-77"}`, "c-77"},
		{"11", `{"sub_id":"2k5a13p.ac.2m74","event_id":"e-88"}`, "e-88"},
		{"10, число", `{"sub_id":"s","click_id":123}`, "123"},
		{"оба набора", `{"sub_id":"s","click_id":"c","event_id":"e"}`, ""},
		{"без ключа", `{"sub_id":"s"}`, ""},
	}
	for _, tc := range clicks {
		var row ClickRow
		if err := json.Unmarshal([]byte(tc.raw), &row); err != nil {
			t.Fatalf("click %s: %v", tc.name, err)
		}
		if row.EventID != tc.id {
			t.Errorf("click %s: EventID %q, ждали %q", tc.name, row.EventID, tc.id)
		}
	}
	convs := []struct{ name, raw, id, typ, status, sub string }{
		{"10", `{"sub_id":"1vepsc2qv8h","status":"rebill","conversion_id":"v-5"}`, "v-5", "", "rebill", "1vepsc2qv8h"},
		{"11", `{"sub_id":"2k5a13p.ac.2m74","event_id":"e-6","event_type":"Vip_Sale"}`, "e-6", "Vip_Sale", "Vip_Sale", "2k5a13p.ac.2m74"},
		{"11 со status", `{"sub_id":"s","status":"lead","event_id":"e-7","event_type":"custom"}`, "e-7", "custom", "lead", "s"},
		{"оба набора", `{"sub_id":"s","status":"sale","conversion_id":"v","event_id":"e","event_type":"t"}`, "", "", "sale", "s"},
	}
	for _, tc := range convs {
		var row ConversionRow
		if err := json.Unmarshal([]byte(tc.raw), &row); err != nil {
			t.Fatalf("conversion %s: %v", tc.name, err)
		}
		if row.EventID != tc.id || row.EventType != tc.typ || row.EffectiveStatus() != tc.status || row.SubID != tc.sub {
			t.Errorf("conversion %s: %+v", tc.name, row)
		}
	}
}

// logColumnFake — журнал, который записывает запрошенные колонки и может не принимать колонки идентификаторов.
type logColumnFake struct {
	rejectIDs bool
	asked     []string
}

// serve поднимает журнал; один ответ — одна строка клика или конверсии.
func (f *logColumnFake) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Offset  int      `json:"offset"`
			Columns []string `json:"columns"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("тело: %s", raw)
		}
		cols := strings.Join(req.Columns, ",")
		f.asked = append(f.asked, r.URL.Path+" "+cols)
		if f.rejectIDs && (strings.Contains(cols, "click_id") || strings.Contains(cols, "event_id") ||
			strings.Contains(cols, "conversion_id") || strings.Contains(cols, "event_type")) {
			http.Error(w, "unknown column", http.StatusBadRequest)
			return
		}
		rows := `[]`
		if req.Offset == 0 {
			rows = `[{"sub_id":"s1","status":"lead","datetime":"2026-10-01 00:00:00"}]`
		}
		_, _ = io.WriteString(w, `{"rows":`+rows+`,"total":1}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// idColumnsOf возвращает из списка запросов колонки идентификаторов, которые попали в журнал path.
func idColumnsOf(asked []string, path string) []string {
	var out []string
	for _, a := range asked {
		path1, cols, _ := strings.Cut(a, " ")
		if path1 != path {
			continue
		}
		for _, c := range strings.Split(cols, ",") {
			switch c {
			case "click_id", "conversion_id", "event_id", "event_type":
				out = append(out, c)
			}
		}
		return out
	}
	return out
}

// TestKeitaroVersionColumns проверяет, что журнал просит колонки идентификаторов той версии, которая задана клиенту,
// и не просит ни одной, пока версия не определена.
func TestKeitaroVersionColumns(t *testing.T) {
	cases := []struct {
		version string
		click   []string
		conv    []string
	}{
		{"", nil, nil},
		{Keitaro10, []string{"click_id"}, []string{"conversion_id"}},
		{Keitaro11, []string{"event_id"}, []string{"event_id", "event_type"}},
	}
	for _, tc := range cases {
		var f logColumnFake
		srv := f.serve(t)
		c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client(), Version: tc.version}
		ctx := context.Background()
		if _, err := c.FetchClicks(ctx, "a", "b", func(ClickRow) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := c.FetchConversions(ctx, "a", "b", func(ConversionRow) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if got := idColumnsOf(f.asked, clicksLogPath); !reflect.DeepEqual(got, tc.click) {
			t.Errorf("версия %q, клики: %v, ждали %v", tc.version, got, tc.click)
		}
		if got := idColumnsOf(f.asked, conversionsLogPath); !reflect.DeepEqual(got, tc.conv) {
			t.Errorf("версия %q, конверсии: %v, ждали %v", tc.version, got, tc.conv)
		}
		if c.IDColumnsRejected() || c.ExtendedColumnsRejected() {
			t.Errorf("версия %q: откат без отказа журнала", tc.version)
		}
	}
}

// TestKeitaroVersionColumnsRejected проверяет, что отказ журнала от колонок идентификаторов
// не отнимает расширенные колонки: чтение повторяется без идентификаторов, а не на базовом наборе.
func TestKeitaroVersionColumnsRejected(t *testing.T) {
	f := logColumnFake{rejectIDs: true}
	srv := f.serve(t)
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client(), Version: Keitaro11}
	n, err := c.FetchClicks(context.Background(), "a", "b", func(ClickRow) error { return nil })
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !c.IDColumnsRejected() || c.ExtendedColumnsRejected() {
		t.Errorf("флаги: id %v ext %v", c.IDColumnsRejected(), c.ExtendedColumnsRejected())
	}
	if len(f.asked) < 2 || !strings.Contains(f.asked[1], "ad_campaign_id") || strings.Contains(f.asked[1], "event_id") {
		t.Errorf("повтор без идентификаторов, с расширенными колонками: %v", f.asked)
	}
}
