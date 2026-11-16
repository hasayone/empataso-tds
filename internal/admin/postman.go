package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

const (
	// postmanSchema is the Collection v2.1 schema URL Postman imports.
	postmanSchema = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"

	// postmanPlaceholderKey is the fictional Api-Key value in the collection.
	// It is not a secret and does not open anything.
	postmanPlaceholderKey = "em_example"

	// postmanExampleHost is the fictional base address stored in the baseUrl variable.
	postmanExampleHost = "https://admin.example"

	// postmanInfoText tells a person importing the collection that the samples are made up.
	postmanInfoText = "Examples are fictional. The Api-Key value em_example is a placeholder, not a real key. " +
		"Set the baseUrl variable to the admin address. The requests are the same routes as the OpenAPI document this collection was built from."
)

// postmanOp is one operation taken from an OpenAPI document, ready to become a request.
type postmanOp struct {
	method      string
	path        string
	tag         string
	summary     string
	description string
	public      bool
	op          map[string]any
}

// postmanCollection builds a Postman Collection v2.1 from an OpenAPI document produced by openAPIDocumentOf.
// doc is that document. The result is a JSON-ready map: one request for each operation and no other request.
// Samples come from the document examples. The key and the host are placeholders. It does not fail.
func postmanCollection(doc map[string]any) map[string]any {
	return map[string]any{
		"info": map[string]any{
			"name":        postmanTitle(doc),
			"description": postmanInfoText,
			"schema":      postmanSchema,
		},
		"auth":     postmanKeyAuth(),
		"variable": []any{map[string]any{"key": "baseUrl", "value": postmanExampleHost}},
		"item":     postmanFolders(doc),
	}
}

// handlePostman writes the Postman collection of the routes the caller may use,
// the same set as handleOpenAPI. The session comes from the request context. It does not fail.
func (s *Server) handlePostman(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, postmanCollection(s.openAPIDocument(sessionFrom(r.Context()))))
}

// handleDocsPostman writes the Postman collection of every route.
// It is mounted behind docsGate, so it answers 404 while public docs are off and 429 over the rate.
// A collection that could not be encoded answers 500.
func (s *Server) handleDocsPostman(w http.ResponseWriter, r *http.Request) {
	d := s.publicDocsBuilt()
	if len(d.postman) == 0 {
		writeErr(w, http.StatusInternalServerError, "postman")
		return
	}
	writeDocsBody(w, r, d.postmanTag, "application/json; charset=utf-8", d.postman)
}

// postmanTitle returns the collection name: the OpenAPI title, or a fixed name when the document has none.
func postmanTitle(doc map[string]any) string {
	info, _ := doc["info"].(map[string]any)
	title, _ := info["title"].(string)
	if title == "" {
		return "emtds admin API"
	}
	return title
}

// postmanKeyAuth is the collection auth: the Api-Key header with a placeholder value.
func postmanKeyAuth() map[string]any {
	return map[string]any{
		"type": "apikey",
		"apikey": []any{
			map[string]any{"key": "key", "value": "Api-Key", "type": "string"},
			map[string]any{"key": "value", "value": postmanPlaceholderKey, "type": "string"},
			map[string]any{"key": "in", "value": "header", "type": "string"},
		},
	}
}

// postmanFolders groups the operations of doc into one folder per tag, in the document tag order.
// Inside a folder, requests go by path and then by method. Empty tags are left out.
func postmanFolders(doc map[string]any) []any {
	ops := postmanOps(doc)
	byTag := map[string][]postmanOp{}
	var tags []string
	seen := map[string]bool{}
	if list, ok := doc["tags"].([]any); ok {
		for _, raw := range list {
			t, _ := raw.(map[string]any)
			name, _ := t["name"].(string)
			if name != "" && !seen[name] {
				seen[name] = true
				tags = append(tags, name)
			}
		}
	}
	for _, op := range ops {
		if !seen[op.tag] {
			seen[op.tag] = true
			tags = append(tags, op.tag)
		}
		byTag[op.tag] = append(byTag[op.tag], op)
	}
	var items []any
	for _, tag := range tags {
		list := byTag[tag]
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].path != list[j].path {
				return list[i].path < list[j].path
			}
			return postmanMethodRank(list[i].method) < postmanMethodRank(list[j].method)
		})
		folder := make([]any, len(list))
		for i, op := range list {
			folder[i] = postmanItem(op)
		}
		items = append(items, map[string]any{"name": postmanTagName(doc, tag), "item": folder})
	}
	return items
}

// postmanMethodRank orders the methods the docs page shows. An unknown method sorts last.
func postmanMethodRank(method string) int {
	switch method {
	case http.MethodGet:
		return 0
	case http.MethodPost:
		return 1
	case http.MethodPut:
		return 2
	case http.MethodPatch:
		return 3
	case http.MethodDelete:
		return 4
	default:
		return 5
	}
}

// postmanTagName returns the English label of an OpenAPI tag, or the tag id when the document has no label.
func postmanTagName(doc map[string]any, tag string) string {
	if list, ok := doc["tags"].([]any); ok {
		for _, raw := range list {
			t, _ := raw.(map[string]any)
			name, _ := t["name"].(string)
			if name != tag {
				continue
			}
			if desc, _ := t["description"].(string); desc != "" {
				return desc
			}
		}
	}
	if tag == "" {
		return "Other"
	}
	return tag
}

// postmanOps lists every operation of doc. Paths are sorted; methods follow postmanMethodRank.
// An operation that is not an object is skipped.
func postmanOps(doc map[string]any) []postmanOp {
	paths, _ := doc["paths"].(map[string]any)
	keys := make([]string, 0, len(paths))
	for path := range paths {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	methods := []string{"get", "post", "put", "patch", "delete", "head", "options"}
	var out []postmanOp
	for _, path := range keys {
		item, _ := paths[path].(map[string]any)
		for _, method := range methods {
			raw, ok := item[method]
			if !ok {
				continue
			}
			op, _ := raw.(map[string]any)
			if op == nil {
				continue
			}
			out = append(out, postmanOp{
				method:      strings.ToUpper(method),
				path:        path,
				tag:         postmanOpTag(op),
				summary:     strField(op, "summary"),
				description: strField(op, "description"),
				public:      opIsPublic(op),
				op:          op,
			})
		}
	}
	return out
}

// postmanOpTag returns the first tag of an operation, or "" when it has none.
func postmanOpTag(op map[string]any) string {
	tags, _ := op["tags"].([]any)
	if len(tags) == 0 {
		return ""
	}
	name, _ := tags[0].(string)
	return name
}

// strField returns a string field of m, or "" when the field is missing or not a string.
func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// opIsPublic reports whether an operation is mounted without a key.
// A public operation has x-emtds-public or an empty security list.
func opIsPublic(op map[string]any) bool {
	if op[publicExtension] == true {
		return true
	}
	sec, ok := op["security"].([]any)
	return ok && len(sec) == 0
}

// postmanItem returns one collection item for op. A public route sends no key.
func postmanItem(op postmanOp) map[string]any {
	name := op.summary
	if name == "" {
		name = op.method + " " + op.path
	}
	req := map[string]any{
		"method":      op.method,
		"header":      postmanHeaders(op.op),
		"url":         postmanURL(op),
		"description": strings.TrimSpace(op.summary + "\n\n" + op.description),
	}
	if op.public {
		req["auth"] = map[string]any{"type": "noauth"}
	}
	if body := postmanBody(op.op); body != nil {
		req["body"] = body
	}
	return map[string]any{"name": name, "request": req}
}

// postmanHeaders returns the Content-Type header when the operation has a request body, and nothing otherwise.
func postmanHeaders(op map[string]any) []any {
	media, _ := postmanBodyContent(op)
	if media == "" {
		return []any{}
	}
	return []any{map[string]any{"key": "Content-Type", "value": media}}
}

// postmanURL returns the Postman URL of op. Path parameters become :name variables with the value example.
// Required query parameters are in the raw URL; optional ones are listed and disabled.
func postmanURL(op postmanOp) map[string]any {
	params := postmanParams(op.op)
	var path []any
	var vars []any
	rawPath := ""
	for _, seg := range strings.Split(strings.Trim(op.path, "/"), "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
			path = append(path, ":"+name)
			rawPath += "/:" + name
			vars = append(vars, map[string]any{
				"key":         name,
				"value":       "example",
				"description": postmanParamText(params, name),
			})
			continue
		}
		path = append(path, seg)
		rawPath += "/" + seg
	}
	url := map[string]any{
		"raw":  "{{baseUrl}}" + rawPath + postmanRawQuery(params),
		"host": []any{"{{baseUrl}}"},
		"path": path,
	}
	if len(vars) > 0 {
		url["variable"] = vars
	}
	if q := postmanQuery(params); len(q) > 0 {
		url["query"] = q
	}
	return url
}

// postmanParams returns the parameter objects of an operation. A parameter that is not an object is skipped.
func postmanParams(op map[string]any) []map[string]any {
	raw, _ := op["parameters"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		p, ok := item.(map[string]any)
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// postmanParamText returns the description of the parameter named name, or "" when it is not listed.
func postmanParamText(params []map[string]any, name string) string {
	for _, p := range params {
		if strField(p, "name") == name {
			return strField(p, "description")
		}
	}
	return ""
}

// postmanQuery returns the query parameters of the operation.
// An optional parameter is disabled so importing the collection does not send it.
func postmanQuery(params []map[string]any) []any {
	var out []any
	for _, p := range params {
		if strField(p, "in") != "query" {
			continue
		}
		required, _ := p["required"].(bool)
		out = append(out, map[string]any{
			"key":         strField(p, "name"),
			"value":       "example",
			"description": strField(p, "description"),
			"disabled":    !required,
		})
	}
	return out
}

// postmanRawQuery returns the query string of the required query parameters, including the leading ?.
// It returns "" when every query parameter is optional or there are none.
func postmanRawQuery(params []map[string]any) string {
	var parts []string
	for _, p := range params {
		if strField(p, "in") != "query" {
			continue
		}
		required, _ := p["required"].(bool)
		if !required {
			continue
		}
		name := strField(p, "name")
		parts = append(parts, name+"=example")
	}
	if len(parts) == 0 {
		return ""
	}
	return "?" + strings.Join(parts, "&")
}

// postmanBodyContent returns the media type and the content object of the request body.
// JSON is preferred when several types are listed. ok is false when the operation has no body.
func postmanBodyContent(op map[string]any) (media string, content map[string]any) {
	rb, _ := op["requestBody"].(map[string]any)
	all, _ := rb["content"].(map[string]any)
	if len(all) == 0 {
		return "", nil
	}
	if raw, ok := all["application/json"]; ok {
		item, _ := raw.(map[string]any)
		return "application/json", item
	}
	for kind, raw := range all {
		item, _ := raw.(map[string]any)
		return kind, item
	}
	return "", nil
}

// postmanBody returns the raw body of an operation from its example, or nil when there is no example.
// A string example is sent as text. Any other example is indented JSON. A value that cannot be
// encoded is omitted.
func postmanBody(op map[string]any) map[string]any {
	media, content := postmanBodyContent(op)
	if content == nil {
		return nil
	}
	ex, ok := content["example"]
	if !ok {
		return nil
	}
	text, lang, ok := postmanExampleText(ex, media)
	if !ok {
		return nil
	}
	return map[string]any{
		"mode":    "raw",
		"raw":     text,
		"options": map[string]any{"raw": map[string]any{"language": lang}},
	}
}

// postmanExampleText renders an OpenAPI example as the raw body of a Postman request.
// media picks the language: json for a JSON media type, text otherwise. A string is used as written.
// ok is false when a non-string example cannot be encoded.
func postmanExampleText(ex any, media string) (text, lang string, ok bool) {
	lang = "text"
	if strings.Contains(media, "json") {
		lang = "json"
	}
	if s, isString := ex.(string); isString {
		return s, lang, true
	}
	b, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return "", "", false
	}
	return string(b), lang, true
}
