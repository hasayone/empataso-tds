package admin

import "net/http"

// netTemplateSample returns a sample template record: a catalog template with one saved edit.
func netTemplateSample() map[string]any {
	return map[string]any{
		"id": "example-network", "name": "Example Network", "offer_query": "sub1={subid}",
		"postback_url": "{origin}/postback?subid={click_id}&status={status}&payout={payout}",
		"instruction":  "<p>Paste the postback address into the postback settings of the network cabinet.</p>",
		"conversions":  map[string]string{"lead_status": "lead", "sale_status": "approved"},
		"source":       "keitaro-json", "extracted": "full", "edited": true, "custom": false,
	}
}

// netTemplateWriteDoc returns the doc of a route that saves a template: a session-only route with
// the template body, the template as the answer, status as the success status and errs as the
// statuses it answers itself.
func netTemplateWriteDoc(id string, status int, en, ru, uk docText, errs ...int) routeDoc {
	d := cookieDoc(id, en, ru, uk).withBody(
		objectBody("name, offer_query, postback_url and instruction; every field is optional on PUT."),
		map[string]string{
			"name": "Example Network", "offer_query": "sub1={subid}",
			"postback_url": "{origin}/postback?subid={click_id}&status={status}&payout={payout}",
			"instruction":  "<p>Paste the postback address into the postback settings of the network cabinet.</p>",
		}, errs...)
	d.Status, d.Response = status, netTemplateSample()
	return d
}

// netTemplateDeleteDoc returns the doc of the route that resets or removes a template: a
// session-only route that answers 204 without a body.
func netTemplateDeleteDoc(en, ru, uk docText) routeDoc {
	d := cookieDoc("delete_affiliate_network_template", en, ru, uk)
	d.Status, d.Errors = http.StatusNoContent, []int{http.StatusNotFound, http.StatusServiceUnavailable}
	return d
}

// netTemplateDocs documents the routes of the network template catalog: the list, and the add, save and reset routes of the edits.
func netTemplateDocs() map[string]routeDoc {
	return map[string]routeDoc{
		"GET /api/affiliate-network-templates": {
			ID: "list_affiliate_network_templates", Summary: "List the affiliate network templates.",
			Description: "Returns the network templates of the network dialog, one record per network name: id, name, offer_query, postback_url, instruction, conversions, source, extracted, edited and custom. " +
				"The built-in catalog comes first, with the edits saved on the data volume laid over it; templates added through the API follow. " +
				"offer_query is the query string of the offer link. postback_url is the address for the network cabinet; the {origin} token stands for the public address of this tracker and the macros of the network stay as they are. " +
				"instruction is the text of the network guide as the source wrote it (it may hold p, br, code and a tags); conversions are the status lists of the network, which the tracker does not map. " +
				"extracted is full (offer query and postback URL known), partial (one of them) or none (only the name is known; the other fields are empty). edited is true when a saved edit replaced a catalog field; custom is true for an added template. " +
				"The list holds no secrets and the engine never calls these addresses.",
			RU: docText{
				Summary: "Список шаблонов партнёрских сетей.",
				Description: "Возвращает шаблоны для диалога сети, по записи на название: id, name, offer_query, postback_url, instruction, conversions, source, extracted, edited и custom. " +
					"Сначала встроенный каталог с наложенными правками с тома данных, затем шаблоны, добавленные через API. " +
					"offer_query — строка параметров ссылки оффера. postback_url — адрес для кабинета сети; токен {origin} означает публичный адрес этого трекера, макросы сети остаются как есть. " +
					"instruction — текст руководства сети как в источнике (допустимы теги p, br, code и a); conversions — списки статусов сети, трекер их не сопоставляет. " +
					"extracted: full (известны параметры оффера и постбек), partial (одно из двух) или none (известно только название, остальные поля пусты). edited равен true, когда сохранённая правка заменила поле каталога; custom равен true у добавленного шаблона. " +
					"В списке нет секретов, движок эти адреса не вызывает.",
			},
			UK: docText{
				Summary: "Список шаблонів партнерських мереж.",
				Description: "Повертає шаблони для діалогу мережі, по запису на назву: id, name, offer_query, postback_url, instruction, conversions, source, extracted, edited і custom. " +
					"Спочатку вбудований каталог із накладеними правками з тому даних, потім шаблони, додані через API. " +
					"offer_query — рядок параметрів посилання офера. postback_url — адреса для кабінету мережі; токен {origin} означає публічну адресу цього трекера, макроси мережі лишаються як є. " +
					"instruction — текст посібника мережі як у джерелі (допустимі теги p, br, code і a); conversions — списки статусів мережі, трекер їх не зіставляє. " +
					"extracted: full (відомі параметри офера і постбек), partial (одне з двох) або none (відома лише назва, решта полів порожні). edited дорівнює true, коли збережена правка замінила поле каталогу; custom дорівнює true у доданого шаблону. " +
					"У списку немає секретів, рушій ці адреси не викликає.",
			},
			Response: []any{netTemplateSample()},
			Errors:   []int{403},
		},
		"POST /api/affiliate-network-templates": netTemplateWriteDoc("create_affiliate_network_template", http.StatusCreated,
			docOf("Add a network template.",
				"Body: name (required, up to 80 characters; its id must be free among the catalog and the added templates), offer_query, postback_url and instruction. Saved on the data volume, never in the built-in catalog. postback_url must be {origin}/postback with an optional query: a foreign host or a path token gives 400. "+
					"201 with the template; 400 for a bad body or a missing name, 409 when the id is taken, 503 when the edits file is not set. Session only; needs the right to change networks."),
			docOf("Добавление шаблона сети.",
				"Тело: name (обязательно, до 80 символов; его id должен быть свободен среди каталога и добавленных шаблонов), offer_query, postback_url и instruction. Хранится на томе данных, не во встроенном каталоге. postback_url — только {origin}/postback с необязательным query: чужой хост или путь-токен даёт 400. "+
					"201 с шаблоном; 400 при неверном теле или без названия, 409, если id занят, 503, если файл правок не задан. Только сессия; нужно право менять сети."),
			docOf("Додавання шаблону мережі.",
				"Тіло: name (обов'язково, до 80 символів; його id має бути вільним серед каталогу і доданих шаблонів), offer_query, postback_url і instruction. Зберігається на томі даних, не у вбудованому каталозі. postback_url — лише {origin}/postback з необов'язковим query: чужий хост або шлях-токен дає 400. "+
					"201 із шаблоном; 400 за хибного тіла або без назви, 409, якщо id зайнятий, 503, якщо файл правок не задано. Лише сесія; потрібне право змінювати мережі."),
			http.StatusBadRequest, http.StatusConflict, http.StatusServiceUnavailable),
		"PUT /api/affiliate-network-templates/{id}": netTemplateWriteDoc("update_affiliate_network_template", http.StatusOK,
			docOf("Save the edit of a network template.",
				"Body: offer_query, postback_url ({origin}/postback only, otherwise 400) and instruction; a name, if sent, must make the same id. For a catalog template a field that is empty or equal to the catalog value is not stored and follows the catalog, so an empty field never blanks it and clearing a field drops its edit. "+
					"For an added template the fields are stored as sent. 200 with the template; 400 for a bad body or another name, 404 for an unknown id, 503 when the edits file is not set. Session only; needs the right to change networks."),
			docOf("Сохранение правки шаблона сети.",
				"Тело: offer_query, postback_url (только {origin}/postback, иначе 400) и instruction; name, если передан, должен дать тот же id. У шаблона каталога поле, пустое или равное значению каталога, не сохраняется и следует каталогу: пустое поле каталог не затирает, а очистка поля снимает его правку. "+
					"У добавленного шаблона поля хранятся как переданы. 200 с шаблоном; 400 при неверном теле или другом названии, 404 для неизвестного id, 503, если файл правок не задан. Только сессия; нужно право менять сети."),
			docOf("Збереження правки шаблону мережі.",
				"Тіло: offer_query, postback_url (лише {origin}/postback, інакше 400) і instruction; name, якщо передано, має дати той самий id. У шаблону каталогу поле, порожнє або рівне значенню каталогу, не зберігається і йде за каталогом: порожнє поле каталог не затирає, а очищення поля знімає його правку. "+
					"У доданого шаблону поля зберігаються як передано. 200 із шаблоном; 400 за хибного тіла або іншої назви, 404 для невідомого id, 503, якщо файл правок не задано. Лише сесія; потрібне право змінювати мережі."),
			http.StatusBadRequest, http.StatusNotFound, http.StatusServiceUnavailable),
		"DELETE /api/affiliate-network-templates/{id}": netTemplateDeleteDoc(
			docOf("Reset or remove a network template.",
				"A catalog template loses its saved edit and returns to the catalog values; an added template is removed. 204 with no body; 404 for an unknown id, 503 when the edits file is not set. Session only; needs the right to change networks."),
			docOf("Сброс или удаление шаблона сети.",
				"Шаблон каталога теряет сохранённую правку и возвращается к значениям каталога; добавленный шаблон удаляется. 204 без тела; 404 для неизвестного id, 503, если файл правок не задан. Только сессия; нужно право менять сети."),
			docOf("Скидання або видалення шаблону мережі.",
				"Шаблон каталогу втрачає збережену правку і повертається до значень каталогу; доданий шаблон видаляється. 204 без тіла; 404 для невідомого id, 503, якщо файл правок не задано. Лише сесія; потрібне право змінювати мережі.")),
	}
}
