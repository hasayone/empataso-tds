package admin

// trafficLogBody is the JSON Schema of the PUT /api/campaigns/{id}/traffic-log request.
var trafficLogBody = map[string]any{
	"type":     "object",
	"required": []any{"enabled"},
	"properties": map[string]any{
		"enabled": map[string]any{"type": "boolean", "description": "true records the click path of every click of the campaign, false stops recording."},
	},
}

// trafficLogInfoSample is the state part shared by the traffic-log samples.
var trafficLogInfoSample = map[string]any{
	"available": true, "enabled": true, "max": 500, "ttl_hours": 168, "click_ttl_hours": 72,
}

// trafficLogTraceSample is a made-up click path: one campaign, two streams tried, the second one served.
var trafficLogTraceSample = map[string]any{
	"v": 1, "ts": "2026-01-15T10:20:30Z", "cid": "k9d2x1", "campaign": "spring-sale", "name": "Spring sale",
	"url": "https://tds.example.com/spring-sale?sub_id_1=abc&api=[hidden]",
	"headers": []any{
		map[string]any{"name": "x-forwarded-for", "value": "203.0.113.7"},
		map[string]any{"name": "cf-connecting-ip", "value": "203.0.113.7"},
		map[string]any{"name": "x-real-ip"},
	},
	"trusted": true, "ip": "203.0.113.7",
	"hops": []any{map[string]any{
		"group": "spring-sale", "selected": "Mobile",
		"streams": []any{
			map[string]any{"name": "Desktop", "match": false, "why": []any{"device", "os"}},
			map[string]any{"name": "Mobile", "match": true},
		},
	}},
	"stream": "Mobile", "action": "http_redirect", "out": "https://offers.example.com/spring?clickid=k9d2x1",
	"offer_id": "12", "saved": true, "dur_us": 840,
	"visitor": map[string]any{
		"country": "de", "city": "Berlin", "asn": 64500, "org": "Example Net", "device": "phone", "os": "Android",
		"os_version": "14", "browser": "Chrome", "browser_ver": "120", "lang": "de", "unique": true,
		"useragent": "Mozilla/5.0 (Linux; Android 14) Chrome/120.0.0.0 Mobile Safari/537.36",
		"subs":      map[string]any{"sub_id_1": "abc"},
	},
}

// trafficLogRouteDocs documents the campaign traffic log routes: the latest
// click paths, the path of one click and the switch that records them.
func trafficLogRouteDocs() map[string]routeDoc {
	listSample := map[string]any{}
	for k, v := range trafficLogInfoSample {
		listSample[k] = v
	}
	listSample["total"] = 1
	listSample["entries"] = []any{trafficLogTraceSample}
	clickSample := map[string]any{}
	for k, v := range trafficLogInfoSample {
		clickSample[k] = v
	}
	clickSample["entries"] = []any{trafficLogTraceSample}
	return map[string]routeDoc{
		"GET /api/campaigns/{id}/traffic-log": {
			ID: "get_campaign_traffic_log", Summary: "Get the latest click paths of a campaign.", MCP: mcpRead,
			Description: "Returns the click paths the engine recorded for the campaign, newest first: the request, the headers that could name the address, " +
				"every stream tried with the rules that turned the visitor away, the stream that served, what the visitor got and what the engine knew about them. " +
				"The log keeps the latest clicks only and is stored in Redis. Without Redis the answer has available false and no entries.",
			RU: docText{
				Summary: "Получить последние пути кликов кампании.",
				Description: "Возвращает пути кликов, которые движок записал по кампании, новые сверху: запрос, заголовки с возможным адресом, " +
					"каждый проверенный поток с правилами, не пропустившими посетителя, поток, который отработал, что получил посетитель и что движок о нём знал. " +
					"Лог хранит только последние клики и лежит в Redis. Без Redis в ответе available false и нет записей.",
			},
			UK: docText{
				Summary: "Отримати останні шляхи кліків кампанії.",
				Description: "Повертає шляхи кліків, які рушій записав по кампанії, нові зверху: запит, заголовки з можливою адресою, " +
					"кожен перевірений потік із правилами, що не пропустили відвідувача, потік, який відпрацював, що отримав відвідувач і що рушій про нього знав. " +
					"Лог зберігає лише останні кліки й лежить у Redis. Без Redis у відповіді available false і немає записів.",
			},
			Query: []queryParam{
				strQ("q", "Words to find, all of them must occur in the click: address, campaign, stream, country, device, sub id, click id.").
					loc("Слова для поиска, все должны встретиться в клике: адрес, кампания, поток, страна, устройство, sub id, id клика.",
						"Слова для пошуку, усі мають зустрітися в кліку: адреса, кампанія, потік, країна, пристрій, sub id, id кліку."),
				intQ("limit", "How many entries to return, 100 by default, at most what the log keeps.").
					loc("Сколько записей вернуть, по умолчанию 100, не больше, чем хранит лог.",
						"Скільки записів повернути, типово 100, не більше, ніж зберігає лог."),
			},
			Response: listSample,
			Errors:   []int{404, 500, 503},
		},
		"GET /api/campaigns/{id}/traffic-log/{cid}": {
			ID: "get_click_path", Summary: "Get the stored path of one click.", MCP: mcpRead,
			Description: "Returns the paths recorded for one click id, in the order they were taken: the landing step and, when it happened, the offer step. " +
				"A click older than the click lifetime, or one of another campaign, answers an empty list.",
			RU: docText{
				Summary: "Получить сохранённый путь одного клика.",
				Description: "Возвращает пути, записанные по одному id клика, в порядке прохождения: шаг лендинга и, если он был, шаг оффера. " +
					"Клик старше срока хранения или чужой кампании даёт пустой список.",
			},
			UK: docText{
				Summary: "Отримати збережений шлях одного кліку.",
				Description: "Повертає шляхи, записані по одному id кліку, у порядку проходження: крок лендингу і, якщо він був, крок оферу. " +
					"Клік старший за термін зберігання або чужої кампанії дає порожній список.",
			},
			Response: clickSample,
			Errors:   []int{404, 500, 503},
		},
		"PUT /api/campaigns/{id}/traffic-log": {
			ID: "set_campaign_traffic_log", Summary: "Switch the recording of click paths of a campaign.", MCP: mcpWrite,
			Description: "Turns the recording of click paths on or off for the campaign. The switch is kept on the campaign, so the engine reads it with its config; " +
				"paths already recorded stay until they expire.",
			RU: docText{
				Summary: "Включить или выключить запись путей кликов кампании.",
				Description: "Включает или выключает запись путей кликов кампании. Переключатель хранится в кампании, движок читает его вместе с конфигом; " +
					"уже записанные пути остаются до истечения срока.",
			},
			UK: docText{
				Summary: "Увімкнути або вимкнути запис шляхів кліків кампанії.",
				Description: "Вмикає або вимикає запис шляхів кліків кампанії. Перемикач зберігається в кампанії, рушій читає його разом із конфігом; " +
					"вже записані шляхи лишаються до завершення терміну.",
			},
			Body: trafficLogBody, Example: map[string]any{"enabled": false},
			Response: trafficLogInfoSample,
			Errors:   []int{400, 403, 404, 500, 503},
		},
	}
}
