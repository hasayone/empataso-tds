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

// bigCampaigns adds count campaigns "camp-NNNN" to the seeded store, each with a stream whose
// comment is pad bytes long, so the whole list passes aiCatalogRawMax. It returns the store size.
func (e *aiToolEnv) bigCampaigns(t *testing.T, count, pad int) int {
	t.Helper()
	ctx := context.Background()
	list, err := e.groups.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	comment := strings.Repeat("c", pad)
	for i := 1; i <= count; i++ {
		list = append(list, config.Group{
			ID: fmt.Sprintf("camp-%04d", i), Num: 1000 + i, Name: fmt.Sprintf("Imported %04d", i), Status: i%2 == 0,
			Streams: []config.Stream{{Name: "s1", Status: true, Comment: comment}, {Name: "s2", Comment: comment}},
		})
	}
	if err := e.groups.Save(ctx, list); err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func TestAssistantCampaignsSmallLimitGivesAPageNotAnError(t *testing.T) {
	env := newAIToolEnv(t)
	size := env.bigCampaigns(t, 5500, 2<<10)
	raw := 0
	list, _ := env.groups.List(context.Background())
	for _, g := range list {
		raw += len(mustJSONText(t, g))
	}
	if raw <= aiCatalogRawMax {
		t.Fatalf("the fixture must pass the old raw ceiling: %d <= %d", raw, aiCatalogRawMax)
	}
	for _, args := range []string{`{"limit":1}`, `{}`, `{"limit":50,"offset":5000}`} {
		page := env.catalogPage(t, env.root, "list_campaigns", args)
		if page.Total != size || page.Total <= len(page.Items) || len(page.Items) == 0 {
			t.Errorf("%s: total %d, %d items, store %d", args, page.Total, len(page.Items), size)
		}
	}
	one := env.catalogPage(t, env.root, "list_campaigns", `{"limit":1}`)
	if one.Limit != 1 || len(one.Items) != 1 || one.Items[0]["id"] != "g1" {
		t.Errorf("limit 1: %+v", one)
	}
	row := one.Items[0]
	if row["streams_total"] != float64(1) || row["streams_active"] != float64(1) || row["streams"] != nil {
		t.Errorf("the row is not a summary: %+v", row)
	}
}

func TestAssistantCampaignsPagesDoNotRepeatAndReachEveryCampaign(t *testing.T) {
	env := newAIToolEnv(t)
	size := env.bigCampaigns(t, 600, 1<<10)
	first := env.catalogPage(t, env.root, "list_campaigns", `{"limit":3}`)
	second := env.catalogPage(t, env.root, "list_campaigns", `{"limit":3,"offset":3}`)
	if len(first.Items) != 3 || len(second.Items) != 3 {
		t.Fatalf("pages of %d and %d", len(first.Items), len(second.Items))
	}
	for _, a := range first.Items {
		for _, b := range second.Items {
			if a["id"] == b["id"] {
				t.Errorf("the second page repeats %v", a["id"])
			}
		}
	}
	seen := map[string]bool{}
	for offset := 0; offset < size; offset += aiReadLimitMax {
		page := env.catalogPage(t, env.root, "list_campaigns", fmt.Sprintf(`{"limit":%d,"offset":%d}`, aiReadLimitMax, offset))
		if page.Total != size || page.Offset != offset {
			t.Fatalf("offset %d: total %d offset %d", offset, page.Total, page.Offset)
		}
		for _, it := range page.Items {
			id := it["id"].(string)
			if seen[id] {
				t.Fatalf("campaign %s came twice", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != size {
		t.Errorf("paging reached %d of %d campaigns", len(seen), size)
	}
}

func TestAssistantCampaignsSearchAndWholeRecord(t *testing.T) {
	env := newAIToolEnv(t)
	env.bigCampaigns(t, 300, 512)
	if page := env.catalogPage(t, env.root, "list_campaigns", `{"q":"imported 0042"}`); page.Total != 1 || page.Items[0]["id"] != "camp-0042" {
		t.Errorf("search by name: %+v", page)
	}
	if page := env.catalogPage(t, env.root, "list_campaigns", `{"q":"1042"}`); page.Total != 1 || page.Items[0]["id"] != "camp-0042" {
		t.Errorf("search by number: %+v", page)
	}
	whole := env.catalogPage(t, env.root, "list_campaigns", `{"ids":["g1"]}`)
	if whole.Total != 1 || whole.Items[0]["streams"] == nil || whole.Items[0]["cost_token"] != "{price}" {
		t.Errorf("ids give no whole campaign: %+v", whole)
	}
	many := make([]string, aiCampaignFullMax+1)
	for i := range many {
		many[i] = fmt.Sprintf("camp-%04d", i+1)
	}
	page := env.catalogPage(t, env.root, "list_campaigns", `{"ids":["`+strings.Join(many, `","`)+`"]}`)
	if page.Total != len(many) || page.Items[0]["streams"] != nil || page.Items[0]["streams_total"] != float64(2) {
		t.Errorf("a long id list must stay summaries: %+v", page)
	}
}

func TestAssistantCampaignsWholeRecordNeverEmpty(t *testing.T) {
	env := newAIToolEnv(t)
	long := strings.Repeat("x", 3000)
	streams := make([]config.Stream, 40)
	for i := range streams {
		streams[i] = config.Stream{Name: fmt.Sprintf("stream %d", i), Status: true, Comment: long}
	}
	if err := env.groups.Save(context.Background(), []config.Group{{ID: "fat", Name: "Fat", Status: true, Streams: streams}}); err != nil {
		t.Fatal(err)
	}
	page := env.catalogPage(t, env.root, "list_campaigns", `{"ids":["fat"],"limit":1}`)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0]["id"] != "fat" {
		t.Fatalf("a campaign over the result size lost its record: %+v", page)
	}
}

func TestAssistantCampaignsForeignAccessStaysClosed(t *testing.T) {
	env := newAIToolEnv(t)
	env.bigCampaigns(t, 200, 256)
	lim := env.limitedSession(t, "camp-lim", security.Access{
		Sections: []string{"flows"},
		Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeSelected, IDs: []string{"camp-0007"}}},
	})
	all := env.catalogPage(t, lim, "list_campaigns", `{"limit":50}`)
	if all.Total != 1 || len(all.Items) != 1 || all.Items[0]["id"] != "camp-0007" {
		t.Fatalf("the limited user sees %+v", all)
	}
	for _, args := range []string{`{"q":"Demo"}`, `{"ids":["g1","camp-0008"]}`, `{"q":"Imported 0008"}`} {
		if page := env.catalogPage(t, lim, "list_campaigns", args); page.Total != 0 || len(page.Items) != 0 {
			t.Errorf("%s: a foreign campaign was found: %+v", args, page)
		}
	}
	if page := env.catalogPage(t, lim, "list_campaigns", `{"ids":["camp-0007"]}`); page.Total != 1 || page.Items[0]["streams"] == nil {
		t.Errorf("an own campaign is not given whole: %+v", page)
	}
}

func TestAssistantCampaignsPageLargerThanFifty(t *testing.T) {
	env := newAIToolEnv(t)
	size := env.bigCampaigns(t, 5500, 2<<10)
	page := env.catalogPage(t, env.root, "list_campaigns", `{"limit":200}`)
	if len(page.Items) != aiCampaignLimitMax || page.Limit != aiCampaignLimitMax || page.Total != size {
		t.Fatalf("limit 200: %d items, limit %d, total %d", len(page.Items), page.Limit, page.Total)
	}
	if more := env.catalogPage(t, env.root, "list_campaigns", `{"limit":5000}`); more.Limit != aiCampaignLimitMax || len(more.Items) > aiCampaignLimitMax {
		t.Errorf("limit 5000 is not cut to %d: limit %d, %d items", aiCampaignLimitMax, more.Limit, len(more.Items))
	}
	if def := env.catalogPage(t, env.root, "list_campaigns", `{}`); def.Limit != aiCampaignLimitDefault || len(def.Items) != aiCampaignLimitDefault {
		t.Errorf("default page: limit %d, %d items", def.Limit, len(def.Items))
	}
	if page.Items[0]["streams"] != nil {
		t.Error("a summary carries streams")
	}
}

// TestAssistantCampaignsNextOffsetWalksEveryCampaign reads the list by next_offset only. The rows are
// long, so a page holds fewer rows than limit; no row may be lost or read twice.
func TestAssistantCampaignsNextOffsetWalksEveryCampaign(t *testing.T) {
	env := newAIToolEnv(t)
	ctx := context.Background()
	list := make([]config.Group, 0, 1200)
	for i := 0; i < 1200; i++ {
		list = append(list, config.Group{ID: fmt.Sprintf("long-%04d", i), Name: fmt.Sprintf("%04d %s", i, strings.Repeat("n", 300)), Status: true})
	}
	if err := env.groups.Save(ctx, list); err != nil {
		t.Fatal(err)
	}
	type page struct {
		Items      []map[string]any `json:"items"`
		Total      int              `json:"total"`
		NextOffset *int             `json:"next_offset"`
	}
	seen, offset, pages := map[string]bool{}, 0, 0
	for {
		res, err := env.runTool(t, env.root, "list_campaigns", fmt.Sprintf(`{"limit":200,"offset":%d}`, offset))
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		if len(res) > aiCampaignResultMax {
			t.Fatalf("offset %d: %d bytes, over %d", offset, len(res), aiCampaignResultMax)
		}
		var p page
		if err := json.Unmarshal(res, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Items) == 0 || len(p.Items) >= aiCampaignLimitMax || p.Total != 1200 {
			t.Fatalf("offset %d: %d items, total %d", offset, len(p.Items), p.Total)
		}
		for _, it := range p.Items {
			id := it["id"].(string)
			if seen[id] {
				t.Fatalf("%s came twice", id)
			}
			seen[id] = true
		}
		pages++
		if p.NextOffset == nil {
			break
		}
		if *p.NextOffset != offset+len(p.Items) {
			t.Fatalf("next_offset %d after %d items from %d", *p.NextOffset, len(p.Items), offset)
		}
		offset = *p.NextOffset
	}
	if len(seen) != 1200 || pages < 2 {
		t.Errorf("read %d of 1200 campaigns in %d pages", len(seen), pages)
	}
}

func TestAssistantCampaignsSpecTellsHowToPage(t *testing.T) {
	tool, ok := aiToolNamed("list_campaigns")
	if !ok {
		t.Fatal("no list_campaigns tool")
	}
	props, _ := tool.Spec.Schema["properties"].(map[string]any)
	if props["shared"] != nil {
		t.Error("the shared argument gives the whole list as one object")
	}
	for _, want := range []string{"offset", "total", "ids", "next_offset", "never call a partial list"} {
		if !strings.Contains(tool.Spec.Description, want) {
			t.Errorf("the description lacks %q: %s", want, tool.Spec.Description)
		}
	}
	limit, _ := props["limit"].(map[string]any)
	if limit["maximum"] != aiCampaignLimitMax {
		t.Errorf("limit: %v", limit)
	}
	for _, bad := range []string{"limit 50", "offset 25", "at most 50"} {
		if strings.Contains(tool.Spec.Description, bad) || strings.Contains(fmt.Sprint(limit), bad) {
			t.Errorf("the spec pushes the model to %q", bad)
		}
	}
	if strings.Contains(tool.Spec.Description, "every campaign") {
		t.Errorf("the description still promises every campaign: %s", tool.Spec.Description)
	}
}
