package admin

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// namecheapLiveURL and namecheapSandboxURL are the API endpoints used when the
	// Config does not inject another base URL. Tests always inject a local server.
	namecheapLiveURL     = "https://" + namecheapLiveHost + namecheapAPIPath
	namecheapSandboxURL  = "https://" + namecheapSandboxHost + namecheapAPIPath
	namecheapLiveHost    = "api.namecheap.com"
	namecheapSandboxHost = "api.sandbox.namecheap.com"
	namecheapAPIPath     = "/xml.response"

	// ncCodePriceUnknown is the ncError code of a price list that has no usable row
	// for the requested term. The purchase is refused before domains.create.
	ncCodePriceUnknown = "price_unknown"

	namecheapBodyLimit = 1 << 20
	namecheapMaxCheck  = 50
	namecheapTimeout   = 60 * time.Second
	namecheapDetectIP  = "0.0.0.0"
	namecheapErrLimit  = 300
	namecheapPriceTTL  = time.Hour
)

var (
	namecheapInvalidIPRe = regexp.MustCompile(`(?i)invalid request ip[:\s]+([0-9a-f:.]+)`)
	namecheapLabelRe     = `[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?`
	namecheapDomainRe    = regexp.MustCompile(`^` + namecheapLabelRe + `(?:\.` + namecheapLabelRe + `)+$`)
)

// namecheapHTTP is the HTTP client for every Namecheap call. It never follows a redirect.
var namecheapHTTP = &http.Client{
	Timeout:       namecheapTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// ncCreds are the identity fields sent with every Namecheap command.
type ncCreds struct {
	APIUser  string
	APIKey   string
	Username string
	ClientIP string
}

// ncError is a failed Namecheap call. Its text never contains the request URL or the API key.
// Definite is true when Namecheap answered with an error status, so the command was refused.
// Otherwise there is no usable answer and the outcome of a write is unknown. Timeout marks a deadline.
type ncError struct {
	Command    string
	Code       string
	Message    string
	Definite   bool
	Timeout    bool
	rawMessage string
}

// Error returns the command and the masked message.
func (e *ncError) Error() string {
	return "namecheap " + e.Command + ": " + e.Message
}

// ncClient talks to one Namecheap endpoint with one set of credentials.
type ncClient struct {
	baseURL string
	creds   ncCreds
	http    *http.Client
}

// newNCClient builds a client. baseURL is the endpoint, creds the account identity,
// hc the HTTP client; a nil hc uses the package client without redirects.
func newNCClient(baseURL string, creds ncCreds, hc *http.Client) *ncClient {
	if hc == nil {
		hc = namecheapHTTP
	}
	return &ncClient{baseURL: baseURL, creds: creds, http: hc}
}

// ncEnvelope is the XML answer of every command.
type ncEnvelope struct {
	XMLName         xml.Name     `xml:"ApiResponse"`
	Status          string       `xml:"Status,attr"`
	Errors          []ncXMLError `xml:"Errors>Error"`
	CommandResponse struct {
		Checks  []ncXMLCheck `xml:"DomainCheckResult"`
		Create  ncXMLCreate  `xml:"DomainCreateResult"`
		Info    ncXMLInfo    `xml:"DomainGetInfoResult"`
		Pricing ncXMLPricing `xml:"UserGetPricingResult"`
	} `xml:"CommandResponse"`
}

type ncXMLError struct {
	Number string `xml:"Number,attr"`
	Text   string `xml:",chardata"`
}

type ncXMLCheck struct {
	Domain       string `xml:"Domain,attr"`
	Available    string `xml:"Available,attr"`
	ErrorNo      string `xml:"ErrorNo,attr"`
	Description  string `xml:"Description,attr"`
	IsPremium    string `xml:"IsPremiumName,attr"`
	PremiumPrice string `xml:"PremiumRegistrationPrice,attr"`
}

type ncXMLCreate struct {
	Domain        string `xml:"Domain,attr"`
	Registered    string `xml:"Registered,attr"`
	ChargedAmount string `xml:"ChargedAmount,attr"`
	OrderID       string `xml:"OrderID,attr"`
}

type ncXMLInfo struct {
	Status     string `xml:"Status,attr"`
	DomainName string `xml:"DomainName,attr"`
	IsOwner    string `xml:"IsOwner,attr"`
}

type ncXMLPricing struct {
	Types []struct {
		Name       string `xml:"Name,attr"`
		Categories []struct {
			Name     string `xml:"Name,attr"`
			Products []struct {
				Name   string `xml:"Name,attr"`
				Prices []struct {
					Duration     string `xml:"Duration,attr"`
					DurationType string `xml:"DurationType,attr"`
					Price        string `xml:"Price,attr"`
					PricingType  string `xml:"PricingType,attr"`
					YourPrice    string `xml:"YourPrice,attr"`
					Additional   string `xml:"AdditionalCost,attr"`
					Currency     string `xml:"Currency,attr"`
				} `xml:"Price"`
			} `xml:"Product"`
		} `xml:"ProductCategory"`
	} `xml:"ProductType"`
}

// fail builds an ncError for command with a message masked of the account key and cut to a safe length.
func (c *ncClient) fail(command, code, msg string, definite, timeout bool) *ncError {
	masked := msg
	if c.creds.APIKey != "" {
		masked = strings.ReplaceAll(masked, c.creds.APIKey, "***")
	}
	if len(masked) > namecheapErrLimit {
		masked = masked[:namecheapErrLimit]
	}
	return &ncError{Command: command, Code: code, Message: masked, Definite: definite, Timeout: timeout, rawMessage: msg}
}

// call sends one command as a POST form and decodes the XML answer.
// command is the Namecheap method name, params the command fields.
// The body is read through io.LimitReader. It returns the decoded envelope.
// Errors are *ncError: a transport or HTTP failure is not Definite, an ERROR status is.
// Nothing is retried.
func (c *ncClient) call(ctx context.Context, command string, params map[string]string) (*ncEnvelope, error) {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	form.Set("ApiUser", c.creds.APIUser)
	form.Set("ApiKey", c.creds.APIKey)
	form.Set("UserName", c.creds.Username)
	form.Set("ClientIp", c.creds.ClientIP)
	form.Set("Command", command)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, c.fail(command, "", "request not built", false, false)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		var ne net.Error
		timeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
		if timeout {
			return nil, c.fail(command, "", "timeout", false, true)
		}
		return nil, c.fail(command, "", "request failed", false, false)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, namecheapBodyLimit+1))
	if err != nil {
		return nil, c.fail(command, "", "read failed", false, false)
	}
	if len(body) > namecheapBodyLimit {
		return nil, c.fail(command, "", "response too large", false, false)
	}
	if res.StatusCode != http.StatusOK {
		return nil, c.fail(command, "", "http "+strconv.Itoa(res.StatusCode), false, false)
	}
	var env ncEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, c.fail(command, "", "answer is not valid xml", false, false)
	}
	if !strings.EqualFold(strings.TrimSpace(env.Status), "OK") {
		return nil, c.errorFromEnvelope(command, env)
	}
	return &env, nil
}

// errorFromEnvelope turns an ERROR status into a Definite ncError.
// command is the method name; env holds the listed errors. The first error number becomes the code.
func (c *ncClient) errorFromEnvelope(command string, env ncEnvelope) *ncError {
	var parts []string
	code := ""
	for _, e := range env.Errors {
		if code == "" {
			code = strings.TrimSpace(e.Number)
		}
		if t := strings.TrimSpace(e.Text); t != "" {
			parts = append(parts, t)
		}
	}
	msg := strings.Join(parts, "; ")
	if msg == "" {
		msg = "error status without a message"
	}
	return c.fail(command, code, msg, true, false)
}

// ncAvailability is one row of domains.check.
type ncAvailability struct {
	Domain    string
	Available bool
	Premium   bool
	Err       string
}

// check asks whether up to namecheapMaxCheck domains can be registered.
// domains are lower-case names. It returns one row per answered domain.
// An error is a failed call or too many domains.
func (c *ncClient) check(ctx context.Context, domains []string) ([]ncAvailability, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	if len(domains) > namecheapMaxCheck {
		return nil, c.fail("domains.check", "", "too many domains", false, false)
	}
	env, err := c.call(ctx, "namecheap.domains.check", map[string]string{"DomainList": strings.Join(domains, ",")})
	if err != nil {
		return nil, err
	}
	out := make([]ncAvailability, 0, len(env.CommandResponse.Checks))
	for _, r := range env.CommandResponse.Checks {
		row := ncAvailability{
			Domain:    strings.ToLower(strings.TrimSpace(r.Domain)),
			Available: strings.EqualFold(r.Available, "true"),
			Premium:   strings.EqualFold(r.IsPremium, "true") || ncNumber(r.PremiumPrice) > 0,
		}
		if n := strings.TrimSpace(r.ErrorNo); n != "" && n != "0" {
			row.Available = false
			row.Err = strings.TrimSpace(r.Description)
		}
		out = append(out, row)
	}
	return out, nil
}

// price returns the total REGISTER price of a TLD for a term of years, in USD.
// tld is lower-case without a dot and years is 1 to 10. Only the row whose Duration equals
// years and whose DurationType is YEAR is used; the price is the larger of YourPrice and
// Price, so a lower account price never lowers the amount the operator is asked to cap, and
// CouponPrice is never read. Namecheap lists the price of one unit of the duration, so the
// price and AdditionalCost are multiplied by years unless the row says PricingType ABSOLUTE.
// A row in another currency is skipped. It returns an *ncError with code ncCodePriceUnknown
// when no row fits, and any other *ncError when the call fails.
func (c *ncClient) price(ctx context.Context, tld string, years int) (float64, error) {
	env, err := c.call(ctx, "namecheap.users.getPricing", map[string]string{
		"ProductType":     "DOMAIN",
		"ProductCategory": "DOMAINS",
		"ActionName":      "REGISTER",
		"ProductName":     strings.ToUpper(tld),
	})
	if err != nil {
		return 0, err
	}
	for _, t := range env.CommandResponse.Pricing.Types {
		if n := strings.ToUpper(t.Name); n != "" && n != "DOMAIN" && n != "DOMAINS" {
			continue
		}
		for _, cat := range t.Categories {
			if n := strings.ToUpper(cat.Name); n != "" && n != "REGISTER" && n != "DOMAINS" {
				continue
			}
			for _, p := range cat.Products {
				if !strings.EqualFold(p.Name, tld) {
					continue
				}
				for _, pr := range p.Prices {
					if strings.TrimSpace(pr.Duration) != strconv.Itoa(years) || (pr.DurationType != "" && !strings.EqualFold(pr.DurationType, "YEAR")) {
						continue
					}
					if cur := strings.TrimSpace(pr.Currency); cur != "" && !strings.EqualFold(cur, "USD") {
						continue
					}
					base := math.Max(ncNumber(pr.YourPrice), ncNumber(pr.Price))
					if base <= 0 {
						continue
					}
					total := base + ncNumber(pr.Additional)
					if !strings.EqualFold(strings.TrimSpace(pr.PricingType), "ABSOLUTE") {
						total *= float64(years)
					}
					return roundCents(total), nil
				}
			}
		}
	}
	return 0, c.fail("users.getPricing", ncCodePriceUnknown, "no price for "+strconv.Itoa(years)+" year(s) of "+tld, false, false)
}

// ncCreateResult is the answer of domains.create.
type ncCreateResult struct {
	Registered bool
	Charged    float64
	OrderID    string
}

// create registers a domain. domain is the full lower-case name, years 1 to 10,
// contacts the single contact used for all four contact types.
// It is called once per purchase and is never retried by this package.
// An error is an *ncError; the caller must reconcile with info before it decides the outcome.
func (c *ncClient) create(ctx context.Context, domain string, years int, contacts ncContacts) (ncCreateResult, error) {
	params := map[string]string{
		"DomainName":        domain,
		"Years":             strconv.Itoa(years),
		"AddFreeWhoisguard": "yes",
		"WGEnabled":         "yes",
	}
	for _, prefix := range []string{"Registrant", "Tech", "Admin", "AuxBilling"} {
		for k, v := range contacts.fields(prefix) {
			params[k] = v
		}
	}
	env, err := c.call(ctx, "namecheap.domains.create", params)
	if err != nil {
		return ncCreateResult{}, err
	}
	r := env.CommandResponse.Create
	return ncCreateResult{
		Registered: strings.EqualFold(r.Registered, "true"),
		Charged:    ncNumber(r.ChargedAmount),
		OrderID:    strings.TrimSpace(r.OrderID),
	}, nil
}

// setCustom points a domain to custom name servers.
// domain is the full name, servers the host names. It returns an error when the call fails.
func (c *ncClient) setCustom(ctx context.Context, domain string, servers []string) error {
	sld, tld, err := splitNCDomain(domain)
	if err != nil {
		return c.fail("domains.dns.setCustom", "", err.Error(), false, false)
	}
	_, err = c.call(ctx, "namecheap.domains.dns.setCustom", map[string]string{
		"SLD": sld, "TLD": tld, "Nameservers": strings.Join(servers, ","),
	})
	return err
}

// ncDomainInfo is the answer of domains.getInfo. Owned is true when the answer names the
// asked domain and says the account is its owner. Status is the registry status text
// (Ok, Locked, Expired) and does not decide ownership. Foreign is true only when the answer
// names the asked domain and says IsOwner is false; an empty or different name, or a missing
// flag, leaves both Owned and Foreign false because the answer is then ambiguous.
type ncDomainInfo struct {
	Owned   bool
	Foreign bool
	Status  string
}

// info asks whether the account owns domain.
// domain is the full lower-case name. It returns the ownership flag and the status.
// An error is a failed call; "domain not found" is an error answer too.
func (c *ncClient) info(ctx context.Context, domain string) (ncDomainInfo, error) {
	env, err := c.call(ctx, "namecheap.domains.getInfo", map[string]string{"DomainName": domain})
	if err != nil {
		return ncDomainInfo{}, err
	}
	r := env.CommandResponse.Info
	named := strings.EqualFold(strings.TrimSpace(r.DomainName), domain)
	return ncDomainInfo{
		Owned:   named && strings.EqualFold(strings.TrimSpace(r.IsOwner), "true"),
		Foreign: named && strings.EqualFold(strings.TrimSpace(r.IsOwner), "false"),
		Status:  strings.TrimSpace(r.Status),
	}, nil
}

// splitNCDomain splits a domain into its first label and the rest.
// It returns an error when there is no dot.
func splitNCDomain(domain string) (sld, tld string, err error) {
	sld, tld, ok := strings.Cut(strings.ToLower(strings.TrimSpace(domain)), ".")
	if !ok || sld == "" || tld == "" {
		return "", "", errors.New("invalid domain")
	}
	return sld, tld, nil
}

// detectOutboundIP reads the caller IP out of a Namecheap "Invalid request IP" error.
// err is the failure of a call made with a wrong client IP.
// It returns the address, or an empty string when err is not that error.
func detectOutboundIP(err error) string {
	var ne *ncError
	if !errors.As(err, &ne) {
		return ""
	}
	m := namecheapInvalidIPRe.FindStringSubmatch(ne.rawMessage)
	if m == nil {
		return ""
	}
	ip := strings.TrimRight(m[1], ".:")
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

// ncNumber parses a decimal attribute. An empty or invalid value is 0.
func ncNumber(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// roundCents rounds an amount to two decimals.
func roundCents(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// ncPriceEntry is one cached one-year price. The cache only feeds the availability
// screen; a purchase always asks Namecheap for a fresh price.
type ncPriceEntry struct {
	price float64
	at    time.Time
}

// ncPriceCache keeps one-year prices per endpoint and TLD for a limited time.
// The zero value is ready; now and ttl may be set by tests.
type ncPriceCache struct {
	mu    sync.Mutex
	items map[string]ncPriceEntry
	ttl   time.Duration
	now   func() time.Time
}

// clock returns the current time from the injected clock or the system clock.
func (p *ncPriceCache) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// get returns a fresh cached price for key.
func (p *ncPriceCache) get(key string) (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[key]
	ttl := p.ttl
	if ttl <= 0 {
		ttl = namecheapPriceTTL
	}
	if !ok || p.clock().Sub(e.at) > ttl {
		return 0, false
	}
	return e.price, true
}

// put stores a price for key.
func (p *ncPriceCache) put(key string, price float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.items == nil {
		p.items = map[string]ncPriceEntry{}
	}
	p.items[key] = ncPriceEntry{price: price, at: p.clock()}
}

// reset drops every cached price.
func (p *ncPriceCache) reset() {
	p.mu.Lock()
	p.items = nil
	p.mu.Unlock()
}

// registerPrice returns the one-year price of tld, from the cache when it is fresh.
// c is the client whose endpoint keys the cache. It returns an error when the call fails.
func (p *ncPriceCache) registerPrice(ctx context.Context, c *ncClient, tld string) (float64, error) {
	key := c.baseURL + "|" + tld
	if v, ok := p.get(key); ok {
		return v, nil
	}
	v, err := c.price(ctx, tld, 1)
	if err != nil {
		return 0, err
	}
	p.put(key, v)
	return v, nil
}

// validNCDomain reports whether name is a lower-case registrable domain: at least two
// labels of letters, digits and dashes, 253 characters at most.
func validNCDomain(name string) bool {
	return len(name) <= 253 && namecheapDomainRe.MatchString(name)
}
