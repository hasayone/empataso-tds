package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
)

const (
	protocolVersion = "2025-06-18"
	serverName      = "emtds"

	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// version is the server version reported in initialize; a release build may
// override it with -ldflags "-X main.version=...".
var version = "dev"

// request is one incoming JSON-RPC message. A missing ID marks a notification.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// response is one outgoing JSON-RPC message; ID is "null" when the request
// could not be parsed.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// textContent is a text item of a tool result.
type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolResult is the result of tools/call.
type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError"`
}

// server answers MCP requests from a tool list backed by the admin API.
type server struct {
	tools  []*tool
	byName map[string]*tool
	writes bool
	client *apiClient
	log    *log.Logger
}

// newServer creates a server for tools. When writes is false the write
// tools are hidden from tools/list and refused by tools/call.
func newServer(tools []*tool, writes bool, client *apiClient, logger *log.Logger) *server {
	s := &server{tools: tools, byName: map[string]*tool{}, writes: writes, client: client, log: logger}
	for _, t := range tools {
		s.byName[t.Name] = t
	}
	return s
}

// serve reads newline-delimited JSON-RPC messages from r and writes the
// responses to w until r is exhausted. Requests are handled one at a time.
// It returns nil on end of input and an error on a read or write failure.
func (s *server) serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	enc := json.NewEncoder(w)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if resp := s.handleLine(line); resp != nil {
				if err := enc.Encode(resp); err != nil {
					return fmt.Errorf("writing response: %w", err)
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("reading request: %w", readErr)
		}
	}
}

// handleLine parses and answers one message. It returns nil for a notification.
func (s *server) handleLine(line []byte) *response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return &response{JSONRPC: "2.0", Error: &rpcError{codeParseError, "parse error: " + err.Error()}}
	}
	if len(req.ID) == 0 {
		return nil
	}
	if req.Method == "" {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidRequest, "method is required"}}
	}
	result, rpcErr := s.dispatch(req)
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
}

// dispatch runs the handler of req.Method. It returns the result, or a
// JSON-RPC error for an unknown method or bad params.
func (s *server) dispatch(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.visibleTools()}, nil
	case "tools/call":
		return s.callTool(req.Params)
	}
	return nil, &rpcError{codeMethodNotFound, "method not found: " + req.Method}
}

// visibleTools returns the tools offered to the agent: all read tools, and
// the write tools only when writes are enabled.
func (s *server) visibleTools() []*tool {
	list := make([]*tool, 0, len(s.tools))
	for _, t := range s.tools {
		if !t.write || s.writes {
			list = append(list, t)
		}
	}
	return list
}

// callTool runs tools/call. params holds {"name": ..., "arguments": {...}}.
// It returns a -32602 error for unparsable params or a missing name; an
// unknown tool, a disabled write tool and API failures are error results.
func (s *server) callTool(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{codeInvalidParams, "tools/call needs params {name, arguments}"}
	}
	t, ok := s.byName[p.Name]
	switch {
	case !ok:
		return errorResult("Unknown tool: " + p.Name), nil
	case t.write && !s.writes:
		return errorResult("Tool " + p.Name + " changes data and is disabled; set EMTDS_MCP_WRITE=1 to enable write tools."), nil
	}
	s.log.Printf("tools/call %s", p.Name)
	res := s.client.call(t, p.Arguments)
	return toolResult{Content: []textContent{{Type: "text", Text: res.Text}}, IsError: res.IsError}, nil
}

// errorResult builds a tool result that reports msg as an error.
func errorResult(msg string) toolResult {
	return toolResult{Content: []textContent{{Type: "text", Text: msg}}, IsError: true}
}
