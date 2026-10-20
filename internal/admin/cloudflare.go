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

const (
	// cloudflareBaseURL is the API address used when the Config does not inject another one.
	cloudflareBaseURL = "https://api.cloudflare.com/client/v4"

	cloudflareTimeout   = 30 * time.Second
	cloudflareBodyLimit = 4 << 20
	cloudflareErrLimit  = 300
	// cloudflareMinPage is the smallest per_page the zone list accepts.
	cloudflareMinPage = 5
	// cloudflareCodeExists is the error code of a zone that already exists.
	cloudflareCodeExists = 1061
	// cloudflareCodeBadToken is the error code of a token the endpoint does not know.
	cloudflareCodeBadToken = 1000
)

// Cloudflare status values of config.Domain.CfStatus.
const (
	// cfStatusConnectPending marks a domain the panel still has to connect to Cloudflare.
	cfStatusConnectPending = "connect_pending"
	// cfStatusPending marks a zone that waits for the name servers to be changed.
	cfStatusPending = "pending"
	// cfStatusActive marks a zone that Cloudflare serves.
	cfStatusActive = "active"
)

// errCFApexCNAME is the refusal to change an apex that is a CNAME record.
var errCFApexCNAME = errors.New("apex has a CNAME record, not changing it")

// errCFApexMany is the refusal to choose between several address records of the apex.
var errCFApexMany = errors.New("apex has more than one address record, not changing them")

// cloudflareHTTP is the HTTP client for every Cloudflare call. It never follows a redirect.
var cloudflareHTTP = &http.Client{
	Timeout:       cloudflareTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// cfError is a failed Cloudflare call. Its text comes from the errors Cloudflare listed and
// never contains the token or the Authorization header.
// Status is the HTTP status, 0 when there was no answer. Code is the first Cloudflare error code.
type cfError struct {
	Status  int
	Code    int
	Message string
}

// Error returns the masked message.
func (e *cfError) Error() string {
	return "cloudflare: " + e.Message
}

// isCFNotFound reports whether err is a Cloudflare answer saying the object does not exist.
func isCFNotFound(err error) bool {
	var ce *cfError
	return errors.As(err, &ce) && ce.Status == http.StatusNotFound
}

// cfAPIError is one entry of the errors list of an answer.
type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// cfEnvelope is the JSON answer of every call.
type cfEnvelope struct {
	Success    bool            `json:"success"`
	Errors     []cfAPIError    `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
}

// cfZone is a Cloudflare zone.
type cfZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	NameServers []string `json:"name_servers"`
}

// cfRecord is a DNS record as Cloudflare returns it.
type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// cfRecordWrite is the body that creates or changes a DNS record. A proxied record uses
// the automatic TTL, which Cloudflare writes as 1.
type cfRecordWrite struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// cfClient talks to one Cloudflare endpoint with one token and account.
type cfClient struct {
	base    string
	token   string
	account string
	http    *http.Client
}

// newCFClient builds a client. base is the API address, creds the token and account,
// hc the HTTP client; a nil hc uses the package client without redirects.
func newCFClient(base string, creds cfAccount, hc *http.Client) *cfClient {
	if hc == nil {
		hc = cloudflareHTTP
	}
	return &cfClient{base: strings.TrimRight(base, "/"), token: creds.Token, account: creds.AccountID, http: hc}
}

// fail builds a cfError with a message masked of the token and cut to a safe length.
func (c *cfClient) fail(status, code int, msg string) *cfError {
	if c.token != "" {
		msg = strings.ReplaceAll(msg, c.token, "***")
	}
	if len(msg) > cloudflareErrLimit {
		msg = truncateText(msg, cloudflareErrLimit)
	}
	return &cfError{Status: status, Code: code, Message: msg}
}

// do sends one request and decodes the envelope.
// method and path select the call, query holds the URL parameters and in the JSON body; both
// may be empty. The body is read through io.LimitReader. A transport failure, an HTTP status
// of 300 or more, an answer that is not JSON and success false are all *cfError. Nothing is retried.
func (c *cfClient) do(ctx context.Context, method, path string, query url.Values, in any) (*cfEnvelope, error) {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, c.fail(0, 0, "request not built")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, c.fail(0, 0, "request not built")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return nil, c.fail(0, 0, "timeout")
		}
		return nil, c.fail(0, 0, "request failed")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, cloudflareBodyLimit+1))
	if err != nil {
		return nil, c.fail(res.StatusCode, 0, "read failed")
	}
	if len(raw) > cloudflareBodyLimit {
		return nil, c.fail(res.StatusCode, 0, "response too large")
	}
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, c.fail(res.StatusCode, 0, "answer is not valid json, http "+strconv.Itoa(res.StatusCode))
	}
	if res.StatusCode >= http.StatusMultipleChoices || !env.Success {
		return nil, c.errorFromEnvelope(res.StatusCode, env)
	}
	return &env, nil
}

// errorFromEnvelope turns a failed answer into a cfError.
// status is the HTTP status and env the decoded answer. The messages of the listed errors form
// the text and the first code becomes the code; without a message the text names the status.
func (c *cfClient) errorFromEnvelope(status int, env cfEnvelope) *cfError {
	var parts []string
	code := 0
	for _, e := range env.Errors {
		if code == 0 {
			code = e.Code
		}
		if m := strings.TrimSpace(e.Message); m != "" {
			parts = append(parts, m)
		}
	}
	msg := strings.Join(parts, "; ")
	if msg == "" {
		msg = "http " + strconv.Itoa(status)
	}
	return c.fail(status, code, msg)
}

// decode reads the result of an answer into out. It returns a cfError when the result does
// not fit out.
func (c *cfClient) decode(env *cfEnvelope, out any) error {
	if err := json.Unmarshal(env.Result, out); err != nil {
		return c.fail(0, 0, "result is not valid json")
	}
	return nil
}

// verify checks that the token is active.
// It asks the user endpoint first. When that refuses the token, which is what an account token
// gets, and an account id is known, it asks the account endpoint and returns that answer.
// An error is a failed call or a token whose status is not active.
func (c *cfClient) verify(ctx context.Context) error {
	err := c.verifyAt(ctx, "/user/tokens/verify")
	var ce *cfError
	if err == nil || c.account == "" || !errors.As(err, &ce) ||
		(ce.Status != http.StatusUnauthorized && ce.Status != http.StatusForbidden && ce.Code != cloudflareCodeBadToken) {
		return err
	}
	return c.verifyAt(ctx, "/accounts/"+url.PathEscape(c.account)+"/tokens/verify")
}

// verifyAt asks one token verification endpoint. path is the endpoint.
// It returns an error when the call fails or the reported status is not active.
func (c *cfClient) verifyAt(ctx context.Context, path string) error {
	env, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := c.decode(env, &res); err != nil {
		return err
	}
	if res.Status != "" && res.Status != "active" {
		return c.fail(0, 0, "token is "+res.Status)
	}
	return nil
}

// listZonesCount returns how many zones the account has.
// An error is a failed call.
func (c *cfClient) listZonesCount(ctx context.Context) (int, error) {
	env, err := c.do(ctx, http.MethodGet, "/zones", url.Values{
		"account.id": {c.account}, "per_page": {strconv.Itoa(cloudflareMinPage)},
	}, nil)
	if err != nil {
		return 0, err
	}
	return env.ResultInfo.TotalCount, nil
}

// findZone returns the zone of the account whose name is name, or nil when there is none.
// name is a lower-case domain. An error is a failed call.
func (c *cfClient) findZone(ctx context.Context, name string) (*cfZone, error) {
	env, err := c.do(ctx, http.MethodGet, "/zones", url.Values{"name": {name}, "account.id": {c.account}}, nil)
	if err != nil {
		return nil, err
	}
	var zones []cfZone
	if err := c.decode(env, &zones); err != nil {
		return nil, err
	}
	for i := range zones {
		if strings.EqualFold(zones[i].Name, name) {
			return &zones[i], nil
		}
	}
	return nil, nil
}

// ensureZone returns the zone for name and creates it when the account has none.
// name is a lower-case domain. A zone created by another request at the same moment is found
// again. An error is a failed call.
func (c *cfClient) ensureZone(ctx context.Context, name string) (*cfZone, error) {
	z, err := c.findZone(ctx, name)
	if err != nil || z != nil {
		return z, err
	}
	env, err := c.do(ctx, http.MethodPost, "/zones", nil, map[string]any{
		"name": name, "account": map[string]string{"id": c.account}, "type": "full", "jump_start": false,
	})
	if err != nil {
		var ce *cfError
		if errors.As(err, &ce) && ce.Code == cloudflareCodeExists {
			if z, ferr := c.findZone(ctx, name); ferr == nil && z != nil {
				return z, nil
			}
		}
		return nil, err
	}
	var created cfZone
	if err := c.decode(env, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// zone reads one zone by id. A vanished zone is an error that isCFNotFound recognises.
func (c *cfClient) zone(ctx context.Context, id string) (*cfZone, error) {
	env, err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return nil, err
	}
	var z cfZone
	if err := c.decode(env, &z); err != nil {
		return nil, err
	}
	return &z, nil
}

// addressRecordType returns the DNS record type for an address: A for IPv4, AAAA for IPv6.
// ip is the address text.
func addressRecordType(ip string) string {
	if p := net.ParseIP(ip); p != nil && p.To4() == nil {
		return "AAAA"
	}
	return "A"
}

// upsertApexA makes the host resolve to ip through a proxied address record.
// zoneID is the zone, host the apex name and ip an IPv4 address, or IPv6 for an AAAA record.
// A CNAME on the host is refused with errCFApexCNAME and more than one record of the address
// type with errCFApexMany; neither is changed. A record with the same address that is already
// proxied is left alone. An existing record is changed to ip with proxying on and the automatic
// TTL, and a missing one is created. An error is a refusal or a failed call.
func (c *cfClient) upsertApexA(ctx context.Context, zoneID, host, ip string) error {
	kind := addressRecordType(ip)
	base := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	env, err := c.do(ctx, http.MethodGet, base, url.Values{"name": {host}, "per_page": {"100"}}, nil)
	if err != nil {
		return err
	}
	var records []cfRecord
	if err := c.decode(env, &records); err != nil {
		return err
	}
	var same []cfRecord
	for _, r := range records {
		if !strings.EqualFold(r.Name, host) {
			continue
		}
		switch strings.ToUpper(r.Type) {
		case "CNAME":
			return errCFApexCNAME
		case kind:
			same = append(same, r)
		}
	}
	want := cfRecordWrite{Type: kind, Name: host, Content: ip, Proxied: true, TTL: 1}
	switch len(same) {
	case 0:
		_, err = c.do(ctx, http.MethodPost, base, nil, want)
	case 1:
		if same[0].Proxied && net.ParseIP(same[0].Content).Equal(net.ParseIP(ip)) {
			return nil
		}
		_, err = c.do(ctx, http.MethodPatch, base+"/"+url.PathEscape(same[0].ID), nil, want)
	default:
		return errCFApexMany
	}
	return err
}

// setSetting changes one zone setting. zoneID is the zone, name the setting such as ssl or
// always_use_https and value its new value. An error is a failed call.
func (c *cfClient) setSetting(ctx context.Context, zoneID, name, value string) error {
	_, err := c.do(ctx, http.MethodPatch, "/zones/"+url.PathEscape(zoneID)+"/settings/"+url.PathEscape(name), nil,
		map[string]string{"value": value})
	return err
}

// activationCheck asks Cloudflare to look at the name servers of a pending zone now.
// zoneID is the zone. Cloudflare limits how often it may be asked, so a caller treats an
// error as harmless.
func (c *cfClient) activationCheck(ctx context.Context, zoneID string) error {
	_, err := c.do(ctx, http.MethodPut, "/zones/"+url.PathEscape(zoneID)+"/activation_check", nil, nil)
	return err
}
