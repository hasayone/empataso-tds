package admin

// geoToolRouteDocs documents the geo database state and download of the Tools page.
func geoToolRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/tools/geo": {
			ID: "get_tools_geo", Summary: "Geo databases: what is loaded and how each one is downloaded.",
			Description: "Lists the city, MaxMind ASN and DB-IP ASN databases with their state (loaded, type, build date, file) and " +
				"the source that fills each in the cron configuration: source (maxmind, dbip or custom), ready (a geo_db run downloads it), " +
				"needs_key (only MaxMind sources without a license key) and free_alt (dbip: the city database can come from DB-IP City Lite, CC BY 4.0, no account). " +
				"job is the last geo_db run of the cron service, alive whether the service answers, can_download whether the caller may call POST /api/tools/geo/download.",
			RU: docText{
				Summary: "Гео-базы: что загружено и откуда каждая скачивается.",
				Description: "Список баз города, ASN MaxMind и ASN DB-IP с состоянием (загружена, тип, дата сборки, файл) и " +
					"источником в настройках cron: source (maxmind, dbip или custom), ready (запуск geo_db её скачает), " +
					"needs_key (только источники MaxMind без лицензионного ключа) и free_alt (dbip: базу города можно взять из DB-IP City Lite, CC BY 4.0, без аккаунта). " +
					"job — последний запуск geo_db службы cron, alive — отвечает ли служба, can_download — может ли вызывающий вызвать POST /api/tools/geo/download.",
			},
			UK: docText{
				Summary: "Гео-бази: що завантажено і звідки кожна завантажується.",
				Description: "Список баз міста, ASN MaxMind та ASN DB-IP зі станом (завантажена, тип, дата збирання, файл) і " +
					"джерелом у налаштуваннях cron: source (maxmind, dbip або custom), ready (запуск geo_db її завантажить), " +
					"needs_key (лише джерела MaxMind без ліцензійного ключа) і free_alt (dbip: базу міста можна взяти з DB-IP City Lite, CC BY 4.0, без облікового запису). " +
					"job — останній запуск geo_db служби cron, alive — чи відповідає служба, can_download — чи може викликаючий викликати POST /api/tools/geo/download.",
			},
			Response: map[string]any{
				"databases": []any{map[string]any{
					"kind": "city", "path": "/var/lib/emtds/geo/city.mmdb", "type": "", "built": "0001-01-01T00:00:00Z", "loaded": false,
					"source": "maxmind", "ready": false, "needs_key": true, "free_alt": "dbip",
				}},
				"can_download": true, "cron": true, "alive": true,
				"job": map[string]any{"last_run": "2026-11-16T10:20:00Z", "ok": true, "message": "dbip_asn: DBIP-ASN-Lite (compat=GeoLite2-ASN) updated",
					"duration_ms": 5200, "next_run": "2026-11-19T10:20:00Z", "running": false},
			},
			MCP: mcpRead,
		},
		geoDownloadPattern: {
			ID: "download_geo_database", Summary: "Download a geo database now.",
			Description: "Queues the geo_db job of the cron service, the same job the schedule runs; the answer is 202 and the result appears in GET /api/tools/geo. " +
				"kind is city, asn or dbip_asn. source dbip (city only) first replaces the MaxMind city source without a key in the cron configuration with DB-IP City Lite. " +
				"The engine picks the new file up within its reload interval (a minute by default). " +
				"409 carries params.reason: needs_key (a MaxMind license key is required: set it on the Automation page), " +
				"no_path (the cron service has no file for this database) or source_set (a working city source is already configured). " +
				"503 means the cron service is not configured. API keys need the cron action.",
			RU: docText{
				Summary: "Скачать гео-базу сейчас.",
				Description: "Ставит в очередь задачу geo_db службы cron — ту же, что запускает расписание; ответ 202, результат появится в GET /api/tools/geo. " +
					"kind — city, asn или dbip_asn. source dbip (только для city) сначала заменяет в настройках cron источник города MaxMind без ключа на DB-IP City Lite. " +
					"Движок подхватит новый файл за свой интервал перечитывания (по умолчанию минута). " +
					"409 несёт params.reason: needs_key (нужен лицензионный ключ MaxMind — задайте его на странице «Автоматизация»), " +
					"no_path (у службы cron нет файла для этой базы) или source_set (рабочий источник города уже настроен). " +
					"503 — служба cron не настроена. API-ключам нужно действие cron.",
			},
			UK: docText{
				Summary: "Завантажити гео-базу зараз.",
				Description: "Ставить у чергу завдання geo_db служби cron — те саме, що запускає розклад; відповідь 202, результат з'явиться в GET /api/tools/geo. " +
					"kind — city, asn або dbip_asn. source dbip (лише для city) спершу замінює в налаштуваннях cron джерело міста MaxMind без ключа на DB-IP City Lite. " +
					"Рушій підхопить новий файл за свій інтервал перечитування (за замовчуванням хвилина). " +
					"409 містить params.reason: needs_key (потрібен ліцензійний ключ MaxMind — задайте його на сторінці «Автоматизація»), " +
					"no_path (у служби cron немає файлу для цієї бази) або source_set (робоче джерело міста вже налаштоване). " +
					"503 — служба cron не налаштована. API-ключам потрібна дія cron.",
			},
			Body: map[string]any{
				"type": "object", "required": []any{"kind"},
				"properties": map[string]any{
					"kind":   map[string]any{"type": "string", "enum": []any{"city", "asn", "dbip_asn"}},
					"source": map[string]any{"type": "string", "enum": []any{"", "dbip"}},
				},
			},
			Example:  map[string]any{"kind": "city", "source": "dbip"},
			Status:   202,
			Response: map[string]any{"status": "requested"},
			Errors:   []int{400, 409, 500, 503},
		},
	}
}

// lookupHistoryRouteDocs documents the IP check history of the Tools page.
func lookupHistoryRouteDocs() map[string]routeDoc {
	userQ := strQ("user", "History id of one author (only the owner may name someone else).").
		loc("Id автора в истории (чужой id может указать только владелец).", "Id автора в історії (чужий id може вказати лише власник).")
	return map[string]routeDoc{
		"GET /api/tools/lookups": {
			ID: "list_ip_lookups", Summary: "History of IP checks on the Tools page.",
			Description: "One page of checks, the newest first: who and when, the address and a short result (country, city, ASN, organization, proxy, lists). " +
				"A user sees their own checks; the owner (the administrator of the installation) sees everyone's, gets users for the filter and may pass user. " +
				"Anyone else naming another user is 403. The history keeps 2000 checks and 90 days.",
			RU: docText{
				Summary: "История проверок IP в «Инструментах».",
				Description: "Одна страница проверок, сначала новые: кто и когда, адрес и краткий результат (страна, город, ASN, организация, прокси, списки). " +
					"Пользователь видит свои проверки; владелец (администратор установки) — все, получает users для фильтра и может передать user. " +
					"Остальным чужой user даёт 403. История хранит 2000 проверок и 90 дней.",
			},
			UK: docText{
				Summary: "Історія перевірок IP в «Інструментах».",
				Description: "Одна сторінка перевірок, спочатку нові: хто і коли, адреса й короткий результат (країна, місто, ASN, організація, проксі, списки). " +
					"Користувач бачить свої перевірки; власник (адміністратор встановлення) — усі, отримує users для фільтра і може передати user. " +
					"Іншим чужий user дає 403. Історія зберігає 2000 перевірок і 90 днів.",
			},
			Query: []queryParam{
				userQ,
				intQ("offset", "Rows to skip.").loc("Сколько строк пропустить.", "Скільки рядків пропустити."),
				intQ("limit", "Rows to return, at most 200. Default: 20.").
					loc("Сколько строк вернуть, не больше 200. По умолчанию 20.", "Скільки рядків повернути, не більше 200. За замовчуванням 20."),
			},
			Response: map[string]any{
				"rows": []any{map[string]any{
					"ts": "2026-11-16T10:20:00Z", "uid": "root", "login": "admin", "ip": "203.0.113.10", "country": "us",
					"city": "Austin", "asn": 64500, "org": "Example ISP", "lists": []any{"wap"},
				}},
				"total": 1, "owner": true, "users": []any{map[string]any{"uid": "root", "login": "admin"}},
			},
			Errors: []int{403},
			MCP:    mcpRead,
		},
		"DELETE /api/tools/lookups": {
			ID: "clear_ip_lookups", Summary: "Clear the IP check history.",
			Description: "Removes every check, or the checks of user. Only the owner signed in to the panel may; anyone else, API keys included, gets 403. The answer counts the removed checks.",
			RU: docText{
				Summary:     "Очистить историю проверок IP.",
				Description: "Удаляет все проверки или проверки пользователя user. Только владелец, вошедший в панель; остальным, включая API-ключи, 403. Ответ — число удалённых проверок.",
			},
			UK: docText{
				Summary:     "Очистити історію перевірок IP.",
				Description: "Видаляє всі перевірки або перевірки користувача user. Лише власник, який увійшов у панель; іншим, зокрема API-ключам, 403. Відповідь — кількість видалених перевірок.",
			},
			Query:    []queryParam{userQ},
			Response: map[string]any{"deleted": 12},
			Errors:   []int{403, 500},
		},
	}
}
