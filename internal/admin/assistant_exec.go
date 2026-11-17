package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/security"
)

const (
	// aiRawResultMax is the most bytes a tool handler may write before the call is refused.
	// An over-size answer is never cut: a cut JSON is useless and misleading.
	aiRawResultMax = 256 << 10

	// aiCatalogRawMax is the most bytes the handler of a catalog list tool may write. The executor
	// pages the list itself, so the whole visible list is read; a longer one is refused.
	aiCatalogRawMax = 8 << 20

	// aiResultMax is the size of a tool result after redaction and compression.
	aiResultMax = 8 << 10

	// aiArgsMax is the largest request body a tool call may send to a handler.
	aiArgsMax = 256 << 10

	// aiToolTimeout bounds one handler run, not counting the wait for a heavy slot.
	aiToolTimeout = 60 * time.Second

	// aiErrMsgMax is the longest handler error text passed on to the model.
	aiErrMsgMax = 300
)

var (
	// errAISessionGone means the user behind the turn is gone, disabled, or has a new password:
	// the turn must stop instead of telling the model.
	errAISessionGone = errors.New("ai: the user session is no longer valid")

	// errAIToolForbidden means the tool is not a registered read tool of the assistant.
	errAIToolForbidden = &aiToolError{Code: "forbidden", Msg: "forbidden: this tool cannot be run"}
)

// aiToolError is a tool failure the model is told about so it can change the call.
// Code is a stable code such as bad_request, forbidden, not_found or result_too_large; Msg is the
// text for the model and holds no secret.
type aiToolError struct {
	Code string
	Msg  string
}

// Error returns the text for the model.
func (e *aiToolError) Error() string { return e.Msg }

// aiHeavySlot admits one heavy ClickHouse read at a time for the whole assistant. It is
// process-wide on purpose: the limit protects the shared database, not one chat.
var aiHeavySlot = make(chan struct{}, 1)

// aiAcquireHeavy waits for the heavy slot or for ctx. It returns the release function, or the
// context error.
func aiAcquireHeavy(ctx context.Context) (func(), error) {
	select {
	case aiHeavySlot <- struct{}{}:
		return func() { <-aiHeavySlot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// aiConfigResources lists the read resources that show configuration, whose links keep their
// query. Every other read tool shows journal data and loses link queries.
var aiConfigResources = map[string]bool{
	"offers": true, "landings": true, "affiliate_networks": true, "traffic_sources": true,
	"domains": true, "campaigns": true, "boards": true, "log_fields": true, "lookup": true,
	"simulate": true,
}

// aiCampaignHiddenFields lists, in lower case, the campaign keys no tool result shows and no
// change may set, at any depth: the outbound postback templates, the shared template ids, the CURL
// find/replace strings of a stream and its [REMOTE] fetch. They are outbound requests and a
// channel for leaking data. Every key that starts with aiCampaignHiddenPrefix is hidden too.
// aiCampaignHidden is the one test of both.
var aiCampaignHiddenFields = map[string]bool{
	"postbacks": true, "postback_ids": true, "curl": true, "b_curl": true, "remote": true,
}

// aiCampaignHiddenPrefix is the start of every server-to-server key of a campaign.
const aiCampaignHiddenPrefix = "s2s"

// aiCampaignHidden reports whether key names a hidden campaign field, whatever its case.
func aiCampaignHidden(key string) bool {
	lk := strings.ToLower(key)
	return aiCampaignHiddenFields[lk] || strings.HasPrefix(lk, aiCampaignHiddenPrefix)
}

// aiSessionValid reports whether sess still names a usable account: the user exists, is enabled,
// has a password and holds the password fingerprint the session was issued under. The
// environment administrator is checked against the current root password. It is the check auth
// makes on a request, repeated before every tool call. An API session is never valid here.
func (s *Server) aiSessionValid(sess security.Session) bool {
	if sess.API {
		return false
	}
	if sess.UID == "" {
		return security.EqualTokens(sess.PwFP, s.current().fp)
	}
	u, ok := s.userByID(sess.UID)
	return ok && u.Enabled && u.Hash != "" && security.EqualTokens(sess.PwFP, u.Fingerprint())
}

// aiRunRead runs one read tool for the user of sess and returns its result as JSON: parsed,
// redacted and at most aiResultMax bytes, with long arrays cut and marked truncated.
//
// ctx is the turn context. board is the panel section of the page the user is on; it becomes the
// X-Emtds-Board header. t must be a read tool of the registry, or the call is refused with
// errAIToolForbidden: the executor is a way into s.inner and must never reach another route.
// args is the JSON object of the tool arguments.
//
// The request runs through the same boardScope, guard and requireAction chain as a panel request,
// so a limited user sees what the panel shows. errAISessionGone is returned when the user is no
// longer valid, before the call and again once a heavy slot was won. Other failures are
// *aiToolError values the model may see: a refused argument, a handler answer other than 2xx, or
// result_too_large when the handler wrote more than aiRawResultMax bytes.
func (s *Server) aiRunRead(ctx context.Context, sess security.Session, board string, t aiTool, args json.RawMessage) (json.RawMessage, error) {
	return s.aiRunReadWith(ctx, sess, board, t, args, nil)
}

// aiRunReadWith is aiRunRead with the list of secrets the turn already built. A nil list means the
// secrets of the process are collected for the call.
func (s *Server) aiRunReadWith(ctx context.Context, sess security.Session, board string, t aiTool, args json.RawMessage, secrets []string) (json.RawMessage, error) {
	tool, ok := aiToolByPattern(t.Pattern)
	if !ok || tool.Name != t.Name || t.Mode != asstRead || tool.Mode != asstRead || routeDocs[tool.Pattern].Assistant != asstRead {
		return nil, errAIToolForbidden
	}
	inner := s.inner[tool.Pattern]
	if inner == nil {
		return nil, errAIToolForbidden
	}
	if !s.aiSessionValid(sess) {
		return nil, errAISessionGone
	}
	page, err := aiParseCatalogQuery(tool, args)
	if err != nil {
		return nil, err
	}
	req, err := aiBuildRequest(ctx, board, tool, args)
	if err != nil {
		return nil, err
	}
	if tool.heavy {
		release, err := aiAcquireHeavy(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		if !s.aiSessionValid(sess) {
			return nil, errAISessionGone
		}
	}
	rctx, cancel := context.WithTimeout(context.WithValue(ctx, sessionKey, sess), aiToolTimeout)
	defer cancel()
	rawMax := aiRawResultMax
	if tool.catalog {
		rawMax = aiCatalogRawMax
	}
	w := newAIBoundedWriter(rawMax)
	if err := s.aiServe(inner, w, req.WithContext(rctx), tool.Name); err != nil {
		return nil, err
	}
	if w.over {
		return nil, &aiToolError{Code: aiErrResultTooLarge, Msg: aiErrResultTooLarge + ": narrow filters"}
	}
	if w.status < http.StatusOK || w.status >= http.StatusMultipleChoices {
		return nil, s.aiHandlerError(w.status, w.buf.Bytes(), secrets)
	}
	return s.aiShape(tool, page, w.buf.Bytes(), secrets)
}

// aiServe runs the handler and turns a panic into a tool error, so a defect in a handler fails
// one tool call and not the process. name is the tool, logged without any data.
func (s *Server) aiServe(h http.Handler, w http.ResponseWriter, r *http.Request, name string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			s.logWarn("assistant tool panicked", "tool", name)
			err = &aiToolError{Code: "internal", Msg: "internal: the tool failed"}
		}
	}()
	h.ServeHTTP(w, r)
	return nil
}

// aiHandlerError converts a non-2xx handler answer to a tool error. The handler text is redacted
// and cut to aiErrMsgMax bytes. secrets is the list of the turn, nil for the secrets of the process.
func (s *Server) aiHandlerError(status int, body []byte, secrets []string) *aiToolError {
	var e struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	code := e.Code
	if code == "" {
		code = errCode(status, "")
	}
	msg := aiRedactText(e.Error, s.aiSecretsOr(secrets))
	if msg == "" {
		msg = http.StatusText(status)
	}
	if len(msg) > aiErrMsgMax {
		msg = msg[:aiErrMsgMax]
		for !utf8.ValidString(msg) {
			msg = msg[:len(msg)-1]
		}
	}
	return &aiToolError{Code: code, Msg: fmt.Sprintf("%s (HTTP %d): %s", code, status, msg)}
}

// aiShape turns a handler body into the tool result: decode, drop the hidden campaign fields, cut
// the page of a catalog list, redact, compress to aiResultMax. page is the request of a catalog
// list tool and unused by other tools. secrets is the list of the turn, nil for the secrets of the
// process.
func (s *Server) aiShape(t aiTool, page aiCatalogQuery, body []byte, secrets []string) (json.RawMessage, error) {
	var v any = map[string]any{"ok": true}
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return nil, &aiToolError{Code: "bad_result", Msg: "bad_result: the answer is not JSON"}
		}
	}
	if t.Pattern == "GET /api/groups" {
		v = aiStripKeys(v, aiCampaignHidden)
	}
	if t.catalog {
		v = page.apply(v)
	}
	v = newAIRedactor(s.aiSecretsOr(secrets), !aiConfigResources[t.Resource]).value(v)
	v, err := aiCompress(v, aiResultMax)
	if err != nil {
		return nil, err
	}
	out, err := aiMarshal(v)
	if err != nil {
		return nil, &aiToolError{Code: "bad_result", Msg: "bad_result: the answer cannot be encoded"}
	}
	return out, nil
}

// aiStripFields removes every key of fields from the maps of v, at any depth, and returns v.
func aiStripFields(v any, fields map[string]bool) any {
	return aiStripKeys(v, func(k string) bool { return fields[strings.ToLower(k)] })
}

// aiStripKeys removes from the maps of v, at any depth, every key for which hidden is true, and
// returns v. v is a decoded JSON value and is changed in place.
func aiStripKeys(v any, hidden func(key string) bool) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if hidden(k) {
				delete(x, k)
				continue
			}
			x[k] = aiStripKeys(e, hidden)
		}
	case []any:
		for i, e := range x {
			x[i] = aiStripKeys(e, hidden)
		}
	}
	return v
}

// aiMarshal encodes v as compact JSON without HTML escaping, which keeps links readable and the
// size honest.
func aiMarshal(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// aiJSONLen returns the encoded size of v, or the largest int when it cannot be encoded.
func aiJSONLen(v any) int {
	b, err := aiMarshal(v)
	if err != nil {
		return math.MaxInt
	}
	return len(b)
}

// aiBuildRequest builds the handler request of tool t from the JSON arguments args.
//
// Path arguments fill the path template and SetPathValue; the other arguments become the query
// (GET) or, under the body argument, the JSON body. An argument the schema does not list, a
// missing required one, a path value with a slash, or a body over aiArgsMax is refused with a
// bad_request error. A limit is forced into 1..aiReadLimitMax, and added when the tool has the
// parameter and the call did not set it.
func aiBuildRequest(ctx context.Context, board string, t aiTool, raw json.RawMessage) (*http.Request, error) {
	args, err := aiDecodeArgs(raw)
	if err != nil {
		return nil, err
	}
	props, _ := t.Spec.Schema["properties"].(map[string]any)
	for k := range args {
		if _, ok := props[k]; !ok {
			return nil, aiBadArg("unknown argument " + k)
		}
	}
	required, _ := t.Spec.Schema["required"].([]string)
	for _, k := range required {
		if v, ok := args[k]; !ok || v == nil {
			return nil, aiBadArg("missing argument " + k)
		}
	}
	path := t.Path
	isPath := map[string]bool{}
	pathValues := map[string]string{}
	for _, name := range t.pathParams {
		isPath[name] = true
		val, ok, err := aiScalar(args[name])
		if err != nil || !ok || val == "" || strings.ContainsAny(val, "/\\?#") || strings.ContainsFunc(val, func(r rune) bool { return r < ' ' }) {
			return nil, aiBadArg("bad value of " + name)
		}
		pathValues[name] = val
		path = strings.Replace(path, "{"+name+"}", url.PathEscape(val), 1)
	}
	q := url.Values{}
	for k, v := range args {
		if isPath[k] || k == t.bodyProp || t.catalog && aiCatalogArgs[k] {
			continue
		}
		vals, err := aiQueryValues(v)
		if err != nil {
			return nil, aiBadArg("bad value of " + k)
		}
		for _, one := range vals {
			q.Add(k, one)
		}
	}
	if _, has := props["limit"]; has && !t.catalog {
		n, _ := strconv.Atoi(q.Get("limit"))
		if n <= 0 || n > aiReadLimitMax {
			n = aiReadLimitMax
		}
		q.Set("limit", strconv.Itoa(n))
	}
	var body *bytes.Reader
	if b, ok := args[t.bodyProp]; t.bodyProp != "" && ok {
		data, err := json.Marshal(b)
		if err != nil || len(data) > aiArgsMax {
			return nil, aiBadArg("bad value of " + t.bodyProp)
		}
		body = bytes.NewReader(data)
	}
	target := path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var reader io.Reader = http.NoBody
	if body != nil {
		reader = body
	}
	req, err := http.NewRequestWithContext(ctx, t.Method, target, reader)
	if err != nil {
		return nil, aiBadArg("bad request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, val := range pathValues {
		req.SetPathValue(name, val)
	}
	if board = strings.TrimSpace(board); board != "" {
		req.Header.Set(boardHeader, board)
	}
	return req, nil
}

// aiBadArg returns the tool error for a refused argument.
func aiBadArg(text string) *aiToolError {
	return &aiToolError{Code: "bad_request", Msg: "bad_request: " + text}
}

// aiDecodeArgs decodes the argument object of a tool call. Empty input and null are an empty
// object. Numbers stay exact.
func aiDecodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]any{}, nil
	}
	if len(raw) > aiArgsMax {
		return nil, aiBadArg("arguments too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil || args == nil {
		return nil, aiBadArg("arguments must be a JSON object")
	}
	return args, nil
}

// aiScalar renders a string, number or boolean argument as text. ok is false for null. A boolean
// is 1 or 0, as the panel sends it. An array or object is an error.
func aiScalar(v any) (text string, ok bool, err error) {
	switch x := v.(type) {
	case nil:
		return "", false, nil
	case string:
		return x, true, nil
	case json.Number:
		return x.String(), true, nil
	case bool:
		if x {
			return "1", true, nil
		}
		return "0", true, nil
	}
	return "", false, errors.New("not a scalar")
}

// aiQueryValues renders an argument as the values of one query parameter: one for a scalar, one per
// element for an array. Null and empty strings give none.
func aiQueryValues(v any) ([]string, error) {
	items := []any{v}
	if arr, ok := v.([]any); ok {
		items = arr
	}
	var out []string
	for _, item := range items {
		text, ok, err := aiScalar(item)
		if err != nil {
			return nil, err
		}
		if ok && text != "" {
			out = append(out, text)
		}
	}
	return out, nil
}

// aiBoundedWriter is the ResponseWriter of a tool call. It keeps the answer in memory and refuses
// more than limit bytes: the write that would pass the limit fails, which stops the handler, and
// over is set. Nothing of an over-size answer is kept for use.
type aiBoundedWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
	limit  int
	over   bool
}

// newAIBoundedWriter returns a writer that accepts at most limit bytes.
func newAIBoundedWriter(limit int) *aiBoundedWriter {
	return &aiBoundedWriter{header: http.Header{}, limit: limit}
}

// Header returns the header map the handler fills; it is not sent anywhere.
func (w *aiBoundedWriter) Header() http.Header { return w.header }

// WriteHeader records the first status code.
func (w *aiBoundedWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

// Write appends p, or fails with an error and sets over when the limit would be passed.
func (w *aiBoundedWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.over || w.buf.Len()+len(p) > w.limit {
		w.over = true
		return 0, errors.New("ai: tool result too large")
	}
	return w.buf.Write(p)
}

// Flush does nothing: the answer is read after the handler returns.
func (w *aiBoundedWriter) Flush() {}

// aiSlot is one array of a result that can be shortened.
type aiSlot struct {
	size int
	arr  []any
	mark func(total int)
	set  func([]any)
}

// aiCompress shrinks v to at most limit encoded bytes without cutting any JSON text. The largest
// array is halved again and again; a map that lost rows gets truncated=true and <key>_total with
// the original length, and a top-level array is wrapped as {items, total, truncated}. If the
// arrays are not enough, long strings are shortened. It fails with result_too_large when nothing
// more can be cut.
func aiCompress(v any, limit int) (any, error) {
	if aiJSONLen(v) <= limit {
		return v, nil
	}
	root := v
	if arr, ok := v.([]any); ok {
		root = map[string]any{"items": arr, "total": len(arr)}
	}
	if m, ok := root.(map[string]any); ok {
		m["truncated"] = true
	}
	for aiJSONLen(root) > limit {
		slot, ok := aiLargestSlot(root)
		if !ok {
			break
		}
		slot.mark(len(slot.arr))
		slot.set(slot.arr[:len(slot.arr)/2])
	}
	for _, longest := range []int{1024, 256, aiShortenFloor} {
		if aiJSONLen(root) <= limit {
			break
		}
		root = aiShortenStrings(root, longest)
	}
	if aiJSONLen(root) > limit {
		return nil, &aiToolError{Code: aiErrResultTooLarge, Msg: aiErrResultTooLarge + ": narrow filters"}
	}
	return root, nil
}

// aiLargestSlot returns the non-empty array of root with the largest encoded size.
func aiLargestSlot(root any) (aiSlot, bool) {
	var slots []aiSlot
	aiCollectSlots(root, func(int) {}, func([]any) {}, &slots)
	best, found := aiSlot{}, false
	for _, s := range slots {
		if !found || s.size > best.size {
			best, found = s, true
		}
	}
	return best, found
}

// aiCollectSlots appends every non-empty array under node to out. mark and set are the hooks of
// the place that holds node: mark records the original length, set replaces the array.
func aiCollectSlots(node any, mark func(int), set func([]any), out *[]aiSlot) {
	switch x := node.(type) {
	case map[string]any:
		for k, child := range x {
			aiCollectSlots(child,
				func(total int) {
					if _, has := x[k+"_total"]; !has {
						x[k+"_total"] = total
					}
				},
				func(a []any) { x[k] = a }, out)
		}
	case []any:
		if len(x) > 0 {
			*out = append(*out, aiSlot{size: aiJSONLen(x), arr: x, mark: mark, set: set})
		}
		for i, child := range x {
			aiCollectSlots(child, func(int) {}, func(a []any) { x[i] = a }, out)
		}
	}
}

// aiShortenStrings cuts every string of v longer than longest bytes to its start and an ellipsis,
// on a rune boundary, and returns v.
func aiShortenStrings(v any, longest int) any {
	switch x := v.(type) {
	case string:
		if len(x) <= longest {
			return x
		}
		cut := longest
		for cut > 0 && !utf8.RuneStart(x[cut]) {
			cut--
		}
		return x[:cut] + aiTruncMark
	case map[string]any:
		for k, e := range x {
			x[k] = aiShortenStrings(e, longest)
		}
	case []any:
		for i, e := range x {
			x[i] = aiShortenStrings(e, longest)
		}
	}
	return v
}
