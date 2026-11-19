//go:build uitest

// Texts of failed API requests in toasts, run under node with the real dictionaries.
// Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestErrTextLogic
package admin

import (
	"strings"
	"testing"
)

// spaStatement returns one top-level statement of the SPA that starts with start, up to the next doc comment,
// const or function. t is the test handle and html the page; a missing statement fails the test.
func spaStatement(t *testing.T, html, start string) string {
	t.Helper()
	i := strings.Index(html, start)
	if i < 0 {
		t.Fatalf("missing %s", start)
	}
	rest := html[i:]
	end := len(rest)
	for _, m := range []string{"\n/**", "\nconst ", "\nfunction ", "\nasync function "} {
		if j := strings.Index(rest[1:], m); j >= 0 && j+1 < end {
			end = j + 1
		}
	}
	return rest[:end] + "\n"
}

// spaLangDicts declares name as the en, ru and uk objects of the SPA constant of the same name.
// html is the page. It returns the JavaScript source.
func spaLangDicts(html, name string) string {
	var b strings.Builder
	b.WriteString("const " + name + "={")
	for _, lang := range []string{"en", "ru", "uk"} {
		b.WriteString(lang + ":{" + spaConstLangBody(html, name, lang) + "},\n")
	}
	b.WriteString("};\n")
	return b.String()
}

// TestErrTextLogic checks that a refusal for asking too often names what and when, that the known codes and texts of the
// server are translated, that a raw code or an untranslated server text never reaches a toast alone, and that every key
// of the mechanism has a text in each language.
func TestErrTextLogic(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "toast"), "errParts(msg)", "a toast takes its text from errParts")
	must(t, spaFunctionLine(html, "call"), "throw apiError(msg,r.status,j,path,r.headers.get('Retry-After'))", "call keeps the path and Retry-After")
	prelude := nodeEq + `
let LANG='ru';
const uiLang=()=>LANG;
const NC_CONTACT_FIELDS=[];
const hideLabel=x=>x;
function aiErrKey(code){ return code==='ai_quota'?'ai.chat.err.ai_quota':''; }
function aiErrOf(){ return 'AI'; }
` + spaLangDicts(html, "PHRASE_EXTRA") + spaLangDicts(html, "I18N") + spaLangDicts(html, "PHRASE") +
		"const NC_ERR_KEYS={" + dictBetween(t, html, "const NC_ERR_KEYS={", "\n};") + "\n};\n" +
		"const ERR_KEYS={" + dictBetween(t, html, "const ERR_KEYS={", "\n};") + "\n};\n" +
		spaStatement(t, html, "const ERR_CODE_KEYS=") + spaStatement(t, html, "const ERR_STATUS_CODES=") +
		spaStatement(t, html, "const ERR_RATE_MSGS=") + spaStatement(t, html, "const ERR_RATE_SCOPES=")
	names := []string{"tr", "trFill", "durationText", "apiError", "errInfo", "errWait", "errRateText", "errParts", "errText", "updErrText"}
	runSPANode(t, prelude, names, `
const keys = new Set(['err.rate','err.rate.wait','err.rate.server','err.code.ref','err.code.unknown',
  ...Object.values(ERR_CODE_KEYS), ...ERR_RATE_SCOPES.map(s=>s[1])]);
for (const lang of ['en','ru','uk']) for (const k of keys) eq(!!I18N[lang][k], true, lang + ' text of ' + k);

const upd = apiError('rate_limited', 429, {error:'rate_limited', code:'too_many_requests', retry_after:240}, '/api/update/check', '240');
eq(errParts(upd), {text:'Проверка обновлений: слишком много запросов. Повторить можно через 4 мин.', code:''}, 'update check with retry_after');
eq(updErrText(upd), 'Проверка обновлений: слишком много запросов. Повторить можно через 4 мин.', 'update button text');
eq(errText(apiError('too many attempts', 429, null, '/api/login', '60')), 'Вход: слишком много запросов. Повторить можно через 1 мин.', 'login wait from Retry-After');
eq(errText(apiError('busy', 429, {error:'busy', code:'too_many_requests'}, '/api/keywords/trend', null)),
  'Отчёты: слишком много запросов. Подождите немного и повторите.', 'no wait from the server');
eq(errText('rate_limited'), 'Сервер: слишком много запросов. Подождите немного и повторите.', 'a bare rate_limited string');
eq(errText(apiError('ai_quota: daily limit is reached', 429, {code:'ai_quota'}, '/api/assistant/chat', null)), 'AI', 'assistant codes keep their table');

eq(errParts(apiError('forbidden', 403, {error:'forbidden', code:'forbidden'}, '/api/users', null)), {text:'Недостаточно прав', code:''}, 'forbidden');
eq(errText(apiError('bad json', 400, {error:'bad json', code:'bad_request'}, '/api/groups', null)), 'Некорректный запрос', 'bad json');
eq(errText(apiError('not found', 404, {error:'not found', code:'not_found'}, '/api/offers/9', null)), 'Не найдено', 'not found');
eq(errParts(apiError('users error', 500, {error:'users error', code:'internal'}, '/api/users', null)),
  {text:'Сервер не смог выполнить действие. Повторите позже.', code:'users error'}, 'an untranslated 500 keeps its text as the code');
eq(errText(apiError('users error', 500, {error:'users error', code:'internal'}, '/api/users', null)),
  'Сервер не смог выполнить действие. Повторите позже. (users error)', 'errText brackets the code');
eq(errParts(apiError('state moved', 409, {error:'state moved', code:'conflict'}, '/api/x', null)).text,
  'Сейчас это действие недоступно: данные изменились. Обновите страницу и повторите.', 'an untranslated conflict');
eq(errParts('inbox_gone_away'), {text:'Не удалось выполнить действие. Если повторится, передайте код ниже в поддержку.', code:'inbox_gone_away'}, 'an unknown raw code');
eq(errParts('HTTP 502').text, 'Внешний сервис ответил ошибкой. Повторите позже.', 'a body without JSON');
eq(errParts('HTTP 413'), {text:'Слишком большой объём данных.', code:'HTTP 413'}, 'a proxy refusal of a large body');
eq(errParts(apiError('body too large', 413, {error:'body too large', code:'too_large'}, '/api/groups/a', null)).text,
  'Слишком большой объём данных.', 'a server refusal of a large body');
eq(errParts(apiError('campaign id is taken: x1', 409, {error:'campaign id is taken: x1', code:'conflict'}, '/api/groups/a', null)),
  {text:'ID «x1» уже занят другой кампанией или алиасом. Выберите другой.', code:''}, 'a taken campaign id');
eq(errParts(apiError('очередь заполнена', 429, null, '/api/checker/apps/1/link', null)), {text:'очередь заполнена', code:''}, 'a Russian refusal of the checker queue stays');
eq(errParts(apiError('Импорт уже идёт', 409, {code:'conflict'}, '/api/x', null)), {text:'Импорт уже идёт', code:''}, 'a server text already in Russian');
eq(errParts('Сохранено'), {text:'Сохранено', code:''}, 'a translated text stays');
eq(errParts({message:'Saved'}), {text:'Saved', code:''}, 'a translated English text without a status stays');

const codes = [...ERR_RATE_MSGS, ...Object.keys(ERR_CODE_KEYS), 'rate_limited', 'too_many_requests'];
for (const lang of ['en','ru','uk']) {
  LANG = lang;
  for (const c of codes) {
    for (const st of [400, 403, 404, 409, 429, 500]) {
      const p = errParts(apiError(c, st, {error:c, code:c}, '/api/x', null));
      if (p.text === c || p.text.includes('_')) { failed++; console.log('FAIL raw code ' + c + ' status ' + st + ' in ' + lang + ': ' + p.text); }
    }
  }
}
if (failed) process.exit(1);
`)
}
