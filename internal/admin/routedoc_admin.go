package admin

import "net/http"

// adminRouteDocs documents the Keitaro transfer, sign-in, session, settings, automation,
// update, account and integration routes. It returns every area table merged into one map
// keyed by mux pattern and panics when two tables hold the same pattern.
func adminRouteDocs() map[string]routeDoc {
	docs := map[string]routeDoc{}
	for _, part := range []map[string]routeDoc{
		keitaroRouteDocs(), publicRouteDocs(), setupRouteDocs(), sessionRouteDocs(), settingsRouteDocs(),
		cronRouteDocs(), updateRouteDocs(), accountRouteDocs(), integrationRouteDocs(), checkerRouteDocs(),
	} {
		for pattern, d := range part {
			if _, dup := docs[pattern]; dup {
				// Intentional startup panic: a route documented twice is a typo in a build-time table.
				panic("admin: route documented twice: " + pattern)
			}
			docs[pattern] = d
		}
	}
	return docs
}

// docOf returns the summary and description of one language.
func docOf(summary, description string) docText {
	return docText{Summary: summary, Description: description}
}

// cookieDoc returns the doc of a route an API key may not call. en, ru and uk are the texts
// of the three languages; the route needs only the summaries, the descriptions are optional.
func cookieDoc(id string, en, ru, uk docText) routeDoc {
	return routeDoc{ID: id, Summary: en.Summary, Description: en.Description, RU: ru, UK: uk}
}

// withResponse returns d with sample as the success answer.
func (d routeDoc) withResponse(sample any) routeDoc {
	d.Response = sample
	return d
}

// withBody returns d with a JSON request body schema, a sample body and the statuses the route
// answers on its own besides the 401, 403 and 429 every route shares.
func (d routeDoc) withBody(schema map[string]any, example any, errs ...int) routeDoc {
	d.Body, d.Example, d.Errors = schema, example, errs
	return d
}

// buildInfoSample returns a sample build record: version, commit, commit_date and channel.
func buildInfoSample() map[string]any {
	return map[string]any{
		"version": "v1.4.0", "commit": "0123456789abcdef0123456789abcdef01234567",
		"commit_date": "2026-11-10T12:00:00Z", "channel": "stable",
	}
}

// keitaroRouteDocs documents the Keitaro status, settings, check, transfer and purge routes.
func keitaroRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/keitaro/job": {
			ID: "get_keitaro_job", Summary: "Get the state of the Keitaro transfer job.", MCP: mcpRead,
			Description: "Shows the running or the last transfer: stage, progress, the number of saved records per kind, " +
				"the losses and the error. Administrator key only. running is true while a transfer runs and sync_running " +
				"while the log catch-up runs; a purge is not shown. The stage is idle, snapshot, streams, catalogs, " +
				"landing_files, lists, pages, groups, users, conversions, clicks, done or failed; done and total count " +
				"the pages or campaigns of the current stage. campaign_id is set only for a transfer of one campaign. " +
				"A failure of the last run is in error, not in the HTTP status. The state survives an admin restart: " +
				"a run cut by the restart is failed with interrupted true. last_stage is the stage a failed run stopped in, " +
				"updated_at the last change, logs the rows written so far per log (clicks, conversions) and whether the log is done. " +
				"resumable true means POST /api/keitaro/import with resume true can continue the run; resumes counts the continuations.",
			RU: docOf("Состояние задачи переноса из Keitaro.",
				"Показывает идущий или последний перенос: этап, прогресс, число сохранённых записей по видам, потери и ошибку. "+
					"Только для ключа администратора. running равен true, пока идёт перенос, sync_running — пока идёт "+
					"догрузка логов; чистка здесь не видна. Этап: idle, snapshot, streams, catalogs, landing_files, lists, "+
					"pages, groups, users, conversions, clicks, done или failed; done и total считают страницы или "+
					"кампании текущего этапа. campaign_id задан только при переносе одной кампании. "+
					"Сбой последнего запуска лежит в поле error, а не в статусе HTTP. Состояние переживает перезапуск admin: "+
					"перенос, оборванный перезапуском, становится failed с interrupted равным true. last_stage — этап, на котором "+
					"остановился сбойный запуск, updated_at — время последнего изменения, logs — сколько строк записано по каждому "+
					"журналу (clicks, conversions) и дочитан ли он. resumable равен true, если POST /api/keitaro/import с resume "+
					"равным true может продолжить перенос; resumes считает продолжения."),
			UK: docOf("Стан завдання перенесення з Keitaro.",
				"Показує перенесення, що триває, або останнє: етап, прогрес, кількість збережених записів за видами, втрати й помилку. "+
					"Лише для ключа адміністратора. running дорівнює true, поки триває перенесення, sync_running — поки триває "+
					"догрузка логів; чистка тут не видна. Етап: idle, snapshot, streams, catalogs, landing_files, lists, "+
					"pages, groups, users, conversions, clicks, done або failed; done і total рахують сторінки або "+
					"кампанії поточного етапу. campaign_id заданий лише при перенесенні однієї кампанії. "+
					"Збій останнього запуску лежить у полі error, а не в статусі HTTP. Стан переживає перезапуск admin: "+
					"перенесення, обірване перезапуском, стає failed з interrupted, рівним true. last_stage — етап, на якому "+
					"зупинився збійний запуск, updated_at — час останньої зміни, logs — скільки рядків записано за кожним "+
					"журналом (clicks, conversions) і чи дочитаний він. resumable дорівнює true, якщо POST /api/keitaro/import з resume, "+
					"рівним true, може продовжити перенесення; resumes рахує продовження."),
			Response: map[string]any{
				"running": false, "sync_running": false, "stage": "failed", "last_stage": "clicks", "done": 40, "total": 120,
				"counts": map[string]int{"networks": 3, "sources": 2, "domains": 4, "landings": 6, "offers": 14, "groups": 12},
				"losses": []map[string]string{
					{"kind": "skipped", "ref": "offer 15", "detail": "unsupported offer type"},
				},
				"error": "context deadline exceeded", "started_at": "2026-11-16T10:00:00Z", "finished_at": "2026-11-16T12:00:00Z",
				"updated_at": "2026-11-16T12:00:00Z", "interrupted": false, "resumable": true, "resumes": 0,
				"logs": map[string]any{"clicks": map[string]any{"rows": 40000, "through": "2026-11-10 08:15:00", "done": false}},
			},
		},
		"POST /api/keitaro/import": {
			ID: "start_keitaro_import", Summary: "Start the Keitaro transfer job.",
			Description: "Runs in the background; GET /api/keitaro/job shows the progress. Only one job runs at a time, " +
				"a second start is 409. A positive campaign_id runs the dosync of that one Keitaro campaign: its streams, " +
				"and the offers, landings, networks, traffic source and domain it uses; other campaigns are not changed. " +
				"Needs the keitaro_import key action. " +
				"Clicks and conversions need both from and to. With filters_only true only the filters of the streams " +
				"of imported campaigns are rewritten: send campaign_ids or all_imported true, one of the two; dry_run true " +
				"then only reports what would change. filters_only does not combine with the other flags or campaign_id, " +
				"and campaign_ids, all_imported and dry_run are refused without it. " +
				"resume true alone continues the last failed transfer (resumable in GET /api/keitaro/job): nothing imported is removed, " +
				"catalogs that already passed are skipped, clicks continue from the last written batch without doubling a cid, " +
				"conversions are read again; 409 nothing to resume when there is no such run, 400 with any other field. " +
				"409 while a transfer, a purge, a catch-up or an update is running; 503 when Keitaro or a store is not set up.",
			RU: docOf("Запуск задачи переноса из Keitaro.",
				"Работает в фоне; ход виден в GET /api/keitaro/job. Одновременно идёт одна задача, второй запуск вернёт 409. "+
					"Положительный campaign_id запускает перенос одной кампании Keitaro: её потоки и оффера, лендинги, "+
					"сети, источник трафика и домен, которые она использует; остальные кампании не меняются. "+
					"Нужна галочка ключа keitaro_import. Только для ключа администратора. "+
					"Для кликов и конверсий нужны оба поля from и to. С filters_only равным true переписываются только "+
					"фильтры потоков импортированных кампаний: передайте campaign_ids или all_imported равным true, одно из двух; "+
					"dry_run равным true только покажет, что изменилось бы. filters_only не сочетается с другими флагами и "+
					"campaign_id, а campaign_ids, all_imported и dry_run без него отклоняются. "+
					"Одно поле resume равным true продолжает последний оборванный перенос (resumable в GET /api/keitaro/job): "+
					"импортированное не удаляется, пройденные каталоги пропускаются, клики дочитываются с последней записанной пачки "+
					"без повторов cid, конверсии читаются заново; 409 nothing to resume, если продолжать нечего, 400 с другими полями. "+
					"409, пока идут перенос, чистка, догрузка или обновление; 503, если Keitaro или хранилище не настроены."),
			UK: docOf("Запуск завдання перенесення з Keitaro.",
				"Працює у фоні; перебіг видно в GET /api/keitaro/job. Одночасно йде одне завдання, другий запуск поверне 409. "+
					"Додатний campaign_id запускає перенесення однієї кампанії Keitaro: її потоки та оффери, лендінги, "+
					"мережі, джерело трафіку й домен, які вона використовує; решта кампаній не змінюється. "+
					"Потрібна галочка ключа keitaro_import. Лише для ключа адміністратора. "+
					"Для кліків і конверсій потрібні обидва поля from і to. З filters_only, рівним true, перезаписуються лише "+
					"фільтри потоків імпортованих кампаній: передайте campaign_ids або all_imported, рівний true, одне з двох; "+
					"dry_run, рівний true, лише покаже, що змінилося б. filters_only не поєднується з іншими прапорцями та "+
					"campaign_id, а campaign_ids, all_imported і dry_run без нього відхиляються. "+
					"Одне поле resume, рівне true, продовжує останнє обірване перенесення (resumable у GET /api/keitaro/job): "+
					"імпортоване не видаляється, пройдені каталоги пропускаються, кліки дочитуються з останньої записаної пачки "+
					"без повторів cid, конверсії читаються заново; 409 nothing to resume, якщо продовжувати нічого, 400 з іншими полями. "+
					"409, поки йдуть перенесення, чистка, догрузка або оновлення; 503, якщо Keitaro чи сховище не налаштовані."),
			Body: objectBody("Flags domains, networks, offers, landings, sources, campaigns, users, clicks, conversions (booleans); " +
				"from and to (YYYY-MM-DD) for clicks and conversions; board_id; campaign_id (Keitaro campaign id, " +
				"not combined with users, clicks or conversions); resume (boolean, alone: continue the last failed transfer)."),
			Example: map[string]any{
				"domains": true, "networks": true, "offers": true, "landings": true, "sources": true, "campaigns": true,
			},
			Status:   http.StatusAccepted,
			Response: map[string]any{"started": true},
			Errors:   []int{http.StatusBadRequest, http.StatusConflict, http.StatusServiceUnavailable},
		},
		"GET /api/keitaro/status": {
			ID: "get_keitaro_status", Summary: "Tell whether the Keitaro address and key are set.",
			Description: "Reads the saved settings and does not call Keitaro. Administrator key only. host is the " +
				"host name of the saved address; the key is never returned.",
			RU: docOf("Заданы ли адрес и ключ Keitaro.",
				"Читает сохранённые настройки и не обращается к Keitaro. Только для ключа администратора. "+
					"host — имя хоста сохранённого адреса; ключ не возвращается никогда."),
			UK: docOf("Чи задані адреса й ключ Keitaro.",
				"Читає збережені налаштування й не звертається до Keitaro. Лише для ключа адміністратора. "+
					"host — ім'я хоста збереженої адреси; ключ не повертається ніколи."),
			Response: map[string]any{"configured": true, "host": "tracker.example.com"},
		},
		"GET /api/keitaro/settings": cookieDoc("get_keitaro_settings",
			docOf("Get the Keitaro address and whether a key is set.",
				"Returns base_url, host, has_key and source (file, env or none). The key itself is never returned."),
			docOf("Адрес Keitaro и признак заданного ключа.",
				"Возвращает base_url, host, has_key и source (file, env или none). Сам ключ не возвращается никогда."),
			docOf("Адреса Keitaro та ознака заданого ключа.",
				"Повертає base_url, host, has_key і source (file, env або none). Сам ключ не повертається ніколи.")),
		"PUT /api/keitaro/settings": cookieDoc("put_keitaro_settings",
			docOf("Save the Keitaro address and key.",
				"Body base_url and api_key. An empty or masked api_key keeps the stored key; a new host needs a new key. "+
					"409 while a transfer or a purge runs, or when the new host differs from the one in the transfer manifest. "+
					"Answers like GET."),
			docOf("Сохранить адрес и ключ Keitaro.",
				"Тело: base_url и api_key. Пустой или замаскированный api_key оставляет прежний ключ; для нового хоста нужен новый ключ. "+
					"409, пока идут перенос или чистка, либо когда новый хост отличается от хоста в манифесте переноса. "+
					"Ответ такой же, как у GET."),
			docOf("Зберегти адресу й ключ Keitaro.",
				"Тіло: base_url і api_key. Порожній або замаскований api_key залишає попередній ключ; для нового хоста потрібен новий ключ. "+
					"409, поки йдуть перенесення чи чистка, або коли новий хост відрізняється від хоста в маніфесті перенесення. "+
					"Відповідь така сама, як у GET.")),
		"POST /api/keitaro/check": {
			ID: "check_keitaro", Summary: "Check the saved Keitaro key and count its campaigns.",
			Description: "Calls Keitaro with the saved address and key and counts the campaigns; the count is remembered " +
				"for the next check. Browser session of an administrator only: an API key gets 403 \"cookie session required\". " +
				"Nothing is saved. 503 when the address or the key is not set, 502 when Keitaro refuses the key or does not answer. " +
				"The Keitaro version is not reported by the API; product_version is \"10\" or \"11\" when read-only samples of the " +
				"click and conversion logs point to one version (version_signals lists the votes), and empty when there are no " +
				"signals or they disagree (version_conflict true).",
			RU: docOf("Проверка сохранённого ключа Keitaro и подсчёт кампаний.",
				"Обращается к Keitaro с сохранённым адресом и ключом и считает кампании; число запоминается для следующей "+
					"проверки. Только сессия браузера администратора: ключ получает 403 «cookie session required». "+
					"Ничего не сохраняется. 503, если адрес или ключ не заданы, 502, если Keitaro отклонил ключ или не ответил. "+
					"API версию Keitaro не сообщает: product_version равен «10» или «11», когда чтение свежих записей журналов "+
					"кликов и конверсий без записи указывает на одну версию (version_signals — голоса), и пуст, если признаков "+
					"нет или они противоречат (version_conflict true)."),
			UK: docOf("Перевірка збереженого ключа Keitaro й підрахунок кампаній.",
				"Звертається до Keitaro зі збереженою адресою та ключем і рахує кампанії; число запам'ятовується для наступної "+
					"перевірки. Лише сесія браузера адміністратора: ключ отримує 403 «cookie session required». "+
					"Нічого не зберігається. 503, якщо адреса або ключ не задані, 502, якщо Keitaro відхилив ключ або не відповів. "+
					"API версію Keitaro не повертає: product_version дорівнює «10» або «11», коли читання свіжих записів журналів "+
					"кліків і конверсій без запису вказує на одну версію (version_signals — голоси), і порожній, якщо ознак "+
					"немає або вони суперечать (version_conflict true)."),
			Response: map[string]any{
				"ok": true, "campaigns": 42, "api": "v1", "product_version": "11",
				"version_signals":  []map[string]string{{"signal": "subid", "version": "11"}},
				"version_conflict": false,
			},
			Errors: []int{http.StatusForbidden, http.StatusBadGateway, http.StatusServiceUnavailable},
		},
		"POST /api/keitaro/purge": {
			ID: "purge_keitaro", Summary: "Remove everything the Keitaro transfer imported.",
			Description: "Deletes the groups, offers, landings, networks, sources, domains, lists, pages and users named in " +
				"the transfer manifest, the files of those landings and the imported clicks and conversions of the Keitaro " +
				"host from the warehouse, then clears the manifest. Records created by hand stay. An empty manifest answers " +
				"zeros and removes nothing. Needs the keitaro_purge key action and a key that sees every board. " +
				"409 while a transfer, a catch-up or another purge runs; 502 when the warehouse fails.",
			RU: docOf("Удаление всего, что принёс перенос из Keitaro.",
				"Удаляет группы, оффера, лендинги, сети, источники, домены, списки, страницы и пользователей из манифеста "+
					"переноса, файлы этих лендингов и импортированные клики и конверсии хоста Keitaro из хранилища, затем "+
					"очищает манифест. Записи, созданные вручную, остаются. Пустой манифест даёт нули и ничего не удаляет. "+
					"Нужна галочка ключа keitaro_purge и ключ, который видит все разделы. "+
					"409, пока идут перенос, догрузка или другая чистка; 502, если хранилище вернуло ошибку."),
			UK: docOf("Видалення всього, що принесло перенесення з Keitaro.",
				"Видаляє групи, оффери, лендінги, мережі, джерела, домени, списки, сторінки та користувачів із маніфесту "+
					"перенесення, файли цих лендінгів і імпортовані кліки та конверсії хоста Keitaro зі сховища, потім "+
					"очищає маніфест. Записи, створені вручну, залишаються. Порожній маніфест дає нулі й нічого не видаляє. "+
					"Потрібна галочка ключа keitaro_purge і ключ, який бачить усі розділи. "+
					"409, поки йдуть перенесення, догрузка або інша чистка; 502, якщо сховище повернуло помилку."),
			Response: map[string]any{"removed": keitaroRemoved{
				Groups: 12, Offers: 14, Landings: 6, Networks: 3, Sources: 2, Domains: 4,
				Lists: 5, Pages: 2, Users: 1, Events: 48210, Conversions: 903,
			}},
			Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusInternalServerError, http.StatusBadGateway},
		},
	}
}

// publicRouteDocs documents the routes mounted without the session check: the probes and
// the brand an API key may call, and the sign-in and recovery routes of the browser.
func publicRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/health": {
			ID: "get_api_health", Summary: "Check that the admin answers.",
			Description: "Needs no key. Answers 200 with status ok while the admin process is up; it does not check " +
				"ClickHouse or Redis.",
			RU: docOf("Проверка, что админка отвечает.",
				"Ключ не нужен. Отвечает 200 и status ok, пока процесс админки жив; ClickHouse и Redis не проверяет."),
			UK: docOf("Перевірка, що адмінка відповідає.",
				"Ключ не потрібен. Відповідає 200 і status ok, поки процес адмінки живий; ClickHouse і Redis не перевіряє."),
			Response: map[string]string{"status": "ok"},
		},
		"GET /healthz": {
			ID: "get_healthz", Summary: "Liveness probe that tells which service and host answered.",
			Description: "Needs no key. Always answers the text ok. The headers X-Emtds-Host (the request host) and " +
				"X-Emtds-Service (admin) show who answered; X-Emtds-Probe repeats a valid probe value.",
			RU: docOf("Проба живости: какой сервис и хост ответили.",
				"Ключ не нужен. Всегда отвечает текстом ok. Заголовки X-Emtds-Host (хост запроса) и X-Emtds-Service (admin) "+
					"показывают, кто ответил; X-Emtds-Probe повторяет допустимое значение probe."),
			UK: docOf("Проба живості: який сервіс і хост відповіли.",
				"Ключ не потрібен. Завжди відповідає текстом ok. Заголовки X-Emtds-Host (хост запиту) і X-Emtds-Service (admin) "+
					"показують, хто відповів; X-Emtds-Probe повторює допустиме значення probe."),
			Query: []queryParam{strQ("probe", "Optional token of 1 to 64 lowercase hex characters, echoed in the X-Emtds-Probe header.").
				loc("Необязательный токен из 1–64 строчных hex-символов; возвращается в заголовке X-Emtds-Probe.",
					"Необов'язковий токен з 1–64 малих hex-символів; повертається в заголовку X-Emtds-Probe.")},
			Response: "ok", ResponseType: "text/plain",
		},
		"GET /api/brand": {
			ID: "get_brand", Summary: "Get the company name and logo shown on the sign-in page.",
			Description: "Needs no key. The logo is a data:image URL or an empty string. The answer is never cached.",
			RU: docOf("Название компании и логотип со страницы входа.",
				"Ключ не нужен. Логотип — ссылка data:image или пустая строка. Ответ никогда не кэшируется."),
			UK: docOf("Назва компанії та логотип зі сторінки входу.",
				"Ключ не потрібен. Логотип — посилання data:image або порожній рядок. Відповідь ніколи не кешується."),
			Response: map[string]string{"name": "Example Ltd", "logo": ""},
		},
		"POST /api/login": cookieDoc("login",
			docOf("Sign in with a login and a password.",
				"Body login and password. 200 with the CSRF token and the session cookie; 401 for wrong credentials; "+
					"429 for too many attempts (10 a minute per address), with the wait in Retry-After and retry_after; "+
					"503 when sign-in is closed on this host, or with the text \"session store unavailable\" "+
					"when Redis is unavailable: the attempt counter or the session store failed. That answer does not depend on the password."),
			docOf("Вход по логину и паролю.",
				"Тело: login и password. 200 с CSRF-токеном и cookie сессии; 401 при неверных данных; "+
					"429 при слишком частых попытках (10 в минуту с адреса), с ожиданием в Retry-After и retry_after; "+
					"503, если вход на этом хосте закрыт, либо с текстом \"session store unavailable\", "+
					"когда Redis недоступен: не сработал счётчик попыток или хранилище сессий. Такой ответ не зависит от пароля."),
			docOf("Вхід за логіном і паролем.",
				"Тіло: login і password. 200 із CSRF-токеном і cookie сесії; 401 при хибних даних; "+
					"429 при надто частих спробах (10 за хвилину з адреси), з очікуванням у Retry-After і retry_after; "+
					"503, якщо вхід на цьому хості закритий, або з текстом \"session store unavailable\", "+
					"коли Redis недоступний: не спрацював лічильник спроб або сховище сесій. Така відповідь не залежить від пароля.")),
		"POST /api/recover": cookieDoc("recover_password",
			docOf("Set a new password with a one-time recovery code.",
				"Body login, code and password (6 or more). A wrong login, a wrong code and an expired code are all 401; "+
					"429 for too many attempts."),
			docOf("Новый пароль по одноразовому коду восстановления.",
				"Тело: login, code и password (от 6 символов). Неверный логин, неверный и просроченный код дают один и тот же 401; "+
					"429 при слишком частых попытках."),
			docOf("Новий пароль за одноразовим кодом відновлення.",
				"Тіло: login, code і password (від 6 символів). Хибний логін, хибний і прострочений код дають той самий 401; "+
					"429 при надто частих спробах.")),
	}
}

// setupRouteDocs documents the first-run form: whether it is still open and the creation of the
// first administrator. Both routes are mounted without the session check.
func setupRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/setup": cookieDoc("get_setup",
			docOf("Tell whether the first-run form is still open.",
				"Needs no session. needed is true until the first administrator is created; public_ip is the server address or empty; "+
					"token_required is true while the form is open and the server was started with a first-run token."),
			docOf("Открыта ли форма первого запуска.",
				"Сессия не нужна. needed равен true, пока не создан первый администратор; public_ip — адрес сервера или пустая строка; "+
					"token_required равен true, пока форма открыта и сервер запущен с токеном первого входа."),
			docOf("Чи відкрита форма першого запуску.",
				"Сесія не потрібна. needed дорівнює true, поки не створений перший адміністратор; public_ip — адреса сервера або порожній рядок; "+
					"token_required дорівнює true, поки форма відкрита і сервер запущений із токеном першого входу.")).
			withResponse(map[string]any{"needed": true, "public_ip": "203.0.113.10", "token_required": true}),
		"POST /api/setup": cookieDoc("post_setup",
			docOf("Create the first administrator and sign in.",
				"Body login (2 to 64 characters, no spaces), password (6 or more, not admin), email and, when GET /api/setup "+
					"says token_required, token. 200 with the CSRF token and the session cookie. 400 for a bad field, "+
					"403 with \"setup token is required\" or \"setup token is wrong\" when the token is missing or does not match, "+
					"409 when the setup is already finished (whatever the token is), 429 for too many attempts."),
			docOf("Создание первого администратора и вход.",
				"Тело: login (2–64 символа без пробелов), password (от 6 символов, не admin), email и, если GET /api/setup "+
					"возвращает token_required, token. 200 с CSRF-токеном и cookie сессии. 400 при неверном поле, "+
					"403 с текстом \"setup token is required\" или \"setup token is wrong\", если токен не передан или не совпал, "+
					"409 если настройка уже завершена (при любом токене), 429 при слишком частых попытках."),
			docOf("Створення першого адміністратора та вхід.",
				"Тіло: login (2–64 символи без пробілів), password (від 6 символів, не admin), email і, якщо GET /api/setup "+
					"повертає token_required, token. 200 із CSRF-токеном і cookie сесії. 400 при хибному полі, "+
					"403 з текстом \"setup token is required\" або \"setup token is wrong\", якщо токен не передано або він не збігся, "+
					"409 якщо налаштування вже завершене (за будь-якого токена), 429 при надто частих спробах.")).
			withBody(objectBody("login, password, email and token (needed only when token_required is true)."),
				map[string]string{"login": "owner", "password": "example-password", "email": "owner@example.com", "token": "example-setup-token"},
				http.StatusBadRequest, http.StatusForbidden, http.StatusConflict).
			withResponse(map[string]string{"csrf": "example-csrf-token"}),
	}
}

// sessionRouteDocs documents the routes of the signed-in browser session.
func sessionRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"POST /api/logout": cookieDoc("logout",
			docOf("Sign out and drop the session.", "Deletes the session and its cookie; the answer is status logged_out."),
			docOf("Выход и удаление сессии.", "Удаляет сессию и её cookie; ответ — status logged_out."),
			docOf("Вихід і видалення сесії.", "Видаляє сесію та її cookie; відповідь — status logged_out.")),
		"GET /api/me": cookieDoc("get_me",
			docOf("Get the signed-in account, its panel sections and boards.",
				"Returns the login, type, CSRF token, panel pages, access, role, hidden fields and the boards the account may open. "+
					"uniq_max_seconds is the ceiling of the uniqueness window kept in Redis (EMTDS_UNIQ_MAX_SECONDS, a day by default)."),
			docOf("Текущий аккаунт, его разделы панели и доски.",
				"Возвращает логин, тип, CSRF-токен, страницы панели, доступ, роль, скрытые поля и доски, которые аккаунт может открыть. "+
					"uniq_max_seconds — потолок окна уникальности в Redis (EMTDS_UNIQ_MAX_SECONDS, по умолчанию сутки)."),
			docOf("Поточний акаунт, його розділи панелі та дошки.",
				"Повертає логін, тип, CSRF-токен, сторінки панелі, доступ, роль, приховані поля та дошки, які акаунт може відкрити. "+
					"uniq_max_seconds — стеля вікна унікальності в Redis (EMTDS_UNIQ_MAX_SECONDS, за замовчуванням доба).")),
		"GET /api/me/views": cookieDoc("get_my_views",
			docOf("Get the saved page views of the account.",
				"Returns an object with one saved view per page key, or an empty object."),
			docOf("Сохранённые виды страниц аккаунта.",
				"Возвращает объект с одним сохранённым видом на ключ страницы или пустой объект."),
			docOf("Збережені вигляди сторінок акаунта.",
				"Повертає об'єкт з одним збереженим виглядом на ключ сторінки або порожній об'єкт.")),
		"PUT /api/me/views": cookieDoc("put_my_views",
			docOf("Replace the saved page views of the account.",
				"The body is an object of views by page key; other keys are dropped and an empty object removes all views. "+
					"413 for a body over 64 KiB, 503 when the views file is not set."),
			docOf("Замена сохранённых видов страниц аккаунта.",
				"Тело — объект видов по ключам страниц; прочие ключи отбрасываются, пустой объект удаляет все виды. "+
					"413 для тела больше 64 КиБ, 503, если файл видов не задан."),
			docOf("Заміна збережених виглядів сторінок акаунта.",
				"Тіло — об'єкт виглядів за ключами сторінок; інші ключі відкидаються, порожній об'єкт видаляє всі вигляди. "+
					"413 для тіла понад 64 КіБ, 503, якщо файл виглядів не заданий.")),
		"POST /api/password": cookieDoc("change_password",
			docOf("Change the password of the signed-in account.",
				"Body old and new (6 or more characters). The old password is checked, other sessions end and a fresh "+
					"session with a new CSRF token is issued."),
			docOf("Смена пароля текущего аккаунта.",
				"Тело: old и new (от 6 символов). Старый пароль проверяется, остальные сессии завершаются, "+
					"выдаётся новая сессия с новым CSRF-токеном."),
			docOf("Зміна пароля поточного акаунта.",
				"Тіло: old і new (від 6 символів). Старий пароль перевіряється, решта сесій завершується, "+
					"видається нова сесія з новим CSRF-токеном.")),
	}
}

// settingsRouteDocs documents the notices, the OpenAPI document, the interface settings,
// the bot profile and the HTML pages.
func settingsRouteDocs() map[string]routeDoc {
	pageRead := map[string]string{"name": "stub.html", "content": "<!doctype html><title>Stub</title><p>Hello</p>"}
	return map[string]routeDoc{
		"GET /api/notices": {
			ID: "list_notices", Summary: "List the system notices.",
			Description: "Returns lines of text: the warnings of the engine, the Redis memory alert of the cron service " +
				"and, for an administrator key, a note that an update is available. An empty list means nothing to report; " +
				"an engine that does not answer adds no lines.",
			RU: docOf("Системные уведомления.",
				"Возвращает строки текста: предупреждения движка, тревогу по памяти Redis от сервиса cron и, для ключа "+
					"администратора, сообщение о доступном обновлении. Пустой список — сообщать нечего; "+
					"молчащий движок строк не добавляет."),
			UK: docOf("Системні сповіщення.",
				"Повертає рядки тексту: попередження рушія, тривогу щодо пам'яті Redis від сервісу cron і, для ключа "+
					"адміністратора, повідомлення про доступне оновлення. Порожній список — повідомляти нічого; "+
					"рушій, що мовчить, рядків не додає."),
			Response: []string{"2026-11-16T10:00:00Z update available stable 0123456789ab v1.4.0"},
		},
		"GET /api/postman.json": {
			ID: "get_postman", Summary: "Get the Postman collection of the routes the caller may use.",
			Description: "The collection lists only the routes the calling key may use, the same set as /api/openapi.json. " +
				"Examples are fictional and the key value em_example is a placeholder, not a real key.",
			RU: docOf("Коллекция Postman с маршрутами, доступными вызывающему.",
				"В коллекции только те маршруты, которые разрешены вызывающему ключу, тот же набор, что в /api/openapi.json. "+
					"Примеры вымышлены, значение ключа em_example — заглушка, а не настоящий ключ."),
			UK: docOf("Колекція Postman з маршрутами, доступними викликачу.",
				"У колекції лише ті маршрути, які дозволені ключу, що викликає, той самий набір, що в /api/openapi.json. "+
					"Приклади вигадані, значення ключа em_example — заглушка, а не справжній ключ."),
			Response: map[string]any{
				"info": map[string]any{"name": "emtds admin API", "schema": postmanSchema},
				"item": []any{},
			},
		},
		"GET /api/openapi.json": {
			ID: "get_openapi", Summary: "Get the OpenAPI document of the routes the caller may use.",
			Description: "The document lists only the routes the calling key may use. Russian and Ukrainian texts are in " +
				"the x-emtds-i18n extension of each operation and parameter.",
			RU: docOf("Документ OpenAPI с маршрутами, доступными вызывающему.",
				"В документе только те маршруты, которые разрешены вызывающему ключу. Тексты на русском и украинском "+
					"лежат в расширении x-emtds-i18n каждой операции и параметра."),
			UK: docOf("Документ OpenAPI з маршрутами, доступними викликачу.",
				"У документі лише ті маршрути, які дозволені ключу, що викликає. Тексти російською та українською "+
					"лежать у розширенні x-emtds-i18n кожної операції та параметра."),
			Response: map[string]any{
				"openapi": openAPIVersion,
				"info":    map[string]any{"title": "emtds admin API", "version": openAPIInfoVersion},
				"paths": map[string]any{"/api/health": map[string]any{"get": map[string]any{
					"operationId": "get_api_health", "summary": "Check that the admin answers.",
				}}},
			},
		},
		"GET /docs": {
			ID: "get_public_docs", Summary: "Open the public API reference page.",
			Description: "Needs no key or session. The page shows every route with its samples. 404 while public docs are " +
				"off in the docs settings; 429 for too many requests from one address.",
			RU: docOf("Публичная страница справочника API.",
				"Ключ и сессия не нужны. На странице все маршруты с примерами. 404, пока публичная документация выключена "+
					"в настройках; 429 при слишком частых запросах с одного адреса."),
			UK: docOf("Публічна сторінка довідника API.",
				"Ключ і сесія не потрібні. На сторінці всі маршрути з прикладами. 404, поки публічну документацію вимкнено "+
					"в налаштуваннях; 429 за надто частих запитів з однієї адреси."),
			Response: "<!doctype html>…", ResponseType: "text/html", Errors: []int{http.StatusNotFound},
		},
		"GET /api/docs/postman.json": {
			ID: "get_public_postman", Summary: "Get the Postman collection of every route.",
			Description: "Needs no key or session. The collection lists every route, the same set as /api/docs/openapi.json. " +
				"Examples are fictional and the key value em_example is a placeholder, not a real key. " +
				"Cached for five minutes. 404 while public docs are off; 429 for too many requests from one address.",
			RU: docOf("Коллекция Postman со всеми маршрутами.",
				"Ключ и сессия не нужны. В коллекции все маршруты, тот же набор, что в /api/docs/openapi.json. "+
					"Примеры вымышлены, значение ключа em_example — заглушка, а не настоящий ключ. "+
					"Кэшируется на пять минут. 404, пока публичная документация выключена; 429 при слишком частых запросах."),
			UK: docOf("Колекція Postman з усіма маршрутами.",
				"Ключ і сесія не потрібні. У колекції всі маршрути, той самий набір, що в /api/docs/openapi.json. "+
					"Приклади вигадані, значення ключа em_example — заглушка, а не справжній ключ. "+
					"Кешується на п'ять хвилин. 404, поки публічну документацію вимкнено; 429 за надто частих запитів."),
			Response: map[string]any{
				"info": map[string]any{"name": "emtds admin API", "schema": postmanSchema},
				"item": []any{},
			},
			Errors: []int{http.StatusNotFound},
		},
		"GET /api/docs/openapi.json": {
			ID: "get_public_openapi", Summary: "Get the OpenAPI document of every route.",
			Description: "Needs no key or session. Unlike /api/openapi.json the list does not depend on the caller: " +
				"cookie-only routes carry x-emtds-cookie-only and gated ones x-emtds-action. The samples are made up. " +
				"Cached for five minutes. 404 while public docs are off; 429 for too many requests from one address.",
			RU: docOf("Документ OpenAPI со всеми маршрутами.",
				"Ключ и сессия не нужны. В отличие от /api/openapi.json список не зависит от вызывающего: у маршрутов "+
					"только для сессии есть x-emtds-cookie-only, у маршрутов с галочкой — x-emtds-action. Примеры вымышлены. "+
					"Кэшируется на пять минут. 404, пока публичная документация выключена; 429 при слишком частых запросах."),
			UK: docOf("Документ OpenAPI з усіма маршрутами.",
				"Ключ і сесія не потрібні. На відміну від /api/openapi.json список не залежить від викликача: маршрути "+
					"лише для сесії мають x-emtds-cookie-only, маршрути з галочкою — x-emtds-action. Приклади вигадані. "+
					"Кешується на п'ять хвилин. 404, поки публічну документацію вимкнено; 429 за надто частих запитів."),
			Response: map[string]any{
				"openapi": openAPIVersion,
				"info":    map[string]any{"title": "emtds admin API", "version": openAPIInfoVersion},
				"paths":   map[string]any{},
			},
			Errors: []int{http.StatusNotFound},
		},
		"GET /api/docs.md": {
			ID: "get_public_docs_markdown", Summary: "Get the whole API reference as one Markdown text.",
			Description: "Needs no key or session. Meant for people and AI agents: every route with its access notes, " +
				"parameters and samples. lang picks en, ru or uk; English otherwise. /llms.txt answers the same English text. " +
				"Cached for five minutes. 404 while public docs are off; 429 for too many requests from one address.",
			RU: docOf("Весь справочник API одним текстом Markdown.",
				"Ключ и сессия не нужны. Для людей и AI-агентов: каждый маршрут с правами, параметрами и примерами. "+
					"lang выбирает en, ru или uk, иначе английский. /llms.txt отдаёт тот же английский текст. Кэшируется на "+
					"пять минут. 404, пока публичная документация выключена; 429 при слишком частых запросах."),
			UK: docOf("Увесь довідник API одним текстом Markdown.",
				"Ключ і сесія не потрібні. Для людей і AI-агентів: кожен маршрут із правами, параметрами та прикладами. "+
					"lang обирає en, ru або uk, інакше англійська. /llms.txt віддає той самий англійський текст. Кешується на "+
					"п'ять хвилин. 404, поки публічну документацію вимкнено; 429 за надто частих запитів."),
			Query:    []queryParam{strQ("lang", "Language: en, ru or uk.").loc("Язык: en, ru или uk.", "Мова: en, ru або uk.")},
			Response: "# emtds admin API\n…", ResponseType: "text/markdown", Errors: []int{http.StatusNotFound},
		},
		"GET /llms.txt": {
			ID: "get_llms_txt", Summary: "Get the English API reference for AI agents.",
			Description: "The same text as /api/docs.md in English, at the address AI agents look for. 404 while public docs are off.",
			RU: docOf("Английский справочник API для AI-агентов.",
				"Тот же текст, что /api/docs.md на английском, по адресу, который ищут AI-агенты. 404, пока публичная документация выключена."),
			UK: docOf("Англійський довідник API для AI-агентів.",
				"Той самий текст, що /api/docs.md англійською, за адресою, яку шукають AI-агенти. 404, поки публічну документацію вимкнено."),
			Response: "# emtds admin API\n…", ResponseType: "text/markdown", Errors: []int{http.StatusNotFound},
		},
		"GET /api/docs/settings": cookieDoc("get_docs_settings",
			docOf("Tell whether the API reference is public.",
				"public is true when /docs, /api/docs/openapi.json, /api/docs.md and /llms.txt answer without sign-in. "+
					"It is true until an administrator turns it off."),
			docOf("Открыта ли документация API без входа.",
				"public равен true, когда /docs, /api/docs/openapi.json, /api/docs.md и /llms.txt отвечают без входа. "+
					"По умолчанию true, пока администратор не выключит."),
			docOf("Чи відкрита документація API без входу.",
				"public дорівнює true, коли /docs, /api/docs/openapi.json, /api/docs.md і /llms.txt відповідають без входу. "+
					"Типово true, поки адміністратор не вимкне.")),
		"PUT /api/docs/settings": cookieDoc("put_docs_settings",
			docOf("Open or close the public API reference.",
				"Body {\"public\": bool}; the answer is the saved value. While public is false the public docs routes answer 404. "+
					"400 for a bad body, 503 when the settings file is not configured."),
			docOf("Открыть или закрыть публичную документацию API.",
				"Тело {\"public\": bool}; в ответе сохранённое значение. Пока public равен false, публичные адреса документации "+
					"отвечают 404. 400 при неверном теле, 503 если файл настроек не задан."),
			docOf("Відкрити або закрити публічну документацію API.",
				"Тіло {\"public\": bool}; у відповіді збережене значення. Поки public дорівнює false, публічні адреси документації "+
					"відповідають 404. 400 за невірного тіла, 503 якщо файл налаштувань не задано.")),
		"GET /api/ui": {
			ID: "get_ui_prefs", Summary: "Get the interface settings shared by every account.",
			Description: "Returns the saved column sets per table (cols), the dashboard panels and dimension (dash) and " +
				"the company name and logo (company). Before anything is saved the answer holds empty values.",
			RU: docOf("Настройки интерфейса, общие для всех аккаунтов.",
				"Возвращает сохранённые наборы колонок по таблицам (cols), панели и разрез дашборда (dash) и "+
					"название и логотип компании (company). Пока ничего не сохранено, значения пустые."),
			UK: docOf("Налаштування інтерфейсу, спільні для всіх акаунтів.",
				"Повертає збережені набори колонок за таблицями (cols), панелі та розріз дашборда (dash) і "+
					"назву й логотип компанії (company). Поки нічого не збережено, значення порожні."),
			Response: map[string]any{
				"cols":    map[string]any{},
				"dash":    map[string]any{"panels": nil, "dim": ""},
				"company": map[string]string{"name": "", "logo": ""},
			},
			Errors: []int{http.StatusInternalServerError},
		},
		"PUT /api/ui": {
			ID: "put_ui_prefs", Summary: "Replace the interface settings shared by every account.",
			Description: "Administrator key only. The body replaces the whole document: send cols, dash and company " +
				"together. A column key is a lowercase name of up to 32 characters, at most 32 keys with 200 columns " +
				"each; the company name is up to 64 characters and the logo is a data:image URL of up to 280000 bytes. " +
				"Answers with the saved document. 503 when the settings file is not set.",
			RU: docOf("Замена настроек интерфейса, общих для всех аккаунтов.",
				"Только для ключа администратора. Тело заменяет документ целиком: передавайте cols, dash и company "+
					"вместе. Ключ колонок — имя из строчных символов до 32 знаков, не больше 32 ключей по 200 колонок; "+
					"название компании — до 64 знаков, логотип — ссылка data:image до 280000 байт. "+
					"Отвечает сохранённым документом. 503, если файл настроек не задан."),
			UK: docOf("Заміна налаштувань інтерфейсу, спільних для всіх акаунтів.",
				"Лише для ключа адміністратора. Тіло замінює документ цілком: передавайте cols, dash і company "+
					"разом. Ключ колонок — ім'я з малих символів до 32 знаків, не більше 32 ключів по 200 колонок; "+
					"назва компанії — до 64 знаків, логотип — посилання data:image до 280000 байт. "+
					"Відповідає збереженим документом. 503, якщо файл налаштувань не заданий."),
			Body: objectBody("cols (object of column lists {id, on} per table), dash {panels, dim}, company {name, logo}."),
			Example: map[string]any{
				"cols":    map[string]any{"logs": []map[string]any{{"id": "country", "on": true}}},
				"dash":    map[string]any{"panels": []string{}, "dim": "country"},
				"company": map[string]string{"name": "Example Ltd", "logo": ""},
			},
			Response: map[string]any{
				"cols":    map[string]any{"logs": []map[string]any{{"id": "country", "on": true}}},
				"dash":    map[string]any{"panels": []string{}, "dim": "country"},
				"company": map[string]string{"name": "Example Ltd", "logo": ""},
			},
			Errors: []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusServiceUnavailable},
		},
		"GET /api/bot-protection": {
			ID: "get_bot_protection", Summary: "Get the bot profile shared by every campaign.",
			Description: "Administrator key only. Returns the detection switches that are on (ch_ua, ch_empty_ua, " +
				"ch_proxy, ch_bot_ip_google and the like); a switch that is off is left out. ch_proxy is always present. " +
				"proxy_default is true while the proxy check was never chosen by hand; private_relay is true when the " +
				"iCloud Private Relay list is loaded. Without a saved profile the default one is returned.",
			RU: docOf("Профиль ботов, общий для всех кампаний.",
				"Только для ключа администратора. Возвращает включённые переключатели обнаружения (ch_ua, ch_empty_ua, "+
					"ch_proxy, ch_bot_ip_google и другие); выключенный переключатель в ответ не попадает. ch_proxy есть всегда. "+
					"proxy_default равен true, пока проверку прокси не выбрали вручную; private_relay равен true, когда "+
					"загружен список iCloud Private Relay. Без сохранённого профиля возвращается профиль по умолчанию."),
			UK: docOf("Профіль ботів, спільний для всіх кампаній.",
				"Лише для ключа адміністратора. Повертає ввімкнені перемикачі виявлення (ch_ua, ch_empty_ua, "+
					"ch_proxy, ch_bot_ip_google та інші); вимкнений перемикач у відповідь не потрапляє. ch_proxy є завжди. "+
					"proxy_default дорівнює true, поки перевірку проксі не вибирали вручну; private_relay дорівнює true, коли "+
					"завантажений список iCloud Private Relay. Без збереженого профілю повертається профіль за замовчуванням."),
			Response: botProfileSample(),
			Errors:   []int{http.StatusInternalServerError},
		},
		"PUT /api/bot-protection": {
			ID: "put_bot_protection", Summary: "Change the bot profile shared by every campaign.",
			Description: "Administrator key only. The body is merged into the saved profile: a switch you do not send " +
				"keeps its value, null removes it, and {\"ch_proxy\": null} returns the proxy check to its default. " +
				"Every value must be true, false or null; unknown keys are ignored. Answers like GET. " +
				"503 when the profile file is not set.",
			RU: docOf("Изменение профиля ботов, общего для всех кампаний.",
				"Только для ключа администратора. Тело вливается в сохранённый профиль: непереданный переключатель "+
					"сохраняет значение, null его убирает, а {\"ch_proxy\": null} возвращает проверку прокси к умолчанию. "+
					"Значения только true, false или null; неизвестные ключи игнорируются. Ответ как у GET. "+
					"503, если файл профиля не задан."),
			UK: docOf("Зміна профілю ботів, спільного для всіх кампаній.",
				"Лише для ключа адміністратора. Тіло вливається у збережений профіль: непереданий перемикач "+
					"зберігає значення, null його прибирає, а {\"ch_proxy\": null} повертає перевірку проксі до типового значення. "+
					"Значення лише true, false або null; невідомі ключі ігноруються. Відповідь як у GET. "+
					"503, якщо файл профілю не заданий."),
			Body: objectBody("Detection switches ch_ua, ch_empty_ua, ch_empty_ref, ch_empty_lang, ch_ipv6, ch_ptr, ch_list_ua, " +
				"ch_proxy, ch_non_mobile, ch_bot_ip_baidu, ch_bot_ip_bing, ch_bot_ip_google, ch_bot_ip_mail, ch_bot_ip_yahoo, " +
				"ch_bot_ip_yandex, ch_bot_ip_others, save_ip (true, false or null)."),
			Example:  map[string]any{"ch_empty_ref": true, "ch_non_mobile": nil},
			Response: botProfileSample(),
			Errors:   []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusServiceUnavailable},
		},
		"GET /api/pages": {
			ID: "list_pages", Summary: "List the HTML page files.",
			Description: "Administrator key only. Returns the name and the size in bytes of every page; an empty list when " +
				"there are none. A stream shows a page with the output @page:<name>. 503 when pages are not set up.",
			RU: docOf("Список HTML-страниц.",
				"Только для ключа администратора. Возвращает имя и размер в байтах каждой страницы; пустой список, если "+
					"страниц нет. Поток показывает страницу выводом @page:<имя>. 503, если страницы не настроены."),
			UK: docOf("Список HTML-сторінок.",
				"Лише для ключа адміністратора. Повертає ім'я та розмір у байтах кожної сторінки; порожній список, якщо "+
					"сторінок немає. Потік показує сторінку виводом @page:<ім'я>. 503, якщо сторінки не налаштовані."),
			Response: []map[string]any{{"name": "stub.html", "size": 2048}},
			Errors:   []int{http.StatusInternalServerError, http.StatusServiceUnavailable},
		},
		"GET /api/pages/{name}": {
			ID: "get_page", Summary: "Read one HTML page.",
			Description: "Administrator key only. The name is a file name that ends in .html and holds letters, digits, " +
				"dots, hyphens and underscores. 400 for another name, 404 for a page that does not exist.",
			RU: docOf("Чтение одной HTML-страницы.",
				"Только для ключа администратора. Имя — имя файла с окончанием .html из букв, цифр, точек, дефисов и "+
					"подчёркиваний. 400 для другого имени, 404, если страницы нет."),
			UK: docOf("Читання однієї HTML-сторінки.",
				"Лише для ключа адміністратора. Ім'я — ім'я файлу із закінченням .html з літер, цифр, крапок, дефісів і "+
					"підкреслень. 400 для іншого імені, 404, якщо сторінки немає."),
			Response: pageRead,
			Errors:   []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable},
		},
		"PUT /api/pages/{name}": {
			ID: "put_page", Summary: "Create or replace one HTML page.",
			Description: "Administrator key only. The body holds the whole page text in content, up to 4 MiB; an existing " +
				"page of that name is replaced. The name rules are those of GET.",
			RU: docOf("Создание или замена одной HTML-страницы.",
				"Только для ключа администратора. В теле весь текст страницы в поле content, до 4 МиБ; страница с таким "+
					"именем заменяется. Правила имени те же, что у GET."),
			UK: docOf("Створення або заміна однієї HTML-сторінки.",
				"Лише для ключа адміністратора. У тілі весь текст сторінки в полі content, до 4 МіБ; сторінка з таким "+
					"іменем замінюється. Правила імені ті самі, що й у GET."),
			Body:     objectBody("content: the whole HTML text of the page (a string, up to 4 MiB)."),
			Example:  map[string]string{"content": "<!doctype html><title>Stub</title><p>Hello</p>"},
			Response: map[string]string{"status": "saved"},
			Errors:   []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusServiceUnavailable},
		},
		"DELETE /api/pages/{name}": {
			ID: "delete_page", Summary: "Delete one HTML page.",
			Description: "Administrator key only. Deleting a page that does not exist is not an error. A stream that still " +
				"points to the page then shows nothing for it.",
			RU: docOf("Удаление одной HTML-страницы.",
				"Только для ключа администратора. Удаление несуществующей страницы не считается ошибкой. Поток, который "+
					"всё ещё ссылается на страницу, после этого ничего по ней не покажет."),
			UK: docOf("Видалення однієї HTML-сторінки.",
				"Лише для ключа адміністратора. Видалення неіснуючої сторінки не вважається помилкою. Потік, який "+
					"усе ще посилається на сторінку, після цього нічого по ній не покаже."),
			Response: map[string]string{"status": "deleted"},
			Errors:   []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusServiceUnavailable},
		},
	}
}

// botProfileSample returns a sample answer of GET and PUT /api/bot-protection.
func botProfileSample() map[string]any {
	return map[string]any{
		"ch_ua": true, "ch_empty_ua": true, "ch_proxy": true,
		"ch_bot_ip_baidu": true, "ch_bot_ip_bing": true, "ch_bot_ip_google": true, "ch_bot_ip_mail": true,
		"ch_bot_ip_yahoo": true, "ch_bot_ip_yandex": true, "ch_bot_ip_others": true,
		"proxy_default": true, "private_relay": false,
	}
}

// cronConfigSample returns a sample cron configuration as GET /api/cron shows it and
// PUT /api/cron accepts it; a stored secret appears as the mask ********.
func cronConfigSample() map[string]any {
	return map[string]any{
		"telegram": map[string]any{"token": "********", "chat_id": "-1001234567890"},
		"ip_lists": map[string]any{"enabled": true, "every_minutes": 360, "sources": []map[string]string{
			{"url": "https://example.com/ranges.txt", "target": "ip_example", "mode": "replace"},
		}},
		"bot_signatures": map[string]any{"enabled": true, "every_minutes": 1440, "sources": []map[string]any{
			{"name": "example", "url": "https://example.com/bots.json", "enabled": true},
		}},
		"geo_db": map[string]any{"enabled": false, "every_minutes": 1440, "sources": []map[string]string{}},
		"virustotal": map[string]any{
			"enabled": false, "every_minutes": 720, "api_key": "********", "domains": []string{"example.com"},
			"from_groups": true, "threshold": 1, "disable_streams": false,
		},
		"disk":    map[string]any{"enabled": true, "every_minutes": 10, "paths": []string{}, "min_free_percent": 10},
		"cleanup": map[string]any{"enabled": true, "every_minutes": 1440, "keys_days": 30},
		"conversions": map[string]any{
			"enabled": false, "every_minutes": 1, "template": "[PROFIT] [GROUP] [COUNTRY]",
			"period": "today", "timezone": "UTC",
		},
		"keitaro_sync": map[string]any{"enabled": false, "every_minutes": 15},
	}
}

// cronRouteDocs documents the automation settings, the state and the run-now routes.
func cronRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/cron": {
			ID: "get_cron", Summary: "Get the automation settings and the state of every job.",
			Description: "Administrator key only. config is the saved configuration with secrets shown as ********. " +
				"status holds the heartbeat of the cron service and the last run of every job; alive is true while the " +
				"heartbeat is fresh. jobs lists the job names and floors the shortest allowed period of each in minutes. " +
				"geo_paths tells which geo databases are known and present; telegram_configured and keitaro_configured " +
				"say whether the notification channel and the Keitaro connection are set. config_error is present when " +
				"the saved configuration fails the checks. status.bot_signatures, after the first run of bot_signatures, " +
				"holds the number of rules in the bot signature database, the time it was last written and, per source, " +
				"the accepted and rejected rules, the time of the last good download and the last error. " +
				"500 when the configuration file cannot be read.",
			RU: docOf("Настройки автоматизации и состояние каждой задачи.",
				"Только для ключа администратора. config — сохранённая конфигурация, секреты показаны как ********. "+
					"status содержит пульс сервиса cron и последний запуск каждой задачи; alive равен true, пока пульс свежий. "+
					"jobs перечисляет имена задач, floors — наименьший допустимый период каждой в минутах. "+
					"geo_paths показывает, какие гео-базы известны и на месте; telegram_configured и keitaro_configured "+
					"говорят, заданы ли канал уведомлений и подключение к Keitaro. config_error есть, когда сохранённая "+
					"конфигурация не проходит проверки. status.bot_signatures после первого запуска bot_signatures содержит "+
					"число правил в базе сигнатур ботов, время её записи и по каждому источнику принятые и отклонённые правила, "+
					"время последней удачной загрузки и последнюю ошибку. 500, если файл конфигурации не читается."),
			UK: docOf("Налаштування автоматизації та стан кожного завдання.",
				"Лише для ключа адміністратора. config — збережена конфігурація, секрети показані як ********. "+
					"status містить пульс сервісу cron і останній запуск кожного завдання; alive дорівнює true, поки пульс свіжий. "+
					"jobs перелічує імена завдань, floors — найменший допустимий період кожного в хвилинах. "+
					"geo_paths показує, які гео-бази відомі та на місці; telegram_configured і keitaro_configured "+
					"кажуть, чи задані канал сповіщень і підключення до Keitaro. config_error є, коли збережена "+
					"конфігурація не проходить перевірки. status.bot_signatures після першого запуску bot_signatures містить "+
					"кількість правил у базі сигнатур ботів, час її запису та для кожного джерела прийняті й відхилені правила, "+
					"час останнього вдалого завантаження та останню помилку. 500, якщо файл конфігурації не читається."),
			Response: map[string]any{
				"available": true, "alive": true, "config": cronConfigSample(),
				"status": map[string]any{
					"heartbeat": "2026-11-16T12:00:00Z",
					"jobs": map[string]any{"disk": map[string]any{
						"last_run": "2026-11-16T11:55:00Z", "ok": true, "message": "free space 42%",
						"duration_ms": 12, "next_run": "2026-11-16T12:05:00Z", "running": false,
					}},
					"bot_signatures": map[string]any{
						"rules": 1840, "updated": "2026-11-16T03:00:00Z",
						"sources": []map[string]any{
							{"name": "example", "url": "https://example.com/bots.json", "rules": 1840, "rejected": 12, "ok": "2026-11-16T03:00:00Z"},
						},
					},
				},
				"jobs":   []string{"ip_lists", "bot_signatures", "geo_db", "virustotal", "disk", "cleanup", "conversions", "keitaro_sync"},
				"floors": map[string]int{"ip_lists": 10, "bot_signatures": 60, "geo_db": 60, "virustotal": 30, "disk": 1, "cleanup": 60, "conversions": 1, "keitaro_sync": 5},
				"geo_paths": map[string]bool{
					"city": true, "asn": true, "city_file": true, "asn_file": true, "dbip": true, "dbip_file": false,
				},
				"dbip_url":            "https://download.db-ip.com/free/dbip-asn-lite-{YYYY-MM}.mmdb.gz",
				"telegram_configured": true, "keitaro_configured": false,
			},
			Errors: []int{http.StatusInternalServerError},
		},
		"PUT /api/cron": {
			ID: "put_cron", Summary: "Save the automation settings.",
			Description: "Administrator key only; needs the cron key action. The body is the whole configuration in the shape " +
				"of config in GET: a section you leave out is saved as disabled, except keitaro_sync, which keeps its " +
				"saved value. A secret sent as ******** or left empty keeps the stored one. 400 with the reason when " +
				"a source URL, a list name, a bot signature source name (unique, letters, digits, dots, dashes, underscores) " +
				"or a period is not valid; the shortest periods are in floors of GET. " +
				"503 when the cron file is not set.",
			RU: docOf("Сохранение настроек автоматизации.",
				"Только для ключа администратора; нужна галочка ключа cron. Тело — вся конфигурация в форме config из GET: "+
					"пропущенный раздел сохраняется выключенным, кроме keitaro_sync, который сохраняет прежнее значение. "+
					"Секрет, присланный как ******** или пустым, сохраняет прежний. 400 с причиной, если адрес источника, "+
					"имя списка, имя источника сигнатур ботов (уникальное: буквы, цифры, точка, дефис, подчёркивание) "+
					"или период недопустимы; наименьшие периоды — в floors у GET. "+
					"503, если файл cron не задан."),
			UK: docOf("Збереження налаштувань автоматизації.",
				"Лише для ключа адміністратора; потрібна галочка ключа cron. Тіло — вся конфігурація у формі config із GET: "+
					"пропущений розділ зберігається вимкненим, окрім keitaro_sync, який зберігає попереднє значення. "+
					"Секрет, надісланий як ******** або порожнім, зберігає попередній. 400 з причиною, якщо адреса джерела, "+
					"ім'я списку, ім'я джерела сигнатур ботів (унікальне: літери, цифри, крапка, дефіс, підкреслення) "+
					"чи період недопустимі; найменші періоди — у floors у GET. "+
					"503, якщо файл cron не заданий."),
			Body: objectBody("The configuration sections telegram, ip_lists, bot_signatures, geo_db, virustotal, disk, cleanup, conversions " +
				"and keitaro_sync, each with enabled and every_minutes plus its own fields."),
			Example:  cronConfigSample(),
			Response: map[string]string{"status": "saved"},
			Errors:   []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusServiceUnavailable},
		},
		"POST /api/cron/run": {
			ID: "run_cron_job", Summary: "Ask the cron service to run one job now.",
			Description: "Administrator key only; needs the cron key action. job is ip_lists, bot_signatures, geo_db, virustotal, disk, " +
				"cleanup, conversions, keitaro_sync or telegram_test (sends a test message to the saved Telegram chat). " +
				"telegram_test from an API key is 403 \"cookie session required\"; a browser session may run it. " +
				"The answer is 202: the job is only queued and its result appears in GET /api/cron. " +
				"400 for an unknown job; for keitaro_sync 409 when Keitaro is not set up or a catch-up is already running. " +
				"503 when the cron file is not set.",
			RU: docOf("Запуск одной задачи cron прямо сейчас.",
				"Только для ключа администратора; нужна галочка ключа cron. job — ip_lists, bot_signatures, geo_db, virustotal, disk, "+
					"cleanup, conversions, keitaro_sync или telegram_test (отправляет тестовое сообщение в сохранённый чат Telegram). "+
					"telegram_test от ключа — 403 «cookie session required»; сессия браузера может его запустить. "+
					"Ответ 202: задача лишь поставлена в очередь, результат появится в GET /api/cron. "+
					"400 для неизвестной задачи; для keitaro_sync 409, если Keitaro не настроен или догрузка уже идёт. "+
					"503, если файл cron не задан."),
			UK: docOf("Запуск одного завдання cron просто зараз.",
				"Лише для ключа адміністратора; потрібна галочка ключа cron. job — ip_lists, bot_signatures, geo_db, virustotal, disk, "+
					"cleanup, conversions, keitaro_sync або telegram_test (надсилає тестове повідомлення у збережений чат Telegram). "+
					"telegram_test від ключа — 403 «cookie session required»; сесія браузера може його запустити. "+
					"Відповідь 202: завдання лише поставлене в чергу, результат з'явиться в GET /api/cron. "+
					"400 для невідомого завдання; для keitaro_sync 409, якщо Keitaro не налаштований або догрузка вже йде. "+
					"503, якщо файл cron не заданий."),
			Body:     objectBody("job: the job name (a string)."),
			Example:  map[string]string{"job": "disk"},
			Status:   http.StatusAccepted,
			Response: map[string]string{"status": "requested"},
			Errors:   []int{http.StatusBadRequest, http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable},
		},
	}
}

// updateRouteDocs documents the version, the update state and the update requests.
func updateRouteDocs() map[string]routeDoc {
	commit := "0123456789abcdef0123456789abcdef01234567"
	target := "89abcdef0123456789abcdef0123456789abcdef"
	images := map[string]string{
		"local": "emtds:v1.4.0", "engine": "emtds:v1.4.0", "admin": "emtds:v1.4.0", "cron": "emtds:v1.4.0",
	}
	return map[string]routeDoc{
		"GET /api/version": {
			ID: "get_version", Summary: "Get the build of the admin, the engine and the cron service.",
			Description: "Administrator key only. admin, engine and cron hold the version and commit of each process; " +
				"engine is null and engine_error says why when the engine does not answer, cron is null without a status " +
				"file. images are the image names the updater sees. mismatch is true when the commits differ.",
			RU: docOf("Сборка админки, движка и сервиса cron.",
				"Только для ключа администратора. admin, engine и cron содержат версию и коммит каждого процесса; "+
					"engine равен null, а engine_error объясняет причину, если движок не отвечает; cron равен null без файла "+
					"состояния. images — имена образов, которые видит обновлятор. mismatch равен true, когда коммиты различаются."),
			UK: docOf("Збірка адмінки, рушія та сервісу cron.",
				"Лише для ключа адміністратора. admin, engine і cron містять версію та коміт кожного процесу; "+
					"engine дорівнює null, а engine_error пояснює причину, якщо рушій не відповідає; cron дорівнює null без файлу "+
					"стану. images — імена образів, які бачить оновлювач. mismatch дорівнює true, коли коміти відрізняються."),
			Response: map[string]any{
				"admin": buildInfoSample(), "engine": buildInfoSample(),
				"cron":   map[string]string{"version": "v1.4.0", "commit": commit},
				"images": images, "mismatch": false,
			},
		},
		"GET /api/system": cookieDoc("get_system",
			docOf("Get the state of the host and of the services around the admin.",
				"Administrator browser session only; an API key gets 403. host holds cpus, load (1, 5 and 15 minutes), mem_total and "+
					"mem_available and disk_total and disk_free of the state volume in bytes, and uptime_s in seconds. services holds "+
					"clickhouse and redis as {state, ms}: state is ok, down or not_configured, and ms is the answer time in milliseconds, "+
					"present only for ok and down; each is checked with a 2 second limit. cron holds heartbeat_age_s and busy, "+
					"and is absent without a status file. A field with no data is left out."),
			docOf("Состояние хоста и сервисов рядом с админкой.",
				"Только для сессии администратора в браузере; ключ API получает 403. host содержит cpus, load (1, 5 и 15 минут), "+
					"mem_total и mem_available, disk_total и disk_free тома состояния в байтах и uptime_s в секундах. services содержит "+
					"clickhouse и redis как {state, ms}: state — ok, down или not_configured, ms — время ответа в миллисекундах, "+
					"только для ok и down; каждый сервис проверяется с лимитом 2 секунды. cron содержит heartbeat_age_s и busy "+
					"и отсутствует без файла состояния. Поле без данных не отдаётся."),
			docOf("Стан хоста та сервісів поруч з адмінкою.",
				"Лише для сесії адміністратора в браузері; ключ API отримує 403. host містить cpus, load (1, 5 і 15 хвилин), "+
					"mem_total і mem_available, disk_total і disk_free тому стану в байтах та uptime_s в секундах. services містить "+
					"clickhouse і redis як {state, ms}: state — ok, down або not_configured, ms — час відповіді в мілісекундах, "+
					"лише для ok і down; кожен сервіс перевіряється з лімітом 2 секунди. cron містить heartbeat_age_s і busy "+
					"та відсутній без файлу стану. Поле без даних не віддається.")).
			withResponse(map[string]any{
				"host": map[string]any{
					"cpus": 4, "load": []float64{0.12, 0.3, 0.25}, "mem_total": 8589934592, "mem_available": 4294967296,
					"uptime_s": 86400, "disk_total": 107374182400, "disk_free": 53687091200,
				},
				"services": map[string]any{
					"clickhouse": map[string]any{"state": "ok", "ms": 3},
					"redis":      map[string]any{"state": "ok", "ms": 1},
					"cron":       map[string]any{"heartbeat_age_s": 12, "busy": false},
				},
			}),
		"GET /api/system/signals": cookieDoc("get_system_signals",
			docOf("Get the marks of the header: a newer commit and the services that do not answer.",
				"Administrator browser session only; an API key gets 403. update is true when a fresh check of the update agent "+
					"found a newer commit for the running build; it turns false as soon as the new build runs. down lists the "+
					"services that do not answer, in the order engine, clickhouse, redis, and is an empty list when all answer; "+
					"a service that is not configured is not listed. Each service is checked with a 2 second limit."),
			docOf("Метки шапки: есть ли коммит новее и какие службы не отвечают.",
				"Только для сессии администратора в браузере; ключ API получает 403. update равен true, когда свежая проверка "+
					"агента обновлений нашла коммит новее работающей сборки, и становится false, как только запущена новая сборка. "+
					"down перечисляет службы, которые не отвечают, в порядке engine, clickhouse, redis, и пуст, когда отвечают все; "+
					"ненастроенная служба не попадает в список. Каждая служба проверяется с лимитом 2 секунды."),
			docOf("Мітки шапки: чи є новіший коміт і які служби не відповідають.",
				"Лише для сесії адміністратора в браузері; ключ API отримує 403. update дорівнює true, коли свіжа перевірка "+
					"агента оновлень знайшла коміт, новіший за робочу збірку, і стає false, щойно запущено нову збірку. "+
					"down перелічує служби, які не відповідають, у порядку engine, clickhouse, redis, і порожній, коли відповідають усі; "+
					"неналаштована служба не потрапляє до списку. Кожна служба перевіряється з лімітом 2 секунди.")).
			withResponse(map[string]any{"update": true, "down": []string{"redis"}}),
		"GET /api/update": {
			ID: "get_update", Summary: "Get the update state and whether an update or a rollback may start.",
			Description: "Administrator key only. build is the running build; agent, check and state are the files of the " +
				"updater and are null when missing or damaged. can_apply and can_rollback tell whether a button may be " +
				"used, with the reason in apply_block and rollback_block. check_stale is true when check was made for another " +
				"build than the running one; such a check offers no update (apply_block check_stale) until a new check. " +
				"keitaro_busy is true while a Keitaro job runs. " +
				"cli lists the matching commands for the server shell. The update itself is started only from a browser session.",
			RU: docOf("Состояние обновления и можно ли начать обновление или откат.",
				"Только для ключа администратора. build — запущенная сборка; agent, check и state — файлы обновлятора, "+
					"равны null, если файла нет или он повреждён. can_apply и can_rollback говорят, можно ли нажать кнопку, "+
					"причина — в apply_block и rollback_block. check_stale равен true, если check сделан для другой сборки, "+
					"чем запущенная; такая проверка не предлагает обновление (apply_block check_stale) до новой проверки. "+
					"keitaro_busy равен true, пока идёт задача Keitaro. "+
					"cli — подходящие команды для консоли сервера. Само обновление запускается только из сессии браузера."),
			UK: docOf("Стан оновлення та чи можна почати оновлення або відкіт.",
				"Лише для ключа адміністратора. build — запущена збірка; agent, check і state — файли оновлювача, "+
					"дорівнюють null, якщо файлу немає або він пошкоджений. can_apply і can_rollback кажуть, чи можна натиснути кнопку, "+
					"причина — в apply_block і rollback_block. check_stale дорівнює true, якщо check зроблено для іншої збірки, "+
					"ніж запущена; така перевірка не пропонує оновлення (apply_block check_stale) до нової перевірки. "+
					"keitaro_busy дорівнює true, поки йде завдання Keitaro. "+
					"cli — відповідні команди для консолі сервера. Саме оновлення запускається лише із сесії браузера."),
			Response: map[string]any{
				"build": buildInfoSample(),
				"agent": map[string]any{
					"schema": 1, "at": "2026-11-16T11:58:00Z", "script_version": "1.0", "repo": "https://example.com/emtds.git",
					"channel": "stable", "signing": true, "docker_ok": true, "inbox": true,
					"images": images, "mismatch": false,
				},
				"agent_fresh": true,
				"check": map[string]any{
					"schema": 1, "at": "2026-11-16T09:00:00Z", "channel": "stable",
					"current": map[string]string{"commit": commit, "tag": "v1.4.0"},
					"target":  map[string]string{"commit": target, "tag": "v1.4.1"},
					"ahead":   1,
					"commits": []map[string]string{{"sha": target, "subject": "Fix redirect rules", "author": "dev", "date": "2026-11-15"}},
					"notes":   "", "manual_steps": []string{}, "signed": true, "signer": "release", "error": "",
				},
				"state":           nil,
				"inbox":           true,
				"request_pending": false,
				"check_stale":     false,
				"keitaro_busy":    false,
				"can_apply":       true,
				"can_rollback":    true,
				"cli": []string{
					"sudo /usr/local/lib/emtds/update.sh --check",
					"sudo /usr/local/lib/emtds/update.sh --channel",
					"sudo /usr/local/lib/emtds/update.sh --to " + target,
					"sudo /usr/local/lib/emtds/update.sh --rollback",
				},
			},
		},
		"GET /api/update/log": {
			ID: "get_update_log", Summary: "Read the log of the last update run.",
			Description: "Administrator key only. log is the text of the updater log, empty when there is none; state_id is " +
				"the id of the run it belongs to.",
			RU: docOf("Журнал последнего запуска обновления.",
				"Только для ключа администратора. log — текст журнала обновлятора, пустой, если его нет; "+
					"state_id — id запуска, к которому он относится."),
			UK: docOf("Журнал останнього запуску оновлення.",
				"Лише для ключа адміністратора. log — текст журналу оновлювача, порожній, якщо його немає; "+
					"state_id — id запуску, до якого він належить."),
			Response: map[string]string{"log": "update started\nimages pulled\nupdate finished", "state_id": "20261116-100000-ab12"},
		},
		"POST /api/update/apply": cookieDoc("apply_update",
			docOf("Ask the updater to install the checked update.",
				"Body target (the commit of the check) and confirm (its first 7 characters). 202 with the request id. "+
					"409 with the reason code when an update cannot start now, for example no_update, check_stale, keitaro_running or request_pending."),
			docOf("Запрос установки проверенного обновления.",
				"Тело: target (коммит проверки) и confirm (его первые 7 символов). 202 с id запроса. "+
					"409 с кодом причины, если обновление сейчас начать нельзя, например no_update, check_stale, keitaro_running или request_pending."),
			docOf("Запит установлення перевіреного оновлення.",
				"Тіло: target (коміт перевірки) і confirm (його перші 7 символів). 202 з id запиту. "+
					"409 з кодом причини, якщо оновлення зараз почати не можна, наприклад no_update, check_stale, keitaro_running або request_pending.")),
		"POST /api/update/rollback": cookieDoc("rollback_update",
			docOf("Ask the updater to return to the previous version.",
				"Body confirm equal to rollback. 202 with the request id; 409 with the reason code when a rollback cannot start now."),
			docOf("Запрос возврата на предыдущую версию.",
				"Тело: confirm, равный rollback. 202 с id запроса; 409 с кодом причины, если откат сейчас начать нельзя."),
			docOf("Запит повернення на попередню версію.",
				"Тіло: confirm, що дорівнює rollback. 202 з id запиту; 409 з кодом причини, якщо відкіт зараз почати не можна.")),
		"POST /api/update/check": cookieDoc("check_update",
			docOf("Ask the updater to look for a new version.",
				"The body is empty. 202 with the request id; 429 rate_limited when the last check, a manual one or the one the updater "+
					"runs after an apply, was less than 5 minutes ago, with the seconds left in Retry-After and retry_after; "+
					"409 with the reason code when a check cannot start now."),
			docOf("Запрос поиска новой версии.",
				"Тело пустое. 202 с id запроса; 429 rate_limited, если прошлая проверка (ручная или та, что агент делает после "+
					"применения) была меньше 5 минут назад, с оставшимися секундами в Retry-After и retry_after; "+
					"409 с кодом причины, если проверку сейчас начать нельзя."),
			docOf("Запит пошуку нової версії.",
				"Тіло порожнє. 202 з id запиту; 429 rate_limited, якщо попередня перевірка (ручна або та, яку агент робить після "+
					"застосування) була менше 5 хвилин тому, із секундами, що лишилися, у Retry-After і retry_after; "+
					"409 з кодом причини, якщо перевірку зараз почати не можна.")),
	}
}

// accountRouteDocs documents the users, roles and API keys of the panel.
func accountRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/users": cookieDoc("list_users",
			docOf("List the panel accounts.",
				"The first row is the environment administrator; the others come from the users file. Password hashes are never returned. "+
					"board_ids lists the sections other than the main one where the account is a member; no_main_board: true means "+
					"a USER does not see the main section. A USER sees the main section unless no_main_board is set, plus board_ids; "+
					"an administrator sees every section."),
			docOf("Список аккаунтов панели.",
				"Первая строка — администратор из окружения, остальные из файла пользователей. Хэши паролей не возвращаются никогда. "+
					"board_ids — разделы, кроме основного, где аккаунт участник; no_main_board: true значит, что USER не видит "+
					"основной раздел. USER видит основной раздел, если no_main_board не задан, и разделы из board_ids; "+
					"администратор видит все разделы."),
			docOf("Список акаунтів панелі.",
				"Перший рядок — адміністратор із середовища, решта з файлу користувачів. Хеші паролів не повертаються ніколи. "+
					"board_ids — розділи, крім основного, де акаунт учасник; no_main_board: true означає, що USER не бачить "+
					"основний розділ. USER бачить основний розділ, якщо no_main_board не задано, і розділи з board_ids; "+
					"адміністратор бачить усі розділи.")),
		"POST /api/users": cookieDoc("create_user",
			docOf("Create a panel account.",
				"Body login, password, type (ADMIN or USER, USER by default), enabled, prefs, access, role_id and no_main_board "+
					"(true hides the main section from a USER; ignored for ADMIN). 201 with the "+
					"account; 400 for a bad field, 409 when the login is taken, 503 when users are disabled."),
			docOf("Создание аккаунта панели.",
				"Тело: login, password, type (ADMIN или USER, по умолчанию USER), enabled, prefs, access, role_id и no_main_board "+
					"(true скрывает основной раздел от USER; для ADMIN не учитывается). 201 с "+
					"аккаунтом; 400 при неверном поле, 409 если логин занят, 503 если пользователи отключены."),
			docOf("Створення акаунта панелі.",
				"Тіло: login, password, type (ADMIN або USER, типово USER), enabled, prefs, access, role_id і no_main_board "+
					"(true приховує основний розділ від USER; для ADMIN не враховується). 201 з "+
					"акаунтом; 400 при хибному полі, 409 якщо логін зайнятий, 503 якщо користувачі вимкнені.")),
		"GET /api/users/{id}": cookieDoc("get_user",
			docOf("Get one panel account.", "404 for an unknown id."),
			docOf("Один аккаунт панели.", "404 для неизвестного id."),
			docOf("Один акаунт панелі.", "404 для невідомого id.")),
		"PUT /api/users/{id}": cookieDoc("update_user",
			docOf("Change the login, type, state, preferences, role or main section of an account.",
				"Fields you leave out keep their value; the password and the access are changed by their own routes. "+
					"no_main_board: true hides the main section from a USER, false returns it; an ADMIN never keeps it. "+
					"board_ids is not changed here: it follows the section members. "+
					"409 when the change would disable yourself or remove the last administrator."),
			docOf("Смена логина, типа, состояния, настроек, роли или основного раздела аккаунта.",
				"Пропущенные поля сохраняют значение; пароль и доступ меняются отдельными маршрутами. "+
					"no_main_board: true скрывает основной раздел от USER, false возвращает его; у ADMIN поле не сохраняется. "+
					"board_ids здесь не меняется: он следует за участниками разделов. "+
					"409, если изменение отключило бы вас самих или убрало последнего администратора."),
			docOf("Зміна логіна, типу, стану, налаштувань, ролі або основного розділу акаунта.",
				"Пропущені поля зберігають значення; пароль і доступ змінюються окремими маршрутами. "+
					"no_main_board: true приховує основний розділ від USER, false повертає його; в ADMIN поле не зберігається. "+
					"board_ids тут не змінюється: він слідує за учасниками розділів. "+
					"409, якщо зміна вимкнула б вас самих або прибрала останнього адміністратора.")),
		"DELETE /api/users/{id}": cookieDoc("delete_user",
			docOf("Delete a panel account.",
				"204 on success. 409 when you delete yourself or the last administrator; 404 for an unknown id."),
			docOf("Удаление аккаунта панели.",
				"204 при успехе. 409, если вы удаляете себя или последнего администратора; 404 для неизвестного id."),
			docOf("Видалення акаунта панелі.",
				"204 у разі успіху. 409, якщо ви видаляєте себе або останнього адміністратора; 404 для невідомого id.")),
		"PUT /api/users/{id}/password": cookieDoc("set_user_password",
			docOf("Set or clear the password of an account.",
				"Body password (6 or more characters); an empty password clears the hash, so the account cannot sign in. 204 on success."),
			docOf("Установка или сброс пароля аккаунта.",
				"Тело: password (от 6 символов); пустой пароль сбрасывает хэш, и аккаунт не сможет войти. 204 при успехе."),
			docOf("Встановлення або скидання пароля акаунта.",
				"Тіло: password (від 6 символів); порожній пароль скидає хеш, і акаунт не зможе увійти. 204 у разі успіху.")),
		"PUT /api/users/{id}/access": cookieDoc("set_user_access",
			docOf("Replace the own access of an account.",
				"The body is the whole access object: sections, grants and hidden fields. 409 when the account has a role, "+
					"because the role then decides the access."),
			docOf("Замена собственного доступа аккаунта.",
				"Тело — весь объект доступа: разделы, права и скрытые поля. 409, если у аккаунта есть роль, "+
					"потому что тогда доступ определяет роль."),
			docOf("Заміна власного доступу акаунта.",
				"Тіло — весь об'єкт доступу: розділи, права та приховані поля. 409, якщо в акаунта є роль, "+
					"бо тоді доступ визначає роль.")),
		"GET /api/roles": cookieDoc("list_roles",
			docOf("List the roles.", "Sorted by name; users is how many accounts use each role."),
			docOf("Список ролей.", "Отсортирован по имени; users — сколько аккаунтов используют роль."),
			docOf("Список ролей.", "Відсортований за ім'ям; users — скільки акаунтів використовують роль.")),
		"POST /api/roles": cookieDoc("create_role",
			docOf("Create a role.",
				"Body name (1 to 64 characters) and access. 201 with the role; 409 when the name is taken."),
			docOf("Создание роли.",
				"Тело: name (1–64 символа) и access. 201 с ролью; 409, если имя занято."),
			docOf("Створення ролі.",
				"Тіло: name (1–64 символи) і access. 201 з роллю; 409, якщо ім'я зайняте.")),
		"POST /api/roles/reload": cookieDoc("reload_roles",
			docOf("Read the roles file again without a restart.",
				"Use it after a manual repair. 200 with the number of roles; 503 when the file is still unusable."),
			docOf("Повторное чтение файла ролей без перезапуска.",
				"Применяйте после ручной правки. 200 с числом ролей; 503, если файл всё ещё непригоден."),
			docOf("Повторне читання файлу ролей без перезапуску.",
				"Застосовуйте після ручного виправлення. 200 з кількістю ролей; 503, якщо файл досі непридатний.")),
		"GET /api/roles/{id}": cookieDoc("get_role",
			docOf("Get one role.", "404 for an unknown id."),
			docOf("Одна роль.", "404 для неизвестного id."),
			docOf("Одна роль.", "404 для невідомого id.")),
		"PUT /api/roles/{id}": cookieDoc("update_role",
			docOf("Change the name or the access of a role.",
				"A field you leave out keeps its value. The change applies to every account with the role at once."),
			docOf("Смена имени или доступа роли.",
				"Пропущенное поле сохраняет значение. Изменение сразу действует на все аккаунты с этой ролью."),
			docOf("Зміна імені або доступу ролі.",
				"Пропущене поле зберігає значення. Зміна одразу діє на всі акаунти з цією роллю.")),
		"DELETE /api/roles/{id}": cookieDoc("delete_role",
			docOf("Delete a role.", "204 on success; 409 while an account still uses the role."),
			docOf("Удаление роли.", "204 при успехе; 409, пока роль используется аккаунтом."),
			docOf("Видалення ролі.", "204 у разі успіху; 409, поки роль використовується акаунтом.")),
		"GET /api/api-keys": cookieDoc("list_api_keys",
			docOf("List the API keys.", "Returns the id, name, prefix, scopes, type, role, access and actions of each key, "+
				"created_at, expires_at (absent: never expires) and last_used_at with last_used_ip masked to a.b.c.x (absent: not used yet). "+
				"The last use is recorded at most once a minute and may lag by that much. The secret is stored as a hash and is never returned."),
			docOf("Список API-ключей.", "Возвращает id, имя, префикс, области, тип, роль, доступ и галочки каждого ключа, "+
				"created_at, expires_at (нет — бессрочный) и last_used_at с last_used_ip, замаскированным до a.b.c.x (нет — ещё не использовался). "+
				"Использование записывается не чаще раза в минуту и может отставать на столько же. Секрет хранится хэшем и не возвращается."),
			docOf("Список API-ключів.", "Повертає id, ім'я, префікс, області, тип, роль, доступ і галочки кожного ключа, "+
				"created_at, expires_at (немає — безстроковий) і last_used_at з last_used_ip, замаскованим до a.b.c.x (немає — ще не використовувався). "+
				"Використання записується не частіше разу на хвилину й може відставати на стільки ж. Секрет зберігається хешем і не повертається.")),
		"POST /api/api-keys": cookieDoc("create_api_key",
			docOf("Create an API key.",
				"Body name, scopes, type (ADMIN or USER), role_id, access, actions and ttl_days: 0 (default, never expires), 7, 30, 90 or 365; any other value is 400. "+
					"201 with key, the secret (em_ and 40 hex characters), shown only this once, and item, the saved record. "+
					"A key past its expiry is refused with 401 and code key_expired. 503 when API keys are disabled."),
			docOf("Создание API-ключа.",
				"Тело: name, scopes, type (ADMIN или USER), role_id, access, actions и ttl_days: 0 (по умолчанию, бессрочный), 7, 30, 90 или 365; другое значение — 400. "+
					"201 с key — секретом (em_ и 40 шестнадцатеричных знаков), который виден только сейчас, и item — сохранённой записью. "+
					"Истёкший ключ отклоняется с 401 и кодом key_expired. 503, если API-ключи отключены."),
			docOf("Створення API-ключа.",
				"Тіло: name, scopes, type (ADMIN або USER), role_id, access, actions і ttl_days: 0 (за замовчуванням, безстроковий), 7, 30, 90 або 365; інше значення — 400. "+
					"201 з key — секретом (em_ і 40 шістнадцяткових знаків), який видно лише зараз, і item — збереженим записом. "+
					"Ключ із терміном, що минув, відхиляється з 401 і кодом key_expired. 503, якщо API-ключі вимкнені.")),
		"POST /api/api-keys/{id}/rotate": cookieDoc("rotate_api_key",
			docOf("Replace the secret of an API key.",
				"Returns the new secret once; the old one stops working at once. 409 for a revoked key."),
			docOf("Замена секрета API-ключа.",
				"Возвращает новый секрет один раз; старый перестаёт работать сразу. 409 для отозванного ключа."),
			docOf("Заміна секрета API-ключа.",
				"Повертає новий секрет один раз; старий перестає працювати одразу. 409 для відкликаного ключа.")),
		"PUT /api/api-keys/{id}": cookieDoc("update_api_key",
			docOf("Change an API key.",
				"A field you leave out keeps its value. ttl_days (0, 7, 30, 90 or 365) sets a new lifetime counted from now and keeps the secret: this is how a key is extended. 409 for a revoked key."),
			docOf("Изменение API-ключа.",
				"Пропущенное поле сохраняет значение. ttl_days (0, 7, 30, 90 или 365) задаёт новый срок от текущего момента и сохраняет секрет — так ключ продлевается. 409 для отозванного ключа."),
			docOf("Зміна API-ключа.",
				"Пропущене поле зберігає значення. ttl_days (0, 7, 30, 90 або 365) задає новий термін від поточного моменту й зберігає секрет — так ключ продовжується. 409 для відкликаного ключа.")),
		"DELETE /api/api-keys/{id}": func() routeDoc {
			d := cookieDoc("delete_api_key",
				docOf("Revoke an API key, or remove a revoked one.",
					"Without hard the key is revoked and stops working. With hard=1 a key that is already revoked is removed from the list; 409 for an active key."),
				docOf("Отзыв API-ключа или удаление уже отозванного.",
					"Без hard ключ отзывается и перестаёт работать. С hard=1 уже отозванный ключ удаляется из списка; 409 для действующего ключа."),
				docOf("Відкликання API-ключа або видалення вже відкликаного.",
					"Без hard ключ відкликається й перестає працювати. З hard=1 уже відкликаний ключ видаляється зі списку; 409 для чинного ключа."))
			d.Query = []queryParam{strQ("hard", "Send 1 to remove an already revoked key from the list instead of revoking it.").
				loc("Передайте 1, чтобы убрать уже отозванный ключ из списка вместо отзыва.",
					"Передайте 1, щоб прибрати вже відкликаний ключ зі списку замість відкликання.")}
			d.Status = http.StatusNoContent
			return d
		}(),
	}
}

// integrationRouteDocs documents the Cloudflare and Namecheap settings and the domain
// connection routes.
func integrationRouteDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/cloudflare": cookieDoc("get_cloudflare",
			docOf("Get the Cloudflare account settings.",
				"Returns account_id, has_token, configured and public_ip; the token is never returned. error is set when the saved file could not be used."),
			docOf("Настройки аккаунта Cloudflare.",
				"Возвращает account_id, has_token, configured и public_ip; токен не возвращается никогда. error задан, если сохранённый файл не удалось использовать."),
			docOf("Налаштування акаунта Cloudflare.",
				"Повертає account_id, has_token, configured і public_ip; токен не повертається ніколи. error заданий, якщо збережений файл не вдалося використати.")),
		"PUT /api/cloudflare": cookieDoc("put_cloudflare",
			docOf("Save the Cloudflare token and account id.",
				"Body token and account_id (32 hex characters). An empty or masked token keeps the stored one. Answers like GET; 503 when the settings file is not set."),
			docOf("Сохранение токена и id аккаунта Cloudflare.",
				"Тело: token и account_id (32 hex-символа). Пустой или замаскированный токен оставляет прежний. Ответ как у GET; 503, если файл настроек не задан."),
			docOf("Збереження токена та id акаунта Cloudflare.",
				"Тіло: token і account_id (32 hex-символи). Порожній або замаскований токен залишає попередній. Відповідь як у GET; 503, якщо файл налаштувань не заданий.")),
		"POST /api/cloudflare/test": cookieDoc("test_cloudflare",
			docOf("Check the Cloudflare token and count the zones of the account.",
				"Empty body fields fall back to the saved ones. Nothing is saved. A refused token is still 200 with ok false and the reason in error."),
			docOf("Проверка токена Cloudflare и подсчёт зон аккаунта.",
				"Пустые поля тела берутся из сохранённых. Ничего не сохраняется. Отклонённый токен — всё равно 200 с ok равным false и причиной в error."),
			docOf("Перевірка токена Cloudflare та підрахунок зон акаунта.",
				"Порожні поля тіла беруться зі збережених. Нічого не зберігається. Відхилений токен — усе одно 200 з ok, рівним false, і причиною в error.")),
		"POST /api/domains/{id}/cloudflare": cookieDoc("connect_domain_cloudflare",
			docOf("Put a parked domain behind Cloudflare.",
				"Body confirm true. 200 with the domain and the Cloudflare name servers (ns); ns_set tells whether they were set at the registrar. "+
					"409 when Cloudflare or the public address is not set up or a connection is already running; 502 with the Cloudflare error."),
			docOf("Подключение припаркованного домена к Cloudflare.",
				"Тело: confirm равный true. 200 с доменом и NS Cloudflare (ns); ns_set показывает, выставлены ли они у регистратора. "+
					"409, если Cloudflare или публичный адрес не настроены либо подключение уже идёт; 502 с ошибкой Cloudflare."),
			docOf("Підключення припаркованого домену до Cloudflare.",
				"Тіло: confirm, рівний true. 200 з доменом і NS Cloudflare (ns); ns_set показує, чи виставлені вони в реєстратора. "+
					"409, якщо Cloudflare чи публічна адреса не налаштовані або підключення вже йде; 502 з помилкою Cloudflare.")),
		"POST /api/domains/{id}/admin-dns": cookieDoc("ensure_domain_admin_dns",
			docOf("Create or update the admin.<host> DNS record of a domain in Cloudflare.",
				"The body is empty. 200 with the updated domain; 409 when the setup is incomplete or an update of this domain is running; "+
					"404 for an unknown id; 502 when Cloudflare fails."),
			docOf("Создание или обновление DNS-записи admin.<хост> домена в Cloudflare.",
				"Тело пустое. 200 с обновлённым доменом; 409, если настройка неполная или обновление этого домена уже идёт; "+
					"404 для неизвестного id; 502, если Cloudflare вернул ошибку."),
			docOf("Створення або оновлення DNS-запису admin.<хост> домену в Cloudflare.",
				"Тіло порожнє. 200 з оновленим доменом; 409, якщо налаштування неповне або оновлення цього домену вже йде; "+
					"404 для невідомого id; 502, якщо Cloudflare повернув помилку.")),
		"POST /api/domains/{id}/ns": cookieDoc("set_domain_nameservers",
			docOf("Point a domain bought through Namecheap to other name servers.",
				"Body nameservers (2 to 12 names) and confirm true. Only a domain registered through the saved Namecheap account is accepted. "+
					"200 with the new name servers; 403 for another account; 502 when Namecheap fails."),
			docOf("Перенаправление домена, купленного через Namecheap, на другие NS.",
				"Тело: nameservers (2–12 имён) и confirm равный true. Принимается только домен, зарегистрированный через сохранённый аккаунт Namecheap. "+
					"200 с новыми NS; 403 для чужого аккаунта; 502, если Namecheap вернул ошибку."),
			docOf("Перенаправлення домену, купленого через Namecheap, на інші NS.",
				"Тіло: nameservers (2–12 імен) і confirm, рівний true. Приймається лише домен, зареєстрований через збережений акаунт Namecheap. "+
					"200 з новими NS; 403 для чужого акаунта; 502, якщо Namecheap повернув помилку.")),
		"POST /api/domains/connected/ack": cookieDoc("ack_connected_domains",
			docOf("Mark the connection notices of domains as seen.",
				"Body ids, at most 500 domain ids. Answers acked, the number of listed domains that have a connection time; it is safe to repeat."),
			docOf("Отметка уведомлений о подключении доменов как просмотренных.",
				"Тело: ids, не больше 500 id доменов. Отвечает acked — числом перечисленных доменов со временем подключения; повтор безопасен."),
			docOf("Позначка сповіщень про підключення доменів як переглянутих.",
				"Тіло: ids, не більше 500 id доменів. Відповідає acked — кількістю перелічених доменів із часом підключення; повтор безпечний.")),
		"GET /api/registrars/namecheap": cookieDoc("get_namecheap",
			docOf("Get the Namecheap account settings.",
				"Returns name, api_user, username, client_ip, sandbox, has_key, has_contacts and configured; the key and the contacts are never returned."),
			docOf("Настройки аккаунта Namecheap.",
				"Возвращает name, api_user, username, client_ip, sandbox, has_key, has_contacts и configured; ключ и контакты не возвращаются никогда."),
			docOf("Налаштування акаунта Namecheap.",
				"Повертає name, api_user, username, client_ip, sandbox, has_key, has_contacts і configured; ключ і контакти не повертаються ніколи.")),
		"PUT /api/registrars/namecheap": cookieDoc("put_namecheap",
			docOf("Save the Namecheap account.",
				"Body name, api_user, username, client_ip, api_key, sandbox and contacts. An empty or masked api_key keeps the stored key and missing contacts keep the stored ones. "+
					"409 while a registration runs; 503 when the settings file is not set."),
			docOf("Сохранение аккаунта Namecheap.",
				"Тело: name, api_user, username, client_ip, api_key, sandbox и contacts. Пустой или замаскированный api_key оставляет прежний ключ, непереданные контакты остаются прежними. "+
					"409, пока идёт регистрация; 503, если файл настроек не задан."),
			docOf("Збереження акаунта Namecheap.",
				"Тіло: name, api_user, username, client_ip, api_key, sandbox і contacts. Порожній або замаскований api_key залишає попередній ключ, непередані контакти залишаються попередніми. "+
					"409, поки йде реєстрація; 503, якщо файл налаштувань не заданий.")),
		"POST /api/registrars/namecheap/test": cookieDoc("test_namecheap",
			docOf("Check the Namecheap account and find the outgoing IP address.",
				"Empty body fields fall back to the saved ones. With detect true the answer holds detected_ip, the address Namecheap saw. "+
					"Nothing is saved. A refused call is still 200 with ok false."),
			docOf("Проверка аккаунта Namecheap и поиск исходящего IP-адреса.",
				"Пустые поля тела берутся из сохранённых. С detect равным true в ответе есть detected_ip — адрес, который увидел Namecheap. "+
					"Ничего не сохраняется. Отклонённый вызов — всё равно 200 с ok равным false."),
			docOf("Перевірка акаунта Namecheap та пошук вихідної IP-адреси.",
				"Порожні поля тіла беруться зі збережених. З detect, рівним true, у відповіді є detected_ip — адреса, яку побачив Namecheap. "+
					"Нічого не зберігається. Відхилений виклик — усе одно 200 з ok, рівним false.")),
		"POST /api/registrars/namecheap/check": {
			ID: "check_namecheap_domains", Summary: "Check whether domains can be registered at Namecheap and what a year costs.",
			Description: "Browser session of an administrator only: an API key gets 403 \"cookie session required\". " +
				"Checks 1 to 50 domain names with the Namecheap account saved in the panel and " +
				"prices the available ordinary names for one year in USD; premium names get no price. Nothing is bought. " +
				"400 for a bad or too long list, 503 when the account is not configured, 502 when Namecheap fails.",
			RU: docOf("Можно ли зарегистрировать домены в Namecheap и сколько стоит год.",
				"Только сессия браузера администратора: ключ получает 403 «cookie session required». "+
					"Проверяет от 1 до 50 имён доменов через аккаунт Namecheap, сохранённый в панели, "+
					"и оценивает свободные обычные имена на один год в USD; у премиум-имён цены нет. Ничего не покупается. "+
					"400 для неверного или слишком длинного списка, 503, если аккаунт не настроен, 502, если Namecheap вернул ошибку."),
			UK: docOf("Чи можна зареєструвати домени в Namecheap і скільки коштує рік.",
				"Лише сесія браузера адміністратора: ключ отримує 403 «cookie session required». "+
					"Перевіряє від 1 до 50 імен доменів через акаунт Namecheap, збережений у панелі, "+
					"і оцінює вільні звичайні імена на один рік у USD; у преміум-імен ціни немає. Нічого не купується. "+
					"400 для хибного або надто довгого списку, 503, якщо акаунт не налаштований, 502, якщо Namecheap повернув помилку."),
			Body:    objectBody("domains: a list of 1 to 50 domain names."),
			Example: map[string]any{"domains": []string{"example.com", "example.net"}},
			Response: map[string]any{"results": []map[string]any{
				{"domain": "example.com", "available": true, "premium": false, "price": 10.98, "currency": "USD"},
				{"domain": "example.net", "available": false, "premium": false, "price": nil, "currency": "USD"},
			}},
			Errors: []int{http.StatusBadRequest, http.StatusForbidden, http.StatusBadGateway, http.StatusServiceUnavailable},
		},
		"POST /api/registrars/namecheap/register": cookieDoc("register_namecheap_domain",
			docOf("Buy a domain at Namecheap and park it.",
				"Body domain, years, max_price (the most you accept for the term), group, nameservers or cloudflare, and reconcile. "+
					"The server prices the term again and refuses a higher price. 201 with the domain; 409 for an existing, pending or unavailable name or a changed price; "+
					"422 for a premium name; 502 when Namecheap fails."),
			docOf("Покупка домена в Namecheap и парковка.",
				"Тело: domain, years, max_price (максимум, который вы принимаете за срок), group, nameservers или cloudflare и reconcile. "+
					"Сервер заново оценивает срок и отказывает при более высокой цене. 201 с доменом; 409 для существующего, ожидающего или недоступного имени либо изменившейся цены; "+
					"422 для премиум-имени; 502, если Namecheap вернул ошибку."),
			docOf("Купівля домену в Namecheap і паркування.",
				"Тіло: domain, years, max_price (максимум, який ви приймаєте за термін), group, nameservers або cloudflare і reconcile. "+
					"Сервер заново оцінює термін і відмовляє за вищої ціни. 201 з доменом; 409 для наявного, очікуваного або недоступного імені чи зміненої ціни; "+
					"422 для преміум-імені; 502, якщо Namecheap повернув помилку.")),
	}
}
