package ktimport

import (
	"encoding/json"
	"strconv"
	"strings"
)

// mapFilters переводит фильтры потока в правила kuztds.
//
// Параметры: filters — фильтры Keitaro; lists — файлы списков IP, которые собрал этот прогон;
// or — поток Keitaro работает по ИЛИ.
// Результат: правила, true если поток можно оставить включённым, причина выключения и строки отчёта.
// Ошибки: нет. Неизвестное имя пропускается. Фильтр, который нельзя склеить с прежним без смены
// смысла, пропускается с записью в отчёте. Поток выключается только когда ни один фильтр не перенёсся.
func mapFilters(filters []KFilter, lists *listBag, or bool) (Rules, bool, string, []Loss) {
	var rules Rules
	var losses []Loss
	named, mapped := 0, 0
	for _, f := range filters {
		name := strings.ToLower(strings.TrimSpace(f.Name))
		if name == "" {
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
		if conflict := mergeConflict(rules, part, or); conflict != "" {
			losses = append(losses, Loss{Kind: "skipped", Detail: conflict})
			continue
		}
		mapped++
		if approx {
			if why == "" {
				why = "фильтр " + name + " перенесён приближённо"
			}
			kind := "approximated"
			if strings.HasPrefix(why, skippedValuesNote) {
				kind = "skipped"
			}
			losses = append(losses, Loss{Kind: kind, Detail: why})
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
	if n, ok := subIDNumber(name); ok {
		if n < 1 || n > maxRuleSubID {
			return Rules{}, false, false, "фильтр " + name + " не переносится: правила Sub ID идут от 1 до " + strconv.Itoa(maxRuleSubID)
		}
		return subIDFilter("sub_id_"+strconv.Itoa(n), f)
	}
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
		return modelFilter(f)
	case "ipv4", "ipv6", "ipv_4", "ipv_6", "ip_version":
		part, ok := ipvFilter(f)
		if !ok {
			return Rules{}, false, false, "фильтр " + name + " не разобран"
		}
		return part, true, false, ""
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
		return triFilter(f, "proxy", func(n int) Rules { return Rules{Proxy: n} })
	case "":
		return Rules{}, true, false, ""
	default:
		return Rules{}, false, false, "фильтр " + name + " не переносится"
	}
}

// modelFilter кладёт список моделей устройства в точное правило model.
//
// Параметры: f — фильтр device_model.
// Результат: правила Model. Режим подстроки или регулярного выражения и значение вида /regex/
// не переносятся: движок сравнивает модель точно, и иначе правило стало бы шире или уже.
// Ошибки: нет.
func modelFilter(f KFilter) (Rules, bool, bool, string) {
	values, mode, _, why := exactValues("device_model", f)
	if why != "" {
		return Rules{}, false, false, why
	}
	return Rules{Model: listFlag(mode, values)}, true, true, "фильтр device_model перенесён приближённо: словарь моделей Keitaro отличается от токена UA"
}

// exactValues читает значения фильтра, который движок сравнивает только точно.
//
// Параметры: label — имя фильтра для отчёта; f — фильтр.
// Результат: значения, режим accept или reject, признак угаданной формы и причина отказа.
// Пустой payload, режим подстроки или regex и значение /…/ дают причину отказа: пустой фильтр
// не ограничивал бы никого, а подстрока или regex сделали бы правило шире или уже.
// Ошибки: нет.
func exactValues(label string, f KFilter) ([]string, string, bool, string) {
	if emptyPayload(f.Payload) {
		return nil, "", false, "фильтр " + label + ": пустой список значений не переносится"
	}
	values, mode, approx, ok := readPayload(f.Payload)
	if !ok || len(values) == 0 {
		return nil, "", false, "фильтр " + label + " не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	if inexactMode(mode) {
		return nil, "", false, "фильтр " + label + ": режим " + strings.TrimSpace(mode) + " (подстрока или regex) не переносится"
	}
	for _, v := range values {
		if len(v) > 1 && strings.HasPrefix(v, "/") && strings.HasSuffix(v, "/") {
			return nil, "", false, "фильтр " + label + ": регулярное выражение не переносится"
		}
	}
	return values, mode, approx, ""
}

// subIDFilter кладёт Sub ID источника в точное правило sub_ids.
//
// Параметры: name — ключ правила вида sub_id_N; f — фильтр.
// Результат: правила SubIDs с ключом sub_id_N. Подстрока и regex не переносятся.
// Ошибки: нет.
func subIDFilter(name string, f KFilter) (Rules, bool, bool, string) {
	values, mode, approx, why := exactValues(name, f)
	if why != "" {
		return Rules{}, false, false, why
	}
	values, dropped := keepShort(values, maxSubIDValueLen)
	if len(values) == 0 {
		return Rules{}, false, false, "фильтр " + name + ": все значения длиннее " + strconv.Itoa(maxSubIDValueLen) + " байт, не переносится"
	}
	note := ""
	if dropped > 0 {
		approx = true
		note = skippedValuesNote + strconv.Itoa(dropped) + " (длиннее " + strconv.Itoa(maxSubIDValueLen) + " байт), фильтр " + name
	}
	lf := listFlag(mode, values)
	return Rules{SubIDs: map[string]SubIDFilter{name: {Flag: lf.Flag, Values: lf.Values}}}, true, approx, note
}

// maxSubIDValueLen — наибольшая длина значения правила sub_ids в байтах; админка
// не сохраняет кампанию с более длинным значением.
const maxSubIDValueLen = 255

// skippedValuesNote начинает пояснение фильтра, из которого перенос отбросил часть значений.
// mapFilters пишет такое пояснение как потерю skipped, а не approximated.
const skippedValuesNote = "отброшены значения: "

// keepShort оставляет значения не длиннее limit байт.
//
// Параметры: values — значения фильтра; limit — наибольшая длина в байтах.
// Результат: оставшиеся значения в прежнем порядке и число отброшенных.
// Ошибки: нет.
func keepShort(values []string, limit int) ([]string, int) {
	kept := make([]string, 0, len(values))
	for _, v := range values {
		if len(v) <= limit {
			kept = append(kept, v)
		}
	}
	return kept, len(values) - len(kept)
}

// maxRuleSubID — наибольший номер Sub ID, который есть в правилах движка.
const maxRuleSubID = 12

// subIDNumber разбирает имя фильтра вида sub_id_N.
//
// Параметры: name — имя фильтра в нижнем регистре.
// Результат: номер N и true, если имя вида sub_id_<число без знака>.
// Ошибки: нет.
func subIDNumber(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "sub_id_")
	if !ok || rest == "" || strings.TrimLeft(rest, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil
}

// inexactMode сообщает, что режим фильтра сравнивает не точно.
//
// Параметры: mode — режим фильтра.
// Результат: true для contains, substring, regex и родственных названий.
// Ошибки: нет.
func inexactMode(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	for _, key := range []string{"contain", "substr", "regex", "regexp", "like", "pattern"} {
		if strings.Contains(m, key) {
			return true
		}
	}
	return false
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
