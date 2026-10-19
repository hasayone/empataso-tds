package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// priceCampaign is c1 with the price names "price" and "cur" on the given source.
func priceCampaign(sourceID string) config.Group {
	g := plainCampaign("https://a.example/")
	g.CostParam, g.CurrencyParam, g.SourceID = "price", "cur", sourceID
	return g
}

// putSourceAs sends the source as client c and returns the status and the body.
func (r *rolesRig) putSourceAs(t *testing.T, c *http.Client, csrf, id string, params map[string]string) (int, string) {
	t.Helper()
	code, raw := r.api(t, c, csrf, http.MethodPut, "/api/traffic-sources/"+id,
		config.TrafficSource{ID: id, Name: id, Parameters: params})
	return code, string(raw)
}

func TestHiddenPriceSourceParameterCannotReadPriceName(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.resetCampaign(priceCampaign(""))
	rig.storedGroups().list[1].CostParam = "sub_id_2"
	rig.seedSource(t, "s1", map[string]string{"sub_id_1": "p1", "keyword": "kw"})
	rig.seedSource(t, "s2", map[string]string{"sub_id_2": "x"})
	rig.seedSource(t, "s3", map[string]string{"sub_id_4": "price"})
	rig.setRole(t, editorAccess("cost"))

	for _, c := range []struct {
		name   string
		params map[string]string
		want   int
	}{
		{"sub_id_1 onto the price name", map[string]string{"sub_id_1": "price", "keyword": "kw"}, http.StatusBadRequest},
		{"sub_id_1 onto the currency name", map[string]string{"sub_id_1": "cur", "keyword": "kw"}, http.StatusBadRequest},
		{"extra field onto the price name", map[string]string{"sub_id_1": "p1", "keyword": "kw", "site": "price"}, http.StatusBadRequest},
		{"keyword onto the price name", map[string]string{"sub_id_1": "p1", "keyword": "price"}, http.StatusBadRequest},
		{"sub_id_14 onto the price name", map[string]string{"sub_id_1": "p1", "keyword": "kw", "sub_id_14": "price"}, http.StatusBadRequest},
		{"price name with spaces", map[string]string{"sub_id_1": " price ", "keyword": "kw"}, http.StatusBadRequest},
		{"an unrelated name", map[string]string{"sub_id_1": "p9", "keyword": "kw"}, http.StatusOK},
	} {
		if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s1", c.params); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, body, c.want)
		}
	}

	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s2", map[string]string{}); code != http.StatusBadRequest {
		t.Errorf("clearing sub_id_2 makes it read its plain name, the price name of campaign c2: %d %s", code, body)
	}
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s2", map[string]string{"sub_id_2": "y"}); code != http.StatusOK {
		t.Errorf("sub_id_2 remapped to another name: %d %s", code, body)
	}
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s3", map[string]string{"sub_id_4": "price"}); code != http.StatusOK {
		t.Errorf("a binding stored as it is: %d %s", code, body)
	}
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s3", map[string]string{}); code != http.StatusOK {
		t.Errorf("clearing a stored binding to the price name: %d %s", code, body)
	}
	code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/traffic-sources",
		config.TrafficSource{ID: "n1", Name: "new", Parameters: map[string]string{"sub_id_5": "price"}})
	if code != http.StatusBadRequest {
		t.Errorf("new source with sub_id_5 on the price name: %d", code)
	}
	code, _ = rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/traffic-sources",
		config.TrafficSource{ID: "n2", Name: "new", Parameters: map[string]string{"sub_id_5": "other"}})
	if code != http.StatusCreated {
		t.Errorf("new source with an unrelated name: %d", code)
	}

	rig.setRole(t, editorAccess("ip"))
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s1", map[string]string{"sub_id_1": "price"}); code != http.StatusOK {
		t.Errorf("price visible: %d %s", code, body)
	}
	rig.setRole(t, editorAccess())
	if code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s1", map[string]string{"sub_id_1": "cur"}); code != http.StatusOK {
		t.Errorf("nothing hidden: %d %s", code, body)
	}
	if code, body := rig.putSourceAs(t, rig.admin, rig.csrf, "s1", map[string]string{"sub_id_1": "price", "site": "cur"}); code != http.StatusOK {
		t.Errorf("administrator: %d %s", code, body)
	}
}

func TestHiddenPriceSourceRefusalDoesNotNameThePrice(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.resetCampaign(priceCampaign(""))
	rig.seedSource(t, "s1", map[string]string{"sub_id_1": "p1"})
	rig.setRole(t, editorAccess("cost"))
	code, body := rig.putSourceAs(t, rig.user, rig.ucsrf, "s1", map[string]string{"sub_id_1": "price"})
	if code != http.StatusBadRequest || !strings.Contains(body, "source maps sub_id_1, a hidden field") {
		t.Fatalf("%d %s", code, body)
	}
	for _, leak := range []string{"price", "cur", "cost_param", "currency_param", "c1"} {
		if strings.Contains(body, leak) {
			t.Errorf("the answer names %q: %s", leak, body)
		}
	}
}

func TestHiddenPriceCampaignSourceSwitchRefused(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "mprice", map[string]string{"sub_id_1": "price"})
	rig.seedSource(t, "mcur", map[string]string{"site": "cur"})
	rig.seedSource(t, "mkw", map[string]string{"keyword": "price"})
	rig.seedSource(t, "clean", map[string]string{"sub_id_1": "p1"})
	rig.seedSource(t, "plain", map[string]string{})
	base := priceCampaign("")

	for _, c := range []struct {
		hidden []string
		source string
		want   int
	}{
		{[]string{"cost"}, "mprice", http.StatusBadRequest},
		{[]string{"cost"}, "mcur", http.StatusBadRequest},
		{[]string{"cost"}, "mkw", http.StatusBadRequest},
		{[]string{"cost"}, "clean", http.StatusOK},
		{[]string{"cost"}, "plain", http.StatusOK},
		{[]string{"cost", "sub_id_1"}, "mprice", http.StatusBadRequest},
		{[]string{"ip"}, "mprice", http.StatusOK},
		{nil, "mprice", http.StatusOK},
	} {
		rig.resetCampaign(base)
		rig.setRole(t, editorAccess(c.hidden...))
		next := base
		next.SourceID = c.source
		if got := rig.putCampaign(t, next); got != c.want {
			t.Errorf("hidden %v, source %s: %d, want %d", c.hidden, c.source, got, c.want)
		}
	}

	rig.resetCampaign(base)
	next := base
	next.SourceID = "mprice"
	if code, raw := rig.api(t, rig.admin, rig.csrf, http.MethodPut, "/api/groups/c1", next); code != http.StatusOK {
		t.Errorf("administrator: %d %s", code, raw)
	}
}

func TestHiddenPriceDefaultNameCollidesWithSourceField(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "mapped", map[string]string{"sub_id_3": "z"})
	rig.seedSource(t, "plain", map[string]string{})
	rig.seedSource(t, "other", map[string]string{"sub_id_1": "p1"})
	base := priceCampaign("")
	base.CostParam = "sub_id_3"
	rig.setRole(t, editorAccess("cost"))

	cases := []struct {
		name     string
		from, to string
		want     int
	}{
		{"from a mapped source to one that leaves sub_id_3 on its plain name", "mapped", "plain", http.StatusBadRequest},
		{"from none to a mapped source", "", "mapped", http.StatusOK},
		{"from none to a source that also leaves it plain: the campaign read it before", "", "plain", http.StatusOK},
		{"the same mapped source, a rename", "mapped", "mapped", http.StatusOK},
	}
	for _, c := range cases {
		g := base
		g.SourceID = c.from
		rig.resetCampaign(g)
		next := g
		next.SourceID = c.to
		if c.from == c.to {
			next.Name = "renamed"
		}
		if got := rig.putCampaign(t, next); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}

	g := base
	g.SourceID = "mapped"
	rig.resetCampaign(g)
	next := g
	next.SourceID = "other"
	if got := rig.putCampaign(t, next); got != http.StatusBadRequest {
		t.Errorf("a source that leaves sub_id_3 on the plain name of the price: %d", got)
	}
}

func TestHiddenPriceParametersOfCampaignStayFixed(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "clean", map[string]string{"sub_id_1": "p1"})
	base := priceCampaign("clean")
	rig.resetCampaign(base)

	rig.setRole(t, editorAccess("cost"))
	for name, edit := range map[string]func(*config.Group){
		"cost_param moved onto a name a visible field reads": func(g *config.Group) { g.CostParam = "p1" },
		"cost_param onto a plain Sub ID name":                func(g *config.Group) { g.CostParam = "sub_id_2" },
		"currency_param cleared":                             func(g *config.Group) { g.CurrencyParam = "" },
	} {
		rig.resetCampaign(base)
		next := base
		edit(&next)
		if got := rig.putCampaign(t, next); got != http.StatusBadRequest {
			t.Errorf("%s: %d", name, got)
		}
	}
	rig.resetCampaign(base)
	same := base
	same.Name = "renamed"
	if got := rig.putCampaign(t, same); got != http.StatusOK {
		t.Errorf("the saved price parameters kept: %d", got)
	}

	rig.setRole(t, editorAccess("ip"))
	rig.resetCampaign(base)
	moved := base
	moved.CostParam = "p1"
	if got := rig.putCampaign(t, moved); got != http.StatusOK {
		t.Errorf("price visible: %d", got)
	}
	rig.resetCampaign(base)
	if code, raw := rig.api(t, rig.admin, rig.csrf, http.MethodPut, "/api/groups/c1", moved); code != http.StatusOK {
		t.Errorf("administrator: %d %s", code, raw)
	}
}
