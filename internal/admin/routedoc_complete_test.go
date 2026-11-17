package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
)

// cyrillic matches one Cyrillic letter; a Russian or Ukrainian text must hold at least one.
var cyrillic = regexp.MustCompile(`\p{Cyrillic}`)

// registeredPatterns returns every pattern s mounts: the guarded routes, then the public ones.
func registeredPatterns(s *Server) []string {
	return append(slices.Clone(s.routes), s.public...)
}

// checkLocalized fails when one of the en, ru and uk texts is empty, or when the
// ru or uk text has no Cyrillic letter. what names the text in the message.
func checkLocalized(t *testing.T, what, en, ru, uk string) {
	t.Helper()
	if strings.TrimSpace(en) == "" {
		t.Errorf("%s: no English text", what)
	}
	for lang, text := range map[string]string{"ru": ru, "uk": uk} {
		if !cyrillic.MatchString(text) {
			t.Errorf("%s: no %s text (%q)", what, lang, text)
		}
	}
}

// TestRouteDocCoversEveryRoute requires every mounted route to be documented in
// routeDocs under one docs section, with a summary in English, Russian and
// Ukrainian, and nothing in routeDocs that the mux does not mount. A route an API
// key may call also needs a description, translated query parameters, a body
// example when it takes a body, a response sample unless it answers 204, and only
// error statuses with a known meaning.
func TestRouteDocCoversEveryRoute(t *testing.T) {
	s := accessTestServer(t)
	registered := map[string]bool{}
	for _, p := range registeredPatterns(s) {
		registered[p] = true
		doc, ok := routeDocs[p]
		if !ok {
			t.Errorf("%s: not in routeDocs", p)
			continue
		}
		_, muxPath, _ := strings.Cut(p, " ")
		if routeTag(muxPath) == "" {
			t.Errorf("%s: no docs section holds the path", p)
		}
		checkLocalized(t, p+" summary", doc.Summary, doc.RU.Summary, doc.UK.Summary)
		if ruleOf(p).cookie {
			continue
		}
		checkLocalized(t, p+" description", doc.Description, doc.RU.Description, doc.UK.Description)
		for _, q := range doc.Query {
			checkLocalized(t, p+" query "+q.Name, q.Description, q.RU, q.UK)
		}
		if (doc.Body != nil || doc.RawBody) && doc.Example == nil {
			t.Errorf("%s: takes a body but has no Example", p)
		}
		if doc.Response == nil && doc.successStatus() != http.StatusNoContent {
			t.Errorf("%s: no Response sample", p)
		}
		for _, code := range doc.Errors {
			if errorDescriptions[code] == "" {
				t.Errorf("%s: error %d has no entry in errorDescriptions", p, code)
			}
		}
	}
	for p := range routeDocs {
		if !registered[p] {
			t.Errorf("routeDocs lists %s, which the mux does not mount", p)
		}
	}
	for p := range publicRoutes {
		if !registered[p] {
			t.Errorf("publicRoutes lists %s, which Handler does not mount", p)
		}
	}
}

// TestRouteDocPathParamsAreDescribed requires every path parameter name of a mounted
// route to have a description in the three languages.
func TestRouteDocPathParamsAreDescribed(t *testing.T) {
	s := accessTestServer(t)
	for _, p := range registeredPatterns(s) {
		for _, m := range pathParamPattern.FindAllStringSubmatch(p, -1) {
			d := pathParamDescriptions[m[1]]
			checkLocalized(t, p+" path "+m[1], d.Description, d.RU, d.UK)
		}
	}
}

// TestRouteDocTagsAreDistinct requires unique section names and prefixes, and a
// name in the three languages for every section.
func TestRouteDocTagsAreDistinct(t *testing.T) {
	names, prefixes := map[string]bool{}, map[string]string{}
	for _, tag := range docTags {
		if names[tag.Name] {
			t.Errorf("section %s listed twice", tag.Name)
		}
		names[tag.Name] = true
		checkLocalized(t, "section "+tag.Name, tag.EN, tag.RU, tag.UK)
		for _, p := range tag.Prefixes {
			if prev, dup := prefixes[p]; dup {
				t.Errorf("prefix %s in %s and %s", p, prev, tag.Name)
			}
			prefixes[p] = tag.Name
		}
	}
	if got := routeTag("/api/keywords/stats"); got != "keywords" {
		t.Errorf("routeTag(/api/keywords/stats) = %q", got)
	}
	if got := routeTag("/api/keys/download"); got != "keywords" {
		t.Errorf("routeTag(/api/keys/download) = %q", got)
	}
	if got := routeTag("/api/nowhere"); got != "" {
		t.Errorf("routeTag(/api/nowhere) = %q, want empty", got)
	}
}

// TestOpenAPIKeyDocsCarryTheRegistry checks the document an ADMIN key with every
// action receives: each operation has a section tag, Russian and Ukrainian texts,
// a success response and the public flag of its route, and the sign-in routes
// stay out of it.
func TestOpenAPIKeyDocsCarryTheRegistry(t *testing.T) {
	s := accessTestServer(t)
	sess := security.Session{API: true, KeyType: security.KeyTypeAdmin, KeyActions: security.AllActions()}
	doc, ops := openAPIOperations(t, s, sess)
	tags, _ := doc["tags"].([]any)
	if len(tags) != len(docTags) {
		t.Errorf("tags %d, want %d", len(tags), len(docTags))
	}
	for key, op := range ops {
		if tl, _ := op["tags"].([]any); len(tl) != 1 || tl[0] == "" {
			t.Errorf("%s: tags = %v", key, op["tags"])
		}
		i18n, _ := op[i18nExtension].(map[string]any)
		for _, lang := range []string{"ru", "uk"} {
			text, _ := i18n[lang].(map[string]any)
			if v, _ := text["summary"].(string); v == "" {
				t.Errorf("%s: no %s summary", key, lang)
			}
			if v, _ := text["description"].(string); v == "" {
				t.Errorf("%s: no %s description", key, lang)
			}
		}
		if len(op["responses"].(map[string]any)) < 2 {
			t.Errorf("%s: no success response", key)
		}
	}
	for p, session := range publicRoutes {
		op, ok := ops[convertedKey(p)]
		if session && ok {
			t.Errorf("%s: a sign-in route is in the key document", p)
		}
		if !session && (!ok || op[publicExtension] != true || len(op["security"].([]any)) != 0) {
			t.Errorf("%s: public route missing or not marked public: %v", p, op)
		}
	}
}

// wikiRoutesBegin and wikiRoutesEnd frame the route list of the admin API wiki page.
const (
	wikiRoutesBegin = "<!-- api-routes:begin -->"
	wikiRoutesEnd   = "<!-- api-routes:end -->"
)

// wikiRoutesMarkdown renders the route list of the admin API wiki page from routeDocs:
// one table per docs section with the routes an API key may call, then the list of
// the routes it may not. patterns are the mounted routes in mount order.
func wikiRoutesMarkdown(patterns []string) string {
	bySection := map[string][]string{}
	var denied []string
	for _, p := range patterns {
		if ruleOf(p).cookie {
			denied = append(denied, p)
			continue
		}
		_, muxPath, _ := strings.Cut(p, " ")
		tag := routeTag(muxPath)
		bySection[tag] = append(bySection[tag], p)
	}
	cell := func(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
	var b strings.Builder
	for _, tag := range docTags {
		list := bySection[tag.Name]
		if len(list) == 0 {
			continue
		}
		slices.Sort(list)
		b.WriteString("\n### " + tag.RU + "\n\n| Маршрут | Что делает | Галочка ключа |\n| --- | --- | --- |\n")
		for _, p := range list {
			action := ruleOf(p).action
			if action != "" {
				action = "`" + action + "`"
			}
			b.WriteString("| `" + convertedKey(p) + "` | " + cell(routeDocs[p].RU.Summary) + " | " + action + " |\n")
		}
	}
	slices.Sort(denied)
	b.WriteString("\n### Ключу недоступно\n\nЭти маршруты работают только в сессии браузера; ключ получает `403`.\n\n")
	for _, p := range denied {
		b.WriteString("- `" + convertedKey(p) + "` — " + routeDocs[p].RU.Summary + "\n")
	}
	return b.String()
}

// TestAdminAPIWikiMatchesTheRegistry requires the route list of docs/wiki/admin-api.md,
// between wikiRoutesBegin and wikiRoutesEnd, to equal wikiRoutesMarkdown. On a mismatch
// the message holds the expected block to paste.
func TestAdminAPIWikiMatchesTheRegistry(t *testing.T) {
	s := accessTestServer(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "wiki", "admin-api.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := strings.ReplaceAll(string(raw), "\r\n", "\n")
	_, rest, ok := strings.Cut(page, wikiRoutesBegin+"\n")
	got, _, closed := strings.Cut(rest, wikiRoutesEnd)
	want := wikiRoutesMarkdown(registeredPatterns(s))
	if !ok || !closed || got != want {
		t.Errorf("admin-api.md route list is out of date; expected block:\n%s\n%s%s", wikiRoutesBegin, want, wikiRoutesEnd)
	}
}
