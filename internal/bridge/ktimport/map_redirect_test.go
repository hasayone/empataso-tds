package ktimport

import "testing"

func TestRedirectMappings(t *testing.T) {
	cases := []struct {
		action   string
		payload  string
		redirect string
		out      string
		loss     string
	}{
		{"status403", "", "403_forbidden", "", ""},
		{"status500", "", "500_server_error", "", ""},
		{"show_html", "<b>hi</b>", "show_page_html", "<b>hi</b>", ""},
		{"show_text", "hello", "show_text", "hello", ""},
		{"http", "https://a.example/x", "http_redirect", "https://a.example/x", ""},
		{"meta", "https://a.example/x", "meta_refresh", "https://a.example/x", ""},
		{"js", "https://a.example/x", "js_redirect", "https://a.example/x", ""},
		{"iframe", "https://a.example/x", "iframe_redirect", "https://a.example/x", ""},
		{"blank", "", "stop", "", ""},
		{"do_nothing", "", "stop", "", ""},
		{"status404", "", "404_not_found", "", ""},
		{"status404", "<html><body>white</body></html>", "show_html_404", "<html><body>white</body></html>", ""},
		{"status404", "<b>{subid}</b>", "show_html_404", "<b>[CID]</b>", ""},
		{"curl", "https://a.example/x", "stop", "", "action_type curl нет соответствия, поток остановлен"},
		{"campaign", "", "stop", "", "целевая кампания не задана"},
		{"campaign", "   ", "stop", "", "целевая кампания не задана"},
		{"campaign", "404", "stop", "", "целевая кампания 404 не найдена"},
	}
	for _, tc := range cases {
		var losses []Loss
		st := KStream{ID: 5, Name: "white", Type: "forced", State: "active", ActionType: tc.action, ActionPayload: tc.payload}
		s := (&mapCtx{losses: &losses}).buildStream(st, false)
		if !s.Status || s.Out.Redirect != tc.redirect || s.Out.Out != tc.out || s.Name != "white" {
			t.Fatalf("%s stream %+v", tc.action, s)
		}
		if tc.loss == "" {
			for _, l := range losses {
				if l.Kind == "approximated" || l.Kind == "disabled" {
					t.Fatalf("%s losses %+v", tc.action, losses)
				}
			}
			continue
		}
		found := false
		for _, l := range losses {
			if l.Kind == "approximated" && l.Ref == "stream 5" && l.Detail == tc.loss {
				found = true
			}
			if l.Kind == "disabled" {
				t.Fatalf("%s disabled %+v", tc.action, losses)
			}
		}
		if !found {
			t.Fatalf("%s losses %+v", tc.action, losses)
		}
	}
}

func TestEmptyStreamName(t *testing.T) {
	var losses []Loss
	s := (&mapCtx{losses: &losses}).buildStream(KStream{ID: 37520, State: "active", ActionType: "blank"}, false)
	if s.Name != "stream 37520" || s.Comment != "keitaro_stream_id=37520" {
		t.Fatalf("%+v", s)
	}
}
