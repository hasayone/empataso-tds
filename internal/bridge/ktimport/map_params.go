package ktimport

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// campaignParams читает параметры кампании Keitaro.
//
// Параметры: raw — JSON parameters. Пустое поле, null, [] и {} — пустая карта.
// Результат: карта ключ → поле и текст потери. Текст пуст, когда разбирать нечего или объект разобран.
// Ошибки: нет. Необъект и битый JSON дают одну причину пропуска, кампания переносится без этих имён.
func campaignParams(raw json.RawMessage) (map[string]KParam, string) {
	s := strings.TrimSpace(string(raw))
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	if compact == "" || compact == "null" || compact == "[]" || compact == "{}" {
		return nil, ""
	}
	var out map[string]KParam
	if json.Unmarshal(raw, &out) != nil {
		return nil, "параметры кампании не переносятся"
	}
	return out, ""
}

// incomingName — имя параметра во входящей ссылке клика.
//
// Параметры: p — поле Keitaro.
// Результат: alias, иначе placeholder без скобок, иначе подпись без пробелов и скобок.
// Пустая строка значит, что имени query нет.
// Ошибки: нет.
func incomingName(p KParam) string {
	if v := queryKey(stripBraces(p.Alias)); v != "" {
		return v
	}
	if v := queryKey(stripBraces(p.Placeholder)); v != "" {
		return v
	}
	name := strings.TrimSpace(p.Name)
	if strings.ContainsAny(name, " {}") {
		return ""
	}
	return queryKey(name)
}

// queryKey оставляет ключ query без пробелов и скобок.
//
// Параметры: raw — кандидат.
// Результат: ключ или пустая строка.
// Ошибки: нет.
func queryKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t{}") {
		return ""
	}
	return raw
}

// stripBraces снимает одну пару фигурных скобок по краям.
//
// Параметры: raw — placeholder или alias.
// Результат: внутренность макроса либо исходная строка без краевых пробелов.
// Ошибки: нет.
func stripBraces(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		return strings.TrimSpace(raw[1 : len(raw)-1])
	}
	return raw
}

// campaignMacroMap строит замену placeholder этой кампании.
//
// Параметры: params — разобранные параметры кампании.
// Результат: макрос вида {bundle_id} → {sub_id_N} или [KEY]. nil, если заменять нечего.
// Ошибки: нет. site, ad_campaign_id, creative_id и external_id в карту не входят:
// движок не читает их из слота. Уже известные {sub_id_1}…{sub_id_5} тоже не входят.
func campaignMacroMap(params map[string]KParam) map[string]string {
	if len(params) == 0 {
		return nil
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := map[string]string{}
	for _, key := range keys {
		canon, ok := canonParam(key)
		if !ok {
			continue
		}
		mask, ok := outgoingMask(canon)
		if !ok {
			continue
		}
		token := placeholderToken(params[key].Placeholder)
		if token == "" {
			continue
		}
		if _, known := macroMap[token]; known {
			continue
		}
		if prev, exists := out[token]; exists && prev != mask {
			continue
		}
		out[token] = mask
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// outgoingMask — маска emtds для ключа, который раскрывается не из query.
//
// Параметры: canon — имя после canonParam.
// Результат: [KEY] для keyword, {sub_id_N} для слота и false для остальных ключей.
// Ошибки: нет.
func outgoingMask(canon string) (string, bool) {
	if canon == "keyword" {
		return "[KEY]", true
	}
	if strings.HasPrefix(canon, "sub_id_") {
		return "{" + canon + "}", true
	}
	return "", false
}

// placeholderToken приводит placeholder к виду {имя}.
//
// Параметры: raw — placeholder Keitaro, со скобками или без.
// Результат: макрос или пустая строка, если имени нет.
// Ошибки: нет.
func placeholderToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") && len(raw) >= 3 {
		inner := strings.TrimSpace(raw[1 : len(raw)-1])
		if inner == "" || strings.ContainsAny(inner, "{}") {
			return ""
		}
		return "{" + inner + "}"
	}
	if strings.ContainsAny(raw, " {}") {
		return ""
	}
	return "{" + raw + "}"
}

// applyMacroMap подставляет маски кампании в уже переписанную строку.
//
// Параметры: raw — строка после rewriteMacros; swaps — карта placeholder → маска.
// Результат: строка с заменёнными placeholder. Неизвестное имя остаётся.
// Ошибки: нет.
func applyMacroMap(raw string, swaps map[string]string) string {
	if raw == "" || len(swaps) == 0 {
		return raw
	}
	return macroRE.ReplaceAllStringFunc(raw, func(m string) string {
		if next, ok := swaps[m]; ok {
			return next
		}
		return m
	})
}

// fillSourceFromCampaigns добирает пустые слоты источника из кампаний этого источника.
//
// Параметры: sources — уже собранный каталог; camps — кампании снимка.
// Результат: строки approximated, где две кампании назвали пустой слот по-разному. Первое имя остаётся.
// Ошибки: нет. Имя, заданное самим источником, не затирается и потерей не считается.
func fillSourceFromCampaigns(sources []CatSource, camps []Campaign) []Loss {
	byID := map[string]int{}
	owned := map[string]map[string]bool{}
	for i, src := range sources {
		byID[src.ID] = i
		owned[src.ID] = map[string]bool{}
		for key, val := range src.Parameters {
			if strings.TrimSpace(val) != "" {
				owned[src.ID][key] = true
			}
		}
	}
	sorted := append([]Campaign(nil), camps...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Alias == sorted[j].Alias {
			return int(sorted[i].ID) < int(sorted[j].ID)
		}
		return sorted[i].Alias < sorted[j].Alias
	})
	filled := map[string]map[string]string{}
	var losses []Loss
	for _, camp := range sorted {
		sid := int(camp.TrafficSourceID)
		if sid <= 0 {
			continue
		}
		id := numID(sid)
		idx, ok := byID[id]
		if !ok {
			continue
		}
		params, bad := campaignParams(camp.Parameters)
		if bad != "" || len(params) == 0 {
			continue
		}
		losses = append(losses, fillOneSource(&sources[idx], id, camp, params, owned[id], filled)...)
	}
	return losses
}

// fillOneSource записывает входящие имена одной кампании в пустые слоты источника.
//
// Параметры: src — источник каталога; id — его id; camp — кампания; params — её параметры;
// owned — ключи, которые источник уже задал сам; filled — имена, добранные прежними кампаниями.
// Результат: потери по ключам, где имя не совпало с уже добранным.
// Ошибки: нет.
func fillOneSource(src *CatSource, id string, camp Campaign, params map[string]KParam, owned map[string]bool, filled map[string]map[string]string) []Loss {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var losses []Loss
	for _, key := range keys {
		canon, ok := canonParam(key)
		if !ok || owned[canon] {
			continue
		}
		name := incomingName(params[key])
		if name == "" {
			continue
		}
		if filled[id] == nil {
			filled[id] = map[string]string{}
		}
		if prev, seen := filled[id][canon]; seen {
			if prev != name {
				losses = append(losses, Loss{
					Kind:   "approximated",
					Ref:    entityRef("campaign", int(camp.ID)),
					Detail: fmt.Sprintf("параметр %s: входящее имя %s не совпало с %s", canon, name, prev),
				})
			}
			continue
		}
		if src.Parameters == nil {
			src.Parameters = map[string]string{}
		}
		src.Parameters[canon] = name
		filled[id][canon] = name
	}
	return losses
}

// applyCatalogMacros подставляет маски кампаний в URL офферов и лендингов.
//
// Параметры: res — результат с группами и каталогами; camps — кампании снимка.
// Результат: потери о копиях, которые понадобились из-за разных placeholder.
// Ошибки: нет. Совпадающий адрес остаётся одной записью. Поток без записи в каталоге не трогается.
func applyCatalogMacros(res *Result, camps []Campaign) []Loss {
	if res == nil || len(res.Groups) == 0 {
		return nil
	}
	byNum := map[int]Campaign{}
	for _, camp := range camps {
		byNum[int(camp.ID)] = camp
	}
	var losses []Loss
	losses = append(losses, applyOfferMacros(&res.Offers, res.Groups, byNum)...)
	losses = append(losses, applyLandingMacros(&res.Landings, res.Groups, byNum)...)
	return losses
}

// applyOfferMacros переписывает URL офферов и копирует запись, если кампаниям нужен разный адрес.
//
// Параметры: offers — каталог, его заголовок заменяется при копиях; groups — кампании результата; byNum — id Keitaro → кампания.
// Результат: потери о копиях.
// Ошибки: нет.
func applyOfferMacros(offers *[]CatOffer, groups []Group, byNum map[int]Campaign) []Loss {
	if offers == nil || len(*offers) == 0 {
		return nil
	}
	list := *offers
	base, pos, used := catalogIndex(len(list), func(i int) (string, string) { return list[i].ID, list[i].URL })
	editions := map[string][]macroEdition{}
	var copies []CatOffer
	losses := walkMacroRefs(groups, byNum, base, "оффер", func(st *Stream) *[]ShareRef {
		return &st.Out.Offers
	}, func(baseID, want, mark, campName string) (string, bool) {
		next, created := takeEdition(baseID, want, mark, editions, used)
		if next == baseID {
			list[pos[baseID]].URL = want
			return next, false
		}
		if created {
			src := list[pos[baseID]]
			if src.Countries != nil {
				src.Countries = append([]string(nil), src.Countries...)
			}
			src.ID = next
			src.URL = want
			src.Name = catalogCopyName(src.Name, campName)
			copies = append(copies, src)
		}
		return next, created
	})
	*offers = append(list, copies...)
	return losses
}

// applyLandingMacros переписывает URL лендингов так же, как applyOfferMacros.
//
// Параметры: landings — каталог; groups — кампании результата; byNum — id Keitaro → кампания.
// Результат: потери о копиях.
// Ошибки: нет.
func applyLandingMacros(landings *[]CatLanding, groups []Group, byNum map[int]Campaign) []Loss {
	if landings == nil || len(*landings) == 0 {
		return nil
	}
	list := *landings
	base, pos, used := catalogIndex(len(list), func(i int) (string, string) { return list[i].ID, list[i].URL })
	editions := map[string][]macroEdition{}
	var copies []CatLanding
	losses := walkMacroRefs(groups, byNum, base, "лендинг", func(st *Stream) *[]ShareRef {
		return &st.Out.Landings
	}, func(baseID, want, mark, campName string) (string, bool) {
		next, created := takeEdition(baseID, want, mark, editions, used)
		if next == baseID {
			list[pos[baseID]].URL = want
			return next, false
		}
		if created {
			src := list[pos[baseID]]
			src.ID = next
			src.URL = want
			src.Name = catalogCopyName(src.Name, campName)
			copies = append(copies, src)
		}
		return next, created
	})
	*landings = append(list, copies...)
	return losses
}

// catalogIndex запоминает исходный URL каждой записи каталога.
//
// Параметры: n — число записей; at — id и URL записи i.
// Результат: исходные URL, позиция по id и множество занятых id.
// Ошибки: нет.
func catalogIndex(n int, at func(i int) (string, string)) (base map[string]string, pos map[string]int, used map[string]bool) {
	base = map[string]string{}
	pos = map[string]int{}
	used = map[string]bool{}
	for i := 0; i < n; i++ {
		id, url := at(i)
		base[id] = url
		pos[id] = i
		used[id] = true
	}
	return base, pos, used
}

// macroEdition — один уже выданный адрес каталога для набора масок.
type macroEdition struct {
	id  string
	url string
}

// walkMacroRefs обходит ссылки потоков и подменяет id, когда адрес другой.
//
// Параметры: groups — кампании результата; byNum — id Keitaro → кампания; base — исходный URL каталога;
// kind — слово для отчёта; refsOf — доли потока; on — выдаёт id записи с нужным адресом и true, если запись создана.
// Результат: потери о созданных копиях.
// Ошибки: нет. Ссылка на id вне каталога остаётся как есть.
func walkMacroRefs(groups []Group, byNum map[int]Campaign, base map[string]string, kind string, refsOf func(*Stream) *[]ShareRef, on func(baseID, want, mark, campName string) (string, bool)) []Loss {
	var losses []Loss
	for gi := range groups {
		g := &groups[gi]
		camp, ok := byNum[g.Num]
		if !ok || g.Num == 0 {
			continue
		}
		params, bad := campaignParams(camp.Parameters)
		if bad != "" {
			continue
		}
		swaps := campaignMacroMap(params)
		campName := strings.TrimSpace(g.Name)
		if campName == "" {
			campName = g.ID
		}
		for si := range g.Streams {
			refs := refsOf(&g.Streams[si])
			if refs == nil {
				continue
			}
			for ri := range *refs {
				id := (*refs)[ri].ID
				raw, known := base[id]
				if !known {
					continue
				}
				next, created := on(id, applyMacroMap(raw, swaps), g.ID, campName)
				(*refs)[ri].ID = next
				if created {
					losses = append(losses, Loss{
						Kind:   "approximated",
						Ref:    g.ID,
						Detail: kind + " " + id + " скопирован: имена параметров кампаний не совпали",
					})
				}
			}
		}
	}
	return losses
}

// takeEdition находит запись каталога с нужным адресом или заводит новую.
//
// Параметры: baseID — исходный id; want — адрес этой кампании; mark — метка копии;
// editions — уже выданные адреса этого id; used — занятые id каталога.
// Результат: id записи и true, если это новая копия. Первая кампания остаётся на baseID.
// Ошибки: нет.
func takeEdition(baseID, want, mark string, editions map[string][]macroEdition, used map[string]bool) (string, bool) {
	for _, ed := range editions[baseID] {
		if ed.url == want {
			return ed.id, false
		}
	}
	if len(editions[baseID]) == 0 {
		editions[baseID] = []macroEdition{{id: baseID, url: want}}
		return baseID, false
	}
	id := uniqueCatalogID(baseID, mark, used)
	editions[baseID] = append(editions[baseID], macroEdition{id: id, url: want})
	return id, true
}

// uniqueCatalogID собирает свободный id копии в пределах id каталога.
//
// Параметры: base — исходный id; mark — alias кампании; used — уже занятые id, пополняется.
// Результат: id из букв, цифр, точки, _ и -, не длиннее 64 символов.
// Ошибки: нет.
func uniqueCatalogID(base, mark string, used map[string]bool) string {
	mark = catalogMark(mark)
	for n := 0; n < 10000; n++ {
		id := fitCatalogID(base, mark, n)
		if id != "" && !used[id] {
			used[id] = true
			return id
		}
	}
	id := fmt.Sprintf("c-%d", len(used)+1)
	used[id] = true
	return id
}

// fitCatalogID склеивает id и обрезает его до 64 символов.
//
// Параметры: base — исходный id; mark — метка; n — 0 для первой попытки, дальше суффикс.
// Результат: годный id или пустая строка.
// Ошибки: нет.
func fitCatalogID(base, mark string, n int) string {
	suffix := mark
	if n > 0 {
		suffix = fmt.Sprintf("%s-%d", mark, n+1)
	}
	id := base + "-" + suffix
	if len(id) > 64 {
		room := 64 - len(base) - 1
		if room < 1 {
			id = fmt.Sprintf("c%d%d", n, len(usedTail(mark)))
		} else if len(suffix) > room {
			id = base + "-" + suffix[:room]
		}
	}
	if !catalogIDRe.MatchString(id) {
		return ""
	}
	return id
}

// usedTail берёт хвост метки, чтобы запасной id не был пустым.
//
// Параметры: mark — метка.
// Результат: последние четыре символа или вся метка.
// Ошибки: нет.
func usedTail(mark string) string {
	if len(mark) <= 4 {
		return mark
	}
	return mark[len(mark)-4:]
}

// catalogMark оставляет в метке только символы id каталога.
//
// Параметры: id — alias кампании.
// Результат: метка или c, если пригодных символов не было.
// Ошибки: нет.
func catalogMark(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "c"
	}
	return b.String()
}

// catalogCopyName помечает имя копии кампанией.
//
// Параметры: name — имя исходной записи; mark — имя кампании.
// Результат: имя с пометкой. Пустая пометка оставляет исходное имя.
// Ошибки: нет.
func catalogCopyName(name, mark string) string {
	mark = strings.TrimSpace(mark)
	if mark == "" {
		return name
	}
	return name + " · " + mark
}
