package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// openAIProvider calls a Chat Completions API: OpenAI, OpenRouter and the
// OpenAI-compatible endpoint of Gemini. The key goes only into the Authorization header.
type openAIProvider struct {
	c *aiClient
	// maxField is the name of the output limit field: max_completion_tokens for OpenAI,
	// max_tokens for the other two.
	maxField string
	// toolName adds the tool name to the tool messages, which Gemini expects.
	toolName bool
	// idPrefix strips a prefix such as "models/" from the listed model ids.
	idPrefix string
	// richModels reads context, prices and tool support from the model list (OpenRouter).
	richModels bool
}

// newOpenAIProvider builds the provider for one of aiProviderOpenAI, aiProviderOpenRouter
// and aiProviderGemini. base is the API address including its version path, key the
// bearer secret and hc the HTTP client.
func newOpenAIProvider(provider, base, key string, hc *http.Client) *openAIProvider {
	p := &openAIProvider{maxField: "max_tokens", c: newAIClient(base, key, hc, func(h http.Header) {
		h.Set("Authorization", "Bearer "+key)
	})}
	switch provider {
	case aiProviderOpenAI:
		p.maxField = "max_completion_tokens"
	case aiProviderGemini:
		p.toolName = true
		p.idPrefix = "models/"
	case aiProviderOpenRouter:
		p.richModels = true
	}
	return p
}

// openAIFunction is the function part of a tool or of a tool call.
type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Arguments   string         `json:"arguments,omitempty"`
}

// openAIToolCall is a tool call of an assistant message.
type openAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

// openAITool is a tool of the request.
type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

// openAIMsg is one chat message. Content is null for an assistant message that only calls tools.
type openAIMsg struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// openAIResponse is the part of the chat answer the assistant uses.
type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content   json.RawMessage  `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// openAIMessages converts the conversation: the system text first, then the messages.
// A tool message keeps its call id, so the model can pair the result with its call.
func (p *openAIProvider) openAIMessages(system string, in []aiTurnMsg) []openAIMsg {
	out := make([]openAIMsg, 0, len(in)+1)
	str := func(s string) *string { return &s }
	if strings.TrimSpace(system) != "" {
		out = append(out, openAIMsg{Role: "system", Content: str(system)})
	}
	for _, m := range in {
		switch m.Role {
		case aiRoleUser:
			out = append(out, openAIMsg{Role: "user", Content: str(m.Text)})
		case aiRoleAssistant:
			msg := openAIMsg{Role: "assistant"}
			if m.Text != "" {
				msg.Content = str(m.Text)
			}
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, openAIToolCall{ID: tc.ID, Type: "function",
					Function: openAIFunction{Name: tc.Name, Arguments: string(aiArgs(tc.Args))}})
			}
			if msg.Content != nil || len(msg.ToolCalls) > 0 {
				out = append(out, msg)
			}
		case aiRoleTool:
			msg := openAIMsg{Role: aiRoleTool, Content: str(m.Text), ToolCallID: m.ToolCallID}
			if p.toolName {
				msg.Name = m.ToolName
			}
			out = append(out, msg)
		}
	}
	return out
}

// openAIText returns the text of a message content: a string, or the text parts of a list.
func openAIText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

// Complete sends one chat call with tools. It maps tool_calls to tool calls and
// prompt/completion usage to input and output tokens. A tool call without an id gets
// one made from its position, since the next request needs it.
func (p *openAIProvider) Complete(ctx context.Context, req aiRequest) (aiReply, error) {
	body := map[string]any{"model": req.Model, "messages": p.openAIMessages(req.System, req.Messages)}
	if req.MaxTokens > 0 {
		body[p.maxField] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]openAITool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, openAITool{Type: "function",
				Function: openAIFunction{Name: t.Name, Description: t.Description, Parameters: aiSchema(t.Schema)}})
		}
		body["tools"] = tools
	}
	var res openAIResponse
	if err := p.c.do(ctx, http.MethodPost, "/chat/completions", body, &res); err != nil {
		return aiReply{}, err
	}
	if len(res.Choices) == 0 {
		return aiReply{}, p.c.fail(http.StatusBadGateway, aiErrProvider, "the provider answered without a choice")
	}
	ch := res.Choices[0]
	reply := aiReply{Text: openAIText(ch.Message.Content), StopReason: ch.FinishReason,
		InputTokens: res.Usage.PromptTokens, OutputTokens: res.Usage.CompletionTokens}
	for i, tc := range ch.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = "call_" + strconv.Itoa(i)
		}
		reply.ToolCalls = append(reply.ToolCalls, aiToolCall{ID: id, Name: tc.Function.Name, Args: aiArgs(json.RawMessage(tc.Function.Arguments))})
	}
	reply.markUsage()
	return reply, nil
}

// Models lists the models of GET /models. OpenAI and Gemini give the id only, with tool support
// unknown (nil); OpenRouter also gives the name, the context window, the prices (per token, shown
// per million) and tool support, which is true when supported_parameters holds "tools".
func (p *openAIProvider) Models(ctx context.Context) ([]aiModelInfo, error) {
	return aiModelList(ctx, p.c, "/models", p.idPrefix, p.richModels)
}
