package admin

import "maps"

// campaignPostbacksBody is the JSON Schema of the PUT /api/campaigns/{id}/postbacks request.
var campaignPostbacksBody = map[string]any{
	"type":        "array",
	"description": "Complete list of outbound postback templates of the campaign; it replaces the stored list.",
	"items": map[string]any{
		"type":     "object",
		"required": []any{"url"},
		"properties": map[string]any{
			"id":       map[string]any{"type": "string", "description": "Template id."},
			"name":     map[string]any{"type": "string", "description": "Template label."},
			"url":      map[string]any{"type": "string", "description": "Address to call, may hold macros."},
			"method":   map[string]any{"type": "string", "description": "HTTP method; default GET."},
			"statuses": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Conversion statuses that fire it; empty fires for every status."},
		},
	},
}

// s2sPostbacksBody is the JSON Schema of the PUT /api/s2s request: the template item of
// campaignPostbacksBody with the description of the shared catalog.
var s2sPostbacksBody = catalogSchemaWith(campaignPostbacksBody,
	"Complete list of the shared server-to-server templates of the current section; it replaces the visible ones.")

// campaignListBody is the JSON Schema of the PUT /api/groups request.
var campaignListBody = map[string]any{
	"type":        "array",
	"description": "Complete list of the campaigns of the current section; a campaign left out of the array is removed.",
	"items": map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"description":          "Campaign record, the same as the body of PUT /api/groups/{id}.",
	},
}

// catalogSchemaWith returns a shallow copy of schema with its description replaced.
// schema is not changed.
func catalogSchemaWith(schema map[string]any, desc string) map[string]any {
	out := maps.Clone(schema)
	out["description"] = desc
	return out
}

var (
	// catalogSavedAnswer is the success body of the routes that store a change.
	catalogSavedAnswer = map[string]string{"status": "saved"}
	// catalogDeletedAnswer is the success body of the routes that remove a record.
	catalogDeletedAnswer = map[string]string{"status": "deleted"}
)

// catalogNote is one sentence added to the text of a route, in the three languages.
type catalogNote struct{ EN, RU, UK string }

// catalogNoun is a noun of a catalog in one language: the singular as the object of a verb,
// the plural as the subject, and the plural after "list of".
type catalogNoun struct{ Acc, Pl, GenPl string }

// catalogDoc describes one catalog collection for catalogRouteDocs.
//
// base is the collection path, for example /api/offers. one is the singular noun and the
// operationId stem, many the plural noun. body lists the key fields of a record for the
// request body; bodyRU and bodyUK say the same in Russian and Ukrainian. ru and uk are the
// nouns of the localized texts. example is a request body and record a stored record as the
// answers show it. create and remove add a sentence to the create and delete texts.
// conflicts marks a catalog whose replace and delete may answer 409.
type catalogDoc struct {
	base, one, many string
	body            string
	bodyRU, bodyUK  string
	ru, uk          catalogNoun
	example, record map[string]any
	create, remove  catalogNote
	conflicts       bool
}

// catalogJoin joins the non-empty parts with single spaces.
func catalogJoin(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += p
	}
	return out
}

// catalogRouteDocs returns the five CRUD route docs of one catalog.
// c names the collection. Reads are mcpRead; create, update and delete are mcpWrite.
func catalogRouteDocs(c catalogDoc) map[string]routeDoc {
	body := objectBody(c.body)
	replaceErrors := []int{400, 403, 404, 500, 503}
	deleteErrors := []int{403, 404, 500, 503}
	if c.conflicts {
		replaceErrors = []int{400, 403, 404, 409, 500, 503}
		deleteErrors = []int{403, 404, 409, 500, 503}
	}
	return map[string]routeDoc{
		"GET " + c.base: {
			ID: "list_" + c.many, Summary: "List " + c.many + ".", MCP: mcpRead,
			Description: "Returns every " + c.one + " the caller may see.",
			RU: docText{
				Summary:     "Список " + c.ru.GenPl + ".",
				Description: "Возвращает все " + c.ru.Pl + ", доступные вызывающему.",
			},
			UK: docText{
				Summary:     "Список " + c.uk.GenPl + ".",
				Description: "Повертає всі " + c.uk.Pl + ", доступні викликачу.",
			},
			Response: []any{c.record},
			Errors:   []int{500, 503},
		},
		"GET " + c.base + "/{id}": {
			ID: "get_" + c.one, Summary: "Get one " + c.one + ".", MCP: mcpRead,
			Description: "Returns the record with this id; 404 when it does not exist or the caller may not see it.",
			RU: docText{
				Summary:     "Получить " + c.ru.Acc + ".",
				Description: "Возвращает запись с этим id; 404, если её нет или вызывающий не должен её видеть.",
			},
			UK: docText{
				Summary:     "Отримати " + c.uk.Acc + ".",
				Description: "Повертає запис з цим id; 404, якщо його немає або викликач не має його бачити.",
			},
			Response: c.record,
			Errors:   []int{404, 500, 503},
		},
		"POST " + c.base: {
			ID: "create_" + c.one, Summary: "Create a " + c.one + ".", Body: body, MCP: mcpWrite,
			Description: catalogJoin("Adds a new record. An empty id is numbered by the server; a taken id answers 409.", c.body, c.create.EN),
			RU: docText{
				Summary:     "Создать " + c.ru.Acc + ".",
				Description: catalogJoin("Добавляет новую запись. Пустой id сервер нумерует сам, занятый id даёт 409.", c.bodyRU, c.create.RU),
			},
			UK: docText{
				Summary:     "Створити " + c.uk.Acc + ".",
				Description: catalogJoin("Додає новий запис. Порожній id сервер нумерує сам, зайнятий id дає 409.", c.bodyUK, c.create.UK),
			},
			Example:  c.example,
			Status:   201,
			Response: c.record,
			Errors:   []int{400, 403, 409, 500, 503},
		},
		"PUT " + c.base + "/{id}": {
			ID: "update_" + c.one, Summary: "Replace a " + c.one + ".", Body: body, MCP: mcpWrite,
			Description: "The id comes from the path; the body replaces the whole record.",
			RU: docText{
				Summary:     "Заменить " + c.ru.Acc + ".",
				Description: "Id берётся из пути; тело заменяет запись целиком.",
			},
			UK: docText{
				Summary:     "Замінити " + c.uk.Acc + ".",
				Description: "Id береться зі шляху; тіло замінює запис повністю.",
			},
			Example:  c.example,
			Response: c.record,
			Errors:   replaceErrors,
		},
		"DELETE " + c.base + "/{id}": {
			ID: "delete_" + c.one, Summary: "Delete a " + c.one + ".", MCP: mcpWrite,
			Description: catalogJoin("Removes the record with this id.", c.remove.EN),
			RU: docText{
				Summary:     "Удалить " + c.ru.Acc + ".",
				Description: catalogJoin("Удаляет запись с этим id.", c.remove.RU),
			},
			UK: docText{
				Summary:     "Видалити " + c.uk.Acc + ".",
				Description: catalogJoin("Видаляє запис з цим id.", c.remove.UK),
			},
			Response: catalogDeletedAnswer,
			Errors:   deleteErrors,
		},
	}
}

// catalogDocs lists the catalogs an agent may read and change.
var catalogDocs = []catalogDoc{
	{
		base: "/api/offers", one: "offer", many: "offers",
		body: "Offer record. Key fields: id, name, url, status (boolean), network_id, group, " +
			"countries (lowercase ISO codes), payout_model (cpa or cpc), payout, payout_currency.",
		bodyRU: "Запись оффера. Ключевые поля: id, name, url, status (логическое), network_id, group, " +
			"countries (коды ISO в нижнем регистре), payout_model (cpa или cpc), payout, payout_currency.",
		bodyUK: "Запис офера. Ключові поля: id, name, url, status (логічне), network_id, group, " +
			"countries (коди ISO в нижньому регістрі), payout_model (cpa або cpc), payout, payout_currency.",
		ru: catalogNoun{"оффер", "офферы", "офферов"},
		uk: catalogNoun{"офер", "офери", "оферів"},
		example: map[string]any{
			"id": "o1", "name": "Spring promo", "url": "https://example.com/offer", "status": true,
			"network_id": "n1", "group": "Spring", "countries": []any{"us", "de"},
			"payout_model": "cpa", "payout": 12.5, "payout_currency": "USD",
		},
		record: map[string]any{
			"id": "o1", "name": "Spring promo", "url": "https://example.com/offer", "status": true,
			"network_id": "n1", "group": "Spring", "countries": []any{"us", "de"},
			"payout_model": "cpa", "payout": 12.5, "payout_currency": "USD",
		},
	},
	{
		base: "/api/landings", one: "landing", many: "landings",
		body: "Landing record. Key fields: id, name, url, status (boolean), " +
			"local (boolean, files served by the engine), entry (file served from the landing directory), group.",
		bodyRU: "Запись лендинга. Ключевые поля: id, name, url, status (логическое), " +
			"local (логическое, файлы отдаёт движок), entry (файл из папки лендинга, который отдаётся), group.",
		bodyUK: "Запис лендінгу. Ключові поля: id, name, url, status (логічне), " +
			"local (логічне, файли віддає рушій), entry (файл із теки лендінгу, який віддається), group.",
		ru: catalogNoun{"лендинг", "лендинги", "лендингов"},
		uk: catalogNoun{"лендінг", "лендінги", "лендінгів"},
		example: map[string]any{
			"id": "l1", "name": "Promo page", "url": "https://example.com/landing", "status": true, "group": "Spring",
		},
		record: map[string]any{
			"id": "l1", "name": "Promo page", "url": "https://example.com/landing", "status": true, "group": "Spring",
		},
		remove: catalogNote{
			EN: "The files of a local landing are removed with it. Needs the landing_files key action.",
			RU: "Файлы локального лендинга удаляются вместе с ним. Нужна галочка ключа landing_files.",
			UK: "Файли локального лендінгу видаляються разом із ним. Потрібна позначка ключа landing_files.",
		},
	},
	{
		base: "/api/affiliate-networks", one: "affiliate_network", many: "affiliate_networks",
		body: "Affiliate network record. Key fields: id, name, " +
			"status (boolean), postback_url (a note for the partner cabinet, the engine does not call it), offer_param, offer_query, group.",
		bodyRU: "Запись партнёрской сети. Ключевые поля: id, name, " +
			"status (логическое), postback_url (заметка для кабинета партнёра, движок его не вызывает), offer_param, offer_query, group.",
		bodyUK: "Запис партнерської мережі. Ключові поля: id, name, " +
			"status (логічне), postback_url (нотатка для кабінету партнера, рушій його не викликає), offer_param, offer_query, group.",
		ru: catalogNoun{"партнёрскую сеть", "партнёрские сети", "партнёрских сетей"},
		uk: catalogNoun{"партнерську мережу", "партнерські мережі", "партнерських мереж"},
		example: map[string]any{
			"id": "n1", "name": "Example Network", "status": true,
			"postback_url": "https://example.com/postback?subid={subid}&status={status}",
			"offer_param":  "offer_id", "offer_query": "sub1={subid}", "group": "CPA",
		},
		record: map[string]any{
			"id": "n1", "name": "Example Network", "status": true,
			"postback_url": "https://example.com/postback?subid={subid}&status={status}",
			"offer_param":  "offer_id", "offer_query": "sub1={subid}", "group": "CPA",
		},
	},
	{
		base: "/api/traffic-sources", one: "traffic_source", many: "traffic_sources",
		body: "Traffic source record. Key fields: id, name, " +
			"parameters (object mapping the query keys the partner sends for keyword and sub_id_1..30), group.",
		bodyRU: "Запись источника трафика. Ключевые поля: id, name, " +
			"parameters (объект: ключи запроса, которые шлёт партнёр, для keyword и sub_id_1..30), group.",
		bodyUK: "Запис джерела трафіку. Ключові поля: id, name, " +
			"parameters (об'єкт: ключі запиту, які надсилає партнер, для keyword і sub_id_1..30), group.",
		ru: catalogNoun{"источник трафика", "источники трафика", "источников трафика"},
		uk: catalogNoun{"джерело трафіку", "джерела трафіку", "джерел трафіку"},
		example: map[string]any{
			"id": "s1", "name": "Example Ads",
			"parameters": map[string]any{"keyword": "kw", "sub_id_1": "campaign_id"}, "group": "Ads",
		},
		record: map[string]any{
			"id": "s1", "name": "Example Ads",
			"parameters": map[string]any{"keyword": "kw", "sub_id_1": "campaign_id"}, "group": "Ads",
		},
	},
	{
		base: "/api/domains", one: "domain", many: "domains",
		body:   "Domain record. Key fields: id, host, group, role, primary (boolean), disabled (boolean).",
		bodyRU: "Запись домена. Ключевые поля: id, host, group, role, primary (логическое), disabled (логическое).",
		bodyUK: "Запис домену. Ключові поля: id, host, group, role, primary (логічне), disabled (логічне).",
		ru:     catalogNoun{"домен", "домены", "доменов"},
		uk:     catalogNoun{"домен", "домени", "доменів"},
		example: map[string]any{
			"id": "d1", "host": "go.example.com", "group": "Main", "role": "links",
		},
		record: map[string]any{
			"id": "d1", "host": "go.example.com", "group": "Main", "role": "links",
			"ok": true, "checked": "2026-11-16T10:30:00Z", "campaigns": 2,
		},
		create: catalogNote{
			EN: "The role is admin, links or both, links by default; only an administrator may set the admin or both role or the primary flag. The check result and the registrar and Cloudflare fields belong to the server. A host that is being bought answers 409.",
			RU: "Роль — admin, links или both, по умолчанию links; роль admin или both и флаг primary может задать только администратор. Результат проверки и поля регистратора и Cloudflare принадлежат серверу. Хост, который сейчас покупается, даёт 409.",
			UK: "Роль — admin, links або both, типово links; роль admin або both і прапорець primary може задати лише адміністратор. Результат перевірки та поля реєстратора й Cloudflare належать серверу. Хост, який зараз купується, дає 409.",
		},
		remove: catalogNote{
			EN: "A domain whose purchase is still pending answers 409.",
			RU: "Домен с неподтверждённой покупкой даёт 409.",
			UK: "Домен із непідтвердженою покупкою дає 409.",
		},
		conflicts: true,
	},
}

// campaignSample is a campaign record as the campaign routes show it.
var campaignSample = map[string]any{
	"id": "promo", "num": 1, "name": "Spring promo", "status": true, "aliases": []any{"spring"},
	"redirect": "stop", "uniq_method": "ip", "uniq_seconds": 86400,
	"streams": []any{map[string]any{"name": "Default", "status": true}},
}

// postbackSample is an outbound postback template as the campaign and S2S routes show it.
var postbackSample = map[string]any{
	"id": "pb-1", "name": "Lead to tracker", "url": "https://example.com/pb?subid={subid}&status={status}",
	"method": "GET", "statuses": []any{"lead", "sale"},
}

// campaignRouteDocs documents campaigns, their postback templates, S2S, spend, landing files,
// the domain check and the boards.
func campaignRouteDocs() map[string]routeDoc {
	docs := map[string]routeDoc{}
	for _, part := range []map[string]routeDoc{campaignCoreDocs(), landingFileDocs(), spendDocs(), boardDocs()} {
		maps.Copy(docs, part)
	}
	return docs
}

// campaignCoreDocs documents the campaigns, the postback templates of a campaign, the S2S
// catalog and the domain check and notices.
func campaignCoreDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/groups": {
			ID: "list_campaigns", Summary: "List campaigns.", MCP: mcpRead,
			Description: "Returns every campaign (group) the caller may see, with its streams.",
			RU: docText{
				Summary:     "Список кампаний.",
				Description: "Возвращает все кампании (группы), доступные вызывающему, вместе с потоками.",
			},
			UK: docText{
				Summary:     "Список кампаній.",
				Description: "Повертає всі кампанії (групи), доступні викликачу, разом із потоками.",
			},
			Query: []queryParam{strQ("shared", "1 returns {groups, lists, refs}: every long list filter of a stream (1 KB of text or more) is cut down to its flag, "+
				"lists holds each distinct one once as {raw, values}, and each refs entry [group index, stream index, rules key, lists index] puts it back.").
				loc("1 возвращает {groups, lists, refs}: каждый длинный списочный фильтр потока (от 1 КБ текста) урезан до флага, "+
					"lists хранит каждый различный список один раз как {raw, values}, а запись refs [индекс кампании, индекс потока, ключ в rules, индекс в lists] возвращает его на место.",
					"1 повертає {groups, lists, refs}: кожен довгий списковий фільтр потоку (від 1 КБ тексту) урізано до прапорця, "+
						"lists зберігає кожен різний список один раз як {raw, values}, а запис refs [індекс кампанії, індекс потоку, ключ у rules, індекс у lists] повертає його на місце.")},
			Response: []any{campaignSample},
			Errors:   []int{500},
		},
		"PUT /api/groups": {
			ID: "replace_campaigns", Summary: "Replace the whole campaign list.",
			Description: "Replaces the campaigns of the current section with the array in the body; a campaign left out of the array is removed. " +
				"Needs the groups_replace key action. The answer is {\"status\":\"saved\"}. " +
				"Sending an empty array when stored campaigns exist returns 409; use DELETE /api/groups/{id} to remove them one by one.",
			RU: docText{
				Summary: "Заменить весь список кампаний.",
				Description: "Заменяет кампании текущего раздела массивом из тела; кампания, которой нет в массиве, удаляется. " +
					"Нужна галочка ключа groups_replace. Ответ — {\"status\":\"saved\"}. " +
					"Пустой массив при наличии сохранённых кампаний даёт 409; удаляйте по одной через DELETE /api/groups/{id}.",
			},
			UK: docText{
				Summary: "Замінити весь список кампаній.",
				Description: "Замінює кампанії поточного розділу масивом із тіла; кампанія, якої немає в масиві, видаляється. " +
					"Потрібна позначка ключа groups_replace. Відповідь — {\"status\":\"saved\"}. " +
					"Порожній масив за наявності збережених кампаній дає 409; видаляйте по одній через DELETE /api/groups/{id}.",
			},
			Body:     campaignListBody,
			Example:  []any{campaignSample},
			Response: catalogSavedAnswer,
			Errors:   []int{400, 403, 409, 413, 500, 503},
		},
		"PUT /api/groups/{id}": {
			ID: "update_campaign", Summary: "Save one campaign.", MCP: mcpWrite,
			Description: "Replaces the campaign with this id, or adds it when it is new. " +
				"A different id in the body renames the stored campaign in one write: its place, section, owner and number stay, " +
				"and campaign links of other campaigns follow the new id. Clicks already logged keep the old id. " +
				"A taken id answers 409; an id with spaces, /, ? or # answers 400; renaming a missing campaign answers 404.",
			RU: docText{
				Summary: "Сохранить одну кампанию.",
				Description: "Заменяет кампанию с этим id или добавляет её, если такой ещё нет. " +
					"Другой id в теле переименовывает сохранённую кампанию одной записью: место, раздел, владелец и номер сохраняются, " +
					"ссылки других кампаний на неё переходят на новый id. Уже записанные клики остаются под старым id. " +
					"Занятый id — 409, id с пробелом, /, ? или # — 400, переименование несуществующей кампании — 404.",
			},
			UK: docText{
				Summary: "Зберегти одну кампанію.",
				Description: "Замінює кампанію з цим id або додає її, якщо такої ще немає. " +
					"Інший id у тілі перейменовує збережену кампанію одним записом: місце, розділ, власник і номер зберігаються, " +
					"посилання інших кампаній на неї переходять на новий id. Уже записані кліки лишаються під старим id. " +
					"Зайнятий id — 409, id із пробілом, /, ? або # — 400, перейменування неіснуючої кампанії — 404.",
			},
			Body: objectBody("Campaign record. Key fields: id, name, status (boolean), aliases, redirect, uniq_method (legacy ip or cookie), " +
				"uniq_by (ip or ip_ua), uniq_cookie (boolean), uniq_seconds, weight (boolean), streams (list of streams with name, on, rules and out)."),
			Example:  campaignSample,
			Response: catalogSavedAnswer,
			Errors:   []int{400, 403, 404, 409, 413, 500, 503},
		},
		"DELETE /api/groups/{id}": {
			ID: "delete_campaign", Summary: "Delete one campaign.", MCP: mcpWrite,
			Description: "Removes the campaign with this id; a missing id answers 404.",
			RU: docText{
				Summary:     "Удалить одну кампанию.",
				Description: "Удаляет кампанию с этим id; если такого id нет, ответ 404.",
			},
			UK: docText{
				Summary:     "Видалити одну кампанію.",
				Description: "Видаляє кампанію з цим id; якщо такого id немає, відповідь 404.",
			},
			Response: catalogDeletedAnswer,
			Errors:   []int{400, 403, 404, 500, 503},
		},
		"GET /api/campaigns/{id}/postbacks": {
			ID: "get_campaign_postbacks", Summary: "Get the outbound postback templates of a campaign.", MCP: mcpRead,
			Description: "Returns the outbound postback templates stored on the campaign; an empty list when it has none.",
			RU: docText{
				Summary:     "Получить исходящие постбек-шаблоны кампании.",
				Description: "Возвращает исходящие постбек-шаблоны кампании; пустой список, если их нет.",
			},
			UK: docText{
				Summary:     "Отримати вихідні постбек-шаблони кампанії.",
				Description: "Повертає вихідні постбек-шаблони кампанії; порожній список, якщо їх немає.",
			},
			Response: []any{postbackSample},
			Errors:   []int{404, 500, 503},
		},
		"PUT /api/campaigns/{id}/postbacks": {
			ID: "set_campaign_postbacks", Summary: "Replace the outbound postback templates of a campaign.",
			Description: "Replaces the whole list of templates of the campaign. Each template needs an http or https url and at least one status; " +
				"the method is GET or POST, GET by default.",
			RU: docText{
				Summary: "Заменить исходящие постбек-шаблоны кампании.",
				Description: "Заменяет весь список шаблонов кампании. У каждого шаблона нужен адрес http или https и хотя бы один статус; " +
					"метод — GET или POST, по умолчанию GET.",
			},
			UK: docText{
				Summary: "Замінити вихідні постбек-шаблони кампанії.",
				Description: "Замінює весь список шаблонів кампанії. Кожен шаблон має мати адресу http або https і хоча б один статус; " +
					"метод — GET або POST, типово GET.",
			},
			Body: campaignPostbacksBody, MCP: mcpWrite,
			Example:  []any{postbackSample},
			Response: []any{postbackSample},
			Errors:   []int{400, 403, 404, 500, 503},
		},
		"GET /api/s2s": {
			ID: "get_s2s", Summary: "Get the server-to-server postback templates.", MCP: mcpRead,
			Description: "Returns the shared server-to-server postback templates of the current section; campaigns pick them by id.",
			RU: docText{
				Summary:     "Получить серверные (S2S) постбек-шаблоны.",
				Description: "Возвращает общие серверные постбек-шаблоны текущего раздела; кампании выбирают их по id.",
			},
			UK: docText{
				Summary:     "Отримати серверні (S2S) постбек-шаблони.",
				Description: "Повертає спільні серверні постбек-шаблони поточного розділу; кампанії обирають їх за id.",
			},
			Response: []any{postbackSample},
			Errors:   []int{500, 503},
		},
		"PUT /api/s2s": {
			ID: "set_s2s", Summary: "Replace the server-to-server postback templates.",
			Description: "Replaces the shared templates the caller sees in the current section with the array in the body; a template left out is removed, " +
				"unless a campaign still uses it (400). An empty id is derived from the template. Administrators only. The answer is the saved list.",
			RU: docText{
				Summary: "Заменить серверные (S2S) постбек-шаблоны.",
				Description: "Заменяет общие шаблоны, которые вызывающий видит в текущем разделе, массивом из тела; пропущенный шаблон удаляется, " +
					"если его не использует кампания (иначе 400). Пустой id строится из самого шаблона. Только для администраторов. Ответ — сохранённый список.",
			},
			UK: docText{
				Summary: "Замінити серверні (S2S) постбек-шаблони.",
				Description: "Замінює спільні шаблони, які викликач бачить у поточному розділі, масивом із тіла; пропущений шаблон видаляється, " +
					"якщо його не використовує кампанія (інакше 400). Порожній id будується із самого шаблону. Лише для адміністраторів. Відповідь — збережений список.",
			},
			Body:     s2sPostbacksBody,
			Example:  []any{postbackSample},
			Response: []any{postbackSample},
			Errors:   []int{400, 403, 500, 503},
		},
		"POST /api/domains/{id}/check": {
			ID: "check_domain", Summary: "Recheck one domain.", MCP: mcpWrite,
			Description: "Probes DNS and the engine answer for the host and stores the result on the domain record.",
			RU: docText{
				Summary:     "Перепроверить один домен.",
				Description: "Проверяет DNS и ответ движка для хоста и сохраняет результат в записи домена.",
			},
			UK: docText{
				Summary:     "Перевірити один домен ще раз.",
				Description: "Перевіряє DNS і відповідь рушія для хоста та зберігає результат у записі домену.",
			},
			Response: map[string]any{
				"id": "d1", "host": "go.example.com", "group": "Main", "role": "links",
				"ok": true, "checked": "2026-11-16T10:30:00Z", "campaigns": 2,
			},
			Errors: []int{403, 404, 409, 500, 503},
		},
		"GET /api/domains/connected": {
			ID: "list_connected_domains", Summary: "List domains that started answering.",
			Description: "Lists the domains that began to answer as this tracker and whose notice nobody has acknowledged yet: id, host and connected_at. " +
				"A caller sees only the domains the grants allow.",
			RU: docText{
				Summary: "Список доменов, которые начали отвечать.",
				Description: "Возвращает домены, которые стали отвечать как этот трекер и о которых ещё никто не подтвердил уведомление: id, host и connected_at. " +
					"Вызывающий видит только домены, которые разрешают его права.",
			},
			UK: docText{
				Summary: "Список доменів, які почали відповідати.",
				Description: "Повертає домени, які стали відповідати як цей трекер і про які ще ніхто не підтвердив сповіщення: id, host і connected_at. " +
					"Викликач бачить лише домени, які дозволяють його права.",
			},
			Response: []any{map[string]any{"id": "d1", "host": "go.example.com", "connected_at": "2026-11-16T10:30:00Z"}},
			Errors:   []int{500, 503},
		},
	}
}

// landingFileDocs documents the file and archive routes of a local landing.
func landingFileDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/landings/{id}/files": {
			ID: "list_landing_files", Summary: "List the files of a local landing.", MCP: mcpRead,
			Description: "Returns the files of the landing directory with path, size in bytes and last change time in UTC, sorted by path; an empty list when it holds none. Size and time are read from disk on every call.",
			RU: docText{
				Summary:     "Список файлов локального лендинга.",
				Description: "Возвращает файлы папки лендинга с путём, размером в байтах и временем последнего изменения в UTC, по порядку путей; пустой список, если файлов нет. Размер и время читаются с диска при каждом запросе.",
			},
			UK: docText{
				Summary:     "Список файлів локального лендінгу.",
				Description: "Повертає файли теки лендінгу зі шляхом, розміром у байтах і часом останньої зміни в UTC, за порядком шляхів; порожній список, якщо файлів немає. Розмір і час читаються з диска під час кожного запиту.",
			},
			Response: []any{
				map[string]any{"path": "css/style.css", "size": 512, "modified": "2026-11-16T09:30:00Z"},
				map[string]any{"path": "index.html", "size": 1024, "modified": "2026-11-16T09:30:00Z"},
			},
			Errors: []int{400, 404, 500, 503},
		},
		"POST /api/landings/{id}/archive": {
			ID: "upload_landing_archive", Summary: "Replace the files of a local landing with a zip archive.",
			Description: "Replaces the whole directory of the landing with the files of a zip archive and stores the detected entry file on the landing. " +
				"Send the zip as the request body (application/zip) or as the multipart form field file, up to 50 MiB and 2000 files; " +
				"one shared top-level folder is dropped. Needs the landing_files key action. The answer holds the file count and the entry.",
			RU: docText{
				Summary: "Заменить файлы локального лендинга zip-архивом.",
				Description: "Заменяет всю папку лендинга файлами zip-архива и сохраняет найденный входной файл в лендинге. " +
					"Архив передаётся телом запроса (application/zip) или полем file формы multipart, до 50 МиБ и 2000 файлов; " +
					"единственная общая папка верхнего уровня отбрасывается. Нужна галочка ключа landing_files. Ответ — число файлов и входной файл.",
			},
			UK: docText{
				Summary: "Замінити файли локального лендінгу zip-архівом.",
				Description: "Замінює всю теку лендінгу файлами zip-архіву й зберігає знайдений вхідний файл у лендінгу. " +
					"Архів передається тілом запиту (application/zip) або полем file форми multipart, до 50 МіБ і 2000 файлів; " +
					"єдина спільна тека верхнього рівня відкидається. Потрібна позначка ключа landing_files. Відповідь — кількість файлів і вхідний файл.",
			},
			Response: map[string]any{"files": 2, "entry": "index.html"},
			Errors:   []int{400, 403, 404, 500, 503},
		},
		"GET /api/landings/{id}/files/{path...}": {
			ID: "read_landing_file", Summary: "Read one text file of a local landing.", MCP: mcpRead,
			Description: "The answer is the raw file text, up to 2 MiB, not JSON.",
			RU: docText{
				Summary:     "Прочитать один текстовый файл локального лендинга.",
				Description: "Ответ — сырой текст файла, до 2 МиБ, не JSON.",
			},
			UK: docText{
				Summary:     "Прочитати один текстовий файл локального лендінгу.",
				Description: "Відповідь — сирий текст файлу, до 2 МіБ, не JSON.",
			},
			Response: "<!doctype html>\n<html><body>Promo</body></html>\n",
			Errors:   []int{400, 404, 413, 500, 503},
		},
		"PUT /api/landings/{id}/files/{path...}": {
			ID: "write_landing_file", Summary: "Replace one text file of a local landing.", MCP: mcpWrite,
			Description: "The request body is the raw file text (text/plain), up to 2 MiB. Needs the landing_files key action.",
			RU: docText{
				Summary:     "Заменить один текстовый файл локального лендинга.",
				Description: "Тело запроса — сырой текст файла (text/plain), до 2 МиБ. Нужна галочка ключа landing_files.",
			},
			UK: docText{
				Summary:     "Замінити один текстовий файл локального лендінгу.",
				Description: "Тіло запиту — сирий текст файлу (text/plain), до 2 МіБ. Потрібна позначка ключа landing_files.",
			},
			RawBody:  true,
			Example:  "<!doctype html>\n<html><body>Promo</body></html>\n",
			Response: catalogSavedAnswer,
			Errors:   []int{400, 403, 404, 413, 500, 503},
		},
		"DELETE /api/landings/{id}/files/{path...}": {
			ID: "delete_landing_file", Summary: "Delete one file of a local landing.",
			Description: "Removes one file of the landing directory; a file that is already gone is still a success. Needs the landing_files key action.",
			RU: docText{
				Summary:     "Удалить один файл локального лендинга.",
				Description: "Удаляет один файл из папки лендинга; уже отсутствующий файл тоже считается успехом. Нужна галочка ключа landing_files.",
			},
			UK: docText{
				Summary:     "Видалити один файл локального лендінгу.",
				Description: "Видаляє один файл із теки лендінгу; вже відсутній файл теж вважається успіхом. Потрібна позначка ключа landing_files.",
			},
			Response: catalogDeletedAnswer,
			Errors:   []int{400, 403, 404, 500, 503},
		},
	}
}

// spendSample is a saved cost entry as the spend routes show it.
var spendSample = map[string]any{
	"id": "1763300000000000000", "group_id": "promo", "zone": "Europe/Kyiv",
	"from": "2026-11-09T22:00:00Z", "to": "2026-11-10T22:00:00Z",
	"cents": 2550, "currency": "USD", "unique": false,
	"buckets": []any{map[string]any{"stream": "Default", "offer_id": "o1", "network_id": "n1", "cents": 2550}},
}

// spendDocs documents the cost routes of the campaigns.
func spendDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/spend": {
			ID: "list_spend", Summary: "List the saved campaign costs.",
			Description: "Returns the saved cost entries of the campaigns the caller may see, each with the amount split into stream and offer buckets. " +
				"Answers 403 when the caller may not see cost.",
			RU: docText{
				Summary: "Список сохранённых расходов кампаний.",
				Description: "Возвращает сохранённые записи расхода кампаний, которые видит вызывающий, каждую с разбивкой суммы по потокам и офферам. " +
					"Если вызывающему расход скрыт, ответ 403.",
			},
			UK: docText{
				Summary: "Список збережених витрат кампаній.",
				Description: "Повертає збережені записи витрат кампаній, які бачить викликач, кожну з розбивкою суми за потоками й оферами. " +
					"Якщо викликачу витрати приховані, відповідь 403.",
			},
			Response: []any{spendSample},
			Errors:   []int{403, 500},
		},
		"POST /api/spend": {
			ID: "create_spend", Summary: "Save the cost of a campaign for a period.",
			Description: "Saves the cost of one campaign for a closed period that ended in the past and started at most 21 days ago, and splits it by the clicks of that period. " +
				"The split is a snapshot and is not recomputed later. The first entry sets the currency of the campaign, and periods of one campaign must not overlap. " +
				"The amount has at most two decimals; the currency is USD, EUR, GBP, RUB or UAH. Answers 403 when the caller may not see cost or may not change the campaign.",
			RU: docText{
				Summary: "Сохранить расход кампании за период.",
				Description: "Сохраняет расход одной кампании за закрытый период, который закончился в прошлом и начался не раньше 21 дня назад, и делит его по кликам этого периода. " +
					"Разбивка — снимок, позже она не пересчитывается. Первая запись задаёт валюту кампании, периоды одной кампании не должны пересекаться. " +
					"В сумме не больше двух знаков после запятой; валюта — USD, EUR, GBP, RUB или UAH. Если вызывающему расход скрыт или кампанию менять нельзя, ответ 403.",
			},
			UK: docText{
				Summary: "Зберегти витрату кампанії за період.",
				Description: "Зберігає витрату однієї кампанії за закритий період, який закінчився в минулому й почався не раніше ніж 21 день тому, та ділить її за кліками цього періоду. " +
					"Розбивка — знімок, пізніше її не перераховують. Перший запис задає валюту кампанії, періоди однієї кампанії не повинні перетинатися. " +
					"У сумі не більше двох знаків після коми; валюта — USD, EUR, GBP, RUB або UAH. Якщо викликачу витрати приховані або кампанію змінювати не можна, відповідь 403.",
			},
			Body: objectBody("Cost entry. Fields: group_id, zone (IANA time zone, for example Europe/Kyiv), from and to (local time like 2026-11-10T00:00), " +
				"amount (text with at most two decimals), currency, unique (split by unique clicks), include_bots."),
			Example: map[string]any{
				"group_id": "promo", "zone": "Europe/Kyiv", "from": "2026-11-10T00:00", "to": "2026-11-11T00:00",
				"amount": "25.50", "currency": "USD", "unique": false, "include_bots": false,
			},
			Status:   201,
			Response: spendSample,
			Errors:   []int{400, 403, 404, 500, 503, 504},
		},
		"DELETE /api/spend/{id}": {
			ID: "delete_spend", Summary: "Delete one saved campaign cost.",
			Description: "Removes one saved cost entry by id; the other entries keep their buckets. The answer is empty (204).",
			RU: docText{
				Summary:     "Удалить одну сохранённую запись расхода.",
				Description: "Удаляет одну сохранённую запись расхода по id; остальные записи сохраняют свои доли. Ответ пустой (204).",
			},
			UK: docText{
				Summary:     "Видалити один збережений запис витрат.",
				Description: "Видаляє один збережений запис витрат за id; інші записи зберігають свої частки. Відповідь порожня (204).",
			},
			Status: 204,
			Errors: []int{403, 404, 500, 503},
		},
	}
}

// boardSample is a panel section as the board routes show it.
var boardSample = map[string]any{"id": "sales", "name": "Sales", "icon": "wallet", "icon_color": "profit"}

// boardPickFields is the sentence about the record lists shared by the import and move requests.
const boardPickFields = "campaigns, offers, landings, networks, sources and s2s are lists of record ids; a list left out picks nothing."

// boardCountsSample is a sample of the per-kind counters of a board import or move.
var boardCountsSample = map[string]any{"campaigns": 1, "offers": 1, "landings": 0, "networks": 0, "sources": 0, "s2s": 0}

// boardDocs documents the panel section (board) routes.
func boardDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/boards": {
			ID: "list_boards", Summary: "List the panel sections (boards) the caller may see.", MCP: mcpRead,
			Description: "Returns the sections the caller may open, the current section, the default one and whether the caller can switch between them. " +
				"The section of a request comes from the X-Emtds-Board header. A user who may open no section gets empty items, current and default_id; " +
				"the section data routes then answer 403 no boards.",
			RU: docText{
				Summary: "Список разделов панели (boards), доступных вызывающему.",
				Description: "Возвращает разделы, которые вызывающий может открыть, текущий и стандартный разделы и признак, можно ли между ними переключаться. " +
					"Раздел запроса берётся из заголовка X-Emtds-Board. Пользователь без доступных разделов получает пустые items, current и default_id; " +
					"маршруты с данными раздела тогда отвечают 403 no boards.",
			},
			UK: docText{
				Summary: "Список розділів панелі (boards), доступних викликачу.",
				Description: "Повертає розділи, які викликач може відкрити, поточний і типовий розділи та ознаку, чи можна між ними перемикатися. " +
					"Розділ запиту береться із заголовка X-Emtds-Board. Користувач без доступних розділів отримує порожні items, current і default_id; " +
					"маршрути з даними розділу тоді відповідають 403 no boards.",
			},
			Response: map[string]any{
				"items": []any{
					map[string]any{"id": "main", "name": "По умолчанию", "include_all": true},
					boardSample,
				},
				"current": "main", "default_id": "main", "switchable": true,
			},
			Errors: []int{500},
		},
		"POST /api/boards": {
			ID: "create_board", Summary: "Create a panel section.",
			Description: "Adds a section; its id is built from the name. The main section cannot be created again. " +
				"Needs the boards key action. Administrators only.",
			RU: docText{
				Summary:     "Создать раздел панели.",
				Description: "Добавляет раздел; его id строится из названия. Основной раздел создать повторно нельзя. Нужна галочка ключа boards. Только для администраторов.",
			},
			UK: docText{
				Summary:     "Створити розділ панелі.",
				Description: "Додає розділ; його id будується з назви. Основний розділ створити повторно не можна. Потрібна позначка ключа boards. Лише для адміністраторів.",
			},
			Body:     objectBody("Section. Fields: name (required), icon (an icon key), icon_color (a tint key)."),
			Example:  map[string]any{"name": "Sales", "icon": "wallet", "icon_color": "profit"},
			Status:   201,
			Response: boardSample,
			Errors:   []int{400, 403, 503},
		},
		"PUT /api/boards/order": {
			ID: "order_boards", Summary: "Save the order of the panel sections.",
			Description: "Saves the order of the sections; ids lists every section except main exactly once, main always stays first. " +
				"Needs the boards key action. Administrators only.",
			RU: docText{
				Summary:     "Сохранить порядок разделов панели.",
				Description: "Сохраняет порядок разделов; ids перечисляет каждый раздел, кроме main, ровно один раз, main всегда остаётся первым. Нужна галочка ключа boards. Только для администраторов.",
			},
			UK: docText{
				Summary:     "Зберегти порядок розділів панелі.",
				Description: "Зберігає порядок розділів; ids перелічує кожен розділ, крім main, рівно один раз, main завжди лишається першим. Потрібна позначка ключа boards. Лише для адміністраторів.",
			},
			Body:    objectBody("Order. Field: ids, the section ids in the new order without main."),
			Example: map[string]any{"ids": []any{"sales"}},
			Response: map[string]any{"items": []any{
				map[string]any{"id": "main", "name": "По умолчанию", "include_all": true},
				boardSample,
			}},
			Errors: []int{400, 403, 503},
		},
		"PATCH /api/boards/{id}": {
			ID: "update_board", Summary: "Change one panel section.",
			Description: "Changes the name, icon, icon tint or, on the main section only, include_all; a field left out stays as it is. " +
				"The main section cannot be renamed. Needs the boards key action. Administrators only.",
			RU: docText{
				Summary: "Изменить один раздел панели.",
				Description: "Меняет название, значок, цвет значка или, только у основного раздела, include_all; пропущенное поле остаётся как было. " +
					"Основной раздел переименовать нельзя. Нужна галочка ключа boards. Только для администраторов.",
			},
			UK: docText{
				Summary: "Змінити один розділ панелі.",
				Description: "Змінює назву, значок, колір значка або, лише в основного розділу, include_all; пропущене поле лишається як було. " +
					"Основний розділ перейменувати не можна. Потрібна позначка ключа boards. Лише для адміністраторів.",
			},
			Body:     objectBody("Changes. Optional fields: name, icon, icon_color (an empty string clears it), include_all (boolean, main only)."),
			Example:  map[string]any{"name": "Sales team", "icon": "wallet"},
			Response: map[string]any{"id": "sales", "name": "Sales team", "icon": "wallet", "icon_color": "profit"},
			Errors:   []int{400, 403, 404, 503},
		},
		"DELETE /api/boards/{id}": {
			ID: "delete_board", Summary: "Delete one panel section.",
			Description: "Removes an empty section; a section that still holds records answers 409 and the main section cannot be deleted. " +
				"Needs the boards key action. Administrators only.",
			RU: docText{
				Summary: "Удалить один раздел панели.",
				Description: "Удаляет пустой раздел; раздел с записями даёт 409, а основной раздел удалить нельзя. " +
					"Нужна галочка ключа boards. Только для администраторов.",
			},
			UK: docText{
				Summary: "Видалити один розділ панелі.",
				Description: "Видаляє порожній розділ; розділ із записами дає 409, а основний розділ видалити не можна. " +
					"Потрібна позначка ключа boards. Лише для адміністраторів.",
			},
			Response: catalogDeletedAnswer,
			Errors:   []int{400, 403, 404, 409, 500, 503},
		},
		"PUT /api/boards/{id}/members": {
			ID: "set_board_members", Summary: "Replace the members of a panel section.",
			Description: "Replaces the member list of the section; each member is a user id with the role read, edit or created, listed once. " +
				"The board_ids of each affected user is rebuilt from the sections other than the main one where the user is a member; " +
				"the main section of a user (no_main_board) never changes here. Needs the boards key action. Administrators only. The answer is the section with its members.",
			RU: docText{
				Summary: "Заменить участников раздела панели.",
				Description: "Заменяет список участников раздела; участник — id пользователя с ролью read, edit или created, каждый один раз. " +
					"board_ids каждого затронутого пользователя пересобирается из разделов, кроме основного, где он участник; " +
					"основной раздел пользователя (no_main_board) здесь не меняется. Нужна галочка ключа boards. Только для администраторов. Ответ — раздел с участниками.",
			},
			UK: docText{
				Summary: "Замінити учасників розділу панелі.",
				Description: "Замінює список учасників розділу; учасник — id користувача з роллю read, edit або created, кожен один раз. " +
					"board_ids кожного зачепленого користувача перебудовується з розділів, крім основного, де він учасник; " +
					"основний розділ користувача (no_main_board) тут не змінюється. Потрібна позначка ключа boards. Лише для адміністраторів. Відповідь — розділ з учасниками.",
			},
			Body:    objectBody("Members. Field: members, a list of {user_id, role}."),
			Example: map[string]any{"members": []any{map[string]any{"user_id": "u1", "role": "edit"}}},
			Response: map[string]any{
				"id": "sales", "name": "Sales", "icon": "wallet", "icon_color": "profit",
				"members": []any{map[string]any{"user_id": "u1", "role": "edit"}},
			},
			Errors: []int{400, 403, 404, 500, 503},
		},
		"POST /api/boards/{id}/assign": {
			ID: "assign_board_items", Summary: "Set the records of a panel section.",
			Description: "Sets the full composition of the section: the listed campaigns, folders, group labels and record ids are put into it, " +
				"and records that were in it but are not listed go back to the main section. Records of other sections are not touched. " +
				"The main section cannot be assigned. Needs the boards key action. Administrators only.",
			RU: docText{
				Summary: "Задать записи раздела панели.",
				Description: "Задаёт полный состав раздела: перечисленные кампании, папки, метки групп и id записей переносятся в него, " +
					"а записи, которые были в нём, но не перечислены, возвращаются в основной раздел. Записи других разделов не затрагиваются. " +
					"Основному разделу состав задать нельзя. Нужна галочка ключа boards. Только для администраторов.",
			},
			UK: docText{
				Summary: "Задати записи розділу панелі.",
				Description: "Задає повний склад розділу: перелічені кампанії, теки, мітки груп та id записів переносяться до нього, " +
					"а записи, які були в ньому, але не перелічені, повертаються в основний розділ. Записи інших розділів не зачіпаються. " +
					"Основному розділу склад задати не можна. Потрібна позначка ключа boards. Лише для адміністраторів.",
			},
			Body: objectBody("Composition. Fields: folders (campaign folders), groups (label lists under offers, landings, networks and sources), " +
				"ids (id lists under campaigns, offers, landings, networks, sources and s2s)."),
			Example: map[string]any{
				"folders": []any{"Spring"},
				"groups":  map[string]any{"offers": []any{"Spring"}},
				"ids":     map[string]any{"campaigns": []any{"promo"}, "landings": []any{"l1"}},
			},
			Response: catalogSavedAnswer,
			Errors:   []int{400, 403, 404, 500},
		},
		"POST /api/boards/import": {
			ID: "import_board_items", Summary: "Copy records from another panel section into the current one.",
			Description: "Copies the picked records from the section named in from into the section of the request (the X-Emtds-Board header). " +
				"The copies get new ids and their links are remapped; a record that already exists in the target and a link that cannot be kept are reported in dropped. " +
				boardPickFields + " Needs the boards key action.",
			RU: docText{
				Summary: "Скопировать записи из другого раздела панели в текущий.",
				Description: "Копирует выбранные записи из раздела from в раздел запроса (заголовок X-Emtds-Board). " +
					"Копии получают новые id, их связи переназначаются; запись, которая уже есть в целевом разделе, и связь, которую нельзя сохранить, попадают в dropped. " +
					"campaigns, offers, landings, networks, sources и s2s — списки id записей; пропущенный список ничего не выбирает. Нужна галочка ключа boards.",
			},
			UK: docText{
				Summary: "Скопіювати записи з іншого розділу панелі в поточний.",
				Description: "Копіює вибрані записи з розділу from до розділу запиту (заголовок X-Emtds-Board). " +
					"Копії отримують нові id, їхні зв'язки перепризначаються; запис, який уже є в цільовому розділі, і зв'язок, який не можна зберегти, потрапляють у dropped. " +
					"campaigns, offers, landings, networks, sources і s2s — списки id записів; пропущений список нічого не вибирає. Потрібна позначка ключа boards.",
			},
			Body: objectBody("Pick. Fields: from (source section id), " + boardPickFields),
			Example: map[string]any{
				"from": "sales", "campaigns": []any{"promo"}, "offers": []any{"o1"},
			},
			Response: map[string]any{
				"copied": boardCountsSample,
				"map": map[string]any{
					"campaigns": map[string]any{"promo": "flow2"},
					"offers":    map[string]any{"o1": "5"},
				},
				"dropped": []any{map[string]any{"kind": "campaigns", "id": "promo", "reason": "campaign not copied"}},
			},
			Errors: []int{400, 403, 500, 503},
		},
		"POST /api/boards/move": {
			ID: "move_board_items", Summary: "Move records from the current panel section into another one.",
			Description: "Moves the picked records from the section of the request (the X-Emtds-Board header) into the section named in to; ids stay the same. " +
				"A link between campaigns that ends up in different sections is cleared and reported in dropped. " +
				boardPickFields + " Needs the boards key action.",
			RU: docText{
				Summary: "Переместить записи из текущего раздела панели в другой.",
				Description: "Перемещает выбранные записи из раздела запроса (заголовок X-Emtds-Board) в раздел to; id остаются прежними. " +
					"Связь между кампаниями, которые оказываются в разных разделах, сбрасывается и попадает в dropped. " +
					"campaigns, offers, landings, networks, sources и s2s — списки id записей; пропущенный список ничего не выбирает. Нужна галочка ключа boards.",
			},
			UK: docText{
				Summary: "Перемістити записи з поточного розділу панелі в інший.",
				Description: "Переміщує вибрані записи з розділу запиту (заголовок X-Emtds-Board) до розділу to; id лишаються тими самими. " +
					"Зв'язок між кампаніями, які опиняються в різних розділах, скидається й потрапляє в dropped. " +
					"campaigns, offers, landings, networks, sources і s2s — списки id записів; пропущений список нічого не вибирає. Потрібна позначка ключа boards.",
			},
			Body: objectBody("Pick. Fields: to (target section id), " + boardPickFields),
			Example: map[string]any{
				"to": "sales", "campaigns": []any{"promo"}, "offers": []any{"o1"},
			},
			Response: map[string]any{
				"moved":   boardCountsSample,
				"dropped": []any{map[string]any{"kind": "campaigns", "id": "promo", "reason": "campaign not copied"}},
			},
			Errors: []int{400, 403, 500, 503},
		},
	}
}
