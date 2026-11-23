package ktimport

import (
	"sort"
	"strings"
)

// maxParamBytes — предел имени и токена параметра в байтах, как у админки.
const maxParamBytes = 255

// linkQueryName — имя параметра во входящей ссылке клика.
//
// Параметры: canon — наше поле (keyword, cost, currency, дополнительное поле или sub_id_N); p — поле Keitaro.
// Результат: alias, иначе имя внутри макроса {…}, иначе canon, когда placeholder — обычный текст,
// иначе подпись, если она сама является ключом query и не совпадает с canon.
// Пустая строка значит, что отдельного имени нет: движок читает поле под своим именем.
// Обычный текст placeholder именем не становится: это токен, а не ключ query.
// Ошибки: нет.
func linkQueryName(canon string, p KParam) string {
	if v := queryKey(stripBraces(p.Alias)); v != "" {
		return v
	}
	ph := strings.TrimSpace(p.Placeholder)
	if macroPlaceholder(ph) {
		if v := queryKey(stripBraces(ph)); v != "" {
			return v
		}
	} else if ph != "" {
		return canon
	}
	if v := queryKey(p.Name); v != "" && v != canon {
		return v
	}
	return ""
}

// macroPlaceholder сообщает, что строка — один макрос {имя}.
//
// Параметры: raw — placeholder Keitaro.
// Результат: true, когда по краям скобки, а внутри непустой ключ query.
// Ошибки: нет.
func macroPlaceholder(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) < 3 || !strings.HasPrefix(raw, "{") || !strings.HasSuffix(raw, "}") {
		return false
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	return queryKey(inner) != ""
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

// CampaignFields — параметры одной кампании emtds, снятые с Keitaro.
// Parameters — имя в ссылке. Tokens — макрос или текст placeholder как есть, без раскрытия.
// CostParam и CurrencyParam — имена цены и валюты. CostToken и CurrencyToken — их placeholder.
// Skipped — ключи, чей токен длиннее maxParamBytes и поэтому не записан.
type CampaignFields struct {
	Parameters    map[string]string
	Tokens        map[string]string
	CostParam     string
	CostToken     string
	CurrencyParam string
	CurrencyToken string
	Skipped       []string
}

// CampaignFieldsOf собирает параметры кампании: источник снизу, кампания сверху.
//
// Параметры: camp — кампания Keitaro; src — её источник. Нулевой источник ничего не добавляет.
// Результат: поля группы и текст потери. Текст пуст, когда parameters пусты или разобраны.
// Ошибки: нет. Битый JSON даёт причину пропуска; поля источника при этом остаются.
func CampaignFieldsOf(camp Campaign, src KSource) (CampaignFields, string) {
	over, bad := campaignParams(camp.Parameters)
	fields := fieldsFromParams(src.Parameters, over)
	return fields, bad
}

// fieldsFromParams раскладывает параметры Keitaro по полям группы.
//
// Параметры: source — параметры источника; camp — параметры кампании. Пустая карта ничего не добавляет.
// Непустое поле кампании заменяет то же поле источника. Токен — placeholder без изменений.
// Результат: имена, токены, цена и валюта. Ключ source не пишется: у кампании такого поля нет.
// Ошибки: нет. Токен длиннее maxParamBytes пропускается и попадает в Skipped.
func fieldsFromParams(source, camp map[string]KParam) CampaignFields {
	merged := layeredParams(source, camp)
	if len(merged) == 0 {
		return CampaignFields{}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	names := map[string]string{}
	tokens := map[string]string{}
	var set CampaignFields
	for _, key := range keys {
		p := merged[key]
		low := strings.ToLower(strings.TrimSpace(key))
		token, tooLong := clipToken(p.Placeholder)
		if tooLong {
			set.Skipped = append(set.Skipped, low)
		}
		switch low {
		case "cost":
			set.CostParam = linkQueryName("cost", p)
			set.CostToken = token
		case "currency":
			set.CurrencyParam = linkQueryName("currency", p)
			set.CurrencyToken = token
		case "source":
			continue
		default:
			canon, ok := canonParam(key)
			if !ok {
				continue
			}
			if name := linkQueryName(canon, p); name != "" {
				names[canon] = name
			}
			if token != "" {
				tokens[canon] = token
			}
		}
	}
	if len(names) > 0 {
		set.Parameters = names
	}
	if len(tokens) > 0 {
		set.Tokens = tokens
	}
	return set
}

// layeredParams кладёт параметры кампании поверх параметров источника.
//
// Параметры: source — поля источника; camp — поля кампании. nil значит пустую карту.
// Результат: копия. Непустые alias, placeholder и name кампании заменяют поля источника.
// Ошибки: нет. Исходные карты не меняются.
func layeredParams(source, camp map[string]KParam) map[string]KParam {
	if len(source) == 0 && len(camp) == 0 {
		return nil
	}
	out := make(map[string]KParam, len(source)+len(camp))
	for key, val := range source {
		out[key] = val
	}
	for key, val := range camp {
		prev := out[key]
		if strings.TrimSpace(val.Alias) != "" {
			prev.Alias = val.Alias
		}
		if strings.TrimSpace(val.Placeholder) != "" {
			prev.Placeholder = val.Placeholder
		}
		if strings.TrimSpace(val.Name) != "" {
			prev.Name = val.Name
		}
		out[key] = prev
	}
	return out
}

// clipToken обрезает placeholder до токена, который можно сохранить.
//
// Параметры: raw — placeholder Keitaro.
// Результат: текст без краевых пробелов и true, когда он длиннее maxParamBytes и отброшен.
// Ошибки: нет. Скобки не добавляются и не снимаются.
func clipToken(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) > maxParamBytes {
		return "", true
	}
	return raw, false
}
