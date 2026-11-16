package admin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// postmanSecret matches a real-looking API key. The collection may only carry the placeholder.
var postmanSecret = regexp.MustCompile(`(?:em_|kz_)[0-9a-fA-F]{32,}`)

// TestPostmanCollectionMatchesCatalog requires the collection built from the full OpenAPI
// document to contain every routeDocs route and no other route, and the same set as that document.
// A narrower document produces exactly its own routes. Samples stay fictional.
func TestPostmanCollectionMatchesCatalog(t *testing.T) {
	s := accessTestServer(t)
	doc := s.openAPIDocumentOf(func(routeRule) bool { return true })
	col := postmanCollection(doc)
	got := postmanRouteKeys(col)
	want := catalogKeys()
	if !slices.Equal(got, want) {
		t.Fatalf("collection vs catalog\n missing: %s\n extra: %s", missingKeys(want, got), missingKeys(got, want))
	}
	fromDoc := openAPIRouteKeys(doc)
	if !slices.Equal(got, fromDoc) {
		t.Fatalf("collection vs openapi\n missing: %s\n extra: %s", missingKeys(fromDoc, got), missingKeys(got, fromDoc))
	}
	narrow := func(rule routeRule) bool { return rule.action == "" && !rule.cookie }
	narrowDoc := s.openAPIDocumentOf(narrow)
	narrowKeys := postmanRouteKeys(postmanCollection(narrowDoc))
	narrowWant := openAPIRouteKeys(narrowDoc)
	if !slices.Equal(narrowKeys, narrowWant) {
		t.Fatalf("narrow collection\n missing: %s\n extra: %s", missingKeys(narrowWant, narrowKeys), missingKeys(narrowKeys, narrowWant))
	}
	raw, err := json.Marshal(col)
	if err != nil {
		t.Fatal(err)
	}
	if postmanSecret.Match(raw) || privateAddr.Match(raw) || strings.Contains(string(raw), "212.2.33.246") {
		t.Fatal("the collection contains a secret or a private address")
	}
	if !strings.Contains(string(raw), postmanPlaceholderKey) || !strings.Contains(string(raw), postmanExampleHost) {
		t.Fatal("the collection has no placeholder key or fictional host")
	}
	info, _ := col["info"].(map[string]any)
	if info["schema"] != postmanSchema {
		t.Fatalf("schema %v", info["schema"])
	}
	if !postmanRoutePublic(col, "GET /api/health") {
		t.Fatal("a public route must send no key")
	}
}

// TestPostmanHTTP checks the public collection and the session collection answer JSON
// with the placeholder key, and that a real key is not echoed back.
func TestPostmanHTTP(t *testing.T) {
	srv, raw := startDocsApp(t)
	code, body := doReq(t, nil, http.MethodGet, srv.URL+"/api/docs/postman.json", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("public collection: %d %s", code, body)
	}
	var col map[string]any
	if err := json.Unmarshal(body, &col); err != nil {
		t.Fatal(err)
	}
	info, _ := col["info"].(map[string]any)
	if info["schema"] != postmanSchema {
		t.Fatalf("public schema %v", info["schema"])
	}
	if postmanSecret.Match(body) || strings.Contains(string(body), raw) {
		t.Fatal("public collection echoes a secret")
	}
	code, body = doReq(t, nil, http.MethodGet, srv.URL+"/api/postman.json", "", raw, "")
	if code != http.StatusOK {
		t.Fatalf("session collection: %d %s", code, body)
	}
	if strings.Contains(string(body), raw) || postmanSecret.Match(body) {
		t.Fatal("session collection echoes the caller key")
	}
}

// catalogKeys returns every routeDocs route as "METHOD /path", sorted.
func catalogKeys() []string {
	out := make([]string, 0, len(routeDocs))
	for pattern := range routeDocs {
		out = append(out, convertedKey(pattern))
	}
	sort.Strings(out)
	return out
}

// openAPIRouteKeys returns every operation of an OpenAPI document as "METHOD /path", sorted.
func openAPIRouteKeys(doc map[string]any) []string {
	ops := postmanOps(doc)
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.method + " " + op.path
	}
	sort.Strings(out)
	return out
}

// postmanRouteKeys returns every request of a collection as "METHOD /path", sorted.
// A folder is walked; a request is one route.
func postmanRouteKeys(col map[string]any) []string {
	var out []string
	var walk func([]any)
	walk = func(items []any) {
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if nested, ok := item["item"].([]any); ok {
				walk(nested)
				continue
			}
			req, _ := item["request"].(map[string]any)
			method, _ := req["method"].(string)
			url, _ := req["url"].(map[string]any)
			out = append(out, method+" "+postmanURLPath(url))
		}
	}
	items, _ := col["item"].([]any)
	walk(items)
	sort.Strings(out)
	return out
}

// postmanURLPath rebuilds an OpenAPI path from a Postman url.path. A :name segment becomes {name}.
func postmanURLPath(url map[string]any) string {
	segs, _ := url["path"].([]any)
	var b strings.Builder
	for _, raw := range segs {
		seg, _ := raw.(string)
		if strings.HasPrefix(seg, ":") {
			seg = "{" + seg[1:] + "}"
		}
		b.WriteByte('/')
		b.WriteString(seg)
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// postmanRoutePublic reports whether the request for key sends no Api-Key.
func postmanRoutePublic(col map[string]any, key string) bool {
	var found bool
	var walk func([]any)
	walk = func(items []any) {
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if nested, ok := item["item"].([]any); ok {
				walk(nested)
				continue
			}
			req, _ := item["request"].(map[string]any)
			method, _ := req["method"].(string)
			url, _ := req["url"].(map[string]any)
			if method+" "+postmanURLPath(url) != key {
				continue
			}
			auth, _ := req["auth"].(map[string]any)
			found = auth["type"] == "noauth"
		}
	}
	items, _ := col["item"].([]any)
	walk(items)
	return found
}

// missingKeys lists the keys of want that got does not have.
func missingKeys(want, got []string) string {
	have := map[string]bool{}
	for _, k := range got {
		have[k] = true
	}
	var out []string
	for _, k := range want {
		if !have[k] {
			out = append(out, k)
		}
	}
	return strings.Join(out, ", ")
}
