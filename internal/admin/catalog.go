package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// JSONStore reads and writes a JSON array. Save replaces the whole file.
// mu serializes read-modify-write cycles of every writer that takes lock.
type JSONStore[T any] struct {
	path string
	mu   sync.Mutex
}

// lock takes the write lock of the file and returns the function that releases it.
// A nil store has nothing to lock. List and Save do not take the lock themselves,
// so a writer that holds it may call them.
func (s *JSONStore[T]) lock() func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

// NewJSONStore returns a store for path. The file is created on the first save.
func NewJSONStore[T any](path string) *JSONStore[T] { return &JSONStore[T]{path: path} }

// List returns the stored records. A missing file is an empty list.
func (s *JSONStore[T]) List(context.Context) ([]T, error) {
	if s == nil || s.path == "" {
		return nil, errors.New("store unavailable")
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []T{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return []T{}, nil
	}
	var list []T
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []T{}
	}
	return list, nil
}

// Save writes the records atomically.
func (s *JSONStore[T]) Save(_ context.Context, list []T) error {
	if s == nil || s.path == "" {
		return errors.New("store unavailable")
	}
	if list == nil {
		list = []T{}
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(s.path, b, 0o644)
}

var catalogIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var offerCountryCodeRe = regexp.MustCompile(`^[a-zA-Z]{2}$`)
var offerCurrencyCodeRe = regexp.MustCompile(`^[a-zA-Z]{3}$`)

func validCatalogID(id string) string {
	if !catalogIDRe.MatchString(id) {
		return "id must be 1-64 letters, digits, dot, underscore or dash"
	}
	return ""
}

// validOfferDestination checks the id and, unless a ready action answers the click, the URL.
// A ready action (404, text or html with a body, none) may omit the URL. A URL that is
// present still has to be http or https. o is the offer. It returns an empty string when
// valid, otherwise a client error message.
func validOfferDestination(o config.Offer) string {
	if o.ActionReady() {
		if msg := validCatalogID(o.ID); msg != "" {
			return msg
		}
		if o.Local || strings.TrimSpace(o.URL) == "" {
			return ""
		}
		return validDestination(o.ID, o.URL, false)
	}
	return validDestination(o.ID, o.URL, o.Local)
}

// validOfferAction checks the action token and its body.
// Empty means a URL redirect. text and html need a non-empty body, so an empty page
// is not stored as that action. An unknown token is refused. o is the offer.
// It returns an empty string when valid, otherwise a client error message.
func validOfferAction(o config.Offer) string {
	switch strings.ToLower(strings.TrimSpace(o.Action)) {
	case "", config.OfferAction404, config.OfferActionText, config.OfferActionHTML, config.OfferActionNone:
	default:
		return "action is not valid"
	}
	if len(o.ActionBody) > config.MaxOfferActionBody {
		return "action_body is too long"
	}
	switch strings.ToLower(strings.TrimSpace(o.Action)) {
	case config.OfferActionText, config.OfferActionHTML:
		if strings.TrimSpace(o.ActionBody) == "" {
			return "action_body required"
		}
	}
	return ""
}

func validDestination(id, raw string, local bool) string {
	if msg := validCatalogID(id); msg != "" {
		return msg
	}
	if local {
		return ""
	}
	u := strings.TrimSpace(raw)
	if u == "" {
		return "url required"
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return "url must be http or https"
	}
	return ""
}

func validSource(src config.TrafficSource) string {
	if msg := validCatalogID(src.ID); msg != "" {
		return msg
	}
	for name := range src.Parameters {
		if !config.AllowedSourceParam(name) {
			return "unknown parameter " + name
		}
	}
	return ""
}

// validPostback checks the method and the URL of one outbound template.
// p is the template. An empty status list is accepted: the engine sends it on every status.
// It returns a client message, or an empty string when the template can be stored.
func validPostback(p config.OutboundPostback) string {
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return "method must be GET or POST"
	}
	u := strings.TrimSpace(p.URL)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return "url must be http or https"
	}
	return ""
}

// trimGroupLabel trims spaces and limits a catalog group label to maxFolderRunes runes.
// An all-space value becomes empty.
func trimGroupLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxFolderRunes {
		return s
	}
	return string([]rune(s)[:maxFolderRunes])
}

// prepOffer trims the group label, the value names and the limits of an offer.
// A value with an empty name is dropped, because it has no macro to stand for.
// A limit that is off keeps its number, so turning it back on restores the saved
// count, and drops the old per-limit hand-over offer: the alternative is FallbackID.
func prepOffer(o *config.Offer) {
	o.Group = trimGroupLabel(o.Group)
	o.Action = strings.ToLower(strings.TrimSpace(o.Action))
	var values []config.OfferValue
	for _, v := range o.Values {
		v.Name = strings.TrimSpace(v.Name)
		if v.Name != "" {
			values = append(values, v)
		}
	}
	o.Values = values
	o.FallbackID = strings.TrimSpace(o.FallbackID)
	o.CapTZ = strings.TrimSpace(o.CapTZ)
	o.ConvCapOfferID = strings.TrimSpace(o.ConvCapOfferID)
	o.ConvTotalOfferID = strings.TrimSpace(o.ConvTotalOfferID)
	if !o.ConvCap {
		o.ConvCapOfferID = ""
	}
	if !o.ConvTotal {
		o.ConvTotalOfferID = ""
	}
}

// relinkOfferFallbacks points the spare offer and the limit hand-over offers of each copied offer at
// the copy of that offer.
// list is the offer list that already holds the copies. copied maps an original offer id to the id of
// its copy. A copy whose target was not copied in the same request gets none, so a copy never sends
// clicks to a record of another section.
func relinkOfferFallbacks(list []config.Offer, copied map[string]string) {
	isCopy := make(map[string]bool, len(copied))
	for _, id := range copied {
		isCopy[id] = true
	}
	for i := range list {
		if !isCopy[list[i].ID] {
			continue
		}
		list[i].FallbackID = copied[list[i].FallbackID]
		list[i].ConvCapOfferID = copied[list[i].ConvCapOfferID]
		list[i].ConvTotalOfferID = copied[list[i].ConvTotalOfferID]
	}
}

func prepLanding(o *config.Landing) {
	o.Group = trimGroupLabel(o.Group)
}

func prepNetwork(n *config.AffiliateNetwork) {
	n.Group = trimGroupLabel(n.Group)
}

func prepSource(src *config.TrafficSource) {
	src.Group = trimGroupLabel(src.Group)
}

func registerOffers(s *Server, mux *http.ServeMux) {
	mountCRUD(s, mux, "/api/offers", s.offerCatalog())
}

// offerCatalog returns the handlers' state of the offer catalog.
// Registration and restoreCatalogByRes build it the same way, so both see one set of hooks.
func (s *Server) offerCatalog() crudCatalog[config.Offer] {
	return crudCatalog[config.Offer]{s: s, store: s.cfg.Offers,
		idOf:  func(o config.Offer) string { return o.ID },
		setID: func(o *config.Offer, id string) { o.ID = id },
		valid: validOffer,
		hooks: &catalogHooks[config.Offer]{
			seq: s.cfg.SeqOffers, floor: s.offerFloor, prep: prepOffer,
			res:       resOffers,
			label:     func(o config.Offer) string { return o.Group },
			owner:     func(o config.Offer) string { return o.CreatedBy },
			setOwner:  func(o *config.Offer, uid string) { o.CreatedBy = uid },
			boardID:   func(o config.Offer) string { return o.BoardID },
			setBoard:  func(o *config.Offer, id string) { o.BoardID = id },
			userCheck: s.offerCheck,
			maskOut:   maskOfferPayout,
			keepIn:    keepOfferPayout,

			restoreCheck: s.offerRestoreCheck,
		},
	}
}

func validOffer(o config.Offer) string {
	if msg := validOfferDestination(o); msg != "" {
		return msg
	}
	if msg := validOfferAction(o); msg != "" {
		return msg
	}
	if o.NetworkID != "" {
		if msg := validCatalogID(o.NetworkID); msg != "" {
			return "network_id: " + msg
		}
	}
	if msg := validOfferCountries(o.Countries); msg != "" {
		return msg
	}
	if msg := validOfferPayoutModel(o.PayoutModel); msg != "" {
		return msg
	}
	if msg := validOfferPayout(o.Payout); msg != "" {
		return msg
	}
	if msg := validOfferPayoutCurrency(o.PayoutCurrency); msg != "" {
		return msg
	}
	if msg := validOfferValues(o.Values); msg != "" {
		return msg
	}
	if len(o.Notes) > config.MaxOfferNotes {
		return "notes is too long"
	}
	if msg := validOfferFallback(o); msg != "" {
		return msg
	}
	if msg := validOfferCap("conv_cap", o.ConvCap, o.ConvCapLimit); msg != "" {
		return msg
	}
	if msg := validOfferCap("conv_total", o.ConvTotal, o.ConvTotalLimit); msg != "" {
		return msg
	}
	if msg := validOfferCapTZ(o.CapTZ); msg != "" {
		return msg
	}
	if msg := validOfferTarget("conv_cap_offer_id", o.ID, o.ConvCapOfferID); msg != "" {
		return msg
	}
	return validOfferTarget("conv_total_offer_id", o.ID, o.ConvTotalOfferID)
}

// validOfferTarget checks an offer a limit or a stop hands the clicks to: empty, or a catalog id that
// is not the offer's own. Whether that offer exists is not checked here: the engine treats a missing
// one as nowhere to go, and a longer loop of offers ends at runtime.
// field is the JSON name for the message, id the offer's own id (empty for a new offer) and target
// the value. It returns an empty string when valid, otherwise a client error message.
func validOfferTarget(field, id, target string) string {
	if target == "" {
		return ""
	}
	if msg := validCatalogID(target); msg != "" {
		return field + ": " + msg
	}
	if id != "" && target == id {
		return field + " must not be the offer itself"
	}
	return ""
}

// validOfferFallback checks the spare offer of an offer: empty, or a catalog id that is not
// the offer's own. Whether the spare offer exists is not checked here: the engine treats a
// missing one as no spare offer, and a longer loop of spare offers ends at runtime.
// It returns an empty string when valid, otherwise a client error message.
func validOfferFallback(o config.Offer) string {
	return validOfferTarget("fallback_id", o.ID, o.FallbackID)
}

// validOfferValues checks the named values of an offer.
// Names are unique, at most config.MaxOfferValueName bytes, free of braces and
// control characters; values are at most config.MaxOfferValueText bytes.
// It returns an empty string when valid, otherwise a client error message.
func validOfferValues(values []config.OfferValue) string {
	if len(values) > config.MaxOfferValues {
		return "values: too many"
	}
	seen := map[string]bool{}
	for _, v := range values {
		name := strings.TrimSpace(v.Name)
		switch {
		case name == "":
			return "values: name must not be empty"
		case len(name) > config.MaxOfferValueName || strings.ContainsAny(name, "{}[]\r\n\t") || seen[name]:
			return "values: name is not valid or repeats: " + name
		case len(v.Value) > config.MaxOfferValueText:
			return "values: value is too long: " + name
		}
		seen[name] = true
	}
	return ""
}

// validOfferCap checks a conversion limit of an offer, the daily or the total one.
// A limit that is on needs a number from 1 to config.MaxConvCapLimit. A limit
// that is off may still carry its number.
// name is the JSON name of the switch (conv_cap or conv_total; its number is name_limit), on the
// switch and limit the number.
// It returns an empty string when valid, otherwise a client error message.
func validOfferCap(name string, on bool, limit int) string {
	if limit < 0 || limit > config.MaxConvCapLimit {
		return name + "_limit is out of range"
	}
	if on && limit == 0 {
		return name + "_limit must be above zero when " + name + " is on"
	}
	return ""
}

// validOfferCapTZ checks the time zone of the daily limit.
// zone is an IANA name. Empty is valid and means the UTC day. Local and an
// unknown name are rejected. It returns an empty string when valid, otherwise
// a client error message.
func validOfferCapTZ(zone string) string {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return ""
	}
	if strings.EqualFold(zone, "Local") {
		return "cap_tz is not a time zone"
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return "cap_tz is not a time zone"
	}
	return ""
}

// validOfferCountries checks each entry is a two-letter Latin country code or unknown, the country without a code.
// It returns an empty string when the list is valid, otherwise a client error message.
func validOfferCountries(codes []string) string {
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if !offerCountryCodeRe.MatchString(code) && !strings.EqualFold(code, ktimport.UnknownCountry) {
			return "countries: each code must be 2 letters"
		}
	}
	return ""
}

// validOfferPayoutModel accepts an empty value, cpa, or cpc.
// It returns an empty string when valid, otherwise a client error message.
func validOfferPayoutModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "cpa", "cpc":
		return ""
	default:
		return "payout_model must be cpa or cpc"
	}
}

// validOfferPayout rejects negative amounts and non-finite floats.
// It returns an empty string when valid, otherwise a client error message.
func validOfferPayout(amount float64) string {
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return "payout must not be negative"
	}
	return ""
}

// validOfferPayoutCurrency accepts an empty value or exactly three Latin letters.
// It returns an empty string when valid, otherwise a client error message.
func validOfferPayoutCurrency(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if !offerCurrencyCodeRe.MatchString(code) {
		return "payout_currency must be 3 letters"
	}
	return ""
}

func registerNetworks(s *Server, mux *http.ServeMux) {
	mountCRUD(s, mux, "/api/affiliate-networks", s.networkCatalog())
	s.handle(mux, "GET /api/affiliate-network-templates", http.HandlerFunc(s.handleNetworkTemplates))
	s.handle(mux, "POST /api/affiliate-network-templates", http.HandlerFunc(s.handleNetworkTemplateCreate))
	s.handle(mux, "PUT /api/affiliate-network-templates/{id}", http.HandlerFunc(s.handleNetworkTemplateUpdate))
	s.handle(mux, "DELETE /api/affiliate-network-templates/{id}", http.HandlerFunc(s.handleNetworkTemplateDelete))
}

// networkCatalog returns the handlers' state of the affiliate network catalog.
func (s *Server) networkCatalog() crudCatalog[config.AffiliateNetwork] {
	return crudCatalog[config.AffiliateNetwork]{s: s, store: s.cfg.Networks,
		idOf:  func(o config.AffiliateNetwork) string { return o.ID },
		setID: func(o *config.AffiliateNetwork, id string) { o.ID = id },
		valid: validNetwork,
		hooks: &catalogHooks[config.AffiliateNetwork]{
			seq: s.cfg.SeqNetworks, floor: s.networkFloor, prep: prepNetwork,
			res:      resNetworks,
			label:    func(n config.AffiliateNetwork) string { return n.Group },
			owner:    func(n config.AffiliateNetwork) string { return n.CreatedBy },
			setOwner: func(n *config.AffiliateNetwork, uid string) { n.CreatedBy = uid },
			boardID:  func(n config.AffiliateNetwork) string { return n.BoardID },
			setBoard: func(n *config.AffiliateNetwork, id string) { n.BoardID = id },
			userCheck: func(_ context.Context, v viewer, n config.AffiliateNetwork, old *config.AffiliateNetwork) string {
				return macroCheck(v, n, old)
			},
		},
	}
}

func validNetwork(n config.AffiliateNetwork) string {
	if msg := validCatalogID(n.ID); msg != "" {
		return msg
	}
	u := strings.TrimSpace(n.PostbackURL)
	if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return "postback_url must be http or https"
	}
	if n.OfferParam != "" {
		if msg := validCatalogID(n.OfferParam); msg != "" {
			return "offer_param: " + msg
		}
	}
	if msg := validOfferQuery(n.OfferQuery); msg != "" {
		return msg
	}
	return ""
}

func validOfferQuery(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 500 || strings.Contains(s, "://") || strings.ContainsAny(s, " \t\r\n") {
		return "offer_query must be a query string"
	}
	return ""
}

// validLanding checks a landing id, for a remote landing its URL, and a non-empty Entry.
// An Entry that config.SafeLandingRel refuses is a value the engine silently ignores in
// favour of index.php or index.html, so it is rejected instead of stored.
// A non-empty Entry must have one of the allowed extensions: .html, .htm, .php.
// o is the landing. It returns an empty string when the record is acceptable.
// It does not return an error; the string is the client message.
func validLanding(o config.Landing) string {
	if entry := strings.TrimSpace(o.Entry); entry != "" {
		if _, ok := config.SafeLandingRel(entry); !ok {
			return "entry must be a relative path inside the landing"
		}
		switch strings.ToLower(path.Ext(entry)) {
		case ".html", ".htm", ".php":
			// allowed
		default:
			return "entry must have extension .html, .htm or .php"
		}
	}
	return validDestination(o.ID, o.URL, o.Local)
}

func registerLandings(s *Server, mux *http.ServeMux) {
	mountCRUD(s, mux, "/api/landings", s.landingCatalog())
}

// landingCatalog returns the handlers' state of the landing catalog.
func (s *Server) landingCatalog() crudCatalog[config.Landing] {
	return crudCatalog[config.Landing]{s: s, store: s.cfg.Landings,
		idOf:  func(o config.Landing) string { return o.ID },
		setID: func(o *config.Landing, id string) { o.ID = id },
		valid: validLanding,
		hooks: &catalogHooks[config.Landing]{
			seq: s.cfg.SeqLandings, floor: s.landingFloor, prep: prepLanding, onDelete: s.removeLandingFiles,
			res:      resLandings,
			label:    func(l config.Landing) string { return l.Group },
			owner:    func(l config.Landing) string { return l.CreatedBy },
			setOwner: func(l *config.Landing, uid string) { l.CreatedBy = uid },
			boardID:  func(l config.Landing) string { return l.BoardID },
			setBoard: func(l *config.Landing, id string) { l.BoardID = id },
			userCheck: func(_ context.Context, v viewer, l config.Landing, old *config.Landing) string {
				return s.landingCheck(v, l, old)
			},
		},
	}
}

func registerSources(s *Server, mux *http.ServeMux) {
	mountCRUD(s, mux, "/api/traffic-sources", s.sourceCatalog())
}

// sourceCatalog returns the handlers' state of the traffic source catalog.
func (s *Server) sourceCatalog() crudCatalog[config.TrafficSource] {
	return crudCatalog[config.TrafficSource]{s: s, store: s.cfg.Sources,
		idOf:  func(o config.TrafficSource) string { return o.ID },
		setID: func(o *config.TrafficSource, id string) { o.ID = id },
		valid: validSource,
		hooks: &catalogHooks[config.TrafficSource]{
			seq: s.cfg.SeqSources, floor: s.sourceFloor, prep: prepSource,
			res:       resSources,
			label:     func(src config.TrafficSource) string { return src.Group },
			owner:     func(src config.TrafficSource) string { return src.CreatedBy },
			setOwner:  func(src *config.TrafficSource, uid string) { src.CreatedBy = uid },
			boardID:   func(src config.TrafficSource) string { return src.BoardID },
			setBoard:  func(src *config.TrafficSource, id string) { src.BoardID = id },
			userCheck: s.sourceCheck,
		},
	}
}

// registerCRUD mounts list, create, read, replace and delete for one catalog.
// A nil store answers 503. PUT of an existing id replaces that record in place.
// When hooks carries a counter, an empty id is filled under that counter's lock
// before validation. An explicit id is left as sent and the counter moves past it.
// hooks.mu serializes create, replace and delete when set. hooks.keep merges stored fields on PUT when set.
func registerCRUD[T any](s *Server, mux *http.ServeMux, base string, store *JSONStore[T], idOf func(T) string, setID func(*T, string), valid func(T) string, hooks *catalogHooks[T]) {
	mountCRUD(s, mux, base, crudCatalog[T]{s: s, store: store, idOf: idOf, setID: setID, valid: valid, hooks: hooks})
}

// mountCRUD registers the five catalog routes of c under base.
// s is the server and mux receives the routes. A nil store answers 503.
func mountCRUD[T any](s *Server, mux *http.ServeMux, base string, c crudCatalog[T]) {
	store := c.store
	wrap := func(pattern string, next http.HandlerFunc) {
		s.handle(mux, pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				writeErr(w, http.StatusServiceUnavailable, "store unavailable")
				return
			}
			next(w, r)
		}))
	}
	wrap("GET "+base, c.crudList)
	wrap("POST "+base, c.crudCreate)
	wrap("GET "+base+"/{id}", c.crudGet)
	wrap("PUT "+base+"/{id}", c.crudPut)
	wrap("DELETE "+base+"/{id}", c.crudDelete)
}

// crudList handles GET list with grant filtering and optional view shaping.
func (c crudCatalog[T]) crudList(w http.ResponseWriter, r *http.Request) {
	list, err := c.store.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	v := c.s.viewerFrom(r.Context())
	list = maskedCatalog(v, c.hooks, visibleCatalog(v, c.hooks, c.idOf, list))
	if list == nil {
		list = []T{}
	}
	if c.hooks != nil && c.hooks.view != nil {
		out, err := c.hooks.view(r.Context(), list)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// crudCreate handles POST with create grants, userLimit and owner assignment.
func (c crudCatalog[T]) crudCreate(w http.ResponseWriter, r *http.Request) {
	var item T
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&item); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	v := c.s.viewerFrom(r.Context())
	if c.hooks != nil && c.hooks.res != "" && !v.canCreate(c.hooks.res) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if !v.admin && c.hooks != nil && c.hooks.userLimit != nil {
		if msg := c.hooks.userLimit(&item, nil); msg != "" {
			writeErr(w, http.StatusForbidden, msg)
			return
		}
	}
	if !v.admin && c.hooks != nil {
		if c.hooks.userCheck != nil {
			if msg := c.hooks.userCheck(r.Context(), v, item, nil); msg != "" {
				writeErr(w, http.StatusBadRequest, msg)
				return
			}
		}
		if c.hooks.keepIn != nil {
			c.hooks.keepIn(v, &item, nil)
		}
	}
	stored, code, msg := c.insert(r.Context(), item, v.uid)
	if code != http.StatusCreated {
		writeErr(w, code, msg)
		return
	}
	c.respondOne(w, r, v, stored, http.StatusCreated)
}

// crudGet handles GET by id and hides invisible rows as 404.
func (c crudCatalog[T]) crudGet(w http.ResponseWriter, r *http.Request) {
	item, _, ok, code, msg := findCatalog(r, c.store, c.idOf)
	if !ok {
		writeErr(w, code, msg)
		return
	}
	v := c.s.viewerFrom(r.Context())
	if c.hooks != nil && c.hooks.boardID != nil && !v.seesBoard(c.hooks.boardID(item)) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if c.hooks != nil && c.hooks.res != "" && !v.canSee(c.hooks.res, catalogItemRef(c.idOf, c.hooks, item)) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	c.respondOne(w, r, v, item, http.StatusOK)
}

// respondOne writes one catalog item, using formatOne when the catalog defines it.
func (c crudCatalog[T]) respondOne(w http.ResponseWriter, r *http.Request, v viewer, item T, code int) {
	if c.hooks != nil && c.hooks.formatOne != nil {
		out, err := c.hooks.formatOne(r.Context(), c.s, item)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		writeJSON(w, code, out)
		return
	}
	writeJSON(w, code, maskedItem(v, c.hooks, item))
}

// crudPut handles PUT with write grants, userLimit and CreatedBy preservation.
func (c crudCatalog[T]) crudPut(w http.ResponseWriter, r *http.Request) {
	var item T
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&item); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	pathID := r.PathValue("id")
	c.setID(&item, pathID)
	unlock := c.lockWrite()
	defer unlock()
	list, err := c.store.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	idx := -1
	var old T
	for i := range list {
		if c.idOf(list[i]) == pathID {
			idx = i
			old = list[i]
			break
		}
	}
	exp := aiExpectFrom(r.Context())
	if idx < 0 {
		c.s.writeMissing(w, exp)
		return
	}
	v := c.s.viewerFrom(r.Context())
	itemRef := catalogItemRef(c.idOf, c.hooks, old)
	if c.hooks != nil && c.hooks.boardID != nil && !v.seesBoard(c.hooks.boardID(old)) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if c.hooks != nil && c.hooks.res != "" && !v.canSee(c.hooks.res, itemRef) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if c.hooks != nil && c.hooks.res != "" && !v.canWrite(c.hooks.res, itemRef) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := exp.check(old, true); err != nil {
		c.s.failWrite(w, err)
		return
	}
	if !v.admin && c.hooks != nil && c.hooks.userLimit != nil {
		if msg := c.hooks.userLimit(&item, &old); msg != "" {
			writeErr(w, http.StatusForbidden, msg)
			return
		}
	}
	if !v.admin && c.hooks != nil && c.hooks.userCheck != nil {
		if msg := c.hooks.userCheck(r.Context(), v, item, &old); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	applyCatalogCreatedBy(v, c.hooks, &item, old)
	if c.hooks != nil && c.hooks.prep != nil {
		c.hooks.prep(&item)
		c.setID(&item, pathID)
	}
	if c.hooks != nil && c.hooks.keep != nil {
		c.hooks.keep(&item, old)
	}
	if !v.admin && c.hooks != nil && c.hooks.keepIn != nil {
		c.hooks.keepIn(v, &item, &old)
	}
	if c.hooks != nil && c.hooks.setBoard != nil && c.hooks.boardID != nil {
		c.hooks.setBoard(&item, c.hooks.boardID(old))
	}
	if c.hooks != nil && c.hooks.guard != nil {
		if code, msg := c.hooks.guard(&item, old); code != 0 {
			writeErr(w, code, msg)
			return
		}
	}
	if msg := c.valid(item); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := exp.before(old); err != nil {
		c.s.failWrite(w, err)
		return
	}
	list[idx] = item
	if c.hooks != nil && c.hooks.tidy != nil {
		list = c.hooks.tidy(list, c.idOf(item))
	}
	if err := c.store.Save(r.Context(), list); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	exp.after(item)
	unlock()
	if c.hooks != nil && c.hooks.afterWrite != nil {
		c.hooks.afterWrite(item, &old)
	}
	c.respondOne(w, r, v, item, http.StatusOK)
}

// lockWrite serializes one read-modify-write of the catalog file.
// It takes hooks.mu when set, then the store lock that mergeCatalog also takes.
// It returns the release function; calling it more than once is safe.
func (c crudCatalog[T]) lockWrite() func() {
	if c.hooks != nil && c.hooks.mu != nil {
		c.hooks.mu.Lock()
	}
	unlockStore := c.store.lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockStore()
			if c.hooks != nil && c.hooks.mu != nil {
				c.hooks.mu.Unlock()
			}
		})
	}
}

// crudDelete handles DELETE with see/write grant checks.
func (c crudCatalog[T]) crudDelete(w http.ResponseWriter, r *http.Request) {
	if c.hooks != nil && c.hooks.preDelete != nil && c.hooks.preDelete(w, r) {
		return
	}
	unlock := c.lockWrite()
	defer unlock()
	list, err := c.store.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	id := r.PathValue("id")
	idx := -1
	var old T
	for i := range list {
		if c.idOf(list[i]) == id {
			idx = i
			old = list[i]
			break
		}
	}
	exp := aiExpectFrom(r.Context())
	if idx < 0 {
		c.s.writeMissing(w, exp)
		return
	}
	v := c.s.viewerFrom(r.Context())
	itemRef := catalogItemRef(c.idOf, c.hooks, old)
	if c.hooks != nil && c.hooks.boardID != nil && !v.seesBoard(c.hooks.boardID(old)) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if c.hooks != nil && c.hooks.res != "" && !v.canSee(c.hooks.res, itemRef) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if c.hooks != nil && c.hooks.res != "" && !v.canWrite(c.hooks.res, itemRef) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := exp.check(old, true); err != nil {
		c.s.failWrite(w, err)
		return
	}
	if c.hooks != nil && c.hooks.guardDelete != nil {
		if code, msg := c.hooks.guardDelete(old); code != 0 {
			writeErr(w, code, msg)
			return
		}
	}
	if err := exp.before(old); err != nil {
		c.s.failWrite(w, err)
		return
	}
	next := append(list[:idx], list[idx+1:]...)
	if err := c.store.Save(r.Context(), next); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	unlock()
	if c.hooks != nil && c.hooks.onDelete != nil {
		c.hooks.onDelete(id)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func findCatalog[T any](r *http.Request, store *JSONStore[T], idOf func(T) string) (T, []T, bool, int, string) {
	var zero T
	list, err := store.List(r.Context())
	if err != nil {
		return zero, nil, false, http.StatusInternalServerError, "read error"
	}
	id := r.PathValue("id")
	for _, item := range list {
		if idOf(item) == id {
			return item, list, true, 0, ""
		}
	}
	return zero, list, false, http.StatusNotFound, "not found"
}
