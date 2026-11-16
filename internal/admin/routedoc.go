package admin

import "net/http"

const (
	// mcpRead marks an operation an agent may call without changing data.
	mcpRead = "read"

	// mcpWrite marks an operation that changes data on the server.
	mcpWrite = "write"

	// asstRead marks a route the AI assistant calls on its own to read data.
	asstRead = "read"

	// asstPropose marks a route the AI assistant never calls: it stores the change as a
	// proposal and the user applies it.
	asstPropose = "propose"
)

// queryParam describes one query parameter of a documented route.
// Description is English; RU and UK are the same text in Russian and Ukrainian.
type queryParam struct {
	Name        string
	Description string
	RU, UK      string
	// Type is the JSON Schema type of one value: string or integer.
	Type string
	// Array marks a parameter that may repeat; each value has the given Type.
	Array    bool
	Required bool
}

// parameter returns the OpenAPI parameter object of q.
// The translations go to x-emtds-i18n on the parameter, never into its schema.
func (q queryParam) parameter() map[string]any {
	schema := map[string]any{"type": q.Type}
	if q.Array {
		schema = map[string]any{"type": "array", "items": schema}
	}
	p := map[string]any{
		"name": q.Name, "in": "query", "required": q.Required,
		"description": q.Description, "schema": schema,
	}
	addParamI18n(p, q)
	return p
}

// addParamI18n writes the Russian and Ukrainian descriptions of q to p as x-emtds-i18n.
// p is an OpenAPI parameter object; it is left as is when q has no translation.
func addParamI18n(p map[string]any, q queryParam) {
	if q.RU != "" || q.UK != "" {
		p[i18nExtension] = map[string]any{"ru": q.RU, "uk": q.UK}
	}
}

// docText is the Russian or Ukrainian text of one route doc.
type docText struct {
	Summary     string
	Description string
}

// routeDoc is the documentation of one route: what the OpenAPI operation
// says beyond the access rule.
//
// ID is the operationId and must be unique. Summary and Description are
// English; RU and UK hold the same text in Russian and Ukrainian. Query lists
// the query parameters. Body is the JSON Schema of a JSON request body;
// RawBody marks a plain-text body instead, and Example is a sample of either.
// Status is the success status, 200 when zero. Response is a sample success
// answer: a JSON value, or a string sent as ResponseType (text/plain when
// empty); nil means the route answers with an empty body. Errors lists the
// statuses the route itself may answer besides the 401, 403 and 429 every
// route shares. MCP is mcpRead or mcpWrite for a route an agent may call, and
// empty for every other route; a route behind a key action is listed only for
// keys that hold the action.
type routeDoc struct {
	ID           string
	Summary      string
	Description  string
	RU, UK       docText
	Query        []queryParam
	Body         map[string]any
	RawBody      bool
	Example      any
	Status       int
	Response     any
	ResponseType string
	Errors       []int
	MCP          string
	// Assistant is asstRead or asstPropose for a route the AI assistant may use, and empty for
	// every other route. It is not written to OpenAPI or x-mcp.
	Assistant string
}

// successStatus returns the status d answers with on success: Status, or 200 when it is zero.
func (d routeDoc) successStatus() int {
	if d.Status == 0 {
		return http.StatusOK
	}
	return d.Status
}

// strQ returns an optional string query parameter.
func strQ(name, desc string) queryParam {
	return queryParam{Name: name, Description: desc, Type: "string"}
}

// intQ returns an optional integer query parameter.
func intQ(name, desc string) queryParam {
	return queryParam{Name: name, Description: desc, Type: "integer"}
}

// loc returns q with its Russian and Ukrainian descriptions.
func (q queryParam) loc(ru, uk string) queryParam {
	q.RU, q.UK = ru, uk
	return q
}

// required returns q marked as required.
func (q queryParam) required() queryParam {
	q.Required = true
	return q
}

// repeated returns q marked as repeatable.
func (q queryParam) repeated() queryParam {
	q.Array = true
	return q
}

// joinParams concatenates parameter lists in order.
func joinParams(lists ...[]queryParam) []queryParam {
	var out []queryParam
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// objectBody returns the schema of a free-form JSON object body.
// desc names the key fields; the server validates the rest.
func objectBody(desc string) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true, "description": desc}
}

// routeDocs documents every route by mux pattern: the routes behind the session
// check (routeAccess) and the public ones (publicRoutes). It is the one source of
// the OpenAPI document, the MCP tools, the assistant tools and the API docs page.
var routeDocs = buildRouteDocs()

// buildRouteDocs assembles routeDocs from the catalog table and the area tables.
// It panics when two tables document the same pattern: the tables are fixed at
// build time and a silent overwrite would lose one of the texts.
func buildRouteDocs() map[string]routeDoc {
	docs := map[string]routeDoc{}
	add := func(part map[string]routeDoc) {
		for pattern, d := range part {
			if _, dup := docs[pattern]; dup {
				// Intentional startup panic: a route documented twice is a typo in a build-time table.
				panic("admin: route documented twice: " + pattern)
			}
			docs[pattern] = d
		}
	}
	for _, c := range catalogDocs {
		add(catalogRouteDocs(c))
	}
	add(campaignRouteDocs())
	add(statsRouteDocs())
	add(journalRouteDocs())
	add(keywordRouteDocs())
	add(toolRouteDocs())
	add(adminRouteDocs())
	add(aiRouteDocs())
	markAssistantRoutes(docs)
	return docs
}

// assistantRoutes lists the routes the AI assistant may use, by mux pattern.
// A read route is called by the assistant itself; a propose route is only ever
// stored as a proposal. Landing files and archives, pages, domain writes, campaign
// postbacks, S2S, users, roles, keys and integrations are deliberately absent.
var assistantRoutes = buildAssistantRoutes()

// buildAssistantRoutes returns the assistantRoutes table: fixed entries plus the
// five CRUD routes of the offer, landing, affiliate network and traffic source catalogs.
func buildAssistantRoutes() map[string]string {
	m := map[string]string{
		"GET /api/groups":            asstRead,
		"PUT /api/groups/{id}":       asstPropose,
		"DELETE /api/groups/{id}":    asstPropose,
		"GET /api/boards":            asstRead,
		"GET /api/stats/summary":     asstRead,
		"GET /api/stats/timeseries":  asstRead,
		"GET /api/stats/breakdown":   asstRead,
		"GET /api/stats/performance": asstRead,
		"GET /api/logs":              asstRead,
		"GET /api/postbacks":         asstRead,
		"GET /api/log-values":        asstRead,
		"GET /api/report":            asstRead,
		"GET /api/log-fields":        asstRead,
		"POST /api/simulate":         asstRead,
		"GET /api/lookup":            asstRead,
		"GET /api/domains":           asstRead,
		"GET /api/domains/{id}":      asstRead,
	}
	for _, base := range []string{"/api/offers", "/api/landings", "/api/affiliate-networks", "/api/traffic-sources"} {
		m["GET "+base] = asstRead
		m["GET "+base+"/{id}"] = asstRead
		m["POST "+base] = asstPropose
		m["PUT "+base+"/{id}"] = asstPropose
		m["DELETE "+base+"/{id}"] = asstPropose
	}
	return m
}

// markAssistantRoutes sets Assistant on the entries of docs named in assistantRoutes.
// It panics on a pattern without an entry in docs: the table is fixed at build
// time and a typo must not start the server.
func markAssistantRoutes(docs map[string]routeDoc) {
	for pattern, mode := range assistantRoutes {
		d, ok := docs[pattern]
		if !ok {
			// Intentional startup panic: an assistant mark on an undocumented route is a typo in a build-time table.
			panic("admin: assistant route without documentation: " + pattern)
		}
		d.Assistant = mode
		docs[pattern] = d
	}
}
