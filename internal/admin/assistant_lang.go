package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// aiLangDefault is the language of a turn whose request names none the assistant knows.
const aiLangDefault = "en"

// aiLangPrompt maps a supported language to the instruction of the system prompt that sets the
// language of the reply. The request never supplies the instruction itself, only the key.
var aiLangPrompt = map[string]string{
	"en": "Answer in English. Do not translate names of records, fields, ids or values taken from the data.",
	"ru": "Отвечай на русском языке. Названия записей, полей, идентификаторы и значения из данных не переводи.",
	"uk": "Відповідай українською мовою. Назви записів, полів, ідентифікатори та значення з даних не перекладай.",
}

// aiLangOf returns the language key of a request: raw after lower-casing and trimming when it is
// exactly one of the supported languages, else aiLangDefault.
func aiLangOf(raw string) string {
	lang := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := aiLangPrompt[lang]; ok {
		return lang
	}
	return aiLangDefault
}

// aiSystemFor returns the system prompt of a turn in lang without the administrator documents:
// the fixed prompt, then the language instruction of the table.
func aiSystemFor(lang string) string {
	return aiSystemWith(lang, "")
}

// aiSystemWith returns the system prompt of a turn in lang: the fixed prompt, the persona block
// (the administrator documents, see aiPersonaBlock; none when persona is empty), then the language
// instruction of the table, which stays the last. lang must be a key of aiLangPrompt (aiLangOf
// makes sure of it).
func aiSystemWith(lang, persona string) string {
	prompt := aiSystemPrompt
	if persona != "" {
		prompt += "\n\n" + persona
	}
	return prompt + "\n\nLanguage: " + aiLangPrompt[aiLangOf(lang)]
}

// aiLangTexts holds the texts of the errors a turn reports to the user, by code and language.
// English is not here: the English text is the one the error carries. The panel builds its own
// text from the code and the params, so these are the fallback of a plain API client.
var aiLangTexts = map[string]map[string]string{
	"ru": {
		aiErrDisabled:      "Ассистент выключен.",
		aiErrBadKey:        "Для провайдера ИИ не задан рабочий ключ.",
		aiErrRateLimited:   "Достигнут лимит запросов провайдера ИИ.",
		aiErrProvider:      "Провайдер ИИ вернул ошибку.",
		aiErrTimeout:       "Ответ занял слишком много времени.",
		aiErrBusy:          "Ассистент сейчас занят, повторите чуть позже.",
		aiErrQuota:         "Достигнут лимит расхода ассистента.",
		aiErrBadModel:      "Эта модель не разрешена.",
		aiErrBadRequest:    "Запрос содержит неверное значение.",
		aiErrStepsLimit:    "Достигнут лимит шагов ответа.",
		aiErrInputTooLarge: "Сообщение и список инструментов не помещаются в лимит входных токенов.",
		aiErrChatFull:      "Чат заполнен, начните новый.",
		aiErrDiskFull:      "На сервере нет места для истории чата.",
		aiErrForbidden:     "Права ассистента или ваш доступ не разрешают это действие.",
		"unauthorized":     "Сессия больше не действует.",
		"internal":         "Ассистент не справился.",
	},
	"uk": {
		aiErrDisabled:      "Асистент вимкнено.",
		aiErrBadKey:        "Для провайдера ШІ не задано робочий ключ.",
		aiErrRateLimited:   "Досягнуто ліміт запитів провайдера ШІ.",
		aiErrProvider:      "Провайдер ШІ повернув помилку.",
		aiErrTimeout:       "Відповідь зайняла надто багато часу.",
		aiErrBusy:          "Асистент зараз зайнятий, повторіть трохи згодом.",
		aiErrQuota:         "Досягнуто ліміт витрат асистента.",
		aiErrBadModel:      "Цю модель не дозволено.",
		aiErrBadRequest:    "Запит містить неправильне значення.",
		aiErrStepsLimit:    "Досягнуто ліміт кроків відповіді.",
		aiErrInputTooLarge: "Повідомлення і список інструментів не вміщуються в ліміт вхідних токенів.",
		aiErrChatFull:      "Чат заповнено, почніть новий.",
		aiErrDiskFull:      "На сервері немає місця для історії чату.",
		aiErrForbidden:     "Права асистента або ваш доступ не дозволяють цю дію.",
		"unauthorized":     "Сесія більше не діє.",
		"internal":         "Асистент не впорався.",
	},
}

// aiStepTexts holds the "step n of max" line of the status event by language.
var aiStepTexts = map[string]string{
	"en": "Step %d of %d",
	"ru": "Шаг %d из %d",
	"uk": "Крок %d з %d",
}

// aiCompactTexts holds the status line shown while the assistant compacts its own context.
var aiCompactTexts = map[string]string{
	"en": "Compacting the conversation to keep going",
	"ru": "Сжимаю контекст, чтобы продолжить",
	"uk": "Стискаю контекст, щоб продовжити",
}

// aiCompactText returns the status line of a context compaction in lang.
func aiCompactText(lang string) string {
	if text, ok := aiCompactTexts[lang]; ok {
		return text
	}
	return aiCompactTexts[aiLangDefault]
}

// aiStepText returns the status line of step out of max in lang.
func aiStepText(lang string, step, max int) string {
	format, ok := aiStepTexts[lang]
	if !ok {
		format = aiStepTexts[aiLangDefault]
	}
	return fmt.Sprintf(format, step, max)
}

// aiErrText returns the text of an error with code in lang. English keeps the text the error
// carries (orig). Another language gets the table text; an error of the provider keeps the
// provider's reason after it. A code with no table text keeps orig.
func aiErrText(lang, code, orig string) string {
	text, ok := aiLangTexts[lang][code]
	if !ok {
		return orig
	}
	if code == aiErrProvider && orig != "" {
		return text + " (" + orig + ")"
	}
	return text
}

// aiErrParams returns the params an error carries for the panel, nil for none.
func aiErrParams(ae *aiError) map[string]any {
	if ae == nil || len(ae.Params) == 0 {
		return nil
	}
	return ae.Params
}

// aiWriteErrIn writes the JSON answer of a refusal before the event stream starts: the status
// of the error, its code, its text in lang and its params. Any error that is not an *aiError is
// answered as the plain API does (aiFail), and a failure of an unknown kind keeps its fixed text.
func (s *Server) aiWriteErrIn(w http.ResponseWriter, err error, lang string) {
	status, msg := s.aiFail(err)
	code := errCode(status, msg)
	out := map[string]any{"code": code, "error": aiErrText(lang, code, msg)}
	var ae *aiError
	if errors.As(err, &ae) {
		if p := aiErrParams(ae); p != nil {
			out["params"] = p
		}
	}
	writeJSON(w, status, out)
}
