package ktimport

import (
	"strings"
	"testing"
)

func TestFixturePagesCampaignBrand(t *testing.T) {
	res := MapAll(loadFixture(t))
	pages, ok := findGroup(res.Groups, "pages")
	if !ok || len(pages.Streams) != 2 {
		t.Fatalf("pages = %+v", pages.Streams)
	}
	long := pages.Streams[0].Out
	name := strings.TrimPrefix(long.Out, "@page:")
	body := res.Pages[name]
	if long.Redirect != "show_page_html" || !strings.HasPrefix(long.Out, "@page:") || len(body) <= 512 || PageName(body) != name || !strings.HasPrefix(name, "page-") || !strings.HasSuffix(name, ".html") || strings.HasPrefix(name, "kt-") {
		t.Fatalf("long = %+v pages=%d", long, len(res.Pages))
	}
	short := pages.Streams[1].Out
	if short.Redirect != "show_text" || short.Out != "short" || strings.Contains(short.Out, "@page:") {
		t.Fatalf("short = %+v", short)
	}

	hop, _ := findGroup(res.Groups, "hop")
	if len(hop.Streams) != 1 || !hop.Streams[0].Status || hop.Streams[0].Out.Redirect != "group" || hop.Streams[0].Out.Out != "offer-es" {
		t.Fatalf("hop = %+v", hop.Streams)
	}
	model := hop.Streams[0].Rules.Model
	if model.Flag != 2 || strings.Join(model.Values, "|") != "iPhone|Pixel" {
		t.Fatalf("model = %+v", model)
	}
	if hop.Streams[0].Rules.Brand.Flag != 0 {
		t.Fatalf("device_model must not fill brand: %+v", hop.Streams[0].Rules.Brand)
	}

	gone, _ := findGroup(res.Groups, "gone")
	if len(gone.Streams) != 1 || !gone.Streams[0].Status || gone.Streams[0].Rules.IPVersion != 1 || gone.Streams[0].Out.Redirect != "stop" || gone.Streams[0].Out.Out != "" {
		t.Fatalf("gone = %+v", gone.Streams)
	}
	text := lossesText(res.Losses)
	if !strings.Contains(text, "целевая кампания 999 не найдена") || strings.Contains(text, "фильтр ipv") || strings.Contains(text, "нет фильтра версии IP") {
		t.Fatalf("losses %s", text)
	}

	geo := findOffer(res.Offers, "801")
	if strings.Join(geo.Countries, "|") != "pf|gf|tf" {
		t.Fatalf("territories = %+v", geo.Countries)
	}
}

func TestPagesSharedAndThreshold(t *testing.T) {
	html := "<html>" + strings.Repeat("z", 600) + "</html>"
	js := strings.Repeat("j", 513)
	exact := strings.Repeat("a", 512)
	snap := Snapshot{
		Campaigns: []Campaign{
			{ID: 1, Alias: "p", Name: "P", State: "active"},
			{ID: 2, Alias: "q", Name: "Q", State: "active"},
		},
		Streams: map[string][]KStream{
			"1": {
				{ID: 1, Type: "regular", State: "active", ActionType: "show_html", ActionPayload: html},
				{ID: 2, Type: "regular", Position: 2, State: "active", ActionType: "show_text", ActionPayload: exact},
				{ID: 3, Type: "regular", Position: 3, State: "active", ActionType: "javascript", ActionPayload: js},
			},
			"2": {
				{ID: 4, Type: "regular", State: "active", ActionType: "html", ActionPayload: html},
				{ID: 5, Type: "regular", Position: 2, State: "active", ActionType: "status404", ActionPayload: html},
			},
		},
	}
	res := MapAll(snap)
	name := PageName(html)
	jsName := PageName(js)
	if len(res.Pages) != 2 || res.Pages[name] != html || res.Pages[jsName] != js {
		t.Fatalf("pages = %+v", res.Pages)
	}
	p, _ := findGroup(res.Groups, "p")
	q, _ := findGroup(res.Groups, "q")
	if p.Streams[0].Out.Out != "@page:"+name || p.Streams[0].Out.Redirect != "show_page_html" {
		t.Fatalf("p0 = %+v", p.Streams[0].Out)
	}
	if p.Streams[1].Out.Out != exact || p.Streams[1].Out.Redirect != "show_text" {
		t.Fatalf("inline = %+v", p.Streams[1].Out)
	}
	if p.Streams[2].Out.Out != "@page:"+jsName || p.Streams[2].Out.Redirect != "javascript" {
		t.Fatalf("js = %+v", p.Streams[2].Out)
	}
	if q.Streams[0].Out.Out != "@page:"+name || q.Streams[1].Out.Redirect != "show_html_404" || q.Streams[1].Out.Out != "@page:"+name {
		t.Fatalf("shared = %+v %+v", q.Streams[0].Out, q.Streams[1].Out)
	}
	if !carriesOut("group") {
		t.Fatal("group drops out")
	}
}

func TestCampaignTargetAndIPVersion(t *testing.T) {
	snap := Snapshot{
		Campaigns: []Campaign{
			{ID: 1, Alias: "src", Name: "Src", State: "active"},
			{ID: 2, Alias: "dst", Name: "Dst", State: "active"},
			{ID: 4, Alias: "", Name: "Empty", State: "active"},
			{ID: 7, Alias: "same", Name: "Same A", State: "active"},
			{ID: 8, Alias: "same", Name: "Same B", State: "active"},
		},
		Streams: map[string][]KStream{
			"1": {
				{ID: 11, Type: "regular", State: "active", ActionType: "campaign", ActionPayload: "2"},
				{ID: 12, Type: "regular", Position: 2, State: "active", ActionType: "campaign", ActionPayload: "4"},
				{ID: 13, Type: "regular", Position: 3, State: "active", ActionType: "campaign", ActionPayload: "8"},
				{ID: 14, Type: "regular", Position: 4, State: "active", ActionType: "campaign", ActionPayload: "404"},
			},
			"2": {{ID: 21, Type: "regular", State: "active", ActionType: "http", ActionPayload: "https://d.example/", Filters: []KFilter{
				{Name: "ipv6", Mode: "accept", Payload: []byte(`["1"]`)},
				{Name: "ipv_4", Mode: "accept", Payload: []byte(`["1"]`)},
				{Name: "ip_version", Mode: "reject", Payload: []byte(`["6"]`)},
				{Name: "ipv_6", Mode: "accept", Payload: []byte(`["1"]`)},
				{Name: "country", Mode: "accept", Payload: []byte(`["ES"]`)},
			}}},
		},
	}
	res := MapAll(snap)
	src, _ := findGroup(res.Groups, "src")
	if src.Streams[0].Out.Redirect != "group" || src.Streams[0].Out.Out != "dst" || !src.Streams[0].Status {
		t.Fatalf("dst = %+v", src.Streams[0])
	}
	if src.Streams[1].Out.Out != "missing-alias-4" || src.Streams[1].Out.Redirect != "group" {
		t.Fatalf("empty alias target = %+v", src.Streams[1].Out)
	}
	if src.Streams[2].Out.Out != "same-dup-8" || src.Streams[2].Out.Redirect != "group" {
		t.Fatalf("dup target = %+v", src.Streams[2].Out)
	}
	if !src.Streams[3].Status || src.Streams[3].Out.Redirect != "stop" || src.Streams[3].Out.Out != "" {
		t.Fatalf("unknown = %+v", src.Streams[3])
	}
	text := lossesText(res.Losses)
	if !strings.Contains(text, "целевая кампания 404 не найдена") || strings.Contains(text, "action_type campaign нет соответствия") {
		t.Fatalf("losses %s", text)
	}
	dst, _ := findGroup(res.Groups, "dst")
	if !dst.Streams[0].Status || dst.Streams[0].Rules.Country.Flag != 2 || dst.Streams[0].Rules.IPVersion != 2 {
		t.Fatalf("dst stream = %+v", dst.Streams[0])
	}
	for _, gone := range []string{
		"фильтр ipv6",
		"фильтр ipv_4",
		"фильтр ipv_6",
		"фильтр ip_version",
		"нет фильтра версии IP",
		"нет переносимых фильтров",
	} {
		if strings.Contains(text, gone) {
			t.Errorf("still present %s\n%s", gone, text)
		}
	}
}

func TestLongURLStaysInline(t *testing.T) {
	payload := "https://a.example/" + strings.Repeat("a", 600)
	var losses []Loss
	s := (&mapCtx{losses: &losses, pages: &pageBag{}}).buildStream(KStream{ID: 3, State: "active", ActionType: "http", ActionPayload: payload}, false)
	if s.Out.Redirect != "http_redirect" || s.Out.Out != payload {
		t.Fatalf("%+v", s.Out)
	}
}
