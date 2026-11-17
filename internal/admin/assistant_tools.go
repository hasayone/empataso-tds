package admin

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/hasayone/empataso-tds/internal/security"
)

const (
	// aiMaxToolName is the longest tool name a model API and an MCP client accept.
	aiMaxToolName = 64

	// aiReadLimitMax is the most rows a read tool returns; a larger limit is cut to it.
	aiReadLimitMax = 50

	// aiBodyProp is the tool argument that carries the JSON request body of a read route.
	aiBodyProp = "body"

	// aiToolCreateCampaign names the tool that proposes a new campaign. The route is the
	// upsert PUT /api/groups/{id}, shared with update_campaign; the tool is kept apart so the
	// model says which of the two it means.
	aiToolCreateCampaign = "create_campaign"

	// aiCatalogLimitDefault is the page size of a catalog list tool when the call sets no limit.
	aiCatalogLimitDefault = 20

	// aiCatalogIDsMax is the most ids one catalog call may ask for.
	aiCatalogIDsMax = 200
)

// aiCatalogResources lists the resources whose list tool pages and searches in the executor: the
// handlers take no search or paging parameters and answer the whole visible list.
var aiCatalogResources = map[string]bool{
	"offers": true, "landings": true, "affiliate_networks": true, "traffic_sources": true,
	"domains": true, "campaigns": true,
}

// aiCatalogArgs names the arguments the executor itself reads from a catalog list call. They are
// never sent to the handler.
var aiCatalogArgs = map[string]bool{"q": true, "ids": true, "limit": true, "offset": true}

// aiTool is one tool the model may call.
//
// Name is the MCP name of the route. Pattern, Method and Path are the mux pattern, its verb and its
// path template. Mode is asstRead (the server runs it) or asstPropose (the server stores a
// proposal and never runs it). Resource is the catalog or area the tool works on: offers,
// affiliate_networks, traffic_sources, landings, domains, campaigns, or for reads the area such as
// stats, logs, postbacks, report. Op is create, update or delete for a propose tool and empty for a
// read. Spec is what the model sees and must not be modified by a caller.
type aiTool struct {
	Name, Pattern, Method, Path, Mode, Resource, Op string
	Spec                                            aiToolSpec

	// bodyProp names the argument that holds the request body, empty when the route has none.
	bodyProp string
	// pathParams lists the path template parameters in order.
	pathParams []string
	// heavy marks a route that scans a large range of ClickHouse data.
	heavy bool
	// catalog marks a catalog list tool: the executor reads the whole list and returns one page
	// of it, found by the synthetic arguments q, ids, limit and offset.
	catalog bool
}

// aiRegistry returns every assistant tool sorted by name. It is built once from routeDocs, the
// single source of truth, and shared: callers must not change the result.
var aiRegistry = sync.OnceValue(buildAIToolRegistry)

// buildAIToolRegistry builds the tool list from the routes of routeDocs that carry an Assistant
// mark. A route without an access rule is skipped. The PUT /api/groups/{id} route yields the extra
// create_campaign tool.
func buildAIToolRegistry() []aiTool {
	var patterns []string
	for pattern, doc := range routeDocs {
		if doc.Assistant != "" {
			patterns = append(patterns, pattern)
		}
	}
	sort.Strings(patterns)
	var out []aiTool
	seen := map[string]bool{}
	add := func(t aiTool) {
		if t.Name != "" && !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	for _, pattern := range patterns {
		rule, ok := routeAccess[pattern]
		if !ok {
			continue
		}
		doc := routeDocs[pattern]
		_, path, op := buildOperation(pattern, rule)
		verb, muxPath, _ := strings.Cut(pattern, " ")
		base := aiTool{
			Pattern: pattern, Method: verb, Path: path, Mode: doc.Assistant,
			Resource: aiResourceOf(path), pathParams: aiPathParams(muxPath),
			heavy: rule.heavy || pattern == "POST /api/simulate",
		}
		id, _ := op["operationId"].(string)
		t := base
		t.Name = aiToolName(id)
		if doc.Assistant == asstRead {
			t.Spec, t.bodyProp = aiReadSpec(t.Name, op, doc)
			if verb == http.MethodGet && len(t.pathParams) == 0 && aiCatalogResources[t.Resource] {
				t.catalog = true
				aiAddCatalogArgs(&t.Spec)
			}
			add(t)
			continue
		}
		t.Op = aiOpOf(verb)
		t.Spec = aiProposeSpec(t.Name, doc, t.Op)
		add(t)
		if pattern == "PUT /api/groups/{id}" {
			c := base
			c.Name, c.Op = aiToolCreateCampaign, aiOpCreate
			c.Spec = aiProposeSpec(c.Name, doc, c.Op)
			add(c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// aiToolName maps an operationId to the tool name by the MCP rule: characters outside
// [a-zA-Z0-9_-] become '_' and the result is cut to aiMaxToolName.
func aiToolName(id string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, id)
	if len(name) > aiMaxToolName {
		name = name[:aiMaxToolName]
	}
	return name
}

// aiOpOf returns the change operation of an HTTP verb: POST creates, PUT updates, DELETE deletes.
// Any other verb gives an empty operation.
func aiOpOf(verb string) string {
	switch verb {
	case http.MethodPost:
		return aiOpCreate
	case http.MethodPut:
		return aiOpUpdate
	case http.MethodDelete:
		return aiOpDelete
	}
	return ""
}

// aiResourceOf derives the resource of a route from its path: the first segment after /api/ with
// dashes turned to underscores. Campaigns live under /api/groups and are named campaigns.
func aiResourceOf(path string) string {
	seg, _, _ := strings.Cut(strings.TrimPrefix(path, "/api/"), "/")
	seg = strings.ReplaceAll(seg, "-", "_")
	if seg == "groups" {
		return "campaigns"
	}
	return seg
}

// aiPathParams returns the names of the {name} segments of a mux path, in order.
func aiPathParams(muxPath string) []string {
	var out []string
	for _, m := range pathParamPattern.FindAllStringSubmatch(muxPath, -1) {
		out = append(out, m[1])
	}
	return out
}

// aiReadSpec builds the model-facing spec of a read tool from the OpenAPI operation op that
// buildOperation made for it. It returns the spec and the name of the body argument ("" when the
// route takes no body). A limit parameter is capped at aiReadLimitMax.
func aiReadSpec(name string, op map[string]any, doc routeDoc) (aiToolSpec, string) {
	props := map[string]any{}
	required := []string{}
	hasLimit := false
	params, _ := op["parameters"].([]any)
	for _, p := range params {
		param, ok := p.(map[string]any)
		if !ok {
			continue
		}
		pname, _ := param["name"].(string)
		prop := map[string]any{}
		if schema, ok := param["schema"].(map[string]any); ok {
			for k, v := range schema {
				if k != pathTailMarker {
					prop[k] = v
				}
			}
		}
		if desc, _ := param["description"].(string); desc != "" {
			prop["description"] = desc
		}
		if pname == "limit" {
			hasLimit = true
			prop["maximum"] = aiReadLimitMax
			cur, _ := prop["description"].(string)
			prop["description"] = joinNonEmpty(" ", cur, fmt.Sprintf("At most %d.", aiReadLimitMax))
		}
		props[pname] = prop
		if in, _ := param["in"].(string); in == "path" || param["required"] == true {
			required = append(required, pname)
		}
	}
	bodyProp := ""
	if body, ok := op["requestBody"].(map[string]any); ok {
		if content, ok := body["content"].(map[string]any); ok {
			if js, ok := content["application/json"].(map[string]any); ok {
				bodyProp = freeAIName(props, aiBodyProp)
				props[bodyProp] = js["schema"]
				if body["required"] == true {
					required = append(required, bodyProp)
				}
			}
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	desc := joinNonEmpty("\n\n", doc.Summary, doc.Description)
	if hasLimit {
		desc = joinNonEmpty("\n\n", desc, fmt.Sprintf("Returns at most %d rows; narrow the filters instead of asking for more.", aiReadLimitMax))
	}
	return aiToolSpec{Name: name, Description: desc, Schema: schema}, bodyProp
}

// aiAddCatalogArgs adds the arguments q, ids, limit and offset to the spec of a catalog list tool
// and says in its description how the answer is paged. The executor, not the handler, serves them.
func aiAddCatalogArgs(spec *aiToolSpec) {
	props, _ := spec.Schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		spec.Schema["properties"] = props
	}
	props["q"] = map[string]any{
		"type": "string",
		"description": "Search text: records whose name, host or id contains it, ignoring case. " +
			"Use q to search by name.",
	}
	props["ids"] = map[string]any{
		"type": "array", "items": map[string]any{"type": "string"}, "maxItems": aiCatalogIDsMax,
		"description": "Return only the records with these ids.",
	}
	props["limit"] = map[string]any{
		"type": "integer", "minimum": 1, "maximum": aiReadLimitMax,
		"description": fmt.Sprintf("Records per page, %d by default, at most %d.", aiCatalogLimitDefault, aiReadLimitMax),
	}
	props["offset"] = map[string]any{
		"type": "integer", "minimum": 0, "description": "Records to skip, for the next page.",
	}
	spec.Description = joinNonEmpty("\n\n", spec.Description,
		"The answer is {items, total, offset, limit}: total counts every match, items is one page. "+
			"Use q to search by name (a substring of the name or id, ignoring case), ids for known ids, "+
			"and offset to read the next page.")
}

// aiProposeSpec builds the spec of a propose tool. Update takes {id, patch}, create {patch},
// delete {id}; patch holds only the fields to change, described by the route body.
func aiProposeSpec(name string, doc routeDoc, op string) aiToolSpec {
	idProp := map[string]any{"type": "string", "description": "Id of the record."}
	patchDesc := "Fields to set."
	if d, _ := doc.Body["description"].(string); d != "" {
		patchDesc = "Fields to set. " + d
	}
	patchProp := map[string]any{"type": "object", "additionalProperties": true, "description": patchDesc}
	props := map[string]any{}
	var required []string
	var note string
	switch op {
	case aiOpCreate:
		props["patch"], required = patchProp, []string{"patch"}
		note = "Send the whole new record in patch."
	case aiOpUpdate:
		props["id"], props["patch"], required = idProp, patchProp, []string{"id", "patch"}
		note = "Send only the fields to change in patch; the server applies them over the stored record."
	default:
		props["id"], required = idProp, []string{"id"}
		note = "Only the id is needed."
	}
	summary := doc.Summary
	if name == aiToolCreateCampaign {
		summary = "Create a campaign."
	}
	desc := joinNonEmpty(" ", "PROPOSAL ONLY:", summary,
		"Nothing changes until the user presses Apply on the card.", note)
	return aiToolSpec{
		Name: name, Description: desc,
		Schema: map[string]any{"type": "object", "properties": props, "required": required},
	}
}

// freeAIName returns name, prefixed with "request_" until it is not a key of props.
func freeAIName(props map[string]any, name string) string {
	for {
		if _, taken := props[name]; !taken {
			return name
		}
		name = "request_" + name
	}
}

// joinNonEmpty joins the parts that are not blank with sep.
func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// aiToolNamed returns the registry tool with the given name.
func aiToolNamed(name string) (aiTool, bool) {
	for _, t := range aiRegistry() {
		if t.Name == name {
			return t, true
		}
	}
	return aiTool{}, false
}

// aiToolByPattern returns the registry tool of a read route, or of the single tool of a propose
// route; the pattern PUT /api/groups/{id} belongs to two tools and gives update_campaign.
func aiToolByPattern(pattern string) (aiTool, bool) {
	for _, t := range aiRegistry() {
		if t.Pattern == pattern {
			return t, true
		}
	}
	return aiTool{}, false
}

// aiTools returns the tools sess may use, sorted by name. A tool is listed only when the access
// rule of its route lets the session in, so the model is never offered a call that would answer
// 403. A read tool is listed only when its handler is registered. The result is shared and must
// not be modified.
func (s *Server) aiTools(sess security.Session) []aiTool {
	var out []aiTool
	for _, t := range aiRegistry() {
		rule, ok := routeAccess[t.Pattern]
		if !ok || !s.allowed(sess, rule) {
			continue
		}
		if t.Mode == asstRead && s.inner[t.Pattern] == nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

// aiToolSpecs returns the specs of tools in the form the provider call takes.
func aiToolSpecs(tools []aiTool) []aiToolSpec {
	out := make([]aiToolSpec, len(tools))
	for i, t := range tools {
		out[i] = t.Spec
	}
	return out
}
