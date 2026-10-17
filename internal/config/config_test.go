package config

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestGroupsJSONRoundTrip checks Unmarshal, Marshal, Unmarshal on the example
// file and on a fully filled group document.
func TestGroupsJSONRoundTrip(t *testing.T) {
	example, err := os.ReadFile("../../configs/groups.example.json")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupsRoundTrip(t, example)

	raw, err := json.Marshal(filledGroups())
	if err != nil {
		t.Fatal(err)
	}
	const marker = `"raw":"empty-values-marker"`
	raw = bytes.Replace(raw, []byte(marker), []byte(marker+`,"values":[]`), 1)
	if !bytes.Contains(raw, []byte(`"values":[]`)) {
		t.Fatal("fixture did not include an empty values slice")
	}
	assertGroupsRoundTrip(t, raw)
}

func TestStatusAlwaysSerialized(t *testing.T) {
	b, err := json.Marshal(Group{
		ID: "g", Name: "n", Status: false,
		Streams: []Stream{{Name: "s", Status: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), `"status":false`) != 2 {
		t.Fatalf("status false missing on a level: %s", b)
	}
}

func TestEmptyStreamIsCompact(t *testing.T) {
	b, err := json.Marshal(Stream{Name: "s", Status: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) >= 60 || bytes.Contains(b, []byte("lang")) {
		t.Fatalf("compact stream = %s (%d bytes)", b, len(b))
	}
}

// assertGroupsRoundTrip decodes raw twice with a marshal between the decodes.
// raw is a JSON array of groups. It returns nothing.
// It fails when a decode fails or the two values differ.
func assertGroupsRoundTrip(t *testing.T, raw []byte) {
	t.Helper()
	var first []Group
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var second []Group
	if err := json.Unmarshal(encoded, &second); err != nil {
		t.Fatal(err)
	}
	// omitempty drops an empty slice, and the next unmarshal restores nil.
	// A non-nil empty Values slice would not survive that, so clear those
	// slices on the original value before the comparison.
	nilEmptySlices(reflect.ValueOf(&first).Elem())
	if reflect.DeepEqual(first, second) {
		return
	}
	want, _ := json.Marshal(first)
	got, _ := json.Marshal(second)
	t.Fatalf("round trip mismatch\nwant %s\ngot  %s", want, got)
}

// nilEmptySlices sets every empty slice in v to nil.
// v must be addressable. It returns nothing and does not report an error.
func nilEmptySlices(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			nilEmptySlices(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			nilEmptySlices(v.Field(i))
		}
	case reflect.Slice:
		if v.IsNil() {
			return
		}
		for i := 0; i < v.Len(); i++ {
			nilEmptySlices(v.Index(i))
		}
		if v.Len() == 0 && v.CanSet() {
			v.Set(reflect.Zero(v.Type()))
		}
	}
}

// filledGroups returns one enabled group, one disabled stream, and one disabled group.
func filledGroups() []Group {
	off := Stream{
		Name: "off", Status: false,
		Rules: Rules{Country: ListFilter{Flag: FlagB, Raw: "empty-values-marker"}},
	}
	return []Group{
		{
			ID: "g1", Aliases: []string{"a"}, Name: "One", Status: true,
			Redirect: "http_redirect", Header: "text/html", Out: "https://out",
			Geo: "cf", UniqMethod: "cookie", UniqSeconds: 30,
			Firewall:   FirewallRule{Enabled: true, Queries: 5, Seconds: 9},
			SaveKeys:   true,
			SaveKeysSE: true,
			Streams:    []Stream{filledStream(), off},
		},
		{
			ID: "g2", Name: "Two", Status: false,
			Streams: []Stream{{Name: "idle", Status: true}},
		},
	}
}

// filledStream returns a stream with every filter and output field set.
func filledStream() Stream {
	f := func(flag Flag, raw string) ListFilter {
		return ListFilter{Flag: flag, Raw: raw, Values: []string{raw}}
	}
	var days [7]bool
	for i := range days {
		days[i] = true
	}
	return Stream{
		Name: "full", Status: true, Comment: "note",
		Rules: Rules{
			Lang: f(FlagOff, "en"), Country: f(FlagB, "ru"), City: f(FlagA, "Moscow"),
			Region: f(FlagB, "MOW"), UAText: f(FlagA, "bot"), Referer: f(FlagB, "https://ref"),
			Domain: f(FlagA, "ex.com"), Key: f(FlagB, "kw"), OS: f(FlagA, "Android 13"),
			Browser: f(FlagB, "Chrome"), Brand: f(FlagA, "Samsung"), ASN: f(FlagB, "15169"),
			Org: f(FlagA, "Google"), Timezone: f(FlagB, "+3"), Query: f(FlagA, "q=1"),
			Schedule: Schedule{Enabled: true, Days: days},
			IPList:   IPListFilter{Flag: FlagA, File: "ip.dat"},
			Computer: FlagA, Phone: FlagB, Tablet: FlagA,
			Operators:  map[string]Flag{"mts": FlagA, "beeline": FlagB},
			Unique:     FlagB,
			YaBrowser:  FlagA,
			HasReferer: FlagB,
			Bot:        FlagA,
			Limit:      LimitRule{Enabled: true, Type: 2, Count: 10, Seconds: 60},
		},
		FilterOr: true, Weight: 4,
		Out: Output{
			Redirect: "http_redirect", Out: "https://a|||https://b", Chance: 40,
			Distribution: "random", Header: "text/plain",
			Offers:    []ShareRef{{ID: "o1", Share: 1}},
			Landings:  []ShareRef{{ID: "l1", Share: 2}},
			OutShares: []int{1, 2},
		},
		Bots: Bots{
			CheckUA: true, EmptyUA: true, EmptyRef: true, EmptyLang: true,
			IPv6: true, PTR: true, ListUA: true, IPBaidu: true, IPBing: true,
			IPGoogle: true, IPMail: true, IPYahoo: true, IPYandex: true,
			IPOthers: true, SaveIP: true,
			Redirect: "404_not_found", Out: "bot-out", Header: "text/html",
		},
		Separation: Separation{Enabled: true, File: "sep.dat"},
		Curl:       "find=>repl", BotCurl: "botfind=>botrepl",
		Remote: Remote{Enabled: true, URL: "https://r/[IP]", Regexp: "/(.*)/", Reserved: "fallback", Cache: 15},
		APIMac: APIMac{Enabled: true, Code: "mac", Prob: 20},
	}
}
