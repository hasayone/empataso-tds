package ktimport

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// localAction определяет действие потока, которое исполняет код на трекере.
//
// Параметры: action — action_type.
// Результат: true для local и php.
// Ошибки: нет.
func localAction(action string) bool {
	a := strings.ToLower(action)
	return strings.Contains(a, "local") || strings.Contains(a, "php")
}

// redirectOf переводит action_type Keitaro в тип редиректа emtds.
//
// Параметры: action — action_type; второй аргумент — схема потока, на выбор типа не влияет.
// Результат: тип из render.Do и текст приближения. Пустой текст значит точное соответствие.
// Ошибки: нет. Тип без пары становится stop, поток при этом не выключается.
func redirectOf(action, _ string) (string, string) {
	a := strings.ToLower(strings.TrimSpace(action))
	switch a {
	case "", "http", "redirect", "http_redirect", "location":
		return "http_redirect", ""
	case "meta", "meta_refresh":
		return "meta_refresh", ""
	case "js", "js_redirect":
		return "js_redirect", ""
	case "iframe", "frame", "iframe_redirect":
		return "iframe_redirect", ""
	case "html", "show_html", "show_page_html":
		return "show_page_html", ""
	case "text", "show_text":
		return "show_text", ""
	case "javascript", "js_selection_raw", "js_selection", "iframe_selection", "api", "show_out", "under_construction":
		return a, ""
	case "403", "status403", "403_forbidden":
		return "403_forbidden", ""
	case "404", "status404", "404_not_found":
		return "404_not_found", ""
	case "500", "status500", "500_server_error":
		return "500_server_error", ""
	case "do_nothing", "nothing", "blank", "stop":
		return "stop", ""
	default:
		return "stop", "action_type " + strings.TrimSpace(action) + " нет соответствия, поток остановлен"
	}
}

// carriesOut сообщает, что тип редиректа показывает поле out.
//
// Параметры: redirect — тип emtds.
// Результат: true, если движок берёт адрес или текст из out.
// Ошибки: нет.
func carriesOut(redirect string) bool {
	switch redirect {
	case "http_redirect", "meta_refresh", "js_redirect", "iframe_redirect",
		"show_text", "show_page_html", "show_html_404", "javascript", "js_selection_raw",
		"js_selection", "iframe_selection", "api", "show_out", "group":
		return true
	default:
		return false
	}
}

// status404Action сообщает, что действие Keitaro — страница 404.
//
// Параметры: action — action_type.
// Результат: true для 404, status404 и 404_not_found.
// Ошибки: нет.
func status404Action(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "404", "status404", "404_not_found":
		return true
	default:
		return false
	}
}

// campaignAction переводит действие campaign в редирект на группу.
//
// Параметры: payload — id целевой кампании; aliases — id кампании → id группы.
// Результат: тип редиректа, out и текст приближения. Пустой текст значит, что цель найдена.
// Ошибки: нет. Пустой id даёт stop и «целевая кампания не задана». Неизвестный id даёт stop и «целевая кампания N не найдена».
func campaignAction(payload string, aliases map[int]string) (string, string, string) {
	raw := strings.TrimSpace(payload)
	if raw == "" {
		return "stop", "", "целевая кампания не задана"
	}
	n, err := strconv.Atoi(raw)
	label := raw
	if err == nil {
		label = strconv.Itoa(n)
	}
	if err != nil || n <= 0 || strings.TrimSpace(aliases[n]) == "" {
		return "stop", "", "целевая кампания " + label + " не найдена"
	}
	return "group", aliases[n], ""
}

// GroupIDs возвращает id группы для каждой кампании так же, как mapCampaigns.
//
// Параметры: camps — кампании снимка или списка GET /admin_api/v1/campaigns.
// Результат: id кампании Keitaro → id группы emtds.
// Ошибки: нет.
func GroupIDs(camps []Campaign) map[int]string {
	return campaignAliases(camps)
}

// campaignAliases повторяет id группы, который назначает mapCampaign.
//
// Параметры: camps — кампании снимка.
// Результат: id кампании → id группы. Порядок и суффиксы dup совпадают с обходом кампаний.
// Ошибки: нет.
func campaignAliases(camps []Campaign) map[int]string {
	rows := append([]Campaign(nil), camps...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Alias == rows[j].Alias {
			return int(rows[i].ID) < int(rows[j].ID)
		}
		return rows[i].Alias < rows[j].Alias
	})
	seen := map[string]bool{}
	out := make(map[int]string, len(rows))
	for _, camp := range rows {
		id, _, _ := assignedID(camp.Alias, int(camp.ID), seen)
		out[int(camp.ID)] = id
	}
	return out
}

// assignedID выдаёт id группы так же, как mapCampaign.
//
// Параметры: alias — alias кампании; n — id кампании; seen — уже выданные id групп.
// Результат: id, true если alias пуст, true если исходный id уже занят. seen пополняется итоговым id.
// Ошибки: нет.
func assignedID(alias string, n int, seen map[string]bool) (string, bool, bool) {
	id := strings.TrimSpace(alias)
	empty := false
	if id == "" {
		empty = true
		id = fmt.Sprintf("missing-alias-%d", n)
	}
	dup := false
	if seen[id] {
		dup = true
		id = fmt.Sprintf("%s-dup-%d", id, n)
	}
	if seen != nil {
		seen[id] = true
	}
	return id, empty, dup
}
