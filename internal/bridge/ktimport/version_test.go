package ktimport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func vote(signal, version string) VersionVote { return VersionVote{Signal: signal, Version: version} }

// TestKeitaroVersionDetect проверяет итог голосования: только 10, только 11, смесь, пусто и мусор.
func TestKeitaroVersionDetect(t *testing.T) {
	cases := []struct {
		name     string
		votes    []VersionVote
		version  string
		conflict bool
		kept     int
	}{
		{"только 10", []VersionVote{vote(SignalSubID, Keitaro10), vote(SignalRebill, Keitaro10)}, Keitaro10, false, 2},
		{"только 11", []VersionVote{vote(SignalSubID, Keitaro11), vote(SignalEventID, Keitaro11)}, Keitaro11, false, 2},
		{"смесь", []VersionVote{vote(SignalSubID, Keitaro11), vote(SignalRebill, Keitaro10)}, "", true, 2},
		{"пусто", nil, "", false, 0},
		{"мусор", []VersionVote{vote("", Keitaro10), vote(SignalSubID, "9"), vote(SignalSubID, "")}, "", false, 0},
		{"мусор не мешает", []VersionVote{vote(SignalSubID, "9"), vote(SignalEventType, Keitaro11)}, Keitaro11, false, 1},
	}
	for _, tc := range cases {
		got := DetectVersion(tc.votes)
		if got.Version != tc.version || got.Conflict != tc.conflict || len(got.Votes) != tc.kept {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}

// TestKeitaroVersionSubID проверяет формат SubID: новый с точкой, старый без точки, остальное без голоса.
func TestKeitaroVersionSubID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1vepsc2qv8h", Keitaro10},
		{"2k5a13p.ac.2m74", Keitaro11},
		{"2k5a13p.ac", Keitaro11},
		{"", ""},
		{".ac.2m74", ""},
		{"2k5a13p..2m74", ""},
		{"abc.", ""},
		{"has space", ""},
		{"toolongsegment1", ""},
		{"a-b", ""},
	}
	for _, tc := range cases {
		if got := SubIDVersion(tc.in); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

// TestKeitaroVersionStatusVotes проверяет, что rebill голосует за 10, а свой тип за 11; встроенные типы молчат.
func TestKeitaroVersionStatusVotes(t *testing.T) {
	cases := []struct {
		in   []string
		want []VersionVote
	}{
		{[]string{"lead", "sale", "rejected", "registration", "deposit", "trash", ""}, nil},
		{[]string{"lead", " Rebill "}, []VersionVote{vote(SignalRebill, Keitaro10)}},
		{[]string{"vip_sale", "lead"}, []VersionVote{vote(SignalCustomType, Keitaro11)}},
		{[]string{"rebill", "vip_sale"}, []VersionVote{vote(SignalRebill, Keitaro10), vote(SignalCustomType, Keitaro11)}},
	}
	for _, tc := range cases {
		if got := StatusVotes(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: got %v want %v", tc.in, got, tc.want)
		}
	}
}

// versionFake — трекер с журналами. subID и statuses — свежие записи; accept — колонки, которые журнал принимает.
type versionFake struct {
	subID    string
	statuses []string
	accept   map[string]bool

	mu    sync.Mutex
	calls []string
}

// handler отвечает как журнал Keitaro: набор из двух колонок — выборка, одна колонка — проба идентификатора.
func (f *versionFake) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Limit   int      `json:"limit"`
			Columns []string `json:"columns"`
			Sort    []struct {
				Order string `json:"order"`
			} `json:"sort"`
		}
		if err := json.Unmarshal(raw, &req); err != nil || req.Limit < 1 || req.Limit > 20 ||
			len(req.Sort) != 1 || req.Sort[0].Order != "desc" || r.Header.Get("Api-Key") != "k-test" {
			t.Errorf("запрос %s %s: %s", r.Method, r.URL.Path, raw)
		}
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.Join(req.Columns, ","))
		f.mu.Unlock()
		rows := []map[string]string{}
		switch {
		case len(req.Columns) == 2 && req.Columns[0] == "sub_id":
			if f.subID != "" {
				rows = append(rows, map[string]string{"sub_id": f.subID, "datetime": "2026-11-01 10:00:00"})
			}
		case len(req.Columns) == 2 && req.Columns[0] == "status":
			for _, s := range f.statuses {
				rows = append(rows, map[string]string{"status": s})
			}
		case f.accept[req.Columns[0]]:
			rows = append(rows, map[string]string{req.Columns[0]: ""})
		default:
			http.Error(w, "unknown column", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": rows, "total": len(rows)})
	})
}

// probe поднимает трекер и собирает голоса.
func (f *versionFake) probe(t *testing.T) []VersionVote {
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	return c.ProbeVersion(context.Background(), "2026-08-01 00:00:00", "2026-11-01 00:00:00")
}

// TestKeitaroVersionProbe проверяет голоса с фикстур трекера: только 10, только 11, смесь, пусто.
func TestKeitaroVersionProbe(t *testing.T) {
	cases := []struct {
		name    string
		fake    versionFake
		version string
		signals []string
	}{
		{"только 10", versionFake{
			subID: "1vepsc2qv8h", statuses: []string{"lead", "rebill"},
			accept: map[string]bool{"click_id": true, "conversion_id": true},
		}, Keitaro10, []string{SignalSubID, SignalRebill, SignalClickID, SignalConvID}},
		{"только 11", versionFake{
			subID: "2k5a13p.ac.2m74", statuses: []string{"lead", "vip_sale"},
			accept: map[string]bool{"event_id": true, "event_type": true},
		}, Keitaro11, []string{SignalSubID, SignalCustomType, SignalEventID, SignalEventType}},
		{"смесь", versionFake{
			subID: "2k5a13p.ac.2m74", statuses: []string{"rebill"},
			accept: map[string]bool{"click_id": true},
		}, "", []string{SignalSubID, SignalRebill, SignalClickID}},
		{"пусто", versionFake{statuses: []string{"lead", "sale"}}, "", nil},
	}
	for i := range cases {
		tc := &cases[i]
		votes := tc.fake.probe(t)
		got := DetectVersion(votes)
		var signals []string
		for _, v := range got.Votes {
			signals = append(signals, v.Signal)
		}
		if got.Version != tc.version || !reflect.DeepEqual(signals, tc.signals) {
			t.Errorf("%s: version %q signals %v", tc.name, got.Version, signals)
		}
		if tc.name == "смесь" && !got.Conflict {
			t.Errorf("смесь без Conflict: %+v", got)
		}
	}
}

// TestKeitaroVersionProbeReadsOnly проверяет, что проба шлёт только POST в два журнала и ничего больше.
func TestKeitaroVersionProbeReadsOnly(t *testing.T) {
	f := versionFake{subID: "1vepsc2qv8h"}
	f.probe(t)
	want := []string{
		"POST /admin_api/v1/clicks/log sub_id,datetime",
		"POST /admin_api/v1/conversions/log status,postback_datetime",
		"POST /admin_api/v1/clicks/log click_id",
		"POST /admin_api/v1/clicks/log event_id",
		"POST /admin_api/v1/conversions/log conversion_id",
		"POST /admin_api/v1/conversions/log event_type",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("запросы:\n%s", strings.Join(f.calls, "\n"))
	}
}

// TestKeitaroVersionProbeFailures проверяет, что отказ журнала и битый ответ не дают голосов и не роняют пробу.
func TestKeitaroVersionProbeFailures(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
	}{
		{"500", http.StatusInternalServerError, "boom"},
		{"не JSON", http.StatusOK, "<html>"},
		{"без строк", http.StatusOK, `{"rows":[]}`},
		{"строка не объект", http.StatusOK, `{"rows":[1]}`},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = io.WriteString(w, tc.body)
		}))
		c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
		if votes := c.ProbeVersion(context.Background(), "a", "b"); len(votes) != 0 {
			t.Errorf("%s: голоса %v", tc.name, votes)
		}
		srv.Close()
	}
	var noHTTP Client
	if votes := noHTTP.ProbeVersion(context.Background(), "a", "b"); len(votes) != 0 {
		t.Errorf("клиент без HTTP дал голоса %v", votes)
	}
}
