package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	cloudflareSettingsBody = 1 << 16
	cloudflareTestTimeout  = 60 * time.Second
)

var (
	cloudflareAccountRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	cloudflareTokenRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

// cfAccount is the Cloudflare account stored in cloudflare.json.
type cfAccount struct {
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
}

// validate checks a stored or merged account the way PUT does.
// It returns an empty string when the account is usable, otherwise a client message naming
// the field. An empty field is allowed: the account is then not configured.
func (a cfAccount) validate() string {
	if a.AccountID != "" && !cloudflareAccountRe.MatchString(a.AccountID) {
		return "account_id must be 32 hex characters"
	}
	if a.Token != "" && !cloudflareTokenRe.MatchString(a.Token) {
		return "token is not valid"
	}
	return ""
}

// ready reports whether the account can call the API: a token and an account id.
func (a cfAccount) ready() bool {
	return a.Token != "" && a.AccountID != ""
}

// cfSettingsView is the body of GET and PUT /api/cloudflare.
// The token is never a field; HasToken only says it is set. Configured means the token and
// the account are both set. PublicIP is Config.PublicIP, empty when the server has none.
// Error is set on GET when cloudflare.json exists but could not be used.
type cfSettingsView struct {
	Error      string `json:"error,omitempty"`
	AccountID  string `json:"account_id"`
	HasToken   bool   `json:"has_token"`
	Configured bool   `json:"configured"`
	PublicIP   string `json:"public_ip"`
}

// cfSettingsPut is the body of PUT /api/cloudflare. An empty or masked Token keeps the stored
// token and an empty AccountID keeps the stored account.
type cfSettingsPut struct {
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
}

// cfTestView is the answer of the connection test. Zones is how many zones the account has.
type cfTestView struct {
	OK    bool   `json:"ok"`
	Zones *int   `json:"zones,omitempty"`
	Error string `json:"error,omitempty"`
}

// cloudflareAccount returns a copy of the stored account.
func (s *Server) cloudflareAccount() cfAccount {
	s.cfMu.RLock()
	defer s.cfMu.RUnlock()
	return s.cfAcct
}

// cloudflareLoadError returns the reason cloudflare.json was not used, or an empty string.
func (s *Server) cloudflareLoadError() string {
	s.cfMu.RLock()
	defer s.cfMu.RUnlock()
	return s.cfErr
}

// loadCloudflare reads cloudflare.json at start. A missing file leaves no account.
// A file that cannot be read, is not JSON or fails the checks of PUT leaves Cloudflare
// disabled: the reason is logged and shown by GET, and a PUT writes the file again.
// The token is never part of the reason.
func (s *Server) loadCloudflare() {
	if s.cfg.CloudflareFile == "" {
		return
	}
	raw, err := os.ReadFile(s.cfg.CloudflareFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	reason := ""
	var a cfAccount
	switch {
	case err != nil:
		reason = "cloudflare.json cannot be read"
	case json.Unmarshal(raw, &a) != nil:
		reason = "cloudflare.json is not valid JSON"
	default:
		if msg := a.validate(); msg != "" {
			reason = "cloudflare.json is not valid: " + msg
		}
	}
	s.cfMu.Lock()
	defer s.cfMu.Unlock()
	if reason != "" {
		s.cfErr = reason
		s.logWarn("cloudflare disabled", "reason", reason)
		return
	}
	s.cfAcct = a
}

// cloudflareClient builds a client for acct. The injected URL replaces the public endpoint.
func (s *Server) cloudflareClient(acct cfAccount) *cfClient {
	base := s.cfg.CloudflareURL
	if base == "" {
		base = cloudflareBaseURL
	}
	return newCFClient(base, acct, s.cfg.CloudflareHTTP)
}

// cloudflareView builds the public view of an account.
func (s *Server) cloudflareView(a cfAccount) cfSettingsView {
	return cfSettingsView{
		AccountID: a.AccountID, HasToken: a.Token != "", Configured: a.ready(), PublicIP: s.cfg.PublicIP,
	}
}

// registerCloudflare mounts the Cloudflare settings routes and the domain connection routes.
// Settings, the test, the connection and the notice acknowledgement are cookie-session only.
// s is the server and mux receives the routes.
func registerCloudflare(s *Server, mux *http.ServeMux) {
	s.handle(mux, "GET /api/cloudflare", cookieOnly(http.HandlerFunc(s.handleCloudflareGet)))
	s.handle(mux, "PUT /api/cloudflare", cookieOnly(http.HandlerFunc(s.handleCloudflarePut)))
	s.handle(mux, "POST /api/cloudflare/test", cookieOnly(http.HandlerFunc(s.handleCloudflareTest)))
	s.handle(mux, "POST /api/domains/{id}/cloudflare", cookieOnly(http.HandlerFunc(s.handleDomainCloudflare)))
	s.handle(mux, "POST /api/domains/{id}/admin-dns", cookieOnly(http.HandlerFunc(s.handleDomainAdminDNS)))
	s.handle(mux, "GET /api/domains/connected", http.HandlerFunc(s.handleDomainsConnected))
	s.handle(mux, "POST /api/domains/connected/ack", cookieOnly(http.HandlerFunc(s.handleDomainsConnectedAck)))
}

// handleCloudflareGet returns the account without the token.
// When cloudflare.json was damaged at start the body carries its reason in "error".
func (s *Server) handleCloudflareGet(w http.ResponseWriter, r *http.Request) {
	v := s.cloudflareView(s.cloudflareAccount())
	v.Error = s.cloudflareLoadError()
	writeJSON(w, http.StatusOK, v)
}

// handleCloudflarePut saves the account to cloudflare.json with mode 0600.
// The merge and the write happen under one lock, so two saves cannot interleave. A PUT also
// repairs a damaged file. Answers: 400 for bad JSON or a bad field, 503 when no file is
// configured, 500 "save error" when the file cannot be written. Success is 200 with the same
// body as GET. The token is never echoed or logged.
func (s *Server) handleCloudflarePut(w http.ResponseWriter, r *http.Request) {
	var body cfSettingsPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, cloudflareSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.cfg.CloudflareFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "cloudflare settings unavailable")
		return
	}
	s.cfMu.Lock()
	defer s.cfMu.Unlock()
	next := mergeCloudflareAccount(s.cfAcct, body)
	if msg := next.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	raw, err := json.Marshal(next)
	if err == nil {
		err = writeFileAtomic(s.cfg.CloudflareFile, append(raw, '\n'), 0o600)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	s.cfAcct, s.cfErr = next, ""
	s.logInfo("cloudflare settings saved")
	writeJSON(w, http.StatusOK, s.cloudflareView(next))
}

// mergeCloudflareAccount applies a PUT body on top of the stored account.
// cur is the stored account. An empty or masked token and an empty account id keep the stored
// values. It returns the account to store; the caller validates it.
func mergeCloudflareAccount(cur cfAccount, body cfSettingsPut) cfAccount {
	next := cur
	if !isKeitaroKeyMask(body.Token) {
		next.Token = strings.TrimSpace(body.Token)
	}
	if id := strings.TrimSpace(body.AccountID); id != "" {
		next.AccountID = strings.ToLower(id)
	}
	return next
}

// handleCloudflareTest checks the token against Cloudflare and counts the account zones.
// The body is {"token", "account_id"}; empty fields fall back to the stored account.
// Nothing is saved. A refused call is still 200 with ok false; 400 means a field is missing
// or malformed. Success is {"ok": true, "zones": n}.
func (s *Server) handleCloudflareTest(w http.ResponseWriter, r *http.Request) {
	var body cfSettingsPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, cloudflareSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	acct := mergeCloudflareAccount(s.cloudflareAccount(), body)
	if msg := acct.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if !acct.ready() {
		writeErr(w, http.StatusBadRequest, "token and account_id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cloudflareTestTimeout)
	defer cancel()
	client := s.cloudflareClient(acct)
	err := client.verify(ctx)
	zones := 0
	if err == nil {
		zones, err = client.listZonesCount(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, cfTestView{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cfTestView{OK: true, Zones: &zones})
}
