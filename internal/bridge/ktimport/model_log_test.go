package ktimport

import (
	"encoding/json"
	"testing"
)

func TestFlexBool(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
		{`"1"`, true},
		{`"0"`, false},
		{`"true"`, true},
		{`"false"`, false},
		{"null", false},
		{`"yes"`, true},
	}
	for _, tc := range cases {
		var got FlexBool
		if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if bool(got) != tc.want {
			t.Fatalf("%s -> %v", tc.in, got)
		}
	}
}

func TestClickRowBooleans(t *testing.T) {
	var row ClickRow
	raw := []byte(`{"sub_id":"k","is_bot":false,"is_unique_campaign":true,"country_code":"ZA","affiliate_network_id":4,"ts_id":"8","operator":"Vodacom","search_engine":"Google","destination":"https://land.example/"}`)
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	if row.IsBot || !row.IsUnique || row.CountryCode != "ZA" || row.NetworkID != 4 || row.SourceID != 8 || row.Operator != "Vodacom" || row.SearchEngine != "Google" || row.Destination == "" {
		t.Fatalf("%+v", row)
	}
}
