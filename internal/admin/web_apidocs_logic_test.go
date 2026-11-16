//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLogicApiDocs runs the docs page helpers under node: sections follow the document tags and
// skip empty ones, cookie-only operations go to the denied list, texts fall back to English, the
// search folds case and ё and needs every word, the method filter works, and curl samples carry
// placeholders, the key header only off public routes and a shell-safe body. It skips without node.
func TestLogicApiDocs(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	m := pureRe.FindStringSubmatch(spaSource())
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	f := filepath.Join(t.TempDir(), "apidocs.js")
	if err := os.WriteFile(f, []byte(m[1]+apiDocsChecks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("api docs checks failed:\n%s", out)
	}
}

const apiDocsChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const doc = {
  tags: [
    {name:'general', description:'General', 'x-emtds-i18n':{ru:'Общее', uk:'Загальне'}},
    {name:'campaigns', description:'Campaigns', 'x-emtds-i18n':{ru:'Кампании', uk:''}},
    {name:'empty', description:'Empty', 'x-emtds-i18n':{ru:'Пусто', uk:'Порожньо'}},
  ],
  paths: {
    '/api/groups/{id}': {
      delete: {operationId:'delete_campaign', summary:'Delete one campaign.', tags:['campaigns'], responses:{'204':{description:'Success.'}, '404':{description:'Not found.'}, default:{}}},
      put: {operationId:'update_campaign', summary:'Save one campaign.', description:'Replaces it.', tags:['campaigns'],
        'x-emtds-i18n':{ru:{summary:'Сохранить кампанию.', description:'Заменяет её.'}, uk:{summary:'', description:''}},
        parameters:[{name:'id', in:'path', required:true, description:'Record id.', 'x-emtds-i18n':{ru:'Id записи.', uk:'Id запису.'}, schema:{type:'string'}}],
        requestBody:{required:true, content:{'application/json':{schema:{type:'object'}, example:{name:"O'Brien"}}}},
        responses:{'200':{description:'Success.', content:{'application/json':{example:{ok:true}}}}, '409':{}, '400':{}, default:{}}},
    },
    '/api/groups': {
      get: {operationId:'list_campaigns', summary:'List campaigns.', tags:['campaigns'], 'x-emtds-heavy':true,
        parameters:[{name:'f', in:'query', required:false, description:'Filter.', schema:{type:'array', items:{type:'string'}}},
          {name:'dim', in:'query', required:true, description:'Dimension.', schema:{type:'string'}}],
        responses:{'200':{description:'Success.', content:{'text/csv':{example:'a,b\n1,2'}}}}},
      put: {operationId:'replace_campaigns', summary:'Replace all campaigns.', tags:['campaigns'], 'x-emtds-action':'groups_replace', 'x-emtds-admin':true, responses:{'200':{}}},
    },
    '/api/health': {get: {operationId:'health', summary:'Liveness.', tags:['general'], 'x-emtds-public':true, security:[], responses:{'200':{}}}},
    '/api/me': {get: {operationId:'get_me', summary:'Current session.', tags:['accounts'], 'x-emtds-cookie-only':true, 'x-emtds-i18n':{ru:{summary:'Текущая сессия.', description:''}}, responses:{'200':{}}}},
    '/api/odd': {post: {operationId:'odd', summary:'Odd one.', tags:['other'], responses:{'202':{}}}},
  },
};

const ru = apiDocsModel(doc, 'ru');
eq(ru.sections.map(s => [s.tag, s.label]), [['general','Общее'],['campaigns','Кампании'],['other','other']], 'sections in tag order, empty skipped, unknown tag last');
eq(ru.sections[1].ops.map(o => o.method + ' ' + o.path), ['GET /api/groups','PUT /api/groups','PUT /api/groups/{id}','DELETE /api/groups/{id}'], 'ops by path, then method order');
eq(ru.denied.map(o => [o.id, o.summary]), [['get_me','Текущая сессия.']], 'cookie-only ops are denied, with the translation');
const put = ru.sections[1].ops[2];
eq([put.summary, put.description, put.params[0].description, put.params[0].type], ['Сохранить кампанию.','Заменяет её.','Id записи.','string'], 'Russian texts');
eq([put.status, put.errors, put.bodyType, put.example, put.response], [200, [400,409], 'application/json', {name:"O'Brien"}, {ok:true}], 'status, sorted errors, body and response');
const list = ru.sections[1].ops[0];
eq([list.heavy, list.params[0].type, list.params[0].description, list.responseType, list.response], [true, 'string[]', 'Filter.', 'text/csv', 'a,b\n1,2'], 'heavy, array type, English fallback, text response');
eq([ru.sections[1].ops[1].action, ru.sections[1].ops[1].admin], ['groups_replace', true], 'action and admin flags');
eq([ru.sections[1].ops[3].status, ru.sections[1].ops[3].response], [204, undefined], 'empty 204 answer');
eq(ru.sections[2].ops[0].status, 202, 'first 2xx code is the success status');
const uk = apiDocsModel(doc, 'uk');
eq([uk.sections[1].label, uk.sections[1].ops[2].summary, uk.sections[1].ops[2].params[0].description], ['Campaigns','Save one campaign.','Id запису.'], 'empty Ukrainian falls back to English');
eq(apiDocsModel(doc, 'en').sections[0].label, 'General', 'English tag label');
eq(apiDocsModel(null, 'ru'), {sections:[], denied:[]}, 'no document, empty model');

const ids = m => m.sections.flatMap(s => s.ops.map(o => o.id)).concat(m.denied.map(o => 'x:' + o.id));
eq(ids(apiDocsFilter(ru, '', '')), ids(ru), 'no filter keeps everything');
eq(ids(apiDocsFilter(ru, 'СОХРАНИТЬ', '')), ['update_campaign'], 'case does not matter');
eq(ids(apiDocsFilter(ru, 'заменяет ее', '')), ['update_campaign'], 'ё folds to е and every word must match');
eq(ids(apiDocsFilter(ru, 'groups', 'put')), ['replace_campaigns','update_campaign'], 'path words and the method filter');
eq(ids(apiDocsFilter(ru, '/api/me', '')), ['x:get_me'], 'denied ops are searched too');
eq(apiDocsFilter(ru, 'nothing-like-this', '').sections, [], 'no match, no sections');
eq(ids(apiDocsFilter(ru, 'dimension', '')), ['list_campaigns'], 'a parameter description is searched');
eq(ids(apiDocsFilter(ru, 'dim', '')), ['list_campaigns'], 'a parameter name is searched');
eq(ids(apiDocsFilter(ru, 'пкщгзы', '')).filter(id => id !== 'x:get_me'), ['list_campaigns','replace_campaigns','update_campaign','delete_campaign'], 'the other keyboard layout matches the path');
eq(apiDocsHits(ru, '', '').map(h => h.id), ids(ru).map(id => 'op-' + (id.startsWith('x:') ? id.slice(2) : id)), 'an empty query lists every route in page order');
eq(apiDocsHits(ru, 'dimension', '').map(h => h.id), ['op-list_campaigns'], 'hits follow the search');
eq(apiDocsHits(ru, '', 'DELETE').map(h => h.method), ['DELETE'], 'the method narrows the hit list');
eq(apiDocsMark('Id <записи>', 'записи'), 'Id &lt;<mark>записи</mark>&gt;', 'matches are marked and the rest is escaped');
eq(apiDocsMark('a<b', ''), 'a&lt;b', 'an empty query only escapes');

eq(apiDocsCurl(ru.sections[0].ops[0], 'https://a.example'), 'curl "https://a.example/api/health"', 'public route has no key');
eq(apiDocsCurl(list, 'https://a.example'), 'curl -H "Api-Key: em_..." "https://a.example/api/groups?dim=DIM"', 'required query placeholder');
eq(apiDocsCurl(put, 'https://a.example'), 'curl \\\n  -X PUT \\\n  -H "Api-Key: em_..." \\\n  -H "Content-Type: application/json" \\\n  --data-binary \'{"name":"O\'\\\'\'Brien"}\' \\\n  "https://a.example/api/groups/ID"', 'body is shell quoted, path placeholder upper-case');
eq(apiDocsCurl(ru.sections[1].ops[3], 'https://a.example'), 'curl \\\n  -X DELETE \\\n  -H "Api-Key: em_..." \\\n  "https://a.example/api/groups/ID"', 'delete without body');
eq([apiDocsSample(undefined), apiDocsSample('a\nb'), apiDocsSample({a:[1]})], ['', 'a\nb', '{\n  "a": [\n    1\n  ]\n}'], 'samples');

eq([ru.sections[1].ops[3].destructive, ru.sections[1].ops[1].destructive, ru.sections[1].ops[2].destructive], [true, true, false],
  'DELETE and a destructive action are marked, a plain PUT is not');
eq(apiDocsHighlight('{"a": "<b>", "n": -1.5e3, "ok": true, "z": null}', 'json'),
  '{<span class="hl-key">&quot;a&quot;</span>: <span class="hl-str">&quot;&lt;b&gt;&quot;</span>, <span class="hl-key">&quot;n&quot;</span>: <span class="hl-num">-1.5e3</span>, '
  + '<span class="hl-key">&quot;ok&quot;</span>: <span class="hl-lit">true</span>, <span class="hl-key">&quot;z&quot;</span>: <span class="hl-lit">null</span>}',
  'json keys, strings, numbers and literals, escaped');
const sh = apiDocsHighlight('curl -H "Api-Key: em_..." "https://a.example/?a=1&b=<x>"', 'bash');
eq([sh.startsWith('<span class="hl-cmd">curl</span>'), sh.includes('<span class="hl-flag">-H</span>'), sh.includes('&amp;b=&lt;x&gt;'), sh.includes('<x>')],
  [true, true, true, false], 'curl command, flags and escaped strings');
eq(apiDocsHighlight('a<b', ''), 'a&lt;b', 'plain text is only escaped');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
