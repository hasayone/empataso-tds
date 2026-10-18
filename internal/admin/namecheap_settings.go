package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	namecheapSettingsBody = 1 << 16
	namecheapNameMax      = 64
	namecheapFieldMax     = 128
	namecheapKeyMax       = 128
	namecheapProbeDomain  = "example.com"
)

var (
	namecheapUserRe    = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}$`)
	namecheapPhoneRe   = regexp.MustCompile(`^\+\d{1,3}\.\d{4,14}$`)
	namecheapCountryRe = regexp.MustCompile(`^[A-Z]{2}$`)
)

// ncContacts is the one contact used for the registrant, technical, administrative and billing roles.
// Phone uses the Namecheap format +NNN.NNNNNNNNNN. Country is a two-letter ISO code.
type ncContacts struct {
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Organization string `json:"organization,omitempty"`
	Address1     string `json:"address1"`
	Address2     string `json:"address2,omitempty"`
	City         string `json:"city"`
	State        string `json:"state"`
	PostalCode   string `json:"postal_code"`
	Country      string `json:"country"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
}

// fields returns the domains.create parameters of one contact role.
// prefix is Registrant, Tech, Admin or AuxBilling. Empty optional fields are left out.
func (c ncContacts) fields(prefix string) map[string]string {
	m := map[string]string{
		prefix + "FirstName":     c.FirstName,
		prefix + "LastName":      c.LastName,
		prefix + "Address1":      c.Address1,
		prefix + "City":          c.City,
		prefix + "StateProvince": c.State,
		prefix + "PostalCode":    c.PostalCode,
		prefix + "Country":       c.Country,
		prefix + "Phone":         c.Phone,
		prefix + "EmailAddress":  c.Email,
	}
	if c.Organization != "" {
		m[prefix+"OrganizationName"] = c.Organization
	}
	if c.Address2 != "" {
		m[prefix+"Address2"] = c.Address2
	}
	return m
}

// trimmed returns c with every field trimmed and the country upper-cased.
func (c ncContacts) trimmed() ncContacts {
	t := strings.TrimSpace
	return ncContacts{
		FirstName: t(c.FirstName), LastName: t(c.LastName), Organization: t(c.Organization),
		Address1: t(c.Address1), Address2: t(c.Address2), City: t(c.City), State: t(c.State),
		PostalCode: t(c.PostalCode), Country: strings.ToUpper(t(c.Country)),
		Phone: t(c.Phone), Email: t(c.Email),
	}
}

// validate checks a trimmed contact. It returns an empty string when the contact is usable,
// otherwise a client message naming the field.
func (c ncContacts) validate() string {
	required := []struct{ name, value string }{
		{"first_name", c.FirstName}, {"last_name", c.LastName}, {"address1", c.Address1},
		{"city", c.City}, {"state", c.State}, {"postal_code", c.PostalCode},
	}
	for _, f := range required {
		if f.value == "" {
			return "contacts: " + f.name + " required"
		}
	}
	for _, v := range []string{c.FirstName, c.LastName, c.Organization, c.Address1, c.Address2, c.City, c.State, c.PostalCode} {
		if utf8.RuneCountInString(v) > namecheapFieldMax || strings.ContainsAny(v, "\r\n\x00") {
			return "contacts: a field is too long or has control characters"
		}
	}
	if !namecheapCountryRe.MatchString(c.Country) {
		return "contacts: country must be a two-letter code"
	}
	if !namecheapPhoneRe.MatchString(c.Phone) {
		return "contacts: phone must look like +1.5551234567"
	}
	if addr, err := mail.ParseAddress(c.Email); err != nil || addr.Address != c.Email {
		return "contacts: email is not valid"
	}
	return ""
}

// ncAccount is the Namecheap account stored in namecheap.json.
// Contacts is nil until they are saved.
type ncAccount struct {
	Name     string      `json:"name"`
	APIUser  string      `json:"api_user"`
	Username string      `json:"username"`
	ClientIP string      `json:"client_ip"`
	APIKey   string      `json:"api_key"`
	Sandbox  bool        `json:"sandbox"`
	Contacts *ncContacts `json:"contacts,omitempty"`
}

// validate checks a stored or merged account the way PUT does.
// Contacts are checked after trimming. It returns an empty string when the account is
// usable, otherwise a client message naming the field.
func (a ncAccount) validate() string {
	if n := utf8.RuneCountInString(a.Name); n == 0 || n > namecheapNameMax || strings.ContainsAny(a.Name, "\r\n\x00") {
		return "name must be 1 to 64 characters"
	}
	if !namecheapUserRe.MatchString(a.APIUser) || !namecheapUserRe.MatchString(a.Username) {
		return "api_user and username must be 1 to 64 letters, digits or . _ @ + -"
	}
	if a.ClientIP != "" && net.ParseIP(a.ClientIP) == nil {
		return "client_ip is not an IP address"
	}
	if len(a.APIKey) > namecheapKeyMax || strings.ContainsAny(a.APIKey, "\r\n\x00") {
		return "api_key is not valid"
	}
	if a.APIKey == "" {
		return "api_key is required"
	}
	if a.Contacts != nil {
		return a.Contacts.trimmed().validate()
	}
	return ""
}

// ready reports whether the account can call the API: identity, key and a confirmed client IP.
func (a ncAccount) ready() bool {
	return a.APIUser != "" && a.Username != "" && a.APIKey != "" && a.ClientIP != ""
}

// creds returns the identity sent with each command.
func (a ncAccount) creds() ncCreds {
	return ncCreds{APIUser: a.APIUser, APIKey: a.APIKey, Username: a.Username, ClientIP: a.ClientIP}
}

// ncSettingsView is the body of GET and PUT /api/registrars/namecheap.
// The key and the contacts are never fields; HasKey and HasContacts only say they are set.
// Error is set on GET when namecheap.json exists but could not be used.
type ncSettingsView struct {
	Error       string `json:"error,omitempty"`
	Configured  bool   `json:"configured"`
	Name        string `json:"name"`
	APIUser     string `json:"api_user"`
	Username    string `json:"username"`
	ClientIP    string `json:"client_ip"`
	Sandbox     bool   `json:"sandbox"`
	HasKey      bool   `json:"has_key"`
	HasContacts bool   `json:"has_contacts"`
}

// ncSettingsPut is the body of PUT /api/registrars/namecheap.
// An empty APIKey or a mask keeps the stored key. A nil Contacts keeps the stored contacts.
type ncSettingsPut struct {
	Name     string      `json:"name"`
	APIUser  string      `json:"api_user"`
	Username string      `json:"username"`
	ClientIP string      `json:"client_ip"`
	APIKey   string      `json:"api_key"`
	Sandbox  bool        `json:"sandbox"`
	Contacts *ncContacts `json:"contacts"`
}

// ncBuyState is the set of domains being registered right now.
// It keeps two purchases of one name from running at the same time.
type ncBuyState struct {
	mu       sync.Mutex
	inflight map[string]struct{}
}

// acquire marks host as being registered. It returns false when it already is.
func (b *ncBuyState) acquire(host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.inflight[host]; ok {
		return false
	}
	if b.inflight == nil {
		b.inflight = map[string]struct{}{}
	}
	b.inflight[host] = struct{}{}
	return true
}

// release clears the mark set by acquire.
func (b *ncBuyState) release(host string) {
	b.mu.Lock()
	delete(b.inflight, host)
	b.mu.Unlock()
}

// has reports whether host is being registered right now.
func (b *ncBuyState) has(host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.inflight[host]
	return ok
}

// whenIdle runs fn while no registration is running. The lock is held during fn, so no
// registration can start before fn returns. It returns false without calling fn when
// a registration is running.
func (b *ncBuyState) whenIdle(fn func()) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.inflight) > 0 {
		return false
	}
	fn()
	return true
}

// namecheapAccount returns a copy of the stored account.
func (s *Server) namecheapAccount() ncAccount {
	s.ncMu.RLock()
	defer s.ncMu.RUnlock()
	a := s.ncAcct
	if a.Contacts != nil {
		c := *a.Contacts
		a.Contacts = &c
	}
	return a
}

// setNamecheapAccount replaces the stored account, clears the load error and drops cached prices.
func (s *Server) setNamecheapAccount(a ncAccount) {
	s.ncMu.Lock()
	s.ncAcct = a
	s.ncErr = ""
	s.ncMu.Unlock()
	s.ncPrices.reset()
}

// namecheapLoadError returns the reason namecheap.json was not used, or an empty string.
func (s *Server) namecheapLoadError() string {
	s.ncMu.RLock()
	defer s.ncMu.RUnlock()
	return s.ncErr
}

// loadNamecheap reads namecheap.json at start. A missing file leaves no account.
// A file that cannot be read, is not JSON or fails the checks of PUT leaves the registrar
// disabled: the reason is logged and shown by GET, and a PUT writes the file again.
// The key is never part of the reason.
func (s *Server) loadNamecheap() {
	if s.cfg.NamecheapFile == "" {
		return
	}
	raw, err := os.ReadFile(s.cfg.NamecheapFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	reason := ""
	var a ncAccount
	switch {
	case err != nil:
		reason = "namecheap.json cannot be read"
	case json.Unmarshal(raw, &a) != nil:
		reason = "namecheap.json is not valid JSON"
	default:
		if msg := a.validate(); msg != "" {
			reason = "namecheap.json is not valid: " + msg
		}
	}
	if reason != "" {
		s.ncMu.Lock()
		s.ncErr = reason
		s.ncMu.Unlock()
		s.logWarn("namecheap registrar disabled", "reason", reason)
		return
	}
	s.setNamecheapAccount(a)
}

// namecheapClient builds a client for acct. The sandbox flag of the account picks the
// endpoint. An empty injected URL falls back to the public Namecheap endpoint.
func (s *Server) namecheapClient(acct ncAccount) *ncClient {
	base := s.cfg.NamecheapURL
	if base == "" {
		base = namecheapLiveURL
	}
	if acct.Sandbox {
		base = s.cfg.NamecheapSandboxURL
		if base == "" {
			base = namecheapSandboxURL
		}
	}
	return newNCClient(base, acct.creds(), s.cfg.NamecheapHTTP)
}

// namecheapView builds the public view of an account.
func namecheapView(a ncAccount) ncSettingsView {
	return ncSettingsView{
		Configured: a.ready() && a.Contacts != nil,
		Name:       a.Name, APIUser: a.APIUser, Username: a.Username, ClientIP: a.ClientIP,
		Sandbox: a.Sandbox, HasKey: a.APIKey != "", HasContacts: a.Contacts != nil,
	}
}

// registerNamecheap mounts the registrar routes and the domain NS route.
// Settings, the connection test, registration and NS changes are cookie-session only.
// s is the server and mux receives the routes.
func registerNamecheap(s *Server, mux *http.ServeMux) {
	s.handle(mux, "GET /api/registrars/namecheap", cookieOnly(http.HandlerFunc(s.handleNamecheapGet)))
	s.handle(mux, "PUT /api/registrars/namecheap", cookieOnly(http.HandlerFunc(s.handleNamecheapPut)))
	s.handle(mux, "POST /api/registrars/namecheap/test", cookieOnly(http.HandlerFunc(s.handleNamecheapTest)))
	s.handle(mux, "POST /api/registrars/namecheap/check", http.HandlerFunc(s.handleNamecheapCheck))
	s.handle(mux, "POST /api/registrars/namecheap/register", cookieOnly(http.HandlerFunc(s.handleNamecheapRegister)))
	s.handle(mux, "POST /api/domains/{id}/ns", cookieOnly(http.HandlerFunc(s.handleDomainNS)))
}

// handleNamecheapGet returns the account without the key and the contacts.
// When namecheap.json was damaged at start the body carries its reason in "error".
func (s *Server) handleNamecheapGet(w http.ResponseWriter, r *http.Request) {
	v := namecheapView(s.namecheapAccount())
	v.Error = s.namecheapLoadError()
	writeJSON(w, http.StatusOK, v)
}

// handleNamecheapPut saves the account to namecheap.json with mode 0600.
// The check for a running registration and the write happen under one lock, so a
// purchase cannot start between them. A PUT also repairs a damaged file.
// Answers: 400 for bad JSON or a bad field, 503 when no file is configured,
// 409 while a registration runs, 500 "save error" when the file cannot be written.
// Success is 200 with the same body as GET. The key is never echoed or logged.
func (s *Server) handleNamecheapPut(w http.ResponseWriter, r *http.Request) {
	var body ncSettingsPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, namecheapSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.cfg.NamecheapFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "namecheap settings unavailable")
		return
	}
	var (
		view ncSettingsView
		code int
		msg  string
	)
	if !s.ncBuy.whenIdle(func() { view, code, msg = s.saveNamecheap(body) }) {
		writeErr(w, http.StatusConflict, "registration in progress")
		return
	}
	if code != http.StatusOK {
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// saveNamecheap merges body into the stored account and writes namecheap.json.
// The caller holds the registration lock. It returns the view and 200 on success,
// otherwise the zero view, 400 with the field message or 500 "save error".
func (s *Server) saveNamecheap(body ncSettingsPut) (ncSettingsView, int, string) {
	next, msg := mergeNamecheapAccount(s.namecheapAccount(), body)
	if msg != "" {
		return ncSettingsView{}, http.StatusBadRequest, msg
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return ncSettingsView{}, http.StatusInternalServerError, "save error"
	}
	if err := writeFileAtomic(s.cfg.NamecheapFile, append(raw, '\n'), 0o600); err != nil {
		return ncSettingsView{}, http.StatusInternalServerError, "save error"
	}
	s.setNamecheapAccount(next)
	s.logInfo("namecheap settings saved", "account", next.Name, "sandbox", next.Sandbox)
	return namecheapView(next), http.StatusOK, ""
}

// mergeNamecheapAccount applies a PUT body on top of the stored account.
// cur is the stored account, body the request. The key and the contacts are kept
// when the body leaves them out. It returns the account to store, or a client message.
func mergeNamecheapAccount(cur ncAccount, body ncSettingsPut) (ncAccount, string) {
	next := ncAccount{
		Name:     strings.TrimSpace(body.Name),
		APIUser:  strings.TrimSpace(body.APIUser),
		Username: strings.TrimSpace(body.Username),
		ClientIP: strings.TrimSpace(body.ClientIP),
		APIKey:   cur.APIKey,
		Sandbox:  body.Sandbox,
		Contacts: cur.Contacts,
	}
	if next.Username == "" {
		next.Username = next.APIUser
	}
	if !isKeitaroKeyMask(body.APIKey) {
		next.APIKey = strings.TrimSpace(body.APIKey)
	}
	if body.Contacts != nil {
		c := body.Contacts.trimmed()
		next.Contacts = &c
	}
	if msg := next.validate(); msg != "" {
		return ncAccount{}, msg
	}
	return next, ""
}

// ncTestReq is the body of POST /api/registrars/namecheap/test.
// Empty fields fall back to the stored account. Detect asks for the outgoing IP.
type ncTestReq struct {
	APIUser  string `json:"api_user"`
	Username string `json:"username"`
	ClientIP string `json:"client_ip"`
	APIKey   string `json:"api_key"`
	Sandbox  *bool  `json:"sandbox"`
	Detect   bool   `json:"detect"`
}

// ncTestView is the answer of the connection test. OK is true when Namecheap accepted the call.
// DetectedIP is the address Namecheap saw, read from its "Invalid request IP" error.
type ncTestView struct {
	OK         bool   `json:"ok"`
	DetectedIP string `json:"detected_ip,omitempty"`
	Error      string `json:"error,omitempty"`
}

// handleNamecheapTest checks the account against Namecheap and finds the outgoing IP.
// With detect true the call carries the client IP 0.0.0.0; Namecheap answers
// "Invalid request IP: x" and x is returned for the operator to confirm.
// Without detect the call uses the given or stored client IP.
// Nothing is saved. A refused call is still 200 with ok false; 400 means a field is missing.
func (s *Server) handleNamecheapTest(w http.ResponseWriter, r *http.Request) {
	var body ncTestReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, namecheapSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	acct := s.namecheapAccount()
	if v := strings.TrimSpace(body.APIUser); v != "" {
		acct.APIUser = v
	}
	if v := strings.TrimSpace(body.Username); v != "" {
		acct.Username = v
	}
	if acct.Username == "" {
		acct.Username = acct.APIUser
	}
	if v := strings.TrimSpace(body.ClientIP); v != "" {
		acct.ClientIP = v
	}
	if !isKeitaroKeyMask(body.APIKey) {
		acct.APIKey = strings.TrimSpace(body.APIKey)
	}
	if body.Sandbox != nil {
		acct.Sandbox = *body.Sandbox
	}
	if acct.APIUser == "" || acct.APIKey == "" {
		writeErr(w, http.StatusBadRequest, "api_user and api_key required")
		return
	}
	if body.Detect {
		acct.ClientIP = namecheapDetectIP
	} else if net.ParseIP(acct.ClientIP) == nil {
		writeErr(w, http.StatusBadRequest, "client_ip required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), namecheapTimeout)
	defer cancel()
	_, err := s.namecheapClient(acct).check(ctx, []string{namecheapProbeDomain})
	if err == nil {
		writeJSON(w, http.StatusOK, ncTestView{OK: true})
		return
	}
	writeJSON(w, http.StatusOK, ncTestView{DetectedIP: detectOutboundIP(err), Error: err.Error()})
}

// ncCheckReq is the body of POST /api/registrars/namecheap/check.
type ncCheckReq struct {
	Domains []string `json:"domains"`
}

// ncCheckRow is one answer of the availability check. Price is the one-year price in USD
// and is null when it could not be read. Error explains an unavailable name or a missing price.
type ncCheckRow struct {
	Domain    string   `json:"domain"`
	Available bool     `json:"available"`
	Premium   bool     `json:"premium"`
	Price     *float64 `json:"price"`
	Currency  string   `json:"currency"`
	Error     string   `json:"error,omitempty"`
}

// handleNamecheapCheck tells whether domains can be registered and what a year costs.
// The body is {"domains": [...]} with 1 to 50 names. Answers: 400 for a bad list,
// 503 when the account is not configured, 502 when Namecheap fails.
// Success is {"results": [...]}. Prices come from the cache while it is fresh.
func (s *Server) handleNamecheapCheck(w http.ResponseWriter, r *http.Request) {
	var body ncCheckReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, namecheapSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	names, msg := normalizeNCDomains(body.Domains)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	acct := s.namecheapAccount()
	if !acct.ready() {
		writeErr(w, http.StatusServiceUnavailable, "namecheap not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), namecheapTimeout)
	defer cancel()
	client := s.namecheapClient(acct)
	rows, err := client.check(ctx, names)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": s.priceRows(ctx, client, rows)})
}

// priceRows turns availability rows into check rows and attaches one-year prices.
// Only available, non-premium names are priced. A price failure leaves Price nil
// and fills Error. One call is made per distinct TLD.
func (s *Server) priceRows(ctx context.Context, client *ncClient, rows []ncAvailability) []ncCheckRow {
	out := make([]ncCheckRow, 0, len(rows))
	for _, r := range rows {
		row := ncCheckRow{Domain: r.Domain, Available: r.Available, Premium: r.Premium, Currency: "USD", Error: r.Err}
		if r.Available && !r.Premium {
			if _, tld, err := splitNCDomain(r.Domain); err == nil {
				if p, err := s.ncPrices.registerPrice(ctx, client, tld); err == nil {
					row.Price = &p
				} else {
					row.Error = err.Error()
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// normalizeNCDomains lower-cases, validates and de-duplicates a list for domains.check.
// It returns the names or a client message for an empty list, more than 50 names or a bad name.
func normalizeNCDomains(in []string) ([]string, string) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		if !validNCDomain(d) {
			return nil, "invalid domain: " + truncateText(raw, 64)
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, "domains required"
	}
	if len(out) > namecheapMaxCheck {
		return nil, "at most 50 domains"
	}
	return out, ""
}

// truncateText cuts s to at most n bytes on a rune boundary.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
