package admin

import "net/http"

// aiRoutePatterns lists every /api/ai/* route in the order they are mounted. All of them are
// closed to Api-Key sessions (see cookieRoutes) and none is visible to MCP.
var aiRoutePatterns = []string{
	"GET /api/ai/settings", "PUT /api/ai/settings", "POST /api/ai/test", "GET /api/ai/models", "GET /api/ai/status",
	"GET /api/ai/chats", "POST /api/ai/chats", "GET /api/ai/chats/{id}", "DELETE /api/ai/chats/{id}",
	"POST /api/ai/chats/{id}/messages", "POST /api/ai/chats/{id}/revert",
	"GET /api/ai/changes", "POST /api/ai/changes/{id}/apply", "POST /api/ai/changes/{id}/reject",
	"POST /api/ai/changes/{id}/revert", "POST /api/ai/messages/{message_id}/revert",
}

// aiHandlers returns the handler of every route in aiRoutePatterns.
func (s *Server) aiHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /api/ai/settings":                      s.handleAISettingsGet,
		"PUT /api/ai/settings":                      s.handleAISettingsPut,
		"POST /api/ai/test":                         s.handleAITest,
		"GET /api/ai/models":                        s.handleAIModels,
		"GET /api/ai/status":                        s.handleAIStatus,
		"GET /api/ai/chats":                         s.handleAIChatsList,
		"POST /api/ai/chats":                        s.handleAIChatCreate,
		"GET /api/ai/chats/{id}":                    s.handleAIChatGet,
		"DELETE /api/ai/chats/{id}":                 s.handleAIChatDelete,
		"POST /api/ai/chats/{id}/messages":          s.handleAIMessage,
		"POST /api/ai/chats/{id}/revert":            s.handleAIChatRevert,
		"GET /api/ai/changes":                       s.handleAIChangesList,
		"POST /api/ai/changes/{id}/apply":           s.handleAIChangeApply,
		"POST /api/ai/changes/{id}/reject":          s.handleAIChangeReject,
		"POST /api/ai/changes/{id}/revert":          s.handleAIChangeRevert,
		"POST /api/ai/messages/{message_id}/revert": s.handleAIMessageRevert,
	}
}

// registerAssistant mounts every /api/ai/* route through s.handle, so each one gets the
// session check, the access rule and the Api-Key refusal of routeAccess. It panics at startup
// when a pattern has no handler: the table is fixed at build time.
func registerAssistant(s *Server, mux *http.ServeMux) {
	handlers := s.aiHandlers()
	for _, pattern := range aiRoutePatterns {
		h, ok := handlers[pattern]
		if !ok {
			// Intentional startup panic: a route in the table without a handler is a build-time mistake.
			panic("admin: AI route without handler: " + pattern)
		}
		s.handle(mux, pattern, h)
	}
}

// addAssistantRoutes adds the access rules of the /api/ai/* routes to m. Settings, the key
// test and the model catalog follow the integrations section. The status is open to every signed-in user: it tells a
// session whether the panel is available to it. Every other route is for administrators
// only while aiAdminOnly holds; the handlers check aiAllowed again.
func addAssistantRoutes(m map[string]routeRule, open func() routeRule, rule func(section) routeRule) {
	m["GET /api/ai/settings"] = rule(secIntegrations)
	m["PUT /api/ai/settings"] = rule(secIntegrations)
	m["POST /api/ai/test"] = rule(secIntegrations)
	m["GET /api/ai/models"] = rule(secIntegrations)
	m["GET /api/ai/status"] = open()
	use := routeRule{sec: secAny, admin: aiAdminOnly}
	for _, pattern := range aiRoutePatterns {
		if _, set := m[pattern]; !set {
			m[pattern] = use
		}
	}
}

// aiRouteDocs returns the documentation of the /api/ai/* routes for OpenAPI. None of them is an
// MCP tool, and the text never names the section.
func aiRouteDocs() map[string]routeDoc {
	chatBody := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string", "description": "Optional chat title, cut to 80 characters."},
		},
	}
	messageBody := map[string]any{
		"type":     "object",
		"required": []any{"text"},
		"properties": map[string]any{
			"text":    map[string]any{"type": "string", "description": "The user message, up to 8000 characters."},
			"context": map[string]any{"type": "object", "additionalProperties": true, "description": "Page context: page, arg, board, range, filters, tab, campaign. Other keys are ignored."},
			"model":   map[string]any{"type": "string", "description": "Model id from allowed_models; empty means the default model."},
		},
	}
	revertBody := map[string]any{
		"type":     "object",
		"required": []any{"chat_id"},
		"properties": map[string]any{
			"chat_id": map[string]any{"type": "string", "description": "Chat that holds the message."},
		},
	}
	return map[string]routeDoc{
		"GET /api/ai/settings": {ID: "get_ai_settings", Summary: "Read the AI helper settings",
			Description: "Provider, model, allowed models, limits, helper permissions (perms), the three helper documents (docs: soul, memory, expertise as markdown, with max_bytes) and which provider keys are set. A key is never returned.",
			RU: docText{Summary: "Прочитать настройки ИИ-помощника",
				Description: "Провайдер, модель, разрешённые модели, лимиты, права помощника (perms), три документа помощника (docs: soul, memory, expertise в markdown, с max_bytes) и то, для каких провайдеров задан ключ. Сам ключ никогда не возвращается."},
			UK: docText{Summary: "Прочитати налаштування ШІ-помічника",
				Description: "Провайдер, модель, дозволені моделі, ліміти, права помічника (perms), три документи помічника (docs: soul, memory, expertise у markdown, з max_bytes) та те, для яких провайдерів задано ключ. Сам ключ ніколи не повертається."}},
		"PUT /api/ai/settings": {ID: "put_ai_settings", Summary: "Change the AI helper settings",
			Description: "Partial update; a field left out stays as stored. perms is {preset: read|edit|full|custom, resources: {resource: none|read|edit|delete}}; resources count only for custom. user_budget_usd and group_budget_usd are maps of account id and of role id to a USD budget of the day; they replace the whole map, an entry of 0 or a missing id means no budget, a negative amount is refused. A turn that would pass the budget of the user or of the group is refused with ai_quota (params scope: user_budget or group_budget). The same maps, budget_accounts and budget_groups come back from GET. docs is {soul, memory, expertise}: markdown texts of the helper documents that go into the context of every turn, each up to 8192 bytes (longer is ai_bad_request, field docs.<name>, reason too_long); a text left out stays as stored and a document never saved reads as its built-in text.",
			RU: docText{Summary: "Изменить настройки ИИ-помощника",
				Description: "Частичное обновление: пропущенное поле остаётся как было. perms — {preset: read|edit|full|custom, resources: {ресурс: none|read|edit|delete}}; resources учитываются только для custom. user_budget_usd и group_budget_usd — карты «id аккаунта» и «id роли» → бюджет в USD на сутки; заменяют карту целиком, запись 0 или отсутствующий id значит «без бюджета», отрицательная сумма отклоняется. Ход, который превысил бы бюджет пользователя или группы, отклоняется с ai_quota (params scope: user_budget или group_budget). Те же карты, budget_accounts и budget_groups приходят в GET. docs — {soul, memory, expertise}: markdown-тексты документов помощника, которые входят в контекст каждого хода, каждый до 8192 байт (длиннее — ai_bad_request, field docs.<имя>, reason too_long); пропущенный текст остаётся как был, а документ, который ни разу не сохраняли, читается как вшитый текст."},
			UK: docText{Summary: "Змінити налаштування ШІ-помічника",
				Description: "Часткове оновлення: пропущене поле лишається як було. perms — {preset: read|edit|full|custom, resources: {ресурс: none|read|edit|delete}}; resources враховуються лише для custom. user_budget_usd і group_budget_usd — мапи «id акаунта» і «id ролі» → бюджет у USD на добу; замінюють мапу цілком, запис 0 або відсутній id означає «без бюджету», від’ємна сума відхиляється. Хід, який перевищив би бюджет користувача або групи, відхиляється з ai_quota (params scope: user_budget або group_budget). Ті самі мапи, budget_accounts і budget_groups приходять у GET. docs — {soul, memory, expertise}: markdown-тексти документів помічника, які входять у контекст кожного ходу, кожен до 8192 байт (довше — ai_bad_request, field docs.<ім’я>, reason too_long); пропущений текст лишається як був, а документ, який жодного разу не зберігали, читається як вбудований текст."},
			Body: objectBody("Settings fields: enabled, provider, model, allowed_models, limits, keys, clear_keys, prices, perms, user_budget_usd, group_budget_usd, docs.")},
		"POST /api/ai/test": {ID: "test_ai_provider", Summary: "Check a provider key",
			Description: "Asks the provider for its model list and refreshes the cached catalog. An empty key checks the stored one.",
			RU: docText{Summary: "Проверить ключ провайдера",
				Description: "Запрашивает у провайдера список моделей и обновляет кэш каталога. Пустой ключ проверяет сохранённый."},
			UK: docText{Summary: "Перевірити ключ провайдера",
				Description: "Запитує в провайдера список моделей і оновлює кеш каталогу. Порожній ключ перевіряє збережений."},
			Body: objectBody("provider and key.")},
		"GET /api/ai/models": {ID: "list_ai_models", Summary: "List the models of an AI provider",
			Description: "The model catalog of the provider: id, name, context window, prices per million tokens and tool support. It is cached for an hour; refresh=1 asks the provider again.",
			RU: docText{Summary: "Список моделей ИИ-провайдера",
				Description: "Каталог моделей провайдера: id, название, контекстное окно, цены за миллион токенов и поддержка инструментов. Кэшируется на час; refresh=1 снова запрашивает провайдера."},
			UK: docText{Summary: "Список моделей ШІ-провайдера",
				Description: "Каталог моделей провайдера: id, назва, контекстне вікно, ціни за мільйон токенів і підтримка інструментів. Кешується на годину; refresh=1 знову запитує провайдера."},
			Query: []queryParam{
				strQ("provider", "anthropic, openai, openrouter or gemini; empty means the stored provider.").
					loc("anthropic, openai, openrouter или gemini; пусто — сохранённый провайдер.", "anthropic, openai, openrouter або gemini; порожньо — збережений провайдер."),
				strQ("refresh", "1 to ask the provider now.").
					loc("1 — запросить провайдера сейчас.", "1 — запитати провайдера зараз."),
			}},
		"GET /api/ai/status": {ID: "get_ai_status", Summary: "Tell whether the AI helper is available to the session",
			Description: "Returns allowed, enabled, the models and today's usage. Answers allowed=false to a session outside the beta.",
			RU: docText{Summary: "Сообщить, доступен ли ИИ-помощник сессии",
				Description: "Возвращает allowed, enabled, модели и расход за сегодня. Сессии вне беты отвечает allowed=false."},
			UK: docText{Summary: "Повідомити, чи доступний ШІ-помічник сесії",
				Description: "Повертає allowed, enabled, моделі та витрату за сьогодні. Сесії поза бетою відповідає allowed=false."}},
		"GET /api/ai/chats": {ID: "list_ai_chats", Summary: "List the caller's AI chats",
			Description: "Newest first. q keeps the chats whose title holds the text.",
			RU: docText{Summary: "Список ИИ-чатов вызывающего",
				Description: "Сначала новые. q оставляет чаты, в названии которых есть этот текст."},
			UK: docText{Summary: "Список ШІ-чатів викликача",
				Description: "Спочатку нові. q залишає чати, у назві яких є цей текст."},
			Query: []queryParam{strQ("q", "Part of a chat title, any case.").
				loc("Часть названия чата, регистр не важен.", "Частина назви чату, регістр неважливий.")}},
		"POST /api/ai/chats": {ID: "create_ai_chat", Summary: "Start an AI chat", Body: chatBody,
			Description: "The oldest chat is dropped when the caller holds 200.",
			RU: docText{Summary: "Начать ИИ-чат",
				Description: "Самый старый чат удаляется, если у вызывающего их уже 200."},
			UK: docText{Summary: "Розпочати ШІ-чат",
				Description: "Найстаріший чат видаляється, якщо в викликача їх уже 200."}},
		"GET /api/ai/chats/{id}": {ID: "get_ai_chat", Summary: "Read an AI chat",
			Description: "The messages and the changes the chat produced.",
			RU: docText{Summary: "Прочитать ИИ-чат",
				Description: "Сообщения и правки, которые создал чат."},
			UK: docText{Summary: "Прочитати ШІ-чат",
				Description: "Повідомлення та правки, які створив чат."}},
		"DELETE /api/ai/chats/{id}": {ID: "delete_ai_chat", Summary: "Delete an AI chat",
			Description: "The changes the chat produced stay and can still be reverted.",
			RU: docText{Summary: "Удалить ИИ-чат",
				Description: "Правки, созданные чатом, остаются, и их по-прежнему можно откатить."},
			UK: docText{Summary: "Видалити ШІ-чат",
				Description: "Правки, створені чатом, лишаються, і їх досі можна відкотити."}},
		"POST /api/ai/chats/{id}/messages": {ID: "send_ai_message", Summary: "Send a message and stream the reply",
			Description: "Needs Accept: text/event-stream. The answer is a server-sent event stream: status, tool, text, proposal, usage, then done or error. Nothing is applied by the reply; edits come as proposal cards.",
			RU: docText{Summary: "Отправить сообщение и получить ответ потоком",
				Description: "Нужен заголовок Accept: text/event-stream. Ответ — поток server-sent events: status, tool, text, proposal, usage, затем done или error. Ответ ничего не применяет; правки приходят карточками-предложениями."},
			UK: docText{Summary: "Надіслати повідомлення й отримати відповідь потоком",
				Description: "Потрібен заголовок Accept: text/event-stream. Відповідь — потік server-sent events: status, tool, text, proposal, usage, потім done або error. Відповідь нічого не застосовує; правки приходять картками-пропозиціями."},
			Body: messageBody},
		"GET /api/ai/changes": {ID: "list_ai_changes", Summary: "List proposed and applied AI changes",
			RU: docText{Summary: "Список предложенных и применённых правок ИИ"},
			UK: docText{Summary: "Список запропонованих і застосованих правок ШІ"},
			Query: []queryParam{
				strQ("chat_id", "Only the changes of this chat.").loc("Только правки этого чата.", "Лише правки цього чату."),
				strQ("status", "Only the changes in this state.").loc("Только правки в этом состоянии.", "Лише правки в цьому стані."),
			}},
		"POST /api/ai/changes/{id}/apply": {ID: "apply_ai_change", Summary: "Apply a proposed change",
			Description: "The body may carry confirm_delete and confirm_host when the card asked for them.",
			RU: docText{Summary: "Применить предложенную правку",
				Description: "В теле могут быть confirm_delete и confirm_host, если карточка их запросила."},
			UK: docText{Summary: "Застосувати запропоновану правку",
				Description: "У тілі можуть бути confirm_delete і confirm_host, якщо картка їх запитала."}},
		"POST /api/ai/changes/{id}/reject": {ID: "reject_ai_change", Summary: "Reject a proposed change",
			RU: docText{Summary: "Отклонить предложенную правку"},
			UK: docText{Summary: "Відхилити запропоновану правку"}},
		"POST /api/ai/changes/{id}/revert": {ID: "revert_ai_change", Summary: "Revert an applied change",
			Description: "Answers 409 when the record changed after the change was applied.",
			RU: docText{Summary: "Откатить применённую правку",
				Description: "Отвечает 409, если запись изменилась после применения правки."},
			UK: docText{Summary: "Відкотити застосовану правку",
				Description: "Відповідає 409, якщо запис змінився після застосування правки."}},
		"POST /api/ai/messages/{message_id}/revert": {ID: "revert_ai_message", Summary: "Revert every change of one reply",
			Description: "Reverts the applied changes of the reply, newest first, and reports each.",
			RU: docText{Summary: "Откатить все правки одного ответа",
				Description: "Откатывает применённые правки ответа, начиная с новых, и сообщает по каждой."},
			UK: docText{Summary: "Відкотити всі правки однієї відповіді",
				Description: "Відкочує застосовані правки відповіді, починаючи з нових, і повідомляє про кожну."},
			Body: revertBody},
		"POST /api/ai/chats/{id}/revert": {ID: "revert_ai_chat", Summary: "Revert every change of one chat",
			Description: "Reverts the applied changes of the chat, newest first, skipping the ones already reverted. Stops at the first change that cannot be reverted and reports what was reverted, where it stopped and why, and what was not tried.",
			RU: docText{Summary: "Откатить все правки одного чата",
				Description: "Откатывает применённые правки чата, начиная с новых, и пропускает уже откаченные. Останавливается на первой правке, которую откатить нельзя, и сообщает, что откачено, где и почему остановилось и что не пробовали."},
			UK: docText{Summary: "Відкотити всі правки одного чату",
				Description: "Відкочує застосовані правки чату, починаючи з нових, і пропускає вже відкочені. Зупиняється на першій правці, яку не можна відкотити, і повідомляє, що відкочено, де й чому зупинилося і що не пробували."}},
	}
}
