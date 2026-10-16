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
// В kuztds 0 — любой визит, 1 — только уникальные, 2 — только повторные.
//
// Параметры: f — фильтр.
// Результат: правило Unique и признаки переноса.
// Ошибки: нет.
func uniqueFilter(f KFilter) (Rules, bool, bool, string) {
	return triFilter(f, "unique", func(v int) Rules { return Rules{Unique: v} })
}

// botFilter переносит фильтр бота: 1 оставляет ботов, 2 оставляет людей.
//
// Параметры: f — фильтр.
// Результат: правило Bot и признаки переноса.
// Ошибки: нет.
func botFilter(f KFilter) (Rules, bool, bool, string) {
	return triFilter(f, "bot", func(v int) Rules { return Rules{Bot: v} })
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
	base.Timezone = mergeList(base.Timezone, add.Timezone, false)
	base.ASN = mergeList(base.ASN, add.ASN, false)
	base.Org = mergeList(base.Org, add.Org, false)
	base.Query = mergeList(base.Query, add.Query, true)
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
	return base
}

// mergeList заменяет фильтр или, для query, дописывает значения.
//
// Параметры: dst и src — фильтры; concat — склеивать значения при одинаковом флаге.
// Результат: итоговый фильтр.
// Ошибки: нет.
func mergeList(dst, src ListFilter, concat bool) ListFilter {
	if src.Flag == 0 {
		return dst
	}
	if !concat || dst.Flag == 0 {
		return src
	}
	vals := append(append([]string{}, dst.Values...), src.Values...)
	return ListFilter{Flag: dst.Flag, Raw: strings.Join(vals, ","), Values: vals}
}
