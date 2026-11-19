package admin

import (
	"strconv"
	"strings"

	"github.com/hasayone/empataso-tds/internal/store"
)

var (
	// rangeParams is the reporting period shared by the statistics routes.
	rangeParams = []queryParam{
		strQ("from", "Start of the period, RFC 3339. Default: 24 hours before now.").
			loc("Начало периода, RFC 3339. По умолчанию — 24 часа назад.", "Початок періоду, RFC 3339. За замовчуванням — 24 години тому."),
		strQ("to", "End of the period, RFC 3339. Default: now.").
			loc("Конец периода, RFC 3339. По умолчанию — сейчас.", "Кінець періоду, RFC 3339. За замовчуванням — зараз."),
	}

	// filterParams is the repeatable condition filter. Its fields depend on the
	// table the route reads.
	filterParams = []queryParam{
		strQ("f", "Condition field:op:value; op is "+strings.Join(store.CondOps, ", ")+"; "+
			"contains and not_contains match a case-insensitive substring and need a field with text: true in log-fields; "+
			"repeat it for several conditions, all of them must hold. Field names come from log-fields.").
			loc("Условие вида поле:оператор:значение; оператор — "+strings.Join(store.CondOps, ", ")+"; "+
				"contains и not_contains ищут подстроку без учёта регистра и работают только с полями, у которых в log-fields стоит text: true; "+
				"параметр можно повторять для нескольких условий, должны выполняться все. Имена полей берите из log-fields.",
				"Умова виду поле:оператор:значення; оператор — "+strings.Join(store.CondOps, ", ")+"; "+
					"contains і not_contains шукають підрядок без урахування регістру й працюють лише з полями, у яких у log-fields стоїть text: true; "+
					"параметр можна повторювати для кількох умов, мають виконуватися всі. Назви полів беріть із log-fields.").repeated(),
	}

	// pageParams is the offset paging of the journals.
	pageParams = []queryParam{
		intQ("limit", "Rows per page.").loc("Строк на странице.", "Рядків на сторінці."),
		intQ("offset", "Rows to skip. Below zero counts as zero.").
			loc("Сколько строк пропустить. Значение меньше нуля считается нулём.", "Скільки рядків пропустити. Значення менше нуля вважається нулем."),
	}

	// sortParams is the server-side ordering of the journals.
	sortParams = []queryParam{
		strQ("sort", "Field name to sort by, taken from log-fields; a field without a ready column is refused.").
			loc("Имя поля для сортировки из log-fields; поле без готовой колонки отклоняется.",
				"Назва поля для сортування з log-fields; поле без готової колонки відхиляється."),
		strQ("dir", "Sort direction: asc or desc. Default: desc.").
			loc("Направление сортировки: asc или desc. По умолчанию desc.", "Напрямок сортування: asc або desc. За замовчуванням desc."),
	}

	// logValueParams filters the click journal by value; each takes a comma-separated list.
	logValueParams = []queryParam{
		strQ("group", "Campaign id; several ids separated by commas.").
			loc("Id кампании; несколько id через запятую.", "Id кампанії; кілька id через кому."),
		strQ("stream", "Stream name; several separated by commas.").
			loc("Имя потока; несколько через запятую.", "Назва потоку; кілька через кому."),
		strQ("country", "Country code; several separated by commas.").
			loc("Код страны; несколько через запятую.", "Код країни; кілька через кому."),
		strQ("device", "Device type; several separated by commas.").
			loc("Тип устройства; несколько через запятую.", "Тип пристрою; кілька через кому."),
		strQ("os", "Operating system; several separated by commas.").
			loc("Операционная система; несколько через запятую.", "Операційна система; кілька через кому."),
		strQ("browser", "Browser; several separated by commas.").
			loc("Браузер; несколько через запятую.", "Браузер; кілька через кому."),
		strQ("brand", "Device brand; several separated by commas.").
			loc("Бренд устройства; несколько через запятую.", "Бренд пристрою; кілька через кому."),
		strQ("ip", "Visitor IP address.").loc("IP-адрес посетителя.", "IP-адреса відвідувача."),
		strQ("bots", "Pass 1 to keep bots only.").loc("Передайте 1, чтобы оставить только ботов.", "Передайте 1, щоб залишити лише ботів."),
		strQ("humans", "Pass 1 to keep humans only.").loc("Передайте 1, чтобы оставить только людей.", "Передайте 1, щоб залишити лише людей."),
	}

	// postbackValueParams filters the conversion journal by value.
	postbackValueParams = []queryParam{
		strQ("group", "Campaign id.").loc("Id кампании.", "Id кампанії."),
		strQ("status", "Conversion status.").loc("Статус конверсии.", "Статус конверсії."),
		strQ("stream", "Stream name.").loc("Имя потока.", "Назва потоку."),
		strQ("offer", "Offer id.").loc("Id оффера.", "Id офера."),
		strQ("landing", "Landing id.").loc("Id лендинга.", "Id лендінга."),
		strQ("source", "Traffic source id.").loc("Id источника трафика.", "Id джерела трафіку."),
		strQ("network", "Affiliate network id.").loc("Id партнёрской сети.", "Id партнерської мережі."),
		strQ("cid", "Subid of the click.").loc("Subid клика.", "Subid кліка."),
	}
)

// Texts shared by several statistics routes, in English, Russian and Ukrainian.
const (
	statsErrEN = "A bad filter is 400, statistics that are not configured are 503, and a query that ClickHouse stopped " +
		"as too heavy is 504 \"query too heavy\": narrow the period."
	statsErrRU = "Неверный фильтр даёт 400, ненастроенная статистика — 503, запрос, который ClickHouse остановил как слишком тяжёлый, — " +
		"504 «query too heavy»: сузьте период."
	statsErrUK = "Хибний фільтр дає 400, неналаштована статистика — 503, запит, який ClickHouse зупинив як надто важкий, — " +
		"504 «query too heavy»: звузьте період."

	hiddenEN = "Keys of fields hidden for the caller are removed from the answer, and a filter on a hidden field is 400."
	hiddenRU = "Ключи полей, скрытых для вызывающего, убираются из ответа, а фильтр по скрытому полю даёт 400."
	hiddenUK = "Ключі полів, прихованих для викликаючого, прибираються з відповіді, а фільтр за прихованим полем дає 400."

	exportEN = "The file is the whole period, up to 50000 rows; when more match, the answer carries the header " +
		"X-Export-Truncated: 1. Columns of fields hidden for the caller are left out. The route is heavy: " +
		"it takes one of the key's slots for heavy queries. A bad filter is 400, missing statistics 503, " +
		"a query stopped as too heavy 504 \"query too heavy\"."
	exportRU = "В файле весь период, не больше 50000 строк; если подходит больше, в ответе есть заголовок " +
		"X-Export-Truncated: 1. Колонки полей, скрытых для вызывающего, не попадают в файл. Маршрут тяжёлый: " +
		"он занимает один из слотов ключа для тяжёлых запросов. Неверный фильтр даёт 400, ненастроенная статистика — 503, " +
		"запрос, остановленный как слишком тяжёлый, — 504 «query too heavy»."
	exportUK = "У файлі весь період, не більше 50000 рядків; якщо підходить більше, у відповіді є заголовок " +
		"X-Export-Truncated: 1. Колонки полів, прихованих для викликаючого, не потрапляють до файлу. Маршрут важкий: " +
		"він займає один зі слотів ключа для важких запитів. Хибний фільтр дає 400, неналаштована статистика — 503, " +
		"запит, зупинений як надто важкий, — 504 «query too heavy»."
)

// simulateBody is the JSON Schema of the POST /api/simulate request.
var simulateBody = map[string]any{
	"type":        "object",
	"description": "Dry run of the router over the groups sent in the request; nothing is stored.",
	"required":    []any{"groups", "group", "visitor"},
	"properties": map[string]any{
		"groups":     map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}, "description": "Campaign records to route through, as edited and not necessarily saved."},
		"offers":     map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}, "description": "Offer records used by the campaigns."},
		"landings":   map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}, "description": "Landing records used by the campaigns."},
		"group":      map[string]any{"type": "string", "description": "Id or alias of the campaign to run."},
		"offer_step": map[string]any{"type": "boolean", "description": "Simulate the second step, the request for the offer."},
		"roll":       map[string]any{"type": "integer", "description": "Fixed weighted draw value instead of a random one."},
		"visitor": map[string]any{
			"type":     "object",
			"required": []any{"ip"},
			"properties": map[string]any{
				"ip":        map[string]any{"type": "string", "description": "Visitor IP address."},
				"useragent": map[string]any{"type": "string", "description": "User-Agent header."},
				"referer":   map[string]any{"type": "string", "description": "Referer header."},
				"lang":      map[string]any{"type": "string", "description": "Accept-Language header."},
				"key":       map[string]any{"type": "string", "description": "Campaign key from the link."},
				"query":     map[string]any{"type": "string", "description": "Query string of the click, with or without a leading ?."},
				"country":   map[string]any{"type": "string", "description": "Two-letter country override, as a CDN header would say."},
				"repeat":    map[string]any{"type": "boolean", "description": "A returning, non-unique visitor."},
			},
		},
	},
}

// listWriteBody is the JSON Schema of the PUT /api/lists/{name} request.
var listWriteBody = map[string]any{
	"type":     "object",
	"required": []any{"content"},
	"properties": map[string]any{
		"content": map[string]any{"type": "string", "description": "The whole new text of the list file."},
	},
}

// logRowSample is one fake click journal row for the response samples.
var logRowSample = map[string]any{
	"ts": "2026-11-16T10:15:00Z", "group": "Promo", "stream": "main", "country": "us", "city": "Austin",
	"device": "mobile", "os": "Android", "browser": "Chrome", "brand": "Samsung", "bot": "", "uniq": 1,
	"ip": "203.0.113.10", "keyword": "", "out": "https://example.com/offer", "region": "TX", "lang": "en",
	"operator": "", "redirect": "302", "referer": "https://example.com/", "useragent": "Mozilla/5.0 (Linux; Android 14)",
	"domain": "example.com", "cid": "c1", "asn": 64500, "org": "Example ISP", "timezone": "America/Chicago",
}

// statsRouteDocs documents the dashboard statistics routes.
func statsRouteDocs() map[string]routeDoc {
	base := joinParams(rangeParams, filterParams)
	return map[string]routeDoc{
		"GET /api/stats/summary": {
			ID: "stats_summary", Summary: "Totals of clicks, uniques and bots for the period.",
			Description: "Conditions in f apply to the events table.",
			RU: docText{
				Summary:     "Итоги по кликам, уникальным посетителям и ботам за период.",
				Description: "Условия в f применяются к таблице событий.",
			},
			UK: docText{
				Summary:     "Підсумки за кліками, унікальними відвідувачами та ботами за період.",
				Description: "Умови в f застосовуються до таблиці подій.",
			},
			Query:    base,
			Response: map[string]any{"total": 1250, "unique": 980, "bots": 140},
			Errors:   []int{400, 500, 503, 504},
			MCP:      mcpRead,
		},
		"GET /api/stats/timeseries": {
			ID: "stats_timeseries", Summary: "Clicks bucketed by time.",
			Description: "One point per time bucket, quiet buckets included: clicks, uniques, bots, confirmed conversions and revenue. " +
				"A step below 3600 is 400; a long period makes the step grow so that the answer has at most 400 points. " +
				"cost, when present, is the click cost in cents by currency. " + statsErrEN,
			RU: docText{
				Summary: "Клики по интервалам времени.",
				Description: "Одна точка на интервал, пустые интервалы тоже есть: клики, уникальные, боты, подтверждённые конверсии и доход. " +
					"Шаг меньше 3600 даёт 400; на длинном периоде шаг увеличивается, чтобы в ответе было не больше 400 точек. " +
					"cost, если он есть, — цена кликов в центах по валютам. " + statsErrRU,
			},
			UK: docText{
				Summary: "Кліки за інтервалами часу.",
				Description: "Одна точка на інтервал, порожні інтервали теж є: кліки, унікальні, боти, підтверджені конверсії та дохід. " +
					"Крок менший за 3600 дає 400; на довгому періоді крок збільшується, щоб у відповіді було не більше 400 точок. " +
					"cost, якщо він є, — ціна кліків у центах за валютами. " + statsErrUK,
			},
			Query: joinParams(base, []queryParam{
				intQ("step", "Bucket size in seconds, at least 3600. Default: 3600.").
					loc("Размер корзины в секундах, не меньше 3600. По умолчанию 3600.", "Розмір кошика в секундах, не менше 3600. За замовчуванням 3600."),
			}),
			Response: []any{
				map[string]any{"ts": "2026-11-16T10:00:00Z", "total": 120, "unique": 95, "bots": 12, "conv": 3, "revenue": 45.5},
				map[string]any{"ts": "2026-11-16T11:00:00Z", "total": 80, "unique": 64, "bots": 5, "conv": 0, "revenue": 0},
			},
			Errors: []int{400, 500, 503, 504},
			MCP:    mcpRead,
		},
		"GET /api/stats/breakdown": {
			ID: "stats_breakdown", Summary: "Top values of one dimension.",
			Description: "Returns the most frequent values of the field dim with their click counts, the biggest first. " +
				"limit outside 1 to 1000 becomes 50. A dimension whose columns are not ready yet gives an empty list with 200. " +
				"A hidden dim and an unknown or non-dimension field are 400. " + statsErrEN,
			RU: docText{
				Summary: "Самые частые значения одного измерения.",
				Description: "Возвращает самые частые значения поля dim с числом кликов, сначала самые большие. " +
					"limit вне диапазона от 1 до 1000 заменяется на 50. Если колонки измерения ещё не готовы, ответ — пустой список с кодом 200. " +
					"Скрытое dim, неизвестное поле и поле без измерения дают 400. " + statsErrRU,
			},
			UK: docText{
				Summary: "Найчастіші значення одного виміру.",
				Description: "Повертає найчастіші значення поля dim з кількістю кліків, спочатку найбільші. " +
					"limit поза діапазоном від 1 до 1000 замінюється на 50. Якщо колонки виміру ще не готові, відповідь — порожній список із кодом 200. " +
					"Приховане dim, невідоме поле й поле без виміру дають 400. " + statsErrUK,
			},
			Query: joinParams(base, []queryParam{
				strQ("dim", "Dimension field name; only fields with a dimension in log-fields are accepted.").required().
					loc("Имя поля-измерения; принимаются только поля, у которых в log-fields есть измерение.",
						"Назва поля-виміру; приймаються лише поля, у яких у log-fields є вимір."),
				intQ("limit", "Rows to return. Default: 50.").loc("Сколько строк вернуть. По умолчанию 50.", "Скільки рядків повернути. За замовчуванням 50."),
			}),
			Response: []any{
				map[string]any{"key": "us", "count": 640},
				map[string]any{"key": "de", "count": 310},
			},
			Errors: []int{400, 500, 503, 504},
			MCP:    mcpRead,
		},
		"GET /api/stats/performance": {
			ID: "stats_performance", Summary: "Hits, uniques, bots, conversions and profit per campaign and stream.",
			Description: "One row per campaign and stream. Stream \"-\" is the campaign default, used when no stream matched. " +
				"forwarded counts the visits the stream handed on to another campaign; they are already added to hits, unique and bots. " +
				"profit is the payout and depsum the sum of deposits. cost is the click cost in cents by currency and is absent when empty. " +
				hiddenEN + " " + statsErrEN,
			RU: docText{
				Summary: "Клики, уникальные, боты, конверсии и прибыль по кампаниям и потокам.",
				Description: "Одна строка на кампанию и поток. Поток «-» — запасной вариант кампании, когда ни один поток не подошёл. " +
					"forwarded — визиты, которые поток передал другой кампании; они уже прибавлены к hits, unique и bots. " +
					"profit — выплата, depsum — сумма депозитов. cost — цена кликов в центах по валютам, пустой cost в ответ не попадает. " +
					hiddenRU + " " + statsErrRU,
			},
			UK: docText{
				Summary: "Кліки, унікальні, боти, конверсії та прибуток за кампаніями й потоками.",
				Description: "Один рядок на кампанію та потік. Потік «-» — запасний варіант кампанії, коли жоден потік не підійшов. " +
					"forwarded — візити, які потік передав іншій кампанії; вони вже додані до hits, unique і bots. " +
					"profit — виплата, depsum — сума депозитів. cost — ціна кліків у центах за валютами, порожній cost у відповідь не потрапляє. " +
					hiddenUK + " " + statsErrUK,
			},
			Query: base,
			Response: []any{map[string]any{
				"group_id": "promo", "group": "Promo", "stream": "main", "hits": 1250, "unique": 980, "bots": 140,
				"conv": 12, "profit": 360.5, "depsum": 0, "forwarded": 0,
			}},
			Errors: []int{400, 500, 503, 504},
			MCP:    mcpRead,
		},
		"GET /api/stats/facts": {
			ID: "stats_facts", Summary: "Click and conversion facts per catalog row.",
			Description: "The answer is {ready, clicks, status}. clicks are the hits, uniques and click cost per campaign, stream, offer, " +
				"landing and affiliate network; status are the conversion count, profit and depsum per offer, affiliate network, " +
				"campaign and status. ready is false when the offer and network columns are not on the tables yet. " +
				"For a caller who may not see the conversion status, the status rows keep only confirmed conversions summed into one row per key. " +
				"Missing statistics give an empty answer with 200. A bad filter is 400 and a query stopped as too heavy is 504 \"query too heavy\".",
			RU: docText{
				Summary: "Факты по кликам и конверсиям для строк каталогов.",
				Description: "Ответ — {ready, clicks, status}. clicks — хиты, уникальные и цена кликов по кампании, потоку, офферу, " +
					"лендингу и партнёрской сети; status — число конверсий, profit и depsum по офферу, партнёрской сети, " +
					"кампании и статусу. ready равен false, если колонок оффера и сети в таблицах ещё нет. " +
					"Для вызывающего, который не видит статус конверсии, в status остаются только подтверждённые конверсии, сложенные в одну строку на ключ. " +
					"Без статистики ответ пустой с кодом 200. Неверный фильтр даёт 400, запрос, остановленный как слишком тяжёлый, — 504 «query too heavy».",
			},
			UK: docText{
				Summary: "Факти за кліками та конверсіями для рядків каталогів.",
				Description: "Відповідь — {ready, clicks, status}. clicks — хіти, унікальні та ціна кліків за кампанією, потоком, офером, " +
					"лендінгом і партнерською мережею; status — кількість конверсій, profit і depsum за офером, партнерською мережею, " +
					"кампанією та статусом. ready дорівнює false, якщо колонок офера й мережі в таблицях ще немає. " +
					"Для викликаючого, який не бачить статус конверсії, у status залишаються лише підтверджені конверсії, складені в один рядок на ключ. " +
					"Без статистики відповідь порожня з кодом 200. Хибний фільтр дає 400, запит, зупинений як надто важкий, — 504 «query too heavy».",
			},
			Query: base,
			Response: map[string]any{
				"ready": true,
				"clicks": []any{map[string]any{
					"offer_id": "o1", "landing_id": "l1", "network_id": "n1", "group_id": "promo", "stream": "main",
					"hits": 1250, "uniq": 980, "uniq_first": 900, "cost_cents": 0, "cost_cur": "",
				}},
				"status": []any{map[string]any{
					"offer_id": "o1", "network_id": "n1", "group_id": "promo", "status": "sale", "n": 12, "profit": 360.5, "depsum": 0,
				}},
			},
			Errors: []int{400, 500, 504},
			MCP:    mcpRead,
		},
	}
}

// entryCampaignEN, entryCampaignRU and entryCampaignUK say how the two journals read a campaign condition.
const (
	entryCampaignEN = "A condition on campaign or campaign_id (eq, in, ne, nin) matches the serving campaign and also the campaign the visitor entered through " +
		"(origin_campaign, origin_campaign_id), so the journal of the entry campaign shows what it handed on; the access scope of the caller stays exact. " +
		"The other routes of statistics compare the serving campaign only."
	entryCampaignRU = "Условие по campaign или campaign_id (eq, in, ne, nin) совпадает с кампанией, обслужившей клик, и с кампанией, через которую пришёл посетитель " +
		"(origin_campaign, origin_campaign_id): журнал входной кампании показывает то, что она передала дальше; доступ вызывающего остаётся строгим. " +
		"Остальные маршруты статистики сравнивают только обслужившую кампанию."
	entryCampaignUK = "Умова за campaign або campaign_id (eq, in, ne, nin) збігається з кампанією, що обслужила клік, і з кампанією, через яку прийшов відвідувач " +
		"(origin_campaign, origin_campaign_id): журнал вхідної кампанії показує те, що вона передала далі; доступ викликаючого лишається строгим. " +
		"Решта маршрутів статистики порівнюють лише кампанію, що обслужила."
)

// journalRouteDocs documents the click journal, conversion journal, report and field catalog.
func journalRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/logs": {
			ID: "list_logs", Summary: "One page of the click journal.",
			Description: "The answer is {total, rows}. The page size defaults to 100 and is capped at 1000. " +
				"A click that one campaign handed on to another is stored under the campaign that served it. " + entryCampaignEN,
			RU: docText{
				Summary: "Одна страница журнала кликов.",
				Description: "Ответ — {total, rows}. Размер страницы по умолчанию 100, максимум 1000. " +
					"Клик, который одна кампания передала другой, записан за кампанией, что его обслужила. " + entryCampaignRU,
			},
			UK: docText{
				Summary: "Одна сторінка журналу кліків.",
				Description: "Відповідь — {total, rows}. Розмір сторінки за замовчуванням 100, максимум 1000. " +
					"Клік, який одна кампанія передала іншій, записано за кампанією, що його обслужила. " + entryCampaignUK,
			},
			Query:    joinParams(rangeParams, filterParams, logValueParams, pageParams, sortParams),
			Response: map[string]any{"total": 1, "rows": []any{logRowSample}},
			Errors:   []int{400, 500, 503, 504},
			MCP:      mcpRead,
		},
		"GET /api/logs/filters": {
			ID: "list_log_filter_values", Summary: "Values of the click journal filters over the period.",
			Description: "For each of the dimensions group, stream, country, device, os, browser and brand, the answer lists up to 1000 values " +
				"seen in the period, the most frequent first, without empty values; it feeds the filter dropdowns. " +
				"A dimension hidden for the caller is left out. A dimension whose columns are not ready yet is an empty list and the answer stays 200. " +
				"Any other query error is 500 \"stats error\". Missing statistics give an empty object with 200. A bad filter is 400. " +
				"The route is heavy: it takes one of the key's slots for heavy queries.",
			RU: docText{
				Summary: "Значения фильтров журнала кликов за период.",
				Description: "Для каждого из измерений group, stream, country, device, os, browser и brand ответ перечисляет до 1000 значений, " +
					"встретившихся в периоде, самые частые первыми, без пустых значений; по нему строятся выпадающие списки фильтров. " +
					"Измерение, скрытое для вызывающего, в ответ не попадает. Если колонки измерения ещё не готовы, у него пустой список, а ответ остаётся 200. " +
					"Любая другая ошибка запроса — 500 «stats error». Без статистики ответ — пустой объект с кодом 200. Неверный фильтр даёт 400. " +
					"Маршрут тяжёлый: он занимает один из слотов ключа для тяжёлых запросов.",
			},
			UK: docText{
				Summary: "Значення фільтрів журналу кліків за період.",
				Description: "Для кожного з вимірів group, stream, country, device, os, browser і brand відповідь перелічує до 1000 значень, " +
					"що трапилися в періоді, найчастіші першими, без порожніх значень; за нею будуються випадні списки фільтрів. " +
					"Вимір, прихований для викликаючого, у відповідь не потрапляє. Якщо колонки виміру ще не готові, у нього порожній список, а відповідь лишається 200. " +
					"Будь-яка інша помилка запиту — 500 «stats error». Без статистики відповідь — порожній об'єкт із кодом 200. Хибний фільтр дає 400. " +
					"Маршрут важкий: він займає один зі слотів ключа для важких запитів.",
			},
			Query: joinParams(rangeParams, filterParams),
			Response: map[string]any{
				"group":   []any{"Promo"},
				"stream":  []any{"main", "backup"},
				"country": []any{"us", "de"},
				"device":  []any{"mobile", "desktop"},
			},
			Errors: []int{400, 500},
		},
		"GET /api/logs/export": {
			ID: "export_logs_csv", Summary: "The click journal of the period as a CSV file.",
			Description: "Takes the same filters and sorting as the click journal; limit and offset are ignored. " +
				"The body is CSV (text/csv; charset=utf-8) with Content-Disposition attachment; filename=logs.csv and one header row: " +
				"ts, group, stream, country, city, device, os, browser, brand, bot, uniq, ip, keyword, out, region, lang, operator, " +
				"redirect, referer, useragent, domain, cid, asn, org, timezone, page, os_version, browser_ver, se, via, origin_campaign, " +
				"origin_campaign_id, campaign_id, offer_id, landing_id, source_id, network_id, sub_id_1 to sub_id_30, site, ad_campaign_id, " +
				"creative_id, external_id, xrw, device_model. A cell that starts with =, +, - or @ gets a leading apostrophe. " +
				"The sample shows the first columns only. " + exportEN,
			RU: docText{
				Summary: "Журнал кликов за период в виде CSV-файла.",
				Description: "Принимает те же фильтры и сортировку, что и журнал кликов; limit и offset игнорируются. " +
					"Тело — CSV (text/csv; charset=utf-8) с Content-Disposition attachment; filename=logs.csv и одной строкой заголовка: " +
					"ts, group, stream, country, city, device, os, browser, brand, bot, uniq, ip, keyword, out, region, lang, operator, " +
					"redirect, referer, useragent, domain, cid, asn, org, timezone, page, os_version, browser_ver, se, via, origin_campaign, " +
					"origin_campaign_id, campaign_id, offer_id, landing_id, source_id, network_id, sub_id_1 … sub_id_30, site, ad_campaign_id, " +
					"creative_id, external_id, xrw, device_model. Ячейка, начинающаяся с =, +, - или @, получает впереди апостроф. " +
					"В примере показаны только первые колонки. " + exportRU,
			},
			UK: docText{
				Summary: "Журнал кліків за період у вигляді CSV-файлу.",
				Description: "Приймає ті самі фільтри й сортування, що й журнал кліків; limit і offset ігноруються. " +
					"Тіло — CSV (text/csv; charset=utf-8) із Content-Disposition attachment; filename=logs.csv і одним рядком заголовка: " +
					"ts, group, stream, country, city, device, os, browser, brand, bot, uniq, ip, keyword, out, region, lang, operator, " +
					"redirect, referer, useragent, domain, cid, asn, org, timezone, page, os_version, browser_ver, se, via, origin_campaign, " +
					"origin_campaign_id, campaign_id, offer_id, landing_id, source_id, network_id, sub_id_1 … sub_id_30, site, ad_campaign_id, " +
					"creative_id, external_id, xrw, device_model. Комірка, що починається з =, +, - або @, отримує попереду апостроф. " +
					"У прикладі показано лише перші колонки. " + exportUK,
			},
			Query: joinParams(rangeParams, filterParams, logValueParams, sortParams),
			Response: "ts,group,stream,country,city,device,os,browser,brand,bot,uniq,ip,keyword,out\n" +
				"2026-11-16T10:15:00Z,Promo,main,us,Austin,mobile,Android,Chrome,Samsung,,1,203.0.113.10,,https://example.com/offer\n",
			ResponseType: "text/csv",
			Errors:       []int{400, 500, 503, 504},
		},
		"DELETE /api/logs": {
			ID: "delete_group_logs", Summary: "Delete the click journal of one campaign.",
			Description: "Removes every click of the campaign group from the journal. The deletion runs in ClickHouse in the background, " +
				"so the clicks disappear shortly after the answer, not at once; the answer {status: deleted} confirms the request was accepted. " +
				"A missing group is 400, missing statistics 503. The key needs the action logs_delete, otherwise the answer is 403.",
			RU: docText{
				Summary: "Удалить журнал кликов одной кампании.",
				Description: "Убирает из журнала все клики группы кампании. Удаление идёт в ClickHouse в фоне, " +
					"поэтому клики исчезают вскоре после ответа, а не мгновенно; ответ {status: deleted} подтверждает, что запрос принят. " +
					"Без параметра group — 400, без статистики — 503. Ключу нужно действие logs_delete, иначе ответ 403.",
			},
			UK: docText{
				Summary: "Видалити журнал кліків однієї кампанії.",
				Description: "Прибирає з журналу всі кліки групи кампанії. Видалення йде в ClickHouse у фоні, " +
					"тому кліки зникають невдовзі після відповіді, а не миттєво; відповідь {status: deleted} підтверджує, що запит прийнято. " +
					"Без параметра group — 400, без статистики — 503. Ключу потрібна дія logs_delete, інакше відповідь 403.",
			},
			Query: []queryParam{
				strQ("group", "Id of the campaign whose clicks are deleted.").required().
					loc("Id кампании, клики которой удаляются.", "Id кампанії, кліки якої видаляються."),
			},
			Response: map[string]any{"status": "deleted"},
			Errors:   []int{400, 403, 500, 503},
		},
		"GET /api/postbacks": {
			ID: "list_postbacks", Summary: "One page of the conversion journal.",
			Description: "The answer is {total, depsum, count, rows}; total is the profit sum. " +
				"Conditions in f apply to the conversions table. The page size defaults to 200. " + entryCampaignEN,
			RU: docText{
				Summary: "Одна страница журнала конверсий.",
				Description: "Ответ — {total, depsum, count, rows}; total — сумма прибыли. " +
					"Условия в f применяются к таблице конверсий. Размер страницы по умолчанию 200. " + entryCampaignRU,
			},
			UK: docText{
				Summary: "Одна сторінка журналу конверсій.",
				Description: "Відповідь — {total, depsum, count, rows}; total — сума прибутку. " +
					"Умови в f застосовуються до таблиці конверсій. Розмір сторінки за замовчуванням 200. " + entryCampaignUK,
			},
			Query: joinParams(rangeParams, filterParams, postbackValueParams, pageParams, sortParams),
			Response: map[string]any{
				"total": 30.5, "depsum": 0, "count": 1,
				"rows": []any{map[string]any{
					"ts": "2026-11-16T10:20:00Z", "group": "Promo", "stream": "main", "domain": "example.com", "page": "/",
					"device": "mobile", "country": "us", "city": "Austin", "profit": 30.5, "depsum": 0, "cid": "c1",
					"status": "sale", "offer_id": "o1", "network_id": "n1",
				}},
			},
			Errors: []int{400, 500, 503, 504},
			MCP:    mcpRead,
		},
		"GET /api/postbacks/export": {
			ID: "export_postbacks_csv", Summary: "The conversion journal of the period as a CSV file.",
			Description: "Takes the same filters and sorting as the conversion journal; limit and offset are ignored. " +
				"The body is CSV (text/csv; charset=utf-8) with Content-Disposition attachment; filename=postbacks.csv and one header row: " +
				"ts, group, stream, domain, page, device, country, city, profit, cid, status, offer_id, landing_id, source_id, network_id, " +
				"sub_id_1 to sub_id_30, campaign_id, origin_campaign, origin_campaign_id, click_ts, original_status, prev_status, tid, ip, " +
				"region, lang, os, os_version, browser, browser_ver, brand, useragent, referer, se, keyword, asn, org, site, ad_campaign_id, " +
				"creative_id, external_id, xrw, device_model, depsum. A cell that starts with =, +, - or @ gets a leading apostrophe. " +
				"The sample shows the first columns only. " + exportEN,
			RU: docText{
				Summary: "Журнал конверсий за период в виде CSV-файла.",
				Description: "Принимает те же фильтры и сортировку, что и журнал конверсий; limit и offset игнорируются. " +
					"Тело — CSV (text/csv; charset=utf-8) с Content-Disposition attachment; filename=postbacks.csv и одной строкой заголовка: " +
					"ts, group, stream, domain, page, device, country, city, profit, cid, status, offer_id, landing_id, source_id, network_id, " +
					"sub_id_1 … sub_id_30, campaign_id, origin_campaign, origin_campaign_id, click_ts, original_status, prev_status, tid, ip, " +
					"region, lang, os, os_version, browser, browser_ver, brand, useragent, referer, se, keyword, asn, org, site, ad_campaign_id, " +
					"creative_id, external_id, xrw, device_model, depsum. Ячейка, начинающаяся с =, +, - или @, получает впереди апостроф. " +
					"В примере показаны только первые колонки. " + exportRU,
			},
			UK: docText{
				Summary: "Журнал конверсій за період у вигляді CSV-файлу.",
				Description: "Приймає ті самі фільтри й сортування, що й журнал конверсій; limit і offset ігноруються. " +
					"Тіло — CSV (text/csv; charset=utf-8) із Content-Disposition attachment; filename=postbacks.csv і одним рядком заголовка: " +
					"ts, group, stream, domain, page, device, country, city, profit, cid, status, offer_id, landing_id, source_id, network_id, " +
					"sub_id_1 … sub_id_30, campaign_id, origin_campaign, origin_campaign_id, click_ts, original_status, prev_status, tid, ip, " +
					"region, lang, os, os_version, browser, browser_ver, brand, useragent, referer, se, keyword, asn, org, site, ad_campaign_id, " +
					"creative_id, external_id, xrw, device_model, depsum. Комірка, що починається з =, +, - або @, отримує попереду апостроф. " +
					"У прикладі показано лише перші колонки. " + exportUK,
			},
			Query: joinParams(rangeParams, filterParams, postbackValueParams, sortParams),
			Response: "ts,group,stream,domain,page,device,country,city,profit,cid,status\n" +
				"2026-11-16T10:20:00Z,Promo,main,example.com,/,mobile,us,Austin,30.50,c1,sale\n",
			ResponseType: "text/csv",
			Errors:       []int{400, 500, 503, 504},
		},
		"GET /api/postback-log": {
			ID: "list_postback_intake_log", Summary: "The log of received and sent postbacks.",
			Description: "Lists the postback lines of the period, the newest first: when it came, the subid of the click, the status, " +
				"the payout, the outcome and the direction (in or out). An inbound line also has the sender ip, the network_id of the click, " +
				"the method, the path and the params (a query string in which tokens, keys and signatures are *** and the key segment of /{key}/postback is masked), " +
				"the useragent, the campaign_id of the click and duplicate (a repeat of a stored click and status). " +
				"Outcomes: ok, click_not_found, no_subid, no_store, store_error, queued, retry, queue_full, retry_overflow, replayed, " +
				"bad_key (wrong key or no secret), invalid (bad payout), method_not_allowed, or the result of an outbound call. " +
				"Filters: outcome (duplicate selects accepted repeats, ok first arrivals), network, ip, cid, direction; offset and limit page the list " +
				"(limit defaults to 200 and is capped at 1000). An administrator sees every line; any other caller with the conversions section sees " +
				"the lines of the campaigns it may see, without lines that have no campaign. A hidden field is dropped from the lines, a hidden payout drops params as well. " +
				"Missing statistics give an empty list with 200. A query stopped as too heavy is 504 \"query too heavy\".",
			RU: docText{
				Summary: "Журнал принятых и отправленных постбэков.",
				Description: "Список строк постбэков за период, сначала новые: время, subid клика, статус, " +
					"выплата, итог и направление (in или out). У входящей строки есть ещё ip отправителя, network_id клика, " +
					"метод, путь и params (строка запроса, где токены, ключи и подписи заменены на ***, а ключ в пути /{key}/postback скрыт), " +
					"useragent, campaign_id клика и duplicate (повтор уже сохранённой пары клик + статус). " +
					"Итоги: ok, click_not_found, no_subid, no_store, store_error, queued, retry, queue_full, retry_overflow, replayed, " +
					"bad_key (неверный ключ или секрет не задан), invalid (неверная выплата), method_not_allowed или результат исходящего вызова. " +
					"Фильтры: outcome (duplicate — принятые повторы, ok — первые), network, ip, cid, direction; offset и limit листают список " +
					"(limit по умолчанию 200, максимум 1000). Администратор видит все строки; остальные с разделом конверсий — строки кампаний, " +
					"которые им видны, без строк без кампании. Скрытое поле убирается из строк, скрытая выплата убирает и params. " +
					"Без статистики ответ — пустой список с кодом 200. Запрос, остановленный как слишком тяжёлый, даёт 504 «query too heavy».",
			},
			UK: docText{
				Summary: "Журнал прийнятих і надісланих постбеків.",
				Description: "Список рядків постбеків за період, спочатку нові: час, subid кліка, статус, " +
					"виплата, підсумок і напрямок (in або out). Вхідний рядок має ще ip відправника, network_id кліка, " +
					"метод, шлях і params (рядок запиту, де токени, ключі й підписи замінено на ***, а ключ у шляху /{key}/postback приховано), " +
					"useragent, campaign_id кліка і duplicate (повтор уже збереженої пари клік + статус). " +
					"Підсумки: ok, click_not_found, no_subid, no_store, store_error, queued, retry, queue_full, retry_overflow, replayed, " +
					"bad_key (хибний ключ або секрет не задано), invalid (хибна виплата), method_not_allowed або результат вихідного виклику. " +
					"Фільтри: outcome (duplicate — прийняті повтори, ok — перші), network, ip, cid, direction; offset і limit гортають список " +
					"(limit за замовчуванням 200, максимум 1000). Адміністратор бачить усі рядки; інші з розділом конверсій — рядки кампаній, " +
					"які їм видно, без рядків без кампанії. Приховане поле прибирається з рядків, прихована виплата прибирає й params. " +
					"Без статистики відповідь — порожній список із кодом 200. Запит, зупинений як надто важкий, дає 504 «query too heavy».",
			},
			Query: append(append([]queryParam{}, rangeParams...),
				strQ("outcome", "One outcome; duplicate selects accepted repeats.").
					loc("Один итог; duplicate — принятые повторы.", "Один підсумок; duplicate — прийняті повтори."),
				strQ("network", "Affiliate network id of the click.").loc("Id партнёрской сети клика.", "Id партнерської мережі кліка."),
				strQ("ip", "Sender address.").loc("Адрес отправителя.", "Адреса відправника."),
				strQ("cid", "Click id (subid).").loc("Id клика (subid).", "Id кліка (subid)."),
				strQ("direction", "in or out.").loc("in или out.", "in або out."),
				intQ("offset", "Rows to skip.").loc("Сколько строк пропустить.", "Скільки рядків пропустити."),
				intQ("limit", "Rows to return, at most 1000. Default: 200.").
					loc("Сколько строк вернуть, не больше 1000. По умолчанию 200.", "Скільки рядків повернути, не більше 1000. За замовчуванням 200.")),
			Response: []any{map[string]any{
				"ts": "2026-11-16T10:20:00Z", "subid": "c1", "status": "sale", "payout": 30.5, "outcome": "ok", "direction": "in",
				"ip": "203.0.113.20", "network_id": "net-example", "method": "GET", "path": "/postback",
				"params": "currency=USD&key=%2A%2A%2A&payout=30.5&status=sale&subid=c1", "useragent": "ExampleNetwork/1.0",
				"campaign_id": "promo",
			}},
			Errors: []int{400, 500, 504},
		},
		"GET /api/report": {
			ID: "get_report", Summary: "Report rows for the period.",
			Description: "Visits and conversions grouped by campaign, stream, country, offer and traffic source. " +
				"The answer is an array of rows {group, stream, country, offer, source, hits, unique, uniq_first, bots, conv, profit, depsum}; " +
				"with meta=1 it is {rows, truncated}. offer and source stay empty when the click columns are not on the table yet. " +
				"cost is the click cost of the row in cents by currency code, for example {\"USD\": 4200}; the field is absent when no click of the row has a price. " +
				"An empty currency code means the tracker currency of imported clicks. cost is hidden from roles without access to cost. " +
				"A condition on a field the conversions table does not have leaves conv at zero instead of failing. " +
				"With limit, offset or sort the answer is one page sorted on the server over the whole period: " +
				"{rows, total, offset, limit, sort, dir, sort_cur, totals, truncated}. total counts every row; totals {hits, unique, bots, conv, profit, depsum, cost} " +
				"are the sums of every row, unique being the first-step uniques; ratios such as CR are taken from these sums. " +
				"The cost sort uses the currency with the largest total cost, named in sort_cur, and puts rows without cost in it last. " +
				"truncated is true only above " + strconv.Itoa(store.PageMaxRows) + " visit groups: the set stops there, the totals still count every visit. " +
				"A paged request with day=1 cuts every row by event day: the row gets day (YYYY-MM-DD in tz) and counts that day alone, so a campaign appears once per day; " +
				"totals still count the whole period, unique being the first-step uniques of the period, not summed over days. " +
				"Missing statistics give an empty answer with 200. A bad filter, page, sort or tz is 400 and a query stopped as too heavy is 504 \"query too heavy\".",
			RU: docText{
				Summary: "Строки отчёта за период.",
				Description: "Визиты и конверсии, сгруппированные по кампании, потоку, стране, офферу и источнику трафика. " +
					"Ответ — массив строк {group, stream, country, offer, source, hits, unique, uniq_first, bots, conv, profit, depsum}; " +
					"при meta=1 — {rows, truncated}. offer и source остаются пустыми, пока колонок кликов нет в таблице. " +
					"cost — расход кликов строки в центах по коду валюты, например {\"USD\": 4200}; поля нет, если ни у одного клика строки нет цены. " +
					"Пустой код валюты означает валюту трекера у перенесённых кликов. Для ролей без доступа к расходу поле cost скрыто. " +
					"Условие по полю, которого нет в таблице конверсий, оставляет conv нулём и не вызывает ошибку. " +
					"С limit, offset или sort ответ — одна страница, отсортированная на сервере по всему периоду: " +
					"{rows, total, offset, limit, sort, dir, sort_cur, totals, truncated}. total — число всех строк; totals {hits, unique, bots, conv, profit, depsum, cost} " +
					"— суммы по всем строкам, unique — уники первого шага; отношения вроде CR считаются из этих сумм. " +
					"Сортировка по расходу идёт по валюте с наибольшим общим расходом (она в sort_cur), строки без расхода в ней — в конце. " +
					"truncated равен true только при больше чем " + strconv.Itoa(store.PageMaxRows) + " группах визитов: набор обрывается, итоги всё равно считают все визиты. " +
					"Постраничный запрос с day=1 режет каждую строку по дню события: у строки появляется day (ГГГГ-ММ-ДД в поясе tz), и она считает только этот день, поэтому кампания встречается раз в день; " +
					"totals по-прежнему считают весь период, unique — уники первого шага за период, а не сумма по дням. " +
					"Без статистики ответ пустой с кодом 200. Неверный фильтр, страница, сортировка или tz дают 400, запрос, остановленный как слишком тяжёлый, — 504 «query too heavy».",
			},
			UK: docText{
				Summary: "Рядки звіту за період.",
				Description: "Візити та конверсії, згруповані за кампанією, потоком, країною, офером і джерелом трафіку. " +
					"Відповідь — масив рядків {group, stream, country, offer, source, hits, unique, uniq_first, bots, conv, profit, depsum}; " +
					"при meta=1 — {rows, truncated}. offer і source залишаються порожніми, доки колонок кліків немає в таблиці. " +
					"cost — витрати кліків рядка в центах за кодом валюти, наприклад {\"USD\": 4200}; поля немає, якщо жоден клік рядка не має ціни. " +
					"Порожній код валюти означає валюту трекера в перенесених кліків. Для ролей без доступу до витрат поле cost приховане. " +
					"Умова за полем, якого немає в таблиці конверсій, залишає conv нулем і не спричиняє помилку. " +
					"З limit, offset або sort відповідь — одна сторінка, відсортована на сервері за весь період: " +
					"{rows, total, offset, limit, sort, dir, sort_cur, totals, truncated}. total — кількість усіх рядків; totals {hits, unique, bots, conv, profit, depsum, cost} " +
					"— суми за всіма рядками, unique — унікальні першого кроку; відношення на кшталт CR рахуються з цих сум. " +
					"Сортування за витратами йде за валютою з найбільшими загальними витратами (вона в sort_cur), рядки без витрат у ній — у кінці. " +
					"truncated дорівнює true лише при понад " + strconv.Itoa(store.PageMaxRows) + " групах візитів: набір обривається, підсумки все одно рахують усі візити. " +
					"Постраничний запит з day=1 ріже кожен рядок за днем події: у рядка з'являється day (РРРР-ММ-ДД у поясі tz), і він рахує лише цей день, тож кампанія трапляється раз на день; " +
					"totals, як і раніше, рахують увесь період, unique — унікальні першого кроку за період, а не сума за днями. " +
					"Без статистики відповідь порожня з кодом 200. Хибний фільтр, сторінка, сортування чи tz дають 400, запит, зупинений як надто важкий, — 504 «query too heavy».",
			},
			Query: joinParams(rangeParams, filterParams, []queryParam{
				strQ("meta", "Pass 1 to get {rows, truncated} instead of the bare array; truncated is true when the rows were cut at 2000.").
					loc("Передайте 1, чтобы получить {rows, truncated} вместо голого массива; truncated равен true, если строки обрезаны на 2000.",
						"Передайте 1, щоб отримати {rows, truncated} замість голого масиву; truncated дорівнює true, якщо рядки обрізано на 2000."),
				strQ("day", "Pass 1 with limit, offset or sort to cut the rows by event day: every row then has day. Default: rows are not cut by day.").
					loc("Передайте 1 вместе с limit, offset или sort, чтобы разрезать строки по дню события: у каждой строки появится day. По умолчанию по дням строки не режутся.",
						"Передайте 1 разом з limit, offset або sort, щоб розрізати рядки за днем події: у кожного рядка з'явиться day. За замовчуванням за днями рядки не ріжуться."),
				strQ("tz", "IANA time zone name that cuts the days with day=1, for example Europe/Berlin. Default: UTC. An unknown zone is 400 \"bad tz\".").
					loc("Имя часового пояса IANA, по которому режутся дни при day=1, например Europe/Berlin. По умолчанию UTC. Неизвестный пояс — 400 «bad tz».",
						"Назва часового поясу IANA, за яким ріжуться дні при day=1, наприклад Europe/Berlin. За замовчуванням UTC. Невідомий пояс — 400 «bad tz»."),
			}, tablePageParams(store.SliceSortKeys, store.SliceDefaultSort)),
			Response: []any{map[string]any{
				"group": "promo", "stream": "main", "country": "us", "offer": "o1", "source": "s1",
				"hits": 1250, "unique": 980, "uniq_first": 900, "bots": 140, "conv": 12, "profit": 360.5, "depsum": 0,
				"cost": map[string]any{"USD": 4200},
			}},
			Errors: []int{400, 500, 504},
			MCP:    mcpRead,
		},
		"GET /api/log-fields": {
			ID: "list_log_fields", Summary: "Field catalog of the journals.",
			Description: "Names usable in f, sort and dim, which table has each, and which column waves are ready.",
			RU: docText{
				Summary:     "Каталог полей журналов.",
				Description: "Имена, которые можно использовать в f, sort и dim, таблицы, в которых есть каждое поле, и какие волны колонок уже готовы.",
			},
			UK: docText{
				Summary:     "Каталог полів журналів.",
				Description: "Назви, які можна використовувати в f, sort і dim, таблиці, у яких є кожне поле, і які хвилі колонок уже готові.",
			},
			Response: map[string]any{
				"fields": []any{map[string]any{
					"name": "country", "type": "LowCardinality(String)", "events": true, "conv": true, "dim": true,
					"text": false, "events_wave": "base", "conv_wave": "base", "hideable": true,
				}},
				"ready": map[string]any{
					"events":      map[string]any{"base": true, "net": true},
					"conversions": map[string]any{"base": true, "net": true},
				},
			},
			MCP: mcpRead,
		},
		"GET /api/log-values": {
			ID: "list_log_values", Summary: "Top values of one field.",
			Description: "Returns the most frequent values of one field of the events or conversions table with their counts, the biggest first. " +
				"A field whose columns are not ready yet gives an empty list with 200. An unknown table, a hidden or unknown field and a field " +
				"without a dimension are 400. " + statsErrEN,
			RU: docText{
				Summary: "Самые частые значения одного поля.",
				Description: "Возвращает самые частые значения одного поля таблицы events или conversions с их числом, сначала самые большие. " +
					"Если колонки поля ещё не готовы, ответ — пустой список с кодом 200. Неизвестная таблица, скрытое или неизвестное поле и поле " +
					"без измерения дают 400. " + statsErrRU,
			},
			UK: docText{
				Summary: "Найчастіші значення одного поля.",
				Description: "Повертає найчастіші значення одного поля таблиці events або conversions з їхньою кількістю, спочатку найбільші. " +
					"Якщо колонки поля ще не готові, відповідь — порожній список із кодом 200. Невідома таблиця, приховане або невідоме поле й поле " +
					"без виміру дають 400. " + statsErrUK,
			},
			Query: joinParams(rangeParams, filterParams, []queryParam{
				strQ("table", "Table to read: events or conversions.").required().
					loc("Таблица для чтения: events или conversions.", "Таблиця для читання: events або conversions."),
				strQ("field", "Field name with a dimension.").required().
					loc("Имя поля с измерением.", "Назва поля з виміром."),
				intQ("limit", "Rows to return, at most 500. Default: 500.").
					loc("Сколько строк вернуть, не больше 500. По умолчанию 500.", "Скільки рядків повернути, не більше 500. За замовчуванням 500."),
			}),
			Response: []any{
				map[string]any{"key": "us", "count": 640},
				map[string]any{"key": "de", "count": 310},
			},
			Errors: []int{400, 500, 503, 504},
			MCP:    mcpRead,
		},
	}
}

// keywordRouteDocs documents the keyword table and its daily trend.
func keywordRouteDocs() map[string]routeDoc {
	const shared = "Clicks are those of people, bots are left out. Conversions are all statuses, counted by the date of " +
		"the conversion; a lead or sale of a click rejected later is not counted. The period is at most 92 days: " +
		"from not before to, or a longer period, is 400 \"bad range\". Pick a campaign with f=campaign_id:eq:<id> " +
		"and search with f=keyword:contains:<text>; a hidden filter field is 400. " +
		"403 when the keyword field is hidden for the caller. 429 \"busy\" when two keyword queries already run. " +
		"504 \"query too heavy\" when ClickHouse stopped the query: narrow the period or pick a campaign. "
	const sharedRU = "Считаются клики людей, боты не учитываются. Конверсии берутся всех статусов и считаются по дате конверсии; " +
		"лид или продажа по клику, который позже отклонили, не учитываются. Период не длиннее 92 дней: если from не раньше to " +
		"(начало не меньше конца) или период длиннее, ответ 400 «bad range». Кампанию выбирайте через f=campaign_id:eq:<id>, " +
		"поиск ведите через f=keyword:contains:<текст>; скрытое поле в фильтре — 400. " +
		"403, если поле keyword скрыто для вызывающего. 429 «busy», если уже выполняются два запроса по ключевым словам. " +
		"504 «query too heavy», если ClickHouse остановил запрос: сузьте период или выберите кампанию. "
	const sharedUK = "Враховуються кліки людей, боти не враховуються. Конверсії беруться всіх статусів і рахуються за датою конверсії; " +
		"лід або продаж за кліком, який пізніше відхилили, не враховуються. Період не довший за 92 дні: якщо from не раніше to " +
		"(початок не менший за кінець) або період довший, відповідь 400 «bad range». Кампанію обирайте через f=campaign_id:eq:<id>, " +
		"пошук ведіть через f=keyword:contains:<текст>; приховане поле у фільтрі — 400. " +
		"403, якщо поле keyword приховане для викликаючого. 429 «busy», якщо вже виконуються два запити за ключовими словами. " +
		"504 «query too heavy», якщо ClickHouse зупинив запит: звузьте період або оберіть кампанію. "
	return map[string]routeDoc{
		"GET /api/keywords/stats": {
			ID: "keyword_stats", Summary: "Clicks, conversions and payout per keyword.",
			Description: shared + "The answer is {rows, truncated, conv_ready}. A row is {keyword, hits, unique, conv, leads, " +
				"sales, rejected, profit, depsum}, ordered by payout, then clicks, at most 2000; truncated is true when more exist. " +
				"conv_ready is false when the conversion columns are not available for the query, " +
				"and the money fields are then zero and mean nothing. An empty keyword is the clicks that carried none. " +
				"conv and profit count all statuses, as /api/report does; leads, sales and rejected split conv by status. " +
				"A hidden status field removes leads, sales and rejected from every row, conv and profit stay. " +
				"A hidden profit field removes profit and depsum, a hidden uniq field removes unique. " +
				"With limit, offset or sort the answer is one page sorted on the server: {rows, total, offset, limit, sort, dir, totals, truncated, conv_ready}; " +
				"total counts every keyword, totals {hits, unique, conv, leads, sales, rejected, profit, depsum} are their sums, ratios are taken from the sums, truncated is false.",
			RU: docText{
				Summary: "Клики, конверсии и выплата по каждому ключевому слову.",
				Description: sharedRU + "Ответ — {rows, truncated, conv_ready}. Строка — {keyword, hits, unique, conv, leads, " +
					"sales, rejected, profit, depsum}, порядок по выплате, затем по кликам, не больше 2000; truncated равен true, если строк больше. " +
					"conv_ready равен false, если колонки конверсий недоступны для этого запроса, " +
					"и денежные поля тогда равны нулю и ничего не значат. Пустое keyword — клики без ключевого слова. " +
					"conv и profit считают все статусы, как и /api/report; leads, sales и rejected делят conv по статусам. " +
					"Скрытое поле status убирает leads, sales и rejected из каждой строки, conv и profit остаются. " +
					"Скрытое поле profit убирает profit и depsum, скрытое поле uniq — unique. " +
					"С limit, offset или sort ответ — одна страница, отсортированная на сервере: {rows, total, offset, limit, sort, dir, totals, truncated, conv_ready}; " +
					"total — число всех ключей, totals {hits, unique, conv, leads, sales, rejected, profit, depsum} — их суммы, отношения считаются из сумм, truncated равен false.",
			},
			UK: docText{
				Summary: "Кліки, конверсії та виплата за кожним ключовим словом.",
				Description: sharedUK + "Відповідь — {rows, truncated, conv_ready}. Рядок — {keyword, hits, unique, conv, leads, " +
					"sales, rejected, profit, depsum}, порядок за виплатою, потім за кліками, не більше 2000; truncated дорівнює true, якщо рядків більше. " +
					"conv_ready дорівнює false, якщо колонки конверсій недоступні для цього запиту, " +
					"і грошові поля тоді дорівнюють нулю й нічого не означають. Порожнє keyword — кліки без ключового слова. " +
					"conv і profit враховують усі статуси, як і /api/report; leads, sales і rejected ділять conv за статусами. " +
					"Приховане поле status прибирає leads, sales і rejected з кожного рядка, conv і profit залишаються. " +
					"Приховане поле profit прибирає profit і depsum, приховане поле uniq — unique. " +
					"З limit, offset або sort відповідь — одна сторінка, відсортована на сервері: {rows, total, offset, limit, sort, dir, totals, truncated, conv_ready}; " +
					"total — кількість усіх ключів, totals {hits, unique, conv, leads, sales, rejected, profit, depsum} — їхні суми, відношення рахуються із сум, truncated дорівнює false.",
			},
			Query: joinParams(rangeParams, filterParams, []queryParam{
				strQ("by", "Attribution mode: empty groups by the last click; origin uses the first click's campaign, keyword and stream. Unknown values are 400; origin is 403 when origin fields are hidden.").
					loc("Режим атрибуции: пусто группирует по последнему клику; origin использует кампанию, ключевое слово и поток первого клика. Неизвестное значение — 400; origin — 403, если поля первоисточника скрыты.",
						"Режим атрибуції: порожньо групує за останнім кліком; origin використовує кампанію, ключове слово й потік першого кліку. Невідоме значення — 400; origin — 403, якщо поля першоджерела приховані."),
			}, tablePageParams(store.KeywordSortKeys, store.KeywordDefaultSort)),
			Response: map[string]any{
				"rows": []any{
					map[string]any{
						"keyword": "buy shoes", "hits": 120, "unique": 100, "conv": 4, "leads": 1, "sales": 3,
						"rejected": 0, "profit": 90.0, "depsum": 0,
					},
					map[string]any{
						"keyword": "", "hits": 800, "unique": 650, "conv": 1, "leads": 1, "sales": 0,
						"rejected": 0, "profit": 5.0, "depsum": 0,
					},
				},
				"truncated": false, "conv_ready": true,
			},
			Errors: []int{400, 403, 429, 500, 504},
			MCP:    mcpRead,
		},
		"GET /api/keywords/trend": {
			ID: "keyword_trend", Summary: "Day-by-day clicks, conversions and payout of the top keywords.",
			Description: shared + "The keywords are the first rows of keyword_stats in its order, so no keys are sent. " +
				"The answer is {days, series, conv_ready}: days are every calendar day of the period in tz, " +
				"series are {keyword, hits, conv, profit} in table order, each array with one number per day and zero for a quiet day. " +
				"When conv_ready is false the conv and profit arrays are absent, not zero. A hidden profit removes the profit arrays. " +
				"top outside 1..25 or not a number, and an unknown tz, are 400.",
			RU: docText{
				Summary: "Клики, конверсии и выплата по дням для самых заметных ключевых слов.",
				Description: sharedRU + "Ключевые слова — первые строки keyword_stats в её порядке, поэтому сами слова в запросе не передаются. " +
					"Ответ — {days, series, conv_ready}: days — каждый календарный день периода в поясе tz, " +
					"series — {keyword, hits, conv, profit} в порядке таблицы, в каждом массиве по одному числу на день, для тихого дня — ноль. " +
					"Если conv_ready равен false, массивов conv и profit в ответе нет, а не нули. Скрытое поле profit убирает массивы profit. " +
					"top вне диапазона 1..25 или не число, а также неизвестный tz дают 400.",
			},
			UK: docText{
				Summary: "Кліки, конверсії та виплата за днями для найпомітніших ключових слів.",
				Description: sharedUK + "Ключові слова — перші рядки keyword_stats у її порядку, тому самі слова в запиті не передаються. " +
					"Відповідь — {days, series, conv_ready}: days — кожен календарний день періоду в поясі tz, " +
					"series — {keyword, hits, conv, profit} у порядку таблиці, у кожному масиві по одному числу на день, для тихого дня — нуль. " +
					"Якщо conv_ready дорівнює false, масивів conv і profit у відповіді немає, а не нулі. Приховане поле profit прибирає масиви profit. " +
					"top поза діапазоном 1..25 або не число, а також невідомий tz дають 400.",
			},
			Query: joinParams(rangeParams, filterParams, []queryParam{
				strQ("by", "Attribution mode: empty groups by the last click; origin uses the first click's campaign, keyword and stream. Unknown values are 400; origin is 403 when origin fields are hidden.").
					loc("Режим атрибуции: пусто группирует по последнему клику; origin использует кампанию, ключевое слово и поток первого клика. Неизвестное значение — 400; origin — 403, если поля первоисточника скрыты.",
						"Режим атрибуції: порожньо групує за останнім кліком; origin використовує кампанію, ключове слово й потік першого кліку. Невідоме значення — 400; origin — 403, якщо поля першоджерела приховані."),
				intQ("top", "How many keywords of the table to follow, 1 to 25. Default: 25.").
					loc("Сколько ключевых слов таблицы показывать, от 1 до 25. По умолчанию 25.",
						"Скільки ключових слів таблиці показувати, від 1 до 25. За замовчуванням 25."),
				strQ("tz", "IANA time zone name that cuts the days, for example Europe/Moscow. Default: UTC.").
					loc("Имя часового пояса IANA, по которому режутся дни, например Europe/Moscow. По умолчанию UTC.",
						"Назва часового поясу IANA, за яким розрізаються дні, наприклад Europe/Kyiv. За замовчуванням UTC."),
			}),
			Response: map[string]any{
				"days": []any{"2026-11-15", "2026-11-16"},
				"series": []any{map[string]any{
					"keyword": "buy shoes", "hits": []any{50, 70}, "conv": []any{1, 3}, "profit": []any{20.0, 70.0},
				}},
				"conv_ready": true,
			},
			Errors: []int{400, 403, 429, 500, 504},
			MCP:    mcpRead,
		},
		"GET /api/keys": {
			ID: "keyword_list", Summary: "Start of the keyword file collected for one campaign and day.",
			Description: "The answer is {content, truncated, lines, size, max_lines}: content is the first lines of the file, " +
				"at most " + strconv.Itoa(keysPreviewLines) + " lines and " + strconv.Itoa(keysPreviewBytes>>20) + " MiB, " +
				"lines is how many it holds, size the whole file in bytes, truncated is true when the file is longer; " +
				"GET /api/keys/download gives the whole file. A missing or unreadable file is an empty content with 200. " +
				keysAccessDoc,
			RU: docText{
				Summary: "Начало файла ключевых слов одной кампании за один день.",
				Description: "Ответ — {content, truncated, lines, size, max_lines}: content — первые строки файла, " +
					"не больше " + strconv.Itoa(keysPreviewLines) + " строк и " + strconv.Itoa(keysPreviewBytes>>20) + " МиБ, " +
					"lines — сколько строк в нём, size — размер всего файла в байтах, truncated равен true, если файл длиннее; " +
					"весь файл отдаёт GET /api/keys/download. Отсутствующий или нечитаемый файл — пустой content с кодом 200. " +
					keysAccessRU,
			},
			UK: docText{
				Summary: "Початок файлу ключових слів однієї кампанії за один день.",
				Description: "Відповідь — {content, truncated, lines, size, max_lines}: content — перші рядки файлу, " +
					"не більше " + strconv.Itoa(keysPreviewLines) + " рядків і " + strconv.Itoa(keysPreviewBytes>>20) + " МіБ, " +
					"lines — скільки рядків у ньому, size — розмір усього файлу в байтах, truncated дорівнює true, якщо файл довший; " +
					"весь файл віддає GET /api/keys/download. Відсутній або нечитний файл — порожній content із кодом 200. " +
					keysAccessUK,
			},
			Query: keysParams,
			Response: map[string]any{
				"content": "buy shoes\nred shoes\n", "truncated": false, "lines": 2, "size": 20, "max_lines": keysPreviewLines,
			},
			Errors: []int{403, 404, 500, 503},
		},
		"GET /api/keys/download": {
			ID: "keyword_list_download", Summary: "Whole keyword file of one campaign and day as a text attachment.",
			Description: "The body is the raw file, text/plain, one keyword a line, with Content-Disposition attachment " +
				"and Content-Length. A bad group or date is 400, a missing file 404. " + keysAccessDoc,
			RU: docText{
				Summary: "Весь файл ключевых слов одной кампании за один день как текстовое вложение.",
				Description: "Тело — сам файл, text/plain, по одному ключевому слову в строке, с Content-Disposition attachment " +
					"и Content-Length. Неверная группа или дата — 400, отсутствующий файл — 404. " + keysAccessRU,
			},
			UK: docText{
				Summary: "Увесь файл ключових слів однієї кампанії за один день як текстове вкладення.",
				Description: "Тіло — сам файл, text/plain, по одному ключовому слову в рядку, із Content-Disposition attachment " +
					"і Content-Length. Хибна група або дата — 400, відсутній файл — 404. " + keysAccessUK,
			},
			Query:        keysParams,
			Response:     "buy shoes\nred shoes\n",
			ResponseType: "text/plain",
			Errors:       []int{400, 403, 404, 500, 503},
		},
	}
}

// The access rule shared by the keyword file routes, in English, Russian and Ukrainian.
const (
	keysAccessDoc = "403 when the keyword field is hidden for the caller, 404 for a campaign outside the caller's view."
	keysAccessRU  = "403, если поле keyword скрыто для вызывающего, 404 для кампании вне его области видимости."
	keysAccessUK  = "403, якщо поле keyword приховане для викликаючого, 404 для кампанії поза його областю видимості."
)

// keysParams are the query parameters of the keyword file routes.
var keysParams = []queryParam{
	strQ("group", "Campaign id.").required().loc("Id кампании.", "Id кампанії."),
	strQ("date", "Day of the file, YYYY-MM-DD, in the engine's time zone.").required().
		loc("День файла, ГГГГ-ММ-ДД, в часовом поясе движка.", "День файлу, РРРР-ММ-ДД, у часовому поясі рушія."),
	strQ("se", "Pass 1 for the search-engine queries instead of the link keywords.").
		loc("Передайте 1, чтобы получить запросы из поисковых систем вместо ключевых слов из ссылки.",
			"Передайте 1, щоб отримати запити з пошукових систем замість ключових слів із посилання."),
}

// toolRouteDocs documents the lookup and simulate tools and the IP list files.
func toolRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/lookup": {
			ID: "lookup_ip", Summary: "What the engine learns from an IP address.",
			Description: "Returns the visitor geo and network data and the .dat lists the address falls into.",
			RU: docText{
				Summary:     "Что движок узнаёт по IP-адресу.",
				Description: "Возвращает гео- и сетевые данные посетителя и списки .dat, в которые попадает адрес.",
			},
			UK: docText{
				Summary:     "Що рушій дізнається за IP-адресою.",
				Description: "Повертає гео- та мережеві дані відвідувача й списки .dat, до яких потрапляє адреса.",
			},
			Query: []queryParam{strQ("ip", "IPv4 or IPv6 address.").required().
				loc("Адрес IPv4 или IPv6.", "Адреса IPv4 або IPv6.")},
			Response: map[string]any{
				"visitor": map[string]any{
					"ip": "203.0.113.10", "country": "us", "city": "Austin", "region": "TX", "timezone": "America/Chicago",
					"utc": "-06:00", "asn": 64500, "org": "Example ISP", "operator": "", "proxy": false,
				},
				"lists": []any{map[string]any{"list": "wap", "label": "example-operator"}},
				"databases": []any{map[string]any{
					"kind": "city", "path": "/data/GeoLite2-City.mmdb", "type": "GeoLite2-City", "built": "2026-11-01T00:00:00Z", "loaded": true,
				}},
			},
			Errors: []int{400},
			MCP:    mcpRead,
		},
		"POST /api/simulate": {
			ID: "simulate_visitor", Summary: "Walk a visitor through a campaign without storing anything.",
			Description: "Reports for every stream whether it matches and which rule turned the visitor away. " +
				"Impression limits, bot detection and the antiflood are not simulated.",
			RU: docText{
				Summary: "Провести посетителя через кампанию, ничего не сохраняя.",
				Description: "Для каждого потока сообщает, подошёл ли он и какое правило отсекло посетителя. " +
					"Лимиты показов, определение ботов и антифлуд не имитируются.",
			},
			UK: docText{
				Summary: "Провести відвідувача через кампанію, нічого не зберігаючи.",
				Description: "Для кожного потоку повідомляє, чи підійшов він і яке правило відсікло відвідувача. " +
					"Ліміти показів, визначення ботів і антифлуд не імітуються.",
			},
			Body: simulateBody,
			Example: map[string]any{
				"groups": []any{map[string]any{
					"id": "promo", "name": "Promo", "status": true,
					"streams": []any{map[string]any{"name": "main", "status": true}},
				}},
				"group": "promo",
				"visitor": map[string]any{
					"ip": "203.0.113.10", "useragent": "Mozilla/5.0 (Linux; Android 14) Chrome/120.0", "lang": "en",
					"query": "?utm_source=test",
				},
			},
			Response: map[string]any{
				"visitor": map[string]any{
					"ip": "203.0.113.10", "country": "us", "city": "Austin", "region": "TX", "timezone": "America/Chicago",
					"utc": "-06:00", "asn": 64500, "org": "Example ISP", "operator": "", "proxy": false,
					"device": "mobile", "os": "Android 14", "browser": "Chrome 120.0", "brand": "Samsung",
				},
				"unique": true, "lang": "en",
				"path": []any{map[string]any{
					"group":    "promo",
					"streams":  []any{map[string]any{"name": "main", "on": true, "match": true}},
					"selected": "main", "redirect": "302", "out": "https://example.com/offer",
				}},
			},
			Errors: []int{400},
			MCP:    mcpRead,
		},
		"GET /api/lists": {
			ID: "list_ip_list_files", Summary: "Names of the IP list files.",
			Description: "Returns the names of the .dat files in the lists directory, sorted, with the .dat suffix; " +
				"the name is what GET /api/lists/{name} takes. Without a lists directory the answer is an empty list with 200. " +
				"An unreadable directory is 500. Needs the lists_read key action.",
			RU: docText{
				Summary: "Имена файлов IP-списков.",
				Description: "Возвращает отсортированные имена файлов .dat из каталога списков, вместе с суффиксом .dat; " +
					"это имя принимает GET /api/lists/{name}. Без каталога списков ответ — пустой список с кодом 200. " +
					"Нечитаемый каталог даёт 500. Нужна галочка ключа lists_read.",
			},
			UK: docText{
				Summary: "Назви файлів IP-списків.",
				Description: "Повертає відсортовані назви файлів .dat із каталогу списків разом із суфіксом .dat; " +
					"цю назву приймає GET /api/lists/{name}. Без каталогу списків відповідь — порожній список із кодом 200. " +
					"Нечитний каталог дає 500. Потрібна позначка ключа lists_read.",
			},
			Response: []any{"proxy.dat", "wap.dat"},
			Errors:   []int{500},
		},
		"GET /api/lists/{name}": {
			ID: "get_ip_list_file", Summary: "Text of one IP list file.",
			Description: "The answer is {name, content}: content is the whole file as text. The name is the file name with the .dat suffix, " +
				"for example wap.dat. A line is a single IP, a CIDR range or a range from-to; a line starting with # is a label " +
				"that applies to the lines below it. An invalid name and a missing file are both 404; " +
				"a server without a lists directory is 503. Needs the lists_read key action.",
			RU: docText{
				Summary: "Текст одного файла IP-списка.",
				Description: "Ответ — {name, content}: content — весь файл текстом. Имя — это имя файла с суффиксом .dat, " +
					"например wap.dat. Строка — одиночный IP, диапазон CIDR или диапазон «от-до»; строка, начинающаяся с #, — метка, " +
					"которая действует на строки ниже. Недопустимое имя и отсутствующий файл одинаково дают 404; " +
					"сервер без каталога списков даёт 503. Нужна галочка ключа lists_read.",
			},
			UK: docText{
				Summary: "Текст одного файлу IP-списку.",
				Description: "Відповідь — {name, content}: content — весь файл текстом. Назва — це ім'я файлу із суфіксом .dat, " +
					"наприклад wap.dat. Рядок — поодинока IP-адреса, діапазон CIDR або діапазон «від-до»; рядок, що починається з #, — мітка, " +
					"яка діє на рядки нижче. Недопустиме ім'я та відсутній файл однаково дають 404; " +
					"сервер без каталогу списків дає 503. Потрібна позначка ключа lists_read.",
			},
			Response: map[string]any{"name": "proxy.dat", "content": "203.0.113.0/24\n198.51.100.7\n"},
			Errors:   []int{404, 503},
		},
		"PUT /api/lists/{name}": {
			ID: "put_ip_list_file", Summary: "Replace the text of one IP list file.",
			Description: "Writes content as the whole new text of the file, replacing it atomically; a name that does not exist yet creates the file. " +
				"The name is the file name with the .dat suffix, only letters, digits, dot, underscore and hyphen. " +
				"The text is stored as sent, one entry a line: an IP, a CIDR range, a range from-to, or a # label line. " +
				"The body is at most 16 MiB. A body that is not JSON or too large and an invalid name are 400; " +
				"a server without a lists directory is 503. Only a key of the ADMIN type may call this route.",
			RU: docText{
				Summary: "Заменить текст одного файла IP-списка.",
				Description: "Записывает content как весь новый текст файла, заменяя его целиком; если такого имени ещё нет, файл создаётся. " +
					"Имя — это имя файла с суффиксом .dat, допустимы только буквы, цифры, точка, подчёркивание и дефис. " +
					"Текст сохраняется как есть, по одной записи в строке: IP, диапазон CIDR, диапазон «от-до» или строка-метка с #. " +
					"Тело не больше 16 МиБ. Тело не в формате JSON или слишком большое и недопустимое имя дают 400; " +
					"сервер без каталога списков даёт 503. Вызвать маршрут может только ключ типа ADMIN.",
			},
			UK: docText{
				Summary: "Замінити текст одного файлу IP-списку.",
				Description: "Записує content як увесь новий текст файлу, замінюючи його повністю; якщо такої назви ще немає, файл створюється. " +
					"Назва — це ім'я файлу із суфіксом .dat, припустимі лише літери, цифри, крапка, підкреслення та дефіс. " +
					"Текст зберігається як є, по одному запису в рядку: IP, діапазон CIDR, діапазон «від-до» або рядок-мітка з #. " +
					"Тіло не більше 16 МіБ. Тіло не у форматі JSON або надто велике й недопустиме ім'я дають 400; " +
					"сервер без каталогу списків дає 503. Викликати маршрут може лише ключ типу ADMIN.",
			},
			Body:     listWriteBody,
			Example:  map[string]any{"content": "203.0.113.0/24\n198.51.100.7\n"},
			Response: map[string]any{"status": "saved"},
			Errors:   []int{400, 503},
		},
	}
}
