package ktimport

import (
	"bytes"
	"encoding/json"
	"strings"
)

// userSectionOrder — фиксированный порядок разделов панели в CatUser.Sections.
var userSectionOrder = []string{
	"dashboard", "flows", "offers", "landings", "networks", "sources", "domains",
	"logs", "conversions", "report", "keywords", "lists", "tools", "groups",
}

// ktResourceSections сопоставляет имя resources Keitaro разделам kuztds.
var ktResourceSections = map[string][]string{
	"dashboard":          {"dashboard"},
	"trends":             {"dashboard"},
	"campaigns":          {"flows", "lists", "tools"},
	"offers":             {"offers"},
	"landings":           {"landings"},
	"affiliate_networks": {"networks"},
	"traffic_sources":    {"sources"},
	"domains":            {"domains"},
	"logs":               {"logs"},
	"clicks":             {"logs"},
	"conversions":        {"conversions"},
	"reports":            {"report", "keywords"},
	"groups":             {"groups"},
}

// ktSkippedResources — resources Keitaro, которые не переносятся в разделы.
var ktSkippedResources = map[string]bool{
	"archive": true, "geo_profiles": true, "geo_dbs": true, "cleaner": true,
	"integrations": true, "api_keys": true,
}

// ktGrantPairs — шесть грантов Keitaro в стабильном порядке обхода.
var ktGrantPairs = []struct{ kt, key string }{
	{"offers", "offers"},
	{"campaigns", "campaigns"},
	{"landings", "landings"},
	{"traffic_sources", "sources"},
	{"affiliate_networks", "networks"},
	{"domains", "domains"},
}

// ktGroupKind переводит имя ресурса Keitaro в kind для groupName.
var ktGroupKind = map[string]string{
	"campaigns": "campaign",
	"offers":    "offer",
	"landings":  "landing",
}

// profitPrefixes — префиксы метрик Keitaro, закрываемых как profit.
var profitPrefixes = []string{"revenue", "profit", "payout", "roi", "epc", "uepc", "epv"}

// costPrefixes — префиксы метрик Keitaro, закрываемых как cost.
var costPrefixes = []string{"cost", "cpc", "cpa", "cpm"}

// catalogIDIndex — id каталогов текущего прогона по Kind*.
type catalogIDIndex map[string]map[string]bool

// userMapCtx — контекст маппинга одного прогона пользователей.
type userMapCtx struct {
	gidx    map[int][]KGroup
	aliases map[int]string
	ids     catalogIDIndex
	sel     Selection
}

// mapUsers переносит пользователей Keitaro в CatUser и дописывает res.Users.
//
// Параметры: src — снимок; sel — флаги и Known; res — результат с уже заполненными каталогами.
// Результат: строки отчёта о пропусках и потерях прав.
// Ошибки: нет.
func mapUsers(src Snapshot, sel Selection, res *Result) []Loss {
	if src.UsersDenied {
		return []Loss{{Kind: "skipped", Ref: "users", Detail: "нет доступа к /users"}}
	}
	ctx := userMapCtx{
		gidx:    indexGroups(src.Groups),
		aliases: campaignAliases(src.Campaigns),
		ids:     newCatalogIDIndex(*res),
		sel:     sel,
	}
	var losses []Loss
	var users []CatUser
	for _, ku := range src.Users {
		cu, extra := ctx.mapUser(ku)
		losses = append(losses, extra...)
		if cu.Login != "" {
			users = append(users, cu)
		}
	}
	res.Users = users
	return losses
}

// newCatalogIDIndex собирает id каталогов и кампаний из результата маппера.
//
// Параметры: res — каталоги и группы текущего прогона.
// Результат: индекс kind → id → true.
// Ошибки: нет.
func newCatalogIDIndex(res Result) catalogIDIndex {
	out := catalogIDIndex{
		KindGroups:   {},
		KindOffers:   {},
		KindLandings: {},
		KindSources:  {},
		KindNetworks: {},
		KindDomains:  {},
	}
	for _, g := range res.Groups {
		out[KindGroups][g.ID] = true
	}
	for _, o := range res.Offers {
		out[KindOffers][o.ID] = true
	}
	for _, l := range res.Landings {
		out[KindLandings][l.ID] = true
	}
	for _, s := range res.Sources {
		out[KindSources][s.ID] = true
	}
	for _, n := range res.Networks {
		out[KindNetworks][n.ID] = true
	}
	for _, d := range res.Domains {
		out[KindDomains][d.ID] = true
	}
	return out
}

// known сообщает, что id есть в текущем прогоне или в prev-манифесте через sel.Known.
//
// Параметры: kind — Kind*; id — идентификатор каталога или alias кампании.
// Результат: true, если id переносим.
// Ошибки: нет.
func (ctx *userMapCtx) known(kind, id string) bool {
	if ctx.ids[kind][id] {
		return true
	}
	if ctx.sel.Known != nil {
		return ctx.sel.Known(kind, id)
	}
	return false
}

// mapUser переводит одного KUser в CatUser и собирает потери прав.
//
// Параметры: ku — пользователь Keitaro.
// Результат: CatUser и строки отчёта. Пустой login даёт пустой CatUser.
// Ошибки: нет.
func (ctx *userMapCtx) mapUser(ku KUser) (CatUser, []Loss) {
	id := int(ku.ID)
	ref := entityRef("user", id)
	login := strings.TrimSpace(ku.Login)
	if login == "" {
		login = strings.TrimSpace(ku.Name)
	}
	if login == "" {
		return CatUser{}, []Loss{{Kind: "skipped", Ref: ref, Detail: "пустой login"}}
	}
	if strings.ToUpper(strings.TrimSpace(ku.Type)) == "ADMIN" {
		return CatUser{
			ID:    numID(id),
			Login: login,
			Type:  "ADMIN",
			Lang:  userLang(ku.Preferences.Language),
			TZ:    strings.TrimSpace(ku.Preferences.Timezone),
		}, nil
	}
	acc := parseAccessObject(ku.AccessData)
	var losses []Loss
	sections, secLoss := mapUserSections(acc, ref)
	losses = append(losses, secLoss...)
	grants, grantLoss := ctx.grants(acc, ref)
	losses = append(losses, grantLoss...)
	hidden, hidLoss := mapUserHidden(acc, ref)
	losses = append(losses, hidLoss...)
	losses = append(losses, mapStreamsAccess(acc, ref)...)
	return CatUser{
		ID:       numID(id),
		Login:    login,
		Type:     "USER",
		Sections: sections,
		Grants:   grants,
		Hidden:   hidden,
		Lang:     userLang(ku.Preferences.Language),
		TZ:       strings.TrimSpace(ku.Preferences.Timezone),
	}, losses
}

// grants мапит шесть access_type Keitaro в CatGrant.
//
// Параметры: acc — разобранный access_data; ref — ссылка пользователя для отчёта.
// Результат: grants с ключами offers, campaigns, landings, sources, networks, domains.
// Ошибки: нет. Неизвестный access_type даёт read и строку отчёта.
func (ctx *userMapCtx) grants(acc map[string]json.RawMessage, ref string) (map[string]CatGrant, []Loss) {
	out := make(map[string]CatGrant, len(ktGrantPairs))
	var losses []Loss
	for _, pair := range ktGrantPairs {
		modeRaw := accessTypeField(acc, pair.kt)
		mode, modeLoss := mapAccessMode(modeRaw, ref, pair.kt)
		losses = append(losses, modeLoss...)
		g := CatGrant{Mode: mode}
		if mode != "full" && mode != "read" {
			ent := parseFlexIntList(acc[pair.kt+"_selected_entities"])
			labels := parseFlexIntList(acc[pair.kt+"_selected_groups"])
			idLoss, idList := ctx.grantIDs(pair.kt, pair.key, ent, ref)
			losses = append(losses, idLoss...)
			lblLoss, lblList := ctx.grantLabels(pair.kt, labels, ref)
			losses = append(losses, lblLoss...)
			g.IDs = idList
			g.Labels = lblList
		}
		out[pair.key] = g
	}
	return out, losses
}

// grantIDs проверяет selected_entities и переводит id в формат kuztds.
//
// Параметры: ktRes — имя ресурса Keitaro; key — Kind* для проверки; entities — id Keitaro; ref — отчёт.
// Результат: потери и список id или alias кампании.
// Ошибки: нет.
func (ctx *userMapCtx) grantIDs(ktRes, key string, entities []int, ref string) ([]Loss, []string) {
	if len(entities) == 0 {
		return nil, nil
	}
	var losses []Loss
	var out []string
	for _, n := range entities {
		idStr := numID(n)
		if ktRes == "campaigns" {
			alias, ok := ctx.aliases[n]
			if !ok || alias == "" || !ctx.known(KindGroups, alias) {
				losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "campaigns " + idStr + " не перенесён"})
				continue
			}
			out = append(out, alias)
			continue
		}
		if !ctx.known(key, idStr) {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: ktRes + " " + idStr + " не перенесён"})
			continue
		}
		out = append(out, idStr)
	}
	return losses, out
}

// grantLabels переводит selected_groups в labels для campaigns, offers и landings.
//
// Параметры: ktRes — ресурс Keitaro; groups — group_id; ref — отчёт.
// Результат: потери и имена групп.
// Ошибки: нет.
func (ctx *userMapCtx) grantLabels(ktRes string, groups []int, ref string) ([]Loss, []string) {
	if len(groups) == 0 {
		return nil, nil
	}
	kind, ok := ktGroupKind[ktRes]
	if !ok {
		return []Loss{{Kind: "skipped", Ref: ref, Detail: ktRes + " groups не переносятся"}}, nil
	}
	var losses []Loss
	var out []string
	for _, gid := range groups {
		name := groupName(ctx.gidx, gid, kind)
		if name == "" {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: ktRes + " group " + numID(gid) + " без имени"})
			continue
		}
		out = append(out, name)
	}
	return losses, out
}

// userLang нормализует язык Keitaro для prefs.lang.
//
// Параметры: lang — preferences.language.
// Результат: ru, en или пустая строка.
// Ошибки: нет.
func userLang(lang string) string {
	low := strings.ToLower(strings.TrimSpace(lang))
	switch low {
	case "ru", "en":
		return low
	default:
		return ""
	}
}

// parseAccessObject разбирает access_data в map полей.
//
// Параметры: raw — JSON access_data.
// Результат: объект или пустая map для null, массива и не-объекта.
// Ошибки: нет.
func parseAccessObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}
	}
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || bytes.Equal(trim, []byte("null")) {
		return map[string]json.RawMessage{}
	}
	if trim[0] == '[' || trim[0] != '{' {
		return map[string]json.RawMessage{}
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return map[string]json.RawMessage{}
	}
	return m
}

// mapUserSections переводит resources[] в sections без дублей.
//
// Параметры: acc — access_data; ref — ссылка пользователя.
// Результат: sections в userSectionOrder и потери по непереносимым resources.
// Ошибки: нет.
func mapUserSections(acc map[string]json.RawMessage, ref string) ([]string, []Loss) {
	resources := parseStringList(acc["resources"])
	seen := map[string]bool{}
	var losses []Loss
	for _, name := range resources {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if ktSkippedResources[key] {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "раздел " + key + " не переносится"})
			continue
		}
		if key == "streams" {
			continue
		}
		secs, ok := ktResourceSections[key]
		if !ok {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "раздел " + key + " не переносится"})
			continue
		}
		for _, s := range secs {
			seen[s] = true
		}
	}
	out := make([]string, 0, len(seen))
	for _, s := range userSectionOrder {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, losses
}

// accessTypeField читает строку <res>_access_type из access_data.
//
// Параметры: acc — access_data; res — имя ресурса Keitaro.
// Результат: значение или пустая строка.
// Ошибки: нет.
func accessTypeField(acc map[string]json.RawMessage, res string) string {
	raw, ok := acc[res+"_access_type"]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// mapAccessMode переводит access_type Keitaro в mode kuztds.
//
// Параметры: raw — access_type; ref и res — для строки отчёта при неизвестном значении.
// Результат: mode и опциональная потеря.
// Ошибки: нет.
func mapAccessMode(raw, ref, res string) (string, []Loss) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "full", nil
	}
	switch raw {
	case "full_access":
		return "full", nil
	case "read_only":
		return "read", nil
	case "to_groups_and_selected":
		return "selected", nil
	case "created_by_user_groups_and_selected":
		return "created", nil
	default:
		return "read", []Loss{{Kind: "skipped", Ref: ref, Detail: res + " access_type " + raw + " неизвестен, выдан read"}}
	}
}

// mapStreamsAccess фиксирует потери по streams в access_data.
//
// Параметры: acc — access_data; ref — ссылка пользователя.
// Результат: строки отчёта.
// Ошибки: нет.
func mapStreamsAccess(acc map[string]json.RawMessage, ref string) []Loss {
	var losses []Loss
	streamType := accessTypeField(acc, "streams")
	campType := accessTypeField(acc, "campaigns")
	if streamType != "" && campType != "" && streamType != campType {
		losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "streams access отличается от campaigns"})
	}
	if len(parseFlexIntList(acc["streams_selected_entities"])) > 0 {
		losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "streams selected_entities не переносятся"})
	}
	return losses
}

// mapUserHidden переводит reports[] в hidden profit/cost.
//
// Параметры: acc — access_data; ref — ссылка пользователя.
// Результат: hidden без дублей и потери по неизвестным метрикам.
// Ошибки: нет.
func mapUserHidden(acc map[string]json.RawMessage, ref string) ([]string, []Loss) {
	reports := parseStringList(acc["reports"])
	seen := map[string]bool{}
	var losses []Loss
	var out []string
	for _, name := range reports {
		m := mapReportMetric(name)
		if m == "" {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "метрика " + name + " не переносится"})
			continue
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, losses
}

// mapReportMetric сопоставляет имя метрики Keitaro hidden id kuztds.
//
// Параметры: name — элемент reports[].
// Результат: profit, cost или пустая строка.
// Ошибки: нет.
func mapReportMetric(name string) string {
	low := strings.ToLower(strings.TrimSpace(name))
	if low == "" {
		return ""
	}
	if hasAnyPrefix(low, profitPrefixes) {
		return "profit"
	}
	if hasAnyPrefix(low, costPrefixes) {
		return "cost"
	}
	return ""
}

// hasAnyPrefix сообщает, что s начинается с одного из prefixes.
//
// Параметры: s — строка; prefixes — префиксы без учёта регистра (s уже в lower).
// Результат: true при совпадении.
// Ошибки: нет.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// parseStringList разбирает JSON-массив строк.
//
// Параметры: raw — JSON поле.
// Результат: срез или nil.
// Ошибки: нет.
func parseStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	return nil
}

// parseFlexIntList разбирает JSON-массив id, допускающий числа-строки.
//
// Параметры: raw — JSON поле.
// Результат: int id или nil.
// Ошибки: нет.
func parseFlexIntList(raw json.RawMessage) []int {
	if len(raw) == 0 {
		return nil
	}
	var flex []FlexInt
	if json.Unmarshal(raw, &flex) != nil {
		return nil
	}
	out := make([]int, 0, len(flex))
	for _, n := range flex {
		out = append(out, int(n))
	}
	return out
}
