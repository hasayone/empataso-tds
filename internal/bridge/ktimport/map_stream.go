package ktimport

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// mapCampaigns превращает кампании в группы.
//
// Параметры: src — снимок; losses — уже собранный отчёт. Справочники лежат в получателе.
// При only > 0 группа строится только для этой кампании, остальные лишь занимают свои id,
// чтобы id групп совпали с полным переносом.
// Результат: группы и отчёт вместе с потерями кампаний.
// Ошибки: нет.
func (c *mapCtx) mapCampaigns(src Snapshot, losses []Loss) ([]Group, []Loss) {
	c.aliases = campaignAliases(src.Campaigns)
	c.sources = map[int]KSource{}
	for _, row := range src.TrafficSources {
		if int(row.ID) > 0 {
			c.sources[int(row.ID)] = row
		}
	}
	c.seen = map[string]bool{}
	camps := append([]Campaign(nil), src.Campaigns...)
	sort.Slice(camps, func(i, j int) bool {
		if camps[i].Alias == camps[j].Alias {
			return int(camps[i].ID) < int(camps[j].ID)
		}
		return camps[i].Alias < camps[j].Alias
	})
	var out []Group
	var mapped []Campaign
	for _, camp := range camps {
		if c.only > 0 && int(camp.ID) != c.only {
			id, _, _ := assignedID(camp.Alias, int(camp.ID), c.seen)
			c.seen[id] = true
			continue
		}
		g, extra := c.mapCampaign(camp, src.Streams[strconv.Itoa(int(camp.ID))])
		out = append(out, g)
		losses = append(losses, extra...)
		c.seen[g.ID] = true
		mapped = append(mapped, camp)
	}
	losses = append(losses, campaignSettingLosses(mapped)...)
	return out, losses
}

// campaignSettingLosses собирает строки о настройках кампании, которые маппер не переносит.
//
// Параметры: camps — кампании одного прогона уже после сортировки.
// Результат: строки kind approximated и ref campaigns. Пустой срез, если отличий нет.
// Ошибки: нет. Дефолт cookies_ttl 0 или 24, обычный срок в часах, bind_visitors s или пусто, нулевая стоимость и cost_auto false строки не дают.
func campaignSettingLosses(camps []Campaign) []Loss {
	var ttl, bind, cost, uniq int
	for _, camp := range camps {
		if _, odd := campaignUniqSeconds(camp.CookiesTTL); odd {
			ttl++
		}
		switch strings.ToLower(strings.TrimSpace(camp.BindVisitors)) {
		case "sl", "slo":
			bind++
		}
		if float64(camp.CostValue) != 0 || bool(camp.CostAuto) {
			cost++
		}
		if campaignUniquenessSet(camp) {
			uniq++
		}
	}
	var out []Loss
	if ttl > 0 {
		out = append(out, Loss{Kind: "approximated", Ref: "campaigns", Detail: fmt.Sprintf("срок уникальности кампании не переносится: %d", ttl)})
	}
	if bind > 0 {
		out = append(out, Loss{Kind: "approximated", Ref: "campaigns", Detail: fmt.Sprintf("привязка посетителя не переносится: %d", bind)})
	}
	if cost > 0 {
		out = append(out, Loss{Kind: "approximated", Ref: "campaigns", Detail: fmt.Sprintf("стоимость кампании не переносится: %d", cost)})
	}
	if uniq > 0 {
		out = append(out, Loss{Kind: "approximated", Ref: "campaigns", Detail: fmt.Sprintf("уникальность кампании по параметру не переносится: %d", uniq)})
	}
	return out
}

// campaignUniqSeconds переводит срок уникальности Keitaro из часов в секунды.
//
// Параметры: ttl — cookies_ttl кампании.
// Результат: секунды и false для срока, который пишется в группу. 0 и 24 дают 0 и false: окно движка уже 24 часа.
// Отрицательный срок и значение, которое не помещается в int после умножения на 3600, дают 0 и true.
// Ошибки: нет.
func campaignUniqSeconds(ttl FlexInt) (int, bool) {
	n := int64(ttl)
	if n == 0 || n == 24 {
		return 0, false
	}
	if n < 0 || n > int64(^uint(0)>>1)/3600 {
		return 0, true
	}
	return int(n * 3600), false
}

// campaignUniquenessSet сообщает, что у кампании задан ключ уникальности вне схемы.
//
// Параметры: camp — кампания после разбора снимка.
// Результат: true, если uniqueness, uniqueness_param или uniqueness_method непусты.
// Ошибки: нет.
func campaignUniquenessSet(camp Campaign) bool {
	return rawMarkerSet(camp.Uniqueness) || rawMarkerSet(camp.UniquenessParam) || rawMarkerSet(camp.UniquenessMethod)
}

// rawMarkerSet сообщает, что сырое JSON-значение ключа уникальности задано.
//
// Параметры: raw — значение uniqueness, uniqueness_param или uniqueness_method.
// Результат: false для пустого, null, "", 0, false, [] и {}.
// Ошибки: нет. Непонятный фрагмент считается заданным.
func rawMarkerSet(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", `""`, "0", "false", "[]", "{}":
		return false
	}
	switch s[0] {
	case '"':
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return true
		}
		return strings.TrimSpace(text) != ""
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n float64
		if json.Unmarshal(raw, &n) != nil {
			return true
		}
		return n != 0
	case '[':
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return true
		}
		return len(arr) != 0
	case '{':
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return true
		}
		return len(obj) != 0
	default:
		return true
	}
}

// mapCampaign превращает одну кампанию в группу.
//
// Параметры: camp — кампания; streams — её потоки. Справочники и занятые id лежат в получателе.
// Результат: группа и строки отчёта этой кампании.
// Ошибки: нет.
func (c *mapCtx) mapCampaign(camp Campaign, streams []KStream) (Group, []Loss) {
	var losses []Loss
	c.losses = &losses
	id, emptyAlias, dupAlias := assignedID(camp.Alias, int(camp.ID), c.seen)
	status := activeState(camp.State)
	if emptyAlias {
		status = false
		losses = append(losses, Loss{Kind: "disabled", Ref: id, Detail: "у кампании нет alias"})
	}
	if dupAlias {
		status = false
		losses = append(losses, Loss{Kind: "disabled", Ref: id, Detail: "alias уже занят"})
	}
	posts, postLoss := parsePostbacks(camp.Postbacks)
	if postLoss != "" {
		kind := "skipped"
		if strings.Contains(postLoss, "часть") || strings.Contains(postLoss, "заменён") {
			kind = "approximated"
		}
		losses = append(losses, Loss{Kind: kind, Ref: id, Detail: postLoss})
	}
	folder, trimmed := trimFolder(groupName(c.groups, int(camp.GroupID), "campaign"))
	if trimmed {
		losses = append(losses, Loss{Kind: "approximated", Ref: id, Detail: "имя группы обрезано до 64 символов"})
	}
	params, bad := campaignParams(camp.Parameters)
	if bad != "" {
		losses = append(losses, Loss{Kind: "skipped", Ref: id, Detail: bad})
		params = nil
	}
	c.swaps = campaignMacroMap(params)
	var src KSource
	if c.sources != nil {
		src = c.sources[int(camp.TrafficSourceID)]
	}
	fields := fieldsFromParams(src.Parameters, params)
	for _, key := range fields.Skipped {
		losses = append(losses, Loss{Kind: "skipped", Ref: id, Detail: "параметр " + key + " длиннее 255 байт"})
	}
	g := Group{
		ID: id, Name: camp.Name, Num: int(camp.ID), Status: status, Redirect: "show_text", Out: "",
		Folder: folder, Weight: strings.EqualFold(camp.Type, "weight"), Postbacks: posts,
		Notes:   trimNotes(camp.Notes),
		Streams: c.mapStreams(streams, !status),
	}
	if secs, odd := campaignUniqSeconds(camp.CookiesTTL); !odd && secs > 0 {
		g.UniqSeconds = secs
	}
	if int(camp.DomainID) > 0 {
		g.DomainID = numID(int(camp.DomainID))
	}
	if int(camp.TrafficSourceID) > 0 {
		g.SourceID = numID(int(camp.TrafficSourceID))
	}
	g.Parameters = fields.Parameters
	g.ParamTokens = fields.Tokens
	g.CostParam = fields.CostParam
	g.CostToken = fields.CostToken
	g.CurrencyParam = fields.CurrencyParam
	g.CurrencyToken = fields.CurrencyToken
	return g, losses
}

// trimFolder ограничивает подпись папки 64 символами, как админка.
//
// Параметры: name — имя группы Keitaro.
// Результат: подпись и true, если строка была обрезана.
// Ошибки: нет.
func trimFolder(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) <= 64 {
		return name, false
	}
	return string([]rune(name)[:64]), true
}

// MaxNotesRunes — предел заметки кампании в символах, как в админке emtds.
const MaxNotesRunes = 10000

// trimNotes готовит заметку кампании Keitaro к записи в группу.
//
// Параметры: text — поле notes кампании.
// Результат: текст без краевых пробелов, обрезанный до MaxNotesRunes символов. Пустая заметка даёт пустую строку.
// Ошибки: нет.
func trimNotes(text string) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= MaxNotesRunes {
		return text
	}
	return string([]rune(text)[:MaxNotesRunes])
}

// parsePostbacks читает массив исходящих шаблонов кампании.
//
// Параметры: raw — JSON postbacks.
// Результат: шаблоны и текст потери. Пустой текст значит, что терять нечего.
// Ошибки: нет. Немассив и пустой разбор возвращают прежнюю причину пропуска.
func parsePostbacks(raw json.RawMessage) ([]Postback, string) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "[]" {
		return nil, ""
	}
	var arr []struct {
		URL      string          `json:"url"`
		Method   string          `json:"method"`
		Statuses []string        `json:"statuses"`
		Status   json.RawMessage `json:"status"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		return nil, "исходящие S2S шаблоны не переносятся"
	}
	var out []Postback
	rewritten := false
	skipped := 0
	for _, item := range arr {
		u := strings.TrimSpace(item.URL)
		if !httpish(u) {
			skipped++
			continue
		}
		method := strings.ToUpper(strings.TrimSpace(item.Method))
		if method == "" {
			method = "GET"
		}
		if method != "GET" && method != "POST" {
			method = "GET"
			rewritten = true
		}
		statuses := append([]string{}, item.Statuses...)
		if len(statuses) == 0 && len(bytesTrim(item.Status)) > 0 {
			var many []string
			var one string
			if json.Unmarshal(item.Status, &many) == nil {
				statuses = many
			} else if json.Unmarshal(item.Status, &one) == nil && strings.TrimSpace(one) != "" {
				statuses = []string{strings.TrimSpace(one)}
			}
		}
		out = append(out, Postback{URL: u, Method: method, Statuses: statuses})
	}
	if len(out) == 0 {
		return nil, "исходящие S2S шаблоны не переносятся"
	}
	if skipped > 0 {
		return out, "часть исходящих S2S пропущена"
	}
	if rewritten {
		return out, "метод постбэка заменён на GET"
	}
	return out, ""
}

// bytesTrim убирает пробелы у JSON-фрагмента.
//
// Параметры: raw — фрагмент.
// Результат: фрагмент без краевых пробелов.
// Ошибки: нет.
func bytesTrim(raw json.RawMessage) string {
	return strings.TrimSpace(string(raw))
}

// mapStreams упорядочивает потоки: forced, regular, default.
//
// Параметры: streams — потоки кампании; forceOff — выключить все потоки. Справочники и отчёт лежат в получателе.
// Результат: потоки emtds, по одному на поток Keitaro.
// Ошибки: нет.
func (c *mapCtx) mapStreams(streams []KStream, forceOff bool) []Stream {
	sorted := append([]KStream(nil), streams...)
	sort.Slice(sorted, func(i, j int) bool {
		ri, rj := typeRank(sorted[i].Type), typeRank(sorted[j].Type)
		if ri != rj {
			return ri < rj
		}
		if int(sorted[i].Position) != int(sorted[j].Position) {
			return int(sorted[i].Position) < int(sorted[j].Position)
		}
		return int(sorted[i].ID) < int(sorted[j].ID)
	})
	var out []Stream
	for _, st := range sorted {
		out = append(out, c.buildStream(st, forceOff))
	}
	if out == nil {
		out = []Stream{}
	}
	return out
}

// typeRank задаёт порядок типов потока Keitaro.
//
// Параметры: t — type потока.
// Результат: 0 forced, 1 regular и неизвестное, 2 default.
// Ошибки: нет.
func typeRank(t string) int {
	switch strings.ToLower(t) {
	case "forced":
		return 0
	case "default":
		return 2
	default:
		return 1
	}
}

// skippedFiltersNote отделяет в заметке потока имена фильтров Keitaro, которые перенос пропустил.
// Заметка начинается с keitaro_stream_id=N, пометка идёт после него.
const skippedFiltersNote = "; не перенесены фильтры Keitaro: "

// buildStream собирает один поток emtds без размножения filter_or.
//
// Параметры: st — поток; forceOff — выключить поток. Справочники и отчёт лежат в получателе.
// Результат: поток с FilterOr, долями и правилами.
// Ошибки: нет.
func (c *mapCtx) buildStream(st KStream, forceOff bool) Stream {
	s, extra := c.streamOutput(st)
	rules, ok, why, flosses, skipped := mapFilters(st.Filters, c.lists, st.FilterOr)
	ref := streamRef(int(st.ID))
	for _, l := range flosses {
		l.Ref = ref
		*c.losses = append(*c.losses, l)
	}
	s.Comment = RenoteStream(s.Comment, skipped)
	s.Rules = rules
	s.FilterOr = st.FilterOr
	on := !forceOff && !strings.EqualFold(st.State, "disabled") && extra == ""
	s.Status = on && ok
	if extra != "" {
		s.Status = false
		*c.losses = append(*c.losses, Loss{Kind: "disabled", Ref: ref, Detail: extra})
	} else if !ok {
		s.Status = false
		*c.losses = append(*c.losses, Loss{Kind: "disabled", Ref: ref, Detail: why})
	}
	return s
}

// streamOutput собирает доли, запасной URL и причину выключения.
//
// Параметры: st — поток. Справочники, отчёт, файлы страниц, alias кампаний и маски placeholder лежат в получателе.
// Результат: поток без правил и текст причины. Пустая причина значит, что выход годится.
// Ошибки: нет.
func (c *mapCtx) streamOutput(st KStream) (Stream, string) {
	offers, landings, losses, pages, aliases, swaps := c.offers, c.landings, c.losses, c.pages, c.aliases, c.swaps
	s := Stream{
		Name:    streamTitle(st),
		Comment: StreamNote(int(st.ID), nil),
		Out:     Output{Redirect: "show_text", Out: "", Chance: 100},
	}
	ref := streamRef(int(st.ID))
	if int(st.Weight) > 0 {
		s.Weight = int(st.Weight)
	}
	s.Out.Offers = offerRefs(st, offers, losses)
	s.Out.Landings = landingRefs(st, landings, losses)
	if localOffer(st, offers, losses) {
		return s, "локальный PHP не скачивается и не исполняется"
	}
	if hasLocalLanding(st, landings) {
		s.Out.Redirect = "landing"
		s.Out.Out = ""
		s.Out.Chance = 100
		return s, ""
	}
	if status404Action(st.ActionType) {
		payload := strings.TrimSpace(st.ActionPayload)
		s.Out.Chance = 100
		if payload == "" || botProtectionStream(st) {
			s.Out.Redirect = "404_not_found"
			s.Out.Out = ""
			return s, ""
		}
		s.Out.Redirect = "show_html_404"
		s.Out.Out = pageRef("show_html_404", finishMacros(payload, swaps), pages)
		return s, ""
	}
	if strings.EqualFold(strings.TrimSpace(st.ActionType), "campaign") {
		redirect, out, approx := campaignAction(st.ActionPayload, aliases)
		s.Out.Redirect = redirect
		s.Out.Out = out
		s.Out.Chance = 100
		if approx != "" {
			*losses = append(*losses, Loss{Kind: "approximated", Ref: ref, Detail: approx})
		}
		return s, ""
	}
	redirect, approx := redirectOf(st.ActionType, st.Schema)
	fallback := ""
	if len(s.Out.Offers) > 0 {
		fallback = firstOfferURL(st, offers)
	} else if len(s.Out.Landings) > 0 {
		fallback = firstLandingURL(st, landings)
	} else if strings.TrimSpace(st.ActionPayload) != "" && !localAction(st.ActionType) && carriesOut(redirect) {
		fallback = st.ActionPayload
	}
	next := finishMacros(fallback, swaps)
	s.Out.Redirect = redirect
	s.Out.Out = next
	s.Out.Chance = 100
	if !carriesOut(redirect) {
		s.Out.Out = ""
	} else {
		s.Out.Out = pageRef(redirect, next, pages)
	}
	if approx != "" {
		*losses = append(*losses, Loss{Kind: "approximated", Ref: ref, Detail: approx})
	}
	return s, ""
}

// botProtectionStream сообщает, что поток Keitaro — Bot Protection или «Замыкающий».
//
// Параметры: st — поток.
// Результат: true для имён по правилу BotProtectionStreamName.
// Ошибки: нет.
func botProtectionStream(st KStream) bool {
	return BotProtectionStreamName(st.Name)
}

// streamTitle берёт имя потока Keitaro.
//
// Параметры: st — поток.
// Результат: имя без краевых пробелов. Пустое имя становится stream и id потока.
// Ошибки: нет.
func streamTitle(st KStream) string {
	name := strings.TrimSpace(st.Name)
	if name == "" {
		return streamRef(int(st.ID))
	}
	return name
}

// streamRef собирает ссылку отчёта на поток.
//
// Параметры: id — id потока Keitaro.
// Результат: строка вида stream 37520.
// Ошибки: нет.
func streamRef(id int) string {
	return fmt.Sprintf("stream %d", id)
}

// firstOfferURL берёт адрес первого живого внешнего оффера.
//
// Параметры: st — поток; offers — справочник.
// Результат: URL или пустая строка.
// Ошибки: нет.
func firstOfferURL(st KStream, offers map[int]Offer) string {
	for _, ref := range st.Offers {
		if strings.EqualFold(ref.State, "deleted") {
			continue
		}
		o, ok := offers[int(ref.OfferID)]
		if !ok || offerLocal(o) {
			continue
		}
		if u := httpURL(o.ActionType, o.ActionPayload); u != "" {
			return u
		}
	}
	return ""
}

// firstLandingURL берёт адрес первого живого внешнего лендинга.
//
// Параметры: st — поток; landings — справочник.
// Результат: URL или пустая строка.
// Ошибки: нет.
func firstLandingURL(st KStream, landings map[int]Landing) string {
	for _, ref := range st.Landings {
		if strings.EqualFold(ref.State, "deleted") {
			continue
		}
		l, ok := landings[int(ref.LandingID)]
		if !ok || landingLocal(l) {
			continue
		}
		if u := httpURL(l.ActionType, l.ActionPayload); u != "" {
			return u
		}
	}
	return ""
}

// hasLocalLanding сообщает, что в потоке есть живой локальный лендинг.
//
// Параметры: st — поток; landings — справочник.
// Результат: true, если ссылка не удалена и лендинг локальный. Файл лендинга не читается и строка отчёта не пишется.
// Ошибки: нет.
func hasLocalLanding(st KStream, landings map[int]Landing) bool {
	for _, ref := range st.Landings {
		if strings.EqualFold(ref.State, "deleted") {
			continue
		}
		l, ok := landings[int(ref.LandingID)]
		if ok && landingLocal(l) {
			return true
		}
	}
	return false
}

// localOffer сообщает о локальном оффере и не читает его файл.
//
// Параметры: st — поток; offers — справочник; losses — отчёт.
// Результат: true, если в потоке есть локальный оффер.
// Ошибки: нет.
func localOffer(st KStream, offers map[int]Offer, losses *[]Loss) bool {
	found := false
	for _, ref := range st.Offers {
		if strings.EqualFold(ref.State, "deleted") {
			continue
		}
		o, ok := offers[int(ref.OfferID)]
		if !ok || !offerLocal(o) {
			continue
		}
		*losses = append(*losses, Loss{
			Kind:   "skipped",
			Ref:    entityRef("offer", int(o.ID)),
			Detail: fmt.Sprintf("id=%d name=%s type=%s", int(o.ID), o.Name, first(o.OfferType, o.ActionType)),
		})
		found = true
	}
	return found
}

// rewriteMacros заменяет известные макросы Keitaro на метки emtds.
// Неизвестное имя в фигурных скобках остаётся: движок читает его как параметр URL.
// Идентификатор клика всегда записывается как [SUBID]: и макрос {subid}, и старая метка [CID] в тексте.
//
// Параметры: raw — исходная строка URL или текста.
// Результат: строка после замены известных макросов.
// Ошибки: нет.
func rewriteMacros(raw string) string {
	out := macroRE.ReplaceAllStringFunc(raw, func(m string) string {
		if next, ok := macroMap[m]; ok {
			return next
		}
		return m
	})
	return strings.ReplaceAll(out, legacyClickIDMacro, clickIDMacro)
}

// finishMacros сначала заменяет известные макросы, затем placeholder этой кампании.
//
// Параметры: raw — исходная строка; swaps — placeholder → маска. Пустая карта оставляет результат rewriteMacros.
// Результат: строка, в которой переименованный слот уже записан как {sub_id_N} или [KEY].
// Ошибки: нет. Повторный проход по известным макросам не делается, чтобы {sub_id_1} из карты не стал [PAR-1].
func finishMacros(raw string, swaps map[string]string) string {
	return applyMacroMap(rewriteMacros(raw), swaps)
}

// unique убирает повторы, сохраняя порядок.
//
// Параметры: items — строки.
// Результат: строки без повторов.
// Ошибки: нет.
func unique(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// first возвращает первую непустую строку.
//
// Параметры: a, b — кандидаты.
// Результат: a, если он не пуст, иначе b.
// Ошибки: нет.
func first(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
