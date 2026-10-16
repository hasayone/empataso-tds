// Package ktimport переносит конфигурацию Keitaro в группы и каталоги kuztds.
// Журналы кликов и конверсий читаются только двумя POST-путями.
// Архив локального лендинга читается GET /admin_api/v1/landing_pages/{id}/download.
package ktimport

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Allow проверяет, что запрос к Keitaro — это GET по известному пути.
//
// Параметры: method — HTTP-метод; rawPath — путь, можно с query.
// Результат: nil, если путь в allowlist и метод GET.
// Ошибки: любой другой метод или путь, включая отчёты и журналы кликов.
func Allow(method, rawPath string) error {
	if !strings.EqualFold(method, "GET") {
		return fmt.Errorf("keitaro: разрешён только GET, получен %s", strings.ToUpper(method))
	}
	path := cleanPath(rawPath)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if !match(parts) {
		return fmt.Errorf("keitaro: путь вне allowlist: %s", path)
	}
	return nil
}

// AllowLog проверяет POST только к двум журналам Keitaro.
//
// Параметры: method — HTTP-метод; rawPath — путь, можно с query.
// Результат: nil для POST /admin_api/v1/clicks/log и POST /admin_api/v1/conversions/log.
// Ошибки: другой метод или любой другой путь.
func AllowLog(method, rawPath string) error {
	if !strings.EqualFold(method, "POST") {
		return fmt.Errorf("keitaro: для журнала разрешён только POST, получен %s", strings.ToUpper(method))
	}
	path := cleanPath(rawPath)
	switch path {
	case "/admin_api/v1/clicks/log", "/admin_api/v1/conversions/log":
		return nil
	default:
		return fmt.Errorf("keitaro: путь вне allowlist: %s", path)
	}
}

// cleanPath отрезает query и завершающий слэш.
//
// Параметры: rawPath — путь как в запросе.
// Результат: путь без query. Пустая строка становится корнем.
// Ошибки: нет.
func cleanPath(rawPath string) string {
	path := rawPath
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		path = "/"
	}
	return path
}

// match сравнивает сегменты пути с allowlist Admin API v1.
//
// Параметры: parts — сегменты пути без пустых краёв.
// Результат: true, если путь разрешён.
// Ошибки: нет.
func match(parts []string) bool {
	if len(parts) < 3 || parts[0] != "admin_api" || parts[1] != "v1" {
		return false
	}
	rest := parts[2:]
	switch {
	case eq(rest, "campaigns"):
		return true
	case len(rest) == 2 && rest[0] == "campaigns" && id(rest[1]):
		return true
	case len(rest) == 3 && rest[0] == "campaigns" && id(rest[1]) && rest[2] == "streams":
		return true
	case len(rest) == 2 && rest[0] == "streams" && id(rest[1]):
		return true
	case listOrOne(rest, "offers"), listOrOne(rest, "landing_pages"):
		return true
	case len(rest) == 3 && rest[0] == "landing_pages" && id(rest[1]) && rest[2] == "download":
		return true
	case listOrOne(rest, "affiliate_networks"), listOrOne(rest, "traffic_sources"), listOrOne(rest, "domains"):
		return true
	case eq(rest, "groups"):
		return true
	case eq(rest, "stream_schemas"), eq(rest, "stream_types"), eq(rest, "stream_filters"), eq(rest, "streams_actions"):
		return true
	default:
		return false
	}
}

// listOrOne разрешает список ресурса и один объект по id.
//
// Параметры: rest — хвост после /admin_api/v1; name — имя ресурса.
// Результат: true для /name и /name/{id}.
// Ошибки: нет.
func listOrOne(rest []string, name string) bool {
	if eq(rest, name) {
		return true
	}
	return len(rest) == 2 && rest[0] == name && id(rest[1])
}

// eq сравнивает путь с одним сегментом ресурса.
//
// Параметры: rest — хвост после /admin_api/v1; name — имя ресурса.
// Результат: true при точном совпадении одного сегмента.
// Ошибки: нет.
func eq(rest []string, name string) bool {
	return len(rest) == 1 && rest[0] == name
}

// id проверяет, что сегмент — положительное целое.
//
// Параметры: s — сегмент пути.
// Результат: true, если это id без нуля.
// Ошибки: нет.
func id(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// MaskURL оставляет в отчёте хост и путь, а значения query заменяет.
//
// Параметры: raw — исходный URL.
// Результат: URL без фрагмента и без значений query.
// Ошибки: нет. Неразборённая строка возвращается как «[unparsed]».
func MaskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" && u.Path == raw {
		return "[unparsed]"
	}
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"=***")
	}
	u.RawQuery = strings.Join(parts, "&")
	u.Fragment = ""
	return u.String()
}
