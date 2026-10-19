package ktimport

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// loadFixture читает синтетический снимок.
//
// Параметры: t — тест.
// Результат: снимок из testdata.
// Ошибки: падение теста, если файл не читается.
func loadFixture(t *testing.T) Snapshot {
	t.Helper()
	raw, err := os.ReadFile("../testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// findGroup ищет группу по id.
//
// Параметры: groups — список; id — искомый id.
// Результат: группа и true, если найдена.
// Ошибки: нет.
func findGroup(groups []Group, id string) (Group, bool) {
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

func TestMapFixture(t *testing.T) {
	res := MapAll(loadFixture(t))
	promo, ok := findGroup(res.Groups, "offer-es")
	if !ok || !promo.Status || promo.Name != "Offer ES" || promo.Num != 1 {
		t.Fatalf("promo = %+v", promo)
	}
	if len(promo.Streams) != 1 || !promo.Streams[0].Status {
		t.Fatalf("promo streams = %+v", promo.Streams)
	}
	if !strings.Contains(promo.Streams[0].Out.Out, "[CID]") || strings.Contains(promo.Streams[0].Out.Out, "{subid}") {
		t.Fatalf("macro url = %s", promo.Streams[0].Out.Out)
	}
	if promo.Streams[0].Rules.Country.Flag != 2 || promo.Streams[0].Name != "es" || promo.Streams[0].Comment != "keitaro_stream_id=11" {
		t.Fatalf("rules = %+v", promo.Streams[0])
	}
	if promo.Streams[0].Out.Chance != 100 || len(promo.Streams[0].Out.Offers) != 1 || promo.Streams[0].Out.Offers[0].ID != "101" {
		t.Fatalf("offer ref = %+v", promo.Streams[0].Out)
	}

	split, _ := findGroup(res.Groups, "split")
	if len(split.Streams) != 1 || split.Streams[0].Name != "or" || !split.Streams[0].FilterOr || !split.Streams[0].Status {
		t.Fatalf("filter_or = %+v", split.Streams)
	}
	if split.Streams[0].Rules.Country.Flag != 2 || split.Streams[0].Rules.City.Flag != 2 {
		t.Fatalf("or rules = %+v", split.Streams[0].Rules)
	}

	weights, _ := findGroup(res.Groups, "weights")
	if !weights.Status || !weights.Weight || len(weights.Streams) == 0 || !weights.Streams[0].Status || weights.Streams[0].Weight != 25 {
		t.Fatalf("weight = %+v", weights)
	}

	missing, _ := findGroup(res.Groups, "missing-alias-4")
	if missing.Status {
		t.Fatal("empty alias is enabled")
	}

	shares, _ := findGroup(res.Groups, "shares")
	out := shares.Streams[0].Out
	if strings.Contains(out.Out, "|||") || len(out.Offers) != 2 {
		t.Fatalf("shares = %+v", out)
	}
	if out.Offers[0].ID != "501" || out.Offers[0].Share != 70 || out.Offers[1].ID != "502" || out.Offers[1].Share != 30 {
		t.Fatalf("share refs = %+v", out.Offers)
	}
	if out.Out != "https://a.example/o" {
		t.Fatalf("fallback = %s", out.Out)
	}

	linked, _ := findGroup(res.Groups, "linked")
	if !linked.Status || !linked.Weight || linked.Folder != "EU" || linked.DomainID != "3" || linked.SourceID != "1" {
		t.Fatalf("linked = %+v", linked)
	}
	if len(linked.Postbacks) != 1 || linked.Postbacks[0].Method != "POST" || linked.Postbacks[0].URL != "https://pb.example/hook" {
		t.Fatalf("postbacks = %+v", linked.Postbacks)
	}
	if len(linked.Streams) != 1 || !linked.Streams[0].FilterOr || !linked.Streams[0].Status || linked.Streams[0].Weight != 40 {
		t.Fatalf("linked stream = %+v", linked.Streams)
	}
	if strings.Contains(linked.Streams[0].Out.Out, "|||") || len(linked.Streams[0].Out.Offers) != 2 {
		t.Fatalf("linked offers = %+v", linked.Streams[0].Out)
	}

	mystery, _ := findGroup(res.Groups, "mystery")
	if mystery.Streams[0].Status {
		t.Fatal("unknown-only stream stayed enabled")
	}

	local, _ := findGroup(res.Groups, "local")
	if len(local.Streams) != 1 || !local.Streams[0].Status || local.Streams[0].Out.Redirect != "landing" || local.Streams[0].Out.Out != "" {
		t.Fatalf("local landing stream = %+v", local.Streams)
	}
	if len(local.Streams[0].Out.Landings) != 1 || local.Streams[0].Out.Landings[0].ID != "701" || local.Streams[0].Out.Landings[0].Share != 100 {
		t.Fatalf("local shares = %+v", local.Streams[0].Out.Landings)
	}
	if ids := res.LocalLandingIDs(); len(ids) != 1 || ids[0] != 701 {
		t.Fatalf("local ids = %v", ids)
	}
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), "<?php") || strings.Contains(string(blob), "index.php") {
		t.Fatal("php leaked into result")
	}

	white, _ := findGroup(res.Groups, "white")
	if len(white.Streams) != 1 || !white.Streams[0].Status || white.Streams[0].Out.Redirect != "show_html_404" || white.Streams[0].Out.Out != "<html><body>white</body></html>" {
		t.Fatalf("white = %+v", white.Streams)
	}
	macros, _ := findGroup(res.Groups, "macros")
	if len(macros.Streams) != 1 || !macros.Streams[0].Status || !strings.Contains(macros.Streams[0].Out.Out, "{bundle_id}") || strings.Contains(macros.Streams[0].Out.Out, "[CID]") {
		t.Fatalf("macros = %+v", macros.Streams)
	}
	nf, _ := findGroup(res.Groups, "netfilters")
	if len(nf.Streams) != 1 || !nf.Streams[0].Status || nf.Streams[0].Rules.IPList.Flag != 2 || nf.Streams[0].Rules.Proxy != 2 {
		t.Fatalf("ip = %+v", nf.Streams)
	}
	ipFile := nf.Streams[0].Rules.IPList.File
	ipBody := res.Lists[ipFile]
	if ipFile != IPListName(ipBody) || !strings.HasPrefix(ipFile, "ip-") || strings.HasPrefix(ipFile, "kt-") || !strings.HasSuffix(ipFile, ".dat") || ipBody != "1.2.3.4\n1.2.3.4-1.2.3.9\n104.154.0.0/15\n99.236.16.124\n" {
		t.Fatalf("list %s %q", ipFile, ipBody)
	}
	org := nf.Streams[0].Rules.Org
	if org.Flag != 2 || strings.Join(org.Values, "|") != "Google LLC|Google Fiber Inc." {
		t.Fatalf("org = %+v", org)
	}
	dupes, _ := findGroup(res.Groups, "dupes")
	if len(dupes.Streams) != 1 || !dupes.Streams[0].Status || len(dupes.Streams[0].Out.Offers) != 1 || dupes.Streams[0].Out.Offers[0].ID != "601" || dupes.Streams[0].Out.Offers[0].Share != 65 {
		t.Fatalf("dupes = %+v", dupes.Streams)
	}
	geo := findOffer(res.Offers, "503")
	if len(geo.Countries) != 2 || geo.Countries[0] != "ru" || geo.Countries[1] != "za" {
		t.Fatalf("geo = %+v", geo)
	}

	offer := findOffer(res.Offers, "101")
	if offer.NetworkID != "2" || offer.Group != "Offers" || offer.PayoutModel != "cpa" || offer.Payout != 12.5 || offer.PayoutCurrency != "USD" || !offer.PayoutParam {
		t.Fatalf("offer = %+v", offer)
	}
	if len(offer.Countries) != 2 || offer.Countries[0] != "es" || offer.Countries[1] != "it" || !strings.Contains(offer.URL, "[CID]") {
		t.Fatalf("offer countries = %+v", offer)
	}
	if len(res.Landings) != 1 || res.Landings[0].ID != "701" || !res.Landings[0].Local || res.Landings[0].URL != "" || res.Landings[0].Entry != "" {
		t.Fatalf("landings = %+v", res.Landings)
	}
	if len(res.Networks) != 2 || res.Networks[0].ID != "2" || res.Networks[0].PostbackURL != "https://net.example/pb" || res.Networks[0].OfferParam != "offer_id" || res.Networks[0].OfferQuery != "" {
		t.Fatalf("networks = %+v", res.Networks)
	}
	if res.Networks[1].ID != "3" || res.Networks[1].OfferParam != "" || res.Networks[1].OfferQuery != "sub1={subid}" {
		t.Fatalf("query network = %+v", res.Networks[1])
	}
	if len(res.Domains) != 1 || res.Domains[0].ID != "3" || res.Domains[0].Host != "track.example" || res.Domains[0].Role != "links" {
		t.Fatalf("domains = %+v", res.Domains)
	}
	if len(res.Sources) != 1 || res.Sources[0].ID != "1" || res.Sources[0].Parameters["keyword"] != "utm_term" || res.Sources[0].Parameters["sub_id_1"] != "sub1" || res.Sources[0].Parameters["cost"] != "" {
		t.Fatalf("sources = %+v", res.Sources)
	}

	text := lossesText(res.Losses)
	for _, want := range []string{"параметр cost", "фильтр weather", "фильтр spider", "нет переносимых фильтров", "Notaland", "фильтр ip отбросил негодные записи: nope, 999.1.1.1, bad-ip", "офферы с одинаковым URL объединены: 601, 602"} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %s\n%s", want, text)
		}
	}
	for _, gone := range []string{"id=701 name=Land type=local", "campaign_type_weight", "share_approximated", "facebook некуда", "track.example некуда", "{bundle_id}", "HTML страницы 404 не перенесён", "also-bad", "фильтр ip не переносится", "фильтр isp не разобран", "offer_param не переносится", "action_type campaign нет соответствия", "фильтр device_model не переносится", "фильтр proxy не переносится", "нет фильтра версии IP", "Polynesie Francaise", "Guyane", "French Southern and Antarctic Lands"} {
		if strings.Contains(text, gone) {
			t.Errorf("report still has %s\n%s", gone, text)
		}
	}
}

func TestMapOmitsDeselectedOffers(t *testing.T) {
	res := Map(loadFixture(t), Selection{Campaigns: true, Landings: true, Networks: true, Sources: true, Domains: true})
	if len(res.Offers) != 0 {
		t.Fatalf("offers emitted: %d", len(res.Offers))
	}
	promo, ok := findGroup(res.Groups, "offer-es")
	if !ok || len(promo.Streams) != 1 || len(promo.Streams[0].Out.Offers) != 1 || promo.Streams[0].Out.Offers[0].ID != "101" {
		t.Fatalf("ref = %+v", promo.Streams)
	}
}

func TestMergeByIDAndPurge(t *testing.T) {
	type item struct {
		ID   string
		Name string
	}
	idOf := func(v item) string { return v.ID }
	imported := func(id string) bool { return id == "1" || id == "2" }
	existing := []item{{ID: "1", Name: "old"}, {ID: "2", Name: "stay"}, {ID: "796", Name: "local"}}
	incoming := []item{{ID: "1", Name: "new"}, {ID: "3", Name: "add"}, {ID: "796", Name: "nope"}}
	got, losses := MergeByIDReport(existing, incoming, idOf, imported)
	if len(got) != 4 || got[0].Name != "new" || got[1].Name != "stay" || got[2].Name != "local" || got[3].Name != "add" {
		t.Fatalf("%+v", got)
	}
	if len(losses) != 1 || losses[0].Kind != "conflict" || losses[0].Ref != "796" || losses[0].Detail != "id занят локальной записью" {
		t.Fatalf("%+v", losses)
	}
	purged := PurgeByID(got, idOf, imported)
	if len(purged) != 2 || purged[0].ID != "796" || purged[1].ID != "3" {
		t.Fatalf("purge %+v", purged)
	}
	groups := PurgeImported([]Group{{ID: "a", Name: "A"}, {ID: "b", Name: "Mine"}}, func(id string) bool { return id == "a" })
	if len(groups) != 1 || groups[0].ID != "b" {
		t.Fatalf("groups %+v", groups)
	}
}

func TestMapIdempotent(t *testing.T) {
	snap := loadFixture(t)
	a := MapAll(snap)
	b := MapAll(snap)
	if !SameGroups(a.Groups, b.Groups) {
		t.Fatal("repeat run changed groups")
	}
	for _, g := range a.Groups {
		if g.Num <= 0 {
			t.Fatalf("map left num 0 on %s", g.ID)
		}
	}
	ab, _ := json.Marshal(a.Manifest)
	bb, _ := json.Marshal(b.Manifest)
	if string(ab) != string(bb) {
		t.Fatal("repeat run changed manifest")
	}
}

func TestMergeKeepsForeignGroup(t *testing.T) {
	incoming := MapAll(loadFixture(t)).Groups
	existing := []Group{{ID: "offer-es", Name: "Handmade", Status: true, Num: 100}, {ID: "mine", Name: "Mine", Status: true, Num: 101}}
	merged, losses := Merge(existing, incoming, func(string) bool { return false })
	promo, _ := findGroup(merged, "offer-es")
	if promo.Name != "Handmade" {
		t.Fatalf("foreign promo overwritten: %+v", promo)
	}
	if _, ok := findGroup(merged, "mine"); !ok {
		t.Fatal("local group dropped")
	}
	if !strings.Contains(lossesText(losses), "id занят локальной записью") {
		t.Fatalf("losses = %+v", losses)
	}
	again, _ := Merge(merged, incoming, func(string) bool { return false })
	promo, _ = findGroup(again, "offer-es")
	if promo.Name != "Handmade" {
		t.Fatal("second merge overwrote foreign group")
	}
}

func TestMergeAssignNums(t *testing.T) {
	existing := []Group{
		{ID: "a", Name: "A", Num: 1},
		{ID: "b", Name: "B", Num: 5},
	}
	incoming := []Group{
		{ID: "c", Name: "C"},
		{ID: "d", Name: "D"},
	}
	imported := func(id string) bool { return id == "c" || id == "d" }
	merged, losses := Merge(existing, incoming, imported)
	if len(losses) != 0 || groupNums(merged) != "1,5,6,7" {
		t.Fatalf("losses=%v nums=%s", losses, groupNums(merged))
	}
	again, losses := Merge(merged, incoming, imported)
	if len(losses) != 0 || groupNums(again) != "1,5,6,7" {
		t.Fatalf("unstable losses=%v nums=%s", losses, groupNums(again))
	}
	purged := PurgeImported(again, imported)
	if groupNums(purged) != "1,5" {
		t.Fatalf("purge renumbered %s", groupNums(purged))
	}

	stored := []Group{
		{ID: "a", Name: "old", Num: 6},
		{ID: "mine", Name: "Mine", Num: 1},
		{ID: "other", Name: "Other", Num: 5},
	}
	inc := []Group{
		{ID: "a", Name: "new"},
		{ID: "b", Name: "fresh"},
	}
	known := func(id string) bool { return id == "a" || id == "b" }
	got := MergeByID(stored, inc, func(g Group) string { return g.ID }, known)
	if len(got) != 4 || got[0].ID != "a" || got[0].Num != 6 || got[0].Name != "new" || got[3].ID != "b" || got[3].Num != 7 {
		t.Fatalf("%+v", got)
	}
	if groupNums(PurgeByID(got, func(g Group) string { return g.ID }, known)) != "1,5" {
		t.Fatalf("purge by id renumbered %+v", got)
	}
}

func TestMergeReplacesImported(t *testing.T) {
	res := MapAll(loadFixture(t))
	incoming := res.Groups
	for _, g := range res.Groups {
		res.Manifest.Add(KindGroups, g.ID)
	}
	imported := res.Manifest.Imported(KindGroups)
	merged, losses := Merge(nil, incoming, imported)
	if len(losses) != 0 {
		t.Fatal(losses)
	}
	again, losses := Merge(merged, incoming, imported)
	if len(losses) != 0 || !SameGroups(merged, again) {
		t.Fatalf("imported groups not stable, losses=%v", losses)
	}
}

func TestValidateHopLimit(t *testing.T) {
	groups := []Group{
		{ID: "a", Name: "kt: a", Redirect: "group", Out: "b"},
		{ID: "b", Name: "kt: b", Redirect: "group", Out: "c"},
		{ID: "c", Name: "kt: c", Redirect: "group", Out: "d"},
		{ID: "d", Name: "kt: d", Redirect: "group", Out: "e"},
		{ID: "e", Name: "kt: e", Redirect: "show_text"},
	}
	if err := Validate(groups, nil); err == nil {
		t.Fatal("deep chain accepted")
	}
}

// findOffer ищет оффер каталога по id.
//
// Параметры: offers — список; id — искомый id.
// Результат: оффер или пустое значение.
// Ошибки: нет.
func findOffer(offers []CatOffer, id string) CatOffer {
	for _, o := range offers {
		if o.ID == id {
			return o
		}
	}
	return CatOffer{}
}

// groupNums склеивает номера групп для проверок.
//
// Параметры: groups — список.
// Результат: номера через запятую, в порядке среза.
// Ошибки: нет.
func groupNums(groups []Group) string {
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = strconv.Itoa(g.Num)
	}
	return strings.Join(parts, ",")
}

// lossesText склеивает отчёт для проверок.
//
// Параметры: losses — строки отчёта.
// Результат: один текст.
// Ошибки: нет.
func lossesText(losses []Loss) string {
	var b strings.Builder
	for _, l := range losses {
		b.WriteString(l.Kind)
		b.WriteByte(' ')
		b.WriteString(l.Ref)
		b.WriteByte(' ')
		b.WriteString(l.Detail)
		b.WriteByte('\n')
	}
	return b.String()
}
