package ktimport

import (
	"strconv"
	"strings"
)

// subIDFilter кладёт Sub ID источника в правило sub_ids: точное или «содержит».
//
// Режим подстроки переносится как «содержит». Регулярное выражение (значение вида /…/ или режим
// regex) переносится как «содержит» только когда оно по сути список литералов, см. regexLiterals.
// Любое другое выражение пропускается: движок не исполняет регулярки на клике.
//
// Параметры: name — ключ правила вида sub_id_N; f — фильтр.
// Результат: правила SubIDs с ключом sub_id_N, признак переноса, признак приближения и пояснение.
// Ошибки: нет. Пустой список значений и слишком сложная регулярка дают отказ с причиной.
func subIDFilter(name string, f KFilter) (Rules, bool, bool, string) {
	if emptyPayload(f.Payload) {
		return Rules{}, false, false, "фильтр " + name + ": пустой список значений не переносится"
	}
	values, mode, approx, ok := readPayload(f.Payload)
	if !ok || len(values) == 0 {
		return Rules{}, false, false, "фильтр " + name + " не разобран"
	}
	if mode == "" {
		mode = f.Mode
	}
	kind := subIDKind(mode, values)
	reject := isReject(mode) || isReject(f.Mode) || negatedMode(mode)
	note := ""
	switch kind {
	case subIDRegex:
		var exact bool
		values, exact, ok = regexValues(values)
		if !ok {
			return Rules{}, false, false, "фильтр " + name + ": регулярное выражение слишком сложное для «содержит», не переносится"
		}
		if !exact {
			approx, note = true, "фильтр "+name+": регулярка перенесена как «содержит» без учёта регистра"
		}
	case subIDContains:
		values = cleanValues(values)
	}
	values, dropped := keepShort(values, maxSubIDValueLen)
	if len(values) == 0 {
		return Rules{}, false, false, "фильтр " + name + ": все значения длиннее " + strconv.Itoa(maxSubIDValueLen) + " байт, не переносится"
	}
	if dropped > 0 {
		approx = true
		note = skippedValuesNote + strconv.Itoa(dropped) + " (длиннее " + strconv.Itoa(maxSubIDValueLen) + " байт), фильтр " + name
	}
	lf := listFlag(mode, values)
	if kind != subIDExact {
		lf.Flag = 2
		if reject {
			lf.Flag = 1
		}
	}
	return Rules{SubIDs: map[string]SubIDFilter{name: {Flag: lf.Flag, Values: lf.Values, Contains: kind != subIDExact}}}, true, approx, note
}

// Виды сравнения правила Sub ID при переносе.
const (
	subIDExact = iota
	subIDContains
	subIDRegex
)

// subIDKind определяет, как Keitaro сравнивает значения фильтра Sub ID.
//
// Параметры: mode — режим фильтра; values — значения.
// Результат: subIDRegex для режима regex и значений вида /…/, subIDContains для режима подстроки,
// иначе subIDExact.
// Ошибки: нет.
func subIDKind(mode string, values []string) int {
	m := strings.ToLower(strings.TrimSpace(mode))
	for _, key := range []string{"regex", "regexp", "pattern"} {
		if strings.Contains(m, key) {
			return subIDRegex
		}
	}
	for _, v := range values {
		if _, _, ok := splitSlashRegex(v); ok {
			return subIDRegex
		}
	}
	if inexactMode(m) {
		return subIDContains
	}
	return subIDExact
}

// negatedMode сообщает, что режим подстроки отрицательный: not_contains, does not contain.
//
// Параметры: mode — режим фильтра.
// Результат: true, если в названии режима есть not или его начинает «!».
// Ошибки: нет.
func negatedMode(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return strings.HasPrefix(m, "!") || strings.Contains(m, "not")
}

// splitSlashRegex разбирает значение вида /тело/флаги.
//
// Параметры: v — значение фильтра.
// Результат: тело, флаги и true, если значение начинается с «/» и после последней «/» стоят
// только латинские буквы или ничего.
// Ошибки: нет.
func splitSlashRegex(v string) (string, string, bool) {
	v = strings.TrimSpace(v)
	end := strings.LastIndexByte(v, '/')
	if len(v) < 2 || v[0] != '/' || end < 1 {
		return "", "", false
	}
	flags := v[end+1:]
	if strings.Trim(flags, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return "", "", false
	}
	return v[1:end], flags, true
}

// regexValues переводит регулярки в литералы для «содержит».
//
// Параметры: values — значения фильтра: /тело/флаги или голый шаблон.
// Результат: литералы без повторов, true если все регистронезависимы (флаг i), и true, если каждая
// регулярка разобрана. При отказе литералов нет.
// Ошибки: нет.
func regexValues(values []string) ([]string, bool, bool) {
	var out []string
	insensitive := true
	for _, v := range values {
		body, flags := strings.TrimSpace(v), ""
		if b, fl, ok := splitSlashRegex(v); ok {
			body, flags = b, fl
		}
		if flags != "" && flags != "i" {
			return nil, false, false
		}
		lits, ok := regexLiterals(body)
		if !ok {
			return nil, false, false
		}
		insensitive = insensitive && flags == "i"
		out = append(out, lits...)
	}
	return unique(out), insensitive, len(out) > 0
}

// regexLiterals разбирает шаблон, который состоит из литералов через |.
//
// Параметры: body — тело регулярки без разделителей.
// Результат: альтернативы как литералы и true. Разрешены обычные символы и экранированные знаки
// препинания (\. \- \/). Классы, квантификаторы, якоря, группы, \d и подобное, а также пустая
// альтернатива дают false.
// Ошибки: нет.
func regexLiterals(body string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	flush := func() bool {
		if cur.Len() == 0 {
			return false
		}
		out = append(out, cur.String())
		cur.Reset()
		return true
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '|':
			if !flush() {
				return nil, false
			}
		case c == '\\':
			i++
			if i >= len(body) || isWordByte(body[i]) || body[i] >= 0x80 {
				return nil, false
			}
			cur.WriteByte(body[i])
		case strings.IndexByte(".*+?^$()[]{}", c) >= 0:
			return nil, false
		default:
			cur.WriteByte(c)
		}
	}
	if !flush() {
		return nil, false
	}
	return out, true
}

// isWordByte сообщает, что байт — латинская буква, цифра или «_»: после «\» это класс, а не знак.
//
// Параметры: c — байт.
// Результат: true для [A-Za-z0-9_].
// Ошибки: нет.
func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
