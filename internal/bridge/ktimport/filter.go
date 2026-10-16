package ktimport

import (
	"encoding/json"
	"strconv"
	"strings"
)

// proxySkip — текст пропуска фильтра proxy. Детектора в kuztds нет, поток из-за этого не выключается.
const proxySkip = "фильтр proxy не переносится: в kuztds нет детектора прокси"

// ipVersionSkip сообщает, что фильтр версии IP пропущен и поток из-за него не выключается.
//
// Параметры: name — имя фильтра, уже в нижнем регистре.
// Результат: текст строки отчёта.
// Ошибки: нет.
func ipVersionSkip(name string) string {
	return "фильтр " + name + " не переносится: в kuztds нет фильтра версии IP"
}

// ipVersionFilter отличает фильтр версии IP от фильтра адреса.
//
// Параметры: name — имя фильтра в нижнем регистре.
// Результат: true для ipv4, ipv6, ipv_4, ipv_6 и ip_version.
// Ошибки: нет.
func ipVersionFilter(name string) bool {
	switch name {
	case "ipv4", "ipv6", "ipv_4", "ipv_6", "ip_version":
		return true
	default:
		return false
	}
}

// mapFilters переводит фильтры потока в правила kuztds.
//
// Параметры: filters — фильтры Keitaro; lists — файлы списков IP, которые собрал этот прогон.
// Результат: правила, true если поток можно оставить включённым, причина выключения и строки отчёта.
// Ошибки: нет. Неизвестное имя пропускается. Поток выключается только когда ни один фильтр не перенёсся.
// Фильтры proxy и версии IP пропускаются и в этот подсчёт не входят.
func mapFilters(filters []KFilter, lists *listBag) (Rules, bool, string, []Loss) {
	var rules Rules
	var losses []Loss
	named, mapped := 0, 0
	for _, f := range filters {
		name := strings.ToLower(strings.TrimSpace(f.Name))
		if name == "" {
			continue
		}
		if name == "proxy" {
			losses = append(losses, Loss{Kind: "skipped", Detail: proxySkip})
			continue
		}
		if ipVersionFilter(name) {
			losses = append(losses, Loss{Kind: "skipped", Detail: ipVersionSkip(name)})
			continue
		}
		named++
		part, ok, approx, why := oneFilter(f, lists)
		if !ok {
			if why == "" {
				why = "фильтр " + name + " не переносится"
			}
			losses = append(losses, Loss{Kind: "skipped", Detail: why})
			continue
		}
		mapped++
		if approx {
			if why == "" {
				why = "фильтр " + name + " перенесён приближённо"
			}
			losses = append(losses, Loss{Kind: "approximated", Detail: why})
		}
		rules = mergeRules(rules, part)
	}
	if named > 0 && mapped == 0 {
		return rules, false, "нет переносимых фильтров, поток совпал бы со всеми", losses
	}
	return rules, true, "", losses
}

// oneFilter переводит один фильтр Keitaro в правила kuztds.
//
// Параметры: f — фильтр; lists — файлы списков IP этого прогона.
// Результат: правила, true если фильтр поддержан, true если значение угадано, и пояснение.
// Ошибки: нет.
func oneFilter(f KFilter, lists *listBag) (Rules, bool, bool, string) {
	name := strings.ToLower(strings.TrimSpace(f.Name))
	if name == "param" || name == "parameter" || name == "sub_id" || strings.HasPrefix(name, "sub_id_") {
		return queryFilter(name, f)
	}
	switch name {
	case "country":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Country: v} })
	case "city":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{City: v} })
	case "region":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Region: v} })
	case "language", "lang":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Lang: v} })
	case "os":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{OS: v} })
	case "browser":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Browser: v} })
	case "referer", "referrer":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Referer: v} })
	case "user_agent", "ua", "useragent":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{UAText: v} })
	case "keyword":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Key: v} })
	case "timezone", "time_zone":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Timezone: v} })
	case "device_type", "device":
		return deviceFilter(f)
	case "device_model":
		return listRule(name, f, func(v ListFilter) Rules { return Rules{Brand: v} })
	case "ipv4", "ipv6", "ipv_4", "ipv_6", "ip_version":
		return Rules{}, false, false, ipVersionSkip(name)
	case "day_of_week", "weekday", "schedule", "timetable":
		return scheduleFilter(name, f)
	case "unique", "uniqueness":
		return uniqueFilter(f)
	case "bot", "is_bot":
		return botFilter(f)
	case "ip":
		return ipFilter(f, lists)
	case "isp":
		return ispFilter(f)
	case "asn":
		return asnFilter(name, f)
	case "proxy":
		return Rules{}, false, false, proxySkip
	case "":
		return Rules{}, true, false, ""
	default:
		return Rules{}, false, false, "фильтр " + name + " не переносится"
	}
}

// ispFilter кладёт имена провайдеров в фильтр организации.
//
// Параметры: f — фильтр isp.
// Результат: правила Org. Значения обрезаны, пустые выброшены, повторы убраны, регистр сохранён.
// Ошибки: нет.
func ispFilter(f KFilter) (Rules, bool, bool, string) {
	rules, ok, approx, why := listRule("isp", f, func(v ListFilter) Rules { return Rules{Org: v} })
	if !ok || rules.Org.Flag == 0 {
		return rules, ok, approx, why
	}
	vals := unique(rules.Org.Values)
	rules.Org.Values = vals
	rules.Org.Raw = strings.Join(vals, ",")
	return rules, ok, approx, why
}

// listRule строит один списочный фильтр.
//
// Параметры: name — имя для отчёта; f — фильтр; apply — кладёт ListFilter в правила.
// Результат: правила, признак переноса, признак приближения и пояснение.
// Ошибки: нет.
func listRule(name string, f KFilter, apply func(ListFilter) Rules) (Rules, bool, bool, string) {
	if emptyPayload(f.Payload) {
		return Rules{}, true, false, ""
	}
	values, mode, approx, ok := readPayload(f.Payload)
	if !ok || len(values) == 0 {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	return apply(listFlag(mode, values)), true, approx, ""
}

// queryFilter кладёт parameter и sub_id в фильтр query.
//
// Параметры: name — имя фильтра; f — фильтр.
// Результат: правила с полем get, признаки переноса и приближения, пояснение.
// Ошибки: нет.
func queryFilter(name string, f KFilter) (Rules, bool, bool, string) {
	if emptyPayload(f.Payload) {
		return Rules{}, true, false, ""
	}
	values, mode, approx, ok := queryValues(name, f.Payload)
	if !ok || len(values) == 0 {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	return Rules{Query: listFlag(mode, values)}, true, approx, ""
}

// queryValues достаёт пары name=value для параметра URL.
//
// Параметры: name — имя фильтра; raw — payload.
// Результат: значения, режим из объекта, признак приближения и true, если форма понятна.
// Ошибки: нет.
func queryValues(name string, raw json.RawMessage) ([]string, string, bool, bool) {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil && !looksLikeList(obj) {
		key := firstString(obj["name"], obj["key"])
		if key == "" && strings.HasPrefix(name, "sub_id") {
			key = name
		}
		if key == "" {
			return nil, "", true, false
		}
		val := firstString(obj["value"], obj["val"])
		mode := firstString(obj["mode"])
		if val == "" {
			return []string{key}, mode, true, true
		}
		return []string{key + "=" + val}, mode, false, true
	}
	values, mode, approx, ok := readPayload(raw)
	if !ok {
		return nil, "", false, false
	}
	if strings.HasPrefix(name, "sub_id") {
		var out []string
		for _, v := range values {
			if strings.Contains(v, "=") {
				out = append(out, v)
				continue
			}
			if v != "" {
				out = append(out, name+"="+v)
			}
		}
		return out, mode, approx, len(out) > 0
	}
	return values, mode, approx, len(values) > 0
}

// looksLikeList отличает объект {values} от объекта {name,value}.
//
// Параметры: obj — разобранный объект.
// Результат: true, если внутри есть values или value-массив.
// Ошибки: нет.
func looksLikeList(obj map[string]any) bool {
	_, values := obj["values"]
	return values
}

// emptyPayload сообщает, что payload пуст и фильтр ничего не ограничивает.
//
// Параметры: raw — JSON payload.
// Результат: true для пустого, null и пустого массива или строки.
// Ошибки: нет.
func emptyPayload(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "[]" || s == `""`
}

// readPayload достаёт строки и необязательный mode из payload.
//
// Параметры: raw — JSON payload.
// Результат: значения, mode, true если форму пришлось угадывать, true если JSON понятен.
// Ошибки: нет.
func readPayload(raw json.RawMessage) ([]string, string, bool, bool) {
	if emptyPayload(raw) {
		return nil, "", false, true
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return cleanValues(list), "", false, true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		one = strings.TrimSpace(one)
		if one == "" {
			return nil, "", false, true
		}
		if strings.HasPrefix(one, "[") || strings.HasPrefix(one, "{") {
			values, mode, _, ok := readPayload(json.RawMessage(one))
			return values, mode, true, ok
		}
		if strings.Contains(one, ",") {
			return splitComma(one), "", true, true
		}
		return []string{one}, "", false, true
	}
	var obj struct {
		Mode   string          `json:"mode"`
		Values json.RawMessage `json:"values"`
		Value  json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil && (len(obj.Values) > 0 || len(obj.Value) > 0 || obj.Mode != "") {
		chunk := obj.Values
		if len(chunk) == 0 || string(chunk) == "null" {
			chunk = obj.Value
		}
		values, _, approx, ok := readPayload(chunk)
		if !ok {
			return nil, obj.Mode, true, false
		}
		return values, obj.Mode, approx, true
	}
	var anyList []any
	if json.Unmarshal(raw, &anyList) == nil {
		var vals []string
		for _, item := range anyList {
			text := strings.TrimSpace(anyText(item))
			if text != "" {
				vals = append(vals, text)
			}
		}
		return vals, "", true, true
	}
	return nil, "", false, false
}

// anyText печатает значение payload как строку.
//
// Параметры: v — значение после JSON.
// Результат: строка без угловых скобок карты. Число печатается как fmt.
// Ошибки: нет.
func anyText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// firstString возвращает первую непустую строку из значений JSON.
//
// Параметры: vals — кандидаты.
// Результат: первая строка. Число становится его текстом.
// Ошибки: нет.
func firstString(vals ...any) string {
	for _, v := range vals {
		switch t := v.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				return strings.TrimSpace(t)
			}
		case float64:
			if text := anyText(t); text != "" {
				return text
			}
		}
	}
	return ""
}
