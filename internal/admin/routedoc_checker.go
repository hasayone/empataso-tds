package admin

// checkerRouteDocs documents the checker integration routes: the settings, the connection test,
// the keyword fill and the application of a campaign. Every route needs a browser session, so only
// the summaries and descriptions are listed, in English, Russian and Ukrainian.
func checkerRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/checker/settings": cookieDoc("get_checker_settings",
			docOf("Get the checker integration settings.",
				"Returns base_url, tenant_id, has_token, enabled, create_apps, admin_url, app_url, locales, default_locales, "+
					"postback_url, postback_default, postback_method, has_postback_token, paused and the queue counters; the tokens are never returned. "+
					"error is set when the saved file could not be used."),
			docOf("Настройки интеграции с чекером.",
				"Возвращает base_url, tenant_id, has_token, enabled, create_apps, admin_url, app_url, locales, default_locales, "+
					"postback_url, postback_default, postback_method, has_postback_token, paused и счётчики очереди; токены не возвращаются никогда. "+
					"error задан, если сохранённый файл не удалось использовать."),
			docOf("Налаштування інтеграції з чекером.",
				"Повертає base_url, tenant_id, has_token, enabled, create_apps, admin_url, app_url, locales, default_locales, "+
					"postback_url, postback_default, postback_method, has_postback_token, paused і лічильники черги; токени не повертаються ніколи. "+
					"error заданий, якщо збережений файл не вдалося використати.")),
		"PUT /api/checker/settings": cookieDoc("put_checker_settings",
			docOf("Save the checker integration settings.",
				"A missing field keeps the stored value; an empty or masked token and postback_token keep the stored ones. base_url is https and ends in .checkmy.store. "+
					"The save lifts the pause after a refused token and brings the postback templates of the s2s catalog in line. Answers like GET; 400 for a bad field, 503 when the settings file is not set."),
			docOf("Сохранение настроек интеграции с чекером.",
				"Непереданное поле остаётся прежним; пустой или замаскированный token и postback_token оставляют прежние. base_url — https и оканчивается на .checkmy.store. "+
					"Сохранение снимает паузу после отклонённого токена и приводит шаблоны постбека в каталоге s2s в соответствие. Ответ как у GET; 400 для неверного поля, 503, если файл настроек не задан."),
			docOf("Збереження налаштувань інтеграції з чекером.",
				"Непередане поле залишається попереднім; порожній або замаскований token і postback_token залишають попередні. base_url — https і закінчується на .checkmy.store. "+
					"Збереження знімає паузу після відхиленого токена і приводить шаблони постбеку в каталозі s2s у відповідність. Відповідь як у GET; 400 для хибного поля, 503, якщо файл налаштувань не заданий.")),
		"POST /api/checker/test": cookieDoc("test_checker",
			docOf("Check the connection to the checker.",
				"The body is shaped like the PUT body; missing fields and a masked token fall back to the saved ones. Nothing is saved. "+
					"409 while the integration is off, with no request sent. A refused call is still 200 with ok false and the reason in error; success has the name and the email of the token owner in user."),
			docOf("Проверка связи с чекером.",
				"Тело как у PUT; пропущенные поля и замаскированный токен берутся из сохранённых. Ничего не сохраняется. "+
					"409, пока интеграция выключена, запрос не уходит. Отклонённый вызов — всё равно 200 с ok равным false и причиной в error; при успехе в user имя и почта владельца токена."),
			docOf("Перевірка зв'язку з чекером.",
				"Тіло як у PUT; пропущені поля і замаскований токен беруться зі збережених. Нічого не зберігається. "+
					"409, поки інтеграція вимкнена, запит не йде. Відхилений виклик — усе одно 200 з ok, рівним false, і причиною в error; за успіху в user ім'я та пошта власника токена.")),
		"GET /api/checker/keywords/preview": cookieDoc("preview_checker_keywords",
			docOf("Preview the keywords read from campaign names.",
				"Returns campaigns, fill, keep, no_key, keys (every keyword with its campaign count) and up to 50 sample campaigns. Nothing is changed and no request leaves the server."),
			docOf("Предпросмотр ключей, прочитанных из названий кампаний.",
				"Возвращает campaigns, fill, keep, no_key, keys (каждый ключ с числом кампаний) и до 50 примеров. Ничего не меняется, запросов наружу нет."),
			docOf("Попередній перегляд ключів, прочитаних із назв кампаній.",
				"Повертає campaigns, fill, keep, no_key, keys (кожен ключ із кількістю кампаній) і до 50 прикладів. Нічого не змінюється, запитів назовні немає.")),
		"POST /api/checker/keywords/fill": cookieDoc("fill_checker_keywords",
			docOf("Fill the empty keywords of campaigns from their names.",
				"Body confirm true, otherwise 400. Only an empty keyword is filled; a filled one and a record that is being sent or linked stay. One write of the file. Answers filled, kept and no_key. Works while the integration is off; no request leaves the server."),
			docOf("Заполнение пустых ключей кампаний по их названиям.",
				"Тело: confirm равный true, иначе 400. Заполняется только пустой ключ; заполненный и запись, которая отправляется или уже связана, остаются. Одна запись файла. Ответ: filled, kept и no_key. Работает и при выключенной интеграции; запросов наружу нет."),
			docOf("Заповнення порожніх ключів кампаній за їхніми назвами.",
				"Тіло: confirm, рівний true, інакше 400. Заповнюється лише порожній ключ; заповнений і запис, що надсилається або вже зв'язаний, лишаються. Один запис файлу. Відповідь: filled, kept і no_key. Працює й за вимкненої інтеграції; запитів назовні немає.")),
		"GET /api/checker/status": cookieDoc("get_checker_status",
			docOf("Tell whether the checker block and the checker postback are offered.",
				"Returns enabled, create_apps and postback with available, method and url_masked; the token in url_masked is hidden."),
			docOf("Показ блока чекера и постбека чекера.",
				"Возвращает enabled, create_apps и postback с available, method и url_masked; токен в url_masked скрыт."),
			docOf("Показ блока чекера і постбеку чекера.",
				"Повертає enabled, create_apps і postback з available, method і url_masked; токен у url_masked приховано.")),
		"GET /api/checker/consoles": cookieDoc("list_checker_consoles",
			docOf("List the consoles of the checker.",
				"Returns [{id, name}] kept for ten minutes. 409 while the integration is off; 502 with the text of the checker when the call fails."),
			docOf("Список консолей чекера.",
				"Возвращает [{id, name}], список хранится десять минут. 409, пока интеграция выключена; 502 с текстом чекера, если вызов не удался."),
			docOf("Список консолей чекера.",
				"Повертає [{id, name}], список зберігається десять хвилин. 409, поки інтеграція вимкнена; 502 з текстом чекера, якщо виклик не вдався.")),
		"GET /api/checker/apps/{group}": cookieDoc("get_checker_app",
			docOf("Get the checker application of a campaign.",
				"Returns integration, app (the record or null), prefill, foreign, app_url and retry_after. 404 for a campaign that is missing or hidden. No request leaves the server."),
			docOf("Приложение кампании в чекере.",
				"Возвращает integration, app (запись или null), prefill, foreign, app_url и retry_after. 404 для отсутствующей или скрытой кампании. Запросов наружу нет."),
			docOf("Застосунок кампанії в чекері.",
				"Повертає integration, app (запис або null), prefill, foreign, app_url і retry_after. 404 для відсутньої або прихованої кампанії. Запитів назовні немає.")),
		"PUT /api/checker/apps/{group}": cookieDoc("put_checker_app",
			docOf("Save the checker fields of a campaign.",
				"Body keyword, bundle_id, notion_url, console_id and console_name. Only a draft, a queued record or an error can change; a record that is being sent or linked answers 409. "+
					"400 for a keyword with a colon or a line break or a bundle outside ^[a-z0-9.]+$; 409 when another campaign holds the bundle. Answers like GET."),
			docOf("Сохранение полей чекера у кампании.",
				"Тело: keyword, bundle_id, notion_url, console_id и console_name. Менять можно черновик, запись в очереди или ошибку; запись, которая отправляется или связана, даёт 409. "+
					"400 для ключа с двоеточием или переводом строки либо bundle вне ^[a-z0-9.]+$; 409, если bundle у другой кампании. Ответ как у GET."),
			docOf("Збереження полів чекера в кампанії.",
				"Тіло: keyword, bundle_id, notion_url, console_id і console_name. Змінювати можна чернетку, запис у черзі або помилку; запис, що надсилається або зв'язаний, дає 409. "+
					"400 для ключа з двокрапкою або переведенням рядка чи bundle поза ^[a-z0-9.]+$; 409, якщо bundle в іншої кампанії. Відповідь як у GET.")),
		"POST /api/checker/apps/{group}/create": cookieDoc("create_checker_app",
			docOf("Queue the creation of the checker application of a campaign.",
				"An empty keyword or bundle is read from the campaign name. 202 with the record queued; 200 when it is already queued, being sent or linked. "+
					"409 while the integration is off or when another campaign holds the bundle; 429 when 200 records already wait. The queue sends later and only while create_apps is on."),
			docOf("Постановка в очередь создания приложения кампании в чекере.",
				"Пустой ключ или bundle берётся из названия кампании. 202, запись в очереди; 200, если она уже в очереди, отправляется или связана. "+
					"409, пока интеграция выключена или если bundle у другой кампании; 429, если уже ждут 200 записей. Очередь отправляет позже и только при включённом create_apps."),
			docOf("Постановка в чергу створення застосунку кампанії в чекері.",
				"Порожній ключ або bundle береться з назви кампанії. 202, запис у черзі; 200, якщо він уже в черзі, надсилається або зв'язаний. "+
					"409, поки інтеграція вимкнена або якщо bundle в іншої кампанії; 429, якщо вже чекають 200 записів. Черга надсилає пізніше й лише за ввімкненого create_apps.")),
		"POST /api/checker/apps/{group}/refresh": cookieDoc("refresh_checker_app",
			docOf("Read the status of the linked checker application.",
				"One GET to the checker, at most once a minute per campaign: 429 with retry_after inside the minute. 409 when the record is not linked to the current checker or the integration is off; 502 with the text of the checker. Answers like GET."),
			docOf("Чтение статуса связанного приложения в чекере.",
				"Один GET к чекеру, не чаще раза в минуту на кампанию: внутри минуты 429 с retry_after. 409, если запись не связана с текущим чекером или интеграция выключена; 502 с текстом чекера. Ответ как у GET."),
			docOf("Читання статусу зв'язаного застосунку в чекері.",
				"Один GET до чекера, не частіше разу на хвилину на кампанію: усередині хвилини 429 з retry_after. 409, якщо запис не зв'язаний із поточним чекером або інтеграція вимкнена; 502 з текстом чекера. Відповідь як у GET.")),
	}
}
