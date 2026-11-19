package ktimport

import (
	"encoding/json"
	"strings"
)

// deviceFilter переводит тип устройства в флаги computer, phone и tablet.
//
// Параметры: f — фильтр. accept блокирует неперечисленные типы, reject блокирует перечисленные.
// Результат: правила и true, если хотя бы один тип узнан.
// Ошибки: нет.
func deviceFilter(f KFilter) (Rules, bool, bool, string) {
	if emptyPayload(f.Payload) {
		return Rules{}, true, false, ""
	}
	values, mode, approx, ok := readPayload(f.Payload)
	if !ok {
		return Rules{}, false, false, "фильтр device_type не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	rules, known := deviceRules(values, mode)
	if !known {
		return Rules{}, false, false, "фильтр device_type не разобран"
	}
	return rules, true, approx, ""
}

// deviceRules выставляет 1 на типах, которые надо отсечь.
//
// Параметры: values — значения; mode — режим Keitaro.
// Результат: правила и true, если тип узнан.
// Ошибки: нет.
func deviceRules(values []string, mode string) (Rules, bool) {
	allow := map[string]bool{}
	known := 0
	for _, v := range values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "mobile", "phone", "smartphone":
			allow["phone"] = true
			known++
		case "desktop", "computer":
			allow["computer"] = true
			known++
		case "tablet":
			allow["tablet"] = true
			known++
		}
	}
	if known == 0 {
		return Rules{}, false
	}
	var r Rules
	if isReject(mode) {
		if allow["computer"] {
			r.Computer = 1
		}
		if allow["phone"] {
			r.Phone = 1
		}
		if allow["tablet"] {
			r.Tablet = 1
		}
		return r, true
	}
	if !allow["computer"] {
		r.Computer = 1
	}
	if !allow["phone"] {
		r.Phone = 1
	}
	if !allow["tablet"] {
		r.Tablet = 1
	}
	return r, true
}

// scheduleFilter переносит дни недели. Часы внутри дня не переносятся и помечаются приближением.
//
// Параметры: name — имя фильтра; f — фильтр.
// Результат: расписание, true если день узнан, признак приближения и пояснение.
// Ошибки: нет.
func scheduleFilter(name string, f KFilter) (Rules, bool, bool, string) {
	sch, approx, ok := parseSchedule(f.Payload)
	if !ok {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	why := ""
	if approx {
		why = "фильтр " + name + " перенесён по дням, часы отброшены"
	}
	return Rules{Schedule: sch}, true, approx, why
}

// parseSchedule читает дни из списка имён, чисел или объекта по дням.
//
// Параметры: raw — payload.
// Результат: расписание, true если номер дня или часы угаданы, true если день есть.
// Ошибки: нет.
func parseSchedule(raw json.RawMessage) (Schedule, bool, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && scheduleObject(obj) {
		var sch Schedule
		sch.Enabled = true
		found := false
		for key, val := range obj {
			idx, _, known := dayIndex(key)
			if !known || !dayOn(val) {
				continue
			}
			sch.Days[idx] = true
			found = true
		}
		return sch, true, found
	}
	values, _, approx, ok := readPayload(raw)
	if !ok {
		return Schedule{}, false, false
	}
	var sch Schedule
	sch.Enabled = true
	found := false
	for _, v := range values {
		idx, guessed, known := dayIndex(v)
		if !known {
			continue
		}
		if guessed {
			approx = true
		}
		sch.Days[idx] = true
		found = true
	}
	return sch, approx, found
}

// scheduleObject отличает таблицу по дням от объекта {values}.
//
// Параметры: obj — объект payload.
// Результат: true, если ключ похож на день и нет поля values.
// Ошибки: нет.
func scheduleObject(obj map[string]json.RawMessage) bool {
	if _, ok := obj["values"]; ok {
		return false
	}
	for key := range obj {
		if _, _, known := dayIndex(key); known {
			return true
		}
	}
	return false
}

// dayOn сообщает, что в расписании дня есть хотя бы один интервал.
//
// Параметры: raw — значение дня.
// Результат: true для непустого массива, true и непустой строки.
// Ошибки: нет.
func dayOn(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "[]" || s == "false" || s == `""` {
		return false
	}
	return true
}

// dayIndex переводит имя или номер дня в индекс недели. 0 — воскресенье.
//
// Параметры: raw — день.
// Результат: индекс, true если это номер а не имя, true если день узнан.
// Ошибки: нет.
func dayIndex(raw string) (int, bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sun", "sunday":
		return 0, false, true
	case "mon", "monday":
		return 1, false, true
	case "tue", "tues", "tuesday":
		return 2, false, true
	case "wed", "wednesday":
		return 3, false, true
	case "thu", "thur", "thurs", "thursday":
		return 4, false, true
	case "fri", "friday":
		return 5, false, true
	case "sat", "saturday":
		return 6, false, true
	}
	n := strings.TrimSpace(raw)
	if n == "" || !allDigits(n) {
		return 0, false, false
	}
	switch n {
	case "0":
		return 0, true, true
	case "1":
		return 1, true, true
	case "2":
		return 2, true, true
	case "3":
		return 3, true, true
	case "4":
		return 4, true, true
	case "5":
		return 5, true, true
	case "6":
		return 6, true, true
	case "7":
		return 0, true, true
	default:
		return 0, false, false
	}
}

// uniqueFilter переносит уникальность.
// В emtds 0 — любой визит, 1 — только уникальные, 2 — только повторные.
//
// Keitaro кладёт в payload область уникальности: stream, campaign или global; режим accept
// оставляет уникальных, reject — повторных. Движок считает уникальность внутри кампании, поэтому
// область stream и global переносится приближённо. Payload вида yes/no читается как прежде.
//
// Параметры: f — фильтр.
// Результат: правило Unique, признаки переноса и пояснение для области не campaign.
// Ошибки: нет.
func uniqueFilter(f KFilter) (Rules, bool, bool, string) {
	apply := func(v int) Rules { return Rules{Unique: v} }
	scope := uniqueScope(f.Payload)
	if scope == "" {
		return presenceFilter(f, "uniqueness", apply)
	}
	rules, ok, _, _ := presenceFilter(KFilter{Name: f.Name, Mode: f.Mode}, "uniqueness", apply)
	if scope == "campaign" {
		return rules, ok, false, ""
	}
	return rules, ok, true, "фильтр uniqueness перенесён приближённо: область " + scope + " заменена уникальностью в кампании"
}

// uniqueScope читает область уникальности Keitaro из payload.
//
// Параметры: raw — payload фильтра uniqueness.
// Результат: stream, campaign или global; пустая строка для любой другой формы.
// Ошибки: нет.
func uniqueScope(raw json.RawMessage) string {
	values, _, _, ok := readPayload(raw)
	if !ok || len(values) != 1 {
		return ""
	}
	switch v := strings.ToLower(values[0]); v {
	case "stream", "campaign", "global":
		return v
	default:
		return ""
	}
}

// botFilter переносит фильтр бота: 1 оставляет ботов, 2 оставляет людей.
// Keitaro шлёт его с payload null, и смысл задаёт режим: accept — только боты, reject — только люди.
//
// Параметры: f — фильтр.
// Результат: правило Bot и признаки переноса.
// Ошибки: нет.
func botFilter(f KFilter) (Rules, bool, bool, string) {
	return presenceFilter(f, "bot", func(v int) Rules { return Rules{Bot: v} })
}

// emptyRefererFilter переносит фильтр пустого Referer в правило has_referer.
// accept оставляет визиты без Referer (has_referer 2), reject — визиты с Referer (has_referer 1).
//
// Параметры: f — фильтр empty_referrer; payload Keitaro не шлёт.
// Результат: правило HasReferer и признаки переноса.
// Ошибки: нет.
func emptyRefererFilter(f KFilter) (Rules, bool, bool, string) {
	return presenceFilter(f, "empty_referrer", func(v int) Rules { return Rules{HasReferer: 3 - v} })
}

// presenceFilter читает фильтр-признак Keitaro, у которого нет значения: bot, proxy, empty_referrer.
// Пустой payload значит «признак есть»: accept даёт 1, reject даёт 2. Payload yes/no читает triFilter.
//
// Параметры: f — фильтр; name — имя для отчёта; apply — кладёт код в правила.
// Результат: правила и признаки переноса.
// Ошибки: нет.
func presenceFilter(f KFilter, name string, apply func(int) Rules) (Rules, bool, bool, string) {
	if !emptyPayload(f.Payload) {
		return triFilter(f, name, apply)
	}
	if isReject(f.Mode) {
		return apply(2), true, false, ""
	}
	return apply(1), true, false, ""
}

// ipvFilter maps a Keitaro IP-version filter onto Rules.IPVersion.
//
// ipv4 and ipv_4 are version 4. ipv6 and ipv_6 are version 6.
// ip_version takes the version from the payload: "4", "ipv4", "6" or "ipv6",
// compared without case. The payload may be a string, a number, or a one-element list.
// Accept of version 4 yields 1 (IPv4 only) and accept of version 6 yields 2 (IPv6 only).
// Reject swaps those two codes.
//
// Parameters: f — the Keitaro filter.
// Returns: rules with IPVersion set, and true when the version is known.
// Errors: none. An unknown version returns false and empty rules.
func ipvFilter(f KFilter) (Rules, bool) {
	ver, known := ipvNamed(strings.ToLower(strings.TrimSpace(f.Name)))
	if !known {
		ver, known = ipvPayload(f.Payload)
	}
	if !known {
		return Rules{}, false
	}
	code := 1
	if ver == 6 {
		code = 2
	}
	if isReject(f.Mode) {
		if code == 1 {
			code = 2
		} else {
			code = 1
		}
	}
	return Rules{IPVersion: code}, true
}

// ipvNamed reads the IP version from a filter name.
//
// Parameters: name — filter name, already lower-case and trimmed.
// Returns: 4 or 6, and true for ipv4, ipv_4, ipv6, and ipv_6.
// Errors: none. ip_version and every other name return false.
func ipvNamed(name string) (int, bool) {
	switch name {
	case "ipv4", "ipv_4":
		return 4, true
	case "ipv6", "ipv_6":
		return 6, true
	default:
		return 0, false
	}
}

// ipvPayload reads one IP version from a filter payload.
//
// Parameters: raw — JSON string, number, or a one-element list.
// Returns: 4 or 6, and true when that single token is a known version.
// Errors: none. Any other shape or token returns false.
func ipvPayload(raw json.RawMessage) (int, bool) {
	values, _, _, ok := readPayload(raw)
	if ok {
		if len(values) != 1 {
			return 0, false
		}
		return ipvToken(values[0])
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil || (n != 4 && n != 6) {
		return 0, false
	}
	if n == 4 {
		return 4, true
	}
	return 6, true
}

// ipvToken classifies one version token.
//
// Parameters: raw — payload text, any case.
// Returns: 4 or 6, and true for 4, ipv4, ipv_4, 6, ipv6, and ipv_6.
// Errors: none.
func ipvToken(raw string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "4", "ipv4", "ipv_4":
		return 4, true
	case "6", "ipv6", "ipv_6":
		return 6, true
	default:
		return 0, false
	}
}

// triFilter читает yes/no и режим accept/reject.
// accept+yes и reject+no дают 1. accept+no и reject+yes дают 2.
//
// Параметры: f — фильтр; name — имя для отчёта; apply — кладёт код в правила.
// Результат: правила и признаки переноса.
// Ошибки: нет.
func triFilter(f KFilter, name string, apply func(int) Rules) (Rules, bool, bool, string) {
	if emptyPayload(f.Payload) {
		return Rules{}, true, false, ""
	}
	values, mode, approx, ok := readPayload(f.Payload)
	if !ok {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	kind, known := triClass(values)
	if !known {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	code := 2
	if (kind == "yes" && !isReject(mode)) || (kind == "no" && isReject(mode)) {
		code = 1
	}
	return apply(code), true, approx, ""
}

// triClass относит значения к yes или no.
//
// Параметры: values — payload.
// Результат: yes, no и true, если все значения одного класса.
// Ошибки: нет.
func triClass(values []string) (string, bool) {
	yes, no := 0, 0
	for _, v := range values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "bot", "unique", "new":
			yes++
		case "0", "false", "no", "n", "human", "repeat", "old":
			no++
		default:
			return "", false
		}
	}
	if yes > 0 && no == 0 {
		return "yes", true
	}
	if no > 0 && yes == 0 {
		return "no", true
	}
	return "", false
}

// asnFilter оставляет только числовые ASN.
//
// Параметры: name — имя фильтра; f — фильтр.
// Результат: список ASN. Имена сетей без числа не переносятся.
// Ошибки: нет.
func asnFilter(name string, f KFilter) (Rules, bool, bool, string) {
	if emptyPayload(f.Payload) {
		return Rules{}, true, false, ""
	}
	values, mode, approx, ok := readPayload(f.Payload)
	if !ok {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	var keep []string
	dropped := false
	for _, v := range values {
		n, good := asnNumber(v)
		if !good {
			if strings.TrimSpace(v) != "" {
				dropped = true
			}
			continue
		}
		keep = append(keep, n)
	}
	if len(keep) == 0 {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	why := ""
	if dropped {
		approx = true
		why = "фильтр " + name + " оставил только номера ASN"
	}
	return Rules{ASN: listFlag(mode, keep)}, true, approx, why
}

// asnNumber достаёт цифры номера автономной системы.
//
// Параметры: raw — значение, можно с префиксом AS.
// Результат: цифры и true, если кроме префикса ничего нет.
// Ошибки: нет.
func asnNumber(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	up := strings.ToUpper(raw)
	up = strings.TrimPrefix(up, "AS")
	if up == "" || !allDigits(up) {
		return "", false
	}
	return up, true
}

// allDigits проверяет, что строка состоит из цифр.
//
// Параметры: s — строка.
// Результат: true, если каждый символ — цифра.
// Ошибки: нет.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// listFlag строит фильтр списка. accept — только совпадение, reject — исключение.
//
// Параметры: mode — режим Keitaro; values — значения.
// Результат: ListFilter с флагом 2 или 1.
// Ошибки: нет.
func listFlag(mode string, values []string) ListFilter {
	flag := 2
	if isReject(mode) {
		flag = 1
	}
	values = cleanValues(values)
	return ListFilter{Flag: flag, Raw: strings.Join(values, ","), Values: values}
}

// isReject сообщает, что режим отсекает совпадение.
//
// Параметры: mode — режим фильтра.
// Результат: true для reject, blacklist, deny и exclude.
// Ошибки: нет.
func isReject(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "reject", "blacklist", "deny", "exclude":
		return true
	default:
		return false
	}
}

// cleanValues убирает пустые значения.
//
// Параметры: values — список.
// Результат: список без пустых строк.
// Ошибки: нет.
func cleanValues(values []string) []string {
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// mergeRules соединяет два набора. Ненулевое поле из add заменяет базу, query склеивается.
//
// Параметры: base и add — правила.
// Результат: объединённые правила.
// Ошибки: нет.
func mergeRules(base, add Rules) Rules {
	base.Country = mergeList(base.Country, add.Country, false)
	base.City = mergeList(base.City, add.City, false)
	base.Region = mergeList(base.Region, add.Region, false)
	base.Lang = mergeList(base.Lang, add.Lang, false)
	base.UAText = mergeList(base.UAText, add.UAText, false)
	base.Referer = mergeList(base.Referer, add.Referer, false)
	base.Key = mergeList(base.Key, add.Key, false)
	base.OS = mergeList(base.OS, add.OS, false)
	base.Browser = mergeList(base.Browser, add.Browser, false)
	base.Brand = mergeList(base.Brand, add.Brand, false)
	base.Model = mergeList(base.Model, add.Model, false)
	base.Timezone = mergeList(base.Timezone, add.Timezone, false)
	base.ASN = mergeList(base.ASN, add.ASN, false)
	base.Org = mergeList(base.Org, add.Org, false)
	base.Query = mergeList(base.Query, add.Query, true)
	base.SubIDs = mergeSubIDs(base.SubIDs, add.SubIDs)
	if add.IPList.File != "" {
		base.IPList = add.IPList
	}
	if add.Schedule.Enabled {
		if !base.Schedule.Enabled {
			base.Schedule = add.Schedule
		} else {
			for i := range base.Schedule.Days {
				base.Schedule.Days[i] = base.Schedule.Days[i] || add.Schedule.Days[i]
			}
		}
	}
	if add.Computer != 0 {
		base.Computer = add.Computer
	}
	if add.Phone != 0 {
		base.Phone = add.Phone
	}
	if add.Tablet != 0 {
		base.Tablet = add.Tablet
	}
	if add.Unique != 0 {
		base.Unique = add.Unique
	}
	if add.Bot != 0 {
		base.Bot = add.Bot
	}
	if add.HasReferer != 0 {
		base.HasReferer = add.HasReferer
	}
	if add.Proxy != 0 {
		base.Proxy = add.Proxy
	}
	if add.IPVersion != 0 {
		base.IPVersion = add.IPVersion
	}
	return base
}

// mergeList заменяет фильтр или, для query, дописывает значения.
//
// Параметры: dst и src — фильтры; concat — склеивать значения при одинаковом флаге.
// Результат: итоговый фильтр. При склейке с разными флагами остаётся dst: флаг никогда не переворачивается.
// Ошибки: нет.
func mergeList(dst, src ListFilter, concat bool) ListFilter {
	if src.Flag == 0 {
		return dst
	}
	if !concat || dst.Flag == 0 {
		return src
	}
	if dst.Flag != src.Flag {
		return dst
	}
	vals := unique(append(append([]string{}, dst.Values...), src.Values...))
	return ListFilter{Flag: dst.Flag, Raw: strings.Join(vals, ","), Values: vals}
}

// exactJoin сообщает, что два фильтра одного ключа склеиваются без смены смысла.
//
// Параметры: a и b — флаги фильтров; or — поток работает по ИЛИ.
// Результат: true для двух исключающих фильтров при И (исключить любое совпадение) и для двух
// разрешающих при ИЛИ (пропустить любое совпадение). Остальные пары склейка исказила бы.
// Ошибки: нет.
func exactJoin(a, b int, or bool) bool {
	return a == b && ((a == 1 && !or) || (a == 2 && or))
}

// mergeConflict ищет фильтр, который нельзя склеить с уже набранными правилами.
//
// Параметры: base — набранные правила; part — правила нового фильтра; or — поток работает по ИЛИ.
// Результат: пояснение для отчёта или пустая строка, если склейка точная.
// Ошибки: нет.
func mergeConflict(base, part Rules, or bool) string {
	if base.Query.Flag != 0 && part.Query.Flag != 0 && !exactJoin(base.Query.Flag, part.Query.Flag, or) {
		return "фильтр get не склеен с прежним: смысл И/ИЛИ или режим accept/reject изменился бы"
	}
	for key, f := range part.SubIDs {
		if prev, has := base.SubIDs[key]; has && (!exactJoin(prev.Flag, f.Flag, or) || prev.Contains != f.Contains) {
			return "фильтр " + key + " не склеен с прежним: смысл И/ИЛИ, режим accept/reject или способ сравнения изменился бы"
		}
	}
	return ""
}

// mergeSubIDs склеивает правила Sub ID. Один ключ объединяет значения и признак пустого.
//
// Параметры: base и add — правила по ключам sub_id_N.
// Результат: новая карта; nil, если оба набора пусты. Входные карты не меняются.
// Ошибки: нет. Флаги одного ключа проверил mergeConflict; при расхождении остаётся значение base.
func mergeSubIDs(base, add map[string]SubIDFilter) map[string]SubIDFilter {
	if len(base) == 0 && len(add) == 0 {
		return nil
	}
	out := make(map[string]SubIDFilter, len(base)+len(add))
	for key, f := range base {
		out[key] = f
	}
	for key, f := range add {
		prev, has := out[key]
		switch {
		case !has:
			out[key] = f
		case prev.Flag == f.Flag:
			out[key] = SubIDFilter{Flag: prev.Flag, Values: unique(append(append([]string{}, prev.Values...), f.Values...)), Empty: prev.Empty || f.Empty, Contains: prev.Contains}
		}
	}
	return out
}
