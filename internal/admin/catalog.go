package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"

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
	if !local && strings.TrimSpace(raw) == "" {
		return "url required"
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
	return ""
}

func registerOffers(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/offers", s.cfg.Offers,
		func(o config.Offer) string { return o.ID },
		func(o *config.Offer, id string) { o.ID = id },
		validOffer,
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
	return ""
}

func registerNetworks(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/affiliate-networks", s.cfg.Networks,
		func(o config.AffiliateNetwork) string { return o.ID },
		func(o *config.AffiliateNetwork, id string) { o.ID = id },
		validNetwork,
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

func registerLandings(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/landings", s.cfg.Landings,
		func(o config.Landing) string { return o.ID },
		func(o *config.Landing, id string) { o.ID = id },
		func(o config.Landing) string { return validDestination(o.ID, o.URL, o.Local) },
	)
}

func registerSources(s *Server, mux *http.ServeMux) {
	registerCRUD(s, mux, "/api/traffic-sources", s.cfg.Sources,
		func(o config.TrafficSource) string { return o.ID },
		func(o *config.TrafficSource, id string) { o.ID = id },
		validSource,
	)
}

// registerCRUD mounts list, create, read, replace and delete for one catalog.
// A nil store answers 503. PUT of an existing id replaces that record in place.
func registerCRUD[T any](s *Server, mux *http.ServeMux, base string, store *JSONStore[T], idOf func(T) string, setID func(*T, string), valid func(T) string) {
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
