package admin

import (
	"strings"
	"testing"
)

// TestAssistantEntityLinks checks aiRichParts and aiRichFill under node with a small DOM stand-in: the token
// [[type:id|Name]] becomes the link of the menu page of the entity, and everything else (markup, other types,
// addresses, broken tokens) stays text that serialises escaped.
func TestAssistantEntityLinks(t *testing.T) {
	prelude := `
const esc=(s)=>String(s==null?'':s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let ME=null;
function pageAllowed(me,page){return !me||!me.deny||!me.deny.includes(page)}
const document={createTextNode:t=>({text:t})};
function aiEl(tag,cls,text){return {tag,cls,text,attrs:{},setAttribute(k,v){this.attrs[k]=v}}}
function R(text){
  const el={children:[]};
  Object.defineProperty(el,'textContent',{set(){el.children=[]},get(){return ''}});
  el.appendChild=c=>el.children.push(c);
  aiRichFill(el,text);
  return el.children.map(c=>c.tag?'<a class="'+c.cls+'" href="'+esc(c.attrs.href)+'">'+esc(c.text)+'</a>':esc(c.text)).join('');
}
`
	checks := `
const A=(href,name)=>'<a class="ai-ent" href="'+href+'">'+name+'</a>';
eq(R('Кампания [[campaign:spring-1|Весна]] жива'), 'Кампания '+A('#/flows/spring-1','Весна')+' жива', 'campaign link is #/flows/{id}');
eq(R('[[offer:42|Оффер А]]'), A('#/offers/42','Оффер А'), 'offer link is #/offers/{id}');
eq(R('[[landing:l1|L]] [[network:n_2|N]] [[source:s.3|S]]'), A('#/landings/l1','L')+' '+A('#/networks/n_2','N')+' '+A('#/sources/s.3','S'), 'landing, network and source');
eq(R('первая\n\nвторая'), 'первая\n\nвторая', 'line breaks stay');
eq(R('<img src=x onerror=alert(1)> & "q"'), '&lt;img src=x onerror=alert(1)&gt; &amp; &quot;q&quot;', 'plain text is escaped');
eq(R('[[offer:1|<b>x</b> "y"]]'), A('#/offers/1','&lt;b&gt;x&lt;/b&gt; &quot;y&quot;'), 'the name is escaped');
eq(R('[[offer:a b|X]]'), '[[offer:a b|X]]', 'an id with a space is not a token');
eq(R('[[offer:1"onclick=x|X]]'), '[[offer:1&quot;onclick=x|X]]', 'an id with a quote is not a token');
eq(R('[[offer:../x|X]]'), '[[offer:../x|X]]', 'a slash in an id is not a token');
eq(R('[[offer:a.b-c_d~e|X]]'), A('#/offers/a.b-c_d~e','X'), 'dots, dashes, underscores and tildes are allowed in an id');
eq(R('[[domain:example.com|example.com]]'), '[[domain:example.com|example.com]]', 'a domain has no page: text');
eq(R('[[report:1|R]]'), '[[report:1|R]]', 'another type stays text');
eq(R('[[url:javascript:alert(1)|клик]]'), '[[url:javascript:alert(1)|клик]]', 'javascript: is text');
eq(R('[[offer:javascript:alert(1)|клик]]'), '[[offer:javascript:alert(1)|клик]]', 'a colon in an id is not a token');
eq(R('[x](https://evil.example) https://evil.example'), '[x](https://evil.example) https://evil.example', 'markdown and addresses stay text');
eq(R('[[campaign:a|нет конца'), '[[campaign:a|нет конца', 'unclosed token');
eq(R('[[campaign:|X]] [[campaign:a|]] [[campaign:a]]'), '[[campaign:|X]] [[campaign:a|]] [[campaign:a]]', 'empty id, empty name, no name');
eq(R('[[offer:1|   ]]'), '[[offer:1|   ]]', 'a blank name is text');
eq(R('[[offer:1|a [[offer:2|B]]'), '[[offer:1|a '+A('#/offers/2','B'), 'a nested start leaves the first one text');
eq(R('[[offer:1|a\nb]]'), '[[offer:1|a\nb]]', 'a line break in the name is not a token');
eq(R('[[offer:'+'x'.repeat(129)+'|X]]'), '[[offer:'+'x'.repeat(129)+'|X]]', 'an id over 128 characters is text');
eq(R('[[offer:1|'+'n'.repeat(201)+']]'), '[[offer:1|'+'n'.repeat(201)+']]', 'a name over 200 characters is text');
eq(R('[[offer:é|X]]'), '[[offer:é|X]]', 'only the id alphabet is accepted');
eq(R(null), '', 'no text');
eq(R(''), '', 'empty text');
ME={deny:['offers']};
eq(R('[[offer:1|Скрыт]] [[campaign:c|Виден]]'), 'Скрыт '+A('#/flows/c','Виден'), 'a page the session may not open shows the name as text');
`
	assistantNode(t, []string{"aiRichParts", "aiRichFill"}, prelude, checks)
}

// TestAssistantEntityLinksWiring checks the bubble: a reply of the assistant goes through aiRichHTML, the text of the user
// stays plain, the click closes the panel only on a phone and does not stop the link.
func TestAssistantEntityLinksWiring(t *testing.T) {
	html := spaSource()
	add := assistantFn(html, "aiAddMessage")
	must(t, add, "m.role==='assistant'", "only the reply of the assistant is linked")
	must(t, add, "aiRichFill(b,m.text)", "the reply is drawn by aiRichFill")
	mustNot(t, add, "innerHTML", "the reply is never parsed as markup")
	must(t, add, "else b.textContent=m.text", "the text of the user stays plain text")
	click := assistantFn(html, "aiEntityClick")
	must(t, click, "closest('a.ai-ent')", "the click reacts to an entity link only")
	must(t, click, "(max-width:640px)", "the panel closes only in the phone layout")
	if strings.Contains(click, "preventDefault") {
		t.Error("the entity click must not stop the link: the address changes as it does for a menu item")
	}
	// every page of a token is a page of the SPA router with an editor opened by its id
	rich := assistantFn(html, "aiRichParts")
	mustNot(t, assistantFn(html, "aiRichFill"), "innerHTML", "the links are built as nodes")
	for _, page := range []string{"flows", "offers", "landings", "sources", "networks"} {
		must(t, rich, "'"+page+"'", "page "+page)
		if !strings.Contains(html, "const PAGES={") || !strings.Contains(html[strings.Index(html, "const PAGES={"):], page) {
			t.Errorf("page %s is not in the router", page)
		}
	}
}

// TestAssistantPromptEntityTokens checks the system prompt teaches the token of the panel and names exactly the types
// the panel turns into links.
func TestAssistantPromptEntityTokens(t *testing.T) {
	p := aiSystemFor("ru")
	for _, want := range []string{
		"[[type:id|Name]]",
		"campaign, offer, landing, network, source",
		"Never invent an id",
		"Domains and reports have no page of their own",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the system prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "no links") {
		t.Error("the prompt still forbids every link")
	}
	rich := assistantFn(spaSource(), "aiRichParts")
	must(t, rich, "(campaign|offer|landing|source|network)", "the types of the prompt are the types of the panel")
}
