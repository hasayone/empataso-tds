package ktimport

import (
	"strings"
	"testing"
)

func TestCountryCode(t *testing.T) {
	cases := map[string]string{
		"RU":                                    "RU",
		"za":                                    "ZA",
		" Russia ":                              "RU",
		"russian federation":                    "RU",
		"United States":                         "US",
		"USA":                                   "US",
		"United States of America":              "US",
		"United Kingdom":                        "GB",
		"UK":                                    "GB",
		"Great Britain":                         "GB",
		"South Korea":                           "KR",
		"Korea, Republic of":                    "KR",
		"Republic of Korea":                     "KR",
		"North Korea":                           "KP",
		"Vietnam":                               "VN",
		"Viet Nam":                              "VN",
		"Czech Republic":                        "CZ",
		"Czechia":                               "CZ",
		"Turkey":                                "TR",
		"Türkiye":                               "TR",
		"Turkiye":                               "TR",
		"Iran":                                  "IR",
		"Syria":                                 "SY",
		"Laos":                                  "LA",
		"Moldova":                               "MD",
		"Bolivia":                               "BO",
		"Venezuela":                             "VE",
		"Tanzania":                              "TZ",
		"Côte d'Ivoire":                         "CI",
		"Cote d'Ivoire":                         "CI",
		"Ivory Coast":                           "CI",
		"Congo":                                 "CG",
		"Republic of the Congo":                 "CG",
		"Democratic Republic of the Congo":      "CD",
		"Congo, The Democratic Republic of the": "CD",
		"DR Congo":                              "CD",
		"Macedonia":                             "MK",
		"North Macedonia":                       "MK",
		"Myanmar":                               "MM",
		"Burma":                                 "MM",
		"Eswatini":                              "SZ",
		"Swaziland":                             "SZ",
		"Cape Verde":                            "CV",
		"Cabo Verde":                            "CV",
		"Brunei":                                "BN",
		"Taiwan":                                "TW",
		"Hong Kong":                             "HK",
		"Macau":                                 "MO",
		"Macao":                                 "MO",
		"Palestine":                             "PS",
		"Palestinian Territory":                 "PS",
		"Vatican":                               "VA",
		"Holy See":                              "VA",
		"East Timor":                            "TL",
		"Timor-Leste":                           "TL",
		"Micronesia":                            "FM",
		"Saint Kitts and Nevis":                 "KN",
		"Saint Lucia":                           "LC",
		"Saint Vincent and the Grenadines":      "VC",
		"Trinidad and Tobago":                   "TT",
		"Bosnia and Herzegovina":                "BA",
		"Antigua and Barbuda":                   "AG",
		"Sao Tome and Principe":                 "ST",
		"United Arab Emirates":                  "AE",
		"UAE":                                   "AE",
		"Kosovo":                                "XK",
		"South Africa":                          "ZA",
		"Polynesie Francaise":                   "PF",
		"French Polynesia":                      "PF",
		"Guyane":                                "GF",
		"Guyane Francaise":                      "GF",
		"French Guiana":                         "GF",
		"Territory of the French Southern and Antarctic Lands": "TF",
		"French Southern and Antarctic Lands":                  "TF",
		"French Southern Territories":                          "TF",
		"Nouvelle-Caledonie":                                   "NC",
		"Bonaire":                                              "BQ",
		"Caribbean Netherlands":                                "BQ",
		"Pitcairn Islands":                                     "PN",
		"United States Virgin Islands":                         "VI",
		"Reunion":                                              "RE",
		"Réunion":                                              "RE",
	}
	for raw, want := range cases {
		got, ok := CountryCode(raw)
		if !ok || got != want {
			t.Errorf("%q -> %q ok=%v", raw, got, ok)
		}
	}
	if _, ok := CountryCode("Notaland"); ok {
		t.Fatal("unknown name accepted")
	}
	if _, ok := CountryCode("  "); ok {
		t.Fatal("blank accepted")
	}
}

func TestCountryTable(t *testing.T) {
	raw := "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW XK"
	have := map[string]bool{}
	for _, code := range countryByName {
		have[code] = true
	}
	for _, code := range strings.Fields(raw) {
		if !have[code] {
			t.Errorf("missing %s", code)
		}
	}
}
