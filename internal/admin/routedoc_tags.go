package admin

import "strings"

// docTag is one section of the API docs: an OpenAPI tag with its names in the
// three panel languages and the path prefixes whose routes it holds.
type docTag struct {
	Name     string
	EN       string
	RU       string
	UK       string
	Prefixes []string
}

// docTags lists the API docs sections in the order the docs page shows them.
// Every documented route falls under exactly one prefix of one tag.
var docTags = []docTag{
	{"general", "General", "Общее", "Загальне",
		[]string{"/api/health", "/healthz", "/api/brand", "/api/openapi.json", "/api/postman.json", "/api/ui", "/api/notices",
			"/docs", "/api/docs", "/api/docs.md", "/llms.txt"}},
	{"campaigns", "Campaigns", "Кампании", "Кампанії",
		[]string{"/api/groups", "/api/campaigns", "/api/s2s", "/api/spend"}},
	{"offers", "Offers", "Офферы", "Офери", []string{"/api/offers"}},
	{"landings", "Landings", "Лендинги", "Лендінги", []string{"/api/landings"}},
	{"networks", "Affiliate networks", "Партнёрские сети", "Партнерські мережі", []string{"/api/affiliate-networks"}},
	{"sources", "Traffic sources", "Источники трафика", "Джерела трафіку", []string{"/api/traffic-sources"}},
	{"domains", "Domains", "Домены", "Домени", []string{"/api/domains", "/api/registrars", "/api/cloudflare"}},
	{"stats", "Statistics and reports", "Статистика и отчёты", "Статистика і звіти", []string{"/api/stats", "/api/report"}},
	{"clicks", "Clicks", "Клики", "Кліки", []string{"/api/logs", "/api/log-fields", "/api/log-values"}},
	{"conversions", "Conversions and postbacks", "Конверсии и постбэки", "Конверсії та постбеки",
		[]string{"/api/postbacks", "/api/postback-log"}},
	{"keywords", "Keywords", "Ключевые слова", "Ключові слова", []string{"/api/keys", "/api/keywords"}},
	{"boards", "Boards", "Доски", "Дошки", []string{"/api/boards"}},
	{"tools", "Tools and lists", "Инструменты и списки", "Інструменти та списки",
		[]string{"/api/lookup", "/api/simulate", "/api/lists"}},
	{"site", "Pages and bot protection", "Страницы и защита от ботов", "Сторінки та захист від ботів",
		[]string{"/api/pages", "/api/bot-protection"}},
	{"automation", "Automation (cron)", "Автоматизация (cron)", "Автоматизація (cron)", []string{"/api/cron"}},
	{"checker", "Checker", "Чекер", "Чекер", []string{"/api/checker"}},
	{"keitaro", "Keitaro transfer", "Перенос из Keitaro", "Перенесення з Keitaro", []string{"/api/keitaro"}},
	{"updates", "Version and updates", "Версия и обновления", "Версія та оновлення", []string{"/api/version", "/api/update"}},
	{"ai", "AI", "AI-ассистент", "AI-асистент", []string{"/api/ai"}},
	{"accounts", "Sign-in, users, roles and keys", "Вход, пользователи, роли и ключи", "Вхід, користувачі, ролі та ключі",
		[]string{"/api/me", "/api/password", "/api/logout", "/api/login", "/api/setup", "/api/recover",
			"/api/users", "/api/roles", "/api/api-keys"}},
}

// routeTag returns the name of the docTags section that holds muxPath, matching
// whole path segments: /api/keys holds /api/keys/download but not /api/keywords.
// It returns "" when no section holds the path.
func routeTag(muxPath string) string {
	for _, t := range docTags {
		for _, p := range t.Prefixes {
			if muxPath == p || strings.HasPrefix(muxPath, p+"/") {
				return t.Name
			}
		}
	}
	return ""
}

// openAPITags returns the top-level tags list of the OpenAPI document in docTags order,
// each with its Russian and Ukrainian names in x-emtds-i18n.
func openAPITags() []any {
	out := make([]any, 0, len(docTags))
	for _, t := range docTags {
		out = append(out, map[string]any{
			"name": t.Name, "description": t.EN,
			i18nExtension: map[string]any{"ru": t.RU, "uk": t.UK},
		})
	}
	return out
}
