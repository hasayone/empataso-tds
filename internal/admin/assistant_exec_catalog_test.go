package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// aiCatalogPage is the answer of a catalog list tool.
type aiCatalogPage struct {
	Items  []map[string]any `json:"items"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

// bigOffers adds count offers named "Bulk offer NNNN" and one "AI-E2E target offer" to the offers
// of the seeded store, and returns the size of the store.
func (e *aiToolEnv) bigOffers(t *testing.T, count int) int {
	t.Helper()
	ctx := context.Background()
	offers, err := e.s.cfg.Offers.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= count; i++ {
		offers = append(offers, config.Offer{ID: fmt.Sprintf("b%04d", i), Name: fmt.Sprintf("Bulk offer %04d", i), Status: true, URL: "https://bulk.test/o"})
	}
	offers = append(offers, config.Offer{ID: "e2e1", Name: "AI-E2E target offer", Status: true, URL: "https://e2e.test/o"})
	if err := e.s.cfg.Offers.Save(ctx, offers); err != nil {
		t.Fatal(err)
	}
	return len(offers)
}

// catalogPage runs a catalog list tool and decodes its page; the result must fit aiResultMax.
func (e *aiToolEnv) catalogPage(t *testing.T, sess security.Session, name, args string) aiCatalogPage {
	t.Helper()
	res, err := e.runTool(t, sess, name, args)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	limit := aiResultMax
	if name == "list_campaigns" && !strings.Contains(args, `"ids"`) {
		limit = aiCampaignResultMax
	}
	if len(res) > limit {
		t.Errorf("%s %s: %d bytes, over %d", name, args, len(res), limit)
	}
	var page aiCatalogPage
	if err := json.Unmarshal(res, &page); err != nil {
		t.Fatalf("%s %s: %v: %s", name, args, err, res)
	}
	return page
}

func TestAssistantExecCatalogSearchByName(t *testing.T) {
	env := newAIToolEnv(t)
	size := env.bigOffers(t, 1500)
	page := env.catalogPage(t, env.root, "list_offers", `{"q":"AI-E2E"}`)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0]["id"] != "e2e1" {
		t.Errorf("q found %+v", page)
	}
	if page := env.catalogPage(t, env.root, "list_offers", `{"q":"  ai-e2e TARGET "}`); page.Total != 1 {
		t.Errorf("q is not case-insensitive or trimmed: %+v", page)
	}
	if page := env.catalogPage(t, env.root, "list_offers", `{"q":"B0042"}`); page.Total != 1 || page.Items[0]["name"] != "Bulk offer 0042" {
		t.Errorf("q by id: %+v", page)
	}
	if page := env.catalogPage(t, env.root, "list_offers", `{"q":"no such offer"}`); page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Errorf("an empty search: %+v", page)
	}
	if wide := env.catalogPage(t, env.root, "list_offers", `{"q":"offer","limit":1}`); wide.Total != 1501 || len(wide.Items) != 1 {
		t.Errorf("a wide search: total %d of %d offers, %d items", wide.Total, size, len(wide.Items))
	}
}

func TestAssistantExecCatalogPaging(t *testing.T) {
	env := newAIToolEnv(t)
	size := env.bigOffers(t, 1500)
	first := env.catalogPage(t, env.root, "list_offers", `{}`)
	if first.Total != size || first.Limit != aiCatalogLimitDefault || first.Offset != 0 || len(first.Items) == 0 || len(first.Items) > aiCatalogLimitDefault {
		t.Errorf("no search on a large catalog: total %d limit %d items %d (store %d)", first.Total, first.Limit, len(first.Items), size)
	}
	wide := env.catalogPage(t, env.root, "list_offers", `{"limit":500}`)
	if wide.Limit != aiReadLimitMax || len(wide.Items) > aiReadLimitMax || wide.Total != size {
		t.Errorf("limit 500: limit %d items %d total %d", wide.Limit, len(wide.Items), wide.Total)
	}
	if low := env.catalogPage(t, env.root, "list_offers", `{"limit":0}`); low.Limit != 1 {
		t.Errorf("limit 0 gives %d", low.Limit)
	}
	tail := env.catalogPage(t, env.root, "list_offers", `{"q":"Bulk offer 01","limit":50,"offset":90}`)
	if tail.Total != 100 || tail.Offset != 90 || len(tail.Items) != 10 || tail.Items[0]["id"] != "b0190" {
		t.Errorf("page 2 of the search: %+v", tail)
	}
	if past := env.catalogPage(t, env.root, "list_offers", `{"q":"Bulk","offset":99999}`); past.Total != 1500 || len(past.Items) != 0 {
		t.Errorf("offset past the end: %+v", past)
	}
}

func TestAssistantExecCatalogIDs(t *testing.T) {
	env := newAIToolEnv(t)
	env.bigOffers(t, 1500)
	page := env.catalogPage(t, env.root, "list_offers", `{"ids":["e2e1","b0005","missing",""]}`)
	got := map[string]bool{}
	for _, it := range page.Items {
		got[it["id"].(string)] = true
	}
	if page.Total != 2 || !got["e2e1"] || !got["b0005"] {
		t.Errorf("ids found %+v", page)
	}
	if page := env.catalogPage(t, env.root, "list_offers", `{"ids":["e2e1","b0005"],"q":"bulk"}`); page.Total != 1 || page.Items[0]["id"] != "b0005" {
		t.Errorf("ids and q together: %+v", page)
	}
	if page := env.catalogPage(t, env.root, "list_offers", `{"ids":[]}`); page.Total != 0 {
		t.Errorf("an empty ids list matched %d", page.Total)
	}
}

func TestAssistantExecCatalogLimitedUserOnlyFindsOwn(t *testing.T) {
	env := newAIToolEnv(t)
	env.bigOffers(t, 300)
	lim := env.limitedSession(t, "lim", security.Access{
		Sections: []string{"offers"},
		Grants:   map[string]security.Grant{"offers": {Mode: security.ModeSelected, IDs: []string{"o1"}}},
		Hidden:   []string{"profit"},
	})
	if page := env.catalogPage(t, lim, "list_offers", `{"q":"AI-E2E"}`); page.Total != 0 || len(page.Items) != 0 {
		t.Errorf("a foreign offer was found: %+v", page)
	}
	if page := env.catalogPage(t, lim, "list_offers", `{"q":"Beta"}`); page.Total != 0 {
		t.Errorf("a foreign offer was found by name: %+v", page)
	}
	if page := env.catalogPage(t, lim, "list_offers", `{"ids":["o2","e2e1"]}`); page.Total != 0 {
		t.Errorf("foreign ids were found: %+v", page)
	}
	own := env.catalogPage(t, lim, "list_offers", `{"q":"alpha"}`)
	if own.Total != 1 || own.Items[0]["id"] != "o1" {
		t.Errorf("an own offer was not found: %+v", own)
	}
	if all := env.catalogPage(t, lim, "list_offers", `{}`); all.Total != 1 {
		t.Errorf("the limited user sees %d offers", all.Total)
	}
	if strings.Contains(aiTestJSON(own), "7777") {
		t.Errorf("the payout is shown: %+v", own)
	}
}

func TestAssistantExecCatalogOtherLists(t *testing.T) {
	env := newAIToolEnv(t)
	if page := env.catalogPage(t, env.root, "list_domains", `{"q":"TDS.EXAMPLE"}`); page.Total != 1 || page.Items[0]["id"] != "d1" {
		t.Errorf("a domain is found by host: %+v", page)
	}
	camp := env.catalogPage(t, env.root, "list_campaigns", `{"q":"demo"}`)
	if camp.Total != 1 || camp.Items[0]["id"] != "g1" {
		t.Fatalf("a campaign is found by name: %+v", camp)
	}
	if text := aiTestJSON(camp); strings.Contains(text, "CANARYPB") || strings.Contains(text, "CANARYCURL") || strings.Contains(text, "postbacks") {
		t.Errorf("campaign page keeps outbound fields: %s", text)
	}
	for name, args := range map[string]string{
		"list_landings": `{"q":"land"}`, "list_affiliate_networks": `{"q":"net"}`, "list_traffic_sources": `{"q":"src"}`,
	} {
		if page := env.catalogPage(t, env.root, name, args); page.Total != 1 {
			t.Errorf("%s %s: %+v", name, args, page)
		}
	}
}

func TestAssistantExecCatalogKeepsSecretsOut(t *testing.T) {
	env := newAIToolEnv(t)
	env.bigOffers(t, 200)
	res, err := env.runTool(t, env.root, "list_offers", `{"q":"note "}`)
	if err != nil {
		t.Fatal(err)
	}
	env.assertClean(t, "list_offers q", string(res))
	if !strings.Contains(string(res), "note cloudflare ***") {
		t.Errorf("the search lost the redacted record: %s", res)
	}
}

func TestAssistantExecCatalogArgumentsStayOutOfTheHandlerQuery(t *testing.T) {
	tool, _ := aiToolNamed("list_offers")
	req, err := aiBuildRequest(context.Background(), "", tool, json.RawMessage(`{"q":"x","ids":["a","b"],"limit":30,"offset":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.RawQuery != "" {
		t.Errorf("the handler got the query %q", req.URL.RawQuery)
	}
	if _, err := aiBuildRequest(context.Background(), "", tool, json.RawMessage(`{"filter":"x"}`)); err == nil {
		t.Error("an unknown argument was accepted")
	}
	logs, _ := aiToolNamed("list_logs")
	if logs.catalog {
		t.Error("list_logs is a catalog tool")
	}
}

func TestAssistantExecCatalogBadArguments(t *testing.T) {
	env := newAIToolEnv(t)
	tooMany := `{"ids":[` + strings.TrimSuffix(strings.Repeat(`"x",`, aiCatalogIDsMax+1), ",") + `]}`
	for label, args := range map[string]string{
		"q array": `{"q":["a"]}`, "ids string": `{"ids":"a"}`, "ids nested": `{"ids":[["a"]]}`, "limit text": `{"limit":"many"}`,
		"offset negative": `{"offset":-1}`, "offset text": `{"offset":"x"}`, "too many ids": tooMany,
	} {
		_, err := env.runTool(t, env.root, "list_offers", args)
		wantToolCode(t, label, err, "bad_request")
	}
}

func TestAssistantExecCatalogReadsAboveTheOldCeiling(t *testing.T) {
	env := newAIToolEnv(t)
	ctx := context.Background()
	pad := strings.Repeat("p", 1<<10)
	offers := make([]config.Offer, 0, 400)
	for i := 1; i <= 400; i++ {
		offers = append(offers, config.Offer{ID: fmt.Sprintf("big%03d", i), Name: fmt.Sprintf("Heavy %03d", i), Status: true, URL: "https://x.test/" + pad})
	}
	if err := env.s.cfg.Offers.Save(ctx, offers); err != nil {
		t.Fatal(err)
	}
	page := env.catalogPage(t, env.root, "list_offers", `{"q":"heavy 005"}`)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0]["id"] != "big005" {
		t.Errorf("a list above %d bytes was not searched: %+v", aiRawResultMax, page)
	}
	more := make([]config.Offer, 0, 100)
	for i := 0; i < 100; i++ {
		more = append(more, config.Offer{ID: fmt.Sprintf("huge%d", i), Name: "Huge", Status: true, URL: "https://x.test/" + strings.Repeat("p", 100<<10)})
	}
	if err := env.s.cfg.Offers.Save(ctx, more); err != nil {
		t.Fatal(err)
	}
	_, err := env.runTool(t, env.root, "list_offers", `{"q":"huge"}`)
	wantToolCode(t, "a list above the catalog ceiling", err, aiErrResultTooLarge)
}

func TestAssistantToolsCatalogSpecs(t *testing.T) {
	want := map[string]bool{
		"list_offers": true, "list_landings": true, "list_affiliate_networks": true,
		"list_traffic_sources": true, "list_domains": true, "list_campaigns": true,
	}
	for _, tool := range aiRegistry() {
		if tool.catalog != want[tool.Name] {
			t.Errorf("%s: catalog = %v", tool.Name, tool.catalog)
		}
		if !tool.catalog {
			continue
		}
		props, _ := tool.Spec.Schema["properties"].(map[string]any)
		for _, k := range []string{"q", "ids", "limit", "offset"} {
			if props[k] == nil {
				t.Errorf("%s: no %s argument", tool.Name, k)
			}
		}
		limit, _ := props["limit"].(map[string]any)
		wantMax := aiReadLimitMax
		if tool.Name == "list_campaigns" {
			wantMax = aiCampaignLimitMax
		}
		if limit["maximum"] != wantMax {
			t.Errorf("%s: limit = %v", tool.Name, limit)
		}
		if !strings.Contains(tool.Spec.Description, "Use q to search by name") {
			t.Errorf("%s: the description does not point to q: %s", tool.Name, tool.Spec.Description)
		}
	}
}
