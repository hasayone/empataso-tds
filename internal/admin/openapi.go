package admin

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/egerkuzma/emtds/internal/security"
)

const (
	// openAPIVersion is the OpenAPI specification version of the served document.
	openAPIVersion = "3.1.0"

	// openAPIInfoVersion is the version of the admin API contract in info.version.
	openAPIInfoVersion = "1"

	// openAPIKeyScheme names the security scheme of the Api-Key header.
	openAPIKeyScheme = "ApiKey"

	// pathTailMarker flags a path parameter that spans several segments, so a
	// client keeps its slashes when it fills the template in.
	pathTailMarker = "x-emtds-path"

	// i18nExtension carries the Russian and Ukrainian texts of an operation, a
	// parameter or a tag: {ru, uk}.
	i18nExtension = "x-emtds-i18n"

	// publicExtension marks an operation mounted without the session check.
	publicExtension = "x-emtds-public"
)

// pathParamPattern matches one {name} or {name...} segment of a mux pattern.
var pathParamPattern = regexp.MustCompile(`\{([^{}]+?)(\.\.\.)?\}`)

// pathParamDescriptions explains the path parameters the admin routes use, by name.
var pathParamDescriptions = map[string]queryParam{
	"id":         {Description: "Record id.", RU: "Id записи.", UK: "Id запису."},
	"group":      {Description: "Campaign id (the slug of #/flows/{slug}).", RU: "Id кампании (slug из #/flows/{slug}).", UK: "Id кампанії (slug із #/flows/{slug})."},
	"name":       {Description: "Name of the stored item.", RU: "Имя сохранённого объекта.", UK: "Ім'я збереженого об'єкта."},
	"message_id": {Description: "Id of one reply in a chat.", RU: "Id одного ответа в чате.", UK: "Id однієї відповіді в чаті."},
	"path": {
		Description: "Relative file path inside the landing directory; slashes are kept.",
		RU:          "Относительный путь файла в папке лендинга; слеши сохраняются.",
		UK:          "Відносний шлях файлу в теці лендінгу; слеші зберігаються.",
	},
}

// errorDescriptions gives the generic meaning of every status a routeDoc may list in Errors.
// The route description says what the status means for that route.
var errorDescriptions = map[int]string{
	http.StatusBadRequest:            "Bad request: a parameter or the body is invalid.",
	http.StatusForbidden:             "Forbidden for this caller: a hidden field, a record outside its view or a missing key action.",
	http.StatusNotFound:              "Not found.",
	http.StatusConflict:              "Conflict with the current state.",
	http.StatusRequestEntityTooLarge: "The body is too large.",
	http.StatusTooManyRequests:       "Too many requests at once.",
	http.StatusInternalServerError:   "Internal error.",
	http.StatusBadGateway:            "An outside service answered with an error.",
	http.StatusServiceUnavailable:    "A storage or service the route needs is not available.",
	http.StatusGatewayTimeout:        "The query or the outside service took too long.",
}

// nonWordPattern matches a run of characters that cannot appear in an operationId.
var nonWordPattern = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// handleOpenAPI writes the OpenAPI document of the routes the caller may use.
// The session comes from the request context. It does not fail.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.openAPIDocument(sessionFrom(r.Context())))
}

// routeVisible reports whether sess may see a route governed by rule.
// A session passes allowed first. An API session also loses cookie-only routes
// and routes whose dangerous action its key does not hold; a cookie session
// keeps every route it is allowed to call.
func (s *Server) routeVisible(sess security.Session, rule routeRule) bool {
	if !s.allowed(sess, rule) {
		return false
	}
	if !sess.API {
		return true
	}
	return !rule.cookie && (rule.action == "" || slices.Contains(sess.KeyActions, rule.action))
}

// ruleOf returns the access rule of a registered pattern: its routeAccess entry,
// or publicRule for a public route.
func ruleOf(pattern string) routeRule {
	if rule, ok := routeAccess[pattern]; ok {
		return rule
	}
	return publicRule(pattern)
}

// openAPIDocument builds the OpenAPI 3.1 document for sess.
// Every pattern registered through handle or handlePublic and visible to sess
// becomes one operation under its converted path. The result is a JSON-ready map.
func (s *Server) openAPIDocument(sess security.Session) map[string]any {
	return s.openAPIDocumentOf(func(rule routeRule) bool { return s.routeVisible(sess, rule) })
}

// openAPIDocumentOf builds the OpenAPI 3.1 document of the registered routes whose access
// rule passes visible. It returns a JSON-ready map and does not fail.
func (s *Server) openAPIDocumentOf(visible func(routeRule) bool) map[string]any {
	paths := map[string]any{}
	for _, pattern := range append(slices.Clone(s.routes), s.public...) {
		rule := ruleOf(pattern)
		if !visible(rule) {
			continue
		}
		method, path, op := buildOperation(pattern, rule)
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = op
	}
	return map[string]any{
		"openapi":  openAPIVersion,
		"info":     map[string]any{"title": "emtds admin API", "version": openAPIInfoVersion, "description": toonAPIDescription},
		"servers":  []any{map[string]any{"url": "/"}},
		"security": []any{map[string]any{openAPIKeyScheme: []any{}}},
		"tags":     openAPITags(),
		"paths":    paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				openAPIKeyScheme: map[string]any{"type": "apiKey", "in": "header", "name": "Api-Key"},
			},
			"parameters": map[string]any{formatParam: toonOpenAPIParam()},
		},
	}
}

// buildOperation converts one mux pattern and its access rule to an operation.
// It returns the lowercase method, the OpenAPI path and the operation object.
// The documentation comes from routeDocs; a route without an entry gets a
// generated operationId and the pattern as its summary.
func buildOperation(pattern string, rule routeRule) (method, path string, op map[string]any) {
	verb, muxPath, _ := strings.Cut(pattern, " ")
	method = strings.ToLower(verb)
	path = pathParamPattern.ReplaceAllString(muxPath, "{$1}")
	doc, documented := routeDocs[pattern]
	op = map[string]any{
		"operationId": doc.ID,
		"summary":     doc.Summary,
		"tags":        []any{routeTag(muxPath)},
		"responses":   responsesOf(doc),
	}
	if !documented {
		op["operationId"] = generatedOperationID(method, muxPath)
		op["summary"] = pattern
	}
	if doc.Description != "" {
		op["description"] = doc.Description
	}
	if i18n := operationI18n(doc); i18n != nil {
		op[i18nExtension] = i18n
	}
	if params := operationParams(muxPath, doc); len(params) > 0 {
		op["parameters"] = params
	}
	if body := requestBodyOf(doc); body != nil {
		op["requestBody"] = body
	}
	if _, public := publicRoutes[pattern]; public {
		op["security"] = []any{}
		op[publicExtension] = true
	}
	applyRuleExtensions(op, rule)
	if doc.MCP != "" {
		op["x-mcp"] = doc.MCP
	}
	return method, path, op
}

// operationI18n returns the x-emtds-i18n value of doc, {ru:{summary, description}, uk:{…}},
// or nil when doc has neither translation.
func operationI18n(doc routeDoc) map[string]any {
	if doc.RU == (docText{}) && doc.UK == (docText{}) {
		return nil
	}
	text := func(t docText) map[string]any {
		return map[string]any{"summary": t.Summary, "description": t.Description}
	}
	return map[string]any{"ru": text(doc.RU), "uk": text(doc.UK)}
}

// responsesOf returns the responses object of doc: the success status with the
// Response sample, every status of doc.Errors with its generic meaning, and the
// default answer shared by all routes.
func responsesOf(doc routeDoc) map[string]any {
	ok := map[string]any{"description": "Success."}
	if doc.Response != nil {
		media := "application/json"
		if _, text := doc.Response.(string); text {
			media = doc.ResponseType
			if media == "" {
				media = "text/plain"
			}
		}
		content := map[string]any{media: map[string]any{"example": doc.Response}}
		if media == "application/json" {
			if example, converted := toonExample(doc.Response); converted {
				content[toonMedia] = map[string]any{"schema": map[string]any{"type": "string"}, "example": example}
			}
		}
		ok["content"] = content
	}
	out := map[string]any{
		"default":                         map[string]any{"description": "JSON, or TOON with ?format=toon or Accept: text/toon; errors are {error, code}"},
		strconv.Itoa(doc.successStatus()): ok,
	}
	for _, code := range doc.Errors {
		out[strconv.Itoa(code)] = map[string]any{"description": errorDescriptions[code]}
	}
	return out
}

// applyRuleExtensions copies the access rule onto op as x-emtds-* extensions.
// op is changed in place; empty and false fields are left out.
func applyRuleExtensions(op map[string]any, rule routeRule) {
	if rule.sec != secAny {
		op["x-emtds-section"] = string(rule.sec)
	}
	if rule.admin {
		op["x-emtds-admin"] = true
	}
	if rule.action != "" {
		op["x-emtds-action"] = rule.action
	}
	if rule.heavy {
		op["x-emtds-heavy"] = true
	}
	if rule.cookie {
		op["x-emtds-cookie-only"] = true
	}
}

// generatedOperationID derives a stable operationId from a method and a mux
// path, for example "put_groups_id". The leading "api" segment is dropped.
func generatedOperationID(method, muxPath string) string {
	name := pathParamPattern.ReplaceAllString(strings.TrimPrefix(muxPath, "/api"), "$1")
	name = strings.Trim(nonWordPattern.ReplaceAllString(name, "_"), "_")
	return method + "_" + name
}

// operationParams lists the path parameters of muxPath followed by the query
// parameters of doc. Path parameters are always required strings.
func operationParams(muxPath string, doc routeDoc) []any {
	var params []any
	for _, m := range pathParamPattern.FindAllStringSubmatch(muxPath, -1) {
		schema := map[string]any{"type": "string"}
		if m[2] != "" {
			schema[pathTailMarker] = true
		}
		desc := pathParamDescriptions[m[1]]
		p := map[string]any{
			"name": m[1], "in": "path", "required": true,
			"description": desc.Description, "schema": schema,
		}
		addParamI18n(p, desc)
		params = append(params, p)
	}
	for _, q := range doc.Query {
		params = append(params, q.parameter())
	}
	return params
}

// requestBodyOf returns the requestBody object of doc, or nil when the route
// takes no body. A JSON body carries doc.Body as its schema; a raw body is
// plain text. doc.Example becomes the example of the media type.
func requestBodyOf(doc routeDoc) map[string]any {
	var media string
	var schema map[string]any
	switch {
	case doc.Body != nil:
		media, schema = "application/json", doc.Body
	case doc.RawBody:
		media, schema = "text/plain", map[string]any{"type": "string", "description": "File text."}
	default:
		return nil
	}
	content := map[string]any{"schema": schema}
	if doc.Example != nil {
		content["example"] = doc.Example
	}
	return map[string]any{"required": true, "content": map[string]any{media: content}}
}
