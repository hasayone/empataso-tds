package ktimport

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var catalogIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// mapNetworks строит партнёрские сети.
//
// Параметры: rows — сети Keitaro.
// Результат: каталог и строки отчёта о негодном поле.
// Ошибки: нет.
func mapNetworks(rows []KNetwork) ([]CatNetwork, []Loss) {
	sorted := append([]KNetwork(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return int(sorted[i].ID) < int(sorted[j].ID) })
	var out []CatNetwork
	var losses []Loss
	for _, row := range sorted {
		n := int(row.ID)
		if n <= 0 {
			continue
		}
		ref := entityRef("network", n)
		pb := strings.TrimSpace(row.PostbackURL)
		if pb != "" && !httpish(pb) {
			losses = append(losses, Loss{Kind: "approximated", Ref: ref, Detail: "postback_url не http и очищен"})
			pb = ""
		}
		param, query := splitOfferParam(row.OfferParam)
		out = append(out, CatNetwork{
			ID: numID(n), Name: row.Name, Status: activeState(row.State),
			PostbackURL: pb, OfferParam: param, OfferQuery: query,
		})
	}
	return out, losses
}

// mapSources строит источники и выбрасывает чужие параметры.
//
// Параметры: rows — источники Keitaro.
// Результат: каталог и строки skipped по каждому чужому ключу.
// Ошибки: нет.
func mapSources(rows []KSource) ([]CatSource, []Loss) {
	sorted := append([]KSource(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return int(sorted[i].ID) < int(sorted[j].ID) })
	var out []CatSource
	var losses []Loss
	for _, row := range sorted {
		n := int(row.ID)
		if n <= 0 {
			continue
		}
		params, extra := sourceParams(row)
		losses = append(losses, extra...)
		out = append(out, CatSource{ID: numID(n), Name: row.Name, Parameters: params})
	}
	return out, losses
}

// sourceParams оставляет keyword и sub_id_1..sub_id_15.
//
// Параметры: row — источник.
// Результат: карта параметр → alias и строки отчёта по отброшенным ключам.
// Ошибки: нет.
func sourceParams(row KSource) (map[string]string, []Loss) {
	keys := make([]string, 0, len(row.Parameters))
	for key := range row.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := map[string]string{}
	var losses []Loss
	ref := entityRef("source", int(row.ID))
	for _, key := range keys {
		canon, ok := canonParam(key)
		if !ok {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "параметр " + key + " не переносится"})
			continue
		}
		p := row.Parameters[key]
		val := strings.TrimSpace(p.Alias)
		if val == "" {
			val = strings.TrimSpace(p.Placeholder)
		}
		if val == "" {
			val = strings.TrimSpace(p.Name)
		}
		if val == "" {
			continue
		}
		out[canon] = val
	}
	if len(out) == 0 {
		return nil, losses
	}
	return out, losses
}

// splitOfferParam делит offer_param Keitaro на токен каталога и строку запроса.
//
// Параметры: raw — offer_param.
// Результат: токен, если строка подходит под id каталога, иначе строка запроса без ведущих ? и &.
// Вторая строка пуста, когда занята первая. Обе пусты для пустого входа.
// Ошибки: нет.
func splitOfferParam(raw string) (param, query string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if catalogIDRe.MatchString(raw) {
		return raw, ""
	}
	return "", strings.TrimSpace(strings.TrimLeft(raw, "?&"))
}

// canonParam приводит ключ параметра к имени, которое принимает kuztds.
//
// Параметры: key — ключ из Keitaro.
// Результат: каноническое имя и true для keyword, site, ad_campaign_id,
// creative_id, external_id и sub_id_1..sub_id_15.
// Ошибки: нет.
func canonParam(key string) (string, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "keyword", "site", "ad_campaign_id", "creative_id", "external_id":
		return key, true
	}
	n, ok := strings.CutPrefix(key, "sub_id_")
	if !ok {
		return "", false
	}
	i, err := strconv.Atoi(n)
	if err != nil || i < 1 || i > 15 {
		return "", false
	}
	return fmt.Sprintf("sub_id_%d", i), true
}

// mapDomains строит домены с ролью links.
//
// Параметры: rows — домены Keitaro. Name используется как хост.
// Результат: каталог и строки отчёта о пустом или негодном хосте.
// Ошибки: нет.
func mapDomains(rows []KDomain) ([]CatDomain, []Loss) {
	sorted := append([]KDomain(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return int(sorted[i].ID) < int(sorted[j].ID) })
	var out []CatDomain
	var losses []Loss
	for _, row := range sorted {
		n := int(row.ID)
		if n <= 0 {
			continue
		}
		host := strings.TrimSpace(row.Name)
		if host == "" || strings.Contains(host, "://") || strings.ContainsAny(host, " /\\") {
			losses = append(losses, Loss{Kind: "skipped", Ref: entityRef("domain", n), Detail: "хост не годится"})
			continue
		}
		out = append(out, CatDomain{ID: numID(n), Host: host, Role: "links"})
	}
	return out, losses
}

// mapOffers строит офферы каталога.
//
// Параметры: rows — офферы Keitaro; groups — имена групп.
// Результат: каталог и строки отчёта.
// Ошибки: нет.
func mapOffers(rows []Offer, groups map[int][]KGroup) ([]CatOffer, []Loss) {
	sorted := append([]Offer(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return int(sorted[i].ID) < int(sorted[j].ID) })
	var out []CatOffer
	var losses []Loss
	for _, row := range sorted {
		n := int(row.ID)
		if n <= 0 {
			continue
		}
		ref := entityRef("offer", n)
		local := offerLocal(row)
		rawURL := ""
		if !local {
			rawURL = httpURL(row.ActionType, row.ActionPayload)
		} else if httpish(row.ActionPayload) {
			rawURL = strings.TrimSpace(row.ActionPayload)
		}
		if !local && rawURL == "" {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "у оффера нет http url"})
			continue
		}
		url := rewriteMacros(rawURL)
		countries, bad := countryCodes(row.Country)
		for _, name := range bad {
			losses = append(losses, Loss{Kind: "approximated", Ref: ref, Detail: "часть стран не является кодом: " + name})
		}
		model := payoutModel(row.PayoutType)
		if strings.TrimSpace(row.PayoutType) != "" && model == "" {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "payout_type не cpa и не cpc"})
		}
		cur := currency(row.PayoutCurrency)
		if strings.TrimSpace(row.PayoutCurrency) != "" && cur == "" {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "payout_currency не из трёх букв"})
		}
		payout := float64(row.PayoutValue)
		if payout < 0 {
			losses = append(losses, Loss{Kind: "approximated", Ref: ref, Detail: "отрицательная выплата обнулена"})
			payout = 0
		}
		grp, trimmed := trimFolder(groupName(groups, int(row.GroupID), "offer"))
		if trimmed {
			losses = append(losses, Loss{Kind: "approximated", Ref: ref, Detail: "имя группы обрезано до 64 символов"})
		}
		item := CatOffer{
			ID: numID(n), Name: row.Name, URL: url, Status: activeState(row.State),
			Group: grp, Countries: countries,
			PayoutModel: model, Payout: payout, PayoutCurrency: cur, PayoutParam: bool(row.PayoutAuto), Local: local,
		}
		if int(row.AffiliateNetworkID) > 0 {
			item.NetworkID = numID(int(row.AffiliateNetworkID))
		}
		out = append(out, item)
	}
	return out, losses
}

// mapLandings строит лендинги каталога.
//
// Параметры: rows — лендинги Keitaro; groups — имена групп.
// Результат: каталог и строки отчёта.
// Ошибки: нет.
func mapLandings(rows []Landing, groups map[int][]KGroup) ([]CatLanding, []Loss) {
	sorted := append([]Landing(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return int(sorted[i].ID) < int(sorted[j].ID) })
	var out []CatLanding
	var losses []Loss
	for _, row := range sorted {
		n := int(row.ID)
		if n <= 0 {
			continue
		}
		ref := entityRef("landing", n)
		local := landingLocal(row)
		rawURL := ""
		if !local {
			rawURL = httpURL(row.ActionType, row.ActionPayload)
		}
		if !local && rawURL == "" {
			losses = append(losses, Loss{Kind: "skipped", Ref: ref, Detail: "у лендинга нет http url"})
			continue
		}
		url := rewriteMacros(rawURL)
		grp, trimmed := trimFolder(groupName(groups, int(row.GroupID), "landing"))
		if trimmed {
			losses = append(losses, Loss{Kind: "approximated", Ref: ref, Detail: "имя группы обрезано до 64 символов"})
		}
		out = append(out, CatLanding{
			ID: numID(n), Name: row.Name, URL: url, Status: activeState(row.State), Local: local, Group: grp,
		})
	}
	return out, losses
}

// offerLocal определяет локальный оффер, который движок не запрашивает.
//
// Параметры: row — оффер.
// Результат: true, если тип local или путь задан при пустом URL.
// Ошибки: нет.
func offerLocal(row Offer) bool {
	if strings.Contains(strings.ToLower(row.OfferType), "local") {
		return true
	}
	return strings.TrimSpace(row.LocalPath) != "" && httpURL(row.ActionType, row.ActionPayload) == ""
}

// landingLocal определяет локальный лендинг.
//
// Параметры: row — лендинг.
// Результат: true, если тип или действие содержит local или php.
// Ошибки: нет.
func landingLocal(row Landing) bool {
	blob := strings.ToLower(row.LandingType + " " + row.ActionType)
	return strings.Contains(blob, "local") || strings.Contains(blob, "php")
}

// httpURL возвращает payload, если это http(s) и действие ведёт по URL.
//
// Параметры: action — action_type; payload — адрес.
// Результат: адрес или пустая строка.
// Ошибки: нет.
func httpURL(action, payload string) string {
	payload = strings.TrimSpace(payload)
	if !httpish(payload) || !urlAction(action) {
		return ""
	}
	return payload
}

// httpish проверяет схему http или https.
//
// Параметры: raw — строка адреса.
// Результат: true, если строка начинается с http:// или https://.
// Ошибки: нет.
func httpish(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

// urlAction сообщает, что действие Keitaro — переход по адресу.
//
// Параметры: action — action_type.
// Результат: true для пустого значения, http, redirect, location и похожих.
// Ошибки: нет.
func urlAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "http", "https", "redirect", "http_redirect", "location", "curl", "meta", "double_meta", "js", "iframe", "frame":
		return true
	default:
		a := strings.ToLower(action)
		return strings.Contains(a, "redirect") || strings.Contains(a, "http") || strings.Contains(a, "location")
	}
}

// countryCodes переводит имена и коды стран оффера в ISO2 в нижнем регистре.
//
// Параметры: raw — страны оффера.
// Результат: коды и исходные значения, которые не удалось перевести. Пустые строки пропускаются.
// Ошибки: нет.
func countryCodes(raw []string) ([]string, []string) {
	var out []string
	var bad []string
	seen := map[string]bool{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		code, ok := CountryCode(item)
		if !ok {
			bad = append(bad, item)
			continue
		}
		code = strings.ToLower(code)
		if seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out, bad
}

// isAlpha проверяет, что строка состоит из латинских букв.
//
// Параметры: s — строка.
// Результат: true, если каждый символ — буква.
// Ошибки: нет.
func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// payoutModel оставляет только cpa и cpc.
//
// Параметры: raw — payout_type.
// Результат: модель в нижнем регистре или пустая строка.
// Ошибки: нет.
func payoutModel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "cpa", "cpc":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

// currency оставляет трёхбуквенный код в верхнем регистре.
//
// Параметры: raw — код валюты.
// Результат: код или пустая строка.
// Ошибки: нет.
func currency(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if len(raw) == 3 && isAlpha(raw) {
		return raw
	}
	return ""
}
