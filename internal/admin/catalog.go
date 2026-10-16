package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
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
		&catalogHooks[config.Offer]{seq: s.cfg.SeqOffers, floor: s.offerFloor, prep: prepOffer},
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
		&catalogHooks[config.AffiliateNetwork]{seq: s.cfg.SeqNetworks, floor: s.networkFloor, prep: prepNetwork},
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
			seq:      s.cfg.SeqLandings,
			floor:    s.landingFloor,
			prep:     prepLanding,
			onDelete: s.removeLandingFiles,
		},
	)
}

func registerSources(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/traffic-sources", s.cfg.Sources,
		func(o config.TrafficSource) string { return o.ID },
		func(o *config.TrafficSource, id string) { o.ID = id },
		validSource,
		&catalogHooks[config.TrafficSource]{seq: s.cfg.SeqSources, floor: s.sourceFloor, prep: prepSource},
	)
}

func registerDomains(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/domains", s.cfg.Domains,
		func(o config.Domain) string { return o.ID },
		func(o *config.Domain, id string) { o.ID = id },
		validDomain,
		&catalogHooks[config.Domain]{seq: s.cfg.SeqDomains, floor: s.domainFloor, prep: prepDomain, tidy: tidyDomains},
	)
}

func validDomain(d config.Domain) string {
	if msg := validCatalogID(d.ID); msg != "" {
		return msg
	}
	host := strings.TrimSpace(d.Host)
	if host == "" || strings.Contains(host, "://") || strings.ContainsAny(host, " /\\") {
		return "host required"
	}
	switch d.Role {
	case "admin", "links", "both":
	default:
		return "role must be admin, links or both"
	}
	return ""
}

func prepDomain(d *config.Domain) {
	d.Host = strings.ToLower(strings.TrimSpace(d.Host))
	if d.Role == "" {
		d.Role = "both"
	}
	d.Group = trimGroupLabel(d.Group)
}

func tidyDomains(list []config.Domain, id string) []config.Domain {
	primary := false
	for _, d := range list {
		if d.ID == id && d.Primary {
			primary = true
		}
	}
	if !primary {
		return list
	}
	for i := range list {
		if list[i].ID != id {
			list[i].Primary = false
		}
	}
	return list
}

// catalogHooks assigns a number and tidies the list around one catalog.
// A nil hooks value keeps the old rule: an empty id is rejected by valid.
// onDelete runs after the record is removed and the file is saved.
type catalogHooks[T any] struct {
	seq      *idSeq
	floor    func() (int, error)
	prep     func(*T)
	tidy     func([]T, string) []T
	onDelete func(id string)
}

// registerCRUD mounts list, create, read, replace and delete for one catalog.
// A nil store answers 503. PUT of an existing id replaces that record in place.
// When hooks carries a counter, an empty id is filled under that counter's lock
// before validation. An explicit id is left as sent and the counter moves past it.
func registerCRUD[T any](s *Server, mux *http.ServeMux, base string, store *JSONStore[T], idOf func(T) string, setID func(*T, string), valid func(T) string, hooks *catalogHooks[T]) {
	h := func(next http.HandlerFunc) http.Handler {
		return s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				writeErr(w, http.StatusServiceUnavailable, "store unavailable")
				return
			}
			next(w, r)
		}))
	}
	mux.Handle("GET "+base, h(func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.Handle("POST "+base, h(func(w http.ResponseWriter, r *http.Request) {
		var item T
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&item); err != nil {
			writeErr(w, http.StatusBadRequest, "bad json")
			return
		}
		if hooks != nil && hooks.seq != nil {
			hooks.seq.mu.Lock()
			defer hooks.seq.mu.Unlock()
		}
		if hooks != nil && hooks.prep != nil {
			hooks.prep(&item)
		}
		if idOf(item) == "" && hooks != nil && hooks.seq != nil {
			floor := 1
			if hooks.floor != nil {
				n, err := hooks.floor()
				if err != nil {
					writeErr(w, http.StatusInternalServerError, "read error")
					return
				}
				floor = n
			}
			n, err := hooks.seq.take(floor)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "save error")
				return
			}
			setID(&item, strconv.Itoa(n))
		} else if hooks != nil && hooks.seq != nil {
			hooks.seq.note(idOf(item))
		}
		if msg := valid(item); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		list, err := store.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		for _, cur := range list {
			if idOf(cur) == idOf(item) {
				writeErr(w, http.StatusConflict, "already exists")
				return
			}
		}
		list = append(list, item)
		if hooks != nil && hooks.tidy != nil {
			list = hooks.tidy(list, idOf(item))
		}
		if err := store.Save(r.Context(), list); err != nil {
			writeErr(w, http.StatusInternalServerError, "save error")
			return
		}
		writeJSON(w, http.StatusCreated, item)
	}))
	mux.Handle("GET "+base+"/{id}", h(func(w http.ResponseWriter, r *http.Request) {
		item, _, ok, code, msg := findCatalog(r, store, idOf)
		if !ok {
			writeErr(w, code, msg)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}))
	mux.Handle("PUT "+base+"/{id}", h(func(w http.ResponseWriter, r *http.Request) {
		var item T
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&item); err != nil {
			writeErr(w, http.StatusBadRequest, "bad json")
			return
		}
		setID(&item, r.PathValue("id"))
		if hooks != nil && hooks.prep != nil {
			hooks.prep(&item)
			setID(&item, r.PathValue("id"))
		}
		if msg := valid(item); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		list, err := store.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		idx := -1
		for i := range list {
			if idOf(list[i]) == idOf(item) {
				idx = i
				break
			}
		}
		if idx < 0 {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		list[idx] = item
		if hooks != nil && hooks.tidy != nil {
			list = hooks.tidy(list, idOf(item))
		}
		if err := store.Save(r.Context(), list); err != nil {
			writeErr(w, http.StatusInternalServerError, "save error")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}))
	mux.Handle("DELETE "+base+"/{id}", h(func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		id := r.PathValue("id")
		next := list[:0]
		found := false
		for _, cur := range list {
			if idOf(cur) == id {
				found = true
				continue
			}
			next = append(next, cur)
		}
		if !found {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if err := store.Save(r.Context(), next); err != nil {
			writeErr(w, http.StatusInternalServerError, "save error")
			return
		}
		if hooks != nil && hooks.onDelete != nil {
			hooks.onDelete(id)
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}))
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

func (s *Server) handleCampaignPostbacksGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	id := r.PathValue("id")
	for _, g := range groups {
		if g.ID == id {
			if g.Postbacks == nil {
				g.Postbacks = []config.OutboundPostback{}
			}
			writeJSON(w, http.StatusOK, g.Postbacks)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not found")
}

func (s *Server) handleCampaignPostbacksPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var list []config.OutboundPostback
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&list); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	for i := range list {
		if strings.TrimSpace(list[i].Method) == "" {
			list[i].Method = http.MethodGet
		}
		list[i].Method = strings.ToUpper(list[i].Method)
		if msg := validPostback(list[i]); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	id := r.PathValue("id")
	found := false
	for i := range groups {
		if groups[i].ID == id {
			groups[i].Postbacks = list
			found = true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.cfg.Groups.Save(r.Context(), groups); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
