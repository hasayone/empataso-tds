package ktimport

import "encoding/json"

// Пути журналов Keitaro.
const (
	clicksLogPath      = "/admin_api/v1/clicks/log"
	conversionsLogPath = "/admin_api/v1/conversions/log"
)

// IDKeys — имена JSON-ключей, по которым версия Keitaro отдаёт идентификаторы событий.
// Это единственный источник имён: по нему читаются строки журнала, просятся колонки
// и ставятся голоса версии. Нулевое значение значит «версия неизвестна, ничего не читать».
type IDKeys struct {
	// Version — Keitaro10, Keitaro11 или пусто.
	Version string
	// Click — ключ идентификатора в журнале кликов.
	Click string
	// Conversion — ключ идентификатора в журнале конверсий.
	Conversion string
	// Type — ключ типа конверсии; в Keitaro 10 тип лежит в status, и ключ пуст.
	Type string
}

// idKeysByVersion — имена ключей каждой версии: 10 даёт click_id и conversion_id, 11 даёт event_id и event_type.
var idKeysByVersion = map[string]IDKeys{
	Keitaro10: {Version: Keitaro10, Click: "click_id", Conversion: "conversion_id"},
	Keitaro11: {Version: Keitaro11, Click: "event_id", Conversion: "event_id", Type: "event_type"},
}

// KeysFor возвращает имена ключей версии.
//
// Параметры: version — Keitaro10 или Keitaro11.
// Результат: ключи версии; для любой другой версии нулевое значение.
// Ошибки: нет.
func KeysFor(version string) IDKeys {
	return idKeysByVersion[version]
}

// KeysOfRow выбирает имена ключей по самой строке журнала, без версии.
//
// Параметры: raw — ключи JSON-объекта одной строки.
// Результат: ключи Keitaro 11, если в строке есть event_id или event_type и нет click_id и conversion_id;
// ключи Keitaro 10 при обратном; иначе нулевое значение: ключей нет или строка противоречит себе.
// Ошибки: нет.
func KeysOfRow(raw map[string]FlexString) IDKeys {
	has := func(v string) bool {
		k := idKeysByVersion[v]
		for _, name := range []string{k.Click, k.Conversion, k.Type} {
			if _, ok := raw[name]; name != "" && ok {
				return true
			}
		}
		return false
	}
	switch {
	case has(Keitaro11) && !has(Keitaro10):
		return idKeysByVersion[Keitaro11]
	case has(Keitaro10) && !has(Keitaro11):
		return idKeysByVersion[Keitaro10]
	}
	return IDKeys{}
}

// clickColumns — колонки идентификатора для журнала кликов; у неизвестной версии их нет.
func (k IDKeys) clickColumns() []string {
	if k.Click == "" {
		return nil
	}
	return []string{k.Click}
}

// conversionColumns — колонки идентификатора и типа для журнала конверсий; у неизвестной версии их нет.
func (k IDKeys) conversionColumns() []string {
	var out []string
	for _, name := range []string{k.Conversion, k.Type} {
		if name != "" && (len(out) == 0 || out[0] != name) {
			out = append(out, name)
		}
	}
	return out
}

// rawFields разбирает строку журнала в карту ключей один раз для всех читателей строки.
//
// Параметры: data — JSON одного объекта.
// Результат: карта ключ → значение; число становится текстом.
// Ошибки: тело не JSON-объект.
func rawFields(data []byte) (map[string]FlexString, error) {
	var raw map[string]FlexString
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}
