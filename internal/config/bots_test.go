package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultBotProfileChecksProxy(t *testing.T) {
	if !DefaultBotProfile().Proxy {
		t.Fatal("the default bot profile must check proxies")
	}
}

func TestParseBotProfileMissingKeyMeansProxyOn(t *testing.T) {
	p, err := ParseBotProfile([]byte(`{"ch_ua":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Proxy || !p.CheckUA {
		t.Fatalf("profile = %+v, want proxy on and ch_ua kept", p)
	}
}

func TestParseBotProfileExplicitValues(t *testing.T) {
	off, err := ParseBotProfile([]byte(`{"ch_proxy":false,"ch_ua":true}`))
	if err != nil || off.Proxy {
		t.Fatalf("ch_proxy:false: %+v, %v", off, err)
	}
	on, err := ParseBotProfile([]byte(`{"ch_proxy":true}`))
	if err != nil || !on.Proxy {
		t.Fatalf("ch_proxy:true: %+v, %v", on, err)
	}
}

func TestParseBotProfileRejectsBadJSON(t *testing.T) {
	for _, in := range []string{``, `{`, `[]`, `"x"`, `null`, ` null `, `1`, `true`} {
		if _, err := ParseBotProfile([]byte(in)); err == nil {
			t.Errorf("%q must be rejected", in)
		}
	}
}

func TestMarshalBotProfileKeepsFalse(t *testing.T) {
	b, err := MarshalBotProfile(Bots{CheckUA: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ch_proxy": false`) {
		t.Fatalf("explicit ch_proxy missing: %s", b)
	}
	back, err := ParseBotProfile(b)
	if err != nil || back.Proxy || !back.CheckUA {
		t.Fatalf("round trip lost the choice: %+v, %v", back, err)
	}
	b, err = MarshalBotProfile(Bots{Proxy: true})
	if err != nil || !strings.Contains(string(b), `"ch_proxy": true`) {
		t.Fatalf("true must be written too: %s, %v", b, err)
	}
}

func TestStreamBotsEncodingHasNoExplicitProxy(t *testing.T) {
	b, err := json.Marshal(Bots{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ch_proxy") {
		t.Fatalf("stream encoding changed: %s", b)
	}
}

func TestParseBotProfileExplicitFlag(t *testing.T) {
	cases := []struct {
		in     string
		set    bool
		failed bool
	}{
		{`{}`, false, false},
		{`{"ch_ua":true}`, false, false},
		{`{"ch_proxy":false}`, true, false},
		{`{"ch_proxy":true,"ch_ua":true}`, true, false},
		{`null`, false, true},
		{`{`, false, true},
	}
	for _, tc := range cases {
		_, set, err := ParseBotProfileExplicit([]byte(tc.in))
		if set != tc.set || (err != nil) != tc.failed {
			t.Errorf("ParseBotProfileExplicit(%s) set=%v err=%v, want set=%v failed=%v", tc.in, set, err, tc.set, tc.failed)
		}
	}
	p, set, err := ParseBotProfileExplicit([]byte(`{}`))
	if err != nil || set || !p.Proxy {
		t.Fatalf("empty object: %+v set=%v err=%v", p, set, err)
	}
	p, set, err = ParseBotProfileExplicit([]byte(`{"ch_proxy":false}`))
	if err != nil || !set || p.Proxy {
		t.Fatalf("explicit false: %+v set=%v err=%v", p, set, err)
	}
}
func TestMergeBotProfile(t *testing.T) {
	cases := []struct {
		name        string
		stored      string
		body        string
		wantErr     bool
		wantProxy   string // "" = key absent, else the literal value
		wantContain []string
		wantAbsent  []string
	}{
		{name: "no file, body without ch_proxy", stored: "", body: `{"ch_ua":true}`,
			wantContain: []string{`"ch_ua": true`}, wantAbsent: []string{"ch_proxy"}},
		{name: "no file, ch_proxy false is explicit", stored: "", body: `{"ch_proxy":false}`, wantProxy: "false"},
		{name: "no file, ch_proxy true is explicit", stored: "", body: `{"ch_proxy":true}`, wantProxy: "true"},
		{name: "save_ip stays when body omits it", stored: `{"save_ip":true,"ch_ua":true}`, body: `{"ch_ua":false,"ch_empty_ua":true}`,
			wantContain: []string{`"save_ip": true`, `"ch_empty_ua": true`}, wantAbsent: []string{"ch_ua", "ch_proxy"}},
		{name: "stored explicit ch_proxy stays", stored: `{"ch_proxy":false}`, body: `{"ch_ua":true}`, wantProxy: "false"},
		{name: "body overrides stored ch_proxy", stored: `{"ch_proxy":false}`, body: `{"ch_proxy":true}`, wantProxy: "true"},
		{name: "null returns to the default", stored: `{"ch_proxy":false,"ch_ua":true}`, body: `{"ch_proxy":null}`,
			wantContain: []string{`"ch_ua": true`}, wantAbsent: []string{"ch_proxy"}},
		{name: "null in the stored file is no value", stored: `{"ch_proxy":null}`, body: `{}`, wantAbsent: []string{"ch_proxy"}},
		{name: "null removes another key", stored: `{"save_ip":true}`, body: `{"save_ip":null}`, wantAbsent: []string{"save_ip"}},
		{name: "unknown and stream keys are dropped", stored: `{"ch_ua":true}`,
			body:       `{"redirect":"https://x.example","out":"o","header":"h","proxy_default":true,"private_relay":true,"nonsense":1}`,
			wantAbsent: []string{"redirect", "out", "header", "proxy_default", "private_relay", "nonsense"}, wantContain: []string{`"ch_ua": true`}},
		{name: "unreadable stored file counts as empty", stored: `{`, body: `{"ch_ua":true}`, wantContain: []string{`"ch_ua": true`}},
		{name: "stored non-object counts as empty", stored: `[]`, body: `{"ch_ua":true}`, wantContain: []string{`"ch_ua": true`}},
		{name: "null body", body: `null`, wantErr: true},
		{name: "array body", body: `[]`, wantErr: true},
		{name: "string body", body: `"x"`, wantErr: true},
		{name: "broken body", body: `{`, wantErr: true},
		{name: "empty body", body: ``, wantErr: true},
		{name: "non-boolean value", body: `{"ch_ua":"yes"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := MergeBotProfile([]byte(tc.stored), []byte(tc.body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			got := string(out)
			switch tc.wantProxy {
			case "":
				if strings.Contains(got, "ch_proxy") {
					t.Errorf("ch_proxy must be absent: %s", got)
				}
			default:
				if !strings.Contains(got, `"ch_proxy": `+tc.wantProxy) {
					t.Errorf("want ch_proxy %s: %s", tc.wantProxy, got)
				}
			}
			for _, s := range tc.wantContain {
				if !strings.Contains(got, s) {
					t.Errorf("%q missing: %s", s, got)
				}
			}
			for _, s := range tc.wantAbsent {
				if strings.Contains(got, `"`+s+`"`) {
					t.Errorf("%q must be absent: %s", s, got)
				}
			}
			if _, _, err := ParseBotProfileExplicit(out); err != nil {
				t.Errorf("the result must parse: %v", err)
			}
		})
	}
}

func TestMergeBotProfileDefaultComesBack(t *testing.T) {
	out, err := MergeBotProfile([]byte(`{"ch_proxy":false}`), []byte(`{"ch_proxy":null}`))
	if err != nil {
		t.Fatal(err)
	}
	p, set, err := ParseBotProfileExplicit(out)
	if err != nil || set || !p.Proxy {
		t.Fatalf("after null: %+v set=%v err=%v", p, set, err)
	}
}

func TestParseBotProfileNullProxyIsNotSet(t *testing.T) {
	p, set, err := ParseBotProfileExplicit([]byte(`{"ch_proxy":null,"ch_ua":true}`))
	if err != nil || set || !p.Proxy || !p.CheckUA {
		t.Fatalf("%+v set=%v err=%v", p, set, err)
	}
}