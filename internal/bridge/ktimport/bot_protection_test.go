package ktimport

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// botProtectionStreams читает testdata/bot_protection_streams.json: потоки кампании 309 из Keitaro.
//
// Параметры: t — тест.
// Результат: потоки Keitaro в порядке файла.
// Ошибки: падает тест, если файл не читается.
func botProtectionStreams(t *testing.T) []KStream {
	t.Helper()
	raw, err := os.ReadFile("testdata/bot_protection_streams.json")
	if err != nil {
		t.Fatal(err)
	}
	var streams []KStream
	if err := json.Unmarshal(raw, &streams); err != nil {
		t.Fatal(err)
	}
	return streams
}

// botProtectionGroup переносит потоки кампании 309 полным маппером.
//
// Параметры: t — тест.
// Результат: результат переноса и группа кампании.
// Ошибки: падает тест, если группа не собрана.
func botProtectionGroup(t *testing.T) (Result, Group) {
	t.Helper()
	res := MapAll(Snapshot{
		Campaigns: []Campaign{{ID: 309, Alias: "alloffers", Name: "All Offers", State: "active"}},
		Streams:   map[string][]KStream{"309": botProtectionStreams(t)},
	})
	g, ok := findGroup(res.Groups, "alloffers")
	if !ok {
		t.Fatalf("campaign not mapped: %+v", res.Groups)
	}
	return res, g
}

func TestBotProtectionKeepsDeviceProvidersAndBot(t *testing.T) {
	res, g := botProtectionGroup(t)
	s := g.Streams[0]
	if s.Comment != "keitaro_stream_id=12694" || !s.Status || !s.FilterOr || s.Out.Redirect != "404_not_found" {
		t.Fatalf("bot protection stream = %+v", s)
	}
	r := s.Rules
	if r.Computer != 0 || r.Phone != 1 || r.Tablet != 1 {
		t.Fatalf("device reject mobile, tablet must block phone and tablet: %+v", r)
	}
	if r.Bot != 1 {
		t.Fatalf("bot accept = %d, want 1", r.Bot)
	}
	want := botProtectionStreams(t)[3].Filters[1]
	var isp []string
	if err := json.Unmarshal(want.Payload, &isp); err != nil {
		t.Fatal(err)
	}
	if r.Org.Flag != 2 || !reflect.DeepEqual(r.Org.Values, isp) || len(isp) != 21 {
		t.Fatalf("org = %+v, want all %d providers in order", r.Org, len(isp))
	}
	text := lossesText(res.Losses)
	if !strings.Contains(text, ispFilterNote) || strings.Contains(text, "фильтр bot") || strings.Contains(text, "фильтр device_type") {
		t.Fatalf("report:\n%s", text)
	}
}

func TestBotOnlyStreamGetsBotRule(t *testing.T) {
	_, g := botProtectionGroup(t)
	s := g.Streams[1]
	if s.Comment != "keitaro_stream_id=977" || !s.Status || s.Rules.Bot != 1 {
		t.Fatalf("bot-only stream = %+v", s)
	}
	if !reflect.DeepEqual(s.Rules, Rules{Bot: 1}) {
		t.Fatalf("bot-only rules = %+v", s.Rules)
	}
}

func TestRegularStreamKeepsHumansOnly(t *testing.T) {
	_, g := botProtectionGroup(t)
	s := g.Streams[3]
	r := s.Rules
	if s.Comment != "keitaro_stream_id=14606" || s.FilterOr || r.Bot != 2 || r.Computer != 1 || r.Phone != 0 || r.Tablet != 0 {
		t.Fatalf("regular stream = %+v", s)
	}
	if r.Country.Flag != 2 || strings.Join(r.Country.Values, ",") != "CM" {
		t.Fatalf("country = %+v", r.Country)
	}
}

func TestEmptyFilterDoesNotLeaveCatchAllStream(t *testing.T) {
	res, g := botProtectionGroup(t)
	s := g.Streams[2]
	if s.Status || !reflect.ValueOf(s.Rules).IsZero() {
		t.Fatalf("stream with only an empty filter must be off: %+v", s)
	}
	if s.Comment != "keitaro_stream_id=30001"+skippedFiltersNote+"country" {
		t.Fatalf("comment = %q", s.Comment)
	}
	if !strings.Contains(lossesText(res.Losses), "фильтр country пуст") {
		t.Fatalf("report:\n%s", lossesText(res.Losses))
	}
}

func TestStreamOrderForcedRegularDefault(t *testing.T) {
	_, g := botProtectionGroup(t)
	var got []string
	for _, s := range g.Streams {
		got = append(got, s.Comment[:strings.IndexByte(s.Comment+";", ';')])
	}
	want := []string{
		"keitaro_stream_id=12694", "keitaro_stream_id=977",
		"keitaro_stream_id=30001", "keitaro_stream_id=14606", "keitaro_stream_id=16665",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want forced by position, then regular, then default: %v", got, want)
	}
}

func TestMapStreamFiltersMatchesFullImport(t *testing.T) {
	streams := botProtectionStreams(t)
	res, g := botProtectionGroup(t)
	got, lists, losses := MapStreamFilters(streams)
	if len(got) != len(streams) {
		t.Fatalf("streams = %d, want %d", len(got), len(streams))
	}
	for _, s := range g.Streams {
		id, ok := NoteStreamID(s.Comment)
		if !ok {
			t.Fatalf("comment %q has no id", s.Comment)
		}
		f := got[id]
		if !reflect.DeepEqual(f.Rules, s.Rules) || f.FilterOr != s.FilterOr {
			t.Fatalf("stream %d: filters %+v, full import %+v", id, f, s.Rules)
		}
		if RenoteStream(StreamNote(id, nil), f.Skipped) != s.Comment {
			t.Fatalf("stream %d note %q, full import %q", id, StreamNote(id, f.Skipped), s.Comment)
		}
		if f.OK != s.Status {
			t.Fatalf("stream %d ok=%v, full import status=%v", id, f.OK, s.Status)
		}
	}
	if lists != nil || len(res.Lists) != 0 {
		t.Fatalf("lists = %v / %v", lists, res.Lists)
	}
	if !strings.Contains(lossesText(losses), "stream 30001") {
		t.Fatalf("filter report lacks the empty stream:\n%s", lossesText(losses))
	}
}

func TestStreamNoteHelpers(t *testing.T) {
	if n, ok := NoteStreamID("keitaro_stream_id=12694; не перенесены фильтры Keitaro: limit"); !ok || n != 12694 {
		t.Fatalf("id = %d %v", n, ok)
	}
	for _, bad := range []string{"", "keitaro_stream_id=", "keitaro_stream_id=x", "note keitaro_stream_id=5", "keitaro_stream_id=0"} {
		if _, ok := NoteStreamID(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
	old := "keitaro_stream_id=5; ручная пометка" + skippedFiltersNote + "bot, limit"
	if got := RenoteStream(old, []string{"limit"}); got != "keitaro_stream_id=5; ручная пометка"+skippedFiltersNote+"limit" {
		t.Fatalf("renote = %q", got)
	}
	if got := RenoteStream(old, nil); got != "keitaro_stream_id=5; ручная пометка" {
		t.Fatalf("renote without skipped = %q", got)
	}
	keys := strings.Join(RuleKeys(), ",")
	for _, want := range []string{"country", "org", "ip_list", "computer", "phone", "tablet", "bot", "sub_ids"} {
		if !strings.Contains(","+keys+",", ","+want+",") {
			t.Fatalf("rule keys %s miss %s", keys, want)
		}
	}
}
