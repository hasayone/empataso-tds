package admin

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// netTemplatesJSON is the catalog of affiliate network templates, one record per network name.
//
//go:embed net_templates.json
var netTemplatesJSON []byte

// Extraction levels of a template: which real settings the catalog holds for it.
const (
	// netTplFull means the template has both an offer query and a postback URL.
	netTplFull = "full"
	// netTplPartial means the template has one of them.
	netTplPartial = "partial"
	// netTplNone means only the name is known; nothing is drawn into the dialog besides it.
	netTplNone = "none"
)

// Sources of a template. A catalog record names its own source; a template added through the
// API is netTplSourceCustom.
const netTplSourceCustom = "custom"

// Limits of the fields a user may save into a template, in bytes (the name in characters).
const (
	netTplMaxName        = 80
	netTplMaxOfferQuery  = 2048
	netTplMaxPostback    = 4096
	netTplMaxInstruction = 16384
	netTplBodyLimit      = 64 << 10
)

// netTemplate is one template shown by the network dialog.
// ID is a slug of Name. Name is the network name. OfferQuery is the query string of the offer
// link and may hold only the macros of this tracker. PostbackURL is the address to paste into
// the network cabinet; the {origin} token stands for the public address of this tracker and the
// macros of the network stay as they are. Instruction is the text of the network cabinet guide,
// kept as the source wrote it (it may hold p, br, code and a tags). Conversions holds the status
// lists of the network by name, shown read-only: the tracker does not map them. Source names
// where the fields came from. Extracted is netTplFull, netTplPartial or netTplNone and is
// computed, not stored. Edited is true when a saved edit replaced a catalog field; Custom is
// true for a template that is not in the catalog.
type netTemplate struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	OfferQuery  string            `json:"offer_query"`
	PostbackURL string            `json:"postback_url"`
	Instruction string            `json:"instruction"`
	Conversions map[string]string `json:"conversions"`
	Source      string            `json:"source"`
	Extracted   string            `json:"extracted"`
	Edited      bool              `json:"edited"`
	Custom      bool              `json:"custom"`
}

// netTemplateEdit is what a user saved for one template. For a catalog template Name is the
// catalog name and only the non-empty fields replace the catalog values. For a template with
// Custom set the record is the whole template.
type netTemplateEdit struct {
	Name        string `json:"name"`
	OfferQuery  string `json:"offer_query,omitempty"`
	PostbackURL string `json:"postback_url,omitempty"`
	Instruction string `json:"instruction,omitempty"`
	Custom      bool   `json:"custom,omitempty"`
}

// netTemplateEditFile is the layout of the file with the saved edits.
type netTemplateEditFile struct {
	Items []netTemplateEdit `json:"items"`
}

// netTemplateBody is the body of POST and PUT on the templates.
type netTemplateBody struct {
	Name        string `json:"name"`
	OfferQuery  string `json:"offer_query"`
	PostbackURL string `json:"postback_url"`
	Instruction string `json:"instruction"`
}

// netTemplateCatalog returns the embedded catalog parsed once, in file order, with the computed
// fields set. It returns an empty list when the embedded file is not valid JSON; a test guards
// the file. Callers must not change the records.
var netTemplateCatalog = sync.OnceValue(func() []netTemplate {
	var list []netTemplate
	if json.Unmarshal(netTemplatesJSON, &list) != nil {
		return []netTemplate{}
	}
	return finishNetTemplates(list)
})

// finishNetTemplates sets ID, Extracted and a non-nil Conversions on every record of list and
// returns list.
func finishNetTemplates(list []netTemplate) []netTemplate {
	for i := range list {
		t := &list[i]
		t.ID = netTemplateSlug(t.Name)
		t.Extracted = netTemplateLevel(*t)
		if t.Conversions == nil {
			t.Conversions = map[string]string{}
		}
	}
	return list
}

// netTemplateLevel reports how much of t the catalog knows: netTplFull when it has an offer
// query and a postback URL, netTplPartial when it has one of them, netTplNone otherwise.
func netTemplateLevel(t netTemplate) string {
	switch has := strings.TrimSpace(t.OfferQuery) != ""; {
	case has && strings.TrimSpace(t.PostbackURL) != "":
		return netTplFull
	case has || strings.TrimSpace(t.PostbackURL) != "":
		return netTplPartial
	}
	return netTplNone
}

// netTemplateSlug turns a network name into a stable id: lower case letters and digits, any
// other run of characters becomes one dash, dashes at the ends are cut.
func netTemplateSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// mergeNetTemplates lays the saved edits over base and returns the result; base is not changed.
// An edit of a catalog template replaces only its non-empty fields, so an empty field never
// blanks the catalog value, and the name never changes. An edit of a custom template is appended
// as a template of its own unless its id is already taken. An edit of a name the catalog no
// longer has is dropped.
func mergeNetTemplates(base []netTemplate, edits []netTemplateEdit) []netTemplate {
	out := make([]netTemplate, len(base), len(base)+len(edits))
	copy(out, base)
	at := make(map[string]int, len(out))
	for i, t := range out {
		at[t.ID] = i
	}
	for _, e := range edits {
		id := netTemplateSlug(e.Name)
		i, known := at[id]
		switch {
		case known && !e.Custom:
			t := &out[i]
			t.OfferQuery = pick(e.OfferQuery, t.OfferQuery)
			t.PostbackURL = pick(e.PostbackURL, t.PostbackURL)
			t.Instruction = pick(e.Instruction, t.Instruction)
			t.Edited = e.OfferQuery != "" || e.PostbackURL != "" || e.Instruction != ""
		case !known && e.Custom && id != "":
			at[id] = len(out)
			out = append(out, netTemplate{
				Name: e.Name, OfferQuery: e.OfferQuery, PostbackURL: e.PostbackURL,
				Instruction: e.Instruction, Source: netTplSourceCustom, Custom: true,
			})
		}
	}
	return finishNetTemplates(out)
}

// pick returns saved when it is not empty and fallback otherwise.
func pick(saved, fallback string) string {
	if saved != "" {
		return saved
	}
	return fallback
}

// loadNetTemplateEdits reads the saved edits from path. A missing file or an empty path is no
// edits. It returns an error when the file cannot be read or is not valid JSON.
func loadNetTemplateEdits(path string) ([]netTemplateEdit, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f netTemplateEditFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f.Items, nil
}

// netTemplates returns the catalog with the saved edits applied. A damaged edits file is logged
// and the plain catalog is served, so the dialog keeps working.
func (s *Server) netTemplates() []netTemplate {
	edits, err := loadNetTemplateEdits(s.cfg.NetTemplatesFile)
	if err != nil {
		s.logWarn("net templates file unreadable", "err", err.Error())
	}
	return mergeNetTemplates(netTemplateCatalog(), edits)
}

// handleNetworkTemplates answers GET /api/affiliate-network-templates with the templates:
// the embedded catalog with the saved edits and the custom templates. It holds no secrets and is
// the same for every caller; the route needs a session with the networks section.
func (s *Server) handleNetworkTemplates(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.netTemplates())
}

// netTemplatePostbackOK reports whether a template postback address goes to this tracker: it is
// {origin}/postback, alone or followed by a query, and holds no other address. A foreign host and
// a path token cannot be written into a template.
func netTemplatePostbackOK(u string) bool {
	rest, ok := strings.CutPrefix(u, "{origin}/postback")
	return ok && (rest == "" || rest[0] == '?') && !strings.Contains(u, "://")
}

// readNetTemplateBody decodes and checks the body of POST and PUT: the strings are trimmed and
// must fit the limits. It returns false after writing the 400 answer to w.
func readNetTemplateBody(w http.ResponseWriter, r *http.Request) (netTemplateBody, bool) {
	var b netTemplateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, netTplBodyLimit)).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return b, false
	}
	b.Name = strings.TrimSpace(b.Name)
	b.OfferQuery = strings.TrimSpace(b.OfferQuery)
	b.PostbackURL = strings.TrimSpace(b.PostbackURL)
	b.Instruction = strings.TrimSpace(b.Instruction)
	switch {
	case utf8.RuneCountInString(b.Name) > netTplMaxName:
		writeErr(w, http.StatusBadRequest, "name is too long")
	case len(b.OfferQuery) > netTplMaxOfferQuery:
		writeErr(w, http.StatusBadRequest, "offer_query is too long")
	case len(b.PostbackURL) > netTplMaxPostback:
		writeErr(w, http.StatusBadRequest, "postback_url is too long")
	case len(b.Instruction) > netTplMaxInstruction:
		writeErr(w, http.StatusBadRequest, "instruction is too long")
	case b.PostbackURL != "" && !netTemplatePostbackOK(b.PostbackURL):
		writeErr(w, http.StatusBadRequest, "postback_url must be {origin}/postback")
	default:
		return b, true
	}
	return b, false
}

// mutateNetTemplates runs change over the saved edits under one lock and writes the result with
// mode 0600. change returns the new edits, or a non-empty message and the status to answer with
// and nothing is written. It returns the edits written and true, or false after writing the error
// answer to w: 503 when no file is configured, 500 "save error" when the file is damaged or
// cannot be written.
func (s *Server) mutateNetTemplates(w http.ResponseWriter, change func([]netTemplateEdit) ([]netTemplateEdit, int, string)) ([]netTemplateEdit, bool) {
	if s.cfg.NetTemplatesFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "template edits unavailable")
		return nil, false
	}
	s.netTplMu.Lock()
	defer s.netTplMu.Unlock()
	edits, err := loadNetTemplateEdits(s.cfg.NetTemplatesFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return nil, false
	}
	next, status, msg := change(edits)
	if msg != "" {
		writeErr(w, status, msg)
		return nil, false
	}
	raw, err := json.Marshal(netTemplateEditFile{Items: next})
	if err == nil {
		err = writeFileAtomic(s.cfg.NetTemplatesFile, append(raw, '\n'), 0o600)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return nil, false
	}
	return next, true
}

// findNetTemplate returns the template with the id after the edits are applied.
func findNetTemplate(edits []netTemplateEdit, id string) (netTemplate, bool) {
	for _, t := range mergeNetTemplates(netTemplateCatalog(), edits) {
		if t.ID == id {
			return t, true
		}
	}
	return netTemplate{}, false
}

// withoutNetTemplateEdit returns edits without the record of the template id.
func withoutNetTemplateEdit(edits []netTemplateEdit, id string) []netTemplateEdit {
	out := make([]netTemplateEdit, 0, len(edits))
	for _, e := range edits {
		if netTemplateSlug(e.Name) != id {
			out = append(out, e)
		}
	}
	return out
}

// handleNetworkTemplateCreate answers POST /api/affiliate-network-templates: it adds a template
// with a new name. The name is required and must make a non-empty id that no template has;
// the catalog names and the custom names are one set. Success is 201 with the template. Answers
// 400 for a bad body, 409 when the id is taken, 503 and 500 as mutateNetTemplates.
func (s *Server) handleNetworkTemplateCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := readNetTemplateBody(w, r)
	if !ok {
		return
	}
	id := netTemplateSlug(body.Name)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	var created netTemplate
	_, ok = s.mutateNetTemplates(w, func(edits []netTemplateEdit) ([]netTemplateEdit, int, string) {
		if _, taken := findNetTemplate(edits, id); taken {
			return nil, http.StatusConflict, "template exists"
		}
		next := append(edits, netTemplateEdit{
			Name: body.Name, OfferQuery: body.OfferQuery, PostbackURL: body.PostbackURL,
			Instruction: body.Instruction, Custom: true,
		})
		created, _ = findNetTemplate(next, id)
		return next, 0, ""
	})
	if ok {
		s.logInfo("net template added", "id", id)
		writeJSON(w, http.StatusCreated, created)
	}
}

// handleNetworkTemplateUpdate answers PUT /api/affiliate-network-templates/{id}: it saves the
// fields of the body as the edit of the template. For a catalog template a field that is empty
// or equal to the catalog value is not stored, so it follows the catalog; an empty body field
// drops an earlier edit of that field. For a custom template the fields are stored as sent. The
// name cannot change: a name in the body must make the same id. Success is 200 with the template.
// Answers 400 for a bad body or a different name, 404 for an unknown id, 503 and 500 as
// mutateNetTemplates.
func (s *Server) handleNetworkTemplateUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, ok := readNetTemplateBody(w, r)
	if !ok {
		return
	}
	if body.Name != "" && netTemplateSlug(body.Name) != id {
		writeErr(w, http.StatusBadRequest, "name cannot change")
		return
	}
	var saved netTemplate
	_, ok = s.mutateNetTemplates(w, func(edits []netTemplateEdit) ([]netTemplateEdit, int, string) {
		cur, found := findNetTemplate(edits, id)
		if !found {
			return nil, http.StatusNotFound, "template not found"
		}
		next := withoutNetTemplateEdit(edits, id)
		edit := netTemplateEdit{Name: cur.Name, Custom: cur.Custom}
		if cur.Custom {
			edit.OfferQuery, edit.PostbackURL, edit.Instruction = body.OfferQuery, body.PostbackURL, body.Instruction
		} else {
			base := catalogNetTemplate(id)
			edit.OfferQuery = changedOnly(body.OfferQuery, base.OfferQuery)
			edit.PostbackURL = changedOnly(body.PostbackURL, base.PostbackURL)
			edit.Instruction = changedOnly(body.Instruction, base.Instruction)
		}
		if edit.Custom || edit.OfferQuery != "" || edit.PostbackURL != "" || edit.Instruction != "" {
			next = append(next, edit)
		}
		saved, _ = findNetTemplate(next, id)
		return next, 0, ""
	})
	if ok {
		s.logInfo("net template saved", "id", id)
		writeJSON(w, http.StatusOK, saved)
	}
}

// changedOnly returns value when it is not empty and differs from the catalog value, else "".
func changedOnly(value, catalog string) string {
	if value == catalog {
		return ""
	}
	return value
}

// catalogNetTemplate returns the embedded template with the id, or the zero value.
func catalogNetTemplate(id string) netTemplate {
	for _, t := range netTemplateCatalog() {
		if t.ID == id {
			return t
		}
	}
	return netTemplate{}
}

// handleNetworkTemplateDelete answers DELETE /api/affiliate-network-templates/{id}: for a catalog
// template it drops the saved edit, so the template returns to the catalog values; for a custom
// template it removes the template. Success is 204. Answers 404 for an unknown id, 503 and 500 as
// mutateNetTemplates.
func (s *Server) handleNetworkTemplateDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, ok := s.mutateNetTemplates(w, func(edits []netTemplateEdit) ([]netTemplateEdit, int, string) {
		if _, found := findNetTemplate(edits, id); !found {
			return nil, http.StatusNotFound, "template not found"
		}
		return withoutNetTemplateEdit(edits, id), 0, ""
	})
	if ok {
		s.logInfo("net template reset", "id", id)
		w.WriteHeader(http.StatusNoContent)
	}
}
