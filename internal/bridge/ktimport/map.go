package ktimport

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var macroRE = regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)
var macroMap = map[string]string{
	"{subid}":    "[CID]",
	"{country}":  "[COUNTRY]",
	"{keyword}":  "[KEY]",
	"{sub_id_1}": "[PAR-1]",
	"{sub_id_2}": "[PAR-2]",
	"{sub_id_3}": "[PAR-3]",
	"{sub_id_4}": "[PAR-4]",
	"{sub_id_5}": "[PAR-5]",
}

// Map строит каталоги, группы emtds и отчёт потерь.
//
// Параметры: src — снимок конфигурации; sel — какие каталоги и кампании писать в результат.
// Результат: выбранные сущности в стабильном порядке и строки отчёта.
// Ошибки: нет. Непереносимое выключается или попадает в отчёт. Ссылка на id остаётся, даже если каталог не выбран.
func Map(src Snapshot, sel Selection) Result {
	offers := map[int]Offer{}
	for _, o := range src.Offers {
		offers[int(o.ID)] = o
	}
	landings := map[int]Landing{}
	for _, l := range src.Landings {
		landings[int(l.ID)] = l
	}
	groups := indexGroups(src.Groups)
	var res Result
	var losses []Loss
	lists := listBag{}
	pages := pageBag{}
	if sel.Networks {
		nets, extra := mapNetworks(src.AffiliateNetworks)
		res.Networks = nets
		losses = append(losses, extra...)
	}
	if sel.Sources {
		sources, extra := mapSources(src.TrafficSources)
		res.Sources = sources
		losses = append(losses, extra...)
	}
	if sel.Domains {
		doms, extra := mapDomains(src.Domains, groups)
		res.Domains = doms
		losses = append(losses, extra...)
	}
	if sel.Offers {
		items, extra := mapOffers(src.Offers, groups)
		res.Offers = items
		losses = append(losses, extra...)
	}
	if sel.Landings {
		items, extra := mapLandings(src.Landings, groups)
		res.Landings = items
		losses = append(losses, extra...)
	}
	if sel.Campaigns {
		ctx := &mapCtx{offers: offers, landings: landings, groups: groups, lists: &lists, pages: &pages, only: sel.Campaign}
		res.Groups, losses = ctx.mapCampaigns(src, losses)
		losses = append(losses, applyCatalogMacros(&res, src.Campaigns)...)
		if sel.Sources {
			losses = append(losses, fillSourceFromCampaigns(res.Sources, src.Campaigns)...)
		}
	}
	if sel.Users {
		losses = append(losses, mapUsers(src, sel, &res)...)
	}
	if len(lists) > 0 {
		res.Lists = map[string]string(lists)
	}
	if len(pages) > 0 {
		res.Pages = map[string]string(pages)
	}
	return fillResult(res, losses)
}

// MapAll включает все каталоги и кампании.
//
// Параметры: src — снимок конфигурации.
// Результат: полный результат Map.
// Ошибки: нет.
func MapAll(src Snapshot) Result {
	return Map(src, Selection{
		Domains: true, Networks: true, Offers: true,
		Landings: true, Sources: true, Campaigns: true,
	})
}

// fillResult заменяет nil-срезы пустыми, чтобы JSON был массивом.
//
// Параметры: res — черновик; losses — строки отчёта.
// Результат: результат с пустыми срезами вместо nil.
// Ошибки: нет.
func fillResult(res Result, losses []Loss) Result {
	res.Losses = losses
	if res.Groups == nil {
		res.Groups = []Group{}
	}
	if res.Offers == nil {
		res.Offers = []CatOffer{}
	}
	if res.Landings == nil {
		res.Landings = []CatLanding{}
	}
	if res.Networks == nil {
		res.Networks = []CatNetwork{}
	}
	if res.Sources == nil {
		res.Sources = []CatSource{}
	}
	if res.Domains == nil {
		res.Domains = []CatDomain{}
	}
	if res.Users == nil {
		res.Users = []CatUser{}
	}
	if res.Losses == nil {
		res.Losses = []Loss{}
	}
	return attachManifest(res)
}

// numID переводит числовой id Keitaro в id каталога emtds.
//
// Параметры: n — id Keitaro.
// Результат: десятичная строка без префикса.
// Ошибки: нет.
func numID(n int) string {
	return strconv.Itoa(n)
}

// entityRef собирает ссылку строки отчёта.
//
// Параметры: kind — короткое имя сущности; n — id Keitaro.
// Результат: строка вида landing 701.
// Ошибки: нет.
func entityRef(kind string, n int) string {
	return fmt.Sprintf("%s %d", kind, n)
}

// indexGroups группирует имена Keitaro по числовому id.
//
// Параметры: rows — группы снимка.
// Результат: id → список групп этого id.
// Ошибки: нет.
func indexGroups(rows []KGroup) map[int][]KGroup {
	out := map[int][]KGroup{}
	for _, row := range rows {
		out[int(row.ID)] = append(out[int(row.ID)], row)
	}
	return out
}

// groupName возвращает имя группы Keitaro нужного вида.
//
// Параметры: idx — индекс; id — group_id; kind — campaign, offer, landing или domain.
// Результат: имя или пустая строка, если группы нет.
// Ошибки: нет.
func groupName(idx map[int][]KGroup, id int, kind string) string {
	if id <= 0 {
		return ""
	}
	rows := idx[id]
	for _, row := range rows {
		if sameKind(row.Type, kind) {
			return row.Name
		}
	}
	if len(rows) == 1 {
		return rows[0].Name
	}
	return ""
}

// sameKind сравнивает тип группы без конечной s.
//
// Параметры: got — тип из Keitaro; want — ожидаемый вид.
// Результат: true, если основа слов совпала.
// Ошибки: нет.
func sameKind(got, want string) bool {
	g := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(got)), "s")
	w := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(want)), "s")
	return g != "" && g == w
}

// activeState переводит state Keitaro в статус emtds.
//
// Параметры: state — поле state.
// Результат: true для пустого, active и enabled.
// Ошибки: нет.
func activeState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "active", "enabled":
		return true
	default:
		return false
	}
}
