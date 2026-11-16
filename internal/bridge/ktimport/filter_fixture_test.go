package ktimport

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// fixtureGroup переносит потоки из testdata/stream_filters.json в одну кампанию.
//
// Параметры: t — тест.
// Результат: результат переноса и потоки кампании по id Keitaro.
// Ошибки: падает тест, если фикстура не читается или кампания не собрана.
func fixtureGroup(t *testing.T) (Result, map[int]Stream) {
	t.Helper()
	raw, err := os.ReadFile("testdata/stream_filters.json")
	if err != nil {
		t.Fatal(err)
	}
	var streams []KStream
	if err := json.Unmarshal(raw, &streams); err != nil {
		t.Fatal(err)
	}
	res := MapAll(Snapshot{
		Campaigns: []Campaign{{ID: 7, Alias: "guard", Name: "Guard", State: "active"}},
		Streams:   map[string][]KStream{"7": streams},
	})
	g, ok := findGroup(res.Groups, "guard")
	if !ok {
		t.Fatalf("campaign not mapped: %+v", res.Groups)
	}
	byID := map[int]Stream{}
	for _, s := range g.Streams {
		id, err := commentStreamID(s.Comment)
		if err != nil {
			t.Fatalf("comment %q has no stream id", s.Comment)
		}
		byID[id] = s
	}
	return res, byID
}

// commentStreamID читает id потока из заметки keitaro_stream_id=N.
//
// Параметры: comment — заметка потока.
// Результат: номер потока Keitaro.
// Ошибки: ошибка разбора числа.
func commentStreamID(comment string) (int, error) {
	rest, _ := strings.CutPrefix(comment, "keitaro_stream_id=")
	rest, _, _ = strings.Cut(rest, ";")
	return strconv.Atoi(rest)
}

func TestFixtureBotAccept(t *testing.T) {
	_, byID := fixtureGroup(t)
	s := byID[43041]
	if s.Rules.Bot != 1 || !s.Status || s.Out.Redirect != "404_not_found" {
		t.Fatalf("bot accept stream = %+v", s)
	}
	if s.Comment != "keitaro_stream_id=43041" {
		t.Fatalf("comment = %q", s.Comment)
	}
}

func TestFixtureBotRejectWithCountry(t *testing.T) {
	_, byID := fixtureGroup(t)
	s := byID[66266]
	if s.Rules.Bot != 2 || !s.Status || s.FilterOr {
		t.Fatalf("bot reject stream = %+v", s)
	}
	if s.Rules.Country.Flag != 2 || !strings.EqualFold(strings.Join(s.Rules.Country.Values, ","), "AE,DE,PL") {
		t.Fatalf("country = %+v", s.Rules.Country)
	}
	if s.Rules.Computer != 1 || s.Rules.Phone != 0 || s.Rules.Tablet != 0 {
		t.Fatalf("device = %+v", s.Rules)
	}
	if dev := byID[66264]; dev.Rules.Country.Flag != 1 || dev.Rules.Bot != 0 {
		t.Fatalf("country reject = %+v", dev.Rules)
	}
}

func TestFixtureOrStreamKeepsBot(t *testing.T) {
	res, byID := fixtureGroup(t)
	s := byID[66267]
	r := s.Rules
	if !s.FilterOr || !s.Status || r.Bot != 1 {
		t.Fatalf("or stream = %+v", s)
	}
	if r.Phone != 1 || r.Tablet != 1 || r.Computer != 0 || r.Org.Flag != 2 || r.UAText.Flag != 2 || r.IPList.Flag != 2 {
		t.Fatalf("or rules = %+v", r)
	}
	if sub := r.SubIDs["sub_id_3"]; sub.Flag != 2 || len(sub.Values) != 3 {
		t.Fatalf("sub_id_3 = %+v", r.SubIDs)
	}
	if strings.Contains(lossesText(res.Losses), "фильтр bot") {
		t.Fatalf("bot reported as lost:\n%s", lossesText(res.Losses))
	}
}

func TestFixtureFlagsAndUnknown(t *testing.T) {
	res, byID := fixtureGroup(t)
	s := byID[70001]
	r := s.Rules
	if !s.Status || r.Proxy != 2 || r.HasReferer != 2 || r.IPVersion != 1 || r.Unique != 1 {
		t.Fatalf("flags stream = %+v", s)
	}
	if s.Comment != "keitaro_stream_id=70001"+skippedFiltersNote+"connection_type, operator" {
		t.Fatalf("comment = %q", s.Comment)
	}
	text := lossesText(res.Losses)
	for _, want := range []string{"фильтр connection_type не переносится", "фильтр operator не переносится", "область stream"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report misses %q:\n%s", want, text)
		}
	}
}

func TestPresenceFilters(t *testing.T) {
	cases := []struct {
		name, mode, payload string
		get                 func(Rules) int
		want                int
	}{
		{"bot", "accept", `null`, func(r Rules) int { return r.Bot }, 1},
		{"bot", "reject", `null`, func(r Rules) int { return r.Bot }, 2},
		{"bot", "accept", `["0"]`, func(r Rules) int { return r.Bot }, 2},
		{"proxy", "accept", ``, func(r Rules) int { return r.Proxy }, 1},
		{"empty_referrer", "accept", `null`, func(r Rules) int { return r.HasReferer }, 2},
		{"empty_referrer", "reject", `null`, func(r Rules) int { return r.HasReferer }, 1},
		{"uniqueness", "accept", `"campaign"`, func(r Rules) int { return r.Unique }, 1},
		{"uniqueness", "reject", `"global"`, func(r Rules) int { return r.Unique }, 2},
		{"uniqueness", "accept", `["1"]`, func(r Rules) int { return r.Unique }, 1},
	}
	for _, c := range cases {
		rules, ok, _, why := oneFilter(KFilter{Name: c.name, Mode: c.mode, Payload: json.RawMessage(c.payload)}, nil)
		if !ok || c.get(rules) != c.want {
			t.Errorf("%s %s %s → %+v ok=%v %s, want %d", c.name, c.mode, c.payload, rules, ok, why, c.want)
		}
	}
	_, _, approx, why := oneFilter(KFilter{Name: "uniqueness", Mode: "accept", Payload: json.RawMessage(`"campaign"`)}, nil)
	if approx || why != "" {
		t.Fatalf("campaign scope is exact: approx=%v %q", approx, why)
	}
}

func TestOnlyUnknownFilterDisablesStream(t *testing.T) {
	_, ok, why, losses, skipped := mapFilters([]KFilter{{Name: "connection_type", Mode: "accept", Payload: json.RawMessage(`["wifi"]`)}}, nil, false)
	if ok || why == "" || len(losses) != 1 || strings.Join(skipped, ",") != "connection_type" {
		t.Fatalf("ok=%v why=%q losses=%+v skipped=%v", ok, why, losses, skipped)
	}
}
