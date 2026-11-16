package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

const (
	// anthropicVersion is the API version header the Messages API requires.
	anthropicVersion = "2023-06-01"
	// anthropicDefaultMax is the max_tokens sent when the request names none; the API requires one.
	anthropicDefaultMax = 2048
	// anthropicModelsQuery asks for the largest page of the model list.
	anthropicModelsQuery = "?limit=1000"
)

// anthropicProvider calls the Anthropic Messages API.
type anthropicProvider struct {
	c *aiClient
}

// newAnthropicProvider builds the provider. base is the API origin, key the secret
// sent in x-api-key, hc the HTTP client.
func newAnthropicProvider(base, key string, hc *http.Client) *anthropicProvider {
	return &anthropicProvider{c: newAIClient(base, key, hc, func(h http.Header) {
		h.Set("x-api-key", key)
		h.Set("anthropic-version", anthropicVersion)
	})}
}

// anthropicBlock is one content block of a message or of an answer.
// Type is text, tool_use or tool_result.
type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

// anthropicMsg is one message of the request.
type anthropicMsg struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicTool is one tool of the request.
type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// anthropicRequest is the body of POST /v1/messages.
type anthropicRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []anthropicMsg  `json:"messages"`
	Tools     []anthropicTool `json:"tools,omitempty"`
}

// anthropicResponse is the part of the Messages answer the assistant uses.
type anthropicResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// anthropicMessages converts the conversation. An assistant message becomes text and
// tool_use blocks; the results of the tool messages that follow it become tool_result
// blocks of one user message. Empty messages are dropped.
func anthropicMessages(in []aiTurnMsg) []anthropicMsg {
	out := make([]anthropicMsg, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case aiRoleUser:
			if strings.TrimSpace(m.Text) != "" {
				out = append(out, anthropicMsg{Role: "user", Content: []anthropicBlock{{Type: "text", Text: m.Text}}})
			}
		case aiRoleAssistant:
			var blocks []anthropicBlock
			if strings.TrimSpace(m.Text) != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Text})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: aiArgs(tc.Args)})
			}
			if len(blocks) > 0 {
				out = append(out, anthropicMsg{Role: "assistant", Content: blocks})
			}
		case aiRoleTool:
			res := anthropicBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Text}
			if n := len(out); n > 0 && out[n-1].Role == "user" && out[n-1].Content[0].Type == "tool_result" {
				out[n-1].Content = append(out[n-1].Content, res)
				continue
			}
			out = append(out, anthropicMsg{Role: "user", Content: []anthropicBlock{res}})
		}
	}
	return out
}

// Complete sends one Messages call. It maps tools to input_schema, tool_use blocks to
// tool calls and the usage to input and output tokens; cached input counts as input.
func (p *anthropicProvider) Complete(ctx context.Context, req aiRequest) (aiReply, error) {
	body := anthropicRequest{Model: req.Model, MaxTokens: req.MaxTokens, System: req.System,
		Messages: anthropicMessages(req.Messages)}
	if body.MaxTokens <= 0 {
		body.MaxTokens = anthropicDefaultMax
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: aiSchema(t.Schema)})
	}
	var res anthropicResponse
	if err := p.c.do(ctx, http.MethodPost, "/v1/messages", body, &res); err != nil {
		return aiReply{}, err
	}
	reply := aiReply{StopReason: res.StopReason,
		InputTokens:  res.Usage.InputTokens + res.Usage.CacheCreationInputTokens + res.Usage.CacheReadInputTokens,
		OutputTokens: res.Usage.OutputTokens}
	var text strings.Builder
	for _, b := range res.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			reply.ToolCalls = append(reply.ToolCalls, aiToolCall{ID: b.ID, Name: b.Name, Args: aiArgs(b.Input)})
		}
	}
	reply.Text = text.String()
	reply.markUsage()
	return reply, nil
}

// Models lists the models of GET /v1/models with the id and the display name. Tool support is
// left unknown (nil), as the list does not state it.
func (p *anthropicProvider) Models(ctx context.Context) ([]aiModelInfo, error) {
	return aiModelList(ctx, p.c, "/v1/models"+anthropicModelsQuery, "", false)
}
