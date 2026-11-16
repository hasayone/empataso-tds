package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// checkerTimeout bounds one request to the checker.
	checkerTimeout = 20 * time.Second
	// checkerMaxBody is the largest checker answer that is read.
	checkerMaxBody = 4 << 20
	// checkerGap is the pause between the end of one request and the start of the next.
	checkerGap = time.Second
	// checkerMaxRetryAfter caps the wait a Retry-After header can ask for.
	checkerMaxRetryAfter = 30 * time.Minute
	// checkerMsgMax is the longest checker message kept in an error.
	checkerMsgMax = 200
	// checkerNotJSONMsg is the error of a redirect or of an answer that is not JSON.
	checkerNotJSONMsg = "чекер ответил не JSON"
)

// checkerPrio orders the callers of the request gate: a user action goes before the queue.
type checkerPrio int

const (
	// prioUser is a request a person waits for: the test, the consoles and the refresh.
	prioUser checkerPrio = iota
	// prioQueue is a request of the background queue.
	prioQueue
)

// checkerError is a failed call to the checker.
// Status is the HTTP status, zero when no answer came. Msg is the short text of the checker or
// of the transport. RetryAfter is the pause a 429 asked for. NotJSON marks a redirect or an
// answer that is not JSON. Body is the raw answer of a non-2xx call, kept for the 409 conflict.
type checkerError struct {
	Status     int
	Msg        string
	RetryAfter time.Duration
	NotJSON    bool
	Body       []byte
}

// Error returns the HTTP status and the short text. It never holds the token or a header.
func (e *checkerError) Error() string {
	switch {
	case e.Status == 0:
		return e.Msg
	case e.Msg == "":
		return fmt.Sprintf("checker HTTP %d", e.Status)
	}
	return fmt.Sprintf("checker HTTP %d: %s", e.Status, e.Msg)
}

// transient reports whether the call may be repeated later: no answer, a 5xx or a 429.
func (e *checkerError) transient() bool {
	return !e.NotJSON && (e.Status == 0 || e.Status >= 500 || e.Status == http.StatusTooManyRequests)
}

// refused reports whether the checker refused the token or the rights: 401 or 403.
func (e *checkerError) refused() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// checkerGate lets one request through at a time for the whole admin process.
// Between two requests it keeps a pause of checkerGap and, after a 429, the wait of Retry-After.
// Waiters of prioUser are served before the waiters of prioQueue. nowFn and sleepFn replace the
// clock and the pause in tests; nil keeps the system ones.
type checkerGate struct {
	mu        sync.Mutex
	busy      bool
	last      time.Time
	notBefore time.Time
	user, bg  []chan struct{}
	nowFn     func() time.Time
	sleepFn   func(ctx context.Context, d time.Duration) error
}

// laterTime returns the later of a and b. Equal times return a.
func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// now returns the current UTC time of the gate.
func (g *checkerGate) now() time.Time {
	if g.nowFn != nil {
		return g.nowFn().UTC()
	}
	return time.Now().UTC()
}

// sleep waits d or until ctx ends and returns the context error in that case.
func (g *checkerGate) sleep(ctx context.Context, d time.Duration) error {
	if g.sleepFn != nil {
		return g.sleepFn(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// acquire waits for the turn of the caller and then for the pause since the last request.
// p is the priority. It returns the context error when ctx ends first; the gate is then left free.
// A caller that gets nil owns the gate and must call done exactly once.
func (g *checkerGate) acquire(ctx context.Context, p checkerPrio) error {
	g.mu.Lock()
	if g.busy {
		ch := make(chan struct{})
		if p == prioUser {
			g.user = append(g.user, ch)
		} else {
			g.bg = append(g.bg, ch)
		}
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			g.mu.Lock()
			removed := g.drop(ch)
			g.mu.Unlock()
			if !removed {
				g.done(0, false)
			}
			return ctx.Err()
		}
	} else {
		g.busy = true
		g.mu.Unlock()
	}
	g.mu.Lock()
	wait := laterTime(g.last.Add(checkerGap), g.notBefore).Sub(g.now())
	g.mu.Unlock()
	if wait > 0 {
		if err := g.sleep(ctx, wait); err != nil {
			g.done(0, false)
			return err
		}
	}
	return nil
}

// drop removes ch from the waiting lists and reports whether it was still waiting.
// The caller holds g.mu.
func (g *checkerGate) drop(ch chan struct{}) bool {
	for _, list := range []*[]chan struct{}{&g.user, &g.bg} {
		for i, c := range *list {
			if c == ch {
				*list = append((*list)[:i], (*list)[i+1:]...)
				return true
			}
		}
	}
	return false
}

// done gives the gate back and hands it to the next waiter, a user one first.
// made tells whether a request was sent, which starts the pause. retry, when positive, is the
// wait of a 429 before the next request.
func (g *checkerGate) done(retry time.Duration, made bool) {
	g.mu.Lock()
	if made {
		g.last = g.now()
	}
	if retry > 0 {
		g.notBefore = laterTime(g.notBefore, g.now().Add(retry))
	}
	var next chan struct{}
	switch {
	case len(g.user) > 0:
		next, g.user = g.user[0], g.user[1:]
	case len(g.bg) > 0:
		next, g.bg = g.bg[0], g.bg[1:]
	default:
		g.busy = false
	}
	g.mu.Unlock()
	if next != nil {
		close(next)
	}
}

// checkerClient calls the five allowed routes of the checker with one connection.
type checkerClient struct {
	gate   *checkerGate
	http   *http.Client
	base   string
	tenant string
	token  string
}

// newCheckerClient builds a client for a connection.
// set holds the base address, the tenant and the token. Config.CheckerURL replaces the address
// and Config.CheckerHTTP the HTTP client; redirects are never followed.
func (s *Server) newCheckerClient(set checkerSettings) *checkerClient {
	hc := &http.Client{}
	if s.cfg.CheckerHTTP != nil {
		cp := *s.cfg.CheckerHTTP
		hc = &cp
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := set.BaseURL
	if s.cfg.CheckerURL != "" {
		base = s.cfg.CheckerURL
	}
	return &checkerClient{gate: &s.checker.gate, http: hc, base: strings.TrimRight(base, "/"), tenant: set.TenantID, token: set.Token}
}

// do sends one request through the gate and returns the body of a 2xx answer.
// path is the route, q the query without format, and body the JSON body or nil.
// A transport failure, a status outside 2xx, a redirect and an answer that is not JSON come back
// as *checkerError. A 429 moves the next request by its Retry-After.
func (c *checkerClient) do(ctx context.Context, prio checkerPrio, method, path string, q url.Values, body []byte) ([]byte, error) {
	if err := c.gate.acquire(ctx, prio); err != nil {
		return nil, &checkerError{Msg: "request cancelled"}
	}
	raw, cerr := c.roundTrip(ctx, method, path, q, body)
	var retry time.Duration
	if cerr != nil {
		retry = cerr.RetryAfter
	}
	c.gate.done(retry, true)
	return raw, errOrNil(cerr)
}

// errOrNil returns nil for a nil *checkerError so that a typed nil never becomes a non-nil error.
func errOrNil(e *checkerError) error {
	if e == nil {
		return nil
	}
	return e
}

// roundTrip performs the HTTP exchange. The caller holds the gate.
// It returns the body of a 2xx JSON answer, or the *checkerError describing the failure.
func (c *checkerClient) roundTrip(ctx context.Context, method, path string, q url.Values, body []byte) ([]byte, *checkerError) {
	query := url.Values{}
	for k, v := range q {
		query[k] = v
	}
	query.Set("format", "json")
	ctx, cancel := context.WithTimeout(ctx, checkerTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path+"?"+query.Encode(), rd)
	if err != nil {
		return nil, &checkerError{Msg: "bad checker address"}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Tenant-ID", c.tenant)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &checkerError{Msg: "checker timed out"}
		}
		return nil, &checkerError{Msg: "checker is unreachable"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, checkerMaxBody+1))
	if err != nil {
		return nil, &checkerError{Status: resp.StatusCode, Msg: "checker answer was cut"}
	}
	if len(raw) > checkerMaxBody {
		return nil, &checkerError{Status: resp.StatusCode, Msg: "checker answer is too large"}
	}
	isJSON := checkerJSONType(resp.Header.Get("Content-Type"))
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, &checkerError{Status: resp.StatusCode, Msg: checkerNotJSONMsg, NotJSON: true}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if !isJSON {
			return nil, &checkerError{Status: resp.StatusCode, Msg: checkerNotJSONMsg, NotJSON: true}
		}
		return raw, nil
	}
	e := &checkerError{Status: resp.StatusCode, Body: raw}
	if isJSON {
		e.Msg = c.scrub(checkerMessage(raw))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return nil, e
}

// checkerJSONType reports whether a Content-Type header names JSON.
func checkerJSONType(header string) bool {
	mt, _, err := mime.ParseMediaType(header)
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

// parseRetryAfter reads a Retry-After header given in seconds.
// It returns zero for a missing or unreadable value and caps the wait at checkerMaxRetryAfter.
func parseRetryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, checkerMaxRetryAfter)
}

// checkerMessage extracts the short text of a checker error answer: "message" or "error",
// followed by the first validation text of "errors" when there is one.
// raw is the JSON body. It returns an empty string when nothing readable is there.
func checkerMessage(raw []byte) string {
	var v struct {
		Message any            `json:"message"`
		Error   any            `json:"error"`
		Errors  map[string]any `json:"errors"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	parts := []string{}
	for _, x := range []any{v.Message, v.Error} {
		if s, ok := x.(string); ok && s != "" {
			parts = append(parts, s)
			break
		}
	}
	keys := make([]string, 0, len(v.Errors))
	for k := range v.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch t := v.Errors[k].(type) {
		case []any:
			if len(t) > 0 {
				if s, ok := t[0].(string); ok {
					parts = append(parts, s)
				}
			}
		case string:
			parts = append(parts, t)
		}
		if len(parts) > 1 {
			break
		}
	}
	return strings.Join(parts, " ")
}

// scrub shortens a checker message to one line of at most checkerMsgMax runes and removes the
// token should the checker echo it.
func (c *checkerClient) scrub(msg string) string {
	if c.token != "" {
		msg = strings.ReplaceAll(msg, c.token, "***")
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if utf8.RuneCountInString(msg) > checkerMsgMax {
		msg = string([]rune(msg)[:checkerMsgMax])
	}
	return msg
}

// checkerUser is the safe part of the profile the token belongs to.
type checkerUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// me calls GET /api/auth/me and returns the name and the email of the token owner.
// The profile may sit in "data", in "user" or at the top level of the answer.
func (c *checkerClient) me(ctx context.Context, prio checkerPrio) (checkerUser, error) {
	raw, err := c.do(ctx, prio, http.MethodGet, "/api/auth/me", nil, nil)
	if err != nil {
		return checkerUser{}, err
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return checkerUser{}, &checkerError{Status: http.StatusOK, Msg: checkerNotJSONMsg, NotJSON: true}
	}
	for _, key := range []string{"data", "user"} {
		var u checkerUser
		if inner, ok := top[key]; ok && json.Unmarshal(inner, &u) == nil && (u.Name != "" || u.Email != "") {
			return u, nil
		}
	}
	var u checkerUser
	_ = json.Unmarshal(raw, &u)
	return u, nil
}

// checkerAppInfo is the part of an application the integration reads: the id, the bundle and
// the status of the checker.
type checkerAppInfo struct {
	ID       string `json:"id"`
	BundleID string `json:"bundle_id"`
	Status   string `json:"status"`
}

// findByBundle calls GET /api/apps?bundle_id=…&show_banned=true&show_archived=true.
// It returns the application whose bundle equals bundle, or nil when the checker lists none.
// An entry with another bundle is ignored, so a checker that skips the filter links nothing wrong.
func (c *checkerClient) findByBundle(ctx context.Context, prio checkerPrio, bundle string) (*checkerAppInfo, error) {
	q := url.Values{"bundle_id": {bundle}, "show_banned": {"true"}, "show_archived": {"true"}}
	raw, err := c.do(ctx, prio, http.MethodGet, "/api/apps", q, nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []checkerAppInfo `json:"data"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, &checkerError{Status: http.StatusOK, Msg: checkerNotJSONMsg, NotJSON: true}
	}
	for _, a := range list.Data {
		if strings.EqualFold(a.BundleID, bundle) && a.ID != "" {
			return &a, nil
		}
	}
	return nil, nil
}

// checkerCreateRequest is the body of POST /api/apps. The optional fields are left out when empty.
type checkerCreateRequest struct {
	Name         string `json:"name"`
	BundleID     string `json:"bundle_id"`
	PlayStoreURL string `json:"play_store_url"`
	KeitaroURL   string `json:"keitaro_url,omitempty"`
	NotionURL    string `json:"notion_url,omitempty"`
	Status       string `json:"status"`
	CheckFreq    int    `json:"check_frequency"`
	ConsoleDevID string `json:"console_developer_id,omitempty"`
	KeywordsData string `json:"keywords_data"`
}

// createApp calls POST /api/apps.
// It returns the created application, or the existing one with existed true when the checker
// answers 409 existing_app. Every other failure is a *checkerError.
func (c *checkerClient) createApp(ctx context.Context, prio checkerPrio, req checkerCreateRequest) (info *checkerAppInfo, existed bool, err error) {
	body, merr := json.Marshal(req)
	if merr != nil {
		return nil, false, &checkerError{Msg: "cannot encode the request"}
	}
	raw, err := c.do(ctx, prio, http.MethodPost, "/api/apps", nil, body)
	if err != nil {
		var ce *checkerError
		if errors.As(err, &ce) && ce.Status == http.StatusConflict {
			var conflict struct {
				Existing *checkerAppInfo `json:"existing_app"`
			}
			if json.Unmarshal(ce.Body, &conflict) == nil && conflict.Existing != nil && conflict.Existing.ID != "" {
				return conflict.Existing, true, nil
			}
		}
		return nil, false, err
	}
	var created struct {
		Data checkerAppInfo `json:"data"`
	}
	if json.Unmarshal(raw, &created) != nil || created.Data.ID == "" {
		return nil, false, &checkerError{Msg: "checker answer has no application id"}
	}
	return &created.Data, false, nil
}

// appByID calls GET /api/apps/{id} and returns the application.
func (c *checkerClient) appByID(ctx context.Context, prio checkerPrio, id string) (*checkerAppInfo, error) {
	raw, err := c.do(ctx, prio, http.MethodGet, "/api/apps/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return nil, err
	}
	var one struct {
		Data checkerAppInfo `json:"data"`
	}
	if json.Unmarshal(raw, &one) != nil || one.Data.ID == "" {
		return nil, &checkerError{Msg: "checker answer has no application"}
	}
	return &one.Data, nil
}

// checkerConsole is one console offered for the choice: the id the checker takes in
// console_developer_id and the name to show.
type checkerConsole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// consoles calls GET /api/consoles?per_page=200.
// The id of a console is its developer id, or its link when the console has only a name.
// A console with neither is left out.
func (c *checkerClient) consoles(ctx context.Context, prio checkerPrio) ([]checkerConsole, error) {
	raw, err := c.do(ctx, prio, http.MethodGet, "/api/consoles", url.Values{"per_page": {"200"}}, nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []struct {
			DeveloperID *string `json:"developer_id"`
			Name        *string `json:"name"`
			URL         string  `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, &checkerError{Status: http.StatusOK, Msg: checkerNotJSONMsg, NotJSON: true}
	}
	out := make([]checkerConsole, 0, len(list.Data))
	for _, d := range list.Data {
		id := ""
		if d.DeveloperID != nil {
			id = strings.TrimSpace(*d.DeveloperID)
		}
		if id == "" {
			id = strings.TrimSpace(d.URL)
		}
		if id == "" {
			continue
		}
		name := id
		if d.Name != nil && strings.TrimSpace(*d.Name) != "" {
			name = strings.TrimSpace(*d.Name)
		}
		out = append(out, checkerConsole{ID: id, Name: name})
	}
	return out, nil
}
