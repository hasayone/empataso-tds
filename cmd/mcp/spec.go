package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	mcpRead  = "read"
	mcpWrite = "write"

	// maxToolName is the longest tool name MCP clients accept.
	maxToolName = 64

	// defaultBodyProp is the input property that carries the request body.
	defaultBodyProp = "body"

	// jsonContent is the only request body media type the admin API uses.
	jsonContent = "application/json"

	// plainTextContent is the media type of a raw text request body, such as a
	// landing file.
	plainTextContent = "text/plain"
)

// operationMethods lists the path item keys that hold an operation.
var operationMethods = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true}

// pathPlaceholder matches one {name} segment of a path template.
var pathPlaceholder = regexp.MustCompile(`\{([^{}]+)\}`)

// param is an OpenAPI path or query parameter.
type param struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Required    bool           `json:"required"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

// isPathTail reports whether the parameter is a multi-segment path tail
// ("x-emtds-path": true in its schema) whose slashes must be kept.
func (p param) isPathTail() bool {
	tail, _ := p.Schema["x-emtds-path"].(bool)
	return tail
}

// requestBody is the OpenAPI request body of an operation.
type requestBody struct {
	Required bool `json:"required"`
	Content  map[string]struct {
		Schema map[string]any `json:"schema"`
	} `json:"content"`
}

// operation is the subset of an OpenAPI operation used to build a tool.
type operation struct {
	OperationID string       `json:"operationId"`
	Summary     string       `json:"summary"`
	Description string       `json:"description"`
	Parameters  []param      `json:"parameters"`
	RequestBody *requestBody `json:"requestBody"`
	MCP         string       `json:"x-mcp"`
	Heavy       bool         `json:"x-emtds-heavy"`
}

// document is the top level of the OpenAPI document served by the admin.
type document struct {
	OpenAPI string                                `json:"openapi"`
	Paths   map[string]map[string]json.RawMessage `json:"paths"`
}

// tool is one MCP tool backed by one admin API operation.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`

	write        bool
	method       string
	path         string
	params       []param
	bodyProp     string
	bodyMedia    string
	bodyRequired bool
}

// parseTools builds the tool list from an OpenAPI document.
// data is the raw JSON document. It returns the tools sorted by name.
// It fails on invalid JSON, a missing "openapi" field, an unknown x-mcp
// value, a malformed operation, duplicate tool names, or when no operation
// is exposed as a tool.
func parseTools(data []byte) ([]*tool, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("openapi document is not valid JSON: %w", err)
	}
	if doc.OpenAPI == "" {
		return nil, fmt.Errorf("response is not an OpenAPI document (no \"openapi\" field)")
	}
	var tools []*tool
	seen := map[string]string{}
	for _, path := range sortedKeys(doc.Paths) {
		for _, method := range sortedKeys(doc.Paths[path]) {
			if !operationMethods[method] {
				continue
			}
			t, err := toolFromOperation(path, method, doc.Paths[path][method])
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			if t == nil {
				continue
			}
			if prev, dup := seen[t.Name]; dup {
				return nil, fmt.Errorf("%s %s: tool name %q already used by %s", strings.ToUpper(method), path, t.Name, prev)
			}
			seen[t.Name] = strings.ToUpper(method) + " " + path
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("openapi document exposes no operations with x-mcp")
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}

// toolFromOperation decodes one operation and converts it to a tool.
// It returns (nil, nil) when the operation has no x-mcp extension.
func toolFromOperation(path, method string, raw json.RawMessage) (*tool, error) {
	var op operation
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, fmt.Errorf("invalid operation: %w", err)
	}
	switch op.MCP {
	case "":
		return nil, nil
	case mcpRead, mcpWrite:
	default:
		return nil, fmt.Errorf("x-mcp must be %q or %q, got %q", mcpRead, mcpWrite, op.MCP)
	}
	name := sanitizeName(op.OperationID)
	if name == "" {
		return nil, fmt.Errorf("operationId is empty")
	}
	if err := checkPlaceholders(path, op.Parameters); err != nil {
		return nil, err
	}
	schema, bodyProp, bodyMedia, err := buildInputSchema(op)
	if err != nil {
		return nil, err
	}
	write := op.MCP == mcpWrite
	t := &tool{
		Name:        name,
		Description: describe(op, write),
		InputSchema: schema,
		write:       write,
		method:      strings.ToUpper(method),
		path:        path,
		params:      op.Parameters,
		bodyProp:    bodyProp,
		bodyMedia:   bodyMedia,
	}
	if op.RequestBody != nil {
		t.bodyRequired = op.RequestBody.Required
	}
	if !write {
		t.Annotations = map[string]any{"readOnlyHint": true}
	}
	return t, nil
}

// sanitizeName maps an operationId to a valid MCP tool name: characters
// outside [a-zA-Z0-9_-] become '_' and the result is cut to 64 characters.
func sanitizeName(id string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, id)
	if len(name) > maxToolName {
		name = name[:maxToolName]
	}
	return name
}

// describe joins summary and description and appends the write and heavy
// warnings. op is the operation; write marks a state-changing tool.
func describe(op operation, write bool) string {
	var parts []string
	for _, s := range []string{op.Summary, op.Description} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	if write {
		parts = append(parts, "WARNING: this tool changes data on the emtds server.")
	}
	if op.Heavy {
		parts = append(parts, "Heavy operation: it may be slow or resource intensive.")
	}
	return strings.Join(parts, "\n\n")
}

// checkPlaceholders verifies that every {name} in the path template has a
// matching path parameter. It returns an error naming the first one missing.
func checkPlaceholders(path string, params []param) error {
	have := map[string]bool{}
	for _, p := range params {
		if p.In == "path" {
			have[p.Name] = true
		}
	}
	for _, m := range pathPlaceholder.FindAllStringSubmatch(path, -1) {
		if !have[m[1]] {
			return fmt.Errorf("path placeholder {%s} has no path parameter", m[1])
		}
	}
	return nil
}

// buildInputSchema builds the JSON Schema of the tool arguments.
// Parameters become properties under their own name; the request body goes
// under "body", or under "request_body" (and so on) when a parameter already
// uses the name. It returns the schema, the body property name and the body
// media type (both "" when the operation has no request body). It fails on
// header or cookie parameters, duplicate parameter names and a body that is
// neither application/json nor text/plain with a string schema.
func buildInputSchema(op operation) (map[string]any, string, string, error) {
	props := map[string]any{}
	required := []string{}
	for _, p := range op.Parameters {
		if p.In != "path" && p.In != "query" {
			return nil, "", "", fmt.Errorf("parameter %q: unsupported location %q", p.Name, p.In)
		}
		if _, dup := props[p.Name]; dup {
			return nil, "", "", fmt.Errorf("parameter %q is declared twice", p.Name)
		}
		props[p.Name] = paramProperty(p)
		if p.Required || p.In == "path" {
			required = append(required, p.Name)
		}
	}
	bodyProp, bodyMedia := "", ""
	if op.RequestBody != nil {
		media, bodySchema, err := pickBody(op.RequestBody)
		if err != nil {
			return nil, "", "", err
		}
		bodyProp, bodyMedia = freeName(props, defaultBodyProp), media
		props[bodyProp] = bodySchema
		if op.RequestBody.Required {
			required = append(required, bodyProp)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema, bodyProp, bodyMedia, nil
}

// pickBody chooses the media type the tool sends and the schema of its input
// property. application/json wins when both are declared. text/plain is
// accepted only with a schema of type string. It fails when neither is usable.
func pickBody(rb *requestBody) (media string, schema map[string]any, err error) {
	if m, ok := rb.Content[jsonContent]; ok {
		return jsonContent, orEmpty(m.Schema), nil
	}
	if m, ok := rb.Content[plainTextContent]; ok {
		if kind, _ := m.Schema["type"].(string); kind != "string" {
			return "", nil, fmt.Errorf("requestBody %s content needs a schema of type string", plainTextContent)
		}
		return plainTextContent, m.Schema, nil
	}
	return "", nil, fmt.Errorf("requestBody has no %s or %s content", jsonContent, plainTextContent)
}

// paramProperty returns the input schema property for a parameter: a copy of
// its schema with the description filled in and the internal path-tail marker
// removed.
func paramProperty(p param) map[string]any {
	prop := map[string]any{}
	for k, v := range p.Schema {
		if k != "x-emtds-path" {
			prop[k] = v
		}
	}
	if _, has := prop["description"]; !has && p.Description != "" {
		prop["description"] = p.Description
	}
	return prop
}

// freeName returns name, prefixed with "request_" as often as needed until
// it is not a key of props.
func freeName(props map[string]any, name string) string {
	for {
		if _, taken := props[name]; !taken {
			return name
		}
		name = "request_" + name
	}
}

// orEmpty returns m, or an empty schema when m is nil.
func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
