package ktimport

import (
	"strings"
	"testing"
)

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
		{"status404", "<html><body>white</body></html>", "404_not_found", "", ""},
		{"status404", "<b>{subid}</b>", "404_not_found", "", ""},
		{"404", "<html><body>gone</body></html>", "404_not_found", "", ""},
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

func TestGroupIDs(t *testing.T) {
	camps := []Campaign{
		{ID: 10, Alias: ""},
		{ID: 11, Alias: "dup"},
		{ID: 12, Alias: "dup"},
		{ID: 5, Alias: "promo"},
	}
	got := GroupIDs(camps)
	if got[10] != "missing-alias-10" || got[5] != "promo" || got[11] != "dup" || got[12] != "dup-dup-12" {
		t.Fatalf("map %#v", got)
	}
	res := Map(Snapshot{Campaigns: camps}, Selection{Campaigns: true})
	for _, g := range res.Groups {
		if GroupIDs(camps)[g.Num] != g.ID {
			t.Fatalf("campaign %d group %q map %q", g.Num, g.ID, GroupIDs(camps)[g.Num])
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

func TestBotProtectionStatus404Import(t *testing.T) {
	html := strings.Repeat("x", 600)
	var pages pageBag
	var losses []Loss
	ctx := &mapCtx{losses: &losses, pages: &pages}
	bot := ctx.buildStream(KStream{
		ID: 1, Name: "Bot Protection", Type: "regular", State: "active",
		ActionType: "status404", ActionPayload: html,
	}, false)
	if !bot.Status || bot.Out.Redirect != "404_not_found" || bot.Out.Out != "" {
		t.Fatalf("bot protection stream %+v", bot.Out)
	}
	if len(pages) != 0 {
		t.Fatalf("page bag = %+v", pages)
	}
	white := ctx.buildStream(KStream{
		ID: 2, Name: "white", Type: "regular", State: "active",
		ActionType: "status404", ActionPayload: html,
	}, false)
	if !white.Status || white.Out.Redirect != "404_not_found" || white.Out.Out != "" {
		t.Fatalf("white stream %+v", white.Out)
	}
	if len(pages) != 0 {
		t.Fatalf("page bag = %+v", pages)
	}

	tail := ctx.buildStream(KStream{
		ID: 3, Name: "Bot Protection -x", Type: "regular", State: "active",
		ActionType: "status404", ActionPayload: html,
	}, false)
	if !tail.Status || tail.Out.Redirect != "404_not_found" || tail.Out.Out != "" {
		t.Fatalf("bot protection tail stream %+v", tail.Out)
	}

	closing := ctx.buildStream(KStream{
		ID: 4, Name: "Замыкающий", Type: "regular", State: "active",
		ActionType: "status404", ActionPayload: html,
	}, false)
	if !closing.Status || closing.Out.Redirect != "404_not_found" || closing.Out.Out != "" {
		t.Fatalf("closing stream %+v", closing.Out)
	}
	if len(pages) != 0 {
		t.Fatalf("page bag after closing = %+v", pages)
	}
	closingTail := ctx.buildStream(KStream{
		ID: 5, Name: "Замыкающий -x", Type: "regular", State: "active",
		ActionType: "status404", ActionPayload: html,
	}, false)
	if !closingTail.Status || closingTail.Out.Redirect != "404_not_found" || closingTail.Out.Out != "" {
		t.Fatalf("closing tail stream %+v", closingTail.Out)
	}
}

// TestStatus404IsNotFoundAndTypeOrder проверяет, что действие 404 становится 404_not_found,
// HTML-действие остаётся HTML-страницей, редирект не меняется, а тип потока задаёт порядок.
func TestStatus404IsNotFoundAndTypeOrder(t *testing.T) {
	body404 := strings.Repeat("<p>404</p>", 80)
	real := "<html><body>" + strings.Repeat("z", 600) + "</body></html>"
	var pages pageBag
	var losses []Loss
	ctx := &mapCtx{losses: &losses, pages: &pages}
	got := ctx.mapStreams([]KStream{
		{ID: 1, Type: "forced", Name: "Bot Protection", Position: 2, State: "active", ActionType: "status404", ActionPayload: body404},
		{ID: 2, Type: "regular", Name: "SEO OFFERS", Position: 1, State: "active", ActionType: "http", ActionPayload: "https://winaso.online/costfors/?subid={subid}"},
		{ID: 4, Type: "regular", Name: "page", Position: 2, State: "active", ActionType: "show_html", ActionPayload: real},
		{ID: 5, Type: "regular", Name: "nothing", Position: 3, State: "active", ActionType: "do_nothing"},
		{ID: 3, Type: "default", Name: "Замыкающий", Position: 1, State: "active", ActionType: "404", ActionPayload: body404},
	}, false)
	if len(got) != 5 {
		t.Fatalf("streams %d", len(got))
	}
	if got[0].Name != "Bot Protection" || got[0].Out.Redirect != "404_not_found" || got[0].Out.Out != "" {
		t.Fatalf("forced %+v", got[0])
	}
	if got[1].Name != "SEO OFFERS" || got[1].Out.Redirect != "http_redirect" || got[1].Out.Out != "https://winaso.online/costfors/?subid=[SUBID]" {
		t.Fatalf("redirect %+v", got[1].Out)
	}
	htmlOut := got[2].Out
	if got[2].Name != "page" || htmlOut.Redirect != "show_page_html" || !strings.HasPrefix(htmlOut.Out, "@page:page-") || !strings.HasSuffix(htmlOut.Out, ".html") {
		t.Fatalf("html %+v", htmlOut)
	}
	if strings.Contains(htmlOut.Out, ".xml") {
		t.Fatalf("html stored as xml %+v", htmlOut)
	}
	if got[3].Name != "nothing" || got[3].Out.Redirect != "stop" || got[3].Out.Out != "" {
		t.Fatalf("do nothing %+v", got[3].Out)
	}
	if got[4].Name != "Замыкающий" || got[4].Out.Redirect != "404_not_found" || got[4].Out.Out != "" {
		t.Fatalf("default %+v", got[4])
	}
	if _, ok := pages[PageName(body404)]; ok || pages[PageName(real)] != real {
		t.Fatalf("pages %+v", pages)
	}
}
