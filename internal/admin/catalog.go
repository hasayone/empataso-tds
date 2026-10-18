package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egerkuzma/kuztds/internal/config"
)

// JSONStore reads and writes a JSON array. Save replaces the whole file.
type JSONStore[T any] struct {
	path string
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
	if len(p.Statuses) == 0 {
		return "status required"
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

func prepOffer(o *config.Offer) {
	o.Group = trimGroupLabel(o.Group)
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
	registerCRUD(s, mux, "/api/offers", s.cfg.Offers,
		func(o config.Offer) string { return o.ID },
		func(o *config.Offer, id string) { o.ID = id },
		validOffer,
		&catalogHooks[config.Offer]{
			seq: s.cfg.SeqOffers, floor: s.offerFloor, prep: prepOffer,
			res:      resOffers,
			label:    func(o config.Offer) string { return o.Group },
			owner:    func(o config.Offer) string { return o.CreatedBy },
			setOwner: func(o *config.Offer, uid string) { o.CreatedBy = uid },
		},
	)
}

func validOffer(o config.Offer) string {
	if msg := validDestination(o.ID, o.URL, o.Local); msg != "" {
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
	return ""
}

// validOfferCountries checks each entry is a two-letter Latin country code.
// It returns an empty string when the list is valid, otherwise a client error message.
func validOfferCountries(codes []string) string {
	for _, code := range codes {
		if !offerCountryCodeRe.MatchString(strings.TrimSpace(code)) {
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
	registerCRUD(s, mux, "/api/affiliate-networks", s.cfg.Networks,
		func(o config.AffiliateNetwork) string { return o.ID },
		func(o *config.AffiliateNetwork, id string) { o.ID = id },
		validNetwork,
		&catalogHooks[config.AffiliateNetwork]{
			seq: s.cfg.SeqNetworks, floor: s.networkFloor, prep: prepNetwork,
			res:      resNetworks,
			label:    func(n config.AffiliateNetwork) string { return n.Group },
			owner:    func(n config.AffiliateNetwork) string { return n.CreatedBy },
			setOwner: func(n *config.AffiliateNetwork, uid string) { n.CreatedBy = uid },
		},
	)
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

// validLanding checks a landing id and, for a remote landing, its URL.
// o is the landing. It returns an empty string when the record is acceptable.
// It does not return an error; the string is the client message.
func validLanding(o config.Landing) string {
	return validDestination(o.ID, o.URL, o.Local)
}

func registerLandings(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/landings", s.cfg.Landings,
		func(o config.Landing) string { return o.ID },
		func(o *config.Landing, id string) { o.ID = id },
		validLanding,
		&catalogHooks[config.Landing]{
			seq: s.cfg.SeqLandings, floor: s.landingFloor, prep: prepLanding, onDelete: s.removeLandingFiles,
			res:      resLandings,
			label:    func(l config.Landing) string { return l.Group },
			owner:    func(l config.Landing) string { return l.CreatedBy },
			setOwner: func(l *config.Landing, uid string) { l.CreatedBy = uid },
		},
	)
}

func registerSources(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/traffic-sources", s.cfg.Sources,
		func(o config.TrafficSource) string { return o.ID },
		func(o *config.TrafficSource, id string) { o.ID = id },
		validSource,
		&catalogHooks[config.TrafficSource]{
			seq: s.cfg.SeqSources, floor: s.sourceFloor, prep: prepSource,
			res:      resSources,
			label:    func(src config.TrafficSource) string { return src.Group },
			owner:    func(src config.TrafficSource) string { return src.CreatedBy },
			setOwner: func(src *config.TrafficSource, uid string) { src.CreatedBy = uid },
		},
	)
}

// catalogHooks assigns a number and tidies the list around one catalog.
// A nil hooks value keeps the old rule: an empty id is rejected by valid.
// onDelete runs after the record is removed and the file is saved.
// view, when set, shapes the GET list response.
type catalogHooks[T any] struct {
	seq      *idSeq
	floor    func() (int, error)
	prep     func(*T)
	tidy     func([]T, string) []T
	onDelete func(id string)
	// view converts the list for the GET response. Nil returns the list as stored.
	view func(ctx context.Context, list []T) (any, error)
	res  string
	// mu, when set, serializes List, modify and Save in create, replace and delete.
	mu *sync.Mutex
	// keep copies server-owned fields from the stored record into the replacement on PUT.
	// It runs after prep and before valid. Nil keeps the request body as sent.
	keep func(item *T, old T)
	// guard runs on PUT after keep and may adjust item. A non-zero status refuses the write
	// with that status and message. Nil allows every replacement.
	guard func(item *T, old T) (int, string)
	// guardDelete runs on DELETE before the record is removed. A non-zero status refuses
	// the delete with that status and message. Nil allows every delete.
	guardDelete func(old T) (int, string)
	// beforeInsert runs inside insert, under mu, after the id check and with the stored list.
	// A non-zero status refuses the new record with that status and message. Nil allows it.
	beforeInsert func(item T, list []T) (int, string)
	// preDelete runs first on DELETE, before any lock is taken. It returns true when it has
	// answered the request itself. Nil leaves every DELETE to the common path.
	preDelete func(w http.ResponseWriter, r *http.Request) bool
	// label and owner feed grant checks. setOwner writes CreatedBy on create.
	label     func(T) string
	owner     func(T) string
	setOwner  func(*T, string)
	userLimit func(*T, *T) string
}

// crudCatalog holds shared state for one JSON catalog's HTTP handlers.
type crudCatalog[T any] struct {
	s     *Server
	store *JSONStore[T]
	idOf  func(T) string
	setID func(*T, string)
	valid func(T) string
	hooks *catalogHooks[T]
}

// catalogItemRef builds a grant ref for item using hooks label and owner funcs.
func catalogItemRef[T any](idOf func(T) string, hooks *catalogHooks[T], item T) ref {
	r := ref{id: idOf(item)}
	if hooks != nil {
		if hooks.label != nil {
			r.label = hooks.label(item)
		}
		if hooks.owner != nil {
			r.owner = hooks.owner(item)
		}
	}
	return r
}

// visibleCatalog returns list entries the viewer may read for hooks.res.
func visibleCatalog[T any](v viewer, hooks *catalogHooks[T], idOf func(T) string, list []T) []T {
	if v.admin || hooks == nil || hooks.res == "" {
		return list
	}
	out := make([]T, 0, len(list))
	for _, item := range list {
		if v.canSee(hooks.res, catalogItemRef(idOf, hooks, item)) {
			out = append(out, item)
		}
	}
	return out
}

// applyCatalogCreatedBy sets CreatedBy on item from old unless an admin sent a non-empty value.
func applyCatalogCreatedBy[T any](v viewer, hooks *catalogHooks[T], item *T, old T) {
	if hooks == nil || hooks.setOwner == nil || hooks.owner == nil {
		return
	}
	if v.admin {
		if hooks.owner(*item) == "" {
			hooks.setOwner(item, hooks.owner(old))
		}
		return
	}
	hooks.setOwner(item, hooks.owner(old))
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
	list = visibleCatalog(v, c.hooks, c.idOf, list)
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
	stored, code, msg := c.insert(r.Context(), item, v.uid)
	if code != http.StatusCreated {
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

// crudGet handles GET by id and hides invisible rows as 404.
func (c crudCatalog[T]) crudGet(w http.ResponseWriter, r *http.Request) {
	item, _, ok, code, msg := findCatalog(r, c.store, c.idOf)
	if !ok {
		writeErr(w, code, msg)
		return
	}
	v := c.s.viewerFrom(r.Context())
	if c.hooks != nil && c.hooks.res != "" && !v.canSee(c.hooks.res, catalogItemRef(c.idOf, c.hooks, item)) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
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
	if c.hooks != nil && c.hooks.mu != nil {
		c.hooks.mu.Lock()
		defer c.hooks.mu.Unlock()
	}
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
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	v := c.s.viewerFrom(r.Context())
	itemRef := catalogItemRef(c.idOf, c.hooks, old)
	if c.hooks != nil && c.hooks.res != "" && !v.canSee(c.hooks.res, itemRef) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if c.hooks != nil && c.hooks.res != "" && !v.canWrite(c.hooks.res, itemRef) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if !v.admin && c.hooks != nil && c.hooks.userLimit != nil {
		if msg := c.hooks.userLimit(&item, &old); msg != "" {
			writeErr(w, http.StatusForbidden, msg)
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
	list[idx] = item
	if c.hooks != nil && c.hooks.tidy != nil {
		list = c.hooks.tidy(list, c.idOf(item))
	}
	if err := c.store.Save(r.Context(), list); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// crudDelete handles DELETE with see/write grant checks.
func (c crudCatalog[T]) crudDelete(w http.ResponseWriter, r *http.Request) {
	if c.hooks != nil && c.hooks.preDelete != nil && c.hooks.preDelete(w, r) {
		return
	}
	if c.hooks != nil && c.hooks.mu != nil {
		c.hooks.mu.Lock()
		defer c.hooks.mu.Unlock()
	}
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
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	v := c.s.viewerFrom(r.Context())
	itemRef := catalogItemRef(c.idOf, c.hooks, old)
	if c.hooks != nil && c.hooks.res != "" && !v.canSee(c.hooks.res, itemRef) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if c.hooks != nil && c.hooks.res != "" && !v.canWrite(c.hooks.res, itemRef) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if c.hooks != nil && c.hooks.guardDelete != nil {
		if code, msg := c.hooks.guardDelete(old); code != 0 {
			writeErr(w, code, msg)
			return
		}
	}
	next := append(list[:idx], list[idx+1:]...)
	if err := c.store.Save(r.Context(), next); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
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
