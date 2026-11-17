package ktimport

import (
	"context"
	"encoding/json"
	"strings"
)

// Версии Keitaro, которые умеет различать DetectVersion.
const (
	Keitaro10 = "10"
	Keitaro11 = "11"
)

// Имена признаков версии. Они уходят в ответ проверки соединения и в подпись интерфейса.
const (
	SignalSubID      = "subid"
	SignalClickID    = "click_id"
	SignalEventID    = "event_id"
	SignalConvID     = "conversion_id"
	SignalEventType  = "event_type"
	SignalRebill     = "rebill"
	SignalCustomType = "custom_type"
)

// versionSampleRows — сколько свежих конверсий читает проверка версии.
const versionSampleRows = 20

// builtinStatuses — типы конверсий Keitaro 10. Любой другой непустой статус — пользовательский тип.
var builtinStatuses = map[string]bool{
	"lead": true, "sale": true, "rejected": true, "registration": true,
	"deposit": true, "rebill": true, "trash": true,
}

// VersionVote — один голос признака за версию.
type VersionVote struct {
	Signal  string `json:"signal"`
	Version string `json:"version"`
}

// VersionVerdict — итог голосования.
// Version пуст, когда голосов нет или они противоречат. Conflict — true только при голосах за обе версии.
type VersionVerdict struct {
	Version  string
	Conflict bool
	Votes    []VersionVote
}

// DetectVersion считает версию Keitaro по голосам признаков.
//
// Параметры: votes — голоса; версия вне Keitaro10 и Keitaro11 или пустой признак игнорируются.
// Результат: версия, если все принятые голоса за одну версию. Нет голосов — пустая версия.
// Голоса за обе версии дают пустую версию и Conflict. Votes — принятые голоса в порядке подачи.
// Ошибки: нет.
func DetectVersion(votes []VersionVote) VersionVerdict {
	var out VersionVerdict
	seen := map[string]bool{}
	for _, v := range votes {
		if v.Signal == "" || (v.Version != Keitaro10 && v.Version != Keitaro11) {
			continue
		}
		out.Votes = append(out.Votes, v)
		seen[v.Version] = true
	}
	switch {
	case seen[Keitaro10] && seen[Keitaro11]:
		out.Conflict = true
	case seen[Keitaro11]:
		out.Version = Keitaro11
	case seen[Keitaro10]:
		out.Version = Keitaro10
	}
	return out
}

// SubIDVersion определяет версию по формату SubID клика.
//
// Параметры: subID — значение sub_id.
// Результат: Keitaro11 для новой записи из коротких буквенно-цифровых сегментов через точку (2k5a13p.ac.2m74),
// Keitaro10 для старой одной буквенно-цифровой строки без точки (1vepsc2qv8h), иначе пустая строка.
// Ошибки: нет.
func SubIDVersion(subID string) string {
	if subID == "" {
		return ""
	}
	parts := strings.Split(subID, ".")
	for _, p := range parts {
		if p == "" || len(p) > 12 || !alnum(p) {
			return ""
		}
	}
	if len(parts) == 1 {
		return Keitaro10
	}
	return Keitaro11
}

// alnum сообщает, что строка состоит только из латинских букв и цифр.
//
// Параметры: s — строка.
// Результат: true для непустой строки без других символов.
// Ошибки: нет.
func alnum(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return s != ""
}

// StatusVotes превращает типы конверсий свежих записей в голоса.
//
// Параметры: statuses — значения status; регистр и пробелы не важны.
// Результат: голос rebill за Keitaro 10, если встретился rebill; голос custom_type за Keitaro 11,
// если встретился непустой статус вне встроенных типов. Каждый признак — не больше одного голоса.
// Ошибки: нет.
func StatusVotes(statuses []string) []VersionVote {
	var rebill, custom bool
	for _, s := range statuses {
		s = strings.ToLower(strings.TrimSpace(s))
		switch {
		case s == "rebill":
			rebill = true
		case s != "" && !builtinStatuses[s]:
			custom = true
		}
	}
	var out []VersionVote
	if rebill {
		out = append(out, VersionVote{Signal: SignalRebill, Version: Keitaro10})
	}
	if custom {
		out = append(out, VersionVote{Signal: SignalCustomType, Version: Keitaro11})
	}
	return out
}

// ProbeVersion собирает голоса версии из журналов кликов и конверсий.
//
// Запросы только читают: POST /admin_api/v1/clicks/log и /admin_api/v1/conversions/log с limit не больше
// versionSampleRows, как у переноса; другие пути и методы Allow и AllowLog отклоняют. Журнал ходит
// по одному запросу за раз. Колонки идентификаторов (click_id, event_id, conversion_id, event_type)
// просятся каждая отдельным запросом: ответ не 200 или пустой список строк голоса не даёт.
//
// Параметры: ctx — отмена; from и to — границы range в UTC, "2006-01-02 15:04:05".
// Результат: голоса признаков. Журнал без строк или с ошибкой голосов не даёт.
// Ошибки: нет, сбой любого запроса просто оставляет признак без голоса.
func (c *Client) ProbeVersion(ctx context.Context, from, to string) []VersionVote {
	var votes []VersionVote
	const clicks, convs = clicksLogPath, conversionsLogPath
	if rows := c.sampleRows(ctx, clicks, from, to, []string{"sub_id", "datetime"}, "datetime", 1); len(rows) > 0 {
		var head struct {
			SubID FlexString `json:"sub_id"`
		}
		if json.Unmarshal(rows[0], &head) == nil {
			if v := SubIDVersion(string(head.SubID)); v != "" {
				votes = append(votes, VersionVote{Signal: SignalSubID, Version: v})
			}
		}
	}
	if rows := c.sampleRows(ctx, convs, from, to, []string{"status", "postback_datetime"}, "postback_datetime", versionSampleRows); len(rows) > 0 {
		statuses := make([]string, 0, len(rows))
		for _, raw := range rows {
			var head struct {
				Status FlexString `json:"status"`
			}
			if json.Unmarshal(raw, &head) == nil {
				statuses = append(statuses, string(head.Status))
			}
		}
		votes = append(votes, StatusVotes(statuses)...)
	}
	k10, k11 := KeysFor(Keitaro10), KeysFor(Keitaro11)
	for _, p := range []struct{ path, sort, col, signal, version string }{
		{clicks, "datetime", k10.Click, SignalClickID, Keitaro10},
		{clicks, "datetime", k11.Click, SignalEventID, Keitaro11},
		{convs, "postback_datetime", k10.Conversion, SignalConvID, Keitaro10},
		{convs, "postback_datetime", k11.Type, SignalEventType, Keitaro11},
	} {
		if c.columnAccepted(ctx, p.path, from, to, p.col, p.sort) {
			votes = append(votes, VersionVote{Signal: p.signal, Version: p.version})
		}
	}
	return votes
}

// columnAccepted сообщает, что журнал принял колонку и вернул строку с этим ключом.
//
// Параметры: ctx — отмена; path — журнал; from и to — границы; col — колонка; sort — колонка сортировки.
// Результат: true, если ответ 200 и в первой строке есть ключ col, пусть и пустой.
// Ошибки: нет, любой сбой даёт false.
func (c *Client) columnAccepted(ctx context.Context, path, from, to, col, sort string) bool {
	rows := c.sampleRows(ctx, path, from, to, []string{col}, sort, 1)
	if len(rows) == 0 {
		return false
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(rows[0], &keys) != nil {
		return false
	}
	_, ok := keys[col]
	return ok
}

// sampleRows читает одну страницу журнала от новых записей к старым.
//
// Параметры: ctx — отмена; path — журнал; from и to — границы; columns — колонки; sortBy — сортировка по убыванию;
// limit — размер страницы.
// Результат: строки страницы. Ошибка сети, статус не 200 или тело не JSON дают nil.
// Ошибки: нет.
func (c *Client) sampleRows(ctx context.Context, path, from, to string, columns []string, sortBy string, limit int) []json.RawMessage {
	payload := map[string]any{
		"range":   map[string]string{"from": from, "to": to, "timezone": "UTC"},
		"limit":   limit,
		"offset":  0,
		"columns": columns,
		"sort":    []map[string]string{{"name": sortBy, "order": "desc"}},
	}
	raw, err := c.postLog(ctx, path, payload)
	if err != nil {
		return nil
	}
	var box struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if json.Unmarshal(raw, &box) != nil {
		return nil
	}
	return box.Rows
}
