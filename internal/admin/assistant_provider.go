package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Provider ids of the AI assistant. They are the keys of ai.json and the values of
// the provider field of the settings.
const (
	aiProviderAnthropic  = "anthropic"
	aiProviderOpenAI     = "openai"
	aiProviderOpenRouter = "openrouter"
	aiProviderGemini     = "gemini"
)

const (
	// aiCallTimeout bounds one request to a provider, retry included per attempt.
	aiCallTimeout = 90 * time.Second
	// aiBodyLimit is the largest provider answer read; a bigger one is ai_provider_error.
	aiBodyLimit = 4 << 20
	// aiRetryMax is the longest Retry-After the client waits for before its one retry.
	aiRetryMax = 10 * time.Second
	// aiRetryDefault is the wait before the retry when the provider names no Retry-After.
	aiRetryDefault = time.Second
	// aiErrTextLimit cuts the provider message that reaches an error text.
	aiErrTextLimit = 200
)

// aiRoleTool is the role of a tool result message in the model conversation. The
// roles user and assistant are aiRoleUser and aiRoleAssistant.
const aiRoleTool = "tool"

// Hosts of the provider APIs and the scheme their base addresses use.
const (
	aiScheme         = "https://"
	aiHostAnthropic  = "api.anthropic.com"
	aiHostOpenAI     = "api.openai.com"
	aiHostOpenRouter = "openrouter.ai"
	aiHostGemini     = "generativelanguage.googleapis.com"
)

// aiProviderInfo describes one supported provider: its id, the public base address of its API and
// the host of that address, the only host the provider client dials for it.
type aiProviderInfo struct {
	ID      string
	BaseURL string
	Host    string
}

// aiProviders is the single table of the supported providers, in the order the panel shows them.
// The addresses are the public, stable API endpoints of the vendors, not deployment settings.
// Gemini is reached through its OpenAI-compatible endpoint.
var aiProviders = []aiProviderInfo{
	{ID: aiProviderAnthropic, BaseURL: aiScheme + aiHostAnthropic, Host: aiHostAnthropic},
	{ID: aiProviderOpenAI, BaseURL: aiScheme + aiHostOpenAI + "/v1", Host: aiHostOpenAI},
	{ID: aiProviderOpenRouter, BaseURL: aiScheme + aiHostOpenRouter + "/api/v1", Host: aiHostOpenRouter},
	{ID: aiProviderGemini, BaseURL: aiScheme + aiHostGemini + "/v1beta/openai", Host: aiHostGemini},
}

// aiProviderIDs lists the ids of aiProviders in the order the panel shows them.
var aiProviderIDs = aiProviderColumn(func(p aiProviderInfo) string { return p.ID })

// aiProviderBases maps each provider id to the public API base address of aiProviders.
var aiProviderBases = aiProviderMap(func(p aiProviderInfo) string { return p.BaseURL })

// aiAllowedHosts is the only set of hosts the provider client dials: the hosts of aiProviders.
var aiAllowedHosts = aiProviderSet(func(p aiProviderInfo) string { return p.Host })

// aiProviderColumn returns pick of every row of aiProviders, in table order.
func aiProviderColumn(pick func(aiProviderInfo) string) []string {
	out := make([]string, 0, len(aiProviders))
	for _, p := range aiProviders {
		out = append(out, pick(p))
	}
	return out
}

// aiProviderMap returns pick of every row of aiProviders, keyed by the provider id.
func aiProviderMap(pick func(aiProviderInfo) string) map[string]string {
	out := make(map[string]string, len(aiProviders))
	for _, p := range aiProviders {
		out[p.ID] = pick(p)
	}
	return out
}

// aiProviderSet returns the set of pick of every row of aiProviders.
func aiProviderSet(pick func(aiProviderInfo) string) map[string]struct{} {
	out := make(map[string]struct{}, len(aiProviders))
	for _, p := range aiProviders {
		out[pick(p)] = struct{}{}
	}
	return out
}

var (
	// errAIHostDenied is the dial refusal for a host outside aiAllowedHosts.
	errAIHostDenied = errors.New("ai: host is not on the provider list")
	// errAIRedirect is the refusal of any redirect of a provider answer.
	errAIRedirect = errors.New("ai: redirects are not followed")
)

// aiProvider is one model API behind the assistant.
//
// Complete sends one model call with tools and returns the text, the tool calls and
// the token usage. Models lists the models the key can use, each with the id and what the provider says about it (see aiModelInfo). Both return an
// *aiError with one of the codes ai_bad_key, ai_rate_limited, ai_provider_error and
// ai_timeout, or the context error when ctx was canceled; no error text contains the key.
//
// Streaming is not part of this interface. A provider that can stream adds a second,
// optional interface that callers detect with a type assertion, so the providers
// written for whole answers keep working unchanged.
type aiProvider interface {
	Complete(ctx context.Context, req aiRequest) (aiReply, error)
	Models(ctx context.Context) ([]aiModelInfo, error)
}

// aiToolSpec describes one tool the model may call. Schema is a JSON Schema object.
type aiToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// aiToolCall is one tool call of the model. Args is the JSON object of the arguments.
type aiToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// aiTurnMsg is one message of the model conversation.
//
// Role is "user", "assistant" or "tool". An assistant message may carry ToolCalls.
// A tool message answers one call: ToolCallID is the call id, ToolName its tool and
// Text the result.
type aiTurnMsg struct {
	Role       string
	Text       string
	ToolCalls  []aiToolCall
	ToolCallID string
	ToolName   string
}

// aiRequest is one model call. MaxTokens limits the output; zero uses the provider default.
type aiRequest struct {
	Model     string
	System    string
	Messages  []aiTurnMsg
	Tools     []aiToolSpec
	MaxTokens int
}

// aiReply is the answer of one model call. StopReason is the finish reason the
// provider reported, unchanged. UsageMissing is set when the answer has content but the
// provider reported no token usage; the token fields are then zero, nothing is charged and
// the turn logs it.
type aiReply struct {
	Text         string
	ToolCalls    []aiToolCall
	InputTokens  int
	OutputTokens int
	StopReason   string
	UsageMissing bool
}

// markUsage sets UsageMissing on a reply that has text or tool calls and no usage.
func (r *aiReply) markUsage() {
	r.UsageMissing = r.InputTokens == 0 && r.OutputTokens == 0 && (r.Text != "" || len(r.ToolCalls) > 0)
}

// aiHostAllowed reports whether host is one of the provider hosts. host is a bare host name.
func aiHostAllowed(host string) bool {
	_, ok := aiAllowedHosts[strings.ToLower(host)]
	return ok
}

// aiDialAllowed reports whether addr ("host:port") may be dialed: a host of aiAllowedHosts
// on port 443, the only port the providers use.
func aiDialAllowed(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	return err == nil && port == "443" && aiHostAllowed(host)
}

// newAITransport builds the transport of the provider client: no proxy, and a dialer
// that refuses every host outside aiAllowedHosts and every port but 443. extra is one more
// "host:port" the dialer lets through (the local test endpoint); empty adds none.
func newAITransport(extra string) *http.Transport {
	d := &net.Dialer{Timeout: 15 * time.Second}
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !aiDialAllowed(addr) && (extra == "" || !strings.EqualFold(addr, extra)) {
				return nil, errAIHostDenied
			}
			return d.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout: 15 * time.Second,
		MaxIdleConns:        4,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}

// newAIHTTPClient wraps rt in a client that never follows a redirect.
func newAIHTTPClient(rt http.RoundTripper) *http.Client {
	return &http.Client{
		Transport:     rt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errAIRedirect },
	}
}

// aiProdHTTP is the client of every real provider call.
var aiProdHTTP = newAIHTTPClient(newAITransport(""))

// aiTestHostPort returns the "host:port" a base URL points at, the port taken from the
// scheme (80 or 443) when the URL has none. It fails for an address that is not http or
// https or has no host. The error never holds the URL, which may carry credentials.
func aiTestHostPort(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("must be an http or https address with a host")
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// aiTestEndpoint returns the test base URL when provider is openai and Config.AITestBaseURL
// is set and no test injection names the provider; every other provider gets "".
func (s *Server) aiTestEndpoint(provider string) string {
	if provider != aiProviderOpenAI || s.cfg.AITestBaseURL == "" || s.cfg.AIBaseURLs[provider] != "" {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(s.cfg.AITestBaseURL), "/")
}

// aiHTTPClient returns the HTTP client for calls to provider.
// Config.AIHTTP wins; Config.AIBaseURLs alone (tests) selects a client without the
// host list so a local stub can answer. The test endpoint of Config.AITestBaseURL gets a
// guarded client that also lets its own host:port through, without connection reuse.
// Otherwise it is the guarded client.
func (s *Server) aiHTTPClient(provider string) *http.Client {
	if s.cfg.AIHTTP != nil {
		return s.cfg.AIHTTP
	}
	if base := s.aiTestEndpoint(provider); base != "" {
		if hp, err := aiTestHostPort(base); err == nil {
			tr := newAITransport(hp)
			tr.DisableKeepAlives = true
			return newAIHTTPClient(tr)
		}
	}
	if len(s.cfg.AIBaseURLs) > 0 {
		return newAIHTTPClient(&http.Transport{})
	}
	return aiProdHTTP
}

// aiBaseURL returns the API base of provider: the injected one when Config.AIBaseURLs
// names it, then the test endpoint (openai only), otherwise the public address.
// Unknown providers give "".
func (s *Server) aiBaseURL(provider string) string {
	if u := s.cfg.AIBaseURLs[provider]; u != "" {
		return strings.TrimRight(u, "/")
	}
	if u := s.aiTestEndpoint(provider); u != "" {
		return u
	}
	return aiProviderBases[provider]
}

// aiClient is the HTTP core shared by the providers: one key, one base, the call
// timeout, the size limit, one retry and the error mapping.
type aiClient struct {
	base      string
	key       string
	hc        *http.Client
	auth      func(http.Header)
	timeout   time.Duration
	retryWait time.Duration
}

// newAIClient builds the core. base is the API address without a trailing slash, key
// the provider key, hc the HTTP client and auth sets the authentication headers.
func newAIClient(base, key string, hc *http.Client, auth func(http.Header)) *aiClient {
	return &aiClient{base: strings.TrimRight(base, "/"), key: key, hc: hc, auth: auth,
		timeout: aiCallTimeout, retryWait: aiRetryDefault}
}

// fail builds the error of a provider call.
// status is the HTTP status of the answer to the client, code the ai_* code and text
// the message. The key is removed from the text, control characters become spaces and
// the text is cut to aiErrTextLimit.
func (c *aiClient) fail(status int, code, text string) *aiError {
	return newAIError(status, code, aiCleanText(text, c.key))
}

// aiCleanText makes a provider text safe for an error: key replaced by ***, control
// characters by spaces, then cut to aiErrTextLimit bytes on a rune boundary.
func aiCleanText(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "***")
	}
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	return truncateText(strings.TrimSpace(text), aiErrTextLimit)
}

// do sends one request and decodes a 2xx JSON answer into out.
// in is the JSON body or nil, out the target or nil. 429 and 5xx get one more try when
// Retry-After (or the default wait) is at most aiRetryMax. The answer is read through
// io.LimitReader at aiBodyLimit. It returns *aiError, or the context error when ctx was
// canceled by the caller.
func (c *aiClient) do(ctx context.Context, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return c.fail(http.StatusBadGateway, aiErrProvider, "request not built")
		}
	}
	for attempt := 0; ; attempt++ {
		status, hdr, body, err := c.once(ctx, method, path, payload)
		if err != nil {
			return err
		}
		if (status == http.StatusTooManyRequests || status >= 500) && attempt == 0 {
			if wait, ok := c.retryDelay(hdr); ok {
				if err := aiSleep(ctx, wait); err != nil {
					return err
				}
				continue
			}
		}
		if status >= 200 && status < 300 {
			if out == nil {
				return nil
			}
			if json.Unmarshal(body, out) != nil {
				return c.fail(http.StatusBadGateway, aiErrProvider, "the provider answer is not valid JSON")
			}
			return nil
		}
		return c.statusErr(status, body)
	}
}

// once makes one HTTP exchange. It returns the status, the headers and the body, or an error
// already mapped to an *aiError (or the caller's context error).
func (c *aiClient) once(ctx context.Context, method, path string, payload []byte) (int, http.Header, []byte, error) {
	cctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(cctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, nil, c.fail(http.StatusBadGateway, aiErrProvider, "request not built")
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.auth(req.Header)
	res, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, nil, c.transportErr(ctx, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, aiBodyLimit+1))
	if err != nil {
		return 0, nil, nil, c.transportErr(ctx, err)
	}
	if len(body) > aiBodyLimit {
		return 0, nil, nil, c.fail(http.StatusBadGateway, aiErrProvider, "the provider answer is too large")
	}
	return res.StatusCode, res.Header, body, nil
}

// transportErr maps a failed exchange. A canceled caller context is returned as is,
// a timeout is ai_timeout and everything else is ai_provider_error with a fixed text.
func (c *aiClient) transportErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return c.fail(http.StatusGatewayTimeout, aiErrTimeout, "the provider did not answer in time")
	case errors.Is(err, errAIHostDenied):
		return c.fail(http.StatusBadGateway, aiErrProvider, "the provider host is not allowed")
	case errors.Is(err, errAIRedirect):
		return c.fail(http.StatusBadGateway, aiErrProvider, "the provider redirected the request")
	}
	return c.fail(http.StatusBadGateway, aiErrProvider, "the provider is not reachable")
}

// retryDelay returns the wait before the retry and whether the retry is allowed.
// A Retry-After in seconds or as a date is used; without one the default wait applies.
// A wait over aiRetryMax forbids the retry.
func (c *aiClient) retryDelay(h http.Header) (time.Duration, bool) {
	wait := c.retryWait
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			wait = time.Duration(n * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			wait = time.Until(t)
		}
	}
	if wait < 0 {
		wait = 0
	}
	return wait, wait <= aiRetryMax
}

// aiSleep waits d or until ctx ends. It returns the context error in the second case.
func aiSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// statusErr maps a non-2xx answer: 401 and 403 are ai_bad_key, 429 is ai_rate_limited,
// anything else ai_provider_error. The key error carries the status and the error code the
// provider gave (never its message, which may quote part of the key); the others carry the
// status and the short message, not the body.
func (c *aiClient) statusErr(status int, body []byte) *aiError {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		text := "the provider rejected the API key (HTTP " + strconv.Itoa(status)
		if code := aiProviderCode(body); code != "" {
			text += ", " + code
		}
		return c.fail(http.StatusBadRequest, aiErrBadKey, text+")")
	case http.StatusTooManyRequests:
		return c.fail(http.StatusTooManyRequests, aiErrRateLimited, "the provider rate limit is reached")
	}
	text := "the provider answered " + strconv.Itoa(status)
	if msg := aiProviderMessage(body); msg != "" {
		text += ": " + msg
	}
	return c.fail(http.StatusBadGateway, aiErrProvider, text)
}

// aiProviderCode extracts the machine error code of a provider error body: error.code, else
// error.type, else error.status (Gemini), of {"error":{...}} or of the first item of a list.
// Only a short identifier of letters, digits, "_", "-" and "." is returned, "" otherwise.
func aiProviderCode(body []byte) string {
	type errObj struct {
		Error struct {
			Code   any    `json:"code"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"error"`
	}
	var one errObj
	if json.Unmarshal(body, &one) != nil {
		var many []errObj
		if json.Unmarshal(body, &many) != nil || len(many) == 0 {
			return ""
		}
		one = many[0]
	}
	code, _ := one.Error.Code.(string)
	for _, v := range []string{code, one.Error.Type, one.Error.Status} {
		if aiShortIdent(v) {
			return v
		}
	}
	return ""
}

// aiShortIdent reports whether v is 1..64 letters, digits, "_", "-" or ".".
func aiShortIdent(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if !(r == '_' || r == '-' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

// aiProviderMessage extracts the short error message from a provider error body.
// It understands {"error":{"message"}}, {"error":"text"} and a list of such objects.
// It returns "" for anything else, so a body that is not an error never leaks.
func aiProviderMessage(body []byte) string {
	var one struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &one) != nil {
		var many []struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &many) != nil || len(many) == 0 {
			return ""
		}
		one = many[0]
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(one.Error, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	var text string
	if json.Unmarshal(one.Error, &text) == nil {
		return text
	}
	return ""
}

// aiArgs returns args as a JSON object: an empty or invalid value becomes {}.
func aiArgs(args json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(args)) == 0 || !json.Valid(args) {
		return json.RawMessage(`{}`)
	}
	return args
}

// aiSchema returns the tool schema, or an empty object schema when there is none.
func aiSchema(s map[string]any) map[string]any {
	if s == nil {
		return map[string]any{"type": "object"}
	}
	return s
}
