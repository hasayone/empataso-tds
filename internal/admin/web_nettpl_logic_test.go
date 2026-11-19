//go:build uitest

// Behaviour of netTplFromNetwork, run under node. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestNetTplFromNetworkLogic
package admin

import "testing"

// TestNetTplFromNetworkLogic checks the template body built from a saved network: the name and the
// offer parameters are kept, an own /postback address becomes {origin}/postback without from=, a foreign
// host or a path token gives an empty postback, and a network with no offer parameters gives nothing.
func TestNetTplFromNetworkLogic(t *testing.T) {
	runSPANode(t, nodeEq, []string{"netTplFromNetwork"}, `
const O = 'https://tds.example';
eq(netTplFromNetwork({name:' Gambeat.io ', offer_query:'sub1={subid}', postback_url:'https://other.example/fef805a/postback?subid={sub1}&status=REPLACE&from=Gambeat.io'}, O),
  {name:'Gambeat.io', offer_query:'sub1={subid}', postback_url:'', instruction:''}, 'foreign host with a path token: no postback');
eq(netTplFromNetwork({name:'A', offer_query:'q={subid}', postback_url:O+'/postback?subid={sub1}&from=A&status=REPLACE'}, O).postback_url,
  '{origin}/postback?subid={sub1}&status=REPLACE', 'own address is rewritten and from= is dropped');
eq(netTplFromNetwork({name:'A', offer_query:'q={subid}', postback_url:O+'/postback'}, O).postback_url, '{origin}/postback', 'own address without a query');
eq(netTplFromNetwork({name:'A', offer_query:'q={subid}', postback_url:O+'/abc/postback?x=1'}, O).postback_url, '', 'a path token on our host is dropped');
eq(netTplFromNetwork({name:'A', offer_query:'q={subid}', postback_url:O+'/postback?next=https://x.example'}, O).postback_url, '', 'an address inside the query is dropped');
eq(netTplFromNetwork({name:'A', offer_query:'  ', postback_url:''}, O), null, 'no offer parameters: nothing to add');
eq(netTplFromNetwork({name:'', offer_query:'a=b'}, O), null, 'no name: nothing to add');
if (failed) process.exit(1);
`)
}

// TestNetInstructionKeepsWords checks that rendering an instruction changes no word: the text of the
// result, with the tags cut and the spaces folded, equals the text of the source, and a tag outside
// p, br, code and a stays as visible text.
func TestNetInstructionKeepsWords(t *testing.T) {
	runSPANode(t, nodeEq+`
const esc=(s)=>String(s==null?'':s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const plainText=h=>h.replace(/<[^>]*>/g,' ').replace(/&lt;/g,'<').replace(/&gt;/g,'>').replace(/&quot;/g,'"').replace(/&#39;/g,"'").replace(/&amp;/g,'&').replace(/\s+/g,' ').trim();
`, []string{"netInstructionHTML"}, `
const src = '<p>В личном кабинете Gambeat.io перейдите во вкладку "Tools" - Global Postback\n            <p>Добавьте Postback URL, предварительно заменив <code>status=[REPLACE]</code> на: \n            <p><code>status=lead</code> - для регистрации\n            <p><code>status=sale</code> - для депозита </p>';
const html = netInstructionHTML(src);
eq(plainText(html), plainText(src), 'words are the same after rendering');
eq((html.match(/<p>/g)||[]).length, (html.match(/<\/p>/g)||[]).length, 'every paragraph is closed');
eq((html.match(/<code>/g)||[]).length, (html.match(/<\/code>/g)||[]).length, 'every code is closed');
const link = netInstructionHTML('<p>See <a href="https://example.com/a?x=1&y=2" target="_blank">the page</a></p>');
eq(link.includes('<a href="https://example.com/a?x=1&amp;y=2" target="_blank" rel="noopener noreferrer">the page</a>'), true, 'a link keeps its text and gets rel');
const bad = netInstructionHTML('<p><b>x</b> <a href="javascript:alert(1)">y</a> <script>z</script></p>');
eq(/<(b|script)/i.test(bad) || bad.includes('href="javascript'), false, 'other tags and addresses stay inert text');
if (failed) process.exit(1);
`)
}